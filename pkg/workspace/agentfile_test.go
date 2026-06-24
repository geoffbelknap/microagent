package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplySpecAgentBlockPopulatesOptions verifies the agent: block maps to the
// three Options fields the Spec otherwise cannot express: the one-shot exec
// command, the egress envelope (mode + extra allowlist), and cred-swap.
func TestApplySpecAgentBlockPopulatesOptions(t *testing.T) {
	spec := Spec{
		Name:     "claude-agent",
		ImageRef: "docker.io/library/python:3.12-slim",
		Agent: AgentSpec{
			Entry:    "python /app/agent.py",
			Egress:   "strict",
			Allow:    []string{"api.anthropic.com"},
			CredSwap: []string{"anthropic"},
		},
	}
	opts := DefaultOptions()
	if err := ApplySpec(&opts, spec, t.TempDir(), SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpec: %v", err)
	}
	if opts.ExecCommand != "python /app/agent.py" {
		t.Fatalf("ExecCommand = %q, want the agent entry", opts.ExecCommand)
	}
	if opts.EgressMode != "strict" {
		t.Fatalf("EgressMode = %q, want strict", opts.EgressMode)
	}
	found := false
	for _, h := range opts.EgressAllow {
		if h == "api.anthropic.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("EgressAllow = %v, want api.anthropic.com unioned in", opts.EgressAllow)
	}
	if len(opts.CredSwapProviders) != 1 || opts.CredSwapProviders[0].Provider != "anthropic" {
		t.Fatalf("CredSwapProviders = %+v, want one anthropic entry", opts.CredSwapProviders)
	}
}

// TestApplySpecAgentEntryDefersToExisting verifies a pre-set ExecCommand (e.g.
// from a CLI --exec, applied before the spec) is not clobbered by agent.entry —
// flags win, consistent with the rest of ApplySpec.
func TestApplySpecAgentEntryDefersToExisting(t *testing.T) {
	spec := Spec{Agent: AgentSpec{Entry: "python /app/agent.py"}}
	opts := DefaultOptions()
	opts.ExecCommand = "echo override"
	if err := ApplySpec(&opts, spec, t.TempDir(), SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpec: %v", err)
	}
	if opts.ExecCommand != "echo override" {
		t.Fatalf("ExecCommand = %q, want the pre-set value preserved", opts.ExecCommand)
	}
}

// TestApplySpecAgentEgressInvalid verifies an unknown egress mode in the agent
// block is rejected (not silently coerced to the default), so a typo fails loud.
func TestApplySpecAgentEgressInvalid(t *testing.T) {
	spec := Spec{Agent: AgentSpec{Egress: "stict"}}
	opts := DefaultOptions()
	err := ApplySpec(&opts, spec, t.TempDir(), SpecApplyOptions{})
	if err == nil {
		t.Fatal("ApplySpec accepted an invalid agent egress mode; want rejection")
	}
	if !strings.Contains(err.Error(), "guarded") {
		t.Fatalf("error = %q, want it to list the valid modes", err)
	}
}

// TestApplySpecAgentCredSwapLiteralRejected verifies a literal secret in an
// agent cred-swap entry is rejected at spec-apply time, before any file write.
func TestApplySpecAgentCredSwapLiteralRejected(t *testing.T) {
	spec := Spec{Agent: AgentSpec{CredSwap: []string{"anthropic=sk-ant-realsecret"}}}
	opts := DefaultOptions()
	err := ApplySpec(&opts, spec, t.TempDir(), SpecApplyOptions{})
	if err == nil {
		t.Fatal("ApplySpec accepted a literal cred-swap secret; want rejection")
	}
	if !strings.Contains(err.Error(), "literal") {
		t.Fatalf("error = %q, want it to explain a literal is rejected", err)
	}
}

// TestApplySpecAgentUnknownFieldRejected verifies the strict YAML decode
// (KnownFields) catches a misspelled agent subfield with a friendly error.
func TestApplySpecAgentUnknownFieldRejected(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(specPath, []byte(`
name: demo
image: docker.io/library/python:3.12-slim
agent:
  entry: python /app/agent.py
  egres: strict
`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	err := ApplySpecFile(&opts, specPath, SpecApplyOptions{})
	if err == nil {
		t.Fatal("ApplySpecFile accepted an unknown agent subfield; want rejection")
	}
	if !strings.Contains(err.Error(), "egres") {
		t.Fatalf("error = %q, want it to name the unknown field", err)
	}
}

// TestApplySpecAgentCredSwapAppends verifies agent cred-swap entries append to
// any already on Options (e.g. from a CLI flag applied earlier), not replace.
func TestApplySpecAgentCredSwapAppends(t *testing.T) {
	spec := Spec{Agent: AgentSpec{CredSwap: []string{"openai"}}}
	opts := DefaultOptions()
	opts.CredSwapProviders = []CredSwapProvider{{Provider: "anthropic"}}
	if err := ApplySpec(&opts, spec, t.TempDir(), SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpec: %v", err)
	}
	if len(opts.CredSwapProviders) != 2 {
		t.Fatalf("CredSwapProviders = %+v, want anthropic + openai", opts.CredSwapProviders)
	}
}
