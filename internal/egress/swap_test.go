package egress

import (
	"bufio"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeResolver map[string]string

func (f fakeResolver) Resolve(_ context.Context, ref string) ([]byte, error) {
	if v, ok := f[ref]; ok {
		return []byte(v), nil
	}
	return nil, errNoSecret
}

type trackingResolver struct {
	calls int
}

func (r *trackingResolver) Resolve(context.Context, string) ([]byte, error) {
	r.calls++
	return []byte("must-not-be-read"), nil
}

func TestStaticAcquire_RendersFormat(t *testing.T) {
	e := SwapEntry{Type: "static", KeyRef: "env:K", Header: "Authorization", Format: "Bearer {key}"}
	sw := &Swapper{Resolver: fakeResolver{"env:K": "sek"}, Cache: newTokenCache()}
	hdr, val, err := sw.acquire(context.Background(), e)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if hdr != "Authorization" || val != "Bearer sek" {
		t.Fatalf("got %q=%q", hdr, val)
	}
}

func TestStaticAcquire_FailsClosedOnMissingSecret(t *testing.T) {
	e := SwapEntry{Type: "static", KeyRef: "env:MISSING"}
	sw := &Swapper{Resolver: fakeResolver{}, Cache: newTokenCache()}
	if _, _, err := sw.acquire(context.Background(), e); err == nil {
		t.Fatal("expected fail-closed error on missing secret")
	}
}

func TestStaticAcquire_FailsClosedOnNilResolver(t *testing.T) {
	e := SwapEntry{Type: "static", KeyRef: "env:K"}
	sw := &Swapper{Resolver: nil, Cache: newTokenCache()}
	if _, _, err := sw.acquire(context.Background(), e); err == nil {
		t.Fatal("expected fail-closed error when resolver is nil")
	}
}

func TestAcquireOAuth2CC_FetchesAndCaches(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostFormValue("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", got)
		}
		if got := r.PostFormValue("client_id"); got != "cid" {
			t.Errorf("client_id = %q, want cid", got)
		}
		if got := r.PostFormValue("client_secret"); got != "csec" {
			t.Errorf("client_secret = %q, want csec", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"AT","expires_in":3600}`))
	}))
	defer ts.Close()

	sw := &Swapper{
		Resolver: fakeResolver{"env:CID": "cid", "env:CSEC": "csec"},
		Cache:    newTokenCache(),
		HTTP:     ts.Client(),
	}
	e := SwapEntry{
		Name:            "svc",
		Type:            "oauth2-cc",
		TokenURL:        ts.URL,
		ClientIDRef:     "env:CID",
		ClientSecretRef: "env:CSEC",
		Scopes:          []string{"read"},
	}

	for i := 0; i < 2; i++ {
		hdr, val, err := sw.acquire(context.Background(), e)
		if err != nil {
			t.Fatalf("acquire #%d: %v", i, err)
		}
		if hdr != "Authorization" || val != "Bearer AT" {
			t.Fatalf("acquire #%d = %q=%q, want Authorization=Bearer AT", i, hdr, val)
		}
	}

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (second acquire must be a cache hit)", got)
	}
}

func TestAcquireOAuth2CC_RejectsInsecureURLBeforeResolvingSecrets(t *testing.T) {
	resolver := &trackingResolver{}
	sw := &Swapper{Resolver: resolver, Cache: newTokenCache()}
	e := SwapEntry{
		Name:            "svc",
		Type:            "oauth2-cc",
		TokenURL:        "http://auth.example.com/token",
		ClientIDRef:     "env:CID",
		ClientSecretRef: "env:CSEC",
	}
	if _, _, err := sw.acquire(context.Background(), e); err == nil {
		t.Fatal("acquire accepted a plaintext remote token endpoint")
	}
	if resolver.calls != 0 {
		t.Fatalf("resolved %d secrets before rejecting token endpoint", resolver.calls)
	}
}

func TestAcquireOAuth2CC_DoesNotFollowRedirects(t *testing.T) {
	var redirectedHits int32
	redirected := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&redirectedHits, 1)
	}))
	defer redirected.Close()

	tokenEndpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirected.URL, http.StatusTemporaryRedirect)
	}))
	defer tokenEndpoint.Close()

	sw := &Swapper{
		Resolver: fakeResolver{"env:CID": "cid", "env:CSEC": "csec"},
		Cache:    newTokenCache(),
		HTTP:     tokenEndpoint.Client(),
	}
	e := SwapEntry{
		Name:            "svc",
		Type:            "oauth2-cc",
		TokenURL:        tokenEndpoint.URL,
		ClientIDRef:     "env:CID",
		ClientSecretRef: "env:CSEC",
	}
	if _, _, err := sw.acquire(context.Background(), e); err == nil || !strings.Contains(err.Error(), "status 307") {
		t.Fatalf("acquire error = %v, want refused redirect status", err)
	}
	if got := atomic.LoadInt32(&redirectedHits); got != 0 {
		t.Fatalf("redirect target received %d credential-bearing requests", got)
	}
}

func TestAcquireJWTBearer_SignsAssertion(t *testing.T) {
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pkPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(pk),
	})

	sw := &Swapper{
		Resolver: fakeResolver{"env:PK": string(pkPEM)},
		Cache:    newTokenCache(),
	}
	e := SwapEntry{
		Name:          "partner",
		Type:          "jwt-bearer",
		Algorithm:     "RS256",
		SigningKeyRef: "env:PK",
		Claims: map[string]string{
			"iss": "app-1",
			"aud": "https://api.partner.com",
		},
		TokenTTLSeconds: 600,
	}

	hdr, val, err := sw.acquire(context.Background(), e)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if hdr != "Authorization" {
		t.Fatalf("header = %q, want Authorization", hdr)
	}
	if !strings.HasPrefix(val, "Bearer ") {
		t.Fatalf("value = %q, want Bearer prefix", val)
	}
	jwt := strings.TrimPrefix(val, "Bearer ")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d segments, want 3", len(parts))
	}

	// Verify the RS256 signature over header.payload with the public key.
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&pk.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature verify: %v", err)
	}
}

func TestInjectRequests_POSTWithBodyForwardsBodyIntact(t *testing.T) {
	tbl, err := LoadSwapTable([]byte(`swaps:
  example:
    type: static
    domains: ["api.example.com"]
    header: Authorization
    format: "Bearer {key}"
    key_ref: "env:K"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sw := &Swapper{Resolver: fakeResolver{"env:K": "REALSECRET"}, Cache: newTokenCache()}
	log := &BufferLogger{}

	const body = `{"hello":"world","n":42}`

	// guest writes a POST carrying a body; injectRequests reads it from guestR
	// and writes the rewritten request to upW; the test reads it back from upR.
	guestR, guestW := net.Pipe()
	upR, upW := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- injectRequests(guestR, upW, "api.example.com", sw, tbl, log) }()

	go func() {
		req, _ := http.NewRequest("POST", "https://api.example.com/v1/thing", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer PLACEHOLDER")
		req.Header.Set("Content-Type", "application/json")
		_ = req.Write(guestW)
		// Close the guest side so injectRequests returns (io.EOF) after the
		// request has been forwarded.
		_ = guestW.Close()
	}()

	got, err := http.ReadRequest(bufio.NewReader(upR))
	if err != nil {
		t.Fatalf("read upstream request: %v", err)
	}
	if got.Method != "POST" {
		t.Fatalf("Method = %q, want POST", got.Method)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer REALSECRET" {
		t.Fatalf("Authorization = %q, want %q", h, "Bearer REALSECRET")
	}
	// The body must be forwarded byte-for-byte to upstream (http.ReadRequest +
	// header rewrite + req.Write must not drop or corrupt it).
	gotBody, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	_ = got.Body.Close()
	if string(gotBody) != body {
		t.Fatalf("upstream body = %q, want %q", string(gotBody), body)
	}

	// Drain injectRequests; closing the pipes lets it finish if still writing.
	_ = upR.Close()
	_ = upW.Close()
	<-done

	// The real secret must never leak into the audit log.
	for _, ev := range log.Snapshot() {
		for k, v := range ev {
			if s, ok := v.(string); ok && (s == "REALSECRET" || s == "Bearer REALSECRET") {
				t.Fatalf("secret leaked into audit field %q=%q", k, s)
			}
		}
	}
}

func TestRelayHTTPRequestsDeniesDNSOverHTTPS(t *testing.T) {
	tests := []struct {
		name string
		req  string
	}{
		{
			name: "well-known path",
			req:  "GET /dns-query?dns=AAAB HTTP/1.1\r\nHost: resolver.example\r\n\r\n",
		},
		{
			name: "wire-format media type",
			req:  "POST /resolve HTTP/1.1\r\nHost: resolver.example\r\nContent-Type: application/dns-message; charset=binary\r\nContent-Length: 2\r\n\r\nxx",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var upstream strings.Builder
			log := &BufferLogger{}
			err := relayHTTPRequests(strings.NewReader(tc.req), &upstream, "resolver.example", nil, nil, log)
			if !errors.Is(err, errDNSOverHTTPS) {
				t.Fatalf("error = %v, want errDNSOverHTTPS", err)
			}
			if upstream.Len() != 0 {
				t.Fatalf("request reached upstream: %q", upstream.String())
			}
			events := log.Snapshot()
			if len(events) != 1 || events[0]["event"] != "egress_deny" || events[0]["signal"] != SignalDNSOverHTTPS {
				t.Fatalf("events = %#v, want one dns-over-https denial", events)
			}
		})
	}
}

func TestRelayHTTPRequestsAllowsOrdinaryRequest(t *testing.T) {
	request := "GET /v1/models HTTP/1.1\r\nHost: api.example.com\r\n\r\n"
	var upstream strings.Builder
	err := relayHTTPRequests(strings.NewReader(request), &upstream, "api.example.com", nil, nil, &BufferLogger{})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v, want EOF", err)
	}
	if !strings.Contains(upstream.String(), "/v1/models") {
		t.Fatalf("ordinary request not forwarded: %q", upstream.String())
	}
}

func TestInjectRequests_RewritesAuthHeader(t *testing.T) {
	tbl, err := LoadSwapTable([]byte(`swaps:
  example:
    type: static
    domains: ["api.example.com"]
    header: Authorization
    format: "Bearer {key}"
    key_ref: "env:K"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sw := &Swapper{Resolver: fakeResolver{"env:K": "REALSECRET"}, Cache: newTokenCache()}
	log := &BufferLogger{}

	// guest writes a request; injectRequests reads it from guestR and writes
	// the rewritten request to upW; the test reads it back from upR.
	guestR, guestW := net.Pipe()
	upR, upW := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- injectRequests(guestR, upW, "api.example.com", sw, tbl, log) }()

	// Send a single request carrying a placeholder credential.
	go func() {
		req, _ := http.NewRequest("GET", "https://api.example.com/v1/thing", nil)
		req.Header.Set("Authorization", "Bearer PLACEHOLDER")
		_ = req.Write(guestW)
		// Close the guest side so injectRequests returns (io.EOF) after the
		// request has been forwarded.
		_ = guestW.Close()
	}()

	got, err := http.ReadRequest(bufio.NewReader(upR))
	if err != nil {
		t.Fatalf("read upstream request: %v", err)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer REALSECRET" {
		t.Fatalf("Authorization = %q, want %q", h, "Bearer REALSECRET")
	}
	if got.Host != "api.example.com" {
		t.Fatalf("Host = %q, want api.example.com", got.Host)
	}

	// Drain injectRequests; closing upW lets it finish if it is still writing.
	_ = upR.Close()
	_ = upW.Close()
	<-done

	// Audit must record host/swap/type but never the secret or header value.
	foundSwap := false
	for _, ev := range log.Snapshot() {
		if ev["event"] == "egress_swap" {
			foundSwap = true
			if ev["host"] != "api.example.com" || ev["swap"] != "example" || ev["type"] != "static" {
				t.Fatalf("egress_swap fields = %+v", ev)
			}
		}
		for k, v := range ev {
			if s, ok := v.(string); ok {
				if s == "REALSECRET" || s == "Bearer REALSECRET" {
					t.Fatalf("secret leaked into audit field %q=%q", k, s)
				}
			}
		}
	}
	if !foundSwap {
		t.Fatal("expected an egress_swap audit event")
	}
}

// TestInjectRequests_InjectsSNICredentialNotInnerHost is the B15 guard: the
// credential is chosen by the TLS-verified SNI (the upstream this connection is
// pinned to), not the guest-controlled inner Host header. A guest smuggling a
// different swap host's Host header over an allowed SNI must NOT get that other
// host's credential injected into this upstream (a credential-isolation breach).
func TestInjectRequests_InjectsSNICredentialNotInnerHost(t *testing.T) {
	tbl, err := LoadSwapTable([]byte(`swaps:
  openai:
    type: static
    domains: ["api.openai.com"]
    header: X-OpenAI-Key
    format: "{key}"
    key_ref: "env:OPENAI"
  anthropic:
    type: static
    domains: ["api.anthropic.com"]
    header: X-Anthropic-Key
    format: "{key}"
    key_ref: "env:ANTHROPIC"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sw := &Swapper{Resolver: fakeResolver{"env:OPENAI": "OPENAI-SECRET", "env:ANTHROPIC": "ANTHROPIC-SECRET"}, Cache: newTokenCache()}
	log := &BufferLogger{}

	guestR, guestW := net.Pipe()
	upR, upW := net.Pipe()

	// The connection's verified SNI is api.openai.com; the guest tries to smuggle a
	// request bearing Host: api.anthropic.com over it to steal the Anthropic key.
	done := make(chan error, 1)
	go func() { done <- injectRequests(guestR, upW, "api.openai.com", sw, tbl, log) }()
	go func() {
		req, _ := http.NewRequest("GET", "http://api.anthropic.com/v1/messages", nil)
		req.Host = "api.anthropic.com"
		_ = req.Write(guestW)
		_ = guestW.Close()
	}()

	got, err := http.ReadRequest(bufio.NewReader(upR))
	if err != nil {
		t.Fatalf("read upstream request: %v", err)
	}
	if v := got.Header.Get("X-Anthropic-Key"); v != "" {
		t.Fatalf("Anthropic credential leaked into the api.openai.com upstream: %q", v)
	}
	if v := got.Header.Get("X-OpenAI-Key"); v != "OPENAI-SECRET" {
		t.Fatalf("X-OpenAI-Key = %q, want the OpenAI secret (credential chosen by the verified SNI)", v)
	}
}
