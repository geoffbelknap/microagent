package workspace

import (
	"path/filepath"
	"strings"
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

func TestBuildRootfsRequestBakesBrokerGuestEnv(t *testing.T) {
	opts := Options{
		Name:         "research",
		StateDir:     "/tmp/microagent",
		ImageRef:     "docker.io/library/ubuntu:24.04",
		Architecture: "arm64",
		// Brokers are gated on the backend capability at request-build time,
		// so the fixture must name a backend that serves broker endpoints.
		Backend: vmkit.BackendLinuxKVM,
		Env:     map[string]string{"FOO": "bar"},
		Broker: &vmkit.BrokerConfig{
			Upstream:   "https://api.example.com",
			Secret:     vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"},
			BaseURLEnv: map[string]string{"EXAMPLE_BASE_URL": ""},
		},
	}
	req, err := rootfsRequest(opts, "/tmp/microagent/workspaces/research/rootfs.ext4")
	if err != nil {
		t.Fatalf("rootfsRequest: %v", err)
	}
	if req.Env["FOO"] != "bar" {
		t.Fatalf("operator env not preserved: %v", req.Env)
	}
	wantBridge := DefaultBrokerGuestListen + "=1032"
	if req.Env["MICROAGENT_VSOCK_TCP_LISTENERS"] != wantBridge {
		t.Fatalf("bridge env = %q, want %q", req.Env["MICROAGENT_VSOCK_TCP_LISTENERS"], wantBridge)
	}
	if req.Env["EXAMPLE_BASE_URL"] != "http://"+DefaultBrokerGuestListen {
		t.Fatalf("base URL env = %q", req.Env["EXAMPLE_BASE_URL"])
	}
	if opts.Env["MICROAGENT_VSOCK_TCP_LISTENERS"] != "" {
		t.Fatal("caller's Env map mutated")
	}

	// Invalid broker config fails the build request, not silently skipped.
	bad := opts
	bad.Broker = &vmkit.BrokerConfig{Upstream: "https://api.example.com", Secret: vmkit.SecretRef{Name: "api", Ref: "sk-literal"}}
	if _, err := rootfsRequest(bad, "/tmp/microagent/workspaces/research/rootfs.ext4"); err == nil {
		t.Fatal("literal broker secret must fail the rootfs request")
	}

	// No broker: env passes through untouched.
	plain := opts
	plain.Broker = nil
	req, err = rootfsRequest(plain, "/tmp/microagent/workspaces/research/rootfs.ext4")
	if err != nil {
		t.Fatalf("rootfsRequest: %v", err)
	}
	if len(req.Env) != 1 || req.Env["FOO"] != "bar" {
		t.Fatalf("no-broker env = %v, want only FOO", req.Env)
	}
}

func TestBuildRootfsRequestCarriesFinalConfigForSetupCreates(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "research",
		StateDir:        "/tmp/microagent",
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "arm64",
		SetupCommands:   []string{"echo setup"},
		Entrypoint:      "/app/entrypoint.sh",
		PrepareForStart: true,
	}, "/tmp/microagent/workspaces/research/rootfs.ext4")

	if !req.ResetFinalConfig {
		t.Fatal("setup creates must request a guest config reset from the builder")
	}
	if strings.Join(req.FinalCommand, " ") != "/bin/sh -lc /app/entrypoint.sh" || req.FinalMode != "" {
		t.Fatalf("final = %#v mode %q", req.FinalCommand, req.FinalMode)
	}
	if strings.Contains(strings.Join(req.Command, " "), "/etc/microagent/run.json") {
		t.Fatalf("setup script should not embed guest config reset: %#v", req.Command)
	}
}
