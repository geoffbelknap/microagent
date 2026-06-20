//go:build linux

package main

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
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
	appSide, forwarderSide := net.Pipe()       // app <-> forwarder (the captured conn)
	mediatorGuest, mediatorHost := net.Pipe()   // forwarder <-> mediator stream

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
	// The REDIRECT rule must target the forwarder's TCP port and carry a uid-skip
	// (loop avoidance) and a loopback-skip somewhere in its expression list.
	if len(rs.redirectExprs) == 0 {
		t.Fatal("no redirect exprs built")
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
	if !rs.permitsDNSUDP {
		t.Fatal("filter chain must permit UDP/53 (resolv.conf path until P6)")
	}
	if !rs.dropsOtherL4 {
		t.Fatal("filter chain must drop non-TCP/non-DNS L4 (fail closed)")
	}
}
