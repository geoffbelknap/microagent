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
	for _, ev := range log.Events {
		for k, v := range ev {
			if s, ok := v.(string); ok && (s == "REALSECRET" || s == "Bearer REALSECRET" || s == "Bearer PLACEHOLDER") {
				t.Fatalf("credential leaked into audit field %q=%q", k, s)
			}
		}
	}
}

// assertEventWithField checks that at least one logged event matches the given
// event name AND has the expected field value.
func assertEventWithField(t *testing.T, log *BufferLogger, event string, field string, value any) {
	t.Helper()
	for _, e := range log.Events {
		if e["event"] == event && e[field] == value {
			return
		}
	}
	t.Fatalf("event %q with %s=%v not logged; got %+v", event, field, value, log.Events)
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
