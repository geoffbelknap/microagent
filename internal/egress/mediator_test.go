package egress

import (
	"bufio"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

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

// TestMediatedAllowsUnlisted proves the Mode field's two behaviors against a
// host that is NOT on the allowlist:
//   - Mode "mediated": the connection is forwarded (L4 splice roundtrips) and
//     logged egress_allow with unlisted=true.
//   - Mode "strict": the same host is denied (egress_deny, no upstream dial).
func TestMediatedAllowsUnlisted(t *testing.T) {
	t.Run("mediated forwards unlisted", func(t *testing.T) {
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
			Mode:         "mediated",
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
			client.Write([]byte("GET / HTTP/1.1\r\nHost: unlisted.example.com\r\n\r\n"))
		}()
		br := bufio.NewReader(client)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || line != "GET / HTTP/1.1\r\n" {
			t.Fatalf("echo = %q err=%v (unlisted host must be forwarded in mediated mode)", line, err)
		}
		client.Close()
		<-done
		assertEventWithField(t, log, "egress_allow", "unlisted", true)
	})

	t.Run("strict denies unlisted", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy([]string{"allowed.example.com"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:         "strict",
			Policy:       pol,
			Logger:       log,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.0.0.9:443"), nil },
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
			t.Fatal("upstream dialed despite strict deny (must fail closed)")
		}
		assertEvent(t, log, "egress_deny")
	})
}

// TestMediatedPassthroughNotUnlisted proves that a passthrough host in mediated
// mode is L4-spliced (forwarded, not MITM'd) and its egress_allow audit record
// does NOT carry unlisted: a passthrough host is explicitly listed, and strict
// would allow it too, so it is not an "unlisted" grant. This guards the
// `unlisted := allowed && !d.Allow && !passthrough` refinement (INV4). The host
// is intentionally NOT on the main allowlist (Policy), so without the
// `&& !passthrough` term it would wrongly be flagged unlisted in mediated mode.
func TestMediatedPassthroughNotUnlisted(t *testing.T) {
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
		Mode:         "mediated",
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
// Without the guard, in mediated mode this would be forwarded to the listener and
// spin into an unbounded self-loop. Guards against the observed TPROXY self-loop.
func TestHandlerLoopGuardDropsOwnBindAddr(t *testing.T) {
	bind := netip.MustParseAddrPort("10.43.7.1:43517")
	dialed := false
	pol, _ := NewPolicy(nil)
	log := &BufferLogger{}
	h := &Handler{
		Mode:     "mediated", // mediated allows everything: only the guard can stop the self-loop
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
	for _, e := range log.Events {
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
			Mode:         "strict",
			Policy:       pol,
			Logger:       log,
			NameCache:    nc,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
			Dial:         net.Dial,
			SniffTimeout: 300 * time.Millisecond,
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
		// Matched by hostname, not mediated mode: not "unlisted".
		assertEventFieldAbsent(t, log, "egress_allow", "unlisted")
	})

	t.Run("uncached IP denied", func(t *testing.T) {
		dialed := false
		uncached := netip.MustParseAddrPort("198.51.100.9:443")
		log := &BufferLogger{}
		nc := NewNameCache()
		nc.Put("allowed.example.com", upAddr.Addr(), time.Minute)
		h := &Handler{
			Mode:         "strict",
			Policy:       pol,
			Logger:       log,
			NameCache:    nc,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return uncached, nil },
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
		Mode:         "strict",
		Policy:       pol,
		Peers:        peers,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:         net.Dial,
		SniffTimeout: 300 * time.Millisecond,
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
		Mode:         "strict",
		Policy:       pol,
		Peers:        peers,
		Logger:       log,
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
		t.Fatal("upstream dialed for an unknown peer under strict (must fail closed)")
	}
	assertEvent(t, log, "egress_deny")
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
		Mode:         "strict",
		Policy:       pol,
		Peers:        peers,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:         net.Dial,
		SniffTimeout: 300 * time.Millisecond,
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

func assertEvent(t *testing.T, log *BufferLogger, event string) {
	t.Helper()
	for _, e := range log.Events {
		if e["event"] == event {
			return
		}
	}
	t.Fatalf("event %q not logged; got %+v", event, log.Events)
}

// assertEventFieldAbsent fails if any logged event of the given name carries the
// named field at all (regardless of value).
func assertEventFieldAbsent(t *testing.T, log *BufferLogger, event string, field string) {
	t.Helper()
	for _, e := range log.Events {
		if e["event"] != event {
			continue
		}
		if _, present := e[field]; present {
			t.Fatalf("event %q unexpectedly carries field %q: %+v", event, field, e)
		}
	}
}
