package main

import (
	"os"
	"strings"
	"testing"
)

// TestParseWorkspaceOptionsCredSwapStashesProviders verifies a valid
// --cred-swap PROVIDER[=ref] parses into Options.CredSwapProviders without
// resolving the file yet (that happens at workspace prep).
func TestParseWorkspaceOptionsCredSwapStashesProviders(t *testing.T) {
	opts, err := parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"docker.io/library/alpine:3.20",
		"--egress", "mitm",
		"--cred-swap", "anthropic",
		"--cred-swap", "openai=env:MY_OPENAI",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.CredSwapProviders) != 2 {
		t.Fatalf("CredSwapProviders = %v, want 2 entries", opts.CredSwapProviders)
	}
	if opts.CredSwapProviders[0].Provider != "anthropic" || opts.CredSwapProviders[0].Ref != "" {
		t.Fatalf("entry[0] = %+v, want {anthropic, \"\"}", opts.CredSwapProviders[0])
	}
	if opts.CredSwapProviders[1].Provider != "openai" || opts.CredSwapProviders[1].Ref != "env:MY_OPENAI" {
		t.Fatalf("entry[1] = %+v, want {openai, env:MY_OPENAI}", opts.CredSwapProviders[1])
	}
	// Parsing must not have generated a swap-config file yet.
	if opts.EgressSwapConfigPath != "" {
		t.Fatalf("EgressSwapConfigPath = %q, want empty at parse time", opts.EgressSwapConfigPath)
	}
}

// TestParseWorkspaceOptionsCredSwapRejectsLiteral verifies a literal secret
// (anything that is not an env:/file:/vault: reference) fails the whole
// invocation at parse time, before any file write or audit, so a secret pasted
// on the command line never gets processed.
func TestParseWorkspaceOptionsCredSwapRejectsLiteral(t *testing.T) {
	_, err := parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"docker.io/library/alpine:3.20",
		"--egress", "mitm",
		"--cred-swap", "anthropic=sk-ant-realsecret",
	})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted a literal cred-swap secret; want rejection")
	}
	if !strings.Contains(err.Error(), "literal") {
		t.Fatalf("error = %q, want it to explain a literal is rejected", err)
	}
}

// TestParseWorkspaceOptionsCredSwapRejectsUnknownProvider verifies an unknown
// provider name is rejected with a helpful list of known providers.
func TestParseWorkspaceOptionsCredSwapRejectsUnknownProvider(t *testing.T) {
	_, err := parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"docker.io/library/alpine:3.20",
		"--egress", "mitm",
		"--cred-swap", "not-a-provider",
	})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted an unknown cred-swap provider; want rejection")
	}
	if !strings.Contains(err.Error(), "unknown cred-swap provider") {
		t.Fatalf("error = %q, want it to flag the unknown provider", err)
	}
}

// TestParseWorkspaceOptionsCredSwapRequiresMediation verifies cred-swap is
// rejected with egress off — there is no mediator to inject the credential, so
// silently ignoring it would mislead the operator (mirrors --egress-swap-config).
func TestParseWorkspaceOptionsCredSwapRequiresMediation(t *testing.T) {
	_, err := parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"docker.io/library/alpine:3.20",
		"--egress", "off",
		"--cred-swap", "anthropic",
	})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted --cred-swap with --egress off; want rejection")
	}
	if !strings.Contains(err.Error(), "mitm") {
		t.Fatalf("error = %q, want it to require mitm", err)
	}
}

// TestParseWorkspaceOptionsCredSwapRejectsBrokerMode verifies cred-swap is
// rejected in broker mode: the swap table is only consulted by the mitm
// datapath, so in broker mode the injection silently never happens. Accepting
// it invites operators to "fix" the resulting auth failures by putting the real
// key in the guest env — defeating the mechanism.
func TestParseWorkspaceOptionsCredSwapRejectsBrokerMode(t *testing.T) {
	_, err := parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"docker.io/library/alpine:3.20",
		"--egress", "broker",
		"--cred-swap", "anthropic",
	})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted --cred-swap with --egress broker; want rejection")
	}
	if !strings.Contains(err.Error(), "mitm") {
		t.Fatalf("error = %q, want it to require mitm", err)
	}
}

// TestParseWorkspaceOptionsCredSwapRejectsDefaultMode verifies cred-swap with no
// explicit --egress is rejected: the default resolves to broker, which does not
// run the swap injection, so accepting it would silently drop the credential.
func TestParseWorkspaceOptionsCredSwapRejectsDefaultMode(t *testing.T) {
	_, err := parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"docker.io/library/alpine:3.20",
		"--cred-swap", "anthropic",
	})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted --cred-swap in the default (broker) mode; want rejection")
	}
	if !strings.Contains(err.Error(), "mitm") {
		t.Fatalf("error = %q, want it to require mitm", err)
	}
}

// TestParseWorkspaceOptionsSwapConfigRejectsBrokerMode mirrors the cred-swap
// guard for --egress-swap-config: it also only takes effect in mitm.
func TestParseWorkspaceOptionsSwapConfigRejectsBrokerMode(t *testing.T) {
	_, err := parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"docker.io/library/alpine:3.20",
		"--egress", "broker",
		"--egress-swap-config", "/tmp/swaps.yaml",
	})
	if err == nil {
		t.Fatal("parseWorkspaceOptions accepted --egress-swap-config with --egress broker; want rejection")
	}
	if !strings.Contains(err.Error(), "mitm") {
		t.Fatalf("error = %q, want it to require mitm", err)
	}
}
