package egress

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// TestAllSignalsExhaustiveAndUnique guards the closed vocabulary: every named
// signal constant appears in AllSignals exactly once, so a consumer (planed
// onSignal) can map them exhaustively.
func TestAllSignalsExhaustiveAndUnique(t *testing.T) {
	want := map[string]bool{
		SignalDenied:              true,
		SignalDirectIPNoSNI:       true,
		SignalQUICUDP443:          true,
		SignalForeignResolver:     true,
		SignalResolverDenied:      true,
		SignalUnresolvedSecretRef: true,
	}
	if len(AllSignals) != len(want) {
		t.Fatalf("AllSignals has %d entries, want %d", len(AllSignals), len(want))
	}
	seen := map[string]bool{}
	for _, s := range AllSignals {
		if seen[s] {
			t.Errorf("duplicate signal %q in AllSignals", s)
		}
		seen[s] = true
		if !want[s] {
			t.Errorf("unexpected signal %q in AllSignals", s)
		}
	}
}

// TestAuditDenyCarriesDeniedSignal: every fail-closed drop on the TCP/fetch path
// carries the denied signal.
func TestAuditDenyCarriesDeniedSignal(t *testing.T) {
	log := &BufferLogger{}
	b := &Brain{Mode: egressModeBroker, Logger: log}
	b.AuditDeny(Verdict{Reason: "not allowlisted"}, map[string]any{"host": "blocked.example"})
	events := log.Snapshot()
	if len(events) != 1 || events[0]["signal"] != SignalDenied {
		t.Fatalf("AuditDeny events = %v, want one carrying signal=denied", events)
	}
}

// TestDNSForeignResolverSignal: a query aimed at a PUBLIC resolver is tagged
// foreign-resolver; a query to the inside/gateway resolver is not.
func TestDNSForeignResolverSignal(t *testing.T) {
	forward := func(netip.AddrPort, []byte) ([]byte, error) {
		// Return a minimal valid-enough response; the handler only relays it.
		return buildQuery(t, 0x1234, "allowed.example.com.", dnsmessage.TypeA), nil
	}
	query := buildQuery(t, 0x1234, "allowed.example.com.", dnsmessage.TypeA)

	// Public resolver (8.8.8.8): foreign-resolver.
	log := &BufferLogger{}
	h := &Handler{Mode: egressModeBroker, Logger: log, NameCache: NewNameCache()}
	_, _ = h.handleDNS(query, netip.MustParseAddrPort("8.8.8.8:53"), forward)
	if !hasEventWithSignal(log, "egress_dns_allow", SignalForeignResolver) {
		t.Fatalf("public resolver query not tagged foreign-resolver: %v", log.Snapshot())
	}

	// Inside/gateway resolver (10.x): no foreign-resolver tag.
	log2 := &BufferLogger{}
	h2 := &Handler{Mode: egressModeBroker, Logger: log2, NameCache: NewNameCache()}
	_, _ = h2.handleDNS(query, netip.MustParseAddrPort("10.43.7.1:53"), forward)
	for _, e := range log2.Snapshot() {
		if e["signal"] == SignalForeignResolver {
			t.Fatalf("gateway resolver query wrongly tagged foreign-resolver: %v", e)
		}
	}
}

func hasEventWithSignal(log *BufferLogger, event, signal string) bool {
	for _, e := range log.Snapshot() {
		if e["event"] == event && e["signal"] == signal {
			return true
		}
	}
	return false
}

// TestQUICUDP443DroppedAndSignalled: a non-STUN UDP:443 datagram is dropped (no
// upstream dial, forcing TCP/TLS fallback where the broker governs it) and
// tagged with the quic-udp443 non-cooperation signal.
func TestQUICUDP443DroppedAndSignalled(t *testing.T) {
	guestSrc := netip.MustParseAddrPort("10.0.0.5:52000")
	quicDst := netip.MustParseAddrPort("203.0.113.9:443")
	log := &BufferLogger{}
	dialed := false
	h := &Handler{
		Mode:   egressModeBroker,
		Policy: mustPolicy(t),
		Logger: log,
		DialUDP: func(netip.AddrPort) (net.Conn, error) {
			dialed = true
			return nil, nil
		},
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	p.handleUDPDatagram(guestSrc, quicDst, []byte("quic-initial"))

	if dialed {
		t.Fatal("UDP:443 must not be forwarded — QUIC is default-denied")
	}
	if !hasEventWithSignal(log, "egress_udp_deny", SignalQUICUDP443) {
		t.Fatalf("UDP:443 drop not tagged quic-udp443: %v", log.Snapshot())
	}
}

func validSTUNBindingRequest() []byte {
	return []byte{
		0x00, 0x01, // Binding Request
		0x00, 0x00, // no attributes
		0x21, 0x12, 0xa4, 0x42, // magic cookie
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
		0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, // transaction ID
	}
}

// TestSTUNUDP443AllowedAndAudited proves the UDP:443 QUIC guard does not
// suppress a standards-conformant STUN exchange. Destination governance still
// runs, and the resulting association remains mediated and audited.
func TestSTUNUDP443AllowedAndAudited(t *testing.T) {
	guestSrc := netip.MustParseAddrPort("10.0.0.5:52000")
	stunDst := netip.MustParseAddrPort("203.0.113.9:443")
	upstream := newScriptedPacketConn()
	log := &BufferLogger{}
	h := &Handler{
		Mode:   egressModeBroker,
		Policy: mustPolicy(t),
		Logger: log,
		OpenUDP: func(netip.AddrPort) (net.PacketConn, error) {
			return upstream, nil
		},
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	payload := validSTUNBindingRequest()
	p.handleUDPDatagram(guestSrc, stunDst, payload)

	select {
	case write := <-upstream.writes:
		if write.to != stunDst {
			t.Fatalf("STUN destination = %v, want %v", write.to, stunDst)
		}
		if string(write.payload) != string(payload) {
			t.Fatalf("STUN payload changed: got %x, want %x", write.payload, payload)
		}
	case <-time.After(time.Second):
		t.Fatal("valid STUN on UDP:443 was not forwarded")
	}
	assertEventWithField(t, log, "egress_udp_allow", "protocol", "stun")
	assertEventWithField(t, log, "egress_udp_allow", "unlisted", true)
}

// TestSTUNUDP443StillRequiresDestinationApproval proves protocol recognition is
// not a policy bypass. A valid STUN message only avoids the QUIC classification;
// strict destination governance remains fail-closed.
func TestSTUNUDP443StillRequiresDestinationApproval(t *testing.T) {
	guestSrc := netip.MustParseAddrPort("10.0.0.5:52000")
	stunDst := netip.MustParseAddrPort("203.0.113.9:443")
	log := &BufferLogger{}
	opened := false
	h := &Handler{
		Mode:   "strict",
		Policy: mustPolicy(t),
		Logger: log,
		OpenUDP: func(netip.AddrPort) (net.PacketConn, error) {
			opened = true
			return newScriptedPacketConn(), nil
		},
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	p.handleUDPDatagram(guestSrc, stunDst, validSTUNBindingRequest())

	if opened {
		t.Fatal("strict policy deny opened a UDP association")
	}
	assertEventWithField(t, log, "egress_udp_deny", "signal", SignalDenied)
}

func TestSTUNDatagramValidation(t *testing.T) {
	valid := validSTUNBindingRequest()
	tests := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{name: "valid", payload: valid, want: true},
		{name: "short", payload: valid[:19]},
		{name: "message type high bits", payload: append([]byte{0xc0}, valid[1:]...)},
		{name: "wrong cookie", payload: append(append([]byte(nil), valid[:4]...), append([]byte{0, 0, 0, 0}, valid[8:]...)...)},
		{name: "unaligned length", payload: append(append([]byte(nil), valid[:2]...), append([]byte{0, 1}, valid[4:]...)...)},
		{name: "declared body missing", payload: append(append([]byte(nil), valid[:2]...), append([]byte{0, 4}, valid[4:]...)...)},
		{name: "trailing bytes", payload: append(append([]byte(nil), valid...), 0, 0, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSTUNDatagram(tt.payload); got != tt.want {
				t.Fatalf("isSTUNDatagram(%x) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}
