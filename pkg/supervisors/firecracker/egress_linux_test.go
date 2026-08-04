//go:build linux

package firecracker

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// TestEgressTProxyConstantsTrackSharedPackage is the anti-drift guard from the
// supervisor side: the values the supervisor verifies fail-closed must be the
// SAME values `microagent host setup-networking` provisions. Both read them from
// pkg/egressprereq; this test fails if anyone reintroduces a local literal that
// could diverge from the provisioner.
func TestEgressTProxyConstantsTrackSharedPackage(t *testing.T) {
	if egressTProxyMark != egressprereq.TProxyMark {
		t.Errorf("egressTProxyMark %#x != egressprereq.TProxyMark %#x", egressTProxyMark, egressprereq.TProxyMark)
	}
	if egressTProxyTable != egressprereq.TProxyTable {
		t.Errorf("egressTProxyTable %d != egressprereq.TProxyTable %d", egressTProxyTable, egressprereq.TProxyTable)
	}
	if !reflect.DeepEqual(egressTProxySysctls, egressprereq.TProxySysctls) {
		t.Errorf("egressTProxySysctls %v != egressprereq.TProxySysctls %v", egressTProxySysctls, egressprereq.TProxySysctls)
	}
}

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

// TestBuildEgressRedirectRuleNamedSubnet proves the egress steering rules build
// for a named-network subnet shape (a /24 like 10.44.1.0/24), not just the small
// tap /29s the nat/user paths use. Named mode reuses the SAME REDIRECT + TPROXY
// rule builders via provisionEgressMediation, so the wiring that captures
// east-west VM↔VM egress depends on both rules accepting the /24.
func TestBuildEgressRedirectRuleNamedSubnet(t *testing.T) {
	const namedSubnet = "10.44.1.0/24"
	const namedGateway = "10.44.1.1"

	redirect, err := buildEgressRedirectRule("microtap0", namedSubnet, 41000)
	if err != nil {
		t.Fatalf("build redirect for named /24: %v", err)
	}
	if redirect.Chain != nftNATPreroutingChain || redirect.Table != nftMicroagentTable {
		t.Fatalf("redirect wrong table/chain: %+v", redirect.transientFirewallRule)
	}
	var hasRedirect bool
	for _, e := range redirect.Exprs {
		if _, ok := e.(*expr.Redir); ok {
			hasRedirect = true
		}
	}
	if !hasRedirect {
		t.Error("named /24 redirect rule has no expr.Redir")
	}

	mediator := netip.AddrPortFrom(netip.MustParseAddr(namedGateway), 41000)
	tproxy, err := buildEgressTProxyRule("microtap0", namedSubnet, egressTProxyMark, mediator)
	if err != nil {
		t.Fatalf("build tproxy for named /24: %v", err)
	}
	if tproxy.Chain != nftManglePreroutingChain || tproxy.Table != nftMicroagentTable {
		t.Fatalf("tproxy wrong table/chain: %+v", tproxy.transientFirewallRule)
	}
	var hasTProxy bool
	for _, e := range tproxy.Exprs {
		if _, ok := e.(*expr.TProxy); ok {
			hasTProxy = true
		}
	}
	if !hasTProxy {
		t.Error("named /24 tproxy rule has no expr.TProxy")
	}
}

func TestBuildEgressTProxyRule(t *testing.T) {
	mediator := netip.AddrPortFrom(netip.MustParseAddr("10.43.7.1"), 41000)
	rule, err := buildEgressTProxyRule("microtap0", "10.43.7.0/29", egressTProxyMark, mediator)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rule.Chain != nftManglePreroutingChain || rule.Table != nftMicroagentTable {
		t.Fatalf("wrong table/chain: %+v", rule.transientFirewallRule)
	}
	// The TPROXY rule must carry an expr.TProxy steering to the mediator's
	// addr:port (registers), and must stamp the fwmark (a MetaKeyMARK source-
	// register write) so the policy route delivers the datagram locally.
	var hasTProxy, hasMarkSet bool
	for _, e := range rule.Exprs {
		switch x := e.(type) {
		case *expr.TProxy:
			hasTProxy = true
			if x.RegAddr == 0 || x.RegPort == 0 {
				t.Errorf("TProxy must steer via addr+port registers: %+v", x)
			}
		case *expr.Meta:
			if x.Key == expr.MetaKeyMARK && x.SourceRegister {
				hasMarkSet = true
			}
		}
	}
	if !hasTProxy {
		t.Error("rule has no expr.TProxy")
	}
	if !hasMarkSet {
		t.Error("rule does not set the fwmark (MetaKeyMARK source-register write)")
	}
}

// TestBuildEgressV6DropRuleMatchesTapV6 proves the fail-closed IPv6 drop rule:
// for a mediated workspace it matches guest IPv6 egress arriving on the tap
// (iifname == tap, nfproto == ipv6) and DROPs it. The guest is IPv4-only today,
// so this is defense in depth — but if a guest ever acquired a v6 address while
// mediated, its v6 egress would NOT be captured by the v4-only REDIRECT/TPROXY
// rules, an unmediated channel. Dropping it at the firewall fails closed until
// v6 mediation lands. The rule lives in a FILTER chain (not the nat chain), and
// must carry the standard tagged comment so teardown removes it.
func TestBuildEgressV6DropRuleMatchesTapV6(t *testing.T) {
	rule, err := buildEgressV6DropRule("microtap0")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rule.Chain != nftFilterPreroutingChain || rule.Table != nftMicroagentTable {
		t.Fatalf("wrong table/chain: %+v", rule.transientFirewallRule)
	}
	var hasIIF, hasNFProtoV6, hasDrop bool
	for _, e := range rule.Exprs {
		switch x := e.(type) {
		case *expr.Meta:
			switch x.Key {
			case expr.MetaKeyIIFNAME:
				hasIIF = true
			case expr.MetaKeyNFPROTO:
				hasNFProtoV6 = true
			}
		case *expr.Cmp:
			// the nfproto comparison data must be the IPv6 protocol family
			if len(x.Data) == 1 && x.Data[0] == unix.NFPROTO_IPV6 {
				hasNFProtoV6 = hasNFProtoV6 && true
			}
		case *expr.Verdict:
			if x.Kind == expr.VerdictDrop {
				hasDrop = true
			}
		}
	}
	if !hasIIF {
		t.Error("v6 drop rule does not match iifname (guest tap)")
	}
	if !hasNFProtoV6 {
		t.Error("v6 drop rule does not match nfproto ipv6")
	}
	if !hasDrop {
		t.Error("v6 drop rule has no expr.Verdict drop")
	}
}

// TestEgressV6DropRuleAcceptedByCleanupAllowlist guards that the v6-drop rule's
// table/chain/comment pass validMicroagentFirewallRule, so the standard transient
// firewall teardown (stop/quarantine/failed-start) removes it rather than orphan
// the drop rule on the host after the workspace is gone.
func TestEgressV6DropRuleAcceptedByCleanupAllowlist(t *testing.T) {
	rule, err := buildEgressV6DropRule("magtap0badc0de")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !validMicroagentFirewallRule(rule.transientFirewallRule) {
		t.Fatalf("v6 drop rule rejected by cleanup allowlist: %+v", rule.transientFirewallRule)
	}
}

// TestProvisionEgressInstallsV6Drop documents that the v6 fail-closed drop is part
// of the mediated egress steering set. provisionEgressMediation needs root + a
// netns to install rules, so this asserts the wiring deterministically without
// touching host state: the v6-drop rule the provisioner installs (built from the
// same tap) carries the expected tagged comment kind, lives in the filter chain,
// and is accepted by the teardown allowlist — i.e. it is appended to the returned
// transient rules and will be torn down with the workspace, exactly like the
// REDIRECT and TPROXY rules.
func TestProvisionEgressInstallsV6Drop(t *testing.T) {
	const tap = "magtap0badc0de"
	v6drop, err := buildEgressV6DropRule(tap)
	if err != nil {
		t.Fatalf("build v6 drop: %v", err)
	}
	// The provisioner tags every steering rule with nftRuleComment(tap, kind); the
	// v6-drop's kind is the stable identifier teardown matches on.
	if got, want := v6drop.Comment, nftRuleComment(tap, "egress-v6-drop"); got != want {
		t.Fatalf("v6 drop comment = %q, want %q", got, want)
	}
	if v6drop.Chain != nftFilterPreroutingChain {
		t.Fatalf("v6 drop chain = %q, want %q (filter, not nat/mangle)", v6drop.Chain, nftFilterPreroutingChain)
	}
	if !validMicroagentFirewallRule(v6drop.transientFirewallRule) {
		t.Fatalf("v6 drop rule not accepted by teardown allowlist: %+v", v6drop.transientFirewallRule)
	}
	// And it is distinct from the v4 REDIRECT rule (a different chain and a
	// different comment kind), so all the steering rules coexist in the returned
	// rule slice rather than colliding.
	redirect, err := buildEgressRedirectRule(tap, "10.43.7.0/29", 41000)
	if err != nil {
		t.Fatalf("build redirect: %v", err)
	}
	if v6drop.Chain == redirect.Chain || v6drop.Comment == redirect.Comment {
		t.Fatalf("v6 drop must be distinct from redirect: v6=%+v redirect=%+v", v6drop.transientFirewallRule, redirect.transientFirewallRule)
	}
}

// TestBuildEgressL4DropRuleDropsNonTCPUDP proves the Tier 5 drop-and-audit of
// guest IPv4 L4 traffic that is neither TCP (REDIRECT-mediated) nor UDP
// (TPROXY-mediated) — i.e. ICMP and other protocols with no allowlistable
// destination identity. The builder emits three precedence rules in one filter
// chain: l4proto==tcp -> accept, l4proto==udp -> accept, then a catch-all
// (iifname==tap, ipv4 saddr in subnet) -> nflog + drop. The two accepts ensure
// the catch-all NEVER drops tcp/udp (those are already steered by the nat/mangle
// hooks anyway); the catch-all contains everything else.
func TestBuildEgressL4DropRuleDropsNonTCPUDP(t *testing.T) {
	rules, err := buildEgressL4DropRule("microtap0", "10.43.7.0/29")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 precedence rules (accept tcp, accept udp, drop rest), got %d", len(rules))
	}
	for i, r := range rules {
		if r.Chain != nftFilterPreroutingChain || r.Table != nftMicroagentTable {
			t.Fatalf("rule %d wrong table/chain: %+v", i, r.transientFirewallRule)
		}
	}

	// Rules 0 and 1: the protocol accepts. Each must compare l4proto against
	// exactly tcp / udp and accept (so the catch-all never sees tcp/udp).
	acceptProto := func(r nftFirewallRule) (byte, bool) {
		var proto byte
		var hasL4, hasAccept bool
		for _, e := range r.Exprs {
			switch x := e.(type) {
			case *expr.Meta:
				if x.Key == expr.MetaKeyL4PROTO {
					hasL4 = true
				}
			case *expr.Cmp:
				if x.Op == expr.CmpOpEq && len(x.Data) == 1 {
					proto = x.Data[0]
				}
			case *expr.Verdict:
				if x.Kind == expr.VerdictAccept {
					hasAccept = true
				}
			}
		}
		return proto, hasL4 && hasAccept
	}
	tcpProto, tcpOK := acceptProto(rules[0])
	if !tcpOK || tcpProto != unix.IPPROTO_TCP {
		t.Errorf("rule 0 must accept l4proto==tcp, got proto=%d ok=%v", tcpProto, tcpOK)
	}
	udpProto, udpOK := acceptProto(rules[1])
	if !udpOK || udpProto != unix.IPPROTO_UDP {
		t.Errorf("rule 1 must accept l4proto==udp, got proto=%d ok=%v", udpProto, udpOK)
	}

	// Rule 2: the catch-all. Must match iifname==tap + ipv4 saddr-in-subnet,
	// nflog (expr.Log) before the drop verdict, and DROP.
	drop := rules[2]
	var hasIIF, hasNFProtoV4, hasLog, hasDrop bool
	logBeforeDrop := false
	sawLog := false
	for _, e := range drop.Exprs {
		switch x := e.(type) {
		case *expr.Meta:
			switch x.Key {
			case expr.MetaKeyIIFNAME:
				hasIIF = true
			case expr.MetaKeyNFPROTO:
				hasNFProtoV4 = true
			}
		case *expr.Cmp:
			if len(x.Data) == 1 && x.Data[0] == unix.NFPROTO_IPV4 {
				hasNFProtoV4 = hasNFProtoV4 && true
			}
		case *expr.Log:
			hasLog = true
			sawLog = true
		case *expr.Verdict:
			if x.Kind == expr.VerdictDrop {
				hasDrop = true
				if sawLog {
					logBeforeDrop = true
				}
			}
		}
	}
	if !hasIIF {
		t.Error("catch-all drop rule does not match iifname (guest tap)")
	}
	if !hasNFProtoV4 {
		t.Error("catch-all drop rule does not match nfproto ipv4 (subnet match)")
	}
	if !hasLog {
		t.Error("catch-all drop rule has no expr.Log (nflog) for audit")
	}
	if !hasDrop {
		t.Error("catch-all drop rule has no expr.Verdict drop")
	}
	if !logBeforeDrop {
		t.Error("nflog must precede the drop verdict so dropped packets are audited")
	}
}

func TestBuildEgressL4DropRuleRejectsBadSubnet(t *testing.T) {
	if _, err := buildEgressL4DropRule("t", "not-a-cidr"); err == nil {
		t.Fatal("expected error for bad subnet")
	}
}

func TestBuildEgressL4DropRuleRejectsIPv6Subnet(t *testing.T) {
	if _, err := buildEgressL4DropRule("t", "fd00::/64"); err == nil {
		t.Fatal("expected error for non-IPv4 subnet")
	}
}

// TestEgressL4DropRulesAcceptedByCleanupAllowlist guards that every L4-drop
// precedence rule's table/chain/comment passes validMicroagentFirewallRule, so
// the standard transient firewall teardown removes them rather than orphaning
// the drop/accept rules on the host after the workspace is gone.
func TestEgressL4DropRulesAcceptedByCleanupAllowlist(t *testing.T) {
	rules, err := buildEgressL4DropRule("magtap0badc0de", "10.43.7.0/29")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for i, r := range rules {
		if !validMicroagentFirewallRule(r.transientFirewallRule) {
			t.Fatalf("L4-drop rule %d rejected by cleanup allowlist: %+v", i, r.transientFirewallRule)
		}
	}
}

// TestProvisionEgressInstallsL4Drop documents that the Tier 5 non-tcp/udp
// drop-and-audit is part of the mediated egress steering set. provisionEgressMediation
// needs root + a netns to install rules, so this asserts the wiring
// deterministically without touching host state: the L4-drop rules the
// provisioner installs (built from the same tap+subnet) carry the expected tagged
// comments, live in the filter chain, and are accepted by the teardown allowlist
// — i.e. they are appended to the returned transient rules and torn down with the
// workspace, exactly like the REDIRECT, TPROXY, and v6-drop rules.
func TestProvisionEgressInstallsL4Drop(t *testing.T) {
	const tap = "magtap0badc0de"
	const subnet = "10.43.7.0/29"
	rules, err := buildEgressL4DropRule(tap, subnet)
	if err != nil {
		t.Fatalf("build L4 drop: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 L4-drop precedence rules, got %d", len(rules))
	}
	wantKinds := []string{"egress-l4-accept-tcp", "egress-l4-accept-udp", "egress-l4-drop"}
	for i, r := range rules {
		if got, want := r.Comment, nftRuleComment(tap, wantKinds[i]); got != want {
			t.Fatalf("L4-drop rule %d comment = %q, want %q", i, got, want)
		}
		if r.Chain != nftFilterPreroutingChain {
			t.Fatalf("L4-drop rule %d chain = %q, want %q (filter, not nat/mangle)", i, r.Chain, nftFilterPreroutingChain)
		}
		if !validMicroagentFirewallRule(r.transientFirewallRule) {
			t.Fatalf("L4-drop rule %d not accepted by teardown allowlist: %+v", i, r.transientFirewallRule)
		}
	}
	// The L4-drop set must be distinct from the v6-drop and REDIRECT rules — a
	// different comment kind — so all the steering rules coexist in the returned
	// rule slice rather than colliding.
	v6drop, err := buildEgressV6DropRule(tap)
	if err != nil {
		t.Fatalf("build v6 drop: %v", err)
	}
	for i, r := range rules {
		if r.Comment == v6drop.Comment {
			t.Fatalf("L4-drop rule %d collides with v6 drop comment %q", i, v6drop.Comment)
		}
	}
}

func TestBuildEgressTProxyRuleRejectsBadSubnet(t *testing.T) {
	mediator := netip.AddrPortFrom(netip.MustParseAddr("10.43.7.1"), 41000)
	if _, err := buildEgressTProxyRule("t", "not-a-cidr", egressTProxyMark, mediator); err == nil {
		t.Fatal("expected error for bad subnet")
	}
}

func TestBuildEgressTProxyRuleRejectsIPv6Mediator(t *testing.T) {
	mediator := netip.AddrPortFrom(netip.MustParseAddr("fd00::1"), 41000)
	if _, err := buildEgressTProxyRule("t", "10.43.7.0/29", egressTProxyMark, mediator); err == nil {
		t.Fatal("expected error for non-IPv4 mediator addr")
	}
}

// TestEgressTProxyRuleAcceptedByCleanupAllowlist guards that the TPROXY rule's
// table/chain/comment pass validMicroagentFirewallRule, so the standard transient
// firewall teardown (stop/quarantine/failed-start) will actually remove it rather
// than silently skip it (which would orphan the steering rule).
func TestEgressTProxyRuleAcceptedByCleanupAllowlist(t *testing.T) {
	mediator := netip.AddrPortFrom(netip.MustParseAddr("10.43.7.1"), 41000)
	rule, err := buildEgressTProxyRule("magtap0badc0de", "10.43.7.0/29", egressTProxyMark, mediator)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !validMicroagentFirewallRule(rule.transientFirewallRule) {
		t.Fatalf("tproxy rule rejected by cleanup allowlist: %+v", rule.transientFirewallRule)
	}
}

// allSysctlsPresent returns a readFile seam reporting every TPROXY sysctl at its
// desired value, so a test can then knock one out to assert the fail-closed path.
func allSysctlsPresent() func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if want, ok := egressTProxySysctls[path]; ok {
			return []byte(want + "\n"), nil
		}
		return nil, os.ErrNotExist
	}
}

func presentRule() func() ([]netlink.Rule, error) {
	return func() ([]netlink.Rule, error) {
		return []netlink.Rule{{Mark: egressTProxyMark, Table: egressTProxyTable}}, nil
	}
}

// TestVerifyEgressTProxyPrereqsFailClosed documents the fail-closed gate that nat
// mode relies on: when the host-global prerequisites (sysctls + fwmark ip rule)
// are all present the verification passes; if ANY sysctl is wrong or the ip rule
// is absent it returns an error. That error is what prepareTAPNATForStart wraps
// with the "run 'microagent host setup-networking' or use --egress off" hint and
// then tears everything down — so a misconfigured host fails the start instead of
// booting a guest whose UDP escapes mediation.
func TestVerifyEgressTProxyPrereqsFailClosed(t *testing.T) {
	if err := verifyEgressTProxyPrereqs(egressTProxyMark, egressTProxyTable, allSysctlsPresent(), presentRule()); err != nil {
		t.Fatalf("all prereqs present: unexpected error: %v", err)
	}

	// A wrong sysctl value must fail closed.
	badSysctl := func(path string) ([]byte, error) {
		if path == "/proc/sys/net/ipv4/ip_forward" {
			return []byte("0\n"), nil
		}
		return allSysctlsPresent()(path)
	}
	if err := verifyEgressTProxyPrereqs(egressTProxyMark, egressTProxyTable, badSysctl, presentRule()); err == nil {
		t.Error("wrong ip_forward sysctl: expected fail-closed error")
	}

	// An absent ip rule must fail closed.
	noRule := func() ([]netlink.Rule, error) { return nil, nil }
	if err := verifyEgressTProxyPrereqs(egressTProxyMark, egressTProxyTable, allSysctlsPresent(), noRule); err == nil {
		t.Error("absent fwmark ip rule: expected fail-closed error")
	}
}

// TestProvisionEgressMediationOffIsNoOp documents the gate the shared helper uses
// before touching any host state: when egress mediation is off it returns
// (0, nil, nil) without minting a CA, spawning a mediator, or installing any
// rule. prepareTAPNATForStart (user mode) calls the helper unconditionally and
// relies on this early return for the unmediated/off path — so it is safe to
// exercise without root or a netns. A nil config (the low-level raw create/start
// path) takes the same no-op path.
func TestProvisionEgressMediationOffIsNoOp(t *testing.T) {
	opts := Options{Name: "ws", StateDir: t.TempDir()}
	cases := []*vmkit.Config{
		nil,
		{EgressMode: vmkit.EgressModeOff},
		{EgressMode: ""},
	}
	for _, cfg := range cases {
		pid, rules, err := provisionEgressMediation(opts, cfg, "microtap0", "10.44.1.1", "10.44.1.0/24", false, "")
		if err != nil {
			t.Fatalf("cfg %+v: unexpected error: %v", cfg, err)
		}
		if pid != 0 {
			t.Errorf("cfg %+v: pid = %d, want 0 (no mediator spawned)", cfg, pid)
		}
		if rules != nil {
			t.Errorf("cfg %+v: rules = %+v, want nil (no rules installed)", cfg, rules)
		}
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

// argValues returns every value following an occurrence of flag in args (for
// repeatable flags like --peer).
func argValues(args []string, flag string) []string {
	var vals []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			vals = append(vals, args[i+1])
		}
	}
	return vals
}

// TestEgressMediatorArgsCoverParityRegistry pins the Firecracker mediator to the
// shared egress-control registry: with every control set, its argv must carry a
// flag for each registered field. Together with the Apple VF datapath's matching
// test, this makes a control dropped from one datapath fail CI (the B1/B22/B23
// fail-open class).
func TestEgressMediatorArgsCoverParityRegistry(t *testing.T) {
	args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "mitm", true,
		[]string{"api.example.com"}, []string{"pass.example.com"}, []string{"1.1.1.1"}, "/swap.yaml",
		[]string{"peer=10.0.0.2"}, "/ca.pem", "/ca.key", "", egressCaps{maxBytesPerSec: 1, maxTotalBytes: 1, maxConns: 1, auditMaxBytes: 1, auditMaxBackups: 1})
	for _, f := range vmkit.EgressDatapathFields() {
		if _, ok := argValue(args, "--"+f.MediatorFlag); !ok {
			t.Errorf("firecracker mediator argv is missing --%s (config %q); a registered egress control is no longer forwarded", f.MediatorFlag, f.ConfigField)
		}
	}
}

func TestEgressMediatorArgsIncludesMode(t *testing.T) {
	cases := map[string]string{
		"broker": "broker",
		"mitm":   "mitm",
		"":       "broker", // empty resolves to the broker default
	}
	for in, want := range cases {
		args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", in, false, nil, nil, nil, "", nil, "", "", "", egressCaps{})
		got, ok := argValue(args, "--mode")
		if !ok {
			t.Fatalf("mode %q: --mode missing from args: %v", in, args)
		}
		if got != want {
			t.Errorf("mode %q: --mode = %q, want %q", in, got, want)
		}
	}
}

// TestEgressMediatorArgsThreadsLockAllowlist proves the locked-allowlist toggle
// is threaded into the mediator argv as --lock-allowlist when set, and omitted
// when clear (so an unlocked workspace's argv is byte-identical).
func TestEgressMediatorArgsThreadsLockAllowlist(t *testing.T) {
	locked := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "broker", true,
		[]string{"api.example.com"}, nil, nil, "", nil, "", "", "", egressCaps{})
	if _, ok := argValue(locked, "--lock-allowlist"); !ok {
		t.Errorf("locked broker did not emit --lock-allowlist: %v", locked)
	}
	unlocked := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "broker", false,
		nil, nil, nil, "", nil, "", "", "", egressCaps{})
	if _, ok := argValue(unlocked, "--lock-allowlist"); ok {
		t.Errorf("unlocked broker emitted --lock-allowlist: %v", unlocked)
	}
}

func TestEgressMediatorArgsThreadsAllowPassthroughCA(t *testing.T) {
	args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "mitm", false,
		[]string{"api.github.com"}, []string{"raw.example.com"}, nil, "", nil, "/state/ws/ca.pem", "/state/ws/ca-key.pem", "", egressCaps{})
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

// TestEgressMediatorArgsThreadsSwapConfig proves the credential-swap config path
// is threaded into the mediator argv as --swap-config when set, and omitted when
// empty (so a swap-less workspace's argv is byte-identical to the pre-swap one).
func TestEgressMediatorArgsThreadsSwapConfig(t *testing.T) {
	args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "mitm", false,
		[]string{"api.openai.com"}, nil, nil, "/state/ws/swaps.yaml", nil, "/state/ws/ca.pem", "/state/ws/ca-key.pem", "", egressCaps{})
	if v, _ := argValue(args, "--swap-config"); v != "/state/ws/swaps.yaml" {
		t.Errorf("--swap-config = %q, want /state/ws/swaps.yaml", v)
	}

	bare := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "mitm", false,
		nil, nil, nil, "", nil, "", "", "", egressCaps{})
	if _, ok := argValue(bare, "--swap-config"); ok {
		t.Errorf("empty swap-config emitted --swap-config: %v", bare)
	}
}

// TestEgressMediatorArgsThreadsPeers proves the repeatable --peer name=ip roster
// is threaded into the mediator argv (one --peer per entry, in order). This is the
// supervisor half of plumbing the named-network roster into the mediator.
func TestEgressMediatorArgsThreadsPeers(t *testing.T) {
	args := egressMediatorArgs("10.44.1.1", 41000, "/state/ws/egress-access.jsonl", "mitm", false,
		nil, nil, nil, "", []string{"builder=10.44.1.3", "db=10.44.1.4"}, "", "", "", egressCaps{})
	got := argValues(args, "--peer")
	want := []string{"builder=10.44.1.3", "db=10.44.1.4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("--peer args = %v, want %v", got, want)
	}
}

// TestEgressMediatorArgsThreadsResolvers proves the workspace's configured
// nameservers are threaded into the mediator argv as repeatable --resolver
// flags (one per entry, in order), and omitted entirely when none are
// configured (so the mediator falls back to its internal-address floor).
func TestEgressMediatorArgsThreadsResolvers(t *testing.T) {
	args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "broker", false,
		nil, nil, []string{"1.1.1.1", "8.8.8.8"}, "", nil, "", "", "", egressCaps{})
	got := argValues(args, "--resolver")
	want := []string{"1.1.1.1", "8.8.8.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("--resolver args = %v, want %v", got, want)
	}

	bare := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "broker", false,
		nil, nil, nil, "", nil, "", "", "", egressCaps{})
	if _, ok := argValue(bare, "--resolver"); ok {
		t.Errorf("no configured resolvers emitted --resolver: %v", bare)
	}
}

// TestEgressMediatorArgsThreadsCaps proves the bounded-operations caps (ASK
// tenet 8) are threaded into the mediator argv when set, and omitted when zero
// (so an uncapped workspace's argv is byte-identical to the pre-caps one).
func TestEgressMediatorArgsThreadsCaps(t *testing.T) {
	caps := egressCaps{
		maxBytesPerSec:  1048576,
		maxTotalBytes:   10485760,
		maxConns:        8,
		auditMaxBytes:   5242880,
		auditMaxBackups: 3,
	}
	args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "mitm", false,
		nil, nil, nil, "", nil, "", "", "", caps)
	if v, _ := argValue(args, "--max-bps"); v != "1048576" {
		t.Errorf("--max-bps = %q, want 1048576", v)
	}
	if v, _ := argValue(args, "--max-bytes"); v != "10485760" {
		t.Errorf("--max-bytes = %q, want 10485760", v)
	}
	if v, _ := argValue(args, "--max-conns"); v != "8" {
		t.Errorf("--max-conns = %q, want 8", v)
	}
	if v, _ := argValue(args, "--audit-max-bytes"); v != "5242880" {
		t.Errorf("--audit-max-bytes = %q, want 5242880", v)
	}
	if v, _ := argValue(args, "--audit-max-backups"); v != "3" {
		t.Errorf("--audit-max-backups = %q, want 3", v)
	}

	// Zero caps: none of the cap flags appear.
	bare := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", "mitm", false,
		nil, nil, nil, "", nil, "", "", "", egressCaps{})
	for _, flag := range []string{"--max-bps", "--max-bytes", "--max-conns", "--audit-max-bytes", "--audit-max-backups"} {
		if _, ok := argValue(bare, flag); ok {
			t.Errorf("zero caps emitted %s: %v", flag, bare)
		}
	}
}

// TestEgressMediationGatesProvisioning documents the guard that prepareTAPNATForStart
// uses to decide whether to provision the mediator: only an EXPLICIT broker or
// mitm mode provisions. An empty mode does NOT — the high-level workspace
// chokepoints resolve the "broker" default via ValidateEgressMode before the
// config reaches the supervisor, while the low-level raw create/start path leaves
// EgressMode empty (and allocates no CA-cert listener), so the supervisor must not
// mediate it. off never provisions.
func TestEgressMediationGatesProvisioning(t *testing.T) {
	if !vmkit.EgressMediationOn(vmkit.EgressModeBroker) {
		t.Error("broker must provision the mediator")
	}
	if !vmkit.EgressMediationOn(vmkit.EgressModeMITM) {
		t.Error("mitm must provision the mediator")
	}
	if vmkit.EgressMediationOn("") {
		t.Error("empty mode must NOT provision the mediator (raw low-level path is unmediated)")
	}
	if vmkit.EgressMediationOn(vmkit.EgressModeOff) {
		t.Error("off must NOT provision the mediator")
	}
}

// TestEgressMediatorLoggedReady exercises the readiness-marker scan used by
// startEgressMediator to gate readiness on the post-UDP signal. The marker is
// only emitted by egress.Run AFTER the transparent UDP socket opens, so its
// presence (after the pre-spawn offset) is the supervisor's proof the mediator
// is fully up — not merely TCP-bound.
func TestEgressMediatorLoggedReady(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "egress-mediator.log")

	// No file yet → not ready (keep polling).
	if egressMediatorLoggedReady(logPath, 0) {
		t.Fatal("missing logfile reported ready")
	}

	// A stale marker from a PRIOR run (the logfile is append-mode/reused). The
	// scan must ignore everything before the recorded offset, so a marker that
	// lives entirely below the offset must NOT count as this run's readiness.
	stale := fmt.Sprintf("%s 10.43.7.1:41000\n", egress.ReadyMarker)
	if err := os.WriteFile(logPath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	staleSize := int64(len(stale))
	if egressMediatorLoggedReady(logPath, staleSize) {
		t.Fatal("stale marker before the offset was treated as ready")
	}

	// Some non-marker startup chatter appended after the offset → still not ready.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("egress_listen addr=10.43.7.1:41000\n"); err != nil {
		t.Fatal(err)
	}
	if egressMediatorLoggedReady(logPath, staleSize) {
		t.Fatal("non-marker chatter was treated as ready")
	}

	// This run's marker appended after the offset → ready.
	if _, err := fmt.Fprintf(f, "%s 10.43.7.1:41000\n", egress.ReadyMarker); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if !egressMediatorLoggedReady(logPath, staleSize) {
		t.Fatal("post-offset readiness marker was not detected")
	}
}
