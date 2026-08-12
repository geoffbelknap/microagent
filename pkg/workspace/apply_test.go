package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestOptionsFromManifestThreadsEgressToConfig is a regression guard for the
// full manifest -> Options -> vmkit.Config path: a manifest declaring an egress
// mode, allowlist, and passthrough set must produce a Config carrying all three
// intact (the Config is what the supervisor hands to the proxy as
// --allow/--passthrough). The narrower TestManifestRoundTripPreservesEgress
// stops at Options; this confirms nothing is dropped at the Request chokepoint.
func TestOptionsFromManifestThreadsEgressToConfig(t *testing.T) {
	base := Options{
		Name:       "ws",
		Backend:    vmkit.BackendLinuxKVM,
		KernelPath: "/k",
		StateDir:   t.TempDir(),
		Network:    vmkit.NetworkConfig{Mode: "user"},
	}
	manifest := Manifest{
		Network:           NetworkSpec{Mode: "user"},
		EgressMode:        vmkit.EgressModeMITM,
		EgressAllow:       []string{"api.github.com", ".example.com"},
		EgressPassthrough: []string{"raw.example.com"},
	}

	opts := OptionsFromManifest(base, manifest)
	if opts.EgressMode != vmkit.EgressModeMITM {
		t.Fatalf("OptionsFromManifest dropped EgressMode: %q", opts.EgressMode)
	}
	if len(opts.EgressAllow) != 2 || opts.EgressAllow[0] != "api.github.com" || opts.EgressAllow[1] != ".example.com" {
		t.Fatalf("OptionsFromManifest dropped EgressAllow: %v", opts.EgressAllow)
	}
	if len(opts.EgressPassthrough) != 1 || opts.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("OptionsFromManifest dropped EgressPassthrough: %v", opts.EgressPassthrough)
	}

	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Config.EgressMode != vmkit.EgressModeMITM {
		t.Fatalf("Config dropped EgressMode: %q", req.Config.EgressMode)
	}
	if len(req.Config.EgressAllow) != 2 || req.Config.EgressAllow[0] != "api.github.com" || req.Config.EgressAllow[1] != ".example.com" {
		t.Fatalf("Config dropped EgressAllow: %v", req.Config.EgressAllow)
	}
	if len(req.Config.EgressPassthrough) != 1 || req.Config.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("Config dropped EgressPassthrough: %v", req.Config.EgressPassthrough)
	}
}

func TestApplyEgressPolicyPreservesUnrelatedManifestFields(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.StateDir, opts.Name = dir, "governed"
	opts.Purpose = "preserve-me"
	opts.SetupComplete = true
	opts.Verification = &vmkit.RuntimeVerification{OK: true, ImageRef: "example/image:tag"}
	opts.EgressMode = vmkit.EgressModeMITM
	opts.EgressAllow = []string{"old.example"}
	opts.EgressPassthrough = []string{"passthrough.example"}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	before, err := ReadManifest(dir, opts.Name)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Apply(context.Background(), Options{StateDir: dir, Backend: opts.Backend}, Spec{
		Name: opts.Name,
		Agent: AgentSpec{
			Egress: vmkit.EgressModeBroker, Allow: []string{" gateway.internal ", "gateway.internal"}, LockAllowlist: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Applied, []string{"egress"}) {
		t.Fatalf("applied = %v", result.Applied)
	}
	after, err := ReadManifest(dir, opts.Name)
	if err != nil {
		t.Fatal(err)
	}
	if after.EgressMode != vmkit.EgressModeBroker || !after.EgressAllowlistLocked ||
		!reflect.DeepEqual(after.EgressAllow, []string{"gateway.internal"}) || len(after.EgressPassthrough) != 0 {
		t.Fatalf("egress policy = mode=%q allow=%v passthrough=%v locked=%t", after.EgressMode, after.EgressAllow, after.EgressPassthrough, after.EgressAllowlistLocked)
	}
	after.EgressMode = before.EgressMode
	after.EgressAllow = before.EgressAllow
	after.EgressPassthrough = before.EgressPassthrough
	after.EgressAllowlistLocked = before.EgressAllowlistLocked
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("egress apply changed unrelated manifest fields:\n before=%+v\n after=%+v", before, after)
	}
}

func TestApplyEgressPolicyRefusesLiveWorkspaceWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.StateDir, opts.Name = dir, "running-agent"
	opts.EgressMode = vmkit.EgressModeBroker
	opts.EgressAllow = []string{"old.example"}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(dir, opts.Name)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(RuntimeState{Event: EventFile{Identity: vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend}, State: vmkit.StateRunning}, PID: os.Getpid()})
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "workspaces", opts.Name, "workspace.json")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(context.Background(), Options{StateDir: dir, Backend: opts.Backend}, Spec{
		Name: opts.Name, Agent: AgentSpec{Egress: vmkit.EgressModeBroker, Allow: []string{"gateway.internal"}, LockAllowlist: true},
	})
	if err == nil || !strings.Contains(err.Error(), "live egress apply is not supported") {
		t.Fatalf("live apply error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("live egress refusal changed the manifest")
	}
}
