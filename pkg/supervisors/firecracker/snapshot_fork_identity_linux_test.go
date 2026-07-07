package firecracker

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// forkRuntimeState builds a minimal runtimeState for a workspace that was
// itself started from a snapshot: host-side bridge ports differ from the
// baked guest ports, and the loaded VM references its ancestor's vsock path.
func forkRuntimeState() runtimeState {
	return runtimeState{
		Config: vmkit.Config{
			CPUCount:          2,
			MemoryMiB:         512,
			ShellPort:         41001, // fork's host-side bridge ports
			ExecPort:          41002,
			GuestShellPort:    31001, // ancestor-derived ports the guest listens on
			GuestExecPort:     31002,
			BakedVsockUDSPath: "/var/lib/planed/state/microagent/mp-agent/vsock.sock",
			Network:           &vmkit.NetworkConfig{Mode: "user", IP: "10.43.1.2/29", Gateway: "10.43.1.1", Subnet: "10.43.1.0/29"},
		},
	}
}

// TestSnapshotManifestCarriesBakedForkIdentity proves that snapshotting a
// workspace that was started FROM a snapshot records the identity baked into
// the running VM — the ancestor's vsock UDS path and the guest service ports —
// not the fork's own host-side values. Recording the fork's own values breaks
// the NEXT restore in the chain: its bind mount targets the wrong directory
// and its port bridge targets ports nobody listens on.
func TestSnapshotManifestCarriesBakedForkIdentity(t *testing.T) {
	opts := Options{Name: "mp-agent-g2", StateDir: t.TempDir()}
	state := forkRuntimeState()

	manifest, err := snapshotManifestFromState("hib", state, opts, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState: %v", err)
	}
	if manifest.VsockUDSPath != state.Config.BakedVsockUDSPath {
		t.Errorf("VsockUDSPath = %q, want baked %q", manifest.VsockUDSPath, state.Config.BakedVsockUDSPath)
	}
	if manifest.ShellPort != 31001 || manifest.ExecPort != 31002 {
		t.Errorf("ports = %d/%d, want baked guest ports 31001/31002", manifest.ShellPort, manifest.ExecPort)
	}
}

// TestSnapshotManifestFreshWorkspaceKeepsOwnIdentity pins the original
// behavior: a workspace that booted fresh records its own vsock path and its
// host ports (which equal the guest ports in that case).
func TestSnapshotManifestFreshWorkspaceKeepsOwnIdentity(t *testing.T) {
	opts := Options{Name: "mp-agent", StateDir: t.TempDir()}
	state := forkRuntimeState()
	state.Config.GuestShellPort = 0
	state.Config.GuestExecPort = 0
	state.Config.BakedVsockUDSPath = ""

	manifest, err := snapshotManifestFromState("hib", state, opts, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState: %v", err)
	}
	if want := vsockSocketPath(opts); manifest.VsockUDSPath != want {
		t.Errorf("VsockUDSPath = %q, want own %q", manifest.VsockUDSPath, want)
	}
	if manifest.ShellPort != 41001 || manifest.ExecPort != 41002 {
		t.Errorf("ports = %d/%d, want own 41001/41002", manifest.ShellPort, manifest.ExecPort)
	}
}
