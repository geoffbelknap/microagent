package egress

import (
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
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
	origDst := netip.MustParseAddrPort("203.0.113.9:53")

	replies := make(chan capturedReply, 4)
	log := &BufferLogger{}
	h := &Handler{
		Mode:   "mediated",
		Policy: mustPolicy(t),
		Logger: log,
		// DialUDP ignores origDst and dials the real echo server so we can
		// exercise the full forward + reply loop without TPROXY.
		DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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
}

// TestUDPStrictDeniesUnlisted proves strict mode drops a datagram to a
// non-allowlisted origDst (no DialUDP call) and audits egress_udp_deny, while
// mediated mode forwards it (egress_udp_allow, unlisted:true).
func TestUDPStrictDeniesUnlisted(t *testing.T) {
	allowed := netip.MustParseAddrPort("203.0.113.9:53")
	denied := netip.MustParseAddrPort("198.51.100.7:53")
	guestSrc := netip.MustParseAddrPort("10.0.0.5:51001")

	t.Run("strict denies unlisted", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy([]string{"203.0.113.9"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:   "strict",
			Policy: pol,
			Logger: log,
			DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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
			Mode:   "strict",
			Policy: pol,
			Logger: log,
			DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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

	t.Run("mediated allows unlisted", func(t *testing.T) {
		echoAddr, cleanup := udpEchoServer(t)
		defer cleanup()
		pol, _ := NewPolicy([]string{"203.0.113.9"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:   "mediated",
			Policy: pol,
			Logger: log,
			DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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
		Mode:   "mediated",
		Policy: mustPolicy(t),
		Logger: log,
		DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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
		Mode:   "mediated",
		Policy: mustPolicy(t),
		Logger: log,
		DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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
	od := netip.MustParseAddrPort("203.0.113.9:53")
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
	log.mu.Lock()
	defer log.mu.Unlock()
	for _, e := range log.Events {
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
		Mode:     "mediated",
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

func mustPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := NewPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return p
}
