package egress

import (
	"bufio"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// shouldMITM is the termination-choice predicate. The security invariant under
// test: broker mode NEVER forges certificates, even if a CA is somehow present —
// it splices opaquely. guarded keeps forging per-SNI leaves for allowed TLS.
func TestHandlerShouldMITM(t *testing.T) {
	ca := &CA{} // non-nil sentinel; the predicate checks presence, not validity
	cases := []struct {
		name                                string
		h                                   *Handler
		isTLS, allowed, passthrough, isPeer bool
		want                                bool
	}{
		{"guarded_tls_allowed_mitms", &Handler{Mode: "mitm", CA: ca}, true, true, false, false, true},
		{"broker_never_mitms_even_with_ca", &Handler{Mode: "broker", CA: ca}, true, true, false, false, false},
		{"no_ca", &Handler{Mode: "mitm"}, true, true, false, false, false},
		{"not_tls", &Handler{Mode: "mitm", CA: ca}, false, true, false, false, false},
		{"passthrough", &Handler{Mode: "mitm", CA: ca}, true, true, true, false, false},
		{"peer", &Handler{Mode: "mitm", CA: ca}, true, true, false, true, false},
		{"denied", &Handler{Mode: "mitm", CA: ca}, true, false, false, false, false},
	}
	for _, c := range cases {
		if got := c.h.shouldMITM(c.isTLS, c.allowed, c.passthrough, c.isPeer); got != c.want {
			t.Errorf("%s: shouldMITM = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHandlerAllowForwards(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c) // echo
		c.Close()
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	pol, _ := NewPolicy([]string{"api.github.com"})
	log := &BufferLogger{}
	h := &Handler{
		Policy:       pol,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:         net.Dial,
		SniffTimeout: 500 * time.Millisecond,
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	go func() {
		client.Write([]byte("GET / HTTP/1.1\r\nHost: api.github.com\r\n\r\n"))
	}()
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "GET / HTTP/1.1\r\n" {
		t.Fatalf("echo = %q err=%v", line, err)
	}
	client.Close()
	<-done // wait for Handle to finish writing the audit log before reading it
	assertEvent(t, log, "egress_allow")
}

func TestHandlerDenyFailsClosed(t *testing.T) {
	dialed := false
	pol, _ := NewPolicy([]string{"api.github.com"})
	log := &BufferLogger{}
	h := &Handler{
		Policy:       pol,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.0.0.9:443"), nil },
		Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
		SniffTimeout: 500 * time.Millisecond,
	}
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	go client.Write([]byte("GET / HTTP/1.1\r\nHost: evil.com\r\n\r\n"))
	<-done
	client.Close()
	if dialed {
		t.Fatal("upstream dialed despite deny (must fail closed)")
	}
	assertEvent(t, log, "egress_deny")
}

// TestGuardedAllowsUnlisted proves the Mode field's two behaviors against a
// public host that is NOT on the allowlist:
//   - Mode "guarded": the connection is forwarded (L4 splice roundtrips) and
//     logged egress_allow with unlisted=true (the guarded-public grant).
//   - Mode "strict": the same host is denied (egress_deny, no upstream dial).
func TestGuardedAllowsUnlisted(t *testing.T) {
	t.Run("guarded forwards unlisted public", func(t *testing.T) {
		up, _ := net.Listen("tcp", "127.0.0.1:0")
		defer up.Close()
		go func() {
			c, err := up.Accept()
			if err != nil {
				return
			}
			io.Copy(c, c) // echo
			c.Close()
		}()
		upAddr := netip.MustParseAddrPort(up.Addr().String())

		pol, _ := NewPolicy([]string{"allowed.example.com"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:   "mitm",
			Policy: pol,
			Logger: log,
			// OrigDst returns a public address (not on the allowlist) but we Dial
			// the local echo server so the test needs no outbound internet.
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("93.184.216.34:80"), nil },
			Dial:         func(network, addr string) (net.Conn, error) { return net.Dial(network, upAddr.String()) },
			SniffTimeout: 500 * time.Millisecond,
		}

		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go func() {
			client.Write([]byte("GET / HTTP/1.1\r\nHost: unlisted.example.com\r\n\r\n"))
		}()
		br := bufio.NewReader(client)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || line != "GET / HTTP/1.1\r\n" {
			t.Fatalf("echo = %q err=%v (unlisted public host must be forwarded in guarded mode)", line, err)
		}
		client.Close()
		<-done
		assertEventWithField(t, log, "egress_allow", "unlisted", true)
	})

	t.Run("locked allowlist denies unlisted", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy([]string{"allowed.example.com"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			// A public, non-allowlisted destination: denied purely for the
			// allowlist miss (egress_deny), not the inside classification.
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("203.0.113.9:443"), nil },
			Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
			SniffTimeout: 500 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go client.Write([]byte("GET / HTTP/1.1\r\nHost: unlisted.example.com\r\n\r\n"))
		<-done
		client.Close()
		if dialed {
			t.Fatal("upstream dialed despite locked-allowlist deny (must fail closed)")
		}
		assertEvent(t, log, "egress_deny")
	})
}

// TestPassthroughNotUnlisted proves that a passthrough host in guarded
// mode is L4-spliced (forwarded, not MITM'd) and its egress_allow audit record
// does NOT carry unlisted: a passthrough host is explicitly listed, and strict
// would allow it too, so it is not an "unlisted" grant. This guards the
// `unlisted := allowed && !d.Allow && !passthrough` refinement (INV4). The host
// is intentionally NOT on the main allowlist (Policy), so without the
// `&& !passthrough` term it would wrongly be flagged unlisted.
func TestPassthroughNotUnlisted(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c) // echo
		c.Close()
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	pol, _ := NewPolicy([]string{"allowed.example.com"})
	passthrough, _ := NewPolicy([]string{"raw.example.com"})
	log := &BufferLogger{}
	h := &Handler{
		Mode:         "mitm",
		Policy:       pol,
		Passthrough:  passthrough,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:         net.Dial,
		SniffTimeout: 500 * time.Millisecond,
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	go func() {
		client.Write([]byte("GET / HTTP/1.1\r\nHost: raw.example.com\r\n\r\n"))
	}()
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "GET / HTTP/1.1\r\n" {
		t.Fatalf("echo = %q err=%v (passthrough host must be L4-spliced)", line, err)
	}
	client.Close()
	<-done
	assertEvent(t, log, "egress_allow")
	assertEventFieldAbsent(t, log, "egress_allow", "unlisted")
}

// TestHandlerLoopGuardDropsOwnBindAddr proves the loop guard: a captured TCP
// connection whose recovered original destination equals the mediator's own bind
// address (the mediator dialing itself — a readiness probe or residual self-dial)
// is dropped and audited egress_loop_guard, and NO upstream Dial is attempted.
// Without the guard this would be forwarded to the listener and spin into an
// unbounded self-loop. Guards against the observed TPROXY self-loop.
func TestHandlerLoopGuardDropsOwnBindAddr(t *testing.T) {
	bind := netip.MustParseAddrPort("10.43.7.1:43517")
	dialed := false
	pol, _ := NewPolicy([]string{"10.43.7.1"}) // allowlist the bind IP so only the loop guard (not the inside-deny) can stop the self-loop
	log := &BufferLogger{}
	h := &Handler{
		Mode:     "mitm", // the loop guard fires before the policy decision
		Policy:   pol,
		Logger:   log,
		BindAddr: bind,
		// OrigDst returns the mediator's OWN bind address — the self-loop condition.
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return bind, nil },
		Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
		SniffTimeout: 200 * time.Millisecond,
	}

	_, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle did not return; loop guard should drop immediately without sniffing")
	}
	if dialed {
		t.Fatal("upstream dialed for the mediator's own bind address (self-loop not guarded)")
	}
	assertEvent(t, log, "egress_loop_guard")
	// And it must NOT be audited as an allow (which is what produced the flood).
	for _, e := range log.Snapshot() {
		if e["event"] == "egress_allow" {
			t.Fatalf("self-loop destination wrongly audited egress_allow: %+v", e)
		}
	}
}

// TestHandlerLoopGuardDisabledWhenBindUnset proves the guard is opt-in: with a
// zero BindAddr (the default in tests that do not exercise it) a connection is
// handled normally, so existing forwarding behavior is unchanged.
func TestHandlerLoopGuardDisabledWhenBindUnset(t *testing.T) {
	if (&Handler{}).isOwnBindAddr(netip.MustParseAddrPort("10.43.7.1:43517")) {
		t.Fatal("zero BindAddr must disable the loop guard")
	}
	h := &Handler{BindAddr: netip.MustParseAddrPort("10.43.7.1:43517")}
	if !h.isOwnBindAddr(netip.MustParseAddrPort("10.43.7.1:43517")) {
		t.Fatal("matching bind address must trip the guard")
	}
	if h.isOwnBindAddr(netip.MustParseAddrPort("10.43.7.1:80")) {
		t.Fatal("same IP different port must not trip the guard")
	}
	if h.isOwnBindAddr(netip.MustParseAddrPort("104.20.23.154:43517")) {
		t.Fatal("different IP same port must not trip the guard")
	}
}

// TestTCPRawIPByName proves a raw-TCP connection with no SNI/Host (sniffHost
// falls back to the bare destination IP) is policed by the hostname the guest
// resolved (NameCache reverse lookup): a connection to a cached, allowlisted IP
// is allowed by name; a connection to an uncached IP is denied fail-closed.
func TestTCPRawIPByName(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }() // echo
		}
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	pol, _ := NewPolicy([]string{"allowed.example.com"})

	t.Run("cached allowlisted IP allowed by name", func(t *testing.T) {
		log := &BufferLogger{}
		nc := NewNameCache()
		nc.Put("allowed.example.com", upAddr.Addr(), time.Minute)
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			NameCache:       nc,
			OrigDst:         func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
			Dial:            net.Dial,
			SniffTimeout:    300 * time.Millisecond,
		}

		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		// Raw bytes: not a TLS ClientHello (no 0x16) and no HTTP Host header, so
		// sniffHost falls back to the bare destination IP.
		go func() { client.Write([]byte("RAWPING\n")) }()
		br := bufio.NewReader(client)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || line != "RAWPING\n" {
			t.Fatalf("echo = %q err=%v (allowlisted-by-name raw TCP must be forwarded)", line, err)
		}
		client.Close()
		<-done
		assertEvent(t, log, "egress_allow")
		// Matched by hostname, not the guarded-public grant: not "unlisted".
		assertEventFieldAbsent(t, log, "egress_allow", "unlisted")
	})

	t.Run("uncached IP denied", func(t *testing.T) {
		dialed := false
		uncached := netip.MustParseAddrPort("198.51.100.9:443")
		log := &BufferLogger{}
		nc := NewNameCache()
		nc.Put("allowed.example.com", upAddr.Addr(), time.Minute)
		h := &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          pol,
			Logger:          log,
			NameCache:       nc,
			OrigDst:         func(net.Conn) (netip.AddrPort, error) { return uncached, nil },
			Dial:            func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
			SniffTimeout:    300 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go client.Write([]byte("RAWPING\n"))
		<-done
		client.Close()
		if dialed {
			t.Fatal("upstream dialed for an uncached IP under strict (must fail closed)")
		}
		assertEvent(t, log, "egress_deny")
	})
}

// TestHandlerAllowsPeerByName proves a raw-TCP east-west flow whose original
// destination IP maps to a named-network peer is policed by the peer's workspace
// name: the peer is on the allowlist, so the connection is forwarded and audited
// egress_allow carrying the peer name (and peer_ip). There is no SNI/Host and no
// DNS NameCache entry — only the static PeerCache resolves the bare IP.
func TestHandlerAllowsPeerByName(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }() // echo
		}
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	// Allowlist the peer by its workspace name; PeerCache maps the dst IP -> name.
	pol, _ := NewPolicy([]string{"builder"})
	peers, err := NewPeerCache([]string{"builder=" + upAddr.Addr().String()})
	if err != nil {
		t.Fatalf("NewPeerCache: %v", err)
	}
	log := &BufferLogger{}
	h := &Handler{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          pol,
		Peers:           peers,
		Logger:          log,
		OrigDst:         func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:            net.Dial,
		SniffTimeout:    300 * time.Millisecond,
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	// Raw bytes: not a TLS ClientHello, no HTTP Host header -> bare-IP fallback.
	go func() { client.Write([]byte("RAWPING\n")) }()
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "RAWPING\n" {
		t.Fatalf("echo = %q err=%v (allowlisted peer raw TCP must be forwarded)", line, err)
	}
	client.Close()
	<-done
	assertEvent(t, log, "egress_allow")
	assertEventWithField(t, log, "egress_allow", "peer", "builder")
	assertEventWithField(t, log, "egress_allow", "peer_ip", upAddr.Addr().String())
}

// TestHandlerDeniesUnknownPeer proves default-deny stands for east-west: an
// original destination IP that is neither a known peer nor allowlisted is denied
// fail-closed (no upstream Dial), audited egress_deny.
func TestHandlerDeniesUnknownPeer(t *testing.T) {
	dialed := false
	pol, _ := NewPolicy([]string{"builder"})
	peers, err := NewPeerCache([]string{"builder=10.44.1.3"})
	if err != nil {
		t.Fatalf("NewPeerCache: %v", err)
	}
	log := &BufferLogger{}
	h := &Handler{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          pol,
		Peers:           peers,
		Logger:          log,
		// Not a known peer (10.44.1.99 is not in the roster) and not allowlisted.
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.44.1.99:443"), nil },
		Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
		SniffTimeout: 300 * time.Millisecond,
	}
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	go client.Write([]byte("RAWPING\n"))
	<-done
	client.Close()
	if dialed {
		t.Fatal("upstream dialed for an unknown peer under a locked allowlist (must fail closed)")
	}
	// The peer IP is RFC1918 (east-west), so an allow-broad-family mode classifies
	// it inside: denied as internal, the more informative east-west audit event.
	assertEvent(t, log, "egress_internal_deny")
}

// TestHandlerAllowsPeerByIPLiteral proves an operator may allowlist a peer by its
// IP literal: the dst resolves to a known peer name that is NOT on the allowlist,
// but the peer's IP IS, so the connection is forwarded. The audit still carries
// the resolved peer name and IP.
func TestHandlerAllowsPeerByIPLiteral(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }() // echo
		}
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	// Allowlist the IP literal, NOT the peer name.
	pol, _ := NewPolicy([]string{upAddr.Addr().String()})
	peers, err := NewPeerCache([]string{"builder=" + upAddr.Addr().String()})
	if err != nil {
		t.Fatalf("NewPeerCache: %v", err)
	}
	log := &BufferLogger{}
	h := &Handler{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          pol,
		Peers:           peers,
		Logger:          log,
		OrigDst:         func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:            net.Dial,
		SniffTimeout:    300 * time.Millisecond,
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	go func() { client.Write([]byte("RAWPING\n")) }()
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "RAWPING\n" {
		t.Fatalf("echo = %q err=%v (peer allowlisted by IP literal must be forwarded)", line, err)
	}
	client.Close()
	<-done
	assertEvent(t, log, "egress_allow")
	assertEventWithField(t, log, "egress_allow", "peer", "builder")
}

// TestHandlerEnforcesByteCap proves the process-wide volume cap (ASK tenet 8):
// with MaxTotalBytes set low, pushing more than the cap through a passthrough L4
// splice tears the breaching flow down and audits egress_cap_exceeded reason
// volume. The mediator does NOT crash; the cap only degrades the breaching flow.
func TestHandlerEnforcesByteCap(t *testing.T) {
	// Upstream sink that reads (and discards) everything the guest sends.
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(io.Discard, c); c.Close() }()
		}
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	pol, _ := NewPolicy([]string{"api.github.com"})
	log := &BufferLogger{}
	h := &Handler{
		Mode:   "mitm",
		Policy: pol,
		Logger: log,
		// OrigDst returns a public address (guarded allows it) but we Dial the
		// local sink so the test needs no outbound internet.
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("93.184.216.34:80"), nil },
		Dial:         func(network, addr string) (net.Conn, error) { return net.Dial(network, upAddr.String()) },
		SniffTimeout: 300 * time.Millisecond,
		Limits:       Limits{MaxTotalBytes: 1024},
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	// Send well past the 1024-byte cap; the splice must tear down once exceeded.
	go func() {
		buf := make([]byte, 4096)
		for i := 0; i < 16; i++ {
			if _, err := client.Write(buf); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after byte cap exceeded — flow not torn down")
	}
	client.Close()
	assertEvent(t, log, "egress_cap_exceeded")
	assertEventWithField(t, log, "egress_cap_exceeded", "reason", "volume")
}

// TestHandlerEnforcesConcurrencyCap proves the TCP concurrency cap fails closed:
// with MaxConcurrentConns=1, a second concurrent Handle is refused BEFORE dialing
// upstream (no dial), audited egress_cap_exceeded reason concurrency. ASK tenet 8.
func TestHandlerEnforcesConcurrencyCap(t *testing.T) {
	// Upstream that accepts and holds the first connection open so the gauge stays
	// at 1 while the second Handle runs.
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	hold := make(chan struct{})
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { <-hold; c.Close() }(c)
		}
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	pol, _ := NewPolicy([]string{"api.github.com"})
	log := &BufferLogger{}
	var dials int32
	h := &Handler{
		Mode:   "mitm",
		Policy: pol,
		Logger: log,
		// OrigDst returns a public address (guarded allows it) but we Dial the
		// local upstream so the test needs no outbound internet.
		OrigDst: func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("93.184.216.34:80"), nil },
		Dial: func(network, addr string) (net.Conn, error) {
			atomic.AddInt32(&dials, 1)
			return net.Dial(network, upAddr.String())
		},
		SniffTimeout: 300 * time.Millisecond,
		Limits:       Limits{MaxConcurrentConns: 1},
	}

	// First connection: occupies the single concurrency slot. It is a raw flow that
	// blocks on the held upstream, so the gauge stays at 1.
	client1, server1 := net.Pipe()
	done1 := make(chan struct{})
	go func() { h.Handle(server1); close(done1) }()
	go func() { client1.Write([]byte("RAWPING\n")) }()
	// Wait until the first flow has been admitted (gauge incremented + dialed).
	waitForEvent(t, log, "egress_allow", 2*time.Second)

	// Second concurrent connection: must be refused fail-closed (no dial).
	dialsBefore := atomic.LoadInt32(&dials)
	client2, server2 := net.Pipe()
	done2 := make(chan struct{})
	go func() { h.Handle(server2); close(done2) }()
	go func() { client2.Write([]byte("RAWPING2\n")) }()
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatal("second Handle did not return — concurrency cap not enforced")
	}
	client2.Close()
	if atomic.LoadInt32(&dials) != dialsBefore {
		t.Fatalf("second flow dialed upstream despite concurrency cap (dials %d -> %d, must fail closed)", dialsBefore, atomic.LoadInt32(&dials))
	}
	assertEvent(t, log, "egress_cap_exceeded")
	assertEventWithField(t, log, "egress_cap_exceeded", "reason", "concurrency")

	// Release the first flow and clean up.
	close(hold)
	client1.Close()
	<-done1
	<-done2
}

func assertEvent(t *testing.T, log *BufferLogger, event string) {
	t.Helper()
	snap := log.Snapshot()
	for _, e := range snap {
		if e["event"] == event {
			return
		}
	}
	t.Fatalf("event %q not logged; got %+v", event, snap)
}

// assertEventFieldAbsent fails if any logged event of the given name carries the
// named field at all (regardless of value).
func assertEventFieldAbsent(t *testing.T, log *BufferLogger, event string, field string) {
	t.Helper()
	for _, e := range log.Snapshot() {
		if e["event"] != event {
			continue
		}
		if _, present := e[field]; present {
			t.Fatalf("event %q unexpectedly carries field %q: %+v", event, field, e)
		}
	}
}

// TestGuardedTCP verifies the guarded-mode inside-deny path in Handle:
//
//   - guarded denies IMDS (169.254.169.254) → egress_internal_deny, no upstream dial
//   - guarded denies RFC1918 (10.0.0.5) → egress_internal_deny, no upstream dial
//   - guarded denies CGNAT (100.64.0.1) → egress_internal_deny, no upstream dial
//   - guarded ALLOWS a public addr (93.184.216.34) → dial proceeds, no deny
//   - guarded + allowlisted internal (10.0.0.5 on the allowlist) → allowed
func TestGuardedTCP(t *testing.T) {
	t.Run("guarded denies IMDS no dial", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy(nil)
		log := &BufferLogger{}
		h := &Handler{
			Mode:         "mitm",
			Policy:       pol,
			Logger:       log,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("169.254.169.254:80"), nil },
			Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
			SniffTimeout: 300 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go client.Write([]byte("GET / HTTP/1.1\r\nHost: 169.254.169.254\r\n\r\n"))
		<-done
		client.Close()
		if dialed {
			t.Fatal("upstream dialed for IMDS under guarded (must fail closed)")
		}
		assertEvent(t, log, "egress_internal_deny")
	})

	t.Run("guarded denies RFC1918 no dial", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy(nil)
		log := &BufferLogger{}
		h := &Handler{
			Mode:         "mitm",
			Policy:       pol,
			Logger:       log,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.0.0.5:443"), nil },
			Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
			SniffTimeout: 300 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go client.Write([]byte("GET / HTTP/1.1\r\nHost: internal.example\r\n\r\n"))
		<-done
		client.Close()
		if dialed {
			t.Fatal("upstream dialed for RFC1918 under guarded (must fail closed)")
		}
		assertEvent(t, log, "egress_internal_deny")
	})

	t.Run("guarded denies CGNAT no dial", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy(nil)
		log := &BufferLogger{}
		h := &Handler{
			Mode:         "mitm",
			Policy:       pol,
			Logger:       log,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("100.64.0.1:443"), nil },
			Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
			SniffTimeout: 300 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go client.Write([]byte("RAWPING\n"))
		<-done
		client.Close()
		if dialed {
			t.Fatal("upstream dialed for CGNAT under guarded (must fail closed)")
		}
		assertEvent(t, log, "egress_internal_deny")
	})

	t.Run("guarded allows public address", func(t *testing.T) {
		up, _ := net.Listen("tcp", "127.0.0.1:0")
		defer up.Close()
		go func() {
			c, err := up.Accept()
			if err != nil {
				return
			}
			io.Copy(c, c) // echo
			c.Close()
		}()
		upAddr := netip.MustParseAddrPort(up.Addr().String())

		pol, _ := NewPolicy([]string{"93.184.216.34"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:   "mitm",
			Policy: pol,
			Logger: log,
			// OrigDst returns a public address (93.184.216.34) but we Dial
			// the local echo server so the test does not need outbound internet.
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("93.184.216.34:80"), nil },
			Dial:         func(network, addr string) (net.Conn, error) { return net.Dial(network, upAddr.String()) },
			SniffTimeout: 300 * time.Millisecond,
		}

		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go func() {
			client.Write([]byte("GET / HTTP/1.1\r\nHost: 93.184.216.34\r\n\r\n"))
		}()
		br := bufio.NewReader(client)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || line != "GET / HTTP/1.1\r\n" {
			t.Fatalf("echo = %q err=%v (public address must be forwarded in guarded mode)", line, err)
		}
		client.Close()
		<-done
		// Must be allowed, not denied.
		for _, e := range log.Snapshot() {
			if e["event"] == "egress_internal_deny" || e["event"] == "egress_deny" {
				t.Fatalf("public address denied in guarded mode: %+v", e)
			}
		}
		assertEvent(t, log, "egress_allow")
	})

	t.Run("guarded allows allowlisted internal address", func(t *testing.T) {
		up, _ := net.Listen("tcp", "127.0.0.1:0")
		defer up.Close()
		go func() {
			c, err := up.Accept()
			if err != nil {
				return
			}
			io.Copy(c, c)
			c.Close()
		}()
		upAddr := netip.MustParseAddrPort(up.Addr().String())

		// 10.0.0.5 is explicitly allowlisted — guarded must honor the exception.
		pol, _ := NewPolicy([]string{"10.0.0.5"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:         "mitm",
			Policy:       pol,
			Logger:       log,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.0.0.5:80"), nil },
			Dial:         func(network, addr string) (net.Conn, error) { return net.Dial(network, upAddr.String()) },
			SniffTimeout: 300 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go func() {
			client.Write([]byte("GET / HTTP/1.1\r\nHost: 10.0.0.5\r\n\r\n"))
		}()
		br := bufio.NewReader(client)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || line != "GET / HTTP/1.1\r\n" {
			t.Fatalf("echo = %q err=%v (allowlisted internal must be forwarded in guarded mode)", line, err)
		}
		client.Close()
		<-done
		for _, e := range log.Snapshot() {
			if e["event"] == "egress_internal_deny" || e["event"] == "egress_deny" {
				t.Fatalf("allowlisted internal address denied in guarded mode: %+v", e)
			}
		}
		assertEvent(t, log, "egress_allow")
	})
}

func TestIsInsideAddr(t *testing.T) {
	inside := []string{
		"169.254.169.254", "::ffff:169.254.169.254", // IMDS + IPv4-mapped IMDS
		"169.254.170.2", "10.0.0.1", "172.16.5.5", "192.168.1.1",
		"fc00::1", "fe80::1", "127.0.0.1", "::1",
		"100.64.0.1", "100.127.255.255", "0.0.0.0",
	}
	outside := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "100.63.255.255", "2606:4700:4700::1111"}
	for _, s := range inside {
		if !isInsideAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be inside", s)
		}
	}
	for _, s := range outside {
		if isInsideAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be outside", s)
		}
	}
}
