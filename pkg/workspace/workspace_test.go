package workspace

import (
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestRequestBuildsBackendNeutralWorkspaceRequest(t *testing.T) {
	opts := Options{
		Name:           "agent-1",
		Backend:        vmkit.BackendFirecracker,
		KernelPath:     "/kernels/Image",
		StateDir:       t.TempDir(),
		MemoryMiB:      512,
		CPUCount:       2,
		ResultPort:     1024,
		SerialInput:    true,
		Network:        vmkit.NetworkConfig{Mode: "nat"},
		VsockListeners: []vmkit.VsockListener{{Port: 2048, Target: "/tmp/service.sock"}},
		Disks: []Disk{{
			Name:       "work",
			Path:       "/tmp/work.ext4",
			Mountpoint: "/work",
			Mode:       "rw",
		}},
	}

	req := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")

	if req.Command != "run" {
		t.Fatalf("Command = %q", req.Command)
	}
	if req.Identity == nil || req.Identity.RequestID != "req-1" || req.Identity.RuntimeID != "agent-1" {
		t.Fatalf("Identity = %#v", req.Identity)
	}
	if req.Config == nil {
		t.Fatal("Config is nil")
	}
	if req.Config.RootfsPath != "/tmp/rootfs.ext4" || req.Config.KernelPath != "/kernels/Image" {
		t.Fatalf("Config paths = %#v", req.Config)
	}
	if len(req.Config.VsockListeners) != 2 {
		t.Fatalf("VsockListeners = %#v", req.Config.VsockListeners)
	}
	if req.Config.VsockListeners[0].Target != filepath.Join(opts.StateDir, opts.Name, "result.json") {
		t.Fatalf("result listener = %#v", req.Config.VsockListeners[0])
	}
	if len(req.Config.Disks) != 1 || req.Config.Disks[0].Mountpoint != "/work" {
		t.Fatalf("Disks = %#v", req.Config.Disks)
	}
	if req.Config.Network == nil || req.Config.Network.Mode != "nat" {
		t.Fatalf("Network = %#v", req.Config.Network)
	}
	if !req.Config.SerialInput {
		t.Fatal("SerialInput = false")
	}
}

func TestApplyProfileAndRestartValidation(t *testing.T) {
	opts := Options{Profile: "tiny"}
	if err := ApplyProfile(&opts, false, false, false); err != nil {
		t.Fatalf("ApplyProfile: %v", err)
	}
	if opts.MemoryMiB != 256 || opts.CPUCount != 1 || opts.SizeMiB != 512 {
		t.Fatalf("resources = %#v", ResourcesFromOptions(opts))
	}
	if err := ValidateRestartPolicy("on-failure"); err != nil {
		t.Fatalf("ValidateRestartPolicy: %v", err)
	}
	if err := ValidateRestartPolicy("sometimes"); err == nil {
		t.Fatal("ValidateRestartPolicy accepted invalid policy")
	}
}
