//go:build windows

package windows_hyperv

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

// snapshotSaveType is the HCS SaveType used to capture a snapshot. "ToFile"
// produces a plain restorable save-to-file (as opposed to "AsTemplate", which
// produces a fast-clone template). It is a variable so the value can be probed
// at runtime — set MICROAGENT_WINDOWS_HYPERV_SAVE_TYPE to override — if a host's
// HCS rejects "ToFile".
var snapshotSaveType = func() string {
	if v := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_SAVE_TYPE")); v != "" {
		return v
	}
	return "ToFile"
}()

// snapshot captures a full snapshot of a live workspace: the guest memory and
// device state (one HCS save-state file) plus a coherent copy of the rootfs VHD,
// taken while the compute system is paused. HCS requires the system be paused
// before a save, so a running workspace is auto-paused around the capture and
// resumed afterward (running -> paused -> running); an already-paused workspace
// is snapshotted in place and left paused. The save flushes guest I/O, so the
// VHD copy taken while paused is coherent with the captured memory.
//
// Fails closed on the wrong state: snapshot requires a running or paused
// workspace with a known compute system. On any artifact error the partial
// snapshot directory is removed and a running workspace is resumed, so a failed
// snapshot never leaves the workspace paused or a half-written snapshot behind.
func (s Supervisor) snapshot(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	current := state.Event.State
	if current != vmkit.StateRunning && current != vmkit.StatePaused {
		err := fmt.Errorf("windows-hyperv workspace %s is %s; snapshot requires a running or paused workspace", req.Identity.RuntimeID, current)
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if state.ComputeSystemID == "" {
		err := fmt.Errorf("windows-hyperv workspace %s has no compute system to snapshot", req.Identity.RuntimeID)
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if strings.TrimSpace(state.Config.RootfsPath) == "" {
		err := fmt.Errorf("windows-hyperv workspace %s has no recorded rootfs to snapshot", req.Identity.RuntimeID)
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}

	dir := vmkit.SnapshotDir(req.Config.StateDir, req.Identity.RuntimeID, req.Tag)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}

	adapter := s.runtimeAdapter()
	autoPaused := current == vmkit.StateRunning
	if autoPaused {
		if err := adapter.Pause(ctx, state.ComputeSystemID); err != nil {
			_ = os.RemoveAll(dir)
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
		}
		if err := s.writeSnapshotTransition(req, state, vmkit.StatePaused); err != nil {
			_ = adapter.Resume(ctx, state.ComputeSystemID)
			_ = os.RemoveAll(dir)
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
		}
	}

	if err := s.writeSnapshotArtifacts(ctx, adapter, state, dir, req); err != nil {
		if autoPaused {
			if resumeErr := adapter.Resume(ctx, state.ComputeSystemID); resumeErr == nil {
				_ = s.writeSnapshotTransition(req, state, vmkit.StateRunning)
			}
		}
		_ = os.RemoveAll(dir)
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}

	finalState := vmkit.StatePaused
	if autoPaused {
		if err := adapter.Resume(ctx, state.ComputeSystemID); err != nil {
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
		}
		finalState = vmkit.StateRunning
		if err := s.writeSnapshotTransition(req, state, vmkit.StateRunning); err != nil {
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
		}
	}
	event, err := eventFromFile(eventFile{
		Identity:   *req.Identity,
		State:      finalState,
		Detail:     "windows-hyperv snapshot " + req.Tag + " created",
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

// writeSnapshotTransition persists a transient pause/resume around a snapshot
// while preserving the compute IDs and the runtime listener PID, so the
// workspace keeps working after the snapshot. It carries the stored runtime
// config (a snapshot request is sparse), mirroring the pause/resume path.
func (s Supervisor) writeSnapshotTransition(req vmkit.Request, state runtimeState, target vmkit.VMState) error {
	runtimeReq := req
	storedConfig := state.Config
	storedConfig.StateDir = req.Config.StateDir
	runtimeReq.Config = &storedConfig
	_, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(runtimeReq, target, "windows-hyperv snapshot transition", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, state.VsockListenerPID)
	return err
}

// writeSnapshotArtifacts saves the guest memory/device state and copies the
// rootfs VHD into the snapshot directory while the workspace is paused, then
// writes the backend-neutral manifest. The compute system must already be
// paused.
func (s Supervisor) writeSnapshotArtifacts(ctx context.Context, adapter runtimeAdapter, state runtimeState, dir string, req vmkit.Request) error {
	vmStatePath := filepath.Join(dir, vmkit.SnapshotVMStateName)
	// The HCS VM worker process (vmwp) writes the save-state file under a
	// VM-specific identity that cannot write an arbitrary user directory, so
	// pre-create the target file and grant the compute system's runtime ID
	// access to it before saving — the same GrantVmAccess the rootfs VHD gets at
	// create. Without this the async save fails with "Access is denied".
	if state.ComputeSystemRuntimeID != "" {
		if f, err := os.OpenFile(vmStatePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600); err != nil {
			return fmt.Errorf("pre-create save-state file: %w", err)
		} else if err := f.Close(); err != nil {
			return fmt.Errorf("pre-create save-state file: %w", err)
		}
		if err := adapter.GrantAccess(ctx, state.ComputeSystemRuntimeID, vmStatePath); err != nil {
			return fmt.Errorf("grant vm access to save-state file: %w", err)
		}
	}
	if err := adapter.Save(ctx, state.ComputeSystemID, vmStatePath, snapshotSaveType); err != nil {
		return fmt.Errorf("save compute system state: %w", err)
	}
	if err := copySnapshotFile(state.Config.RootfsPath, filepath.Join(dir, vmkit.SnapshotRootfsVHDName)); err != nil {
		return fmt.Errorf("copy rootfs into snapshot: %w", err)
	}
	manifest, err := snapshotManifestFromState(req.Tag, state)
	if err != nil {
		return err
	}
	return vmkit.WriteSnapshotManifest(dir, manifest)
}

// snapshotManifestFromState builds the backend-neutral snapshot manifest from
// the recorded runtime state: the kernel hash that guards against load against a
// different kernel, the network mode and guest IP to re-establish, the VM
// sizing, and the guest vsock service ports a fork must adopt to reach the
// resumed guest.
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
	netIP, netGateway, netSubnet := "", "", ""
	if state.Config.Network != nil {
		mode = strings.TrimSpace(state.Config.Network.Mode)
		guestIP = guestIPFromNetwork(*state.Config.Network)
		netIP = strings.TrimSpace(state.Config.Network.IP)
		netGateway = strings.TrimSpace(state.Config.Network.Gateway)
		netSubnet = strings.TrimSpace(state.Config.Network.Subnet)
	}
	return vmkit.SnapshotManifest{
		Tag:            tag,
		NetworkMode:    mode,
		GuestIP:        guestIP,
		KernelSHA256:   kernelSHA,
		VCPUCount:      state.Config.CPUCount,
		MemoryMiB:      state.Config.MemoryMiB,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		ShellPort:      guestShellPort(state.Config),
		ExecPort:       guestExecPort(state.Config),
		NetworkIP:      netIP,
		NetworkGateway: netGateway,
		NetworkSubnet:  netSubnet,
	}, nil
}

// prepareSnapshotRestore validates that a snapshot can be loaded into this
// workspace and rolls the workspace rootfs back to the snapshot's coherent VHD
// copy. It rejects a kernel mismatch (the snapshot is bound to the kernel it was
// taken against) and bridged networking (a fork would collide on the shared L2;
// restore shares the rejection for a single, predictable contract). It returns
// the absolute path of the snapshot's vmstate file so the restore can build the
// compute-system document with VirtualMachine.RestoreState pointing at it.
func prepareSnapshotRestore(req vmkit.Request) (vmStatePath string, err error) {
	dir := vmkit.SnapshotDir(req.Config.StateDir, req.Identity.RuntimeID, req.Tag)
	manifest, err := vmkit.ReadSnapshotManifest(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("snapshot %q not found for workspace %s", req.Tag, req.Identity.RuntimeID)
		}
		return "", err
	}
	if req.Config.Network != nil && strings.TrimSpace(req.Config.Network.Mode) == "bridged" {
		return "", fmt.Errorf("snapshot restore does not support bridged networking; use user, nat, or isolated")
	}
	if manifest.KernelSHA256 != "" {
		sha, err := fileSHA256(req.Config.KernelPath)
		if err != nil {
			return "", fmt.Errorf("hash kernel for snapshot restore: %w", err)
		}
		if sha != manifest.KernelSHA256 {
			return "", fmt.Errorf("snapshot %q was taken against kernel sha256 %s but the workspace kernel is %s; refusing to load", req.Tag, manifest.KernelSHA256, sha)
		}
	}
	if err := copySnapshotFile(filepath.Join(dir, vmkit.SnapshotRootfsVHDName), req.Config.RootfsPath); err != nil {
		return "", fmt.Errorf("restore snapshot rootfs: %w", err)
	}
	return filepath.Join(dir, vmkit.SnapshotVMStateName), nil
}

// guestIPFromNetwork returns the bare guest IP (no CIDR suffix) recorded for a
// workspace, preferring the live runtime network when present.
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

func copySnapshotFile(source, target string) error {
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
