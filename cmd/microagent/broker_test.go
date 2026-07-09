package main

import (
	"strings"
	"testing"
)

// TestParseWorkspaceOptionsBroker verifies the --broker-* flags parse into
// Options.Broker: upstream, host-side secret reference, base-URL env keys,
// and the proxy toggle.
func TestParseWorkspaceOptionsBroker(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"--name", "ws",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-upstream", "https://api.example.com",
		"--broker-secret", "api=env:MY_TOKEN",
		"--broker-env", "EXAMPLE_BASE_URL",
		"--broker-env", "OTHER_BASE_URL=http://127.0.0.1:18888/v1",
		"--broker-proxy",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Broker == nil {
		t.Fatal("Options.Broker not set")
	}
	if opts.Broker.Upstream != "https://api.example.com" {
		t.Fatalf("Upstream = %q", opts.Broker.Upstream)
	}
	if opts.Broker.Secret.Name != "api" || opts.Broker.Secret.Ref != "env:MY_TOKEN" {
		t.Fatalf("Secret = %+v", opts.Broker.Secret)
	}
	if !opts.Broker.Proxy {
		t.Fatal("Proxy not set")
	}
	if opts.Broker.BaseURLEnv["EXAMPLE_BASE_URL"] != "" {
		t.Fatalf("BaseURLEnv[EXAMPLE_BASE_URL] = %q, want empty (filled with broker URL)", opts.Broker.BaseURLEnv["EXAMPLE_BASE_URL"])
	}
	if opts.Broker.BaseURLEnv["OTHER_BASE_URL"] != "http://127.0.0.1:18888/v1" {
		t.Fatalf("BaseURLEnv[OTHER_BASE_URL] = %q", opts.Broker.BaseURLEnv["OTHER_BASE_URL"])
	}
	// The broker secret is host-side only: it must not join the guest-delivered
	// secrets.
	if _, leaked := opts.Secrets["api"]; leaked {
		t.Fatal("broker secret leaked into Options.Secrets (guest-delivered)")
	}
}

// TestParseWorkspaceOptionsBrokerRejectsLiteral verifies a pasted literal
// fails at parse time, before any state is written, matching --cred-swap.
func TestParseWorkspaceOptionsBrokerRejectsLiteral(t *testing.T) {
	_, err := parseWorkspaceOptions("create", []string{
		"--name", "ws",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-upstream", "https://api.example.com",
		"--broker-secret", "api=sk-real-secret",
	})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted a literal broker secret; want rejection")
	}
	if !strings.Contains(err.Error(), "literal") {
		t.Fatalf("error = %q, want it to explain a literal is rejected", err)
	}
}

// TestParseWorkspaceOptionsBrokerFlagsRequireEachOther verifies a partial
// broker declaration fails loudly rather than silently producing a workspace
// with no broker (or a broker with no credential).
func TestParseWorkspaceOptionsBrokerFlagsRequireEachOther(t *testing.T) {
	if _, err := parseWorkspaceOptions("create", []string{
		"--name", "ws",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-upstream", "https://api.example.com",
	}); err == nil {
		t.Fatal("--broker-upstream without --broker-secret must fail")
	}
	if _, err := parseWorkspaceOptions("create", []string{
		"--name", "ws",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-secret", "api=env:MY_TOKEN",
	}); err == nil {
		t.Fatal("--broker-secret without --broker-upstream must fail")
	}
	if _, err := parseWorkspaceOptions("create", []string{
		"--name", "ws",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-proxy",
	}); err == nil {
		t.Fatal("--broker-proxy without a broker must fail")
	}
}
