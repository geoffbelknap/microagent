package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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

func TestApplySpecParsesModelRef(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "microagent.yaml")
	if err := os.WriteFile(specPath, []byte(`
name: demo
image: docker.io/library/ubuntu:24.04
model: Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf
modelRunner:
  backend: vllm
  gpu: on
  backendModel: Qwen/Qwen2.5-0.5B-Instruct
  servedModel: local-chat
  args: ["--max-model-len", "2048"]
modelMediation:
  mode: policy
  policyFile: model-policy.json
  policyTimeout: 250ms
`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	if err := ApplySpecFile(&opts, specPath, SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpecFile: %v", err)
	}
	if opts.Model != "Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf" {
		t.Fatalf("spec model not applied: %q", opts.Model)
	}
	if opts.ModelRunner.Backend != "vllm" || opts.ModelRunner.BackendModel != "Qwen/Qwen2.5-0.5B-Instruct" || opts.ModelRunner.ServedModel != "local-chat" {
		t.Fatalf("spec model runner not applied: %+v", opts.ModelRunner)
	}
	wantPolicy := filepath.Join(filepath.Dir(specPath), "model-policy.json")
	if opts.ModelMediation.Mode != "policy" || opts.ModelMediation.PolicyFile != wantPolicy || opts.ModelMediation.PolicyTimeout != "250ms" {
		t.Fatalf("spec model mediation not applied: %+v", opts.ModelMediation)
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
	result, err := Apply(t.Context(), Options{StateDir: dir, Backend: vmkit.BackendLinuxKVM}, Spec{
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

func TestApplyReturnsStructuredNetworkCapabilityGap(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		StateDir: dir,
		Name:     "homebridge",
		Network: vmkit.NetworkConfig{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
		},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(t.Context(), Options{StateDir: dir, Backend: "unsupported"}, Spec{
		Name: "homebridge",
		Network: NetworkSpec{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", Host: "0.0.0.0", HostPort: 8581, GuestPort: 8581}},
		},
	})
	var gap vmkit.UnsupportedFeatureError
	if !errors.As(err, &gap) {
		t.Fatalf("Apply error = %v (%T), want UnsupportedFeatureError", err, err)
	}
	if gap.Capability != vmkit.FeatureCapabilityNetworkPublish || gap.Operation != "persist network configuration" {
		t.Fatalf("Apply gap = %#v", gap)
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
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	req.Config.Network = &originalNetwork
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(t.Context(), Options{StateDir: dir, Backend: vmkit.BackendLinuxKVM}, Spec{
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
