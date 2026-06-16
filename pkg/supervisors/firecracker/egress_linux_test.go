//go:build linux

package firecracker

import (
	"testing"

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
