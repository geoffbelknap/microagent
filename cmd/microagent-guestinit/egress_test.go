//go:build linux

package main

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/google/nftables/expr"
)

// TestApplyKernelConfigEgressMediatorPort asserts the cmdline param toggles the
// in-guest forwarder: present (positive uint16) => mediation on with that port;
// absent => off; a bad value is rejected fail-closed so a mediated guest never
// silently boots without the capture.
func TestApplyKernelConfigEgressMediatorPort(t *testing.T) {
	var on config
	if err := applyKernelConfigOverridesFromCmdline(&on, "console=ttyS0 microagent_egress_mediator_port=1032 rw"); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if on.EgressMediatorPort != 1032 {
		t.Fatalf("EgressMediatorPort = %d, want 1032", on.EgressMediatorPort)
	}

	var off config
	if err := applyKernelConfigOverridesFromCmdline(&off, "console=ttyS0 root=/dev/sda rw"); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if off.EgressMediatorPort != 0 {
		t.Fatalf("EgressMediatorPort = %d, want 0 when param absent", off.EgressMediatorPort)
	}

	var bad config
	if err := applyKernelConfigOverridesFromCmdline(&bad, "microagent_egress_mediator_port=0"); err == nil {
		t.Fatal("expected error for zero egress mediator port")
	}
	var bad2 config
	if err := applyKernelConfigOverridesFromCmdline(&bad2, "microagent_egress_mediator_port=notaport"); err == nil {
		t.Fatal("expected error for malformed egress mediator port")
	}
}

// TestForwardEgressConnWritesDestHeaderAndPumps verifies the per-connection
// forwarder logic: it writes a DestHeader naming the recovered original
// destination, then pumps bytes bidirectionally between the captured local
// connection and the mediator stream. A net.Pipe stands in for the captured app
// connection; a fake mediator reads the header off its end and echoes the body.
func TestForwardEgressConnWritesDestHeaderAndPumps(t *testing.T) {
	appSide, forwarderSide := net.Pipe()      // app <-> forwarder (the captured conn)
	mediatorGuest, mediatorHost := net.Pipe() // forwarder <-> mediator stream

	origDst := netip.MustParseAddrPort("203.0.113.7:443")

	gotHeader := make(chan egress.DestHeader, 1)
	echoed := make(chan []byte, 1)
	go func() {
		hdr, err := egress.ReadDestHeader(mediatorHost)
		if err != nil {
			t.Errorf("fake mediator read header: %v", err)
			close(gotHeader)
			return
		}
		gotHeader <- hdr
		// Echo whatever the app sent back to the app.
		buf := make([]byte, 5)
		if _, err := io.ReadFull(mediatorHost, buf); err != nil {
			t.Errorf("fake mediator read body: %v", err)
		}
		if _, err := mediatorHost.Write(buf); err != nil {
			t.Errorf("fake mediator echo: %v", err)
		}
		echoed <- buf
		_ = mediatorHost.Close()
	}()

	forwardDone := make(chan struct{})
	go func() {
		forwardEgressConn(forwarderSide, origDst, mediatorGuest)
		close(forwardDone)
	}()

	// App writes a request; expects the echo back through the forwarder.
	go func() {
		_, _ = appSide.Write([]byte("hello"))
	}()

	select {
	case hdr, ok := <-gotHeader:
		if !ok {
			t.Fatal("fake mediator failed to read header")
		}
		if hdr.Proto != "tcp" || hdr.Host != "203.0.113.7" || hdr.Port != 443 {
			t.Fatalf("DestHeader = %+v, want tcp/203.0.113.7/443", hdr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for DestHeader")
	}

	reply := make([]byte, 5)
	if _, err := io.ReadFull(appSide, reply); err != nil {
		t.Fatalf("read echo at app: %v", err)
	}
	if !bytes.Equal(reply, []byte("hello")) {
		t.Fatalf("echo = %q, want %q", reply, "hello")
	}

	_ = appSide.Close()
	select {
	case <-forwardDone:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardEgressConn did not return after both sides closed")
	}
}

// TestBuildGuestEgressRulesetShape asserts the nft ruleset the forwarder installs
// has the expected inet-family tables/chains and verdicts: a nat/output chain with
// a TCP REDIRECT (skipping loopback + the forwarder's own uid), and a filter/output
// chain that drops all IPv6 and all non-TCP/non-DNS L4 — the OUTPUT-path analog of
// the firecracker PREROUTING fail-closed shape.
func TestBuildGuestEgressRulesetShape(t *testing.T) {
	rs := buildGuestEgressRuleset(12345, 4242)
	if rs.table == nil || rs.natChain == nil || rs.filterChain == nil {
		t.Fatalf("ruleset missing table/chains: %+v", rs)
	}
	if rs.table.Family != nftInet {
		t.Fatalf("table family = %v, want inet", rs.table.Family)
	}

	// The nat/output chain MUST be three separate rules, in order: the two RETURN
	// guards (skip-lo, skip-uid) preceding the REDIRECT. A single concatenated rule
	// makes the kernel AND the leading oifname=="lo" match with the REDIRECT, so the
	// REDIRECT never fires on real egress (eth0) and traffic falls through policy
	// accept unmediated — the capture bug this fix closes.
	natRules := guestNATChainRules(rs)
	if len(natRules) != 3 {
		t.Fatalf("nat chain has %d rules, want exactly 3 (skip-lo return, skip-uid return, tcp redirect)", len(natRules))
	}
	// Rule 1: oifname "lo" -> return.
	if !exprsHaveMeta(natRules[0], expr.MetaKeyOIFNAME) || !exprsHaveVerdict(natRules[0], expr.VerdictReturn) {
		t.Fatalf("nat rule 0 must be oifname lo -> return, got %s", summarizeExprs(natRules[0]))
	}
	// Rule 2: meta skuid <uid> -> return.
	if !exprsHaveMeta(natRules[1], expr.MetaKeySKUID) || !exprsHaveVerdict(natRules[1], expr.VerdictReturn) {
		t.Fatalf("nat rule 1 must be meta skuid -> return, got %s", summarizeExprs(natRules[1]))
	}
	// Rule 3: meta l4proto tcp -> redirect. This must NOT carry an oifname/skuid
	// match (those are their own preceding rules now) — the REDIRECT must reach a
	// real-egress packet on eth0.
	if !exprsHaveMeta(natRules[2], expr.MetaKeyL4PROTO) || !exprsHaveRedirect(natRules[2]) {
		t.Fatalf("nat rule 2 must be l4proto tcp -> redirect, got %s", summarizeExprs(natRules[2]))
	}
	if exprsHaveMeta(natRules[2], expr.MetaKeyOIFNAME) {
		t.Fatalf("redirect rule must not AND an oifname match (that gates the REDIRECT): %s", summarizeExprs(natRules[2]))
	}
	if exprsHaveVerdict(natRules[2], expr.VerdictReturn) {
		t.Fatalf("redirect rule must not contain a return verdict before the redirect: %s", summarizeExprs(natRules[2]))
	}

	if !rs.skipsForwarderUID {
		t.Fatal("redirect must skip the forwarder's own uid to avoid loops")
	}
	if !rs.skipsLoopback {
		t.Fatal("redirect must skip loopback traffic")
	}
	if !rs.dropsIPv6 {
		t.Fatal("filter chain must drop all guest IPv6 egress")
	}
	if rs.permitsDNSUDP {
		t.Fatal("filter chain must NOT permit UDP/53: DNS goes over TCP through the mediator (no unmediated UDP leak)")
	}
	if !rs.dropsOtherL4 {
		t.Fatal("filter chain must drop all non-TCP L4 incl UDP/53 (fail closed)")
	}
}

// guestNATChainRules returns the nat/output chain's rules in installation order,
// each as its own []expr.Any — the exact slices applyGuestEgressRuleset adds as
// separate nftables.Rules. Keeping this in one helper means the shape test and the
// terminal-verdict invariant guard agree on what the installer builds.
func guestNATChainRules(rs guestEgressRuleset) [][]expr.Any {
	return [][]expr.Any{rs.skipLoExprs, rs.skipUIDExprs, rs.redirectExprs}
}

// guestFilterChainRules returns the filter/output chain's rules in installation
// order.
func guestFilterChainRules(rs guestEgressRuleset) [][]expr.Any {
	return [][]expr.Any{rs.v6DropExprs, rs.tcpAllowExprs, rs.otherL4Exprs}
}

// TestGuestEgressRulesetNoExprAfterTerminalVerdict guards the invariant nft(8)
// enforces and the kernel silently violates via netlink: a terminal verdict
// (accept/drop/return) must be the LAST expression in a rule. An expression after
// a terminal verdict "has no effect" (nft rejects it as "Statement after terminal
// statement has no effect"); via the netlink path it installs but the trailing
// statement is dead. The original capture bug was exactly this — three
// match->verdict groups concatenated into one rule, so the REDIRECT after the two
// RETURNs was unreachable. Applying the guard across EVERY rule the installer
// builds means this class of bug cannot silently regress.
func TestGuestEgressRulesetNoExprAfterTerminalVerdict(t *testing.T) {
	rs := buildGuestEgressRuleset(egressForwarderUID, egressForwarderPort)
	rules := append(guestNATChainRules(rs), guestFilterChainRules(rs)...)
	for ruleIdx, exprs := range rules {
		for i, e := range exprs {
			v, ok := e.(*expr.Verdict)
			if !ok {
				continue
			}
			if !isTerminalVerdict(v.Kind) {
				continue
			}
			if i != len(exprs)-1 {
				t.Fatalf("rule %d has a terminal verdict (%v) at expr %d of %d — a non-terminal trailing expr would be dead/AND'd; verdict must be last: %s",
					ruleIdx, v.Kind, i, len(exprs), summarizeExprs(exprs))
			}
		}
	}
}

// isTerminalVerdict reports whether a verdict kind terminates rule evaluation
// (accept, drop, return, queue, stolen). For these, no further expression in the
// same rule executes, so any trailing expr is dead. (jump/goto continue into
// another chain and are not terminal for this rule's expr list.)
func isTerminalVerdict(k expr.VerdictKind) bool {
	switch k {
	case expr.VerdictAccept, expr.VerdictDrop, expr.VerdictReturn, expr.VerdictQueue, expr.VerdictStolen:
		return true
	default:
		return false
	}
}

func exprsHaveMeta(exprs []expr.Any, key expr.MetaKey) bool {
	for _, e := range exprs {
		if m, ok := e.(*expr.Meta); ok && m.Key == key {
			return true
		}
	}
	return false
}

func exprsHaveVerdict(exprs []expr.Any, kind expr.VerdictKind) bool {
	for _, e := range exprs {
		if v, ok := e.(*expr.Verdict); ok && v.Kind == kind {
			return true
		}
	}
	return false
}

func exprsHaveRedirect(exprs []expr.Any) bool {
	for _, e := range exprs {
		if _, ok := e.(*expr.Redir); ok {
			return true
		}
	}
	return false
}

// TestWriteMediatedResolvConf asserts the mediated resolv.conf forces DNS over
// TCP: it carries a nameserver (any address — the nft OUTPUT REDIRECT captures
// the TCP/53 connection regardless) and the resolver options that make glibc/musl
// use a TCP virtual circuit (use-vc) and not pack two queries into one socket
// (single-request), so the guest's DNS leaves as TCP/53 and is mediated.
func TestWriteMediatedResolvConf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	if err := writeMediatedResolvConfAt(path); err != nil {
		t.Fatalf("writeMediatedResolvConfAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resolv.conf: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "nameserver ") {
		t.Errorf("resolv.conf missing a nameserver line:\n%s", got)
	}
	if !strings.Contains(got, "options use-vc") {
		t.Errorf("resolv.conf missing 'options use-vc' (forces TCP DNS):\n%s", got)
	}
	if !strings.Contains(got, "single-request") {
		t.Errorf("resolv.conf missing 'single-request':\n%s", got)
	}
}

// TestWriteRouteLocalnet asserts the route_localnet sysctl is set to 1 so the
// nat/output REDIRECT's rewritten 127.0.0.1 destination is routed to the
// loopback forwarder instead of being martian-dropped.
func TestWriteRouteLocalnet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route_localnet")
	if err := writeRouteLocalnetAt(path); err != nil {
		t.Fatalf("writeRouteLocalnetAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read route_localnet: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Errorf("route_localnet = %q, want 1", string(data))
	}
}
