//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func prepareWorkspace(opts Options, req vmkit.Request) error {
	if err := writeConfig(opts, req); err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return err
	}
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o700); err != nil {
		return err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := serialLog.Close(); err != nil {
		return err
	}
	return writeProcessState(opts, req, vmkit.StatePrepared, 0, "")
}

func startProcess(ctx context.Context, opts Options, req vmkit.Request, detached bool) (vmkit.Response, error) {
	if vmkit.ContainmentMarked(opts.StateDir, opts.Name) {
		err := fmt.Errorf("firecracker workspace %s has a durable containment marker; start denied", opts.Name)
		return failedResponse(req, err.Error()), err
	}
	// Serialize concurrent starts of this workspace so two supervisors cannot both
	// pass the not-running checks and boot two firecrackers against one rootfs
	// (concurrent ext4 writers → guest filesystem corruption). Only the outer
	// (host) start takes the lock; the user-network re-exec runs inside the
	// namespace and must NOT re-acquire it, or it would deadlock against the outer
	// holder. Failure to acquire this safety boundary is fatal: booting without it
	// could put two writers on the same ext4 images.
	if !insideUserNetworkNamespace() {
		wsDir := filepath.Join(opts.StateDir, opts.Name)
		if err := os.MkdirAll(wsDir, 0o755); err != nil {
			return failedResponse(req, err.Error()), err
		}
		release, err := fsutil.Lock(filepath.Join(wsDir, ".start.lock"))
		if err != nil {
			return failedResponse(req, err.Error()), err
		}
		defer func() { _ = release() }()
		held, err := workspace.RuntimeLeaseHeld(opts.StateDir, opts.Name)
		if err != nil {
			return failedResponse(req, err.Error()), err
		}
		if held {
			err := fmt.Errorf("workspace %s runtime lease is already held; another VM may be running outside this PID namespace", opts.Name)
			return failedResponse(req, err.Error()), err
		}
	}
	// Snapshot restore/fork rolls the rootfs back to the snapshot copy and
	// validates kernel/network compatibility once on the host, before any
	// user-network namespace re-exec carries the request inward.
	if req.Tag != "" && !insideUserNetworkNamespace() {
		if err := prepareSnapshotRestore(opts, req); err != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
	}
	if networkMode(req.Config) == "user" && !insideUserNetworkNamespace() {
		return startUserNetworkProcess(ctx, opts, req, detached)
	}
	runtimeLease, err := acquireRuntimeLease(opts)
	if err != nil {
		return failedResponse(req, err.Error()), err
	}
	defer func() { _ = runtimeLease.Close() }()
	path := opts.FirecrackerPath
	if path == "" {
		resolved, err := opts.ResolveFirecracker()
		if err != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		path = resolved
	}
	// On a snapshot restore/fork (req.Tag != "") the egress mediator must be
	// re-armed with the SAME per-workspace CA the guest's baked trust store was
	// built against, NOT a freshly minted one. Read the recorded CA fingerprint
	// from the snapshot manifest so prepareNetworkForStart can reuse-and-verify
	// the persisted CA instead of re-minting. Fail closed if the manifest is
	// unreadable during a restore.
	restore := req.Tag != ""
	expectedCASHA := ""
	var restoreManifest *vmkit.SnapshotManifest
	if restore {
		manifest, merr := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag))
		if merr != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, merr.Error())
			return failedResponse(req, merr.Error()), merr
		}
		restoreManifest = &manifest
		expectedCASHA = manifest.EgressCASHA256
		// Re-apply the persisted bounded-operations caps (ASK tenet 8) so a restored
		// workspace keeps the SAME bounds it was snapshotted under, just as the CA is
		// reused. Threaded onto req.Config here so provisionEgressMediation hands them
		// to the mediator flags via egressCapsFromConfig. Idempotent: if the restore
		// request already carries caps, the manifest reproduces the same values.
		applyManifestEgressCaps(req.Config, manifest)
	}
	networkDevices, firewallRules, runtimeNetwork, egressMediatorPID, err := prepareNetworkForStart(opts, req.Config, restore, expectedCASHA)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	// prepareNetworkForStart may have started the egress mediator — a host-side
	// companion already bound to the tap gateway. Every failure path below cleans
	// up the transient firewall rules and network devices, but the mediator is a
	// separate process and must be reaped too, or it is orphaned with the
	// workspace gone. Guard it with a deferred reaper that is disarmed only on the
	// detached-success path (where the mediator is intentionally left running and
	// recorded in runtime.json for stop/halt to reap later). terminateAuxProcess
	// is idempotent, so paths that also reap it explicitly stay correct.
	egressMediatorRunning := egressMediatorPID != 0
	defer func() {
		if egressMediatorRunning {
			terminateAuxProcess(egressMediatorPID)
		}
	}()
	runtimeReq := requestWithRuntimeNetwork(req, runtimeNetwork)
	// Move the shell/exec host binds off any unbindable port (e.g. a WSL2/Windows
	// reserved range) before the VM config (boot args) and runtime state are
	// written, so the guest, the forwarder, and readiness/connect all agree on
	// the final ports.
	ensureBindableManagementPorts(runtimeReq.Config)
	if err := writeConfig(opts, runtimeReq); err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	var vsockListeners *vsockListenerSet
	if !detached && !insideUserNetworkNamespace() {
		vsockListeners, err = startVsockListeners(opts, runtimeReq.Config)
		if err != nil {
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		defer vsockListeners.Close()
	}
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o700); err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		return vmkit.Response{}, err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		return vmkit.Response{}, err
	}
	var serialInput *os.File
	if req.Config.SerialInput {
		input, inputErr := openSerialInputFIFO(opts)
		if inputErr != nil {
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, inputErr.Error())
			return failedResponse(req, inputErr.Error()), inputErr
		}
		serialInput = input
	}
	// Do NOT blindly delete an existing API socket. If a firecracker is still
	// alive for this workspace, that socket is live and removing it to boot again
	// would run two firecrackers against the same rootfs (concurrent ext4 writers
	// → corruption). Re-check liveness here — under the start lock, and at the
	// point the second start would clobber the socket — and refuse instead. Do
	// not overwrite the live workspace's runtime state with Failed.
	if existing, rerr := readRuntimeState(opts); rerr == nil && firecrackerAlive(existing, opts) {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		_ = serialLog.Close()
		if serialInput != nil {
			_ = serialInput.Close()
		}
		err := fmt.Errorf("firecracker workspace %s is already running (pid %d)", opts.Name, existing.PID)
		return failedResponse(req, err.Error()), err
	}
	// Firecracker refuses to start if the API socket already exists. Any socket
	// still present here belongs to a VM that is NOT alive (checked above), so it
	// is stale and safe to remove.
	if err := os.Remove(apiSocketPath(opts)); err != nil && !os.IsNotExist(err) {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		_ = serialLog.Close()
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	// Boot from the config file and additionally expose the API socket so
	// pause/resume and snapshot can control the running VM. Only --no-api would
	// disable the API. A snapshot restore/fork instead launches with just the
	// API socket and loads the snapshot (which carries its own machine config)
	// over it.
	loadMode := req.Tag != ""
	launchArgs := []string{"--api-sock", apiSocketPath(opts)}
	if !loadMode {
		launchArgs = append(launchArgs, "--config-file", configPath(opts))
	}
	cmd, err := firecrackerLaunchCommand(ctx, opts, req, path, launchArgs, loadMode)
	if err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	cmd.Stdout = serialLog
	cmd.Stderr = serialLog
	if serialInput != nil {
		cmd.Stdin = serialInput
	}
	cmd.SysProcAttr = firecrackerSysProcAttr(detached)
	if err := cmd.Start(); err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	if loadMode {
		if err := restoreFromSnapshot(ctx, opts, req.Tag, cmd.Process.Pid, snapshotNetworkOverrides(opts, req.Config)); err != nil {
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		// A snapshot that recorded materialized guest secrets resumes with
		// zeroed /run/secrets (purged before memory capture). Rehydrate before
		// the restored/forked workspace is considered running. Fail closed: do
		// not leave a secret-bearing restore booted with silently missing
		// materialized secrets.
		if restoreManifest != nil && restoreManifest.SecretsMaterialized {
			if err := rehydrateGuestSecrets(opts, runtimeReq.Config.SecretsControlPort); err != nil {
				wrapped := fmt.Errorf("rehydrate secrets after snapshot restore: %w", err)
				_ = cmd.Process.Kill()
				cleanupTransientFirewallRules(firewallRules)
				cleanupTransientNetworkDevices(networkDevices)
				if serialInput != nil {
					_ = serialInput.Close()
				}
				_ = serialLog.Close()
				_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
				return failedResponse(req, wrapped.Error()), wrapped
			}
		}
		// PUT /snapshot/load can succeed while the guest panics moments into
		// resume (see waitForRestoreLiveness) - hold the state at not-running
		// until the guest proves it came back, instead of reporting running
		// and leaving the caller to discover the crash via an unrelated exec
		// or clock-sync failure later.
		if err := waitForRestoreLiveness(ctx, cmd, serialLogPath(opts), vsockSocketPath(opts), guestExecPort(*runtimeReq.Config)); err != nil {
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			errorText := fmt.Sprintf("%s; serial log: %s", err.Error(), serialLogPath(opts))
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, errorText)
			return failedResponse(req, errorText), fmt.Errorf("%s", errorText)
		}
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, 0, 0, egressMediatorPID, networkDevices, firewallRules, ""); err != nil {
		_ = cmd.Process.Kill()
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		return vmkit.Response{}, err
	}
	portForwardPID := 0
	vsockListenerPID := 0
	if detached && hasVsockListeners(req.Config) {
		pid, err := startVsockListenerProcess(opts)
		if err != nil {
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		// Same contract as the user-mode path: the listener process is
		// detached and long-lived, so a startup failure (bad broker secret,
		// unreadable CA, unbound socket) cannot surface as its exit code —
		// wait for the ready marker and fail the workspace loudly if the
		// process dies first, instead of leaving it "running" with dead
		// egress.
		if err := waitForVsockListenersReady(opts, pid, vsockListenerReadyTimeout); err != nil {
			_ = signalProcessGroup(pid, syscall.SIGTERM)
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		vsockListenerPID = pid
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, vsockListenerPID, egressMediatorPID, networkDevices, firewallRules, ""); err != nil {
			_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			return vmkit.Response{}, err
		}
	}
	if detached && needsPortForwarder(req.Config) {
		pid, err := startReadyPortForwarderProcessWithManagementPortRetry(ctx, opts, runtimeReq.Config, func() error {
			return writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, 0, vsockListenerPID, egressMediatorPID, networkDevices, firewallRules, "")
		})
		if err != nil {
			if vsockListenerPID != 0 {
				_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
			}
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		portForwardPID = pid
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, vsockListenerPID, egressMediatorPID, networkDevices, firewallRules, ""); err != nil {
			_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
			if vsockListenerPID != 0 {
				_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
			}
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			return vmkit.Response{}, err
		}
	}
	if detached {
		// Fresh boots have no positive guest-liveness signal at this point, so
		// retain the short observation window that catches immediate process
		// exits. Snapshot restores already completed waitForRestoreLiveness
		// above with a successful guest exec round trip; sleeping another 500ms
		// adds no evidence and directly delays every restore and fork.
		if err := detachedStartExitError(cmd, detachedExitObservationWindow(loadMode)); err != nil {
			if portForwardPID != 0 {
				_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
			}
			if vsockListenerPID != 0 {
				terminateAuxProcess(vsockListenerPID)
			}
			if egressMediatorPID != 0 {
				terminateAuxProcess(egressMediatorPID)
			}
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			// An intentional stop can SIGTERM firecracker inside this detached
			// startup window — that's a clean stop, not a launch failure. The stop
			// records intent (and writes its terminal state) before signaling, so if
			// intent is on disk (or a terminal Stopped/Halted is already recorded),
			// defer to the stop instead of overwriting it with Failed.
			if fresh, ferr := readRuntimeState(opts); ferr == nil && (fresh.Stopping || fresh.Event.State == vmkit.StateStopped || fresh.Event.State == vmkit.StateHalted) {
				return eventResponse(req, vmkit.StateStopped, ""), nil
			}
			errorText := fmt.Sprintf("%s; serial log: %s", err.Error(), serialLogPath(opts))
			_ = writeProcessState(opts, runtimeReq, vmkit.StateFailed, 0, errorText)
			return failedResponse(req, errorText), fmt.Errorf("%s", errorText)
		}
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		_ = cmd.Process.Release()
		// Detached start succeeded: the mediator stays up as a recorded companion
		// (reaped later by stop/halt/quarantine), so disarm the deferred reaper.
		egressMediatorRunning = false
		// Leave an event-driven per-VM reaper: when firecracker exits it reconciles
		// the workspace to its terminal state (and reaps companions + transient
		// network) without waiting for a status read or gc sweep. The deadman also
		// inherits the runtime lease, so failing to start it is fatal: returning a
		// live but unleased VM would reopen the duplicate-writer corruption bug.
		if _, err := startDeadmanProcessWithLease(opts, runtimeLease); err != nil {
			errorText := fmt.Sprintf("start workspace reaper %s: %v", opts.Name, err)
			_ = signalProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
			if portForwardPID != 0 {
				_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
			}
			if vsockListenerPID != 0 {
				terminateAuxProcess(vsockListenerPID)
			}
			if egressMediatorPID != 0 {
				terminateAuxProcess(egressMediatorPID)
			}
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			_ = writeProcessState(opts, runtimeReq, vmkit.StateFailed, 0, errorText)
			return failedResponse(req, errorText), fmt.Errorf("%s", errorText)
		}
		return eventResponse(req, vmkit.StateRunning, ""), nil
	}
	waitErr := waitForeground(ctx, cmd, serialLogPath(opts), opts.Timeout)
	// In detached user-network mode the outer start records companion PIDs
	// (port forwarder, vsock listener) in runtime.json after boot, and this
	// in-namespace foreground supervisor is the only process that observes the
	// VM exit. Reap those host-side companions before the final state write
	// below discards the recorded PIDs.
	terminateRecordedCompanions(opts)
	if egressMediatorPID != 0 {
		terminateAuxProcess(egressMediatorPID)
	}
	inputCloseErr := error(nil)
	if serialInput != nil {
		inputCloseErr = serialInput.Close()
	}
	closeErr := serialLog.Close()
	state := vmkit.StateStopped
	errorText := ""
	if waitErr != nil {
		state = vmkit.StateFailed
		errorText = waitErr.Error()
	}
	cleanupTransientFirewallRules(firewallRules)
	cleanupTransientNetworkDevices(networkDevices)
	if closeErr != nil && errorText == "" {
		state = vmkit.StateFailed
		errorText = closeErr.Error()
	}
	if inputCloseErr != nil && errorText == "" {
		state = vmkit.StateFailed
		errorText = inputCloseErr.Error()
	}
	if err := writeProcessState(opts, runtimeReq, state, 0, errorText); err != nil && waitErr == nil && closeErr == nil && inputCloseErr == nil {
		return vmkit.Response{}, err
	}
	if errorText != "" {
		return failedResponse(req, errorText), fmt.Errorf("%s", errorText)
	}
	return eventResponse(req, vmkit.StateStopped, ""), nil
}

func openSerialInputFIFO(opts Options) (*os.File, error) {
	path := serialInputPath(opts)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create firecracker serial input fifo: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return nil, fmt.Errorf("firecracker serial input path is not a fifo: %s", path)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open firecracker serial input fifo: %w", err)
	}
	return file, nil
}

func stopWorkspace(ctx context.Context, opts Options, req vmkit.Request, signal syscall.Signal, finalState vmkit.VMState) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	// Record stop intent before signaling firecracker, but only for clean stops
	// (Stopped/Halted) — not for the failure writes below. This closes the race
	// window where a concurrent supervise-loop inspect could read the killed
	// command's non-zero result.json and mis-classify the intentional stop as
	// Failed: once Stopping is on disk, inspect/gc resolve a dead firecracker to
	// Stopped. Best-effort: a write failure here must not block the stop itself.
	cleanStop := signal != syscall.SIGKILL && (finalState == vmkit.StateStopped || finalState == vmkit.StateHalted)
	// Record stop intent whenever we are about to kill a LIVE firecracker for an
	// intentional clean stop — keyed on actual liveness, not the event label.
	// Under confinement / slow boots a just-restarted VM can be alive but still
	// transitional (not yet labeled Running) when the next stop fires; gating on
	// the label alone misses the intent there, and the classifier then reads the
	// killed command's non-zero result.json and mis-labels the stop as Failed. A
	// genuine crash (firecracker already dead) leaves Stopping unset, so it still
	// classifies via guestHaltedState.
	fcAlive := firecrackerAlive(state, opts)
	debugSupLog(opts, fmt.Sprintf("STOP finalState=%s eventState=%s stoppingBefore=%v cleanStop=%v fcAlive=%v pid=%d", finalState, state.Event.State, state.Stopping, cleanStop, fcAlive, state.PID))
	if cleanStop && !state.Stopping && fcAlive {
		if err := persistStopIntent(opts, state); err == nil {
			state.Stopping = true
		}
	}

	if state.PID == 0 {
		if state.PortForwardPID != 0 {
			_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
		}
		if state.VsockListenerPID != 0 {
			terminateAuxProcess(state.VsockListenerPID)
		}
		if state.EgressMediatorPID != 0 {
			terminateAuxProcess(state.EgressMediatorPID)
		}
		cleanupTransientFirewallRules(state.FirewallRules)
		cleanupTransientNetworkDevices(state.NetworkDevices)
		cleanupUserNetworkProcess(opts)
		if err := writeProcessState(opts, runtimeStateRequest(req, state), finalState, 0, ""); err != nil {
			return vmkit.Response{}, err
		}
		return eventResponse(req, finalState, ""), nil
	}
	active, err := processActive(state.PID)
	if err != nil {
		return vmkit.Response{}, err
	}
	if active {
		waitTimeout := 5 * time.Second
		if cleanStop {
			// The shared workspace layer has already asked guest PID 1 to shut
			// down. Do not signal Firecracker here: doing so bypasses workload
			// StopSignal handling and turns halt into an ungraceful VMM kill.
			// PID 1 allows the workload up to ten seconds, so give the VM a
			// little additional time to flush the result and exit.
			waitTimeout = 15 * time.Second
		} else if err := signalProcessGroup(state.PID, signal); err != nil && err != syscall.ESRCH {
			errorText := err.Error()
			_ = writeProcessState(opts, runtimeStateRequest(req, state), vmkit.StateFailed, state.PID, errorText)
			return failedResponse(req, errorText), err
		}
		if err := waitForProcessExit(ctx, state.PID, waitTimeout); err != nil {
			errorText := err.Error()
			_ = writeProcessState(opts, runtimeStateRequest(req, state), vmkit.StateFailed, state.PID, errorText)
			return failedResponse(req, errorText), err
		}
	}
	if state.PortForwardPID != 0 {
		_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
	}
	if state.VsockListenerPID != 0 {
		terminateAuxProcess(state.VsockListenerPID)
	}
	if state.EgressMediatorPID != 0 {
		terminateAuxProcess(state.EgressMediatorPID)
	}
	cleanupTransientFirewallRules(state.FirewallRules)
	cleanupTransientNetworkDevices(state.NetworkDevices)
	cleanupUserNetworkProcess(opts)
	if err := writeProcessState(opts, runtimeStateRequest(req, state), finalState, 0, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, finalState, ""), nil
}

func detachedExitObservationWindow(loadMode bool) time.Duration {
	if loadMode {
		return 0
	}
	return 500 * time.Millisecond
}

// quarantineWorkspace is the legacy one-shot supervisor command. The public
// workspace library does not call it: Quarantine uses contain-freeze,
// contain-sever, forensic snapshot, and contain-stop so evidence never leaves
// the actor running. Keep this fail-safe stop for older direct supervisor
// clients.
//
// Stopping is deliberate, not incidental. Previously this left the VM process
// alone and severed around it, which behaved differently per network mode: with
// user-mode networking the VM died anyway as collateral of tearing down pasta,
// while an isolated-network workspace kept running. Explicitly stopping makes
// containment identical across modes and makes the contract truthful.
func quarantineWorkspace(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	resp, err := stopWorkspace(ctx, opts, req, syscall.SIGTERM, vmkit.StateQuarantined)
	if err != nil {
		return resp, err
	}
	// Containment additionally removes the guest-facing socket paths, so no
	// stale endpoint survives for something to reconnect to.
	_ = os.Remove(vsockSocketPath(opts))
	_ = os.Remove(serialInputPath(opts))
	return resp, nil
}

// freezeForContainment freezes guest execution without touching any host-side
// capability. Persisting Paused after the VMM transition prevents normal
// reconciliation from treating the freeze as an interrupted snapshot.
func freezeForContainment(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.Event.State == vmkit.StatePaused {
		if state.PID == 0 {
			err := fmt.Errorf("firecracker workspace %s has no running VM process to freeze", opts.Name)
			return failedResponse(req, err.Error()), err
		}
		active, activeErr := processActive(state.PID)
		if activeErr != nil {
			return vmkit.Response{}, activeErr
		}
		if !active {
			err := fmt.Errorf("firecracker workspace %s VM process %d is not running", opts.Name, state.PID)
			return failedResponse(req, err.Error()), err
		}
		return eventResponse(req, vmkit.StatePaused, ""), nil
	}
	return transitionVMState(ctx, opts, req, "Paused", vmkit.StateRunning, vmkit.StatePaused)
}

// severForContainment revokes guest-reachable host authority while the vCPUs
// remain paused. In user-network mode pasta is the only path out of the private
// netns; SIGSTOP freezes that datapath without destroying the namespace or VM,
// so Firecracker can still capture memory. Final custody later SIGKILLs it and
// the namespace init. Broker/vsock and published-port companions are stopped
// independently here, before the VMM is stopped.
func severForContainment(opts Options, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.Event.State != vmkit.StatePaused {
		err := fmt.Errorf("firecracker workspace %s is %s; containment severance requires paused", opts.Name, state.Event.State)
		return failedResponse(req, err.Error()), err
	}
	if networkMode(&state.Config) == "user" {
		pastaPID := readPIDFile(userNetworkPIDPath(opts))
		if pastaPID <= 0 {
			err := fmt.Errorf("firecracker workspace %s user-network datapath pid is unavailable", opts.Name)
			return failedResponse(req, err.Error()), err
		}
		// Signal only pasta, not its process group: the namespace-init and VMM may
		// share the group and must remain responsive to the snapshot API.
		if err := syscall.Kill(pastaPID, syscall.SIGSTOP); err != nil {
			return failedResponse(req, err.Error()), fmt.Errorf("freeze user-network datapath: %w", err)
		}
		if err := waitForProcessStopped(pastaPID, time.Second); err != nil {
			return failedResponse(req, err.Error()), fmt.Errorf("confirm user-network datapath frozen: %w", err)
		}
	}
	if state.PortForwardPID != 0 {
		terminateAuxProcess(state.PortForwardPID)
	}
	if state.VsockListenerPID != 0 {
		terminateAuxProcess(state.VsockListenerPID)
	}
	// The egress mediator may be in the nested PID namespace and its numeric PID
	// is not a safe host handle. Freezing pasta above severs every packet path it
	// could use. Isolated mode has no network device or mediator.
	_ = os.Remove(vsockSocketPath(opts))
	_ = os.Remove(serialInputPath(opts))
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeStateRequest(req, state), vmkit.StatePaused, state.PID, 0, 0, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, vmkit.StatePaused, ""), nil
}

// vmStateController issues runtime state transitions over a running VM's API
// unix socket. It is satisfied by *apiClient and is a package variable so unit
// tests can substitute a fake without a live Firecracker process.
type vmStateController interface {
	patchVMState(ctx context.Context, state string) error
	getVMState(ctx context.Context) (string, error)
	createSnapshot(ctx context.Context, snapshotPath, memFilePath string) error
	loadSnapshot(ctx context.Context, snapshotPath, memFilePath string, resume bool, networkOverrides []networkOverride) error
}

// firecrackerFrozen reports whether a workspace's live Firecracker has its vCPUs
// paused while the workspace is recorded Running — an alive-but-frozen VM (e.g. a
// residual of an interrupted snapshot). Detection keys off the recorded Running
// state, which the caller checks: an intentionally paused workspace is recorded
// Paused and is never asked about here. Fail-safe: a GET error or any non-Paused
// state returns false, so a wedged or healthy API never yields a false positive.
func firecrackerFrozen(opts Options) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := newVMStateController(apiSocketPath(opts)).getVMState(ctx)
	return err == nil && s == fcStatePaused
}

// healFrozenVM resumes an alive-but-frozen VM (see firecrackerFrozen). It is
// best-effort and fail-safe: a non-frozen VM or any API error leaves the VM
// untouched. The resume runs on a context detached from any request deadline so
// it can complete even when the caller was cancelled. Called from both inspect
// and gc — a freeze must not need an operator to notice it.
// It returns nil when the VM was not frozen or was successfully resumed, and
// the resume error when the repair failed — so a caller can surface an anomaly
// that outlived its repair instead of reporting the workspace healthy.
func healFrozenVM(opts Options) error {
	if !firecrackerFrozen(opts) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := newVMStateController(apiSocketPath(opts)).patchVMState(ctx, "Resumed"); err != nil {
		fmt.Fprintf(os.Stderr, "microagent: resume frozen workspace %s: %v\n", opts.Name, err)
		return err
	}
	debugSupLog(opts, fmt.Sprintf("UNFREEZE %s: firecracker vCPUs were paused while recorded running; resumed", opts.Name))
	return nil
}

var newVMStateController = func(socketPath string) vmStateController {
	return newAPIClient(socketPath)
}

func pauseWorkspace(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	return transitionVMState(ctx, opts, req, "Paused", vmkit.StateRunning, vmkit.StatePaused)
}

func resumeWorkspace(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	if vmkit.ContainmentMarked(opts.StateDir, opts.Name) {
		err := fmt.Errorf("firecracker workspace %s has a durable containment marker; resume denied", opts.Name)
		return failedResponse(req, err.Error()), err
	}
	return transitionVMState(ctx, opts, req, "Resumed", vmkit.StatePaused, vmkit.StateRunning)
}

// transitionVMState pauses or resumes the running VM over its API socket. It
// requires the workspace to be in fromState, issues the PATCH /vm transition,
// and persists toState while preserving the host-side aux processes (port
// forwarder, vsock listener, transient network) so resume keeps working. The
// VM process is untouched; only its vCPUs are frozen or thawed.
func transitionVMState(ctx context.Context, opts Options, req vmkit.Request, apiState string, fromState, toState vmkit.VMState) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.Event.State != fromState {
		err := fmt.Errorf("firecracker workspace %s is %s; %s requires state %s", opts.Name, state.Event.State, req.Command, fromState)
		return failedResponse(req, err.Error()), err
	}
	if state.PID == 0 {
		err := fmt.Errorf("firecracker workspace %s has no running VM process to %s", opts.Name, req.Command)
		return failedResponse(req, err.Error()), err
	}
	active, err := processActive(state.PID)
	if err != nil {
		return vmkit.Response{}, err
	}
	if !active {
		err := fmt.Errorf("firecracker workspace %s VM process %d is not running", opts.Name, state.PID)
		return failedResponse(req, err.Error()), err
	}
	if err := newVMStateController(apiSocketPath(opts)).patchVMState(ctx, apiState); err != nil {
		// The PATCH is atomic; on failure the VM stays in fromState, so leave the
		// persisted state untouched rather than recording a spurious failure.
		return failedResponse(req, err.Error()), err
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeStateRequest(req, state), toState, state.PID, state.PortForwardPID, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, toState, ""), nil
}

func ensureCanDelete(opts Options) error {
	if vmkit.ContainmentMarked(opts.StateDir, opts.Name) {
		return fmt.Errorf("firecracker workspace %s is in durable containment custody; delete denied", opts.Name)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if state.PID == 0 {
		if state.Event.State == vmkit.StateStarting || state.Event.State == vmkit.StateRunning {
			return fmt.Errorf("firecracker workspace %s is running; stop or kill it before delete", opts.Name)
		}
		return ensureWorkspaceProcessesStopped(opts, state)
	}
	active, err := processActive(state.PID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("firecracker workspace %s is running; stop or kill it before delete", opts.Name)
	}
	return ensureWorkspaceProcessesStopped(opts, state)
}

// ensureWorkspaceProcessesStopped rejects delete while any recorded companion
// or user-network process for the workspace is still running, regardless of
// whether the VM process itself is gone (a guest that exits on its own leaves
// the VM dead but can leave companions behind).
func ensureWorkspaceProcessesStopped(opts Options, state runtimeState) error {
	if state.PortForwardPID != 0 {
		active, err := processActive(state.PortForwardPID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("firecracker workspace %s port forwarder is running; stop or kill it before delete", opts.Name)
		}
	}
	if state.VsockListenerPID != 0 {
		active, err := processActive(state.VsockListenerPID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("firecracker workspace %s vsock listener is running; stop or kill it before delete", opts.Name)
		}
	}
	if state.EgressMediatorPID != 0 {
		active, err := processActive(state.EgressMediatorPID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("firecracker workspace %s egress mediator is running; stop or kill it before delete", opts.Name)
		}
	}
	if active, err := userNetworkProcessActive(opts); err != nil {
		return err
	} else if active {
		return fmt.Errorf("firecracker workspace %s user network process is running; stop or kill it before delete", opts.Name)
	}
	return nil
}
