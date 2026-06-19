package workspace

import (
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
		EgressMode:        vmkit.EgressModeStrict,
		EgressAllow:       []string{"api.github.com", ".example.com"},
		EgressPassthrough: []string{"raw.example.com"},
	}

	opts := OptionsFromManifest(base, manifest)
	if opts.EgressMode != vmkit.EgressModeStrict {
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
	if req.Config.EgressMode != vmkit.EgressModeStrict {
		t.Fatalf("Config dropped EgressMode: %q", req.Config.EgressMode)
	}
	if len(req.Config.EgressAllow) != 2 || req.Config.EgressAllow[0] != "api.github.com" || req.Config.EgressAllow[1] != ".example.com" {
		t.Fatalf("Config dropped EgressAllow: %v", req.Config.EgressAllow)
	}
	if len(req.Config.EgressPassthrough) != 1 || req.Config.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("Config dropped EgressPassthrough: %v", req.Config.EgressPassthrough)
	}
}
