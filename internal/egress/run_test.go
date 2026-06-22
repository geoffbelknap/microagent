package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// plainUDPListen is a UDPListen seam for tests that need Serve to start (which
// now always serves UDP) without TPROXY caps: it returns a plain UDP socket
// bound to the same addr, with no IP_TRANSPARENT/IP_RECVORIGDSTADDR. The UDP
// datagram path itself is exercised by udp_test.go and the live e2e.
func plainUDPListen(t *testing.T) func(netip.AddrPort) (*net.UDPConn, error) {
	t.Helper()
	return func(addr netip.AddrPort) (*net.UDPConn, error) {
		return net.ListenUDP("udp4", net.UDPAddrFromAddrPort(addr))
	}
}

func TestRunMediatesAcceptedConn(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err == nil {
			io.Copy(c, c)
			c.Close()
		}
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Allow:     []string{"api.github.com"},
			Logger:    &BufferLogger{},
			OrigDst:   func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
			Ready:     &strings.Builder{},
			UDPListen: plainUDPListen(t),
		})
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: api.github.com\r\n\r\n"))
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "GET / HTTP/1.1\r\n" {
		t.Fatalf("echo = %q err=%v", line, err)
	}
	conn.Close()
	cancel()
	<-done
}

// TestServePoliciesPeerRoster proves Serve plumbs Options.Peers into the
// Handler: a raw-TCP connection whose origdst maps to an allowlisted peer name is
// forwarded (egress_allow carrying peer), confirming the static roster police
// east-west by name under default-deny.
func TestServePoliciesPeerRoster(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err == nil {
			io.Copy(c, c)
			c.Close()
		}
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	log := &BufferLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Mode:      "strict",
			Allow:     []string{"builder"},
			Peers:     []string{"builder=" + upAddr.Addr().String()},
			Logger:    log,
			OrigDst:   func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
			Ready:     &strings.Builder{},
			UDPListen: plainUDPListen(t),
		})
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	conn.Write([]byte("RAWPING\n"))
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "RAWPING\n" {
		t.Fatalf("echo = %q err=%v (allowlisted peer must be forwarded)", line, err)
	}
	conn.Close()
	ev := waitForEvent(t, log, "egress_allow", 2*time.Second)
	if ev["peer"] != "builder" {
		t.Fatalf("egress_allow peer = %v, want builder", ev["peer"])
	}
	cancel()
	<-done
}

// TestServeBadPeerRosterFailsClosed proves a malformed peer roster aborts startup
// (fail-closed): Serve returns an error and does not leak the TCP listener.
func TestServeBadPeerRosterFailsClosed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	err := Serve(context.Background(), ln, Options{
		Allow:     []string{"x.com"},
		Peers:     []string{"builder"}, // missing '=' -> NewPeerCache rejects
		Logger:    &BufferLogger{},
		OrigDst:   func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
		UDPListen: plainUDPListen(t),
	})
	if err == nil {
		t.Fatal("expected Serve to fail on malformed peer roster")
	}
	// TCP listener must not be leaked.
	relisten, rerr := net.Listen("tcp", addr)
	if rerr != nil {
		t.Fatalf("TCP listener leaked (addr %s still bound): %v", addr, rerr)
	}
	_ = relisten.Close()
}

func TestServeLoadsCA(t *testing.T) {
	ca, err := NewCA("test-ca", time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")
	keyPEM, _ := ca.KeyPEM()
	if err := os.WriteFile(certPath, ca.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Allow: []string{"api.github.com"}, Passthrough: []string{"raw.example.com"},
			CACertPath: certPath, CAKeyPath: keyPath,
			Logger:    &BufferLogger{},
			OrigDst:   func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
			UDPListen: plainUDPListen(t),
		})
	}()
	// connect once so we know it is serving, then shut down
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

// TestServeLoadsSwapConfig verifies the Phase-1 wiring: Serve reads the
// SwapConfigPath file, loads it into a SwapTable, and starts serving with no
// error. No injection happens this phase — this only proves the config path is
// plumbed through and a valid file does not break startup.
func TestServeLoadsSwapConfig(t *testing.T) {
	dir := t.TempDir()
	swapPath := filepath.Join(dir, "swaps.yaml")
	swapYAML := `swaps:
  example:
    type: static
    domains: ["api.example.com"]
    header: Authorization
    format: "Bearer {key}"
    key_ref: "env:EXAMPLE_KEY"
`
	if err := os.WriteFile(swapPath, []byte(swapYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Allow:          []string{"api.example.com"},
			SwapConfigPath: swapPath,
			Logger:         &BufferLogger{},
			OrigDst:        func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
			Ready:          &strings.Builder{},
			UDPListen:      plainUDPListen(t),
		})
	}()
	// Connect once so we know it is serving, then shut down.
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

func TestServeBadSwapConfigFailsClosed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	err := Serve(context.Background(), ln, Options{
		Allow:          []string{"x.com"},
		SwapConfigPath: "/nonexistent/swaps.yaml",
		Logger:         &BufferLogger{},
		OrigDst:        func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
		UDPListen:      plainUDPListen(t),
	})
	if err == nil {
		t.Fatal("expected Serve to fail on missing swap config file")
	}
}

func TestServeBadCAPathFailsClosed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	err := Serve(context.Background(), ln, Options{
		Allow: []string{"x.com"}, CACertPath: "/nonexistent/ca.pem", CAKeyPath: "/nonexistent/key.pem",
		Logger:  &BufferLogger{},
		OrigDst: func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
	})
	if err == nil {
		t.Fatal("expected Serve to fail on missing CA files")
	}
}

// waitForEvent polls the (mutex-guarded) BufferLogger for an event of the given
// name until it appears or the deadline elapses. It locks log.mu so it is safe
// to call while a serveUDP goroutine is concurrently logging (avoids -race).
func waitForEvent(t *testing.T, log *BufferLogger, event string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		for _, e := range log.Snapshot() {
			if e["event"] == event {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("event %q not logged within %v", event, within)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestServeServesUDP verifies the WIRING that Task 3.3a adds: Serve opens a UDP
// socket (via the injected UDPListen seam), starts serveUDP on it, logs
// egress_udp_listen with the bound addr, and tears the socket down on shutdown.
//
// It is a wiring test, not a full datagram round-trip: a plain (non-TPROXY)
// socket never carries the IP_ORIGDSTADDR cmsg serveUDP needs to recover the
// original destination, so a datagram sent directly to it cannot exercise the
// allow/forward/reply path here. We instead prove serveUDP's receive loop is
// alive by sending a datagram and observing the egress_udp_origdst_error it logs
// (the loop read the datagram, tried to recover origdst, and failed for lack of
// the cmsg). The full forward+reply path is covered by udp_test.go (Task 3.2)
// and the live e2e (Task 5).
func TestServeServesUDP(t *testing.T) {
	// Injected listener: a plain UDP socket (no IP_TRANSPARENT, no caps needed).
	udpc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	udpAddr := udpc.LocalAddr().String()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	log := &BufferLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Mode:    "mediated", // bare-IP host allowed (not that we reach allow here)
			Logger:  log,
			OrigDst: func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
			UDPListen: func(_ netip.AddrPort) (*net.UDPConn, error) {
				return udpc, nil
			},
		})
	}()

	// Serve opened + announced the UDP socket.
	listenEv := waitForEvent(t, log, "egress_udp_listen", 2*time.Second)
	if got := listenEv["addr"]; got != udpAddr {
		t.Fatalf("egress_udp_listen addr = %v, want %v", got, udpAddr)
	}

	// serveUDP's receive loop is alive: a datagram sent to the socket is read and
	// (lacking the origdst cmsg on a plain socket) logged as an origdst error.
	send, err := net.Dial("udp4", udpAddr)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	if _, err := send.Write([]byte("probe")); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	_ = send.Close()
	waitForEvent(t, log, "egress_udp_origdst_error", 2*time.Second)

	// Shutdown tears the UDP socket down: after cancel, Serve returns and the
	// socket is closed (a further read on it fails).
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	udpc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 8)
	if _, _, rerr := udpc.ReadFromUDP(buf); !errors.Is(rerr, net.ErrClosed) {
		t.Fatalf("expected udp socket closed after shutdown, got read err: %v", rerr)
	}
}

// TestServeFailsClosedOnUDPListenError verifies fail-closed startup: if the
// transparent UDP socket cannot be opened, Serve returns the error (no TCP-only
// fallback) and does not leak the TCP listener.
func TestServeFailsClosedOnUDPListenError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	wantErr := errors.New("no TPROXY capability")
	log := &BufferLogger{}
	err := Serve(context.Background(), ln, Options{
		Mode:    "mediated",
		Logger:  log,
		OrigDst: func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
		UDPListen: func(_ netip.AddrPort) (*net.UDPConn, error) {
			return nil, wantErr
		},
	})
	if err == nil {
		t.Fatal("expected Serve to fail closed on UDP listen error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Serve error = %v, want wrapped %v", err, wantErr)
	}
	assertEvent(t, log, "egress_udp_listen_error")
	// TCP listener must not be leaked: it is closed, so re-binding its addr works.
	relisten, rerr := net.Listen("tcp", addr)
	if rerr != nil {
		t.Fatalf("TCP listener leaked (addr %s still bound): %v", addr, rerr)
	}
	_ = relisten.Close()
}

// TestServeFailsClosedWhenCapsExhausted is the bounded-operations contract test
// (ASK tenet 8): with MaxTotalBytes:1, the first byte-exceeding flow is dropped
// (torn down + audited egress_cap_exceeded reason volume) and the mediator KEEPS
// serving — the cap degrades the breaching flow, it does not crash the mediator,
// and no new bytes escape once exceeded.
//
// Recorded decision: caps are per-mediator-process (= per-workspace, since each
// mediated workspace runs its own mediator) and reset on restart. Persistent
// cross-restart volume accounting is intentionally out of scope.
func TestServeFailsClosedWhenCapsExhausted(t *testing.T) {
	// Upstream sink that drains whatever it receives.
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

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	log := &BufferLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Mode:      "mediated",
			Logger:    log,
			OrigDst:   func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
			Ready:     &strings.Builder{},
			UDPListen: plainUDPListen(t),
			Limits:    Limits{MaxTotalBytes: 1}, // any forwarded byte exceeds the cap
		})
	}()

	// First flow: push more than 1 byte; the splice must tear down (cap volume).
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	go func() {
		buf := make([]byte, 2048)
		for i := 0; i < 8; i++ {
			if _, werr := conn.Write(buf); werr != nil {
				return
			}
		}
	}()
	waitForEvent(t, log, "egress_cap_exceeded", 3*time.Second)
	_ = conn.Close()

	// Mediator KEEPS serving: a fresh connection is still accepted (it will itself
	// be capped, but the listener is alive — proving no crash).
	conn2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("mediator stopped serving after cap exhaustion (second dial failed): %v", err)
	}
	_ = conn2.Close()

	// Confirm the cap event carried reason volume.
	ev := waitForEvent(t, log, "egress_cap_exceeded", 2*time.Second)
	if ev["reason"] != "volume" {
		t.Fatalf("egress_cap_exceeded reason = %v, want volume", ev["reason"])
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

func TestServeRejectsHalfSetCA(t *testing.T) {
	// Only CACertPath set, no CAKeyPath — must fail closed.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	err := Serve(context.Background(), ln, Options{
		Allow:      []string{"x.com"},
		CACertPath: "/some/ca.pem",
		// CAKeyPath deliberately omitted
		Logger:  &BufferLogger{},
		OrigDst: func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
	})
	if err == nil {
		t.Fatal("expected Serve to fail when only CACertPath is set")
	}
	if !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
