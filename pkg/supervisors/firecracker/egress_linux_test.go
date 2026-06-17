//go:build linux

package firecracker

import (
	"net/netip"
	"os"
	"reflect"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/geoffbelknap/microagent/pkg/network"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
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
	if !reflect.DeepEqual(map[string]string(egressTProxySysctls), egressprereq.TProxySysctls) {
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
// rule. Both prepareTAPNATForStart (nat/user) and prepareNamedNetworkForStart
// (named) call the helper unconditionally and rely on this early return for the
// unmediated/off path — so it is safe to exercise without root or a netns. A nil
// config (the low-level raw create/start path) takes the same no-op path.
func TestProvisionEgressMediationOffIsNoOp(t *testing.T) {
	opts := Options{Name: "ws", StateDir: t.TempDir()}
	cases := []*vmkit.Config{
		nil,
		{EgressMode: vmkit.EgressModeOff},
		{EgressMode: ""},
	}
	for _, cfg := range cases {
		for _, mode := range []string{"nat", "user", "named"} {
			pid, rules, err := provisionEgressMediation(opts, cfg, mode, "microtap0", "10.44.1.1", "10.44.1.0/24", nil)
			if err != nil {
				t.Fatalf("mode %q cfg %+v: unexpected error: %v", mode, cfg, err)
			}
			if pid != 0 {
				t.Errorf("mode %q cfg %+v: pid = %d, want 0 (no mediator spawned)", mode, cfg, pid)
			}
			if rules != nil {
				t.Errorf("mode %q cfg %+v: rules = %+v, want nil (no rules installed)", mode, cfg, rules)
			}
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

func TestEgressMediatorArgsIncludesMode(t *testing.T) {
	cases := map[string]string{
		"mediated": "mediated",
		"strict":   "strict",
		"":         "mediated", // secure default normalization
	}
	for in, want := range cases {
		args := egressMediatorArgs("10.43.7.1", 41000, "/state/ws/egress-access.jsonl", in, nil, nil, nil, "", "")
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
		[]string{"api.github.com"}, []string{"raw.example.com"}, nil, "/state/ws/ca.pem", "/state/ws/ca-key.pem")
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

// TestEgressMediatorArgsThreadsPeers proves the repeatable --peer name=ip roster
// is threaded into the mediator argv (one --peer per entry, in order). This is the
// supervisor half of plumbing the named-network roster into the mediator.
func TestEgressMediatorArgsThreadsPeers(t *testing.T) {
	args := egressMediatorArgs("10.44.1.1", 41000, "/state/ws/egress-access.jsonl", "strict",
		nil, nil, []string{"builder=10.44.1.3", "db=10.44.1.4"}, "", "")
	got := argValues(args, "--peer")
	want := []string{"builder=10.44.1.3", "db=10.44.1.4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("--peer args = %v, want %v", got, want)
	}
}

// TestNamedNetworkPeersExcludesSelf proves the roster handed to the mediator
// contains every OTHER member as name=ip and omits this workspace's own entry —
// the mediator never reverse-resolves a flow to "self".
func TestNamedNetworkPeersExcludesSelf(t *testing.T) {
	record := network.Record{
		Name:    "team",
		Subnet:  "10.44.1.0/24",
		Gateway: "10.44.1.1",
		Members: []network.Member{
			{Workspace: "self", IP: "10.44.1.2"},
			{Workspace: "builder", IP: "10.44.1.3"},
			{Workspace: "db", IP: "10.44.1.4"},
		},
	}
	got := namedNetworkPeers(record, "self")
	want := []string{"builder=10.44.1.3", "db=10.44.1.4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("namedNetworkPeers = %v, want %v (self must be excluded)", got, want)
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
