//go:build linux

package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/eventhistory"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func inspectWorkspace(opts Options) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		event, eventErr := readEvent(opts)
		if eventErr != nil {
			return vmkit.Response{}, err
		}
		return responseFromEvent(event, ""), nil
	}
	fcAlive := firecrackerAlive(state, opts)
	// Reconcile a workspace whose VM is gone: a Running/Stopping VM that halted or
	// died, OR a Paused VM whose firecracker has since died — e.g. one an
	// interrupted snapshot (a cancelled Dispatch SIGKILLing the supervisor between
	// Paused and Resumed) left frozen, whose process later exited. Without the
	// Paused case such a workspace stays "Paused" forever with its aux resources
	// (mediator, taps, nft rules) leaked, since gc and inspect otherwise only
	// reconcile Running/Stopping. A paused VM whose firecracker is still alive is a
	// valid (possibly intentional) pause and is left untouched.
	reconcileDead := ((state.Event.State == vmkit.StateRunning || state.Stopping) && (GuestHalted(serialLogPath(opts)) || !fcAlive)) ||
		(state.Event.State == vmkit.StatePaused && !fcAlive)
	if reconcileDead {
		resultWait := time.Duration(0)
		if runtimeHasResultListener(opts, state) {
			resultWait = 2 * time.Second
		}
		// An intentional host stop records Stopping before signaling firecracker.
		// Now that firecracker is dead, the stop has completed: classify it as a
		// clean Stopped rather than re-reading the killed command's non-zero
		// result.json (which guestHaltedState would report as Failed). A genuine
		// crash (Stopping==false) still classifies via guestHaltedState. This is
		// gated inside the fc-dead branch, so a still-alive fc yields no premature
		// reclassification or restart.
		// Re-read for the freshest stop intent (see gcWorkspace): a concurrent
		// stop may have recorded Stopping between our snapshot and the fc dying.
		if fresh, err := readRuntimeState(opts); err == nil {
			state.Stopping = fresh.Stopping
			state.Event.State = fresh.Event.State
		}
		finalState, errorText := vmkit.StateStopped, ""
		if !state.Stopping && state.Event.State != vmkit.StateStopped && state.Event.State != vmkit.StateHalted {
			finalState, errorText = guestHaltedState(opts, resultWait)
		}
		debugSupLog(opts, fmt.Sprintf("INSPECT-RECLASSIFY eventState=%s stopping=%v -> %s err=%q", state.Event.State, state.Stopping, finalState, errorText))
		// Only signal the recorded pid if it is still THIS workspace's firecracker.
		// A pid we no longer own (recycled by an unrelated process) must not be
		// signaled: signalProcessGroup targets the pid's group and would kill an
		// innocent bystander. A genuinely dead pid won't reference us either, so the
		// guard is a safe no-op there too.
		if state.PID != 0 && processReferencesWorkspace(state.PID, opts) {
			_ = signalProcessGroup(state.PID, syscall.SIGTERM)
			_ = waitForProcessExit(context.Background(), state.PID, 5*time.Second)
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
		req := runtimeStateRequest(vmkit.Request{}, state)
		if err := writeProcessStateWithProcessesAndNetwork(opts, req, finalState, 0, 0, 0, 0, nil, nil, errorText); err != nil {
			return vmkit.Response{}, err
		}
		state, err = readRuntimeState(opts)
		if err != nil {
			return vmkit.Response{}, err
		}
		return responseFromRuntimeState(opts, state), nil
	}
	resp := responseFromRuntimeState(opts, state)
	// Heal an alive-but-frozen VM: a workspace recorded Running with a live
	// Firecracker whose vCPUs are actually paused. That happens when the process
	// driving a snapshot dies between the auto-pause and the resume — SIGKILL, an
	// OOM kill, a host reboot, a node daemon restart — where no deferred resume
	// can run because no code runs at all.
	//
	// This used to only be REPORTED here and healed by gc. But gc runs only when
	// an operator types `microagent gc`, so in practice nothing healed it: a guest
	// froze indefinitely while every state file still said Running, and anything
	// scheduling on top kept its slot allocated against a workspace making no
	// progress. inspect already reconciles a workspace whose VM has died, so
	// repairing one whose VM is wrongly frozen belongs here too — it restores the
	// state the record already claims rather than imposing a new one.
	//
	// The recorded state IS the intent: an operator pause records Paused and never
	// reaches this branch, so there is no way to confuse a deliberate pause with
	// this anomaly. Healing is best-effort; a failure leaves the anomaly reported.
	if state.Event.State == vmkit.StateRunning && fcAlive {
		if err := healFrozenVM(opts); err != nil {
			if resp.Readiness == nil {
				resp.Readiness = &vmkit.RuntimeReadiness{}
			}
			resp.Readiness.GuestReady = vmkit.ReadinessSignal{
				Ready:  false,
				Detail: "firecracker reports vCPUs paused while recorded running; resume failed",
				Error:  err.Error(),
			}
		}
	}
	return resp, nil
}

// firecrackerAlive reports whether the recorded VMM PID is genuinely this
// workspace's firecracker. A live PID alone isn't proof — PIDs get reused — so
// it must still carry this workspace's argv. False means the VM is gone (crashed,
// killed, or exited cleanly) and the workspace is stale.
func firecrackerAlive(state runtimeState, opts Options) bool {
	a, _ := processActive(state.PID)
	return a && processReferencesWorkspace(state.PID, opts)
}

// debugSupLog appends a diagnostic line to the per-workspace sup-debug.log when
// MICROAGENT_DEBUG_SUPERVISE is set. Multiple processes (supervise loop, gc,
// stop) append concurrently; O_APPEND keeps each line intact. Off by default
// (zero cost); flip the env to trace the stop/inspect/gc/write sequence when
// debugging a supervise lifecycle or classification race — these subsystems
// produce subtle timing bugs and re-instrumenting from scratch each time is
// wasted work.
func debugSupLog(opts Options, msg string) {
	if os.Getenv("MICROAGENT_DEBUG_SUPERVISE") == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(opts.StateDir, opts.Name, "sup-debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintf(f, "%s pid=%d %s\n", time.Now().Format("15:04:05.000000"), os.Getpid(), msg)
}

// gcWorkspace reconciles one workspace against reality: if it's recorded as
// running but its firecracker process is gone (crashed, OOM-killed, host
// rebooted, or an orphaned supervisor), it's stale — reap any lingering
// companion processes + transient network state and record it stopped. Unlike
// inspectWorkspace's clean-halt path, this triggers on PID liveness, not the
// serial log. Idempotent + ESRCH-tolerant, so a sweep can call it on every
// workspace safely.
func gcWorkspace(opts Options) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		event, eventErr := readEvent(opts)
		if eventErr != nil {
			return vmkit.Response{Backend: vmkit.BackendLinuxKVM}, nil
		}
		return responseFromEvent(event, ""), nil
	}
	if state.Event.State != vmkit.StateRunning {
		// A Paused workspace whose firecracker has died must still be reaped (an
		// interrupted snapshot left it paused and the process later exited), or it
		// stays "Paused" forever with its aux resources leaked — the reap body
		// below handles it exactly like a dead Running VM. A paused VM whose
		// firecracker is still alive is a valid (possibly intentional) pause; any
		// other non-Running state is returned as-is.
		if state.Event.State != vmkit.StatePaused || firecrackerAlive(state, opts) {
			return responseFromRuntimeState(opts, state), nil
		}
	}
	// A live PID alone isn't proof of life — PIDs get reused (including by gc's
	// own freshly-spawned supervisor). The VM is healthy only if the recorded
	// PID is alive AND still carries this workspace's argv.
	alive := firecrackerAlive(state, opts)
	expired := leaseExpired(state, opts)
	if alive && !expired {
		// A live, in-lease VM recorded Running may still be frozen (vCPUs paused)
		// if a residual interrupted snapshot left it so. gc is the mutating sweep,
		// so it heals: resume the VM back to Running. A non-frozen or intentionally
		// paused (recorded Paused, handled above) VM is untouched.
		if state.Event.State == vmkit.StateRunning {
			_ = healFrozenVM(opts)
		}
		return responseFromRuntimeState(opts, state), nil
	}
	// Reap: the VMM is gone (dead or its PID was reused), or a live VM is past
	// its declared lifetime lease. SIGKILL covers both — a no-op on a dead pid,
	// a teardown of a live one.
	reapReason := "reaped by gc: firecracker process gone"
	if alive && expired {
		reapReason = "reaped by gc: lifetime lease expired"
	}
	// A VMM that exited on its own carries a guest exit code: honor it so a failed
	// guest is recorded Failed (and restart-on-failure can act on it), matching the
	// inspect path, not Stopped. A forced teardown (lease expiry, or a reused/
	// never-recorded pid that still looks "alive") has no clean exit, so it stays
	// Stopped.
	// A concurrent stop can record intent (or write a terminal Stopped) between
	// our snapshot and firecracker actually dying — persistStopIntent runs before
	// the SIGTERM, so once the pid is gone the intent is already on disk. Re-read
	// so we honor the freshest value rather than a stale snapshot that raced ahead
	// of the stop and would mis-classify the killed command as Failed.
	if fresh, err := readRuntimeState(opts); err == nil {
		state.Stopping = fresh.Stopping
		state.Event.State = fresh.Event.State
	}
	finalState, detail := vmkit.StateStopped, reapReason
	if !alive && !state.Stopping && state.Event.State != vmkit.StateStopped && state.Event.State != vmkit.StateHalted {
		if s, errText := guestHaltedState(opts, 0); s == vmkit.StateFailed {
			finalState, detail = s, errText
		}
	}
	debugSupLog(opts, fmt.Sprintf("GC-REAP eventState=%s stopping=%v alive=%v expired=%v -> %s (%s)", state.Event.State, state.Stopping, alive, expired, finalState, detail))
	// Only kill the recorded pid if it is still THIS workspace's firecracker (see
	// inspectWorkspace): a recycled pid belongs to an unrelated process and must
	// not be signaled.
	if state.PID != 0 && processReferencesWorkspace(state.PID, opts) {
		_ = signalProcessGroup(state.PID, syscall.SIGKILL)
	}
	if state.PortForwardPID != 0 {
		_ = signalProcessGroup(state.PortForwardPID, syscall.SIGKILL)
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
	req := runtimeStateRequest(vmkit.Request{}, state)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, finalState, 0, 0, 0, 0, nil, nil, detail); err != nil {
		return vmkit.Response{}, err
	}
	state, err = readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	return responseFromRuntimeState(opts, state), nil
}

func runtimeHasResultListener(opts Options, state runtimeState) bool {
	resultPath := resultPathFromState(opts, state)
	for _, listener := range state.Config.VsockListeners {
		if listener.Target == resultPath {
			return true
		}
	}
	return false
}

func guestHaltedState(opts Options, waitForResult time.Duration) (vmkit.VMState, string) {
	resultPath := filepath.Join(opts.StateDir, opts.Name, "result.json")
	deadline := time.Now().Add(waitForResult)
	var data []byte
	var err error
	for {
		data, err = os.ReadFile(resultPath)
		if err != nil && (waitForResult <= 0 || !os.IsNotExist(err) || time.Now().After(deadline)) {
			return vmkit.StateStopped, ""
		}
		if err == nil {
			var result struct {
				ExitCode   int    `json:"exit_code"`
				Error      string `json:"error"`
				PoweredOff bool   `json:"powered_off"`
			}
			if err := json.Unmarshal(data, &result); err == nil {
				// An intentional power-off is a clean stop even when the
				// workspace command it interrupted was killed and reported a
				// non-zero exit code. Classify by the marker, not the code.
				if result.PoweredOff {
					return vmkit.StateStopped, ""
				}
				if result.ExitCode == 0 {
					return vmkit.StateStopped, ""
				}
				if result.Error != "" {
					return vmkit.StateFailed, result.Error
				}
				return vmkit.StateFailed, fmt.Sprintf("guest exited with code %d", result.ExitCode)
			} else if waitForResult <= 0 || time.Now().After(deadline) {
				return vmkit.StateFailed, err.Error()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func responseFromEvent(file eventFile, errorText string) vmkit.Response {
	event := vmkit.Event{Identity: file.Identity, State: file.State, Detail: file.Detail, ObservedAt: time.Now().UTC()}
	if parsed, err := time.Parse(time.RFC3339, file.ObservedAt); err == nil {
		event.ObservedAt = parsed
	}
	resp := vmkit.Response{OK: file.State != vmkit.StateFailed, Backend: vmkit.BackendLinuxKVM, Event: &event}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func responseFromRuntimeState(opts Options, state runtimeState) vmkit.Response {
	resp := responseFromEvent(state.Event, state.Error)
	readiness := readinessFromRuntimeState(state)
	resp.Readiness = &readiness
	if state.Config.Network != nil {
		network := *state.Config.Network
		network.Runtime = nil
		resp.Network = &network
	}
	resp.Mediation = state.Config.Mediation
	if result, err := runtimeResultFromState(opts, state); err == nil {
		resp.Result = &result
	}
	return resp
}

func runtimeResultFromState(opts Options, state runtimeState) (vmkit.RuntimeResult, error) {
	path := resultPathFromState(opts, state)
	data, err := os.ReadFile(path)
	if err != nil {
		return vmkit.RuntimeResult{}, err
	}
	var guest guestResult
	if err := json.Unmarshal(data, &guest); err != nil {
		return vmkit.RuntimeResult{}, err
	}
	return vmkit.RuntimeResult{
		Identity:    state.Event.Identity,
		Backend:     vmkit.BackendLinuxKVM,
		ResultPath:  path,
		StartedAt:   guest.StartedAt,
		CompletedAt: guest.ExitedAt,
		ExitCode:    guest.ExitCode,
		Stdout:      guest.Stdout,
		Stderr:      guest.Stderr,
		Error:       guest.Error,
	}, nil
}

func writeProcessState(opts Options, req vmkit.Request, state vmkit.VMState, pid int, errorText string) error {
	return writeProcessStateWithForwarder(opts, req, state, pid, 0, errorText)
}

func writeProcessStateWithForwarder(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID int, errorText string) error {
	return writeProcessStateWithForwarderAndNetwork(opts, req, state, pid, portForwardPID, nil, nil, errorText)
}

func writeProcessStateWithForwarderAndNetwork(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID int, networkDevices []transientNetworkDevice, firewallRules []transientFirewallRule, errorText string) error {
	return writeProcessStateWithProcessesAndNetwork(opts, req, state, pid, portForwardPID, 0, 0, networkDevices, firewallRules, errorText)
}

func writeProcessStateWithProcessesAndNetwork(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID, vsockListenerPID, egressMediatorPID int, networkDevices []transientNetworkDevice, firewallRules []transientFirewallRule, errorText string) error {
	if req.Identity == nil || req.Config == nil {
		return fmt.Errorf("workspace request is missing identity or config")
	}
	if shouldPreserveQuarantine(opts, req, state) {
		return nil
	}
	debugSupLog(opts, fmt.Sprintf("WRITE state=%s pid=%d err=%q (resets Stopping)", state, pid, errorText))
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	now := time.Now().UTC()
	fileEvent := eventFile{
		Identity:   *req.Identity,
		State:      state,
		Detail:     "serial=" + serialLogPath(opts),
		ObservedAt: now.Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "event.json"), fileEvent); err != nil {
		return err
	}
	if err := appendEvent(filepath.Join(dir, "events.json"), fileEvent); err != nil {
		return err
	}
	runtime := runtimeState{
		Event:             fileEvent,
		Config:            *req.Config,
		PID:               pid,
		PortForwardPID:    portForwardPID,
		VsockListenerPID:  vsockListenerPID,
		EgressMediatorPID: egressMediatorPID,
		NetworkDevices:    append([]transientNetworkDevice{}, networkDevices...),
		FirewallRules:     append([]transientFirewallRule{}, firewallRules...),
		SerialLogPath:     serialLogPath(opts),
		SerialInputPath:   serialInputPath(opts),
		UpdatedAt:         now.Format(time.RFC3339),
		Error:             errorText,
	}
	// A declared lifetime lease is set once (at create/launch) and must survive
	// later state writes — e.g. a start that didn't re-specify --ttl.
	if runtime.Config.LeaseSeconds == 0 {
		if prev, err := readRuntimeState(opts); err == nil && prev.Config.LeaseSeconds > 0 {
			runtime.Config.LeaseSeconds = prev.Config.LeaseSeconds
		}
	}
	if state == vmkit.StateStarting || state == vmkit.StateRunning {
		runtime.StartedAt = now.Format(time.RFC3339)
	}
	runtime.Readiness = readinessFromRuntimeState(runtime)
	return writeJSONFile(filepath.Join(dir, "runtime.json"), runtime)
}

func shouldPreserveQuarantine(opts Options, req vmkit.Request, next vmkit.VMState) bool {
	if next != vmkit.StateStopped && next != vmkit.StateFailed {
		return false
	}
	switch req.Command {
	case "halt", "stop", "kill", "delete":
		return false
	}
	prev, err := readRuntimeState(opts)
	return err == nil && prev.Event.State == vmkit.StateQuarantined
}

func readinessFromRuntimeState(state runtimeState) vmkit.RuntimeReadiness {
	readiness := vmkit.RuntimeReadiness{}
	if state.StartedAt != "" || state.Event.State == vmkit.StateRunning || state.Event.State == vmkit.StateHalted || state.Event.State == vmkit.StateStopped || state.Event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: firstEventTime(state.StartedAt, state.Event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(state.Event.State),
		}
	}
	if state.Event.State == vmkit.StateRunning && state.SerialInputPath != "" {
		if signal, ok := shellReadinessFromRuntimeState(state); ok {
			readiness.ShellReady = signal
		}
	}
	if signal, ok := execReadinessFromRuntimeState(state); ok {
		readiness.ExecReady = signal
	}
	resultPath := resultPathFromState(Options{}, state)
	if _, err := os.Stat(resultPath); err == nil {
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: fileModTime(resultPath),
			Detail:     "guest result is available",
		}
	} else if !os.IsNotExist(err) {
		readiness.ResultReady = vmkit.ReadinessSignal{Error: err.Error()}
	}
	if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
		readiness.MediationReady = vmkit.MediationReadinessSignal(context.Background(), *state.Config.Mediation, state.Event.State, firstEventTime(state.StartedAt, state.Event.ObservedAt), 150*time.Millisecond)
	}
	return readiness
}

func execReadinessFromRuntimeState(state runtimeState) (vmkit.ReadinessSignal, bool) {
	if state.Event.State != vmkit.StateRunning {
		return vmkit.ReadinessSignal{}, false
	}
	observedAt := time.Now().UTC()
	if state.Config.ExecPort == 0 {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     "structured exec port is not configured",
		}, true
	}
	if detail, ok := inactivePortForwarderDetail(state); ok {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     detail,
		}, true
	}
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(state.Config.ExecPort)))
	req := execprotocol.NewExecRequest([]string{"true"})
	req.TimeoutMS = 2000
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	start := time.Now()
	result, err := execclient.New(target).Exec(ctx, req)
	cancel()
	elapsed := time.Since(start)
	if err != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service unreachable at %s after %s: %v", target, elapsed.Round(time.Millisecond), err),
		}, true
	}
	if result.Error != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service returned %s: %s", result.Error.Code, result.Error.Message),
			Error:      result.Error.Error(),
		}, true
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		exit := "nil"
		if result.ExitCode != nil {
			exit = strconv.Itoa(*result.ExitCode)
		}
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec probe command failed unexpectedly: status=%s exit_code=%s", result.Status, exit),
		}, true
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: &observedAt,
		Detail:     fmt.Sprintf("exec service round-trip ready at %s in %s", target, elapsed.Round(time.Millisecond)),
	}, true
}

func shellReadinessFromRuntimeState(state runtimeState) (vmkit.ReadinessSignal, bool) {
	if _, err := os.Stat(state.SerialInputPath); err != nil {
		if !os.IsNotExist(err) {
			return vmkit.ReadinessSignal{Error: err.Error()}, true
		}
		return vmkit.ReadinessSignal{}, false
	}
	if state.Config.ShellPort != 0 {
		target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(state.Config.ShellPort)))
		observedAt := time.Now().UTC()
		if detail, ok := inactivePortForwarderDetail(state); ok {
			return vmkit.ReadinessSignal{
				Ready:      false,
				ObservedAt: &observedAt,
				Detail:     detail,
			}, true
		}
		start := time.Now()
		conn, err := net.DialTimeout("tcp", target, 150*time.Millisecond)
		elapsed := time.Since(start)
		if err != nil {
			return vmkit.ReadinessSignal{
				Ready:      false,
				ObservedAt: &observedAt,
				Detail:     fmt.Sprintf("shell target unreachable at %s after %s: %v", target, elapsed.Round(time.Millisecond), err),
			}, true
		}
		_ = conn.Close()
		return vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("shell target reachable at %s in %s", target, elapsed.Round(time.Millisecond)),
		}, true
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: fileModTime(state.SerialInputPath),
		Detail:     "console input is available",
	}, true
}

func inactivePortForwarderDetail(state runtimeState) (string, bool) {
	if state.PortForwardPID == 0 {
		return "", false
	}
	active, err := processActive(state.PortForwardPID)
	if err != nil || active {
		return "", false
	}
	return fmt.Sprintf("port forwarder process %d is not running", state.PortForwardPID), true
}

func resultPathFromState(opts Options, state runtimeState) string {
	stateDir := state.Config.StateDir
	if stateDir == "" {
		stateDir = opts.StateDir
	}
	name := state.Event.Identity.RuntimeID
	if name == "" {
		name = opts.Name
	}
	return filepath.Join(stateDir, name, "result.json")
}

func fileModTime(path string) *time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	modified := info.ModTime().UTC()
	return &modified
}

func firstEventTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func appendEvent(path string, event eventFile) error {
	return eventhistory.Append(path, event, eventhistory.Options{})
}

// runtimeStateRequest rebuilds a request whose config is the recorded runtime
// config, so state rewrites (stop, pause/resume, snapshot, apply) never drop
// runtime fields the originating verb's sparse config does not carry. Only the
// caller's StateDir is preserved; everything else comes from the state file —
// a field-by-field copy here is exactly how shell/exec ports were once lost.
func runtimeStateRequest(req vmkit.Request, state runtimeState) vmkit.Request {
	if req.Identity == nil {
		identity := state.Event.Identity
		req.Identity = &identity
	}
	config := state.Config
	if req.Config != nil && strings.TrimSpace(req.Config.StateDir) != "" {
		config.StateDir = req.Config.StateDir
	}
	req.Config = &config
	return req
}

func readRuntimeState(opts Options) (runtimeState, error) {
	var state runtimeState
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "runtime.json"))
	if err != nil {
		return state, err
	}
	return state, json.Unmarshal(data, &state)
}

// persistStopIntent records, in the still-Running runtime state, that a clean
// host stop/halt is in progress. It re-writes the runtime file in place
// (preserving every PID, network device, and firewall rule) with Stopping set,
// so a concurrent inspect/gc re-classification of the soon-to-be-dead
// firecracker resolves the intentional stop to Stopped instead of the killed
// command's failure. It must be called BEFORE firecracker is signaled.
func persistStopIntent(opts Options, state runtimeState) error {
	state.Stopping = true
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	debugSupLog(opts, fmt.Sprintf("PERSIST-STOP-INTENT eventState=%s pid=%d", state.Event.State, state.PID))
	return writeJSONFile(filepath.Join(opts.StateDir, opts.Name, "runtime.json"), state)
}

func readEvent(opts Options) (eventFile, error) {
	var event eventFile
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "event.json"))
	if err != nil {
		return event, err
	}
	return event, json.Unmarshal(data, &event)
}
