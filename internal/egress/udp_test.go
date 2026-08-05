package egress

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// udpEchoServer stands up a local UDP "upstream" that echoes every datagram
// back to its sender. It returns the server's AddrPort and a cleanup func.
func udpEchoServer(t *testing.T) (netip.AddrPort, func()) {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp echo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDP(buf[:n], from)
		}
	}()
	addr := netip.MustParseAddrPort(pc.LocalAddr().String())
	return addr, func() { close(done); pc.Close() }
}

// capturedReply records one spoofed-source reply delivered via ReplyTo.
type capturedReply struct {
	origDst  netip.AddrPort
	guestSrc netip.AddrPort
	payload  []byte
}

// TestUDPProxyForwardsAndReplies drives the per-datagram seam directly: a real
// upstream echo server is dialed via injected DialUDP, and replies are captured
// via injected ReplyTo. It asserts the echo comes back to the guest src with
// origDst as the spoofed source, and that egress_udp_allow was audited.
func TestUDPProxyForwardsAndReplies(t *testing.T) {
	echoAddr, cleanup := udpEchoServer(t)
	defer cleanup()

	guestSrc := netip.MustParseAddrPort("10.0.0.5:51000")
	// Non-DNS port: :53 routes to the DNS resolver-filter (one-shot, no flow),
	// which is exercised separately; this test drives the generic flow path.
	origDst := netip.MustParseAddrPort("203.0.113.9:4433")

	replies := make(chan capturedReply, 4)
	log := &BufferLogger{}
	h := &Handler{
		Mode:   "mitm",
		Policy: mustPolicy(t),
		Logger: log,
		// DialUDP ignores origDst and dials the real echo server so we can
		// exercise the full forward + reply loop without TPROXY.
		DialUDP: func(netip.AddrPort) (net.Conn, error) {
			return net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(echoAddr))
		},
		ReplyTo: func(od, gs netip.AddrPort, payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			replies <- capturedReply{origDst: od, guestSrc: gs, payload: cp}
			return nil
		},
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	p.handleUDPDatagram(guestSrc, origDst, []byte("ping"))

	select {
	case r := <-replies:
		if string(r.payload) != "ping" {
			t.Fatalf("reply payload = %q, want ping", r.payload)
		}
		if r.origDst != origDst {
			t.Fatalf("reply spoofed-source = %v, want origDst %v", r.origDst, origDst)
		}
		if r.guestSrc != guestSrc {
			t.Fatalf("reply guestSrc = %v, want %v", r.guestSrc, guestSrc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply delivered via ReplyTo within timeout")
	}
	assertEventWithField(t, log, "egress_udp_allow", "unlisted", true)
	assertEventWithField(t, log, "egress_udp_allow", "src", guestSrc.String())
}

func reserveUDPSourcePort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("reserve UDP source port: %v", err)
	}
	port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
	if err := conn.Close(); err != nil {
		t.Fatalf("release UDP source port: %v", err)
	}
	return port
}

// TestDefaultOpenUDPPreservesGuestSourcePort is the RTP/RTCP regression lock:
// the mediator's upstream socket must use the port the guest negotiated with
// its peer. Rebinding to an ephemeral port makes return traffic target an
// endpoint the guest never opened.
func TestDefaultOpenUDPPreservesGuestSourcePort(t *testing.T) {
	echoAddr, cleanup := udpEchoServer(t)
	defer cleanup()

	port := reserveUDPSourcePort(t)
	guestSrc := netip.AddrPortFrom(netip.MustParseAddr("10.0.0.5"), port)
	conn, err := defaultOpenUDP(guestSrc, netip.MustParseAddrPort("203.0.113.1:123"))
	if err != nil {
		t.Fatalf("defaultOpenUDP: %v", err)
	}
	defer func() { _ = conn.Close() }()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("local address type = %T, want *net.UDPAddr", conn.LocalAddr())
	}
	if got := uint16(local.Port); got != port {
		t.Fatalf("upstream source port = %d, want guest source port %d", got, port)
	}

	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.WriteTo([]byte("preserved"), net.UDPAddrFromAddrPort(echoAddr)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 32)
	n, from, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if got := from.(*net.UDPAddr).AddrPort(); got != echoAddr {
		t.Fatalf("reply source = %v, want %v", got, echoAddr)
	}
	if got := string(buf[:n]); got != "preserved" {
		t.Fatalf("reply = %q, want preserved", got)
	}
}

// TestDefaultOpenUDPUsesOneSourcePortAcrossDestinations proves a single guest
// socket retains its source port while talking to more than one peer.
func TestDefaultOpenUDPUsesOneSourcePortAcrossDestinations(t *testing.T) {
	echoOne, cleanupOne := udpEchoServer(t)
	defer cleanupOne()
	echoTwo, cleanupTwo := udpEchoServer(t)
	defer cleanupTwo()

	port := reserveUDPSourcePort(t)
	guestSrc := netip.AddrPortFrom(netip.MustParseAddr("10.0.0.5"), port)
	conn, err := defaultOpenUDP(guestSrc, netip.MustParseAddrPort("203.0.113.1:123"))
	if err != nil {
		t.Fatalf("open association: %v", err)
	}
	defer func() { _ = conn.Close() }()

	local := conn.LocalAddr().(*net.UDPAddr)
	if got := uint16(local.Port); got != port {
		t.Fatalf("association source port = %d, want %d", got, port)
	}
	for i, destination := range []netip.AddrPort{echoOne, echoTwo} {
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		payload := []byte{byte('1' + i)}
		if _, err := conn.WriteTo(payload, net.UDPAddrFromAddrPort(destination)); err != nil {
			t.Fatalf("destination %d write: %v", i+1, err)
		}
		buf := make([]byte, 1)
		if _, from, err := conn.ReadFrom(buf); err != nil {
			t.Fatalf("destination %d read: %v", i+1, err)
		} else if got := from.(*net.UDPAddr).AddrPort(); got != destination {
			t.Fatalf("destination %d reply source = %v, want %v", i+1, got, destination)
		}
		if buf[0] != payload[0] {
			t.Fatalf("destination %d reply = %q, want %q", i+1, buf, payload)
		}
	}
}

// TestUDPAssociationAcceptsNegotiatedReplyPort is the HomeKit/RTP regression
// lock. The controller receives media on one port but sends its stateful return
// traffic from another port on the same policy-approved IP. A connected UDP
// upstream silently filters that response; the shared unconnected association
// must deliver it to the guest with the actual peer endpoint as its source.
func TestUDPAssociationAcceptsNegotiatedReplyPort(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen receiver: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	returner, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen returner: %v", err)
	}
	defer func() { _ = returner.Close() }()

	receiverAddr := receiver.LocalAddr().(*net.UDPAddr).AddrPort()
	returnerAddr := returner.LocalAddr().(*net.UDPAddr).AddrPort()
	if receiverAddr.Port() == returnerAddr.Port() {
		t.Fatal("test fixtures unexpectedly share a UDP port")
	}
	guestSrc := netip.AddrPortFrom(
		netip.MustParseAddr("10.0.0.5"),
		reserveUDPSourcePort(t),
	)
	policy, err := NewPolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan capturedReply, 1)
	log := &BufferLogger{}
	p := newUDPProxy(&Handler{
		Mode:   "broker",
		Policy: policy,
		Logger: log,
		ReplyTo: func(peer, src netip.AddrPort, payload []byte) error {
			replies <- capturedReply{
				origDst:  peer,
				guestSrc: src,
				payload:  append([]byte(nil), payload...),
			}
			return nil
		},
	})
	defer p.closeAll()

	p.handleUDPDatagram(guestSrc, receiverAddr, []byte("outbound-media"))

	if err := receiver.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, associationAddr, err := receiver.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("receive outbound datagram: %v", err)
	}
	if got := string(buf[:n]); got != "outbound-media" {
		t.Fatalf("outbound payload = %q, want outbound-media", got)
	}
	if got := uint16(associationAddr.Port); got != guestSrc.Port() {
		t.Fatalf("upstream source port = %d, want guest source port %d", got, guestSrc.Port())
	}

	if _, err := returner.WriteToUDP([]byte("negotiated-return"), associationAddr); err != nil {
		t.Fatalf("send negotiated-port reply: %v", err)
	}
	select {
	case reply := <-replies:
		if reply.origDst != returnerAddr {
			t.Fatalf("reply source = %v, want actual negotiated peer %v", reply.origDst, returnerAddr)
		}
		if reply.guestSrc != guestSrc {
			t.Fatalf("reply guest source = %v, want %v", reply.guestSrc, guestSrc)
		}
		if got := string(reply.payload); got != "negotiated-return" {
			t.Fatalf("reply payload = %q, want negotiated-return", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("negotiated-port reply was not delivered")
	}
	event := waitForEvent(t, log, "egress_udp_reply_port_change", time.Second)
	if got := event["peer"]; got != returnerAddr.String() {
		t.Fatalf("negotiated reply peer = %v, want %v", got, returnerAddr)
	}
}

// TestUDPAssociationRejectsUnapprovedReplyPeer proves the relaxed reply-port
// matching does not relax peer identity. Even after an allowed flow exists, a
// datagram to the preserved source port from another IP is dropped and audited.
func TestUDPAssociationRejectsUnapprovedReplyPeer(t *testing.T) {
	guestSrc := netip.MustParseAddrPort("10.0.0.5:51003")
	allowedPeer := netip.MustParseAddrPort("203.0.113.9:5000")
	roguePeer := netip.MustParseAddrPort("198.51.100.7:6000")
	policy, err := NewPolicy([]string{allowedPeer.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	upstream := newScriptedPacketConn()
	replies := make(chan capturedReply, 1)
	log := &BufferLogger{}
	p := newUDPProxy(&Handler{
		Mode:   "broker",
		Policy: policy,
		Logger: log,
		OpenUDP: func(netip.AddrPort) (net.PacketConn, error) {
			return upstream, nil
		},
		ReplyTo: func(peer, src netip.AddrPort, payload []byte) error {
			replies <- capturedReply{origDst: peer, guestSrc: src, payload: append([]byte(nil), payload...)}
			return nil
		},
	})
	defer p.closeAll()

	p.handleUDPDatagram(guestSrc, allowedPeer, []byte("open-association"))
	select {
	case write := <-upstream.writes:
		if write.to != allowedPeer {
			t.Fatalf("outbound destination = %v, want %v", write.to, allowedPeer)
		}
	case <-time.After(time.Second):
		t.Fatal("allowed outbound datagram was not written")
	}
	upstream.inject([]byte("spoofed"), roguePeer)

	event := waitForEvent(t, log, "egress_udp_reply_deny", 2*time.Second)
	if got := event["peer"]; got != roguePeer.String() {
		t.Fatalf("denied peer = %v, want %v", got, roguePeer)
	}
	select {
	case reply := <-replies:
		t.Fatalf("unapproved peer reply was delivered: %+v", reply)
	case <-time.After(100 * time.Millisecond):
	}
}

type scriptedPacket struct {
	payload []byte
	from    netip.AddrPort
}

type scriptedWrite struct {
	payload []byte
	to      netip.AddrPort
}

// scriptedPacketConn gives peer-policy tests arbitrary source addresses without
// assuming the host has extra loopback aliases (Darwin runners commonly do not).
type scriptedPacketConn struct {
	reads     chan scriptedPacket
	writes    chan scriptedWrite
	done      chan struct{}
	closeOnce sync.Once
}

func newScriptedPacketConn() *scriptedPacketConn {
	return &scriptedPacketConn{
		reads:  make(chan scriptedPacket, 1),
		writes: make(chan scriptedWrite, 1),
		done:   make(chan struct{}),
	}
}

func (c *scriptedPacketConn) inject(payload []byte, from netip.AddrPort) {
	c.reads <- scriptedPacket{payload: append([]byte(nil), payload...), from: from}
}

func (c *scriptedPacketConn) ReadFrom(buf []byte) (int, net.Addr, error) {
	select {
	case packet := <-c.reads:
		n := copy(buf, packet.payload)
		return n, net.UDPAddrFromAddrPort(packet.from), nil
	case <-c.done:
		return 0, nil, net.ErrClosed
	}
}

func (c *scriptedPacketConn) WriteTo(payload []byte, to net.Addr) (int, error) {
	ap, ok := addrPortFromNetAddr(to)
	if !ok {
		return 0, errors.New("unrecognized destination address")
	}
	c.writes <- scriptedWrite{payload: append([]byte(nil), payload...), to: ap}
	return len(payload), nil
}

func (c *scriptedPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}

func (c *scriptedPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *scriptedPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedPacketConn) SetWriteDeadline(time.Time) error { return nil }

// TestUDPSourcePortCollisionFailsClosed proves the mediator never falls back
// to a random source port when the negotiated port cannot be retained. Such a
// fallback would appear allowed in the audit while silently breaking the peer's
// return path.
func TestUDPSourcePortCollisionFailsClosed(t *testing.T) {
	blocker, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("occupy UDP source port: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	port := uint16(blocker.LocalAddr().(*net.UDPAddr).Port)
	guestSrc := netip.AddrPortFrom(netip.MustParseAddr("10.0.0.5"), port)
	origDst := netip.MustParseAddrPort("127.0.0.1:44444")
	policy, err := NewPolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	log := &BufferLogger{}
	p := newUDPProxy(&Handler{
		Mode:    "broker",
		Policy:  policy,
		Logger:  log,
		ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
	})
	defer p.closeAll()

	p.handleUDPDatagram(guestSrc, origDst, []byte("must-not-send"))

	if got := p.flowCount(); got != 0 {
		t.Fatalf("flow count = %d, want 0 after source-port collision", got)
	}
	assertEventWithField(t, log, "egress_udp_dial_error", "src", guestSrc.String())
	if eventLogged(log, "egress_udp_allow") {
		t.Fatal("source-port collision was audited as allowed")
	}
}

// TestUDPStrictDeniesUnlisted proves strict mode drops a datagram to a
// non-allowlisted origDst (no DialUDP call) and audits egress_udp_deny, while
// guarded mode forwards it (egress_udp_allow, unlisted:true).
func TestUDPStrictDeniesUnlisted(t *testing.T) {
	// Non-DNS ports: :53 routes to the DNS resolver-filter (covered by
	// TestUDPRoutesDNSToHandler); these drive the generic UDP flow policy path.
	allowed := netip.MustParseAddrPort("203.0.113.9:4433")
	denied := netip.MustParseAddrPort("198.51.100.7:4433")
	guestSrc := netip.MustParseAddrPort("10.0.0.5:51001")

	t.Run("strict denies unlisted", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy([]string{"203.0.113.9"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				dialed = true
				return nil, nil
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		p.handleUDPDatagram(guestSrc, denied, []byte("ping"))
		if dialed {
			t.Fatal("DialUDP called despite strict deny (must fail closed)")
		}
		assertEvent(t, log, "egress_udp_deny")
	})

	t.Run("strict allows allowlisted", func(t *testing.T) {
		echoAddr, cleanup := udpEchoServer(t)
		defer cleanup()
		pol, _ := NewPolicy([]string{"203.0.113.9"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				return net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(echoAddr))
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		p.handleUDPDatagram(guestSrc, allowed, []byte("ping"))
		assertEvent(t, log, "egress_udp_allow")
		assertEventFieldAbsent(t, log, "egress_udp_allow", "unlisted")
	})

	t.Run("guarded allows unlisted", func(t *testing.T) {
		echoAddr, cleanup := udpEchoServer(t)
		defer cleanup()
		pol, _ := NewPolicy([]string{"203.0.113.9"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:   "mitm",
			Policy: pol,
			Logger: log,
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				return net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(echoAddr))
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		p.handleUDPDatagram(guestSrc, denied, []byte("ping"))
		assertEventWithField(t, log, "egress_udp_allow", "unlisted", true)
	})
}

// TestUDPAssociationReusedAndClosedOnce drives many allowed destinations from
// one guest socket. They must share one upstream FD and reader goroutine, and
// shutdown must close that association exactly once.
func TestUDPAssociationReusedAndClosedOnce(t *testing.T) {
	var mu sync.Mutex
	opened := 0
	closed := 0
	h := &Handler{
		Mode:   "mitm",
		Policy: mustPolicy(t),
		Logger: &BufferLogger{},
		OpenUDP: func(netip.AddrPort) (net.PacketConn, error) {
			mu.Lock()
			opened++
			mu.Unlock()
			return newFakeUDPConn(func() {
				mu.Lock()
				closed++
				mu.Unlock()
			}), nil
		},
		ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
	}
	p := newUDPProxy(h)
	src := netip.MustParseAddrPort("10.0.0.5:51002")
	for i := 0; i < 250; i++ {
		dst := netip.AddrPortFrom(netip.MustParseAddr("203.0.113.9"), uint16(10000+i))
		p.handleUDPDatagram(src, dst, []byte("x"))
	}

	if got := p.flowCount(); got != 250 {
		t.Fatalf("flow count = %d, want 250", got)
	}
	mu.Lock()
	gotOpened := opened
	mu.Unlock()
	if gotOpened != 1 {
		t.Fatalf("upstream associations opened = %d, want 1", gotOpened)
	}

	p.closeAll()
	mu.Lock()
	gotClosed := closed
	mu.Unlock()
	if gotClosed != 1 {
		t.Fatalf("upstream associations closed = %d, want 1", gotClosed)
	}
}

// TestUDPFlowTableBounded drives more than maxUDPFlows distinct (src,origDst)
// flows and asserts the table stays bounded and that evicted upstream conns are
// closed (no fd/goroutine leak). It uses a fake upstream conn (no real socket)
// so the test exercises eviction deterministically without exhausting ephemeral
// ports.
func TestUDPFlowTableBounded(t *testing.T) {
	var mu sync.Mutex
	closed := 0

	log := &BufferLogger{}
	h := &Handler{
		Mode:   "mitm",
		Policy: mustPolicy(t),
		Logger: log,
		DialUDP: func(netip.AddrPort) (net.Conn, error) {
			return newFakeUDPConn(func() {
				mu.Lock()
				closed++
				mu.Unlock()
			}), nil
		},
		ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	total := maxUDPFlows + 50
	for i := 0; i < total; i++ {
		// Distinct (src,origDst) per flow; port disambiguates the wrapped octet.
		od := netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(i & 0xff)}), uint16(40000+i))
		src := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, 0, byte(i & 0xff)}), uint16(50000+i))
		p.handleUDPDatagram(src, od, []byte("x"))
	}

	if got := p.flowCount(); got > maxUDPFlows {
		t.Fatalf("flow table not bounded: %d > %d", got, maxUDPFlows)
	}
	// At least (total - maxUDPFlows) flows must have been evicted and closed.
	wantClosed := total - maxUDPFlows
	mu.Lock()
	gotClosed := closed
	mu.Unlock()
	if gotClosed < wantClosed {
		t.Fatalf("evicted flows closed = %d, want >= %d (fd leak?)", gotClosed, wantClosed)
	}
}

// fakeUDPConn is an in-memory net.Conn stub for the flow table: Write succeeds
// silently, Read blocks until Close (so the reader goroutine parks like a real
// idle upstream), and Close unblocks Read once and invokes onClose.
type fakeUDPConn struct {
	onClose  func()
	closeOne sync.Once
	done     chan struct{}
}

func newFakeUDPConn(onClose func()) *fakeUDPConn {
	return &fakeUDPConn{onClose: onClose, done: make(chan struct{})}
}

func (c *fakeUDPConn) Read(b []byte) (int, error) {
	<-c.done
	return 0, net.ErrClosed
}
func (c *fakeUDPConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *fakeUDPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := c.Read(b)
	return n, &net.UDPAddr{}, err
}
func (c *fakeUDPConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }
func (c *fakeUDPConn) Close() error {
	c.closeOne.Do(func() {
		close(c.done)
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}
func (c *fakeUDPConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *fakeUDPConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (c *fakeUDPConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeUDPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeUDPConn) SetWriteDeadline(time.Time) error { return nil }

// countingConn wraps a net.Conn to count Close calls (fd-leak detection).
type countingConn struct {
	net.Conn
	once    sync.Once
	onClose func()
}

func (c *countingConn) Close() error {
	c.once.Do(c.onClose)
	return c.Conn.Close()
}

// TestUDPFlowIdleClose proves an idle flow is swept, closed, and audited
// egress_udp_close.
func TestUDPFlowIdleClose(t *testing.T) {
	echoAddr, cleanup := udpEchoServer(t)
	defer cleanup()

	closed := make(chan struct{}, 1)
	log := &BufferLogger{}
	h := &Handler{
		Mode:   "mitm",
		Policy: mustPolicy(t),
		Logger: log,
		DialUDP: func(netip.AddrPort) (net.Conn, error) {
			c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(echoAddr))
			if err != nil {
				return nil, err
			}
			return &countingConn{Conn: c, onClose: func() {
				select {
				case closed <- struct{}{}:
				default:
				}
			}}, nil
		},
		ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
	}
	// Tiny idle window so the sweep fires quickly.
	p := newUDPProxyWithIdle(h, 30*time.Millisecond, 10*time.Millisecond)
	defer p.closeAll()

	src := netip.MustParseAddrPort("10.0.0.5:51010")
	od := netip.MustParseAddrPort("203.0.113.9:4433") // non-DNS: drive the flow path, not the DNS one-shot
	p.handleUDPDatagram(src, od, []byte("ping"))

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("idle flow not closed within timeout")
	}
	// Give the sweep a moment to emit the audit event.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if eventLogged(log, "egress_udp_close") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertEvent(t, log, "egress_udp_close")
	if p.flowCount() != 0 {
		t.Fatalf("flow table not emptied after idle close: %d", p.flowCount())
	}
}

func eventLogged(log *BufferLogger, event string) bool {
	for _, e := range log.Snapshot() {
		if e["event"] == event {
			return true
		}
	}
	return false
}

// TestUDPProxyLoopGuardDropsOwnBindAddr proves the UDP loop guard: a datagram
// whose recovered original destination equals the mediator's own bind address is
// dropped and audited egress_loop_guard, with NO upstream DialUDP attempted — the
// UDP analogue of the TCP self-loop break.
func TestUDPProxyLoopGuardDropsOwnBindAddr(t *testing.T) {
	bind := netip.MustParseAddrPort("10.43.7.1:43517")
	dialed := false
	log := &BufferLogger{}
	h := &Handler{
		Mode:     "mitm",
		Policy:   mustPolicy(t),
		Logger:   log,
		BindAddr: bind,
		DialUDP:  func(netip.AddrPort) (net.Conn, error) { dialed = true; return nil, nil },
		ReplyTo:  func(netip.AddrPort, netip.AddrPort, []byte) error { return nil },
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	// origDst == the mediator's own bind address: the self-loop condition.
	p.handleUDPDatagram(netip.MustParseAddrPort("10.0.0.5:51000"), bind, []byte("ping"))

	if dialed {
		t.Fatal("upstream UDP dialed for the mediator's own bind address (self-loop not guarded)")
	}
	if p.flowCount() != 0 {
		t.Fatalf("loop-guard datagram created a flow (count=%d), must be dropped", p.flowCount())
	}
	assertEvent(t, log, "egress_loop_guard")
}

// TestUDPRoutesDNSToHandler proves a UDP:53 datagram is handled by the filtering
// DNS forwarder (one-shot, no flow), not the normal flow path:
//   - an allowlisted name is forwarded (via injected dnsForward), the answer is
//     delivered to the guest with spoofed source = the resolver (origDst), the
//     name->IP mapping is cached, and NO flow is created;
//   - a non-allowlisted name in strict mode is REFUSED without forwarding.
func TestUDPRoutesDNSToHandler(t *testing.T) {
	resolver := netip.MustParseAddrPort("203.0.113.53:53")
	guestSrc := netip.MustParseAddrPort("10.0.0.5:52000")
	pol, _ := NewPolicy([]string{"allowed.example.com"})

	t.Run("allowlisted name forwarded, cached, no flow", func(t *testing.T) {
		log := &BufferLogger{}
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			NameCache:       NewNameCache(),
			ReplyTo:         func(netip.AddrPort, netip.AddrPort, []byte) error { return nil },
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				t.Fatal("DialUDP called for DNS (must be one-shot, no flow)")
				return nil, nil
			},
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		want := buildResponseWithA(t, 0x0101, "allowed.example.com.", "allowed.example.com.",
			[4]byte{203, 0, 113, 7}, 300)
		forwardCalled := false
		replies := make(chan capturedReply, 1)
		p.dnsForward = func(r netip.AddrPort, q []byte) ([]byte, error) {
			forwardCalled = true
			if r != resolver {
				t.Errorf("dnsForward resolver = %v, want %v", r, resolver)
			}
			return want, nil
		}
		p.replyTo = func(od, gs netip.AddrPort, payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			replies <- capturedReply{origDst: od, guestSrc: gs, payload: cp}
			return nil
		}

		query := buildQuery(t, 0x0101, "allowed.example.com.", dnsmessage.TypeA)
		p.handleUDPDatagram(guestSrc, resolver, query)

		// DNS is forwarded in its own goroutine (so a blocking resolver round-trip
		// never stalls the serveUDP loop), so wait for the reply before asserting:
		// the reply is sent last (after forward + cache), so its arrival establishes
		// a happens-before for forwardCalled and the NameCache below.
		select {
		case r := <-replies:
			if string(r.payload) != string(want) {
				t.Error("reply payload != forwarded DNS response")
			}
			// Spoofed source must be the resolver the guest targeted (origDst).
			if r.origDst != resolver {
				t.Errorf("reply spoofed-source = %v, want resolver %v", r.origDst, resolver)
			}
			if r.guestSrc != guestSrc {
				t.Errorf("reply guestSrc = %v, want %v", r.guestSrc, guestSrc)
			}
		case <-time.After(time.Second):
			t.Fatal("DNS response not delivered via replyTo")
		}
		if !forwardCalled {
			t.Fatal("dnsForward not called for an allowlisted query")
		}
		// Name cache now resolves the answer IP back to the queried name.
		if host, ok := h.NameCache.HostForIP(netip.AddrFrom4([4]byte{203, 0, 113, 7})); !ok || host != "allowed.example.com" {
			t.Errorf("NameCache HostForIP = (%q,%v), want (allowed.example.com,true)", host, ok)
		}
		// One-shot: a DNS datagram must NOT create a flow.
		if p.flowCount() != 0 {
			t.Fatalf("DNS datagram created %d flow(s); must be one-shot with no flow", p.flowCount())
		}
		assertEvent(t, log, "egress_dns_allow")
	})

	t.Run("non-allowlisted name refused without forwarding", func(t *testing.T) {
		log := &BufferLogger{}
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			NameCache:       NewNameCache(),
			ReplyTo:         func(netip.AddrPort, netip.AddrPort, []byte) error { return nil },
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				t.Fatal("DialUDP called for DNS deny")
				return nil, nil
			},
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		forwardCalled := false
		replies := make(chan capturedReply, 1)
		p.dnsForward = func(netip.AddrPort, []byte) ([]byte, error) {
			forwardCalled = true
			return nil, nil
		}
		p.replyTo = func(od, gs netip.AddrPort, payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			replies <- capturedReply{origDst: od, guestSrc: gs, payload: cp}
			return nil
		}

		query := buildQuery(t, 0x0102, "blocked.example.com.", dnsmessage.TypeA)
		p.handleUDPDatagram(guestSrc, resolver, query)

		// DNS handling is async (its own goroutine); wait for the synthesized reply
		// before asserting forwardCalled (the reply's arrival happens-after the
		// handler decided not to forward).
		select {
		case r := <-replies:
			// The reply is a synthesized REFUSED response.
			var dp dnsmessage.Parser
			hdr, err := dp.Start(r.payload)
			if err != nil {
				t.Fatalf("parse refused reply: %v", err)
			}
			if hdr.RCode != dnsmessage.RCodeRefused {
				t.Errorf("reply RCode = %v, want %v", hdr.RCode, dnsmessage.RCodeRefused)
			}
		case <-time.After(time.Second):
			t.Fatal("REFUSED response not delivered via replyTo")
		}
		if forwardCalled {
			t.Error("dnsForward called for a strict-denied query; must not forward")
		}
		if p.flowCount() != 0 {
			t.Fatalf("DNS deny created %d flow(s); must create none", p.flowCount())
		}
		assertEvent(t, log, "egress_dns_deny")
	})
}

// TestServeDNSAuditsReplyFailure proves a DNS answer that cannot be delivered to
// the guest leaves a trace: when replyTo fails (in production, the transparent
// reply socket's bind colliding with a pasta-mirrored wildcard :53 socket in the
// workspace netns), serveDNS must audit egress_dns_reply_error. Before this
// event existed the audit showed egress_dns_allow while the guest timed out
// EAI_AGAIN — an allowed-and-forwarded answer vanishing without a trace.
func TestServeDNSAuditsReplyFailure(t *testing.T) {
	resolver := netip.MustParseAddrPort("203.0.113.53:53")
	guestSrc := netip.MustParseAddrPort("10.0.0.5:52000")
	pol, _ := NewPolicy([]string{"allowed.example.com"})
	log := &BufferLogger{}
	h := &Handler{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          pol,
		Logger:          log,
		NameCache:       NewNameCache(),
		ReplyTo:         func(netip.AddrPort, netip.AddrPort, []byte) error { return nil },
		DialUDP: func(netip.AddrPort) (net.Conn, error) {
			t.Fatal("DialUDP called for DNS")
			return nil, nil
		},
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	resp := buildResponseWithA(t, 0x0103, "allowed.example.com.", "allowed.example.com.",
		[4]byte{203, 0, 113, 7}, 300)
	p.dnsForward = func(netip.AddrPort, []byte) ([]byte, error) { return resp, nil }
	p.replyTo = func(netip.AddrPort, netip.AddrPort, []byte) error {
		return errors.New("bind transparent reply socket to 203.0.113.53:53: address already in use")
	}

	query := buildQuery(t, 0x0103, "allowed.example.com.", dnsmessage.TypeA)
	p.handleUDPDatagram(guestSrc, resolver, query)
	// DNS handling is async (its own goroutine); dnsWG.Wait ensures the audit
	// write happens-before the assertion.
	p.dnsWG.Wait()

	assertEvent(t, log, "egress_dns_allow")
	assertEvent(t, log, "egress_dns_reply_error")
}

// TestDNSForwardDoesNotStallOtherDatagrams is the regression guard for the Phase 4
// bug where DNS (UDP:53) was forwarded synchronously inside handleUDPDatagram and
// the single-threaded serveUDP receive loop blocked for the whole resolver
// round-trip. A slow resolver (here: a dnsForward that blocks until released)
// would then wedge every other UDP datagram — and, because the host's stack was
// kept busy, the guest's concurrent upstream TCP fetch returned 0 bytes. DNS is
// now dispatched in its own goroutine, so a blocking forward must not delay a
// concurrent non-DNS datagram. We assert the non-DNS reply arrives while the DNS
// forward is still blocked, then release it.
func TestDNSForwardDoesNotStallOtherDatagrams(t *testing.T) {
	echoAddr, cleanup := udpEchoServer(t)
	defer cleanup()

	resolver := netip.MustParseAddrPort("203.0.113.53:53")
	dataDst := netip.MustParseAddrPort("203.0.113.9:4433") // non-DNS port -> generic flow
	guestSrc := netip.MustParseAddrPort("10.0.0.5:53000")

	replies := make(chan capturedReply, 4)
	h := &Handler{
		Mode:      "mitm",
		Policy:    mustPolicy(t),
		Logger:    &BufferLogger{},
		NameCache: NewNameCache(),
		DialUDP: func(netip.AddrPort) (net.Conn, error) {
			return net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(echoAddr))
		},
		ReplyTo: func(od, gs netip.AddrPort, payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			replies <- capturedReply{origDst: od, guestSrc: gs, payload: cp}
			return nil
		},
	}
	p := newUDPProxy(h)
	defer p.closeAll()

	// dnsForward blocks until released — simulating a slow/silent resolver. If DNS
	// were handled inline in the receive loop (the regression), the non-DNS
	// datagram below would never be processed until release.
	release := make(chan struct{})
	dnsEntered := make(chan struct{})
	p.dnsForward = func(netip.AddrPort, []byte) ([]byte, error) {
		close(dnsEntered)
		<-release
		return buildResponseWithA(t, 0x0303, "allowed.example.com.", "allowed.example.com.",
			[4]byte{203, 0, 113, 7}, 300), nil
	}

	// Fire the DNS query. handleUDPDatagram must return promptly (the forward runs
	// in its own goroutine); we drive it from a helper goroutine and require it to
	// return so a regression that forwards inline — blocking the caller until
	// release — fails this assertion instead of only hanging.
	dnsDispatched := make(chan struct{})
	go func() {
		p.handleUDPDatagram(guestSrc, resolver, buildQuery(t, 0x0303, "allowed.example.com.", dnsmessage.TypeA))
		close(dnsDispatched)
	}()
	select {
	case <-dnsEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("dnsForward was never entered")
	}
	select {
	case <-dnsDispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("handleUDPDatagram did not return while the DNS forward was blocked: DNS is being forwarded inline and stalls the receive loop")
	}

	// While DNS is still blocked, a non-DNS datagram must be forwarded and echoed
	// back promptly.
	p.handleUDPDatagram(guestSrc, dataDst, []byte("ping"))

	select {
	case r := <-replies:
		// The first reply must be the non-DNS echo, proving DNS did not stall it.
		if string(r.payload) != "ping" {
			t.Fatalf("first reply payload = %q, want the non-DNS echo \"ping\" (DNS forward stalled the loop)", r.payload)
		}
		if r.origDst != dataDst {
			t.Fatalf("first reply spoofed-source = %v, want non-DNS origDst %v", r.origDst, dataDst)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("non-DNS datagram was not forwarded while DNS forward was blocked (loop stalled)")
	}

	// Release the DNS forward; its reply (the synthesized A answer) must now arrive.
	close(release)
	select {
	case r := <-replies:
		if r.origDst != resolver {
			t.Fatalf("DNS reply spoofed-source = %v, want resolver %v", r.origDst, resolver)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DNS reply not delivered after release")
	}
}

// TestStrictUDPByName proves a non-DNS UDP flow is policed by the hostname the
// guest resolved (NameCache reverse lookup), not by bare IP: a datagram to a
// cached, allowlisted IP is allowed; a datagram to an uncached IP is denied
// fail-closed with no upstream dial.
func TestStrictUDPByName(t *testing.T) {
	allowedIP := netip.AddrFrom4([4]byte{203, 0, 113, 7})
	pol, _ := NewPolicy([]string{"allowed.example.com"})

	t.Run("cached allowlisted IP allowed by name", func(t *testing.T) {
		echoAddr, cleanup := udpEchoServer(t)
		defer cleanup()
		log := &BufferLogger{}
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			NameCache:       NewNameCache(),
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				return net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(echoAddr))
			},
			ReplyTo: func(netip.AddrPort, netip.AddrPort, []byte) error { return nil },
		}
		h.NameCache.Put("allowed.example.com", allowedIP, time.Minute)
		p := newUDPProxy(h)
		defer p.closeAll()

		dst := netip.AddrPortFrom(allowedIP, 4433)
		p.handleUDPDatagram(netip.MustParseAddrPort("10.0.0.5:52100"), dst, []byte("ping"))

		assertEvent(t, log, "egress_udp_allow")
		// Allowed by hostname match, not guarded mode: not "unlisted".
		assertEventFieldAbsent(t, log, "egress_udp_allow", "unlisted")
		if p.flowCount() != 1 {
			t.Fatalf("flowCount = %d, want 1 (flow created for allowed UDP)", p.flowCount())
		}
	})

	t.Run("uncached IP denied with no dial", func(t *testing.T) {
		dialed := false
		log := &BufferLogger{}
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			NameCache:       NewNameCache(),
			DialUDP:         func(netip.AddrPort) (net.Conn, error) { dialed = true; return nil, nil },
			ReplyTo:         func(netip.AddrPort, netip.AddrPort, []byte) error { return nil },
		}
		h.NameCache.Put("allowed.example.com", allowedIP, time.Minute)
		p := newUDPProxy(h)
		defer p.closeAll()

		uncached := netip.MustParseAddrPort("198.51.100.9:4433")
		p.handleUDPDatagram(netip.MustParseAddrPort("10.0.0.5:52101"), uncached, []byte("ping"))

		if dialed {
			t.Fatal("DialUDP called for an uncached/unlisted IP (must fail closed)")
		}
		if p.flowCount() != 0 {
			t.Fatalf("flowCount = %d, want 0 (denied UDP must create no flow)", p.flowCount())
		}
		assertEvent(t, log, "egress_udp_deny")
	})
}

func mustPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := NewPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return p
}

// TestGuardedUDP verifies the guarded-mode inside-deny path in handleUDPDatagram:
//
//   - guarded drops UDP to 169.254.169.254:123 → egress_udp_internal_deny, NO upstream DialUDP
//   - guarded allows UDP to a public IP (203.0.113.9:123) → DialUDP called, egress_udp_allow
//   - guarded + allowlisted internal (169.254.169.254 on the allowlist) → DialUDP called
//   - DNS datagrams (port 53) to an inside address pass through without triggering inside-deny
func TestGuardedUDP(t *testing.T) {
	imds := netip.MustParseAddrPort("169.254.169.254:123")
	public := netip.MustParseAddrPort("203.0.113.9:123")
	guestSrc := netip.MustParseAddrPort("10.0.0.5:55000")

	t.Run("guarded drops inside addr no dial", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy(nil)
		log := &BufferLogger{}
		h := &Handler{
			Mode:   egressModeMITM,
			Policy: pol,
			Logger: log,
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				dialed = true
				return nil, nil
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		p.handleUDPDatagram(guestSrc, imds, []byte("ping"))

		if dialed {
			t.Fatal("DialUDP called for inside addr under guarded (must fail closed)")
		}
		if p.flowCount() != 0 {
			t.Fatalf("guarded inside deny created a flow (count=%d), must be dropped", p.flowCount())
		}
		assertEvent(t, log, "egress_udp_internal_deny")
	})

	t.Run("guarded allows public addr", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy(nil)
		log := &BufferLogger{}
		h := &Handler{
			Mode:   egressModeMITM,
			Policy: pol,
			Logger: log,
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				dialed = true
				return newFakeUDPConn(nil), nil
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		p.handleUDPDatagram(guestSrc, public, []byte("ping"))

		if !dialed {
			t.Fatal("DialUDP not called for public addr under guarded (must be allowed)")
		}
		assertEvent(t, log, "egress_udp_allow")
	})

	t.Run("guarded allowlisted inside overrides deny", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy([]string{"169.254.169.254"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:   egressModeMITM,
			Policy: pol,
			Logger: log,
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				dialed = true
				return newFakeUDPConn(nil), nil
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		p.handleUDPDatagram(guestSrc, imds, []byte("ping"))

		if !dialed {
			t.Fatal("DialUDP not called for allowlisted inside addr under guarded (allowlist must override)")
		}
		assertEvent(t, log, "egress_udp_allow")
	})

	t.Run("guarded DNS port 53 to inside addr takes DNS path and refuses the resolver", func(t *testing.T) {
		// A port-53 datagram branches to the DNS handler (serveDNS), not the raw-UDP
		// flow / inside-deny path. Under the confused-deputy guard, handleDNS then
		// REFUSES to forward to an inside resolver address (no configured resolver
		// set), so dnsForward is never called and the refusal is audited as
		// egress_dns_deny — never egress_udp_internal_deny (which would mean the
		// raw-UDP inside-deny path handled it) and never a DialUDP flow.
		insideDNS := netip.MustParseAddrPort("169.254.169.254:53")
		dialedUDP := false
		pol, _ := NewPolicy([]string{"example.com"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:      egressModeMITM,
			Policy:    pol,
			Logger:    log,
			NameCache: NewNameCache(),
			DialUDP: func(netip.AddrPort) (net.Conn, error) {
				dialedUDP = true
				return nil, nil
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		dnsForwarded := false
		p.dnsForward = func(_ netip.AddrPort, _ []byte) ([]byte, error) {
			dnsForwarded = true
			return nil, nil
		}

		query := buildQuery(t, 0x1001, "example.com.", dnsmessage.TypeA)
		p.handleUDPDatagram(guestSrc, insideDNS, query)

		// The refusal happens on the async per-datagram DNS goroutine; wait for the
		// egress_dns_deny audit record (log Snapshot is mutex-guarded, providing the
		// synchronization). On the refuse path neither callback fires, so the bools
		// below are never written concurrently.
		deadline := time.Now().Add(time.Second)
		denied := false
		for time.Now().Before(deadline) {
			for _, e := range log.Snapshot() {
				if e["event"] == "egress_udp_internal_deny" {
					t.Fatalf("egress_udp_internal_deny logged for a DNS datagram (DNS path, not raw-UDP inside-deny, must handle it)")
				}
				if e["event"] == "egress_dns_deny" {
					denied = true
				}
			}
			if denied {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !denied {
			t.Fatal("egress_dns_deny not logged: the DNS path did not refuse the inside resolver")
		}
		if dnsForwarded {
			t.Fatal("dnsForward called for an inside resolver; the confused-deputy guard must refuse it")
		}
		if dialedUDP {
			t.Fatal("DialUDP called for a DNS (port-53) datagram (must be one-shot, no flow)")
		}
	})
}
