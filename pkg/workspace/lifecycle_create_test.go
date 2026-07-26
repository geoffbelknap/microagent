package workspace

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestWorkspaceRootfsPathUsesBackendFormat(t *testing.T) {
	tests := []struct {
		name       string
		backend    string
		wantSuffix string
		wantFormat string
	}{
		{
			name:       "linux-kvm",
			backend:    vmkit.BackendLinuxKVM,
			wantSuffix: filepath.Join("workspaces", "research", "rootfs.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "apple-vf",
			backend:    vmkit.BackendAppleVF,
			wantSuffix: filepath.Join("workspaces", "research", "rootfs.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath := WorkspaceRootfsPath("/tmp/microagent", "research", tt.backend)
			if !strings.HasSuffix(gotPath, tt.wantSuffix) {
				t.Fatalf("WorkspaceRootfsPath = %q, want suffix %q", gotPath, tt.wantSuffix)
			}
			req := buildRootfsRequest(Options{
				Name:         "research",
				StateDir:     "/tmp/microagent",
				Backend:      tt.backend,
				ImageRef:     "docker.io/library/ubuntu:24.04",
				Architecture: "arm64",
				SizeMiB:      1024,
			}, gotPath)
			if req.Format != tt.wantFormat {
				t.Fatalf("BuildRequest.Format = %q, want %q", req.Format, tt.wantFormat)
			}
		})
	}
}

func TestWorkspaceDiskPathUsesBackendFormat(t *testing.T) {
	tests := []struct {
		name       string
		backend    string
		wantSuffix string
		wantFormat string
	}{
		{
			name:       "linux-kvm",
			backend:    vmkit.BackendLinuxKVM,
			wantSuffix: filepath.Join("workspaces", "research", "disks", "work.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "apple-vf",
			backend:    vmkit.BackendAppleVF,
			wantSuffix: filepath.Join("workspaces", "research", "disks", "work.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath := WorkspaceDiskPath("/tmp/microagent", "research", tt.backend, "work")
			if !strings.HasSuffix(gotPath, tt.wantSuffix) {
				t.Fatalf("WorkspaceDiskPath = %q, want suffix %q", gotPath, tt.wantSuffix)
			}
			if got := WorkspaceDiskFormat(tt.backend); got != tt.wantFormat {
				t.Fatalf("WorkspaceDiskFormat = %q, want %q", got, tt.wantFormat)
			}
		})
	}
}

func TestBuildRootfsRequestCanUseImageCommandForPreparedWorkspace(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "homebridge/homebridge:latest",
		Architecture:    "arm64",
		SizeMiB:         4096,
		PrepareForStart: true,
		UseImageCommand: true,
	}, "/tmp/microagent/workspaces/homebridge/rootfs.ext4")

	if req.NoImageCommand {
		t.Fatal("NoImageCommand = true, want image Entrypoint/Cmd preserved")
	}
	if req.Mode != "service" {
		t.Fatalf("Mode = %q, want service", req.Mode)
	}
	if req.ShellPort != ShellPortForName("homebridge") {
		t.Fatalf("ShellPort = %d, want %d", req.ShellPort, ShellPortForName("homebridge"))
	}
	if len(req.Command) != 0 {
		t.Fatalf("Command = %#v, want OCI image command", req.Command)
	}
}

func TestBuildRootfsRequestCanUseServiceCommandForPreparedWorkspace(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "homebridge/homebridge:latest",
		Architecture:    "arm64",
		SizeMiB:         4096,
		PrepareForStart: true,
		ServiceCommand:  "/opt/homebridge/start.sh --allow-root",
	}, "/tmp/microagent/workspaces/homebridge/rootfs.ext4")

	if req.Mode != "managed-service" {
		t.Fatalf("Mode = %q, want managed-service", req.Mode)
	}
	if strings.Join(req.Command, " ") != "/bin/sh -lc /opt/homebridge/start.sh --allow-root" {
		t.Fatalf("Command = %#v", req.Command)
	}
	if req.ResultPort != 0 {
		t.Fatalf("ResultPort = %d, want 0", req.ResultPort)
	}
}

func TestBuildRootfsRequestRunsSetupBeforeManagedService(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "arm64",
		SizeMiB:         4096,
		ResultPort:      1024,
		PrepareForStart: true,
		SetupCommands:   []string{"echo setup"},
		ServiceCommand:  "/usr/local/bin/microagent-homebridge",
	}, "/tmp/microagent/workspaces/homebridge/rootfs.ext4")

	if req.Mode != "" {
		t.Fatalf("Mode = %q, want setup foreground mode", req.Mode)
	}
	if req.ResultPort != 1024 {
		t.Fatalf("ResultPort = %d, want 1024", req.ResultPort)
	}
	joined := strings.Join(req.Command, " ")
	if !strings.Contains(joined, "echo setup") {
		t.Fatalf("Command = %#v", req.Command)
	}
	if !req.ResetFinalConfig || req.FinalMode != "managed-service" {
		t.Fatalf("final reset = %v mode %q, want managed-service reset", req.ResetFinalConfig, req.FinalMode)
	}
	if !strings.Contains(strings.Join(req.FinalCommand, " "), "/usr/local/bin/microagent-homebridge") {
		t.Fatalf("FinalCommand = %#v", req.FinalCommand)
	}
}

func TestEnsureCanCreateRejectsRunningWorkspace(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "homebridge"}
	req, err := Request(opts, "start", filepath.Join(dir, "rootfs.ext4"), NewRequestID())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	err = EnsureCanCreate(opts)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("EnsureCanCreate err = %v", err)
	}
}

func TestDetachedSupervisorCommandUsesStartForPersistentBackends(t *testing.T) {
	if got := detachedSupervisorCommand(vmkit.BackendLinuxKVM); got != "start" {
		t.Fatalf("detachedSupervisorCommand(%q) = %q, want start", vmkit.BackendLinuxKVM, got)
	}
	if got := detachedSupervisorCommand(vmkit.BackendAppleVF); got != "run" {
		t.Fatalf("detachedSupervisorCommand(%q) = %q, want run", vmkit.BackendAppleVF, got)
	}
}

func TestAppleVFStartFailsBeforeDetachedRunWhenKernelMissing(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "missing-kernel",
		StateDir:       dir,
		Backend:        vmkit.BackendAppleVF,
		Architecture:   "arm64",
		KernelPath:     filepath.Join(dir, "missing-kernel"),
		SupervisorPath: filepath.Join(dir, "missing-supervisor"),
		Profile:        "small",
		RestartPolicy:  "never",
		MemoryMiB:      512,
		CPUCount:       2,
		SizeMiB:        128,
		Network:        vmkit.NetworkConfig{Mode: "isolated"},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	rootfsPath := WorkspaceRootfsPath(dir, opts.Name, opts.Backend)
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	req, err := Request(opts, "run", rootfsPath, "req-missing-kernel")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	_, err = startDetached(opts, req)
	if err == nil || !strings.Contains(err.Error(), "kernel is not readable") {
		t.Fatalf("Start err = %v, want missing kernel preflight", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, opts.Name, "runtime.json")); !os.IsNotExist(statErr) {
		t.Fatalf("runtime state exists after preflight failure: %v", statErr)
	}
}

func TestEnsureCanCreateRejectsUnavailableHostPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureCanCreate(Options{
		StateDir: t.TempDir(),
		Name:     "homebridge",
		Network: vmkit.NetworkConfig{
			Mode:         "nat",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: uint16(port), GuestPort: 8581}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "host port 127.0.0.1:"+portText+" is unavailable") {
		t.Fatalf("EnsureCanCreate err = %v", err)
	}
}
