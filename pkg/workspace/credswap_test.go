package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/internal/egress"
	"gopkg.in/yaml.v3"
)

func loadGeneratedSwaps(t *testing.T, path string) egress.SwapConfigFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated cred-swap.yaml: %v", err)
	}
	var cfg egress.SwapConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse generated cred-swap.yaml: %v", err)
	}
	return cfg
}

// TestMaterializeCredSwapConfigGeneratesEntryAndAllowlistsHost verifies a single
// --cred-swap provider produces a static swap entry at the durable per-workspace
// path, unions the provider host into EgressAllow, and records only the key
// reference (never a literal secret).
func TestMaterializeCredSwapConfigGeneratesEntryAndAllowlistsHost(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:              "demo",
		StateDir:          dir,
		EgressAllow:       []string{"example.com"},
		CredSwapProviders: []CredSwapProvider{{Provider: "anthropic"}},
	}
	if err := materializeCredSwapConfig(&opts); err != nil {
		t.Fatalf("materializeCredSwapConfig: %v", err)
	}

	wantPath := filepath.Join(dir, "workspaces", "demo", "cred-swap.yaml")
	if opts.EgressSwapConfigPath != wantPath {
		t.Fatalf("EgressSwapConfigPath = %q, want %q", opts.EgressSwapConfigPath, wantPath)
	}
	// The provider host must be allowlisted (alongside the pre-existing host).
	allow := map[string]bool{}
	for _, h := range opts.EgressAllow {
		allow[h] = true
	}
	if !allow["example.com"] || !allow["api.anthropic.com"] {
		t.Fatalf("EgressAllow = %v, want example.com and api.anthropic.com", opts.EgressAllow)
	}

	cfg := loadGeneratedSwaps(t, wantPath)
	entry, ok := cfg.Swaps["anthropic"]
	if !ok {
		t.Fatalf("generated swaps = %v, want an \"anthropic\" entry", cfg.Swaps)
	}
	if entry.Type != "static" || entry.Header != "x-api-key" || entry.KeyRef != "env:ANTHROPIC_API_KEY" {
		t.Fatalf("entry = %+v, want static/x-api-key/env:ANTHROPIC_API_KEY", entry)
	}
	if len(entry.Domains) != 1 || entry.Domains[0] != "api.anthropic.com" {
		t.Fatalf("entry.Domains = %v, want [api.anthropic.com]", entry.Domains)
	}
}

// TestMaterializeCredSwapConfigCustomRef verifies an explicit env reference is
// carried through verbatim and a literal is never written.
func TestMaterializeCredSwapConfigCustomRef(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:              "demo",
		StateDir:          dir,
		CredSwapProviders: []CredSwapProvider{{Provider: "openai", Ref: "env:MY_OPENAI"}},
	}
	if err := materializeCredSwapConfig(&opts); err != nil {
		t.Fatalf("materializeCredSwapConfig: %v", err)
	}
	cfg := loadGeneratedSwaps(t, opts.EgressSwapConfigPath)
	if cfg.Swaps["openai"].KeyRef != "env:MY_OPENAI" {
		t.Fatalf("openai key_ref = %q, want env:MY_OPENAI", cfg.Swaps["openai"].KeyRef)
	}
}

// TestMaterializeCredSwapConfigMergesExisting verifies a generated provider
// entry is merged on top of an operator-supplied --egress-swap-config rather
// than replacing it.
func TestMaterializeCredSwapConfigMergesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "swaps.yaml")
	existingYAML := "swaps:\n  internal:\n    type: static\n    domains: [api.internal.example]\n    header: Authorization\n    format: \"Bearer {key}\"\n    key_ref: env:INTERNAL_TOKEN\n"
	if err := os.WriteFile(existing, []byte(existingYAML), 0o600); err != nil {
		t.Fatalf("write existing swap config: %v", err)
	}
	opts := Options{
		Name:                 "demo",
		StateDir:             dir,
		EgressSwapConfigPath: existing,
		CredSwapProviders:    []CredSwapProvider{{Provider: "anthropic"}},
	}
	if err := materializeCredSwapConfig(&opts); err != nil {
		t.Fatalf("materializeCredSwapConfig: %v", err)
	}
	cfg := loadGeneratedSwaps(t, opts.EgressSwapConfigPath)
	if _, ok := cfg.Swaps["internal"]; !ok {
		t.Fatalf("merged swaps = %v, want the existing \"internal\" entry preserved", cfg.Swaps)
	}
	if _, ok := cfg.Swaps["anthropic"]; !ok {
		t.Fatalf("merged swaps = %v, want the generated \"anthropic\" entry added", cfg.Swaps)
	}
}

// TestMaterializeCredSwapConfigCollision verifies a generated provider name that
// collides with an existing swap entry is an error, never a silent overwrite.
func TestMaterializeCredSwapConfigCollision(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "swaps.yaml")
	existingYAML := "swaps:\n  anthropic:\n    type: static\n    domains: [api.anthropic.com]\n    header: x-api-key\n    format: \"{key}\"\n    key_ref: env:SOMETHING_ELSE\n"
	if err := os.WriteFile(existing, []byte(existingYAML), 0o600); err != nil {
		t.Fatalf("write existing swap config: %v", err)
	}
	opts := Options{
		Name:                 "demo",
		StateDir:             dir,
		EgressSwapConfigPath: existing,
		CredSwapProviders:    []CredSwapProvider{{Provider: "anthropic"}},
	}
	err := materializeCredSwapConfig(&opts)
	if err == nil {
		t.Fatal("materializeCredSwapConfig overwrote a colliding entry; want an error")
	}
	if !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("error = %q, want it to flag the collision", err)
	}
}

// TestMaterializeCredSwapConfigNoOp verifies the helper does nothing when no
// providers are declared (the common case): no file, no egress mutation.
func TestMaterializeCredSwapConfigNoOp(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "demo", StateDir: dir, EgressAllow: []string{"example.com"}}
	if err := materializeCredSwapConfig(&opts); err != nil {
		t.Fatalf("materializeCredSwapConfig: %v", err)
	}
	if opts.EgressSwapConfigPath != "" {
		t.Fatalf("EgressSwapConfigPath = %q, want empty", opts.EgressSwapConfigPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspaces", "demo", "cred-swap.yaml")); !os.IsNotExist(err) {
		t.Fatalf("cred-swap.yaml should not exist, stat err = %v", err)
	}
}

// TestMaterializeCredSwapConfigRejectsEgressOff is the library backstop: cred-swap
// with egress off can never inject (no mediator runs), so it must fail loud even
// for a direct Go-API caller that bypasses the CLI guard. No file is written.
func TestMaterializeCredSwapConfigRejectsEgressOff(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:              "demo",
		StateDir:          dir,
		EgressMode:        "off",
		CredSwapProviders: []CredSwapProvider{{Provider: "anthropic"}},
	}
	err := materializeCredSwapConfig(&opts)
	if err == nil {
		t.Fatal("materializeCredSwapConfig accepted cred-swap with egress off; want rejection")
	}
	if !strings.Contains(err.Error(), "guarded or strict") {
		t.Fatalf("error = %q, want it to require guarded or strict", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "workspaces", "demo", "cred-swap.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("cred-swap.yaml should not be written on rejection, stat err = %v", statErr)
	}
}
