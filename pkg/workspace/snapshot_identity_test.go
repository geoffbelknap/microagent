package workspace

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestAdoptSnapshotIdentity pins the resume-in-place identity rules: baked
// guest ports and vsock path default from the snapshot manifest (a fork
// resumed in place must bridge to the ports its guest actually listens on),
// and explicit caller values always win.
func TestAdoptSnapshotIdentity(t *testing.T) {
	manifest := vmkit.SnapshotManifest{
		ShellPort:    31001,
		ExecPort:     31002,
		VsockUDSPath: "/state/mp-agent/vsock.sock",
	}

	var opts Options
	adoptSnapshotIdentity(&opts, manifest)
	if opts.GuestShellPort != 31001 || opts.GuestExecPort != 31002 {
		t.Fatalf("guest ports = %d/%d, want 31001/31002", opts.GuestShellPort, opts.GuestExecPort)
	}
	if opts.BakedVsockUDSPath != "/state/mp-agent/vsock.sock" {
		t.Fatalf("BakedVsockUDSPath = %q", opts.BakedVsockUDSPath)
	}

	explicit := Options{GuestShellPort: 41001, GuestExecPort: 41002, BakedVsockUDSPath: "/elsewhere/vsock.sock"}
	adoptSnapshotIdentity(&explicit, manifest)
	if explicit.GuestShellPort != 41001 || explicit.GuestExecPort != 41002 || explicit.BakedVsockUDSPath != "/elsewhere/vsock.sock" {
		t.Fatalf("explicit values must win: %+v", explicit)
	}
}
