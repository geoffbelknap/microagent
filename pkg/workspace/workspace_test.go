package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
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
	if req.Config.ShellPort != ShellPortForName("agent-1") {
		t.Fatalf("ShellPort = %d, want %d", req.Config.ShellPort, ShellPortForName("agent-1"))
	}
	if req.Config.ExecPort != ExecPortForName("agent-1") {
		t.Fatalf("ExecPort = %d, want %d", req.Config.ExecPort, ExecPortForName("agent-1"))
	}
	if !req.Config.SerialInput {
		t.Fatal("SerialInput = false")
	}
}

func TestWindowsHyperVSupportsConsoleInput(t *testing.T) {
	if !BackendSupportsConsoleInput(vmkit.BackendWindowsHyperV) {
		t.Fatal("windows-hyperv console input support = false")
	}
}

func TestShellPortCanBeExplicit(t *testing.T) {
	opts := Options{Name: "agent-1", ShellPort: 25000}
	if got := ShellPort(opts); got != 25000 {
		t.Fatalf("ShellPort = %d, want 25000", got)
	}
}

func TestExecPortCanBeExplicit(t *testing.T) {
	opts := Options{Name: "agent-1", ExecPort: 45000}
	if got := ExecPort(opts); got != 45000 {
		t.Fatalf("ExecPort = %d, want 45000", got)
	}
}

func TestDefaultOptionsUseUserNetworkMode(t *testing.T) {
	opts := DefaultOptions()
	if opts.Network.Mode != "user" {
		t.Fatalf("default network mode = %q", opts.Network.Mode)
	}
}

func TestDefaultOptionsDoNotSetAppleVFPathForNonAppleVF(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("host default backend is apple-vf on darwin")
	}
	opts := DefaultOptions()
	if opts.Backend == vmkit.BackendAppleVF {
		t.Fatalf("backend = %q, want non-apple host backend", opts.Backend)
	}
	if opts.SupervisorPath != "" {
		t.Fatalf("SupervisorPath = %q, want empty non-Apple VF default", opts.SupervisorPath)
	}
}

func TestDispatchAddsLifecycleFailureContext(t *testing.T) {
	backend := ""
	switch runtime.GOOS {
	case "linux":
		backend = vmkit.BackendFirecracker
	case "darwin":
		backend = vmkit.BackendAppleVF
	default:
		t.Skipf("fake executable supervisor dispatch test does not support %s", runtime.GOOS)
	}
	dir := t.TempDir()
	supervisorPath := filepath.Join(dir, "supervisor")
	if err := os.WriteFile(supervisorPath, []byte("#!/bin/sh\necho supervisor unavailable >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	opts := Options{
		Name:           "apply-stopped",
		Backend:        backend,
		StateDir:       stateDir,
		SupervisorPath: supervisorPath,
	}
	req := Request(opts, "halt", "", "req-1")

	resp, err := Dispatch(context.Background(), opts, req)
	if err == nil {
		t.Fatal("Dispatch error = nil")
	}
	for _, want := range []string{
		`halt workspace "apply-stopped" failed`,
		"backend=" + backend,
		"state-dir=" + stateDir,
		"supervisor=" + supervisorPath,
		"exit status 1",
		"supervisor unavailable",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Dispatch error = %q, want substring %q", err.Error(), want)
		}
		if !strings.Contains(resp.Error, want) {
			t.Fatalf("Response error = %q, want substring %q", resp.Error, want)
		}
	}
	if resp.Backend != backend {
		t.Fatalf("Response backend = %q, want %q", resp.Backend, backend)
	}
}

func TestAppleVFSupervisorPathResolvesDevBuildSupervisor(t *testing.T) {
	dir := t.TempDir()
	devDir := filepath.Join(dir, ".build", "dev")
	releaseDir := filepath.Join(dir, "supervisors", "applevf", ".build", "release")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(devDir, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	supervisor := filepath.Join(releaseDir, "microagent-applevf-supervisor")
	if err := os.WriteFile(supervisor, []byte("supervisor"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedSupervisor, err := filepath.EvalSymlinks(supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if got := AppleVFSupervisorPathFromExecutable(executable); got != resolvedSupervisor {
		t.Fatalf("AppleVFSupervisorPathFromExecutable() = %q, want %q", got, resolvedSupervisor)
	}
}

func TestAppleVFSupervisorPathResolvesSiblingSupervisor(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	supervisor := filepath.Join(dir, "microagent-applevf-supervisor")
	if err := os.WriteFile(supervisor, []byte("supervisor"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedSupervisor, err := filepath.EvalSymlinks(supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if got := AppleVFSupervisorPathFromExecutable(executable); got != resolvedSupervisor {
		t.Fatalf("AppleVFSupervisorPathFromExecutable() = %q, want %q", got, resolvedSupervisor)
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

func TestManifestPersistsSecretReferences(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = dir
	opts.Secrets = map[string]string{"API": "vault:secret/data/app#api_key"}
	opts.SecretEnvFiles = []string{"/etc/app.env"}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "ws")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(manifest.Secrets) != 1 || manifest.Secrets[0].Name != "API" || manifest.Secrets[0].Ref != "vault:secret/data/app#api_key" {
		t.Fatalf("secrets not persisted as references: %+v", manifest.Secrets)
	}
	if len(manifest.SecretEnvFiles) != 1 || manifest.SecretEnvFiles[0] != "/etc/app.env" {
		t.Fatalf("env files not persisted: %+v", manifest.SecretEnvFiles)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "workspaces", "ws", "workspace.json"))
	if strings.Contains(string(raw), "api_key\":\"") {
		t.Fatal("manifest unexpectedly contains a resolved value")
	}

	restored := OptionsFromManifest(opts, manifest)
	if restored.Secrets["API"] != "vault:secret/data/app#api_key" {
		t.Fatalf("OptionsFromManifest lost secrets: %+v", restored.Secrets)
	}
}

func TestRequestAddsSecretsListenerAndPort(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendFirecracker
	opts.Secrets = map[string]string{"API": "env:CI_TOKEN"}
	req := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if req.Config.SecretsPort != DefaultSecretsPort {
		t.Fatalf("SecretsPort = %d, want %d", req.Config.SecretsPort, DefaultSecretsPort)
	}
	if len(req.Config.Secrets) != 1 || req.Config.Secrets[0].Ref != "env:CI_TOKEN" {
		t.Fatalf("secrets not threaded into config: %+v", req.Config.Secrets)
	}
	found := false
	for _, l := range req.Config.VsockListeners {
		if l.Port == DefaultSecretsPort && l.Target == "secrets://serve" {
			found = true
		}
	}
	if !found {
		t.Fatalf("secrets vsock listener missing: %+v", req.Config.VsockListeners)
	}
}
