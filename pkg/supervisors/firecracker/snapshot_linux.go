package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
	"golang.org/x/sys/unix"
)

// snapshotWorkspace captures a full snapshot of a live workspace. Firecracker
// requires the VM be paused before PUT /snapshot/create, so a running workspace
// is auto-paused around the capture and resumed afterward (the brief pause is
// recorded in the event history); an already-paused workspace is snapshotted in
// place and left paused. The Firecracker snapshot captures memory and device
// state only, so while the VM is paused — which quiesces guest I/O — the
// supervisor also copies the workspace rootfs so memory and disk are coherent.
func snapshotWorkspace(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	current := state.Event.State
	// Snapshot needs a live VM: memory and device state come from the running
	// Firecracker. Quarantine STOPS the runtime, so there is nothing to capture
	// from a contained workspace — capture BEFORE containing when the volatile
	// state matters, which is the ordering incident response wants anyway.
	if current != vmkit.StateRunning && current != vmkit.StatePaused {
		err := fmt.Errorf("firecracker workspace %s is %s; snapshot requires a running or paused workspace (capture before quarantining — containment stops the runtime)", opts.Name, current)
		return failedResponse(req, err.Error()), err
	}
	if state.PID == 0 {
		err := fmt.Errorf("firecracker workspace %s has no running VM process to snapshot", opts.Name)
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

	finalDir := vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag)
	// Capture into a staging dir OUTSIDE the snapshots directory, then publish it
	// atomically only on success (publishSnapshot below). A failure at ANY step
	// then leaves an existing snapshot at this tag untouched — previously every
	// failure path ran os.RemoveAll on the real snapshot dir, so re-snapshotting a
	// tag and hitting any error destroyed the good snapshot. Staging outside
	// SnapshotsDir keeps a partially-captured dir out of ListSnapshots; it stays
	// under the workspace dir so a confined firecracker can still write to it via
	// confinedWorkspacePath.
	stagingParent := vmkit.SnapshotStagingParent(opts.StateDir, opts.Name)
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return vmkit.Response{}, err
	}
	dir, err := os.MkdirTemp(stagingParent, req.Tag+"-*")
	if err != nil {
		return vmkit.Response{}, err
	}

	// Purge materialized secrets from the guest tmpfs while the VM is still
	// running (before the auto-pause below), so the captured memory holds zeros.
	// Fail closed: a snapshot of a secrets-bearing workspace is never created
	// with un-purged plaintext.
	// A forensic capture deliberately retains secrets, so the purge (and its
	// fail-closed gate) is skipped entirely for that mode.
	retainSecrets := req.RetainSecrets
	purged := false
	if !retainSecrets && vmkit.MaterializedSecretsDeclared(&state.Config) {
		if state.Config.SecretsControlPort == 0 {
			err := fmt.Errorf("cannot purge secrets for snapshot: workspace %s has materialized secrets but no secrets control port", opts.Name)
			_ = os.RemoveAll(dir)
			return failedResponse(req, err.Error()), err
		}
		if current != vmkit.StateRunning {
			// Purge needs the running guest's secrets control channel; a paused
			// guest cannot service it. Fail closed rather than persist plaintext.
			// A forensic capture takes the RetainSecrets path instead and never
			// reaches here.
			err := fmt.Errorf("cannot purge secrets for snapshot: workspace %s is %s, must be running to purge", opts.Name, current)
			_ = os.RemoveAll(dir)
			return failedResponse(req, err.Error()), err
		}
		if err := purgeGuestSecrets(opts, state.Config.SecretsControlPort); err != nil {
			_ = os.RemoveAll(dir)
			wrapped := fmt.Errorf("purge guest secrets before snapshot: %w", err)
			return failedResponse(req, wrapped.Error()), wrapped
		}
		purged = true
	}

	controller := newVMStateController(apiSocketPath(opts))
	// Resume the VM on ANY exit from the snapshot — including a cancelled request
	// (Ctrl-C / caller timeout) — using a context DETACHED from the request ctx.
	// Pause and capture correctly abort on cancellation, but the resume must still
	// run: reusing the cancelled request ctx would make the resume PATCH itself
	// fail, leaving the guest frozen with no automatic recovery. A fresh
	// short-timeout context lets the supervisor un-freeze the guest before it
	// exits (the client grants a SIGTERM grace window for exactly this).
	resumeCtx, cancelResume := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelResume()
	// A running workspace has live vCPUs Firecracker must pause before PUT
	// /snapshot/create; an already-paused one is captured in place. The pause is
	// transient and the workspace is resumed back to the state it came from.
	autoPaused := current == vmkit.StateRunning
	if autoPaused {
		if err := controller.patchVMState(ctx, "Paused"); err != nil {
			_ = os.RemoveAll(dir)
			return failedResponse(req, err.Error()), err
		}
		if err := writeSnapshotState(opts, req, state, vmkit.StatePaused); err != nil {
			_ = controller.patchVMState(resumeCtx, "Resumed")
			_ = os.RemoveAll(dir)
			return vmkit.Response{}, err
		}
	}

	confined := firecrackerProcessConfinedToWorkspace(state.PID, opts)
	if err := writeSnapshotArtifacts(ctx, controller, opts, state, dir, req.Tag, purged, retainSecrets, confined); err != nil {
		if autoPaused {
			_ = controller.patchVMState(resumeCtx, "Resumed")
			_ = writeSnapshotState(opts, req, state, current)
		}
		_ = os.RemoveAll(dir)
		return failedResponse(req, err.Error()), err
	}

	// Artifacts fully written to the staging dir; publish atomically into place.
	// Do it while still paused so a publish failure can resume + report without
	// leaving a half-swapped snapshot dir.
	if err := publishSnapshot(dir, finalDir); err != nil {
		if autoPaused {
			_ = controller.patchVMState(resumeCtx, "Resumed")
			_ = writeSnapshotState(opts, req, state, current)
		}
		_ = os.RemoveAll(dir)
		return failedResponse(req, err.Error()), err
	}

	finalState := vmkit.StatePaused
	if autoPaused {
		if err := controller.patchVMState(resumeCtx, "Resumed"); err != nil {
			return failedResponse(req, err.Error()), err
		}
		finalState = current
		if err := writeSnapshotState(opts, req, state, current); err != nil {
			return vmkit.Response{}, err
		}
		// Rehydrate the source after resume: the snapshot is already captured
		// (purged), so re-fetch the secrets so the running source keeps working.
		// Best-effort: the snapshot artifact is correct regardless, and a failed
		// rehydrate is a recoverable runtime condition, not a reason to fail the
		// completed snapshot.
		if purged {
			if err := rehydrateGuestSecrets(opts, state.Config.SecretsControlPort); err != nil {
				fmt.Fprintf(os.Stderr, "warning: rehydrate source secrets after snapshot failed: %v\n", err)
			}
		}
	}
	return eventResponse(req, finalState, ""), nil
}

// publishSnapshot is the shared temp-swap publish (vmkit.PublishSnapshotDir),
// kept as a local name so the capture flow above reads at one altitude.
func publishSnapshot(stagingDir, finalDir string) error {
	return vmkit.PublishSnapshotDir(stagingDir, finalDir)
}

// writeSnapshotState persists a transient pause/resume around a snapshot while
// preserving the host-side aux processes so the workspace keeps working.
func writeSnapshotState(opts Options, req vmkit.Request, state runtimeState, target vmkit.VMState) error {
	return writeProcessStateWithProcessesAndNetwork(opts, runtimeStateRequest(req, state), target, state.PID, state.PortForwardPID, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, "")
}

func writeSnapshotArtifacts(ctx context.Context, controller vmStateController, opts Options, state runtimeState, dir, tag string, purged, retainSecrets, confined bool) error {
	vmstatePath := filepath.Join(dir, vmkit.SnapshotVMStateName)
	memoryPath := filepath.Join(dir, vmkit.SnapshotMemoryName)
	vmstateAPIPath, memoryAPIPath, err := snapshotAPIPaths(opts, confined, vmstatePath, memoryPath)
	if err != nil {
		return err
	}
	if err := controller.createSnapshot(ctx, vmstateAPIPath, memoryAPIPath); err != nil {
		return err
	}
	if err := copyFile(state.Config.RootfsPath, filepath.Join(dir, vmkit.SnapshotRootfsName)); err != nil {
		return fmt.Errorf("copy rootfs into snapshot: %w", err)
	}
	// The vmstate records the config disk's device geometry; restores must
	// re-attach a byte-identical file, so the capture is authoritative.
	if state.Config.ConfigDiskPath != "" {
		if err := copyFile(state.Config.ConfigDiskPath, filepath.Join(dir, vmkit.SnapshotConfigDiskName)); err != nil {
			return fmt.Errorf("copy config disk into snapshot: %w", err)
		}
	}
	manifest, err := snapshotManifestFromState(tag, state, opts, purged, retainSecrets)
	if err != nil {
		return err
	}
	return vmkit.WriteSnapshotManifest(dir, manifest)
}

func snapshotManifestFromState(tag string, state runtimeState, opts Options, purged, retainSecrets bool) (vmkit.SnapshotManifest, error) {
	if err := vmkit.ValidateSnapshotSecretCapture(&state.Config, purged, retainSecrets); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	kernelSHA := ""
	if path := strings.TrimSpace(state.Config.KernelPath); path != "" {
		sha, err := fileSHA256(path)
		if err != nil {
			return vmkit.SnapshotManifest{}, fmt.Errorf("hash kernel for snapshot: %w", err)
		}
		kernelSHA = sha
	}
	mode, guestIP := "", ""
	netIP, netGateway, netSubnet := "", "", ""
	if state.Config.Network != nil {
		mode = strings.TrimSpace(state.Config.Network.Mode)
		guestIP = guestIPFromNetwork(*state.Config.Network)
		netIP = strings.TrimSpace(state.Config.Network.IP)
		netGateway = strings.TrimSpace(state.Config.Network.Gateway)
		netSubnet = strings.TrimSpace(state.Config.Network.Subnet)
	}
	vsockPath := ""
	if needsVsock(&state.Config) {
		// A workspace started from a snapshot runs a VM whose vsock device
		// still references its ancestor's baked path (resolved through the
		// fork's bind mount). The manifest must carry that baked path, not
		// this workspace's own — the next fork's bind mount targets it.
		if baked := strings.TrimSpace(state.Config.BakedVsockUDSPath); baked != "" {
			vsockPath = baked
		} else {
			vsockPath = vsockSocketPath(opts)
			if firecrackerProcessConfinedToWorkspace(state.PID, opts) {
				var err error
				vsockPath, err = confinedWorkspacePath(opts, vsockPath)
				if err != nil {
					return vmkit.SnapshotManifest{}, err
				}
			}
		}
	}
	// Same for the guest service ports: a fork's guest listens on the baked
	// (ancestor-derived) ports recorded in GuestShellPort/GuestExecPort, while
	// Config.ShellPort/ExecPort are this workspace's host-side bridge ports.
	shellPort := state.Config.ShellPort
	if state.Config.GuestShellPort != 0 {
		shellPort = state.Config.GuestShellPort
	}
	execPort := state.Config.ExecPort
	if state.Config.GuestExecPort != 0 {
		execPort = state.Config.GuestExecPort
	}
	// Capture the egress posture so a restore/fork re-arms the mediator with the
	// recorded policy AND reuses the SAME per-workspace CA the guest's baked trust
	// store was built against. Only certificate-forging modes mint a CA — broker
	// splices and delivers none — so only for those must the persisted CA cert
	// exist; record its DER SHA-256 as the restore-time integrity check. Fail
	// closed if the cert is gone — never snapshot a forging workspace whose CA
	// cannot be reproduced, because restoring it would silently break every MITM
	// handshake of the guest.
	caSHA := ""
	if vmkit.EgressModeForgesCerts(state.Config.EgressMode) && vmkit.NetworkModeMediates(mode) {
		sha, err := egressCACertSHA256(filepath.Join(opts.StateDir, opts.Name))
		if err != nil {
			return vmkit.SnapshotManifest{}, fmt.Errorf("snapshot of mediated workspace %s requires its persisted egress CA: %w", opts.Name, err)
		}
		caSHA = sha
	}
	return vmkit.SnapshotManifest{
		Tag:                   tag,
		SourceSessionID:       state.Event.Identity.SessionID,
		NetworkMode:           mode,
		GuestIP:               guestIP,
		KernelSHA256:          kernelSHA,
		VCPUCount:             state.Config.CPUCount,
		MemoryMiB:             state.Config.MemoryMiB,
		CreatedAt:             time.Now().UTC().Format(time.RFC3339),
		VsockUDSPath:          vsockPath,
		ShellPort:             shellPort,
		ExecPort:              execPort,
		NetworkIP:             netIP,
		NetworkGateway:        netGateway,
		NetworkSubnet:         netSubnet,
		RootfsArtifact:        vmkit.SnapshotRootfsName,
		MachineStateArtifacts: vmkit.FirecrackerSnapshotArtifacts(),
		SecretsMaterialized:   vmkit.MaterializedSecretsDeclared(&state.Config),
		SecretsPurged:         purged,
		EgressMode:            state.Config.EgressMode,
		EgressAllow:           state.Config.EgressAllow,
		EgressPassthrough:     state.Config.EgressPassthrough,
		EgressSwapConfigPath:  state.Config.EgressSwapConfigPath,
		EgressCASHA256:        caSHA,
		// Bounded-operations caps (ASK tenet 8): persist so a restore re-applies the
		// SAME bounds the workspace ran under.
		EgressMaxBytesPerSec:     state.Config.EgressMaxBytesPerSec,
		EgressMaxTotalBytes:      state.Config.EgressMaxTotalBytes,
		EgressMaxConcurrentConns: state.Config.EgressMaxConcurrentConns,
		EgressAuditMaxBytes:      state.Config.EgressAuditMaxBytes,
		EgressAuditMaxBackups:    state.Config.EgressAuditMaxBackups,
	}, nil
}

// egressCACertSHA256 reads the persisted per-workspace egress CA certificate
// (egress-ca.pem) from the workspace directory, PEM-decodes it, and returns the
// hex SHA-256 of the certificate's DER bytes. This is the stable fingerprint
// snapshot records and restore verifies before reusing the CA. It errors if the
// cert file is absent or not a well-formed CERTIFICATE PEM block.
func egressCACertSHA256(wsDir string) (string, error) {
	pemBytes, err := os.ReadFile(filepath.Join(wsDir, "egress-ca.pem"))
	if err != nil {
		return "", fmt.Errorf("read egress CA cert: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("egress CA cert at %s is not a valid CERTIFICATE PEM", wsDir)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// firecrackerLaunchCommand builds the command that launches Firecracker. A
// normal boot or a resume-in-place launches Firecracker directly; a fork (the
// snapshot's baked vsock path belongs to another workspace) launches it through
// unshare in a user+mount namespace that bind-mounts the fork's directory over
// the source's so the baked vsock path resolves to the fork's socket.
func firecrackerLaunchCommand(ctx context.Context, opts Options, req vmkit.Request, firecracker string, launchArgs []string, loadMode bool) (*exec.Cmd, error) {
	if loadMode {
		manifest, err := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag))
		if err != nil {
			return nil, fmt.Errorf("read snapshot manifest for load: %w", err)
		}
		if src, dst, need := snapshotForkBind(opts, manifest); need {
			unsharePath, err := exec.LookPath("unshare")
			if err != nil {
				return nil, fmt.Errorf("snapshot fork requires the unshare binary (util-linux): %w", err)
			}
			supervisor, err := os.Executable()
			if err != nil {
				return nil, fmt.Errorf("resolve supervisor executable for fork: %w", err)
			}
			// A host-side fork creates a fresh user+mount namespace
			// (--map-root-user keeps /dev/kvm reachable). A user-networked fork
			// already runs as root inside pasta's namespace, so it only needs a
			// nested mount namespace; mapping root again would shadow pasta's
			// network setup.
			mapRoot := !insideUserNetworkNamespace()
			return exec.CommandContext(ctx, unsharePath, forkMountExecArgs(mapRoot, supervisor, src, dst, firecracker, launchArgs)...), nil
		}
	}
	if mode, err := resolveConfinementMode(opts); err != nil {
		return nil, fmt.Errorf("resolve confinement: %w", err)
	} else if mode != confinementOff {
		return confinedLaunchCommand(ctx, opts, req, firecracker, launchArgs)
	}
	return exec.CommandContext(ctx, firecracker, launchArgs...), nil
}

// confinedLaunchCommand builds the launch for a confined workspace: it stages
// the static artifacts into the jail and returns an unshare command that runs
// the supervisor's --confined-exec handler, which jails and execs Firecracker.
// Firecracker sees workspace files through the workspace dir bound at /run, so
// caller-provided launch paths are translated from host workspace paths to /run.
func confinedLaunchCommand(ctx context.Context, opts Options, req vmkit.Request, firecracker string, launchArgs []string) (*exec.Cmd, error) {
	layout := confinedJailLayout(opts, req.Config, firecracker)
	if err := stageJailArtifacts(layout); err != nil {
		return nil, fmt.Errorf("stage confined jail: %w", err)
	}
	unsharePath, err := exec.LookPath("unshare")
	if err != nil {
		return nil, fmt.Errorf("confinement requires the unshare binary (util-linux): %w", err)
	}
	supervisor, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve supervisor executable for confinement: %w", err)
	}
	// Already root inside pasta's user namespace? Don't map root again (it would
	// shadow pasta's network setup) — only add the nested mount namespace.
	mapRoot := !insideUserNetworkNamespace()
	workDir := filepath.Join(opts.StateDir, opts.Name)
	confinedArgs, err := confinedLaunchArgs(opts, launchArgs)
	if err != nil {
		return nil, err
	}
	args := confinedExecArgs(mapRoot, supervisor, layout.Root, workDir, layout.Firecracker.Guest, confinedArgs)
	return exec.CommandContext(ctx, unsharePath, args...), nil
}

func confinedLaunchArgs(opts Options, launchArgs []string) ([]string, error) {
	args := make([]string, 0, len(launchArgs))
	for i := 0; i < len(launchArgs); i++ {
		arg := launchArgs[i]
		args = append(args, arg)
		if (arg == "--api-sock" || arg == "--config-file") && i+1 < len(launchArgs) {
			i++
			path, err := confinedWorkspacePath(opts, launchArgs[i])
			if err != nil {
				return nil, err
			}
			args = append(args, path)
		}
	}
	return args, nil
}

// snapshotForkBind reports whether loading this snapshot into the given
// workspace is a fork — i.e. the snapshot's baked vsock UDS path lives in a
// different workspace directory than this one. Firecracker cannot remap the
// vsock path on load, so a fork launches in a mount namespace that bind-mounts
// its own directory (dst) over the source's (src) to make the baked path
// resolve to the fork's socket. Resume-in-place returns needed=false.
func snapshotForkBind(opts Options, manifest vmkit.SnapshotManifest) (src, dst string, needed bool) {
	baked := strings.TrimSpace(manifest.VsockUDSPath)
	if baked == "" {
		return "", "", false
	}
	if clean := filepath.Clean(baked); clean == "/run" || strings.HasPrefix(clean, "/run/") {
		return "", "", false
	}
	src = filepath.Dir(baked)
	dst = filepath.Join(opts.StateDir, opts.Name)
	if src == dst {
		return "", "", false
	}
	return src, dst, true
}

// forkMountExecArgs builds the argv passed to the unshare binary that launches
// Firecracker inside a mount namespace with the fork's directory bind-mounted
// over the source's. mapRoot adds --map-root-user to create a user namespace
// (so /dev/kvm stays reachable) for a host-side fork; a user-networked fork is
// already root inside pasta's namespace and only needs the mount namespace. The
// supervisor's --fork-mount-exec handler performs the bind and execs Firecracker.
func forkMountExecArgs(mapRoot bool, supervisor, src, dst, firecracker string, launchArgs []string) []string {
	args := unshareJailNamespaceFlags(mapRoot)
	args = append(args,
		supervisor, "--fork-mount-exec",
		"--bind-src", src,
		"--bind-dst", dst,
		"--", firecracker,
	)
	return append(args, launchArgs...)
}

// RunForkMountExec is the re-exec entry point launched via unshare inside a new
// user+mount namespace. It bind-mounts bindDst over bindSrc so Firecracker's
// baked vsock path resolves to the fork's socket, then execs Firecracker
// (replacing this process) with the remaining arguments.
func RunForkMountExec(args []string) error {
	bindSrc, bindDst := "", ""
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--bind-src":
			if i+1 >= len(args) {
				return fmt.Errorf("--bind-src requires a value")
			}
			bindSrc = args[i+1]
			i += 2
		case "--bind-dst":
			if i+1 >= len(args) {
				return fmt.Errorf("--bind-dst requires a value")
			}
			bindDst = args[i+1]
			i += 2
		case "--":
			i++
			goto exec
		default:
			return fmt.Errorf("unexpected fork-mount-exec argument %q", args[i])
		}
	}
exec:
	rest := args[i:]
	if bindSrc == "" || bindDst == "" || len(rest) == 0 {
		return fmt.Errorf("usage: --fork-mount-exec --bind-src <dir> --bind-dst <dir> -- <firecracker> [args...]")
	}
	// The baked path's directory may not exist on this host: a chain fork's
	// ancestor workspace, or a bundle restored onto a fresh node. It is only
	// a mountpoint — create it before binding over it.
	if err := os.MkdirAll(bindSrc, 0o700); err != nil {
		return fmt.Errorf("create fork bind mountpoint %s: %w", bindSrc, err)
	}
	if err := unix.Mount(bindDst, bindSrc, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind-mount %s over %s for fork: %w", bindDst, bindSrc, err)
	}
	if err := unix.Exec(rest[0], rest, os.Environ()); err != nil {
		return fmt.Errorf("exec firecracker for fork: %w", err)
	}
	return nil
}

// restoreFromSnapshot waits for the freshly launched Firecracker API socket and
// loads the snapshot's vmstate and memory into it, resuming the guest. The
// rootfs has already been put in place by prepareSnapshotRestore.
func restoreFromSnapshot(ctx context.Context, opts Options, tag string, firecrackerPID int, networkOverrides []networkOverride) error {
	sock := apiSocketPath(opts)
	if err := waitForAPISocket(ctx, sock, 10*time.Second); err != nil {
		return err
	}
	dir := vmkit.SnapshotDir(opts.StateDir, opts.Name, tag)
	manifest, err := vmkit.ReadSnapshotManifest(dir)
	if err != nil {
		return err
	}
	vmstate, memory := firecrackerSnapshotArtifactPaths(manifest)
	vmstatePath := filepath.Join(dir, vmstate)
	memoryPath := filepath.Join(dir, memory)
	vmstateAPIPath, memoryAPIPath, err := snapshotAPIPaths(opts, firecrackerProcessConfinedToWorkspace(firecrackerPID, opts), vmstatePath, memoryPath)
	if err != nil {
		return err
	}
	return newVMStateController(sock).loadSnapshot(ctx,
		vmstateAPIPath,
		memoryAPIPath,
		true, networkOverrides)
}

// restoreLivenessWait bounds how long start --from-snapshot blocks after
// PUT /snapshot/load succeeds, waiting for the resumed guest to prove it is
// actually alive before the workspace is reported running. A var, not a
// const, so tests can shrink it.
var restoreLivenessWait = 5 * time.Second

// restoreLivenessPoll is how often waitForRestoreLiveness re-checks process,
// serial, and exec state while the window is open.
const restoreLivenessPoll = 100 * time.Millisecond

// waitForRestoreLiveness blocks until the guest resumed by restoreFromSnapshot
// proves it survived the load, or fails closed if it cannot prove that within
// the window. The kernel boots with panic=1 reboot=k (see config_linux.go),
// so a guest that panics immediately after resume reboots and Firecracker
// exits within a second or two - detachedStartExitError catches that.
// GuestHalted is a second, cheaper signal for the same crash caught before
// the process has finished exiting. But neither of those firing is required
// for the restore to fail: the exec service answering is the only accepted
// proof the guest is alive, and the window elapsing without it is itself a
// failure, not a pass - a guest that hasn't visibly died yet is not the same
// as a guest that is known to be alive. That holds even when no exec port is
// configured: with no probe available to supply positive proof, the restore
// cannot be verified and is treated the same as one that timed out waiting
// for an answer.
func waitForRestoreLiveness(ctx context.Context, cmd *exec.Cmd, serialPath string, execPort uint16) error {
	deadline := time.Now().Add(restoreLivenessWait)
	for {
		if err := detachedStartExitError(cmd, 0); err != nil {
			return fmt.Errorf("guest did not survive snapshot resume: %w", err)
		}
		if GuestHalted(serialPath) {
			return fmt.Errorf("guest halted immediately after snapshot resume")
		}
		if execPort != 0 && restoreExecProbe(ctx, execPort) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("guest liveness unverified after snapshot resume: no exec probe answered within %s", restoreLivenessWait)
		}
		select {
		case <-time.After(restoreLivenessPoll):
		case <-ctx.Done():
			return fmt.Errorf("snapshot resume liveness check canceled: %w", ctx.Err())
		}
	}
}

// restoreExecProbe reports whether the guest structured-exec service answers
// a trivial command, the same round-trip execReadinessFromRuntimeState uses
// to report exec readiness once a workspace is already running.
func restoreExecProbe(ctx context.Context, execPort uint16) bool {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(execPort)))
	req := execprotocol.NewExecRequest([]string{"true"})
	req.TimeoutMS = 500
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	result, err := execclient.New(target).Exec(probeCtx, req)
	if err != nil || result.Error != nil {
		return false
	}
	return result.Status == execprotocol.ExecStatusExited && result.ExitCode != nil && *result.ExitCode == 0
}

func snapshotAPIPaths(opts Options, confined bool, vmstatePath, memoryPath string) (string, string, error) {
	vmstateAPIPath, err := snapshotAPIPath(opts, confined, vmstatePath)
	if err != nil {
		return "", "", err
	}
	memoryAPIPath, err := snapshotAPIPath(opts, confined, memoryPath)
	if err != nil {
		return "", "", err
	}
	return vmstateAPIPath, memoryAPIPath, nil
}

func snapshotAPIPath(opts Options, confined bool, hostPath string) (string, error) {
	if !confined {
		return hostPath, nil
	}
	return confinedWorkspacePath(opts, hostPath)
}

func confinedWorkspacePath(opts Options, hostPath string) (string, error) {
	workspaceDir := filepath.Clean(filepath.Join(opts.StateDir, opts.Name))
	rel, err := filepath.Rel(workspaceDir, filepath.Clean(hostPath))
	if err != nil {
		return "", fmt.Errorf("translate confined firecracker path %s: %w", hostPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("confined firecracker path %s is outside workspace dir %s", hostPath, workspaceDir)
	}
	if rel == "." {
		return "/run", nil
	}
	return filepath.Join("/run", rel), nil
}

func firecrackerSnapshotArtifactPaths(manifest vmkit.SnapshotManifest) (vmstate, memory string) {
	vmstate = vmkit.SnapshotVMStateName
	memory = vmkit.SnapshotMemoryName
	for _, artifact := range vmkit.SnapshotMachineStateArtifacts(manifest) {
		switch artifact.Kind {
		case "firecracker-vmstate":
			if artifact.Path != "" {
				vmstate = artifact.Path
			}
		case "firecracker-memory":
			if artifact.Path != "" {
				memory = artifact.Path
			}
		}
	}
	return vmstate, memory
}

// snapshotNetworkOverrides remaps the restored guest network interface to this
// workspace's tap. The tap name is derived from the workspace name, so a fork
// (a different name) must remap the snapshot's baked tap to its own; for
// resume-in-place the name matches and the override is a no-op. Returns nil for
// workspaces without a host network interface (isolated mode).
func snapshotNetworkOverrides(opts Options, config *vmkit.Config) []networkOverride {
	iface, ok := firecrackerNetworkInterface(opts, config)
	if !ok {
		return nil
	}
	return []networkOverride{{IfaceID: iface.IfaceID, HostDevName: iface.HostDevName}}
}

// waitForAPISocket polls the Firecracker API unix socket until it accepts a
// connection, the context is cancelled, or the timeout elapses.
func waitForAPISocket(ctx context.Context, sock string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("firecracker api socket %s not ready after %s: %w", sock, timeout, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// prepareSnapshotRestore validates that a snapshot can be loaded into this
// workspace and rolls the workspace rootfs back to the snapshot's coherent
// copy. It rejects a kernel mismatch (the snapshot is bound to the kernel it
// was taken against). It runs once on the host before any user-network
// namespace re-exec.
func prepareSnapshotRestore(opts Options, req vmkit.Request) error {
	dir := vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag)
	manifest, err := vmkit.ReadSnapshotManifest(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot %q not found for workspace %s", req.Tag, opts.Name)
		}
		return err
	}
	if manifest.KernelSHA256 != "" {
		sha, err := fileSHA256(req.Config.KernelPath)
		if err != nil {
			return fmt.Errorf("hash kernel for snapshot restore: %w", err)
		}
		if sha != manifest.KernelSHA256 {
			return fmt.Errorf("snapshot %q was taken against kernel sha256 %s but the workspace kernel is %s; refusing to load", req.Tag, manifest.KernelSHA256, sha)
		}
	}
	if err := vmkit.ValidateSnapshotSecretRestore(manifest, req.Config); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(dir, vmkit.SnapshotRootfsArtifact(manifest)), req.Config.RootfsPath); err != nil {
		return fmt.Errorf("restore snapshot rootfs: %w", err)
	}
	// Restore the captured config disk beside the rootfs. A snapshot taken
	// before config disks existed has none — its vmstate expects no config
	// device either, so absence is legitimate, not an error.
	captured := filepath.Join(dir, vmkit.SnapshotConfigDiskName)
	if _, err := os.Stat(captured); err == nil && req.Config.ConfigDiskPath != "" {
		if err := copyFile(captured, req.Config.ConfigDiskPath); err != nil {
			return fmt.Errorf("restore snapshot config disk: %w", err)
		}
	}
	return nil
}

// guestIPFromNetwork returns the bare guest IP (no CIDR suffix) recorded for a
// running workspace, preferring the live runtime network when present.
func guestIPFromNetwork(network vmkit.NetworkConfig) string {
	ip := strings.TrimSpace(network.IP)
	if ip == "" && network.Runtime != nil {
		ip = strings.TrimSpace(network.Runtime.IP)
	}
	if ip == "" {
		return ""
	}
	if host, _, err := net.ParseCIDR(ip); err == nil {
		return host.String()
	}
	return ip
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if err := cloneFile(in, out); err == nil {
		return out.Close()
	}
	if err := out.Truncate(info.Size()); err != nil {
		_ = out.Close()
		return err
	}
	if err := copyFileSparse(in, out, info.Size()); err != nil {
		if !isSparseSeekUnsupported(err) {
			_ = out.Close()
			return err
		}
		if _, err := in.Seek(0, io.SeekStart); err != nil {
			_ = out.Close()
			return err
		}
		if _, err := out.Seek(0, io.SeekStart); err != nil {
			_ = out.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
	}
	return out.Close()
}

func cloneFile(in, out *os.File) error {
	return unix.IoctlSetInt(int(out.Fd()), unix.FICLONE, int(in.Fd()))
}

// errCopyRangeUnsupported signals that copy_file_range cannot service a request
// (old kernel, cross-filesystem, or a filesystem that does not implement it), so
// the caller falls back to a userspace copy for that extent.
var errCopyRangeUnsupported = errors.New("copy_file_range unsupported")

// copyRange copies length bytes at off from in to out with copy_file_range(2),
// looping until the extent is done because the syscall may copy less than asked.
// It reports errCopyRangeUnsupported only when nothing was copied, so a partial
// copy is never silently retried on top of itself.
func copyRange(in, out *os.File, off, length int64) error {
	inOff, outOff := off, off
	copied := int64(0)
	for copied < length {
		n, err := unix.CopyFileRange(int(in.Fd()), &inOff, int(out.Fd()), &outOff, int(length-copied), 0)
		if err != nil {
			if copied == 0 {
				switch err {
				case unix.ENOSYS, unix.EXDEV, unix.EINVAL, unix.EOPNOTSUPP, unix.EPERM, unix.EBADF:
					return errCopyRangeUnsupported
				}
			}
			if err == unix.EINTR {
				continue
			}
			return err
		}
		if n == 0 {
			if copied == 0 {
				return errCopyRangeUnsupported
			}
			return io.ErrUnexpectedEOF
		}
		copied += int64(n)
	}
	return nil
}

func copyFileSparse(in, out *os.File, size int64) error {
	if size == 0 {
		return nil
	}
	inFD := int(in.Fd())
	offset := int64(0)
	for offset < size {
		data, err := unix.Seek(inFD, offset, unix.SEEK_DATA)
		if err != nil {
			if err == unix.ENXIO {
				return nil
			}
			return err
		}
		if data >= size {
			return nil
		}
		hole, err := unix.Seek(inFD, data, unix.SEEK_HOLE)
		if err != nil {
			return err
		}
		if hole > size {
			hole = size
		}
		// Kernel-side copy first: the guest is PAUSED for the whole of this
		// copy, so every microsecond here is stop-the-world for the workload.
		// copy_file_range keeps the extent in the kernel instead of bouncing
		// each block through a userspace buffer. Reflink (cloneFile) already
		// makes this instant on btrfs/XFS; this is the path ext4 takes, which
		// is what the fleet runs on.
		if err := copyRange(in, out, data, hole-data); err != nil {
			if !errors.Is(err, errCopyRangeUnsupported) {
				return err
			}
			if _, err := in.Seek(data, io.SeekStart); err != nil {
				return err
			}
			if _, err := out.Seek(data, io.SeekStart); err != nil {
				return err
			}
			if _, err := io.CopyN(out, in, hole-data); err != nil {
				return err
			}
		}
		offset = hole
	}
	return nil
}

func isSparseSeekUnsupported(err error) bool {
	return err == unix.EINVAL || err == unix.ENOTTY || err == unix.EOPNOTSUPP
}
