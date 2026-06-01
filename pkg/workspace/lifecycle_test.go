package workspace

import (
	"context"
	"fmt"
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

func TestPauseAndResumeDispatchControlCommands(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "agent-1",
		StateDir:       dir,
		Backend:        vmkit.BackendFirecracker,
		SupervisorPath: filepath.Join(dir, "no-such-supervisor"),
	}
	// With a missing supervisor binary, both calls fail at dispatch — but they
	// must get past Control's command whitelist, proving pause/resume are wired
	// through as supervisor commands rather than rejected as unsupported.
	if _, err := Pause(context.Background(), opts); err == nil || strings.Contains(err.Error(), "unsupported workspace control command") {
		t.Fatalf("Pause not wired to a pause control command: %v", err)
	}
	if _, err := Resume(context.Background(), opts); err == nil || strings.Contains(err.Error(), "unsupported workspace control command") {
		t.Fatalf("Resume not wired to a resume control command: %v", err)
	}
}

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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
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
				Target:     listener.Addr().String(),
				FailClosed: true,
			},
		},
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	readiness := readinessFromRuntime(state)
	if !readiness.MediationReady.Ready {
		t.Fatalf("mediation readiness = %#v", readiness.MediationReady)
	}
	if !strings.Contains(readiness.MediationReady.Detail, "port=2048") || !strings.Contains(readiness.MediationReady.Detail, "target="+listener.Addr().String()) {
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

func TestStatusNonLiveStatesUseFastReadinessAndRecordedRootfs(t *testing.T) {
	for _, state := range []vmkit.VMState{vmkit.StatePrepared, vmkit.StateHalted} {
		t.Run(string(state), func(t *testing.T) {
			dir := t.TempDir()
			kernelPath := filepath.Join(dir, "Image")
			if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
				t.Fatal(err)
			}
			missingRootfs := filepath.Join(dir, "workspaces", "agent", "rootfs.ext4")
			opts := Options{
				Name:          "agent",
				StateDir:      dir,
				Backend:       HostBackend(),
				KernelPath:    kernelPath,
				Profile:       "tiny",
				RestartPolicy: DefaultRestartPolicy,
				Verification: &vmkit.RuntimeVerification{
					OK: true,
					Kernel: &vmkit.VerifiedArtifact{
						Path:   kernelPath,
						SHA256: "recorded-kernel",
					},
					Rootfs: &vmkit.VerifiedArtifact{
						Path:   missingRootfs,
						SHA256: "recorded-rootfs",
					},
				},
			}
			if err := WriteManifest(opts); err != nil {
				t.Fatalf("WriteManifest: %v", err)
			}
			req := Request(opts, "inspect", missingRootfs, "req-1")
			req.Config.KernelPath = kernelPath
			req.Config.RootfsPath = missingRootfs
			if err := WriteProcessState(opts, req, state, 0, ""); err != nil {
				t.Fatalf("WriteProcessState: %v", err)
			}

			start := time.Now()
			resp, err := Status(opts)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if elapsed := time.Since(start); elapsed >= time.Second {
				t.Fatalf("Status elapsed = %s, want < 1s", elapsed)
			}
			if resp.Verification == nil || resp.Verification.Rootfs == nil {
				t.Fatalf("verification = %#v", resp.Verification)
			}
			if resp.Verification.Rootfs.Error != "" {
				t.Fatalf("rootfs verification error = %q, want fast recorded metadata", resp.Verification.Rootfs.Error)
			}
			if resp.Verification.Rootfs.SHA256 != "recorded-rootfs" || resp.Verification.Rootfs.RecordedSHA256 != "recorded-rootfs" {
				t.Fatalf("rootfs verification = %#v, want recorded checksum", resp.Verification.Rootfs)
			}
			if resp.Readiness == nil {
				t.Fatal("readiness missing")
			}
			if resp.Readiness.ExecReady.Ready || !strings.Contains(resp.Readiness.ExecReady.Detail, "live readiness unavailable") {
				t.Fatalf("exec readiness = %#v, want fast unavailable detail", resp.Readiness.ExecReady)
			}
			if resp.Readiness.ShellReady.Ready || !strings.Contains(resp.Readiness.ShellReady.Detail, "live readiness unavailable") {
				t.Fatalf("shell readiness = %#v, want fast unavailable detail", resp.Readiness.ShellReady)
			}
			if resp.Readiness.ResultReady.Ready || !strings.Contains(resp.Readiness.ResultReady.Detail, "live readiness unavailable") {
				t.Fatalf("result readiness = %#v, want fast unavailable detail", resp.Readiness.ResultReady)
			}
		})
	}
}

func TestStatusRunningWorkspaceStillChecksCurrentRootfs(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingRootfs := filepath.Join(dir, "workspaces", "agent", "rootfs.ext4")
	opts := Options{
		Name:          "agent",
		StateDir:      dir,
		Backend:       HostBackend(),
		KernelPath:    kernelPath,
		Profile:       "tiny",
		RestartPolicy: DefaultRestartPolicy,
		Verification: &vmkit.RuntimeVerification{
			OK:     true,
			Rootfs: &vmkit.VerifiedArtifact{Path: missingRootfs, SHA256: "recorded-rootfs"},
		},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	req := Request(opts, "inspect", missingRootfs, "req-1")
	req.Config.KernelPath = kernelPath
	req.Config.RootfsPath = missingRootfs
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 0, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Verification == nil || resp.Verification.Rootfs == nil || resp.Verification.Rootfs.Error == "" {
		t.Fatalf("rootfs verification = %#v, want current rootfs error for running workspace", resp.Verification)
	}
	if !strings.Contains(resp.Verification.Rootfs.Error, "no such file") {
		t.Fatalf("rootfs error = %q", resp.Verification.Rootfs.Error)
	}
}

func TestReadinessFromRuntimeRequiresLiveShellTarget(t *testing.T) {
	for _, backend := range []string{vmkit.BackendFirecracker, vmkit.BackendAppleVF} {
		t.Run(backend, func(t *testing.T) {
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
					Identity:   vmkit.Identity{RuntimeID: "agent", Backend: backend},
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
				t.Fatalf("shell readiness = %#v, want not ready before shell target is reachable", readiness.ShellReady)
			}
			if !strings.Contains(readiness.ShellReady.Detail, "command probe failed") {
				t.Fatalf("shell readiness detail = %q, want command probe failure detail", readiness.ShellReady.Detail)
			}
			if err := os.WriteFile(serialPath, []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			readiness = readinessFromRuntime(state)
			if readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want not ready when only the guest helper log exists", readiness.ShellReady)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serveDone := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serveDone <- err
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				var command strings.Builder
				buf := make([]byte, 1024)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						command.Write(buf[:n])
						if strings.Contains(command.String(), "exit\r") {
							break
						}
					}
					if err != nil {
						serveDone <- err
						return
					}
				}
				text := command.String()
				tokenStart := strings.Index(text, "__ma_token=")
				if tokenStart == -1 {
					serveDone <- fmt.Errorf("command %q missing token assignment", text)
					return
				}
				tokenStart += len("__ma_token=")
				tokenEnd := strings.Index(text[tokenStart:], ";")
				if tokenEnd == -1 {
					serveDone <- fmt.Errorf("command %q missing token terminator", text)
					return
				}
				token := text[tokenStart : tokenStart+tokenEnd]
				_, err = fmt.Fprintf(conn, "\r\n__MICROAGENT_DONE_%s__0\r\n", token)
				serveDone <- err
			}()
			_, portText, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				t.Fatal(err)
			}
			state.Config.ShellPort = uint16(port)
			readiness = readinessFromRuntime(state)
			if !readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want ready when shell target completes a command probe", readiness.ShellReady)
			}
			if !strings.Contains(readiness.ShellReady.Detail, "command round-trip ready at") {
				t.Fatalf("shell readiness detail = %q", readiness.ShellReady.Detail)
			}
			if err := <-serveDone; err != nil {
				t.Fatalf("shell target probe server: %v", err)
			}
		})
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

func TestApplySpecFilePopulatesWorkspaceOptions(t *testing.T) {
	dir := t.TempDir()
	setupPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(setupPath, []byte("apt-get update\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "microagent.yaml")
	if err := os.WriteFile(specPath, []byte(`
name: demo
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
setupFiles:
  - setup.sh
env:
  FOO: bar
resources:
  memoryMiB: 1024
network:
  mode: user
files:
  - src: config.txt
    dst: /etc/demo/config.txt
outputs:
  - name: result
    path: /workspace/result.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	if err := ApplySpecFile(&opts, specPath, SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpecFile: %v", err)
	}
	if opts.Name != "demo" || opts.ImageRef != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("spec identity not applied: %+v", opts)
	}
	if opts.Profile != "medium" || opts.MemoryMiB != 1024 || opts.RestartPolicy != "on-failure" {
		t.Fatalf("spec resources not applied: %+v", opts)
	}
	if len(opts.SetupCommands) != 1 || opts.SetupCommands[0] != "apt-get update" {
		t.Fatalf("setup commands = %#v", opts.SetupCommands)
	}
	if opts.Env["FOO"] != "bar" {
		t.Fatalf("env = %#v", opts.Env)
	}
	if len(opts.Files) != 1 || opts.Files[0].SourcePath != filePath || opts.Files[0].Path != "/etc/demo/config.txt" {
		t.Fatalf("files = %#v", opts.Files)
	}
	if len(opts.Outputs) != 1 || opts.Outputs[0].Name != "result" {
		t.Fatalf("outputs = %#v", opts.Outputs)
	}
}

func TestReadSpecReportsUnknownField(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "microagent.yaml")
	if err := os.WriteFile(specPath, []byte("resources:\n  network: user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSpec(specPath)
	if err == nil || !strings.Contains(err.Error(), `unknown field "network" under resources`) {
		t.Fatalf("ReadSpec error = %v", err)
	}
}

func TestApplyUpdatesStoppedWorkspaceNetwork(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network: vmkit.NetworkConfig{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
		},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(t.Context(), Options{StateDir: dir, Backend: vmkit.BackendFirecracker}, Spec{
		Name: "homebridge",
		Network: NetworkSpec{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", Host: "0.0.0.0", HostPort: 8581, GuestPort: 8581}},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Workspace != "homebridge" || len(result.Applied) != 1 || result.Applied[0] != "network" {
		t.Fatalf("result = %#v", result)
	}
	manifest, err := ReadManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].Host; got != "0.0.0.0" {
		t.Fatalf("forward host = %q, want 0.0.0.0", got)
	}
}

func TestApplyRejectsLiveNonHostNetworkChange(t *testing.T) {
	dir := t.TempDir()
	originalNetwork := vmkit.NetworkConfig{
		Mode:         "user",
		PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
	}
	opts := Options{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network:       originalNetwork,
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	req.Config.Network = &originalNetwork
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(t.Context(), Options{StateDir: dir, Backend: vmkit.BackendFirecracker}, Spec{
		Name: "homebridge",
		Network: NetworkSpec{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", Host: "0.0.0.0", HostPort: 8581, GuestPort: 8582}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "host bind changes") {
		t.Fatalf("err = %v, want host-bind-only rejection", err)
	}
	manifest, err := ReadManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].GuestPort; got != uint16(8581) {
		t.Fatalf("guest port changed to %d", got)
	}
}

func TestConsoleTargetUsesWindowsHyperVRuntimeID(t *testing.T) {
	state := RuntimeState{
		Event: EventFile{
			Identity: vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendWindowsHyperV},
			State:    vmkit.StateRunning,
		},
		Config:                 vmkit.Config{ShellPort: 25000},
		ComputeSystemRuntimeID: "11111111-1111-1111-1111-111111111111",
	}
	target, err := ConsoleTarget("agent-1", state)
	if err != nil {
		t.Fatal(err)
	}
	if target.Network != "hvsock" || target.RuntimeID != "11111111-1111-1111-1111-111111111111" || target.Port != 25000 {
		t.Fatalf("target = %#v", target)
	}
}

func TestConsoleTargetRejectsWindowsHyperVWithoutRuntimeID(t *testing.T) {
	state := RuntimeState{
		Event: EventFile{
			Identity: vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendWindowsHyperV},
			State:    vmkit.StateRunning,
		},
		Config: vmkit.Config{ShellPort: 25000},
	}
	if _, err := ConsoleTarget("agent-1", state); err == nil || !strings.Contains(err.Error(), "compute system runtime ID") {
		t.Fatalf("ConsoleTarget err = %v, want compute system runtime ID", err)
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

	req := Request(opts, "run", rootfsPath, "req-missing-kernel")
	_, err := startDetached(opts, req)
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
