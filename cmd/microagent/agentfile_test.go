package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEgressOffWarning verifies the "yolo mode" notice fires only when egress is
// actually off, so operators know they've turned off network mediation.
func TestEgressOffWarning(t *testing.T) {
	if w := egressOffWarning("off"); w == "" || !strings.Contains(strings.ToLower(w), "egress") {
		t.Fatalf("egressOffWarning(off) = %q, want a non-empty egress notice", w)
	}
	for _, mode := range []string{"guarded", "strict", ""} {
		if w := egressOffWarning(mode); w != "" {
			t.Fatalf("egressOffWarning(%q) = %q, want no warning", mode, w)
		}
	}
}

func writeAgentfile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestParseWorkspaceOptionsAgentfile verifies an Agentfile (a Spec with an
// agent: block) loaded via --file populates exec, egress, allow, and cred-swap.
func TestParseWorkspaceOptionsAgentfile(t *testing.T) {
	path := writeAgentfile(t, `
name: claude-agent
image: docker.io/library/python:3.12-slim
agent:
  entry: python /app/agent.py
  egress: strict
  allow: [api.anthropic.com]
  cred-swap: [anthropic]
`)
	opts, err := parseWorkspaceOptions("dispatch", []string{"--file", path})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ExecCommand != "python /app/agent.py" {
		t.Fatalf("ExecCommand = %q, want the agent entry", opts.ExecCommand)
	}
	if opts.EgressMode != "strict" {
		t.Fatalf("EgressMode = %q, want strict", opts.EgressMode)
	}
	got := map[string]bool{}
	for _, h := range opts.EgressAllow {
		got[h] = true
	}
	if !got["api.anthropic.com"] {
		t.Fatalf("EgressAllow = %v, want api.anthropic.com", opts.EgressAllow)
	}
	if len(opts.CredSwapProviders) != 1 || opts.CredSwapProviders[0].Provider != "anthropic" {
		t.Fatalf("CredSwapProviders = %+v, want anthropic", opts.CredSwapProviders)
	}
}

// TestParseWorkspaceOptionsAgentfileFlagOverridesEgress verifies an explicit
// --egress flag wins over the Agentfile's egress mode (flags > spec).
func TestParseWorkspaceOptionsAgentfileFlagOverridesEgress(t *testing.T) {
	path := writeAgentfile(t, `
name: demo
image: docker.io/library/python:3.12-slim
agent:
  entry: python /app/agent.py
  egress: guarded
`)
	opts, err := parseWorkspaceOptions("dispatch", []string{"--file", path, "--egress", "strict"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.EgressMode != "strict" {
		t.Fatalf("EgressMode = %q, want the flag value strict to override the spec", opts.EgressMode)
	}
}

// TestParseWorkspaceOptionsAgentfileFlagOverridesEntry verifies an explicit
// --exec flag wins over the Agentfile's entry.
func TestParseWorkspaceOptionsAgentfileFlagOverridesEntry(t *testing.T) {
	path := writeAgentfile(t, `
name: demo
image: docker.io/library/python:3.12-slim
agent:
  entry: python /app/agent.py
`)
	opts, err := parseWorkspaceOptions("dispatch", []string{"--file", path, "--exec", "echo override"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ExecCommand != "echo override" {
		t.Fatalf("ExecCommand = %q, want the flag value to override the spec", opts.ExecCommand)
	}
}

// TestParseWorkspaceOptionsAgentfileCredSwapUnions verifies a flag --cred-swap
// is unioned with the Agentfile's cred-swap, not replaced.
func TestParseWorkspaceOptionsAgentfileCredSwapUnions(t *testing.T) {
	path := writeAgentfile(t, `
name: demo
image: docker.io/library/python:3.12-slim
agent:
  entry: python /app/agent.py
  egress: strict
  cred-swap: [anthropic]
`)
	opts, err := parseWorkspaceOptions("dispatch", []string{"--file", path, "--cred-swap", "openai"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	names := map[string]bool{}
	for _, p := range opts.CredSwapProviders {
		names[p.Provider] = true
	}
	if !names["anthropic"] || !names["openai"] {
		t.Fatalf("CredSwapProviders = %+v, want both anthropic (spec) and openai (flag)", opts.CredSwapProviders)
	}
}

// TestParseWorkspaceOptionsAgentfileEgressOffCredSwap verifies the off+cred-swap
// contradiction is caught even when both arrive via the Agentfile.
func TestParseWorkspaceOptionsAgentfileEgressOffCredSwap(t *testing.T) {
	path := writeAgentfile(t, `
name: demo
image: docker.io/library/python:3.12-slim
agent:
  entry: python /app/agent.py
  egress: off
  cred-swap: [anthropic]
`)
	_, err := parseWorkspaceOptions("dispatch", []string{"--file", path})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted egress off + cred-swap from a spec; want rejection")
	}
	if !strings.Contains(err.Error(), "guarded or strict") {
		t.Fatalf("error = %q, want it to require guarded or strict", err)
	}
}
