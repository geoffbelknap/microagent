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
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
	return startMediatorWithSwapConfig(t, swapDomain, `swaps:
  e2e:
    type: static
    domains: ["`+swapDomain+`"]
    header: Authorization
    format: "Bearer {key}"
    key_ref: "env:E2E_KEY"
`, "Authorization", "e2e")
}

// startMediatorWithSwapConfig is the generalized harness: swapYAML is the swap
// config the mediator loads (its single entry must be named swapName and inject
// captureHeader), and captureHeader is the request header the upstream records
// so a test can assert what the mediator actually injected. This lets the same
// data path be driven by a hand-written Authorization/Bearer swap or by a
// provider-registry entry (e.g. anthropic's x-api-key/"{key}").
func startMediatorWithSwapConfig(t *testing.T, swapDomain, swapYAML, captureHeader, swapName string) *mediatorWithSwap {
	t.Helper()
	return startMediatorWithSwapConfigAndResolver(t, swapDomain, swapYAML, captureHeader, swapName, fakeResolver{"env:E2E_KEY": realSecret})
}

// startMediatorWithSwapConfigAndResolver is startMediatorWithSwapConfig with an
// injectable resolver, for entries (oauth2-cc, jwt-bearer) that need more than
// the single "env:E2E_KEY" ref the static-only default resolves.
func startMediatorWithSwapConfigAndResolver(t *testing.T, swapDomain, swapYAML, captureHeader, swapName string, res resolver) *mediatorWithSwap {
	t.Helper()

	// Upstream records the swapped header it actually received and replies "ok".
	// The channel is buffered so the handler never blocks.
	gotAuth := make(chan string, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAuth <- r.Header.Get(captureHeader):
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

	// The swap table comes from the caller's YAML; its key_ref resolves to the
	// real secret via a fake resolver (no real env read).
	tbl, err := LoadSwapTable([]byte(swapYAML))
	if err != nil {
		upstream.Close()
		t.Fatalf("LoadSwapTable: %v", err)
	}

	log := &BufferLogger{}
	h := &Handler{
		Mode:          egressModeMITM, // credential swap requires TLS interception (forging)
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
		Resolver:     res,
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
		swapName:       swapName,
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
	logSnap := m.log.Snapshot()
	for _, ev := range logSnap {
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
	for _, ev := range logSnap {
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
		t.Fatalf("no egress_swap audit event; got %+v", logSnap)
	}
}

// TestE2E_CredSwapProviderEntry_InjectsProviderHeader proves the `--cred-swap`
// surface end-to-end at the data-path level: an entry built by the provider
// registry (ProviderSwapEntry, the core of materializeCredSwapConfig) and
// marshaled exactly as the generated cred-swap.yaml is, loads into the mediator
// and injects the real credential into the provider's OWN header and format.
// anthropic is deliberately chosen because it uses x-api-key with a bare "{key}"
// format (not Authorization/Bearer) — so this also guards against the registry
// header/format ever silently regressing to the generic default.
func TestE2E_CredSwapProviderEntry_InjectsProviderHeader(t *testing.T) {
	// The upstream test cert covers api.example.com (see the marquee test), so the
	// data path uses that host while the entry's header/format come straight from
	// the anthropic registry definition — which is what this test guards.
	const swapDomain = "api.example.com"

	// Build the entry the way `--cred-swap anthropic=env:E2E_KEY` does, then
	// marshal it into a cred-swap.yaml exactly as materializeCredSwapConfig would.
	entry, hosts, err := ProviderSwapEntry("anthropic", "env:E2E_KEY")
	if err != nil {
		t.Fatalf("ProviderSwapEntry: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "api.anthropic.com" {
		t.Fatalf("provider hosts = %v, want [api.anthropic.com]", hosts)
	}
	if entry.Header != "x-api-key" || entry.Format != "{key}" {
		t.Fatalf("anthropic entry header/format = %q/%q, want x-api-key/{key}", entry.Header, entry.Format)
	}
	// Rebind only the destination to the cert-covered host; the header/format/
	// key_ref under test are unchanged.
	entry.Domains = []string{swapDomain}
	yamlBytes, err := yaml.Marshal(SwapConfigFile{Swaps: map[string]SwapEntry{"anthropic": entry}})
	if err != nil {
		t.Fatalf("marshal generated cred-swap.yaml: %v", err)
	}

	m := startMediatorWithSwapConfig(t, swapDomain, string(yamlBytes), "x-api-key", "anthropic")
	defer m.closeUpstream()
	defer m.closeListener()

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

	// The guest carries only a worthless placeholder in the provider header.
	const placeholderKey = "PLACEHOLDER-NOT-A-KEY"
	req := "GET / HTTP/1.1\r\nHost: " + swapDomain + "\r\nx-api-key: " + placeholderKey + "\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(clientTLS, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	respBytes, err := io.ReadAll(clientTLS)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp := string(respBytes)

	var upstreamKey string
	select {
	case upstreamKey = <-m.gotAuth:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received the request")
	}
	clientTLS.Close()
	select {
	case <-m.handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after client close")
	}

	// anthropic's format is bare "{key}", so the upstream must receive exactly the
	// real secret in x-api-key — never the placeholder, never a "Bearer " wrapper.
	if upstreamKey != realSecret {
		t.Fatalf("upstream x-api-key = %q, want the real secret %q (injected by the registry entry)", upstreamKey, realSecret)
	}
	if strings.Contains(upstreamKey, placeholderKey) {
		t.Fatalf("placeholder leaked upstream as the credential: %q", upstreamKey)
	}
	if strings.Contains(resp, realSecret) {
		t.Fatalf("real secret leaked into guest-visible response bytes:\n%s", resp)
	}
}

// startOAuth2TokenServer runs a hermetic OAuth2 client_credentials token
// endpoint for the duration of the test: it validates grant_type, client id,
// client secret, and scopes, then returns a signed-looking JSON access token.
// hits counts how many times the endpoint was actually called, so a test can
// assert a second acquire was served from cache instead of a second exchange.
func startOAuth2TokenServer(t *testing.T, wantClientID, wantClientSecret string, wantScopes []string, expiresIn int) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint: ParseForm: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.PostFormValue("grant_type"); got != "client_credentials" {
			t.Errorf("token endpoint: grant_type = %q, want client_credentials", got)
		}
		if got := r.PostFormValue("client_id"); got != wantClientID {
			t.Errorf("token endpoint: client_id = %q, want %q", got, wantClientID)
		}
		if got := r.PostFormValue("client_secret"); got != wantClientSecret {
			t.Errorf("token endpoint: client_secret = %q, want %q", got, wantClientSecret)
		}
		if got := r.PostFormValue("scope"); got != strings.Join(wantScopes, " ") {
			t.Errorf("token endpoint: scope = %q, want %q", got, strings.Join(wantScopes, " "))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"%s","expires_in":%d}`, mintedToken, expiresIn)
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

// mintedToken is the access token the fake token endpoint returns. Treated the
// same as realSecret: it must never reach the guest and never appear in an
// audited field.
const mintedToken = "MINTED-ACCESS-TOKEN"

// oauth2SwapYAML builds a single oauth2-cc swap entry for domain, pointed at
// tokenURL, resolving its client id/secret through the refs a matching
// fakeResolver carries.
func oauth2SwapYAML(domain, tokenURL string) string {
	return `swaps:
  oauth2-e2e:
    type: oauth2-cc
    domains: ["` + domain + `"]
    header: Authorization
    token_url: "` + tokenURL + `"
    client_id_ref: "env:E2E_CLIENT_ID"
    client_secret_ref: "env:E2E_CLIENT_SECRET"
    scopes: ["read", "write"]
`
}

const (
	oauth2ClientID     = "e2e-client-id"
	oauth2ClientSecret = "e2e-client-secret"
)

func oauth2Resolver() fakeResolver {
	return fakeResolver{
		"env:E2E_CLIENT_ID":     oauth2ClientID,
		"env:E2E_CLIENT_SECRET": oauth2ClientSecret,
	}
}

// readOneResponse parses exactly one HTTP response off conn for req, leaving
// the connection positioned for a subsequent request (keep-alive).
func readOneResponse(t *testing.T, conn *tls.Conn, req *http.Request) *http.Response {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	resp.Body.Close()
	resp2 := *resp
	resp2.Body = io.NopCloser(strings.NewReader(string(body)))
	return &resp2
}

// TestE2E_OAuth2CC_AcquiresInjectsAndCachesToken is the oauth2-cc counterpart
// of the marquee static-strategy proof: a hermetic token endpoint mints the
// credential, the mediator acquires and injects it into the guest's request
// without ever handing the guest the real client secret or token, and a
// second request over the same connection is served from cache rather than
// re-exchanging.
func TestE2E_OAuth2CC_AcquiresInjectsAndCachesToken(t *testing.T) {
	const swapDomain = "api.example.com"
	tokenServer, hits := startOAuth2TokenServer(t, oauth2ClientID, oauth2ClientSecret, []string{"read", "write"}, 3600)

	m := startMediatorWithSwapConfigAndResolver(t, swapDomain, oauth2SwapYAML(swapDomain, tokenServer.URL), "Authorization", "oauth2-e2e", oauth2Resolver())
	defer m.closeUpstream()
	defer m.closeListener()

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

	// --- Request 1: acquisition + injection ----------------------------------
	req1, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req1.Host = swapDomain
	req1.Header.Set("Authorization", placeholderCred)
	if err := req1.Write(clientTLS); err != nil {
		t.Fatalf("write request 1: %v", err)
	}
	readOneResponse(t, clientTLS, req1)

	var upstreamAuth string
	select {
	case upstreamAuth = <-m.gotAuth:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received request 1")
	}
	if want := "Bearer " + mintedToken; upstreamAuth != want {
		t.Fatalf("upstream Authorization = %q, want %q", upstreamAuth, want)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("token endpoint hit %d times after request 1, want 1", got)
	}

	// --- Request 2, same connection: served from cache, no second exchange ---
	req2, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req2.Host = swapDomain
	req2.Header.Set("Authorization", placeholderCred)
	req2.Close = true // last request on this connection; let the server close after replying
	if err := req2.Write(clientTLS); err != nil {
		t.Fatalf("write request 2: %v", err)
	}
	readOneResponse(t, clientTLS, req2)

	select {
	case upstreamAuth = <-m.gotAuth:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received request 2")
	}
	if want := "Bearer " + mintedToken; upstreamAuth != want {
		t.Fatalf("upstream Authorization on request 2 = %q, want %q", upstreamAuth, want)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("token endpoint hit %d times after request 2, want 1 (should have been a cache hit)", got)
	}

	select {
	case <-m.handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after client close")
	}

	// --- Leak assertions: neither the client secret nor the minted token ever
	// reach the guest or land in an audited field. ---------------------------
	leakNeedles := []string{oauth2ClientSecret, mintedToken, "Bearer " + mintedToken}
	logSnap := m.log.Snapshot()
	for _, ev := range logSnap {
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
	foundSwap := false
	for _, ev := range logSnap {
		if ev["event"] != "egress_swap" {
			continue
		}
		foundSwap = true
		if ev["type"] != "oauth2-cc" {
			t.Fatalf("egress_swap type = %v, want oauth2-cc", ev["type"])
		}
	}
	if !foundSwap {
		t.Fatalf("no egress_swap audit event; got %+v", logSnap)
	}
}

// TestE2E_OAuth2CC_FailsClosedWhenTokenEndpointUnavailable proves that a guest
// request whose swap entry cannot reach its token endpoint never reaches
// upstream: acquisition fails, injectRequests returns the error, and the
// connection tears down with no response body and no upstream hit.
func TestE2E_OAuth2CC_FailsClosedWhenTokenEndpointUnavailable(t *testing.T) {
	const swapDomain = "api.example.com"
	// A closed server: the URL is well-formed but nothing answers on it.
	deadServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	tokenURL := deadServer.URL
	deadServer.Close()

	m := startMediatorWithSwapConfigAndResolver(t, swapDomain, oauth2SwapYAML(swapDomain, tokenURL), "Authorization", "oauth2-e2e", oauth2Resolver())
	defer m.closeUpstream()
	defer m.closeListener()

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

	req := "GET / HTTP/1.1\r\nHost: " + swapDomain + "\r\nAuthorization: " + placeholderCred + "\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(clientTLS, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	respBytes, _ := io.ReadAll(clientTLS) // a torn-down connection may error or just EOF; either is fine here

	select {
	case <-m.handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after the acquisition failure")
	}
	select {
	case <-m.gotAuth:
		t.Fatal("upstream received a request; acquisition failure must not reach upstream")
	default:
	}
	if len(respBytes) != 0 {
		t.Fatalf("guest received response bytes on a failed-closed acquisition: %q", respBytes)
	}

	foundError := false
	for _, ev := range m.log.Snapshot() {
		if ev["event"] != "egress_swap_error" {
			continue
		}
		foundError = true
		if ev["type"] != "oauth2-cc" {
			t.Fatalf("egress_swap_error type = %v, want oauth2-cc", ev["type"])
		}
		if errStr, _ := ev["error"].(string); strings.Contains(errStr, oauth2ClientSecret) {
			t.Fatalf("client secret leaked into egress_swap_error: %q", errStr)
		}
	}
	if !foundError {
		t.Fatalf("no egress_swap_error audit event; got %+v", m.log.Snapshot())
	}
}

// TestE2E_OAuth2CC_FailsClosedOnInvalidTokenResponse covers a token endpoint
// that answers 200 OK with a body carrying no access_token: parseToken must
// reject it, and the guest request must fail closed exactly as the
// unavailable-endpoint case does.
func TestE2E_OAuth2CC_FailsClosedOnInvalidTokenResponse(t *testing.T) {
	const swapDomain = "api.example.com"
	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token_type":"bearer"}`) // no access_token field
	}))
	defer badServer.Close()

	m := startMediatorWithSwapConfigAndResolver(t, swapDomain, oauth2SwapYAML(swapDomain, badServer.URL), "Authorization", "oauth2-e2e", oauth2Resolver())
	defer m.closeUpstream()
	defer m.closeListener()

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

	req := "GET / HTTP/1.1\r\nHost: " + swapDomain + "\r\nAuthorization: " + placeholderCred + "\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(clientTLS, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	respBytes, _ := io.ReadAll(clientTLS)

	select {
	case <-m.handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not return after the acquisition failure")
	}
	select {
	case <-m.gotAuth:
		t.Fatal("upstream received a request; an invalid token response must not reach upstream")
	default:
	}
	if len(respBytes) != 0 {
		t.Fatalf("guest received response bytes on a failed-closed acquisition: %q", respBytes)
	}
}

// TestE2E_OAuth2CC_NearExpiryTokenIsReacquired is the caching test's mirror
// image: when the token endpoint mints a token already within the cache's
// expiry skew window (tokenSkew, 60s), every acquisition is treated as a
// miss and re-exchanged — a near-dead token is never served as if it were
// good for another request.
func TestE2E_OAuth2CC_NearExpiryTokenIsReacquired(t *testing.T) {
	const swapDomain = "api.example.com"
	// expires_in well inside tokenSkew: get() must always report a miss.
	tokenServer, hits := startOAuth2TokenServer(t, oauth2ClientID, oauth2ClientSecret, []string{"read", "write"}, 1)

	m := startMediatorWithSwapConfigAndResolver(t, swapDomain, oauth2SwapYAML(swapDomain, tokenServer.URL), "Authorization", "oauth2-e2e", oauth2Resolver())
	defer m.closeUpstream()
	defer m.closeListener()

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

	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodGet, "/", nil)
		if err != nil {
			t.Fatalf("build request %d: %v", i, err)
		}
		req.Host = swapDomain
		req.Header.Set("Authorization", placeholderCred)
		if i == 1 {
			req.Close = true
		}
		if err := req.Write(clientTLS); err != nil {
			t.Fatalf("write request %d: %v", i, err)
		}
		readOneResponse(t, clientTLS, req)
		select {
		case <-m.gotAuth:
		case <-time.After(3 * time.Second):
			t.Fatalf("upstream never received request %d", i)
		}
	}

	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("token endpoint hit %d times across 2 requests, want 2 (a near-expiry token must never be reused)", got)
	}
}
