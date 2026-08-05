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

// The GuestBootConfig matrix replaces the old baked-request assertions:
// the same command/mode/port decisions, now made per boot on the host.

func TestGuestBootConfigUsesImageCommandForPreparedWorkspace(t *testing.T) {
	cfg, err := GuestBootConfig(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "homebridge/homebridge:latest",
		Architecture:    "arm64",
		PrepareForStart: true,
		UseImageCommand: true,
		ImageEntrypoint: []string{"/entrypoint"},
		ImageCmd:        []string{"serve"},
	})
	if err != nil {
		t.Fatalf("GuestBootConfig: %v", err)
	}
	if cfg.Mode != "service" {
		t.Fatalf("Mode = %q, want service", cfg.Mode)
	}
	if strings.Join(cfg.Command, " ") != "/entrypoint serve" {
		t.Fatalf("Command = %#v, want the persisted image Entrypoint+Cmd", cfg.Command)
	}
	if cfg.ShellPort != ShellPortForName("homebridge") {
		t.Fatalf("ShellPort = %d, want %d", cfg.ShellPort, ShellPortForName("homebridge"))
	}
}

func TestGuestBootConfigUsesServiceCommandForPreparedWorkspace(t *testing.T) {
	cfg, err := GuestBootConfig(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "homebridge/homebridge:latest",
		Architecture:    "arm64",
		PrepareForStart: true,
		ServiceCommand:  "/opt/homebridge/start.sh --allow-root",
	})
	if err != nil {
		t.Fatalf("GuestBootConfig: %v", err)
	}
	if cfg.Mode != "managed-service" {
		t.Fatalf("Mode = %q, want managed-service", cfg.Mode)
	}
	if strings.Join(cfg.Command, " ") != "/bin/sh -lc /opt/homebridge/start.sh --allow-root" {
		t.Fatalf("Command = %#v", cfg.Command)
	}
	if cfg.Port != 0 {
		t.Fatalf("Port = %d, want 0 (services report no result)", cfg.Port)
	}
}

// TestGuestBootConfigSetupThenService pins the host-side setup→final
// transition that replaced the in-guest run.json rewrite: while setup is
// incomplete every boot runs the setup script; once SetupComplete flips,
// boots run the service. A setup script that dies can no longer poison
// later boots — the host just serves the setup config again.
func TestGuestBootConfigSetupThenService(t *testing.T) {
	opts := Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "arm64",
		ResultPort:      1024,
		PrepareForStart: true,
		SetupCommands:   []string{"echo setup"},
		ServiceCommand:  "/usr/local/bin/microagent-homebridge",
	}
	setup, err := GuestBootConfig(opts)
	if err != nil {
		t.Fatalf("GuestBootConfig(setup pending): %v", err)
	}
	if setup.Mode != "" {
		t.Fatalf("setup boot Mode = %q, want foreground", setup.Mode)
	}
	if setup.Port != 1024 {
		t.Fatalf("setup boot Port = %d, want 1024", setup.Port)
	}
	if !strings.Contains(strings.Join(setup.Command, " "), "echo setup") {
		t.Fatalf("setup boot Command = %#v", setup.Command)
	}

	opts.SetupComplete = true
	final, err := GuestBootConfig(opts)
	if err != nil {
		t.Fatalf("GuestBootConfig(final): %v", err)
	}
	if final.Mode != "managed-service" {
		t.Fatalf("final boot Mode = %q, want managed-service", final.Mode)
	}
	if !strings.Contains(strings.Join(final.Command, " "), "/usr/local/bin/microagent-homebridge") {
		t.Fatalf("final boot Command = %#v", final.Command)
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

func TestApplyBoundedOperationsDefaultsAppliesLeaseWhenUnset(t *testing.T) {
	opts := Options{}
	applyBoundedOperationsDefaults(&opts)
	if opts.LeaseSeconds != DefaultLeaseSeconds {
		t.Fatalf("LeaseSeconds = %d, want DefaultLeaseSeconds (%d)", opts.LeaseSeconds, DefaultLeaseSeconds)
	}
}

func TestApplyBoundedOperationsDefaultsPreservesExplicitPermanentLease(t *testing.T) {
	opts := Options{LeaseSeconds: 0, LeaseSecondsExplicit: true}
	applyBoundedOperationsDefaults(&opts)
	if opts.LeaseSeconds != 0 {
		t.Fatalf("LeaseSeconds = %d, want 0 (explicit --ttl 0 must still mean permanent)", opts.LeaseSeconds)
	}
}

func TestApplyBoundedOperationsDefaultsPreservesExplicitCustomLease(t *testing.T) {
	opts := Options{LeaseSeconds: 3600, LeaseSecondsExplicit: true}
	applyBoundedOperationsDefaults(&opts)
	if opts.LeaseSeconds != 3600 {
		t.Fatalf("LeaseSeconds = %d, want 3600 (explicit custom value must survive)", opts.LeaseSeconds)
	}
}

func TestApplyBoundedOperationsDefaultsAppliesEgressCapsUnderMediation(t *testing.T) {
	for _, mode := range []string{"", vmkit.EgressModeBroker, vmkit.EgressModeMITM} {
		t.Run("mode="+mode, func(t *testing.T) {
			opts := Options{EgressMode: mode}
			applyBoundedOperationsDefaults(&opts)
			if opts.EgressMaxBytesPerSec != DefaultEgressMaxBytesPerSec {
				t.Fatalf("EgressMaxBytesPerSec = %d, want %d", opts.EgressMaxBytesPerSec, DefaultEgressMaxBytesPerSec)
			}
			if opts.EgressMaxTotalBytes != DefaultEgressMaxTotalBytes {
				t.Fatalf("EgressMaxTotalBytes = %d, want %d", opts.EgressMaxTotalBytes, DefaultEgressMaxTotalBytes)
			}
			if opts.EgressMaxConcurrentConns != DefaultEgressMaxConcurrentConns {
				t.Fatalf("EgressMaxConcurrentConns = %d, want %d", opts.EgressMaxConcurrentConns, DefaultEgressMaxConcurrentConns)
			}
		})
	}
}

func TestApplyBoundedOperationsDefaultsSkipsEgressCapsWhenOff(t *testing.T) {
	opts := Options{EgressMode: vmkit.EgressModeOff}
	applyBoundedOperationsDefaults(&opts)
	if opts.EgressMaxBytesPerSec != 0 || opts.EgressMaxTotalBytes != 0 || opts.EgressMaxConcurrentConns != 0 {
		t.Fatalf("egress caps defaulted under --egress off: bps=%d total=%d conns=%d", opts.EgressMaxBytesPerSec, opts.EgressMaxTotalBytes, opts.EgressMaxConcurrentConns)
	}
}

func TestApplyBoundedOperationsDefaultsPreservesExplicitDisabledEgressCaps(t *testing.T) {
	opts := Options{
		EgressMode:                       vmkit.EgressModeMITM,
		EgressMaxBytesPerSecExplicit:     true,
		EgressMaxTotalBytesExplicit:      true,
		EgressMaxConcurrentConnsExplicit: true,
	}
	applyBoundedOperationsDefaults(&opts)
	if opts.EgressMaxBytesPerSec != 0 {
		t.Fatalf("EgressMaxBytesPerSec = %d, want 0 (explicit disable must survive)", opts.EgressMaxBytesPerSec)
	}
	if opts.EgressMaxTotalBytes != 0 {
		t.Fatalf("EgressMaxTotalBytes = %d, want 0 (explicit disable must survive)", opts.EgressMaxTotalBytes)
	}
	if opts.EgressMaxConcurrentConns != 0 {
		t.Fatalf("EgressMaxConcurrentConns = %d, want 0 (explicit disable must survive)", opts.EgressMaxConcurrentConns)
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
