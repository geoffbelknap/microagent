package egress

import (
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

// realSecret is the live credential the host side injects. It must never appear
// in any byte the guest can read, nor in any audited field. The guest only ever
// holds the placeholder.
const realSecret = "REAL-SUPER-SECRET-TOKEN"

// placeholderCred is the dummy credential the guest carries. The mediator swaps
// it for realSecret host-side; it must therefore never reach upstream as the
// effective Authorization value either.
const placeholderCred = "Bearer PLACEHOLDER-DO-NOT-USE"

// mediatorWithSwap is the wired in-process mediator for the e2e: a real upstream
// TLS server, a CA-trusting guest TLS config, and a running listener whose
// Handler performs the SNI-scoped credential swap.
type mediatorWithSwap struct {
	guestTLSConfig *tls.Config // trusts only the workspace CA
	mediatorAddr   string      // dial target for the guest
	gotAuth        <-chan string
	log            *BufferLogger
	swapName       string
	handleDone     <-chan struct{}
	closeUpstream  func()
	closeListener  func()
}

// startMediatorWithSwap stands up the full credential-swap data path in-process,
// reusing the exact MITM harness wiring from mitm_test.go: a real upstream TLS
// server, a per-workspace CA, an OrigDst override that points the mediator at the
// upstream regardless of the dialed host name, and a guest TLS config trusting
// only the CA. swapDomain is both the swap entry's domain and the SNI the guest
// dials, so the SNI-scoped injection path fires.
func startMediatorWithSwap(t *testing.T, swapDomain string) *mediatorWithSwap {
	t.Helper()

	// Upstream records the Authorization header it actually received and replies
	// "ok". The channel is buffered so the handler never blocks.
	gotAuth := make(chan string, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAuth <- r.Header.Get("Authorization"):
		default:
		}
		fmt.Fprint(w, "ok")
	}))
	upstreamRoots := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	upstreamAddrPort := netip.MustParseAddrPort(upstream.Listener.Addr().String())

	testCA, err := NewCA("test-workspace-ca", 24*time.Hour)
	if err != nil {
		upstream.Close()
		t.Fatalf("NewCA: %v", err)
	}
	pol, err := NewPolicy([]string{swapDomain})
	if err != nil {
		upstream.Close()
		t.Fatalf("NewPolicy: %v", err)
	}

	// A single static swap for swapDomain: render "Bearer {key}" with the real
	// secret resolved from env:E2E_KEY (a fake resolver, no real env read).
	tbl, err := LoadSwapTable([]byte(`swaps:
  e2e:
    type: static
    domains: ["` + swapDomain + `"]
    header: Authorization
    format: "Bearer {key}"
    key_ref: "env:E2E_KEY"
`))
	if err != nil {
		upstream.Close()
		t.Fatalf("LoadSwapTable: %v", err)
	}

	log := &BufferLogger{}
	h := &Handler{
		Policy:        pol,
		CA:            testCA,
		UpstreamRoots: upstreamRoots,
		Logger:        log,
		// OrigDst always returns the upstream addr (simulates the nft redirect),
		// so the dialed host name is decoupled from where the bytes actually go.
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upstreamAddrPort, nil },
		Dial:         net.Dial,
		SniffTimeout: 2 * time.Second,
		Swaps:        tbl,
		Resolver:     fakeResolver{"env:E2E_KEY": realSecret},
		tokenCache:   newTokenCache(),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		upstream.Close()
		t.Fatalf("listen: %v", err)
	}
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
		ln.Close()
		upstream.Close()
		t.Fatal("failed to append CA cert to pool")
	}

	return &mediatorWithSwap{
		// Guest trusts ONLY the workspace CA: a successful handshake proves the
		// mediator presented a CA-signed leaf for swapDomain, not the upstream cert.
		guestTLSConfig: &tls.Config{ServerName: swapDomain, RootCAs: caCertPool},
		mediatorAddr:   ln.Addr().String(),
		gotAuth:        gotAuth,
		log:            log,
		swapName:       "e2e",
		handleDone:     handleDone,
		closeUpstream:  upstream.Close,
		closeListener:  func() { ln.Close() },
	}
}

// TestE2E_CredentialSwap_SecretNeverCrossesBoundary is the marquee end-to-end
// proof of the credential-swap pipeline. A guest dials the mediator over TLS
// carrying only a placeholder Authorization header; the mediator terminates the
// guest TLS, swaps the placeholder for the real secret host-side, and re-
// originates to the upstream. It then asserts, on real wire bytes:
//
//  1. The upstream received Authorization: Bearer REAL-SUPER-SECRET-TOKEN.
//  2. The guest-visible response bytes never contain the real secret.
//  3. No audited field (any value of any event) contains the real secret, nor
//     does the placeholder leak through as the effective upstream credential.
//  4. An egress_swap event fired naming the swap entry (mediation proof, no
//     credential value carried).
func TestE2E_CredentialSwap_SecretNeverCrossesBoundary(t *testing.T) {
	const swapDomain = "api.example.com"
	m := startMediatorWithSwap(t, swapDomain)
	defer m.closeUpstream()
	defer m.closeListener()

	// --- Guest connects to the mediator over TLS and issues one request ------
	rawConn, err := net.DialTimeout("tcp", m.mediatorAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial mediator: %v", err)
	}
	defer rawConn.Close()

	clientTLS := tls.Client(rawConn, m.guestTLSConfig)
	if err := clientTLS.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client TLS handshake: %v", err)
	}

	// The guest carries ONLY the placeholder credential — it never sees the real
	// secret. Connection: close so the upstream handler runs and the response is
	// fully delivered before the stream ends.
	req := "GET / HTTP/1.1\r\nHost: " + swapDomain + "\r\nAuthorization: " + placeholderCred + "\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(clientTLS, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Capture every byte the guest can read off the wire.
	respBytes, err := io.ReadAll(clientTLS)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp := string(respBytes)
	if !strings.Contains(resp, "ok") {
		t.Fatalf("guest did not receive upstream body %q; got:\n%s", "ok", resp)
	}

	// Wait for the upstream to record the Authorization it received.
	var upstreamAuth string
	select {
	case upstreamAuth = <-m.gotAuth:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received the request")
	}

	clientTLS.Close()
	select {
	case <-m.handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after client close")
	}

	// --- Assertion 1: the swap happened host-side ----------------------------
	// The upstream got the REAL secret rendered into the header.
	if want := "Bearer " + realSecret; upstreamAuth != want {
		t.Fatalf("upstream Authorization = %q, want %q", upstreamAuth, want)
	}

	// --- Assertion 3 (placeholder half): the placeholder never reached upstream
	// as the effective credential. The mediator must have replaced it entirely.
	if strings.Contains(upstreamAuth, "PLACEHOLDER") {
		t.Fatalf("placeholder leaked to upstream as the credential: %q", upstreamAuth)
	}

	// --- Assertion 2: the secret is in no guest-visible byte -----------------
	if strings.Contains(resp, realSecret) {
		t.Fatalf("real secret leaked into guest-visible response bytes:\n%s", resp)
	}
	// The guest also must never have received a Bearer-rendered form of it.
	if strings.Contains(resp, "Bearer "+realSecret) {
		t.Fatalf("rendered secret leaked into guest-visible response bytes:\n%s", resp)
	}

	// --- Assertion 3: no audited field carries the secret (nor the rendered
	// form, nor the placeholder masquerading as a real value) ----------------
	leakNeedles := []string{
		realSecret,
		"Bearer " + realSecret,
		placeholderCred,
		"PLACEHOLDER-DO-NOT-USE",
	}
	for _, ev := range m.log.Events {
		for k, v := range ev {
			s, ok := v.(string)
			if !ok {
				continue
			}
			for _, needle := range leakNeedles {
				if strings.Contains(s, needle) {
					t.Fatalf("credential material leaked into audit field %q=%q (needle %q)", k, s, needle)
				}
			}
		}
	}

	// --- Assertion 4: egress_swap fired naming the entry (no value carried) ---
	foundSwap := false
	for _, ev := range m.log.Events {
		if ev["event"] != "egress_swap" {
			continue
		}
		foundSwap = true
		if ev["swap"] != m.swapName {
			t.Fatalf("egress_swap swap = %v, want %q", ev["swap"], m.swapName)
		}
		if ev["host"] != swapDomain {
			t.Fatalf("egress_swap host = %v, want %q", ev["host"], swapDomain)
		}
		if ev["type"] != "static" {
			t.Fatalf("egress_swap type = %v, want %q", ev["type"], "static")
		}
	}
	if !foundSwap {
		t.Fatalf("no egress_swap audit event; got %+v", m.log.Events)
	}
}
