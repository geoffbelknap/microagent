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
	origDst := netip.MustParseAddrPort("203.0.113.9:443")

	replies := make(chan capturedReply, 4)
	log := &BufferLogger{}
	h := &Handler{
		Mode:   "mitm",
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
// guarded mode forwards it (egress_udp_allow, unlisted:true).
func TestUDPStrictDeniesUnlisted(t *testing.T) {
	// Non-DNS ports: :53 routes to the DNS resolver-filter (covered by
	// TestUDPRoutesDNSToHandler); these drive the generic UDP flow policy path.
	allowed := netip.MustParseAddrPort("203.0.113.9:443")
	denied := netip.MustParseAddrPort("198.51.100.7:443")
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
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
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

	t.Run("guarded allows unlisted", func(t *testing.T) {
		echoAddr, cleanup := udpEchoServer(t)
		defer cleanup()
		pol, _ := NewPolicy([]string{"203.0.113.9"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:   "mitm",
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
		Mode:   "mitm",
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
		Mode:   "mitm",
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
	od := netip.MustParseAddrPort("203.0.113.9:443") // non-DNS: drive the flow path, not the DNS one-shot
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
			DialUDP:         func(netip.AddrPort) (net.Conn, error) { t.Fatal("DialUDP called for DNS deny"); return nil, nil },
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
		DialUDP:         func(netip.AddrPort) (net.Conn, error) { t.Fatal("DialUDP called for DNS"); return nil, nil },
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
	dataDst := netip.MustParseAddrPort("203.0.113.9:443") // non-DNS port -> generic flow
	guestSrc := netip.MustParseAddrPort("10.0.0.5:53000")

	replies := make(chan capturedReply, 4)
	h := &Handler{
		Mode:      "mitm",
		Policy:    mustPolicy(t),
		Logger:    &BufferLogger{},
		NameCache: NewNameCache(),
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

		dst := netip.AddrPortFrom(allowedIP, 443)
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

		uncached := netip.MustParseAddrPort("198.51.100.9:443")
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
			DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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
			DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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
			DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
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

	t.Run("guarded DNS port 53 to inside addr not blocked by inside-deny", func(t *testing.T) {
		// DNS (port 53) datagrams branch to the DNS handler before the inside-deny
		// check — Task 5 will handle DNS under guarded; Task 4 must not touch that path.
		// We use a valid DNS query (buildQuery) because handleDNS parses the payload;
		// arbitrary bytes fail the parse and serveDNS drops without calling dnsForward,
		// making it impossible to confirm the DNS branch was taken.
		insideDNS := netip.MustParseAddrPort("169.254.169.254:53")
		dialedUDP := false
		// A policy that allows the query name so handleDNS forwards it (and calls dnsForward).
		pol, _ := NewPolicy([]string{"example.com"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:      egressModeMITM,
			Policy:    pol,
			Logger:    log,
			NameCache: NewNameCache(),
			DialUDP: func(_ netip.AddrPort) (net.Conn, error) {
				dialedUDP = true
				return nil, nil
			},
			ReplyTo: func(_, _ netip.AddrPort, _ []byte) error { return nil },
		}
		p := newUDPProxy(h)
		defer p.closeAll()

		// Inject a dnsForward that signals it was reached.
		dnsHandled := make(chan struct{}, 1)
		p.dnsForward = func(_ netip.AddrPort, _ []byte) ([]byte, error) {
			select {
			case dnsHandled <- struct{}{}:
			default:
			}
			return nil, nil // drop the response; we only need to confirm the path
		}

		query := buildQuery(t, 0x1001, "example.com.", dnsmessage.TypeA)
		p.handleUDPDatagram(guestSrc, insideDNS, query)

		// Wait for the async DNS goroutine to complete.
		select {
		case <-dnsHandled:
		case <-time.After(time.Second):
			t.Fatal("dnsForward not called: DNS path was not taken for port-53 datagram to inside addr")
		}

		// The DNS path must be taken and DialUDP must NOT be called (DNS is one-shot).
		if dialedUDP {
			t.Fatal("DialUDP called for a DNS (port-53) datagram (must be one-shot, no flow)")
		}
		// egress_udp_internal_deny must NOT be logged — the DNS path exits before the inside-deny.
		for _, e := range log.Snapshot() {
			if e["event"] == "egress_udp_internal_deny" {
				t.Fatalf("egress_udp_internal_deny logged for a DNS datagram (inside-deny must not apply on DNS path)")
			}
		}
	})
}
