package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
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
	if current != vmkit.StateRunning && current != vmkit.StatePaused {
		err := fmt.Errorf("firecracker workspace %s is %s; snapshot requires a running or paused workspace", opts.Name, current)
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

	dir := vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return vmkit.Response{}, err
	}

	// Purge materialized secrets from the guest tmpfs while the VM is still
	// running (before the auto-pause below), so the captured memory holds zeros.
	// Fail closed: a snapshot of a secrets-bearing workspace is never created
	// with un-purged plaintext.
	purged := false
	if materializedSecretsDeclared(&state.Config) && state.Config.SecretsControlPort != 0 {
		if current != vmkit.StateRunning {
			err := fmt.Errorf("cannot purge secrets for snapshot: workspace %s is %s, must be running", opts.Name, current)
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
	autoPaused := current == vmkit.StateRunning
	if autoPaused {
		if err := controller.patchVMState(ctx, "Paused"); err != nil {
			_ = os.RemoveAll(dir)
			return failedResponse(req, err.Error()), err
		}
		if err := writeSnapshotState(opts, req, state, vmkit.StatePaused); err != nil {
			_ = controller.patchVMState(ctx, "Resumed")
			_ = os.RemoveAll(dir)
			return vmkit.Response{}, err
		}
	}

	if err := writeSnapshotArtifacts(ctx, controller, opts, state, dir, req.Tag, purged); err != nil {
		if autoPaused {
			_ = controller.patchVMState(ctx, "Resumed")
			_ = writeSnapshotState(opts, req, state, vmkit.StateRunning)
		}
		_ = os.RemoveAll(dir)
		return failedResponse(req, err.Error()), err
	}

	finalState := vmkit.StatePaused
	if autoPaused {
		if err := controller.patchVMState(ctx, "Resumed"); err != nil {
			return failedResponse(req, err.Error()), err
		}
		finalState = vmkit.StateRunning
		if err := writeSnapshotState(opts, req, state, vmkit.StateRunning); err != nil {
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

// writeSnapshotState persists a transient pause/resume around a snapshot while
// preserving the host-side aux processes so the workspace keeps working.
func writeSnapshotState(opts Options, req vmkit.Request, state runtimeState, target vmkit.VMState) error {
	return writeProcessStateWithProcessesAndNetwork(opts, runtimeStateRequest(req, state), target, state.PID, state.PortForwardPID, state.VsockListenerPID, state.NetworkDevices, state.FirewallRules, "")
}

func writeSnapshotArtifacts(ctx context.Context, controller vmStateController, opts Options, state runtimeState, dir, tag string, purged bool) error {
	if err := controller.createSnapshot(ctx, filepath.Join(dir, vmkit.SnapshotVMStateName), filepath.Join(dir, vmkit.SnapshotMemoryName)); err != nil {
		return err
	}
	if err := copyFile(state.Config.RootfsPath, filepath.Join(dir, vmkit.SnapshotRootfsName)); err != nil {
		return fmt.Errorf("copy rootfs into snapshot: %w", err)
	}
	manifest, err := snapshotManifestFromState(tag, state, opts, purged)
	if err != nil {
		return err
	}
	return vmkit.WriteSnapshotManifest(dir, manifest)
}

func snapshotManifestFromState(tag string, state runtimeState, opts Options, purged bool) (vmkit.SnapshotManifest, error) {
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
		vsockPath = vsockSocketPath(opts)
	}
	return vmkit.SnapshotManifest{
		Tag:            tag,
		NetworkMode:    mode,
		GuestIP:        guestIP,
		KernelSHA256:   kernelSHA,
		VCPUCount:      state.Config.CPUCount,
		MemoryMiB:      state.Config.MemoryMiB,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		VsockUDSPath:   vsockPath,
		ShellPort:      state.Config.ShellPort,
		ExecPort:       state.Config.ExecPort,
		NetworkIP:      netIP,
		NetworkGateway: netGateway,
		NetworkSubnet:  netSubnet,
		SecretsPurged:  purged,
	}, nil
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
	return exec.CommandContext(ctx, firecracker, launchArgs...), nil
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
	args := []string{}
	if mapRoot {
		args = append(args, "--map-root-user")
	}
	args = append(args,
		"--mount",
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
func restoreFromSnapshot(ctx context.Context, opts Options, tag string, networkOverrides []networkOverride) error {
	sock := apiSocketPath(opts)
	if err := waitForAPISocket(ctx, sock, 10*time.Second); err != nil {
		return err
	}
	dir := vmkit.SnapshotDir(opts.StateDir, opts.Name, tag)
	return newVMStateController(sock).loadSnapshot(ctx,
		filepath.Join(dir, vmkit.SnapshotVMStateName),
		filepath.Join(dir, vmkit.SnapshotMemoryName),
		true, networkOverrides)
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
// was taken against) and bridged networking (a fork would collide on the shared
// L2; restore shares the rejection for a single, predictable contract). It runs
// once on the host before any user-network namespace re-exec.
func prepareSnapshotRestore(opts Options, req vmkit.Request) error {
	dir := vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag)
	manifest, err := vmkit.ReadSnapshotManifest(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot %q not found for workspace %s", req.Tag, opts.Name)
		}
		return err
	}
	if networkMode(req.Config) == "bridged" {
		return fmt.Errorf("snapshot restore does not support bridged networking; use user, nat, or isolated")
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
	if err := copyFile(filepath.Join(dir, vmkit.SnapshotRootfsName), req.Config.RootfsPath); err != nil {
		return fmt.Errorf("restore snapshot rootfs: %w", err)
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
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
