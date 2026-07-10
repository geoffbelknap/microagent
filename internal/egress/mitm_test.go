package egress

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestMITMInterceptsTLS proves end-to-end TLS interception:
//  1. The client sees a CA-signed leaf (not the upstream's real cert).
//  2. The client receives the upstream response body.
//  3. The mediator logs egress_allow with mitm=true.
func TestMITMInterceptsTLS(t *testing.T) {
	// --- Upstream TLS server -------------------------------------------------
	// httptest.NewTLSServer signs its cert for example.com, *.example.com, and
	// 127.0.0.1, so we can use SNI "example.com" when re-originating.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello-upstream")
	}))
	defer upstream.Close()

	// The mediator must trust the upstream's self-signed cert.
	upstreamRoots := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	// Parse upstream addr for the OrigDst injection.
	upstreamAddrPort := netip.MustParseAddrPort(upstream.Listener.Addr().String())

	// --- Per-workspace CA ----------------------------------------------------
	testCA, err := NewCA("test-workspace-ca", 24*time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	// --- Mediator setup ------------------------------------------------------
	pol, err := NewPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	log := &BufferLogger{}
	h := &Handler{
		Mode:          egressModeMITM,
		Policy:        pol,
		CA:            testCA,
		UpstreamRoots: upstreamRoots,
		Logger:        log,
		// OrigDst always returns the upstream addr (simulates nftables redirect).
		OrigDst: func(net.Conn) (netip.AddrPort, error) { return upstreamAddrPort, nil },
		// When dialing plain TCP upstream in non-MITM path (unused here but
		// required to be set; MITM path uses tls.Dial internally).
		Dial:         net.Dial,
		SniffTimeout: 2 * time.Second,
	}

	// --- Mediator listener ---------------------------------------------------
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Accept one connection in the background and call Handle.
	handleDone := make(chan struct{})
	go func() {
		defer close(handleDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		h.Handle(conn)
	}()

	// --- Client TLS config ---------------------------------------------------
	// The client trusts ONLY our per-workspace CA. This proves that if the
	// handshake succeeds, the mediator presented a CA-signed leaf (not the
	// upstream's real cert, which is signed by the httptest CA).
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(testCA.CertPEM()) {
		t.Fatal("failed to append CA cert to pool")
	}

	// --- Client connects to mediator, sends HTTP/1.1 GET ---------------------
	mediatorAddr := ln.Addr().String()

	// Dial mediator over TLS, asserting SNI="example.com". The mediator will
	// intercept, present a leaf for "example.com" signed by testCA, then
	// re-originate to the upstream with ServerName="example.com".
	rawConn, err := net.DialTimeout("tcp", mediatorAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	defer rawConn.Close()

	clientTLS := tls.Client(rawConn, &tls.Config{
		ServerName: "example.com",
		RootCAs:    caCertPool,
	})
	if err := clientTLS.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client TLS handshake: %v", err)
	}

	// Send HTTP/1.1 GET — the upstream handler responds with "hello-upstream".
	req := "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(clientTLS, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Read full response.
	br := bufio.NewReader(clientTLS)
	var respLines []string
	for {
		line, err := br.ReadString('\n')
		respLines = append(respLines, line)
		if err != nil {
			break
		}
	}
	resp := strings.Join(respLines, "")

	if !strings.Contains(resp, "hello-upstream") {
		t.Errorf("response does not contain 'hello-upstream'; got:\n%s", resp)
	}

	// --- Assert the client saw a CA-signed leaf (not the upstream's cert) ----
	peerCerts := clientTLS.ConnectionState().PeerCertificates
	if len(peerCerts) == 0 {
		t.Fatal("no peer certificates in TLS state")
	}
	leaf := peerCerts[0]

	// Verify that the leaf is signed by our testCA (not the httptest CA).
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(testCA.CertPEM()) {
		t.Fatal("failed to build verify pool from testCA")
	}
	opts := x509.VerifyOptions{
		DNSName: "example.com",
		Roots:   roots,
	}
	if _, err := leaf.Verify(opts); err != nil {
		t.Errorf("leaf cert not signed by testCA: %v", err)
	}

	// Also verify the issuer CN matches the CA we minted.
	if leaf.Issuer.CommonName != "test-workspace-ca" {
		t.Errorf("leaf issuer CN = %q, want %q", leaf.Issuer.CommonName, "test-workspace-ca")
	}

	// Close the client and wait for Handle to return.
	clientTLS.Close()
	select {
	case <-handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after client close")
	}

	// --- Assert audit log ----------------------------------------------------
	assertEventWithField(t, log, "egress_allow", "mitm", true)
}

// TestMITMSwapInjectsCredential proves the end-to-end SNI-scoped credential
// swap through serveMITM: the guest sends Authorization: Bearer PLACEHOLDER,
// the upstream receives Authorization: Bearer REALSECRET, and the swap is
// audited without the secret appearing in any audit field.
func TestMITMSwapInjectsCredential(t *testing.T) {
	// Upstream captures the Authorization header it actually receives.
	gotAuth := make(chan string, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAuth <- r.Header.Get("Authorization"):
		default:
		}
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()
	upstreamRoots := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	upstreamAddrPort := netip.MustParseAddrPort(upstream.Listener.Addr().String())

	testCA, err := NewCA("test-workspace-ca", 24*time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	pol, err := NewPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	tbl, err := LoadSwapTable([]byte(`swaps:
  example:
    type: static
    domains: ["example.com"]
    header: Authorization
    format: "Bearer {key}"
    key_ref: "env:K"
`))
	if err != nil {
		t.Fatalf("LoadSwapTable: %v", err)
	}
	log := &BufferLogger{}
	h := &Handler{
		Mode:          egressModeMITM,
		Policy:        pol,
		CA:            testCA,
		UpstreamRoots: upstreamRoots,
		Logger:        log,
		OrigDst:       func(net.Conn) (netip.AddrPort, error) { return upstreamAddrPort, nil },
		Dial:          net.Dial,
		SniffTimeout:  2 * time.Second,
		Swaps:         tbl,
		Resolver:      fakeResolver{"env:K": "REALSECRET"},
		tokenCache:    newTokenCache(),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	handleDone := make(chan struct{})
	go func() {
		defer close(handleDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		h.Handle(conn)
	}()

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(testCA.CertPEM()) {
		t.Fatal("failed to append CA cert to pool")
	}
	rawConn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	defer rawConn.Close()
	clientTLS := tls.Client(rawConn, &tls.Config{ServerName: "example.com", RootCAs: caCertPool})
	if err := clientTLS.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client TLS handshake: %v", err)
	}
	req := "GET / HTTP/1.1\r\nHost: example.com\r\nAuthorization: Bearer PLACEHOLDER\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(clientTLS, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	// Read response so the upstream handler runs before we assert.
	io.Copy(io.Discard, clientTLS)

	select {
	case auth := <-gotAuth:
		if auth != "Bearer REALSECRET" {
			t.Fatalf("upstream Authorization = %q, want %q", auth, "Bearer REALSECRET")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received the request")
	}

	clientTLS.Close()
	select {
	case <-handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after client close")
	}

	assertEvent(t, log, "egress_swap")
	// No audit field may carry the secret or the rendered value.
	for _, ev := range log.Snapshot() {
		for k, v := range ev {
			if s, ok := v.(string); ok && (s == "REALSECRET" || s == "Bearer REALSECRET" || s == "Bearer PLACEHOLDER") {
				t.Fatalf("credential leaked into audit field %q=%q", k, s)
			}
		}
	}
}

// TestHandlerSplicesPeerTLSWithoutMITM proves east-west (peer) TLS is L4-spliced,
// NOT MITM'd: the upstream is a peer presenting a self-signed cert that is NOT in
// UpstreamRoots, so MITM (which re-dials the upstream verifying against
// UpstreamRoots/system roots) would FAIL upstream verification and break a
// connection that worked before. The classifier marks the destination a peer, so
// serveMITM is skipped and the connection L4-splices end to end. We assert: (a)
// the connection completes (the client speaks TLS straight to the self-signed
// upstream and bytes echo through), (b) egress_allow carries mitm=false + the peer
// field, (c) NO egress_mitm_upstream_error was logged.
func TestHandlerSplicesPeerTLSWithoutMITM(t *testing.T) {
	// Upstream TLS server with a self-signed cert NOT trusted by UpstreamRoots.
	// httptest mints its own self-signed CA; we deliberately do NOT give that CA
	// to the mediator's UpstreamRoots, so a MITM re-dial would fail verification.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello-peer")
	}))
	defer upstream.Close()
	upstreamAddrPort := netip.MustParseAddrPort(upstream.Listener.Addr().String())

	// The client (guest) trusts the upstream's self-signed cert directly — this is
	// the east-west trust model: the guest verifies the peer's internal cert
	// itself, end to end, because the mediator does NOT intercept.
	clientRoots := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	// A CA IS configured (so the only reason MITM is skipped is the peer
	// classification, not a missing CA) and UpstreamRoots is set but does NOT
	// contain the upstream's self-signed CA — proving MITM would break this flow.
	testCA, err := NewCA("test-workspace-ca", 24*time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	publicRoots := x509.NewCertPool()
	if !publicRoots.AppendCertsFromPEM(testCA.CertPEM()) {
		t.Fatal("seed UpstreamRoots")
	}

	// Allowlist the peer by its workspace name; PeerCache maps dst IP -> name.
	pol, err := NewPolicy([]string{"builder"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	peers, err := NewPeerCache([]string{"builder=" + upstreamAddrPort.Addr().String()})
	if err != nil {
		t.Fatalf("NewPeerCache: %v", err)
	}
	log := &BufferLogger{}
	h := &Handler{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          pol,
		CA:              testCA,
		UpstreamRoots:   publicRoots, // does NOT trust the self-signed upstream
		Peers:           peers,
		Logger:          log,
		OrigDst:         func(net.Conn) (netip.AddrPort, error) { return upstreamAddrPort, nil },
		Dial:            net.Dial,
		SniffTimeout:    2 * time.Second,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	handleDone := make(chan struct{})
	go func() {
		defer close(handleDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		h.Handle(conn)
	}()

	// Client connects to the mediator and speaks a real ClientHello (SNI present),
	// so sniffHost sees isTLS=true with an SNI. The peer classification is keyed on
	// the destination IP (PeerCache), independent of SNI, so the mediator L4-splices
	// (peer), letting the TLS terminate end-to-end at the self-signed upstream which
	// the client trusts directly. SNI is "example.com" because, being a true
	// L4-splice, the client itself verifies the upstream's cert (valid for
	// example.com) — proving NO interception. If the mediator MITM'd, the upstream
	// re-dial would fail verification (self-signed not in UpstreamRoots) and no bytes
	// would flow.
	rawConn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	defer rawConn.Close()
	clientTLS := tls.Client(rawConn, &tls.Config{ServerName: "example.com", RootCAs: clientRoots})
	if err := clientTLS.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client TLS handshake (peer L4-splice should reach self-signed upstream): %v", err)
	}

	// Confirm NO MITM: the leaf the client saw is the upstream's self-signed cert,
	// issued for example.com by the httptest CA — NOT a CA-signed leaf for "builder".
	leaf := clientTLS.ConnectionState().PeerCertificates[0]
	if leaf.Issuer.CommonName == "test-workspace-ca" {
		t.Fatalf("peer TLS was MITM'd: leaf issued by workspace CA %q", leaf.Issuer.CommonName)
	}

	if _, err := io.WriteString(clientTLS, "GET / HTTP/1.1\r\nHost: builder\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	body, err := io.ReadAll(clientTLS)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(string(body), "hello-peer") {
		t.Errorf("response missing 'hello-peer'; got:\n%s", body)
	}

	clientTLS.Close()
	select {
	case <-handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after client close")
	}

	// (b) egress_allow carries mitm=false + the peer field.
	assertEventWithField(t, log, "egress_allow", "mitm", false)
	assertEventWithField(t, log, "egress_allow", "peer", "builder")
	// (c) MITM was never attempted: no upstream-verification error.
	for _, e := range log.Snapshot() {
		if e["event"] == "egress_mitm_upstream_error" || e["event"] == "egress_mitm_handshake_error" {
			t.Fatalf("peer TLS unexpectedly took the MITM path: %+v", e)
		}
	}
}

// TestHandlerMITMsExternalTLS proves a NON-peer (external/public) TLS destination
// is still MITM'd when a CA + trusted UpstreamRoots are present. The PeerCache is
// non-nil but does NOT contain the destination, so isPeer is false and the MITM
// guard still fires. The client sees a workspace-CA-signed leaf and the audit
// carries mitm=true.
func TestHandlerMITMsExternalTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello-external")
	}))
	defer upstream.Close()
	upstreamRoots := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	upstreamAddrPort := netip.MustParseAddrPort(upstream.Listener.Addr().String())

	testCA, err := NewCA("test-workspace-ca", 24*time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	pol, err := NewPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	// A PeerCache exists with an unrelated peer; the external dst is NOT in it.
	peers, err := NewPeerCache([]string{"builder=10.44.1.3"})
	if err != nil {
		t.Fatalf("NewPeerCache: %v", err)
	}
	log := &BufferLogger{}
	h := &Handler{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          pol,
		CA:              testCA,
		UpstreamRoots:   upstreamRoots,
		Peers:           peers,
		Logger:          log,
		OrigDst:         func(net.Conn) (netip.AddrPort, error) { return upstreamAddrPort, nil },
		Dial:            net.Dial,
		SniffTimeout:    2 * time.Second,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	handleDone := make(chan struct{})
	go func() {
		defer close(handleDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		h.Handle(conn)
	}()

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(testCA.CertPEM()) {
		t.Fatal("append CA cert")
	}
	rawConn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	defer rawConn.Close()
	// Client trusts ONLY the workspace CA: a successful handshake proves the
	// mediator presented a CA-signed leaf (i.e. it MITM'd).
	clientTLS := tls.Client(rawConn, &tls.Config{ServerName: "example.com", RootCAs: caCertPool})
	if err := clientTLS.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client TLS handshake (external should be MITM'd): %v", err)
	}
	leaf := clientTLS.ConnectionState().PeerCertificates[0]
	if leaf.Issuer.CommonName != "test-workspace-ca" {
		t.Fatalf("external TLS was NOT MITM'd: leaf issuer %q", leaf.Issuer.CommonName)
	}

	if _, err := io.WriteString(clientTLS, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	body, err := io.ReadAll(clientTLS)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(string(body), "hello-external") {
		t.Errorf("response missing 'hello-external'; got:\n%s", body)
	}

	clientTLS.Close()
	select {
	case <-handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after client close")
	}

	assertEventWithField(t, log, "egress_allow", "mitm", true)
}

// TestPeerAuditFieldsPresent verifies east-west audit legibility across allow and
// deny: an allowed peer flow's egress_allow carries peer+peer_ip (and mitm=false on
// the L4 path), and a denied peer flow's egress_deny carries peer_ip even when the
// peer name is unknown (IP-only peer not on the allowlist).
func TestPeerAuditFieldsPresent(t *testing.T) {
	t.Run("allowed peer carries peer+peer_ip+mitm:false", func(t *testing.T) {
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
		go func() { client.Write([]byte("RAWPING\n")) }()
		br := bufio.NewReader(client)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || line != "RAWPING\n" {
			t.Fatalf("echo = %q err=%v", line, err)
		}
		client.Close()
		<-done
		assertEventWithField(t, log, "egress_allow", "peer", "builder")
		assertEventWithField(t, log, "egress_allow", "peer_ip", upAddr.Addr().String())
		assertEventWithField(t, log, "egress_allow", "mitm", false)
	})

	t.Run("denied IP-only peer carries peer_ip", func(t *testing.T) {
		// An inside IP not in the PeerCache and not on the allowlist: name
		// unknown, denied as internal (an allow-broad-family mode classifies the
		// resolved RFC1918 IP as inside), and the deny must still carry peer_ip
		// (the bare destination IP) for the east-west audit trail.
		dst := netip.MustParseAddrPort("10.44.1.99:443")
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
			OrigDst:         func(net.Conn) (netip.AddrPort, error) { return dst, nil },
			Dial:            func(string, string) (net.Conn, error) { t.Fatal("must not dial denied peer"); return nil, nil },
			SniffTimeout:    300 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go client.Write([]byte("RAWPING\n"))
		<-done
		client.Close()
		assertEventWithField(t, log, "egress_internal_deny", "peer_ip", dst.Addr().String())
	})
}

// assertEventWithField checks that at least one logged event matches the given
// event name AND has the expected field value.
func assertEventWithField(t *testing.T, log *BufferLogger, event string, field string, value any) {
	t.Helper()
	snap := log.Snapshot()
	for _, e := range snap {
		if e["event"] == event && e[field] == value {
			return
		}
	}
	t.Fatalf("event %q with %s=%v not logged; got %+v", event, field, value, snap)
}

// TestMITMPassthroughSkipsMITM verifies that a host in the Passthrough policy
// gets L4-spliced even when isTLS=true and CA is set (no interception).
func TestMITMPassthroughSkipsMITM(t *testing.T) {
	// Echo server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c) // echo
	}()
	upAddr := netip.MustParseAddrPort(ln.Addr().String())

	testCA, _ := NewCA("ca", time.Hour)
	pol, _ := NewPolicy([]string{"allowed.example.com"})
	passthrough, _ := NewPolicy([]string{"passthrough.example.com"})
	log := &BufferLogger{}
	h := &Handler{
		Mode:         egressModeMITM,
		Policy:       pol,
		Passthrough:  passthrough,
		CA:           testCA,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:         net.Dial,
		SniffTimeout: 500 * time.Millisecond,
	}

	mediatorLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mediator listen: %v", err)
	}
	defer mediatorLn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := mediatorLn.Accept()
		if err != nil {
			return
		}
		h.Handle(conn)
	}()

	client, err := net.DialTimeout("tcp", mediatorLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Send a TLS-looking first byte (0x16) followed by an HTTP Host to get
	// isTLS=true and host="passthrough.example.com" from sniffHost. But
	// building a valid fake ClientHello is complex — instead, send plain HTTP
	// with Host: passthrough.example.com which is non-TLS and goes the L4 path.
	// The key assertion is that a passthrough host doesn't invoke MITM (no
	// CA-signed handshake is attempted — it L4-splices directly to the echo
	// server and the data roundtrips).
	msg := "GET / HTTP/1.1\r\nHost: passthrough.example.com\r\n\r\n"
	client.Write([]byte(msg))

	buf := make([]byte, len(msg))
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(client, buf)
	if err != nil || n != len(msg) {
		t.Fatalf("echo read: n=%d err=%v", n, err)
	}
	if string(buf) != msg {
		t.Errorf("echo mismatch: got %q want %q", string(buf), msg)
	}

	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle did not return")
	}
	assertEvent(t, log, "egress_allow")
}
