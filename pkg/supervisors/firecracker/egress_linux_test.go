//go:build linux

package firecracker

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/google/nftables/expr"
)

func TestBuildEgressRedirectRule(t *testing.T) {
	rule, err := buildEgressRedirectRule("microtap0", "10.43.7.0/29", 41000)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rule.Chain != nftNATPreroutingChain || rule.Table != nftMicroagentTable {
		t.Fatalf("wrong table/chain: %+v", rule.transientFirewallRule)
	}
	var hasRedirect bool
	for _, e := range rule.Exprs {
		if _, ok := e.(*expr.Redir); ok {
			hasRedirect = true
		}
	}
	if !hasRedirect {
		t.Fatal("rule has no expr.Redir")
	}
}

func TestBuildEgressRedirectRuleRejectsBadSubnet(t *testing.T) {
	if _, err := buildEgressRedirectRule("t", "not-a-cidr", 41000); err == nil {
		t.Fatal("expected error for bad subnet")
	}
}

// argValue returns the value following the first occurrence of flag in args.
func argValue(args []string, flag string) (string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

func TestEgressMediatorArgsIncludesMode(t *testing.T) {
	cases := map[string]string{
		"mediated": "mediated",
		"strict":   "strict",
		"":         "mediated", // secure default normalization
	}
	for in, want := range cases {
		args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", in, nil, nil, "", "")
		got, ok := argValue(args, "--mode")
		if !ok {
			t.Fatalf("mode %q: --mode missing from args: %v", in, args)
		}
		if got != want {
			t.Errorf("mode %q: --mode = %q, want %q", in, got, want)
		}
	}
}

func TestEgressMediatorArgsThreadsAllowPassthroughCA(t *testing.T) {
	args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "strict",
		[]string{"api.github.com"}, []string{"raw.example.com"}, "/state/ws/ca.pem", "/state/ws/ca-key.pem")
	if v, _ := argValue(args, "--allow"); v != "api.github.com" {
		t.Errorf("--allow = %q, want api.github.com", v)
	}
	if v, _ := argValue(args, "--passthrough"); v != "raw.example.com" {
		t.Errorf("--passthrough = %q, want raw.example.com", v)
	}
	if v, _ := argValue(args, "--ca-cert"); v != "/state/ws/ca.pem" {
		t.Errorf("--ca-cert = %q, want /state/ws/ca.pem", v)
	}
	if v, _ := argValue(args, "--ca-key"); v != "/state/ws/ca-key.pem" {
		t.Errorf("--ca-key = %q, want /state/ws/ca-key.pem", v)
	}
}

// TestEgressMediationGatesProvisioning documents the guard that prepareTAPNATForStart
// uses to decide whether to provision the mediator: only an EXPLICIT mediated or
// strict mode provisions. An empty mode does NOT — the high-level workspace
// chokepoints set the "mediated" default via NormalizeEgressMode before the config
// reaches the supervisor, while the low-level raw create/start path leaves
// EgressMode empty (and allocates no CA-cert listener), so the supervisor must not
// mediate it; otherwise it would MITM the guest's TLS with a CA the guest never
// receives. off never provisions.
func TestEgressMediationGatesProvisioning(t *testing.T) {
	if !vmkit.EgressMediationOn(vmkit.EgressModeMediated) {
		t.Error("mediated must provision the mediator")
	}
	if !vmkit.EgressMediationOn(vmkit.EgressModeStrict) {
		t.Error("strict must provision the mediator")
	}
	if vmkit.EgressMediationOn("") {
		t.Error("empty mode must NOT provision the mediator (raw low-level path is unmediated)")
	}
	if vmkit.EgressMediationOn(vmkit.EgressModeOff) {
		t.Error("off must NOT provision the mediator")
	}
}
