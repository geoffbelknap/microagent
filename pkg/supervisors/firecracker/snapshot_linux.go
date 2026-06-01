package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
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

	if err := writeSnapshotArtifacts(ctx, controller, state, dir, req.Tag); err != nil {
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
	}
	return eventResponse(req, finalState, ""), nil
}

// writeSnapshotState persists a transient pause/resume around a snapshot while
// preserving the host-side aux processes so the workspace keeps working.
func writeSnapshotState(opts Options, req vmkit.Request, state runtimeState, target vmkit.VMState) error {
	return writeProcessStateWithProcessesAndNetwork(opts, runtimeStateRequest(req, state), target, state.PID, state.PortForwardPID, state.VsockListenerPID, state.NetworkDevices, state.FirewallRules, "")
}

func writeSnapshotArtifacts(ctx context.Context, controller vmStateController, state runtimeState, dir, tag string) error {
	if err := controller.createSnapshot(ctx, filepath.Join(dir, vmkit.SnapshotVMStateName), filepath.Join(dir, vmkit.SnapshotMemoryName)); err != nil {
		return err
	}
	if err := copyFile(state.Config.RootfsPath, filepath.Join(dir, vmkit.SnapshotRootfsName)); err != nil {
		return fmt.Errorf("copy rootfs into snapshot: %w", err)
	}
	manifest, err := snapshotManifestFromState(tag, state)
	if err != nil {
		return err
	}
	return vmkit.WriteSnapshotManifest(dir, manifest)
}

func snapshotManifestFromState(tag string, state runtimeState) (vmkit.SnapshotManifest, error) {
	kernelSHA := ""
	if path := strings.TrimSpace(state.Config.KernelPath); path != "" {
		sha, err := fileSHA256(path)
		if err != nil {
			return vmkit.SnapshotManifest{}, fmt.Errorf("hash kernel for snapshot: %w", err)
		}
		kernelSHA = sha
	}
	mode, guestIP := "", ""
	if state.Config.Network != nil {
		mode = strings.TrimSpace(state.Config.Network.Mode)
		guestIP = guestIPFromNetwork(*state.Config.Network)
	}
	return vmkit.SnapshotManifest{
		Tag:          tag,
		NetworkMode:  mode,
		GuestIP:      guestIP,
		KernelSHA256: kernelSHA,
		VCPUCount:    state.Config.CPUCount,
		MemoryMiB:    state.Config.MemoryMiB,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}, nil
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
