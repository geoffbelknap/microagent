package workspace

import (
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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

func TestBuildRootfsRequestSetsLocalImageLayout(t *testing.T) {
	opts := Options{
		Name:         "research",
		StateDir:     "/tmp/microagent",
		ImageRef:     "docker.io/library/ubuntu:24.04",
		Architecture: "arm64",
		SizeMiB:      1024,
	}
	req := buildRootfsRequest(opts, "/tmp/microagent/workspaces/research/rootfs.ext4")

	// Same value commit.LayoutPath(opts.StateDir) produces; asserted as a
	// literal rather than by importing pkg/commit, since pkg/commit imports
	// pkg/workspace (importing it back here would be a cycle).
	want := filepath.Join(opts.StateDir, "images", "oci")
	if req.LocalImageLayout != want {
		t.Fatalf("LocalImageLayout = %q, want %q", req.LocalImageLayout, want)
	}
}

// TestGuestBootConfigMergesBrokerGuestEnv: broker env is merged into the
// per-boot guest config (fail-closed on invalid broker config) — nothing is
// baked into the rootfs anymore, so a broker added after create takes
// effect on the next start without a rebuild.
func TestGuestBootConfigMergesBrokerGuestEnv(t *testing.T) {
	opts := Options{
		Name:         "research",
		StateDir:     "/tmp/microagent",
		ImageRef:     "docker.io/library/ubuntu:24.04",
		Architecture: "arm64",
		// Brokers are gated on the backend capability, so the fixture must
		// name a backend that serves broker endpoints.
		Backend: vmkit.BackendLinuxKVM,
		Env:     map[string]string{"FOO": "bar"},
		Broker: &vmkit.BrokerConfig{
			Upstream:   "https://api.example.com",
			Secret:     vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"},
			BaseURLEnv: map[string]string{"EXAMPLE_BASE_URL": ""},
			Assurance:  vmkit.BrokerAssuranceTrustedUpstream,
		},
	}
	cfg, err := GuestBootConfig(opts)
	if err != nil {
		t.Fatalf("GuestBootConfig: %v", err)
	}
	env := envMap(cfg.Env)
	if env["FOO"] != "bar" {
		t.Fatalf("operator env not preserved: %v", cfg.Env)
	}
	wantBridge := DefaultBrokerGuestListen + "=1032"
	if env["MICROAGENT_VSOCK_TCP_LISTENERS"] != wantBridge {
		t.Fatalf("bridge env = %q, want %q", env["MICROAGENT_VSOCK_TCP_LISTENERS"], wantBridge)
	}
	if env["EXAMPLE_BASE_URL"] != "http://"+DefaultBrokerGuestListen {
		t.Fatalf("base URL env = %q", env["EXAMPLE_BASE_URL"])
	}
	if opts.Env["MICROAGENT_VSOCK_TCP_LISTENERS"] != "" {
		t.Fatal("caller's Env map mutated")
	}

	// Invalid broker config fails the boot config, not silently skipped.
	bad := opts
	bad.Broker = &vmkit.BrokerConfig{Upstream: "https://api.example.com", Secret: vmkit.SecretRef{Name: "api", Ref: "sk-literal"}}
	if _, err := GuestBootConfig(bad); err == nil {
		t.Fatal("literal broker secret must fail the boot config")
	}

	// No broker: env passes through untouched.
	plain := opts
	plain.Broker = nil
	cfg, err = GuestBootConfig(plain)
	if err != nil {
		t.Fatalf("GuestBootConfig: %v", err)
	}
	if len(cfg.Env) != 1 || cfg.Env[0] != "FOO=bar" {
		t.Fatalf("no-broker env = %v, want only FOO", cfg.Env)
	}
}

// TestBuildRootfsRequestCarriesNothingPerWorkspace pins the config-disk
// contract from the request side: the build request depends only on the
// image, init binary, and size — no command, env, files, ports, or
// console shell — so two workspaces of the same image build identical
// rootfs bytes.
func TestBuildRootfsRequestCarriesNothingPerWorkspace(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "research",
		StateDir:        "/tmp/microagent",
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "arm64",
		SetupCommands:   []string{"echo setup"},
		Entrypoint:      "/app/entrypoint.sh",
		Env:             map[string]string{"SECRET": "value"},
		ConsoleShell:    "/bin/bash",
		PrepareForStart: true,
	}, "/tmp/microagent/workspaces/research/rootfs.ext4")

	if len(req.Command) != 0 || len(req.Env) != 0 || len(req.Files) != 0 || req.ConsoleShell != "" {
		t.Fatalf("per-workspace content leaked into the build request: %+v", req)
	}
}
