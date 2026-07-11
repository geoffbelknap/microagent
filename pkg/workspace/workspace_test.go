package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestRequestBuildsBackendNeutralWorkspaceRequest(t *testing.T) {
	opts := Options{
		Name:           "agent-1",
		Backend:        vmkit.BackendLinuxKVM,
		KernelPath:     "/kernels/Image",
		StateDir:       t.TempDir(),
		MemoryMiB:      512,
		CPUCount:       2,
		ResultPort:     1024,
		SerialInput:    true,
		Network:        vmkit.NetworkConfig{Mode: "user"},
		VsockListeners: []vmkit.VsockListener{{Port: 2048, Target: "/tmp/service.sock"}},
		Disks: []Disk{{
			Name:       "work",
			Path:       "/tmp/work.ext4",
			Mountpoint: "/work",
			Mode:       "rw",
		}},
	}

	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

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
	// Result listener + the service vsock listener. The default mode is broker,
	// which mediates but forges no certificates — so it allocates NO CA-cert
	// listener (unlike the retired guarded default).
	if len(req.Config.VsockListeners) != 2 {
		t.Fatalf("VsockListeners = %#v", req.Config.VsockListeners)
	}
	if req.Config.VsockListeners[0].Target != filepath.Join(opts.StateDir, opts.Name, "result.json") {
		t.Fatalf("result listener = %#v", req.Config.VsockListeners[0])
	}
	if hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("broker default must not allocate a %q listener; got %#v", secretxfer.CACertTarget, req.Config.VsockListeners)
	}
	if req.Config.CACertPort != 0 {
		t.Fatalf("CACertPort = %d, want 0 for the broker default (no forging)", req.Config.CACertPort)
	}
	if len(req.Config.Disks) != 1 || req.Config.Disks[0].Mountpoint != "/work" {
		t.Fatalf("Disks = %#v", req.Config.Disks)
	}
	if req.Config.Network == nil || req.Config.Network.Mode != "user" {
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

func TestFirecrackerSupervisorPathFromExecutableResolvesLibexec(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	libexec := filepath.Join(dir, "libexec")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libexec, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	supervisor := filepath.Join(libexec, "microagent-firecracker-supervisor")
	if err := os.WriteFile(supervisor, []byte("supervisor"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := FirecrackerSupervisorPathFromExecutable(executable)
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	wantReal, err := filepath.EvalSymlinks(supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if gotReal != wantReal {
		t.Fatalf("FirecrackerSupervisorPathFromExecutable() = %q, want %q", got, supervisor)
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

func TestNetworkSpecRoundTripPreservesUserMode(t *testing.T) {
	spec := NetworkSpecFromConfig(vmkit.NetworkConfig{Mode: "user"})
	if spec.Mode != "user" {
		t.Fatalf("NetworkSpecFromConfig changed the mode: %#v", spec)
	}
	cfg := NetworkConfigFromSpec(spec)
	if cfg.Mode != "user" {
		t.Fatalf("NetworkConfigFromSpec changed the mode: %#v", cfg)
	}
	if err := vmkit.ValidateNetworkConfig(cfg); err != nil {
		t.Fatalf("round-tripped user config failed validation: %v", err)
	}
}

func TestNetworkSpecRemovedModeRejectedAtStart(t *testing.T) {
	// A persisted or hand-written manifest naming a removed mode must be rejected.
	for _, mode := range []string{"bridged", "nat", "named"} {
		cfg := NetworkConfigFromSpec(NetworkSpec{Mode: mode})
		if err := vmkit.ValidateNetworkConfig(cfg); err == nil {
			t.Fatalf("removed network mode %q passed validation", mode)
		}
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
		backend = vmkit.BackendLinuxKVM
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
	req, err := Request(opts, "halt", "", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

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
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Dispatch error = %q, want chain to *exec.ExitError via errors.As", err.Error())
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

func TestAppleVFDetachedSupervisorEnvIncludesDatapathBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := supervisorEnvironment(Options{Backend: vmkit.BackendAppleVF})
	if !containsEnv(env, "MICROAGENT_EGRESS_DATAPATH_BIN", exe) {
		t.Fatalf("MICROAGENT_EGRESS_DATAPATH_BIN not set to current executable in %#v", env)
	}
}

func TestNonAppleVFDetachedSupervisorEnvDoesNotAddDatapathBinary(t *testing.T) {
	env := supervisorEnvironment(Options{Backend: vmkit.BackendLinuxKVM})
	if hasEnvKey(env, "MICROAGENT_EGRESS_DATAPATH_BIN") {
		t.Fatalf("non-apple-vf supervisor env unexpectedly contains MICROAGENT_EGRESS_DATAPATH_BIN: %#v", env)
	}
}

func containsEnv(env []string, key, value string) bool {
	want := key + "=" + value
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
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

func TestRequestSetsSecretsControlPort(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendLinuxKVM
	opts.Secrets = map[string]string{"API": "env:X"}
	req, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.SecretsControlPort != DefaultSecretsControlPort {
		t.Fatalf("SecretsControlPort = %d, want %d", req.Config.SecretsControlPort, DefaultSecretsControlPort)
	}

	bare := DefaultOptions()
	bare.Name = "ws2"
	bare.StateDir = t.TempDir()
	bare.Backend = vmkit.BackendLinuxKVM
	bareReq, err := Request(bare, "", "/tmp/rootfs.ext4", "req-2")
	if err != nil {
		t.Fatalf("Request bare: %v", err)
	}
	if got := bareReq.Config.SecretsControlPort; got != 0 {
		t.Fatalf("SecretsControlPort = %d, want 0 when no secrets declared", got)
	}
}

func TestManifestPersistsOnDemandAndAudit(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = dir
	opts.OnDemandSecrets = map[string]string{"DB": "vault:secret/data/app#db"}
	opts.SecretsAudit = true
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "ws")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(manifest.OnDemandSecrets) != 1 || manifest.OnDemandSecrets[0].Name != "DB" || !manifest.SecretsAudit {
		t.Fatalf("on-demand/audit not persisted: %+v", manifest)
	}
	restored := OptionsFromManifest(opts, manifest)
	if restored.OnDemandSecrets["DB"] != "vault:secret/data/app#db" || !restored.SecretsAudit {
		t.Fatalf("OptionsFromManifest lost on-demand/audit: %+v", restored)
	}
}

func TestApplyManifestRestoresSecretsForStartRequest(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendAppleVF
	manifest := Manifest{
		Secrets:         []vmkit.SecretRef{{Name: "API", Ref: "env:API_TOKEN"}},
		SecretEnvFiles:  []string{"/tmp/app.env"},
		OnDemandSecrets: []vmkit.SecretRef{{Name: "DB", Ref: "env:DB_TOKEN"}},
		SecretsAudit:    true,
	}
	applyManifest(&opts, manifest)
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.SecretsPort != DefaultSecretsPort || req.Config.SecretsControlPort != DefaultSecretsControlPort {
		t.Fatalf("secrets ports not restored into request: %+v", req.Config)
	}
	if len(req.Config.Secrets) != 1 || req.Config.Secrets[0].Ref != "env:API_TOKEN" {
		t.Fatalf("materialized secrets not restored: %+v", req.Config.Secrets)
	}
	if len(req.Config.SecretEnvFiles) != 1 || req.Config.SecretEnvFiles[0] != "/tmp/app.env" {
		t.Fatalf("secret env files not restored: %+v", req.Config.SecretEnvFiles)
	}
	if len(req.Config.OnDemandSecrets) != 1 || req.Config.OnDemandSecrets[0].Ref != "env:DB_TOKEN" || !req.Config.SecretsAudit {
		t.Fatalf("on-demand/audit not restored: %+v", req.Config)
	}
	found := false
	for _, l := range req.Config.VsockListeners {
		if l.Port == DefaultSecretsPort && l.Target == "secrets://serve" {
			found = true
		}
	}
	if !found {
		t.Fatalf("secrets listener missing after manifest apply: %+v", req.Config.VsockListeners)
	}
}

func TestRequestThreadsOnDemandAndAudit(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendLinuxKVM
	opts.OnDemandSecrets = map[string]string{"DB": "env:X"}
	opts.SecretsAudit = true
	req, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.SecretsPort != DefaultSecretsPort {
		t.Fatalf("SecretsPort = %d, want %d (on-demand should enable the port)", req.Config.SecretsPort, DefaultSecretsPort)
	}
	if len(req.Config.OnDemandSecrets) != 1 || req.Config.OnDemandSecrets[0].Ref != "env:X" || !req.Config.SecretsAudit {
		t.Fatalf("on-demand/audit not threaded: %+v", req.Config)
	}
	found := false
	for _, l := range req.Config.VsockListeners {
		if l.Port == DefaultSecretsPort && l.Target == "secrets://serve" {
			found = true
		}
	}
	if !found {
		t.Fatalf("secrets listener missing for on-demand-only workspace: %+v", req.Config.VsockListeners)
	}
}

func TestRequestAddsSecretsListenerAndPort(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendLinuxKVM
	opts.Secrets = map[string]string{"API": "env:CI_TOKEN"}
	req, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
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

func TestRequestWiresModelTarget(t *testing.T) {
	opts := Options{Name: "w", StateDir: t.TempDir(), Backend: "linux-kvm", ModelTarget: "127.0.0.1:38001"}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	found := false
	for _, l := range req.Config.VsockListeners {
		if l.Port == DefaultModelVsockPort && l.Target == "127.0.0.1:38001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("model vsock listener not wired: %+v", req.Config.VsockListeners)
	}
	if req.Config.ModelGuestPort != DefaultModelGuestPort || req.Config.ModelVsockPort != DefaultModelVsockPort {
		t.Fatalf("model ports not set: guest=%d vsock=%d", req.Config.ModelGuestPort, req.Config.ModelVsockPort)
	}
}

func TestRequestNoModelTarget(t *testing.T) {
	opts := Options{Name: "w", StateDir: t.TempDir(), Backend: "linux-kvm"}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.ModelGuestPort != 0 || req.Config.ModelVsockPort != 0 {
		t.Fatalf("model ports should be zero when unpaired")
	}
}

func TestManifestPersistsModelRef(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = dir
	opts.Model = "Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf"
	opts.ModelRunner = ModelRunnerSpec{
		Backend:      "vllm",
		GPU:          "on",
		BackendModel: "Qwen/Qwen2.5-0.5B-Instruct",
		ServedModel:  "local-chat",
		Args:         []string{"--max-model-len", "2048"},
		Env:          []string{"RUNNER_SECRET=must-not-persist"},
	}
	opts.ModelMediation = ModelMediationSpec{
		Mode:          "policy",
		PolicyFile:    "/tmp/model-policy.json",
		PolicyTimeout: "250ms",
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "ws")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Model != opts.Model {
		t.Fatalf("model ref not persisted: %q", manifest.Model)
	}
	if manifest.ModelRunner == nil || manifest.ModelRunner.Backend != "vllm" || manifest.ModelRunner.BackendModel != "Qwen/Qwen2.5-0.5B-Instruct" || len(manifest.ModelRunner.Env) != 0 {
		t.Fatalf("model runner manifest = %+v", manifest.ModelRunner)
	}
	if manifest.ModelMediation == nil || manifest.ModelMediation.Mode != "policy" || manifest.ModelMediation.PolicyFile != "/tmp/model-policy.json" {
		t.Fatalf("model mediation manifest = %+v", manifest.ModelMediation)
	}
	restored := OptionsFromManifest(opts, manifest)
	if restored.Model != opts.Model {
		t.Fatalf("OptionsFromManifest lost model ref: %q", restored.Model)
	}
	if restored.ModelRunner.Backend != "vllm" || restored.ModelMediation.Mode != "policy" {
		t.Fatalf("OptionsFromManifest lost model config: %+v %+v", restored.ModelRunner, restored.ModelMediation)
	}

	bare := DefaultOptions()
	bare.Name = "ws2"
	bare.StateDir = dir
	if err := WriteManifest(bare); err != nil {
		t.Fatalf("write bare manifest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "workspaces", "ws2", "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\"model\"") {
		t.Fatalf("unpaired manifest should omit model: %s", raw)
	}
}

func TestApplyManifestRestoresModelRef(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	applyManifest(&opts, Manifest{Model: "org/repo/model.gguf"})
	if opts.Model != "org/repo/model.gguf" {
		t.Fatalf("applyManifest did not restore model ref: %q", opts.Model)
	}
	applyManifest(&opts, Manifest{})
	if opts.Model != "" {
		t.Fatalf("applyManifest should clear model when manifest has none: %q", opts.Model)
	}
}

func TestManifestRoundTripPreservesEgress(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = dir
	opts.EgressMode = "mitm"
	opts.EgressAllow = []string{"api.github.com", ".example.com"}
	opts.EgressPassthrough = []string{"raw.example.com"}

	if err := WriteManifest(opts); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "ws")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.EgressMode != "mitm" {
		t.Fatalf("EgressMode not persisted in manifest: %q", manifest.EgressMode)
	}
	if len(manifest.EgressAllow) != 2 || manifest.EgressAllow[0] != "api.github.com" || manifest.EgressAllow[1] != ".example.com" {
		t.Fatalf("EgressAllow not persisted in manifest: %v", manifest.EgressAllow)
	}
	if len(manifest.EgressPassthrough) != 1 || manifest.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("EgressPassthrough not persisted in manifest: %v", manifest.EgressPassthrough)
	}

	restored := OptionsFromManifest(opts, manifest)
	if restored.EgressMode != "mitm" {
		t.Fatalf("OptionsFromManifest lost EgressMode: %q", restored.EgressMode)
	}
	if len(restored.EgressAllow) != 2 || restored.EgressAllow[0] != "api.github.com" || restored.EgressAllow[1] != ".example.com" {
		t.Fatalf("OptionsFromManifest lost EgressAllow: %v", restored.EgressAllow)
	}
	if len(restored.EgressPassthrough) != 1 || restored.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("OptionsFromManifest lost EgressPassthrough: %v", restored.EgressPassthrough)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "workspaces", "ws", "workspace.json"))
	if err != nil {
		t.Fatalf("read raw manifest: %v", err)
	}
	if !strings.Contains(string(raw), `"egress_mode"`) {
		t.Fatalf("egress_mode missing from manifest JSON: %s", raw)
	}
	if !strings.Contains(string(raw), `"egress_allow"`) {
		t.Fatalf("egress_allow missing from manifest JSON: %s", raw)
	}
	if !strings.Contains(string(raw), `"egress_passthrough"`) {
		t.Fatalf("egress_passthrough missing from manifest JSON: %s", raw)
	}
}

func TestRequestThreadsEgress(t *testing.T) {
	opts := Options{Name: "a", Backend: vmkit.BackendLinuxKVM, KernelPath: "/k", StateDir: t.TempDir(),
		Network: vmkit.NetworkConfig{Mode: "user"}, EgressMode: "mitm", EgressAllow: []string{"api.github.com"},
		EgressPassthrough: []string{"raw.example.com"}}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.EgressMode != "mitm" || len(req.Config.EgressAllow) != 1 || req.Config.EgressAllow[0] != "api.github.com" {
		t.Fatalf("egress not threaded: %+v", req.Config)
	}
	if len(req.Config.EgressPassthrough) != 1 || req.Config.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("EgressPassthrough not threaded to vmkit.Config: %+v", req.Config)
	}
}

// TestEgressDefaultsToGuarded asserts an empty EgressMode normalizes to
// "broker" at the Request chokepoint — the secure default. Both the config
// passed to the supervisor and the CA-cert vsock listener must reflect it.
// TestEgressDefaultsToBroker: an empty egress mode resolves to the broker
// default, which mediates but forges no certificates — so it allocates NO
// CA-cert listener or port. This is the S4 default flip (previously empty
// resolved to the cert-forging guarded default).
func TestEgressDefaultsToBroker(t *testing.T) {
	opts := Options{Name: "a", Backend: vmkit.BackendLinuxKVM, KernelPath: "/k", StateDir: t.TempDir(),
		Network: vmkit.NetworkConfig{Mode: "user"}, EgressMode: ""}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.EgressMode != vmkit.EgressModeBroker {
		t.Fatalf("empty EgressMode should resolve to %q, got %q", vmkit.EgressModeBroker, req.Config.EgressMode)
	}
	if req.Config.CACertPort != 0 {
		t.Fatalf("broker default must not allocate CACertPort, got %d", req.Config.CACertPort)
	}
	if hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("broker default must not allocate a CACertTarget vsock listener: %+v", req.Config.VsockListeners)
	}
}

// TestRequestAllocatesCACertForMITM: only the cert-forging mitm mode allocates
// the CA-cert vsock listener and port — the guest must trust the per-workspace
// CA the mitm datapath forges leaves from.
func TestRequestAllocatesCACertForMITM(t *testing.T) {
	opts := Options{Name: "a", Backend: vmkit.BackendLinuxKVM, KernelPath: "/k", StateDir: t.TempDir(),
		Network: vmkit.NetworkConfig{Mode: "user"}, EgressMode: vmkit.EgressModeMITM}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.CACertPort != DefaultCACertPort {
		t.Errorf("mitm: CACertPort = %d, want %d", req.Config.CACertPort, DefaultCACertPort)
	}
	if !hasCACertListener(req.Config.VsockListeners) {
		t.Errorf("mitm: missing CACertTarget vsock listener: %+v", req.Config.VsockListeners)
	}
}

// TestRequestNoCACertForBroker confirms a broker-mode workspace mediates but
// delivers NO CA to the guest — the broker splices opaquely and forges no
// certificates, so a CA-cert listener would tell the guest to trust a CA that
// is never used.
func TestRequestNoCACertForBroker(t *testing.T) {
	opts := Options{Name: "a", Backend: vmkit.BackendLinuxKVM, KernelPath: "/k", StateDir: t.TempDir(),
		Network: vmkit.NetworkConfig{Mode: "user"}, EgressMode: vmkit.EgressModeBroker}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.EgressMode != vmkit.EgressModeBroker {
		t.Fatalf("EgressMode should stay %q, got %q", vmkit.EgressModeBroker, req.Config.EgressMode)
	}
	if req.Config.CACertPort != 0 {
		t.Fatalf("broker workspace must not allocate CACertPort, got %d", req.Config.CACertPort)
	}
	if hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("broker workspace must not allocate a CACertTarget vsock listener: %+v", req.Config.VsockListeners)
	}
}

// TestRequestNoCACertForOff confirms an explicit "off" workspace gets no CA-cert
// listener or port — mediation is disabled.
func TestRequestNoCACertForOff(t *testing.T) {
	opts := Options{Name: "a", Backend: vmkit.BackendLinuxKVM, KernelPath: "/k", StateDir: t.TempDir(),
		Network: vmkit.NetworkConfig{Mode: "user"}, EgressMode: vmkit.EgressModeOff}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.EgressMode != vmkit.EgressModeOff {
		t.Fatalf("EgressMode should stay %q, got %q", vmkit.EgressModeOff, req.Config.EgressMode)
	}
	if req.Config.CACertPort != 0 {
		t.Fatalf("off workspace should not allocate CACertPort, got %d", req.Config.CACertPort)
	}
	if hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("off workspace should not allocate a CACertTarget vsock listener: %+v", req.Config.VsockListeners)
	}
}

// TestRequestNoCACertForNonMediatedNetworkMode asserts that the isolated network
// mode (no egress) does NOT allocate the CA-cert vsock listener or port even
// when EgressMode is guarded/strict. The guest is isolated — there is no
// mediator — so allocating the CA-cert listener would tell the guest to trust a
// CA for a mediator that will never exist (dead state).
func TestRequestNoCACertForNonMediatedNetworkMode(t *testing.T) {
	cases := []struct {
		name       string
		network    vmkit.NetworkConfig
		egressMode string
	}{
		{"isolated-broker", vmkit.NetworkConfig{Mode: "isolated"}, vmkit.EgressModeBroker},
		{"isolated-mitm", vmkit.NetworkConfig{Mode: "isolated"}, vmkit.EgressModeMITM},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Name: "a", Backend: vmkit.BackendLinuxKVM, KernelPath: "/k", StateDir: t.TempDir(),
				Network: tc.network, EgressMode: tc.egressMode}
			req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			if req.Config.CACertPort != 0 {
				t.Errorf("%s: CACertPort = %d, want 0 (isolated network mode)", tc.name, req.Config.CACertPort)
			}
			if hasCACertListener(req.Config.VsockListeners) {
				t.Errorf("%s: should not allocate a CACertTarget vsock listener: %+v", tc.name, req.Config.VsockListeners)
			}
		})
	}
}

// TestRequestAllocatesCACertForMediatedNetworkModes confirms the mediatable
// network modes (user and the empty default) DO allocate the CA-cert listener
// when egress mediation is on — guarding that the network-mode gate did not
// over-tighten and suppress the listener for modes that actually run the
// mediator.
func TestRequestAllocatesCACertForMediatedNetworkModes(t *testing.T) {
	for _, mode := range []string{"", "user"} {
		opts := Options{Name: "a", Backend: vmkit.BackendLinuxKVM, KernelPath: "/k", StateDir: t.TempDir(),
			Network: vmkit.NetworkConfig{Mode: mode}, EgressMode: vmkit.EgressModeMITM}
		req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
		if err != nil {
			t.Fatalf("network mode %q: Request: %v", mode, err)
		}
		if req.Config.CACertPort != DefaultCACertPort {
			t.Errorf("network mode %q: CACertPort = %d, want %d", mode, req.Config.CACertPort, DefaultCACertPort)
		}
		if !hasCACertListener(req.Config.VsockListeners) {
			t.Errorf("network mode %q: missing CACertTarget vsock listener: %+v", mode, req.Config.VsockListeners)
		}
	}
}

// TestRequestFailsClosedOnRemovedNetworkMode asserts that Request returns an
// error when a removed network mode (e.g. bridged) is requested — the mode is no
// longer supported, so the start fails closed instead of running unmediated.
func TestRequestFailsClosedOnRemovedNetworkMode(t *testing.T) {
	for _, egressMode := range []string{"", "broker", "mitm"} {
		opts := Options{
			Name:       "a",
			Backend:    vmkit.BackendLinuxKVM,
			KernelPath: "/k",
			StateDir:   t.TempDir(),
			Network:    vmkit.NetworkConfig{Mode: "bridged"},
			EgressMode: egressMode,
		}
		_, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
		if err == nil {
			t.Errorf("EgressMode=%q + network=bridged: Request returned nil, want fail-closed error", egressMode)
		}
	}
}

func hasCACertListener(listeners []vmkit.VsockListener) bool {
	for _, l := range listeners {
		if l.Target == secretxfer.CACertTarget {
			return true
		}
	}
	return false
}

// TestEgressPolicyFromOptionsMapsFields verifies that EgressPolicyFromOptions
// maps the three Options egress fields into an EgressPolicy, and that
// NormalizeEgressPolicy trims/deduplicates Allow and resolves an empty Mode to
// the secure default ("broker").
func TestEgressPolicyFromOptionsMapsFields(t *testing.T) {
	opts := Options{
		EgressMode:        "",
		EgressAllow:       []string{" api.example.com ", "api.example.com"},
		EgressPassthrough: []string{"raw.example.com"},
	}
	pol := vmkit.NormalizeEgressPolicy(EgressPolicyFromOptions(opts))
	if pol.Mode != vmkit.EgressModeBroker {
		t.Fatalf("Mode = %q, want %q", pol.Mode, vmkit.EgressModeBroker)
	}
	if len(pol.Allow) != 1 || pol.Allow[0] != "api.example.com" {
		t.Fatalf("Allow = %v, want [api.example.com] (trimmed and deduped)", pol.Allow)
	}
	if len(pol.Passthrough) != 1 || pol.Passthrough[0] != "raw.example.com" {
		t.Fatalf("Passthrough = %v, want [raw.example.com]", pol.Passthrough)
	}
}

// TestEgressPolicyIsHostSourcedNotAgentInfluenceable is a regression lock that
// codifies ASK Tenets 1 & 18: the egress policy is host-sourced and cannot be
// influenced or changed by guest-controlled inputs. This test is expected to
// PASS on the first run — EgressPolicyFromOptions only reads the three egress
// fields (EgressMode, EgressAllow, EgressPassthrough) and is blind to all other
// Options fields. It guards against a future change that wires a
// guest-controllable field into the policy derivation.
func TestEgressPolicyIsHostSourcedNotAgentInfluenceable(t *testing.T) {
	optsBase := Options{
		EgressMode:        "mitm",
		EgressAllow:       []string{"api.example.com"},
		EgressPassthrough: []string{"passthrough.example.com"},
		// benign non-egress fields
		Name:    "base-agent",
		Backend: vmkit.BackendLinuxKVM,
	}

	optsAdversarial := Options{
		// identical host egress fields
		EgressMode:        "mitm",
		EgressAllow:       []string{"api.example.com"},
		EgressPassthrough: []string{"passthrough.example.com"},
		// guest-controlled fields set to subversion attempts
		Env: map[string]string{
			"EGRESS_MODE":  "off",
			"EGRESS_ALLOW": "evil.example.com",
		},
		Files: []File{
			{SourcePath: "/etc/egress.conf", Path: "/etc/egress-policy.conf"},
		},
		ServiceCommand: "EGRESS_MODE=off /usr/bin/service",
		Entrypoint:     "EGRESS_ALLOW=evil.example.com /usr/bin/entrypoint",
		SetupCommands:  []string{"echo EGRESS_MODE=off > /etc/egress.conf"},
		Hostname:       "evil-egress-override",
		Network: vmkit.NetworkConfig{
			Mode: "user",
			DNS:  []string{"8.8.8.8"},
		},
	}

	polBase := vmkit.NormalizeEgressPolicy(EgressPolicyFromOptions(optsBase))
	polAdversarial := vmkit.NormalizeEgressPolicy(EgressPolicyFromOptions(optsAdversarial))

	if !reflect.DeepEqual(polBase, polAdversarial) {
		t.Fatalf("egress policy differs between base and adversarial opts: base=%+v adversarial=%+v", polBase, polAdversarial)
	}

	// also assert the policy reflects the host-set fields — the DeepEqual above
	// must not be vacuously comparing two zero-value policies.
	if polBase.Mode != "mitm" {
		t.Fatalf("Mode = %q, want %q (host field not reflected)", polBase.Mode, "mitm")
	}
	if len(polBase.Allow) != 1 || polBase.Allow[0] != "api.example.com" {
		t.Fatalf("Allow = %v, want [api.example.com] (host field not reflected)", polBase.Allow)
	}
}

// TestRequestFlatFieldsEqualNormalizedPolicy verifies that the flat Config
// egress fields produced by Request equal the normalized policy — i.e. the
// deduplication and normalization applied by EgressPolicyFromOptions is
// reflected end-to-end in the wire config.
func TestRequestFlatFieldsEqualNormalizedPolicy(t *testing.T) {
	opts := Options{
		Name:              "a",
		Backend:           vmkit.BackendLinuxKVM,
		KernelPath:        "/k",
		StateDir:          t.TempDir(),
		Network:           vmkit.NetworkConfig{Mode: "user"},
		EgressMode:        "",
		EgressAllow:       []string{" api.example.com ", "api.example.com"},
		EgressPassthrough: []string{"raw.example.com"},
	}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.EgressMode != vmkit.EgressModeBroker {
		t.Fatalf("Config.EgressMode = %q, want %q", req.Config.EgressMode, vmkit.EgressModeBroker)
	}
	if len(req.Config.EgressAllow) != 1 || req.Config.EgressAllow[0] != "api.example.com" {
		t.Fatalf("Config.EgressAllow = %v, want [api.example.com] (trimmed and deduped)", req.Config.EgressAllow)
	}
}

func TestRequestWiresBroker(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendLinuxKVM
	opts.Broker = &vmkit.BrokerConfig{
		Upstream:   "https://api.example.com",
		Secret:     vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"},
		BaseURLEnv: map[string]string{"EXAMPLE_BASE_URL": ""},
	}
	req, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(req.Config.Brokers) != 1 {
		t.Fatalf("Config.Brokers not threaded: %+v", req.Config.Brokers)
	}
	if req.Config.Brokers[0].VsockPort != DefaultBrokerPort {
		t.Fatalf("Brokers[0].VsockPort = %d, want default %d", req.Config.Brokers[0].VsockPort, DefaultBrokerPort)
	}
	if req.Config.Brokers[0].GuestListen != DefaultBrokerGuestListen {
		t.Fatalf("Brokers[0].GuestListen = %q, want default %q", req.Config.Brokers[0].GuestListen, DefaultBrokerGuestListen)
	}
	found := false
	for _, l := range req.Config.VsockListeners {
		if l.Port == DefaultBrokerPort && l.Target == "broker://serve" {
			found = true
		}
	}
	if !found {
		t.Fatalf("broker vsock listener missing: %+v", req.Config.VsockListeners)
	}
	// The broker credential is host-side only: it must never join the
	// guest-delivered secret bundle or allocate the guest secrets listener.
	for _, ref := range req.Config.Secrets {
		if ref.Name == "api" {
			t.Fatalf("broker secret leaked into guest-delivered Config.Secrets: %+v", req.Config.Secrets)
		}
	}
	if req.Config.SecretsPort != 0 {
		t.Fatalf("SecretsPort = %d, want 0 (broker-only workspace delivers no guest secrets)", req.Config.SecretsPort)
	}
}

func TestRequestBrokerValidation(t *testing.T) {
	base := func() Options {
		opts := DefaultOptions()
		opts.Name = "ws"
		opts.StateDir = t.TempDir()
		opts.Backend = vmkit.BackendLinuxKVM
		return opts
	}
	missingUpstream := base()
	missingUpstream.Broker = &vmkit.BrokerConfig{Secret: vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"}}
	if _, err := Request(missingUpstream, "", "/tmp/rootfs.ext4", "req-1"); err == nil {
		t.Fatalf("Request accepted broker config with no upstream")
	}
	missingSecret := base()
	missingSecret.Broker = &vmkit.BrokerConfig{Upstream: "https://api.example.com"}
	if _, err := Request(missingSecret, "", "/tmp/rootfs.ext4", "req-1"); err == nil {
		t.Fatalf("Request accepted broker config with no secret ref")
	}
	literalSecret := base()
	literalSecret.Broker = &vmkit.BrokerConfig{Upstream: "https://api.example.com", Secret: vmkit.SecretRef{Name: "api", Ref: "sk-pasted-literal"}}
	if _, err := Request(literalSecret, "", "/tmp/rootfs.ext4", "req-1"); err == nil {
		t.Fatalf("Request accepted a literal (non-reference) broker secret")
	}
}

func TestManifestPersistsBroker(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = dir
	opts.Broker = &vmkit.BrokerConfig{
		Upstream:   "https://api.example.com",
		Secret:     vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"},
		Proxy:      true,
		BaseURLEnv: map[string]string{"EXAMPLE_BASE_URL": ""},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "ws")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	restored := DefaultOptions()
	restored.Name = "ws"
	restored.StateDir = dir
	applyManifest(&restored, manifest)
	if !reflect.DeepEqual(restored.Broker, opts.Broker) {
		t.Fatalf("broker config did not round-trip: %+v != %+v", restored.Broker, opts.Broker)
	}

	// And a broker-less manifest restores to nil (no stale carry-over).
	opts.Broker = nil
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err = ReadManifest(dir, "ws")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	applyManifest(&restored, manifest)
	if restored.Broker != nil {
		t.Fatalf("broker config should restore to nil: %+v", restored.Broker)
	}
}

func TestRequestNoBroker(t *testing.T) {
	opts := Options{Name: "w", StateDir: t.TempDir(), Backend: "linux-kvm"}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.Brokers != nil {
		t.Fatalf("Config.Brokers should be nil when unconfigured")
	}
	for _, l := range req.Config.VsockListeners {
		if l.Target == "broker://serve" {
			t.Fatalf("broker vsock listener present without broker config: %+v", req.Config.VsockListeners)
		}
	}
}
