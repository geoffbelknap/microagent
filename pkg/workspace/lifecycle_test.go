package workspace

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestManifestAndStatusLifecycleAreLibraryOwned(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "agency-task",
		StateDir:       dir,
		Backend:        HostBackend(),
		Profile:        "small",
		RestartPolicy:  "never",
		MemoryMiB:      512,
		CPUCount:       2,
		SizeMiB:        1024,
		Network:        vmkit.NetworkConfig{Mode: "nat"},
		ServiceCommand: "/opt/homebridge/start.sh --allow-root",
		Disks: []Disk{{
			Name:       "work",
			Path:       "/tmp/work.ext4",
			Mountpoint: "/work",
			Mode:       "rw",
		}},
		Outputs: []Output{{Name: "result", Path: "/work/result.txt"}},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "agency-task")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Name != "agency-task" || manifest.Artifacts.Egress[0].Name != "result" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.Service != "/opt/homebridge/start.sh --allow-root" {
		t.Fatalf("manifest Service = %q", manifest.Service)
	}

	req := Request(opts, "run", filepath.Join(dir, "workspaces", "agency-task", "rootfs.ext4"), "req-1")
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("status response = %#v", resp)
	}
	if resp.Artifacts == nil || len(resp.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", resp.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(dir, "agency-task", "runtime.json")); err != nil {
		t.Fatalf("runtime.json not written: %v", err)
	}
	artifacts, err := ArtifactsFor(dir, "agency-task")
	if err != nil {
		t.Fatalf("ArtifactsFor: %v", err)
	}
	if len(artifacts.Egress) != 1 || artifacts.Egress[0].Name != "result" {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "agency-task" || entries[0].State != string(vmkit.StateRunning) {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestDefaultHostnameSanitizesWorkspaceName(t *testing.T) {
	tests := map[string]string{
		"homebridge":            "homebridge",
		"Home_Bridge.local":     "home-bridge-local",
		"---":                   "microagent",
		strings.Repeat("a", 70): strings.Repeat("a", 63),
	}
	for name, want := range tests {
		if got := DefaultHostname(name); got != want {
			t.Fatalf("DefaultHostname(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestValidateHostnameRejectsInvalidValues(t *testing.T) {
	for _, hostname := range []string{"bad_name", "-bad", "bad-", "", strings.Repeat("a", 64)} {
		if err := ValidateHostname(hostname); err == nil {
			t.Fatalf("ValidateHostname(%q) error = nil", hostname)
		}
	}
}

func TestBackendOwnsRuntimeState(t *testing.T) {
	for _, backend := range []string{vmkit.BackendFirecracker, vmkit.BackendWindowsHyperV} {
		if !backendOwnsRuntimeState(backend) {
			t.Fatalf("backendOwnsRuntimeState(%q) = false, want true", backend)
		}
	}
	if backendOwnsRuntimeState(vmkit.BackendAppleVF) {
		t.Fatalf("backendOwnsRuntimeState(%q) = true, want false", vmkit.BackendAppleVF)
	}
}

func TestReadinessFromRuntimeReportsWindowsHyperVMediation(t *testing.T) {
	state := RuntimeState{
		Event: EventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent", Backend: vmkit.BackendWindowsHyperV},
			State:      vmkit.StateRunning,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config: vmkit.Config{
			StateDir: t.TempDir(),
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     "127.0.0.1:9000",
				FailClosed: true,
			},
		},
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	readiness := readinessFromRuntime(state)
	if !readiness.MediationReady.Ready {
		t.Fatalf("mediation readiness = %#v", readiness.MediationReady)
	}
	if !strings.Contains(readiness.MediationReady.Detail, "port=2048") || !strings.Contains(readiness.MediationReady.Detail, "target=127.0.0.1:9000") {
		t.Fatalf("mediation readiness detail = %q", readiness.MediationReady.Detail)
	}
}

func TestReadinessFromRuntimeReportsWindowsHyperVShell(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "agent", "serial.in")
	if err := os.MkdirAll(filepath.Dir(inputPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	state := RuntimeState{
		Event: EventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent", Backend: vmkit.BackendWindowsHyperV},
			State:      vmkit.StateRunning,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config:          vmkit.Config{StateDir: dir, SerialInput: true},
		SerialInputPath: inputPath,
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	readiness := readinessFromRuntime(state)
	if !readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v", readiness.ShellReady)
	}
	if readiness.ShellReady.Detail != "console input is available" {
		t.Fatalf("shell readiness detail = %q", readiness.ShellReady.Detail)
	}
}

func TestReadinessFromRuntimeRequiresFirecrackerShellHelperLog(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, "agent")
	inputPath := filepath.Join(runtimeDir, "serial.in")
	serialPath := filepath.Join(runtimeDir, "serial.log")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	state := RuntimeState{
		Event: EventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent", Backend: vmkit.BackendFirecracker},
			State:      vmkit.StateRunning,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config:          vmkit.Config{StateDir: dir, SerialInput: true, ShellPort: 24279},
		SerialInputPath: inputPath,
		SerialLogPath:   serialPath,
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	readiness := readinessFromRuntime(state)
	if readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v, want not ready before guest helper log", readiness.ShellReady)
	}
	if err := os.WriteFile(serialPath, []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readiness = readinessFromRuntime(state)
	if !readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v, want ready after guest helper log", readiness.ShellReady)
	}
	if readiness.ShellReady.Detail != "guest shell helper listening on vsock port 24279" {
		t.Fatalf("shell readiness detail = %q", readiness.ShellReady.Detail)
	}
}

func TestBuildRootfsRequestAllowsMutableWorkspaceImages(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:         "research",
		StateDir:     "/tmp/microagent",
		ImageRef:     "docker.io/library/ubuntu:24.04",
		Architecture: "arm64",
		SizeMiB:      1024,
	}, "/tmp/microagent/workspaces/research/rootfs.ext4")

	if !req.AllowMutable {
		t.Fatal("workspace rootfs builds should allow mutable image tags")
	}
}

func TestWorkspaceRootfsPathUsesBackendFormat(t *testing.T) {
	tests := []struct {
		name       string
		backend    string
		wantSuffix string
		wantFormat string
	}{
		{
			name:       "firecracker",
			backend:    vmkit.BackendFirecracker,
			wantSuffix: filepath.Join("workspaces", "research", "rootfs.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "apple-vf",
			backend:    vmkit.BackendAppleVF,
			wantSuffix: filepath.Join("workspaces", "research", "rootfs.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "windows-hyperv",
			backend:    vmkit.BackendWindowsHyperV,
			wantSuffix: filepath.Join("workspaces", "research", "rootfs.vhd"),
			wantFormat: rootfs.FormatVHD,
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
			name:       "firecracker",
			backend:    vmkit.BackendFirecracker,
			wantSuffix: filepath.Join("workspaces", "research", "disks", "work.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "apple-vf",
			backend:    vmkit.BackendAppleVF,
			wantSuffix: filepath.Join("workspaces", "research", "disks", "work.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "windows-hyperv",
			backend:    vmkit.BackendWindowsHyperV,
			wantSuffix: filepath.Join("workspaces", "research", "disks", "work.vhd"),
			wantFormat: rootfs.FormatVHD,
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

func TestBuildRootfsRequestUsesHyperVSCSIDevicesForWindowsDisks(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "research",
		StateDir:        "/tmp/microagent",
		Backend:         vmkit.BackendWindowsHyperV,
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "amd64",
		SizeMiB:         1024,
		PrepareForStart: true,
		ServiceCommand:  "sleep infinity",
		Disks: []Disk{
			{Name: "config", Path: "/tmp/config.vhd", Mountpoint: "/config", Mode: "ro"},
			{Name: "work", Path: "/tmp/work.vhd", Mountpoint: "/work", Mode: "rw"},
		},
	}, "/tmp/microagent/workspaces/research/rootfs.vhd")

	if len(req.Mounts) != 2 {
		t.Fatalf("Mounts = %#v", req.Mounts)
	}
	if req.Mounts[0].Device != "/dev/sdb" || req.Mounts[1].Device != "/dev/sdc" {
		t.Fatalf("mount devices = %#v, want /dev/sdb and /dev/sdc", req.Mounts)
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
	if !strings.Contains(joined, "echo setup") || !strings.Contains(joined, `"mode":"managed-service"`) {
		t.Fatalf("Command = %#v", req.Command)
	}
}

func TestEnsureCanCreateRejectsRunningWorkspace(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "homebridge"}
	req := Request(opts, "start", filepath.Join(dir, "rootfs.ext4"), NewRequestID())
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	err := EnsureCanCreate(opts)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("EnsureCanCreate err = %v", err)
	}
}

func TestDetachedSupervisorCommandUsesStartForPersistentBackends(t *testing.T) {
	for _, backend := range []string{vmkit.BackendFirecracker, vmkit.BackendWindowsHyperV} {
		if got := detachedSupervisorCommand(backend); got != "start" {
			t.Fatalf("detachedSupervisorCommand(%q) = %q, want start", backend, got)
		}
	}
	if got := detachedSupervisorCommand(vmkit.BackendAppleVF); got != "run" {
		t.Fatalf("detachedSupervisorCommand(%q) = %q, want run", vmkit.BackendAppleVF, got)
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

func TestStatusDoesNotTreatStartedRootfsMutationAsDivergence(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	initPath := filepath.Join(dir, "microagent-init")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("kernel-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initPath, []byte("init-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Name:          "research",
		StateDir:      dir,
		Backend:       HostBackend(),
		KernelPath:    kernelPath,
		GuestInitPath: initPath,
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}
	result := Result{
		Workspace:  "research",
		RootfsPath: rootfsPath,
		Image: rootfs.Provenance{
			ImageRef:    "docker.io/library/busybox:1.36",
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
		},
	}
	verification, err := BuildVerification(opts, result)
	if err != nil {
		t.Fatal(err)
	}
	opts.Verification = &verification
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := Request(opts, "run", rootfsPath, "req-1")
	req.Config.KernelPath = kernelPath
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verification == nil || !resp.Verification.OK {
		t.Fatalf("verification = %#v, want ok after started rootfs mutation", resp.Verification)
	}
	if resp.Verification.Rootfs == nil || resp.Verification.Rootfs.RecordedSHA256 == "" || resp.Verification.Rootfs.SHA256 == "" {
		t.Fatalf("rootfs verification details missing: %#v", resp.Verification)
	}
}
