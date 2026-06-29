package govfetch

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// writeSwapConfig writes a static credential-swap config for host and returns its
// path. The referenced secret (env:K) is resolved host-side by the brain.
func writeSwapConfig(t *testing.T, host string) string {
	t.Helper()
	yaml := fmt.Sprintf("swaps:\n  k:\n    type: static\n    domains: [%s]\n    header: Authorization\n    format: 'Bearer {key}'\n    key_ref: env:K\n", host)
	path := filepath.Join(t.TempDir(), "swap.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write swap config: %v", err)
	}
	return path
}

func auditHas(events []AuditEvent, name string) bool {
	for _, e := range events {
		if e.Event == name {
			return true
		}
	}
	return false
}

// An allowlisted host returns its data, audited allow+close.
func TestFetchAllowlistedReturnsData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "DATA-OK")
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), Spec{URL: srv.URL, EgressAllow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Status != 200 || string(res.Body) != "DATA-OK" {
		t.Fatalf("unexpected: status=%d body=%q", res.Status, res.Body)
	}
	if !auditHas(res.Audit, "egress_allow") || !auditHas(res.Audit, "egress_close") {
		t.Fatalf("missing allow/close audit: %+v", res.Audit)
	}
}

// A non-allowlisted host is refused fail-closed (403, no body, no dial), audited
// egress_deny.
func TestFetchDeniesNonAllowlisted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), Spec{URL: srv.URL, EgressAllow: []string{"allowed.example"}})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Status != 403 || len(res.Body) != 0 {
		t.Fatalf("expected fail-closed 403 with no body, got status=%d body=%q", res.Status, res.Body)
	}
	if hits.Load() != 0 {
		t.Fatalf("denied request still reached upstream %d time(s)", hits.Load())
	}
	if !auditHas(res.Audit, "egress_deny") {
		t.Fatalf("missing egress_deny audit: %+v", res.Audit)
	}
}

// The load-bearing contract: a swapped credential is injected host-side into the
// upstream request and NEVER appears in Result — not in Body, not in any Audit
// field. Result carries the data + the audit, never the secret.
func TestFetchCredBlind(t *testing.T) {
	t.Setenv("K", "SUPERSECRET")
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, "DATA-OK")
	}))
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	upstreamRootsForTest = pool
	defer func() { upstreamRootsForTest = nil }()

	res, err := Fetch(context.Background(), Spec{
		URL:            srv.URL,
		EgressAllow:    []string{"127.0.0.1"},
		SwapConfigPath: writeSwapConfig(t, "127.0.0.1"),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Status != 200 || string(res.Body) != "DATA-OK" {
		t.Fatalf("unexpected: status=%d body=%q", res.Status, res.Body)
	}
	// The credential WAS injected host-side (the upstream saw it)...
	if gotAuth != "Bearer SUPERSECRET" {
		t.Fatalf("credential not injected host-side: %q", gotAuth)
	}
	// ...but it must NOT appear anywhere in Result.
	if strings.Contains(string(res.Body), "SUPERSECRET") {
		t.Fatalf("secret leaked into Result.Body")
	}
	for _, e := range res.Audit {
		for k, v := range e.Fields {
			if s, ok := v.(string); ok && strings.Contains(s, "SUPERSECRET") {
				t.Fatalf("secret leaked into Audit %q field %q: %v", e.Event, k, v)
			}
		}
	}
	if !auditHas(res.Audit, "egress_swap") {
		t.Fatalf("missing egress_swap audit: %+v", res.Audit)
	}
}

// A swap over plaintext http is refused (the brain never sends a real credential
// in the clear): fail-closed, no upstream dial, no secret anywhere.
func TestFetchRefusesPlaintextCredential(t *testing.T) {
	t.Setenv("K", "SUPERSECRET")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), Spec{
		URL:            srv.URL, // http, not https
		EgressAllow:    []string{"127.0.0.1"},
		SwapConfigPath: writeSwapConfig(t, "127.0.0.1"),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Status != 403 {
		t.Fatalf("expected plaintext-credential refusal (403), got status=%d", res.Status)
	}
	if hits.Load() != 0 {
		t.Fatalf("plaintext credential request still reached upstream")
	}
	if strings.Contains(string(res.Body), "SUPERSECRET") {
		t.Fatalf("secret leaked into Result.Body")
	}
}

// A bad swap config fails closed: an error, no data, no fetch.
func TestFetchBadSwapConfigFailsClosed(t *testing.T) {
	res, err := Fetch(context.Background(), Spec{
		URL:            "https://127.0.0.1/",
		EgressAllow:    []string{"127.0.0.1"},
		SwapConfigPath: filepath.Join(t.TempDir(), "does-not-exist.yaml"),
	})
	if err == nil {
		t.Fatal("expected an error for an unreadable swap config")
	}
	if res.Status != 0 || len(res.Body) != 0 {
		t.Fatalf("expected empty result on config failure, got status=%d body=%q", res.Status, res.Body)
	}
}

// MaxBytes bounds the response body: an over-cap body fails closed (502), audited
// egress_cap_exceeded.
func TestFetchMaxBytesCaps(t *testing.T) {
	big := strings.Repeat("x", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, big)
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), Spec{
		URL:         srv.URL,
		EgressAllow: []string{"127.0.0.1"},
		MaxBytes:    1024,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Status != 502 {
		t.Fatalf("expected 502 for an over-cap body, got status=%d", res.Status)
	}
	if !auditHas(res.Audit, "egress_cap_exceeded") {
		t.Fatalf("missing egress_cap_exceeded audit: %+v", res.Audit)
	}
}

// An unparseable URL is a host-internal fault: error, no data.
func TestFetchInvalidURLErrors(t *testing.T) {
	_, err := Fetch(context.Background(), Spec{URL: "://nope", EgressAllow: []string{"x"}})
	if err == nil {
		t.Fatal("expected an error for an invalid URL")
	}
}

// The Host/Dst/Reason accessors normalize the per-event key differences so a
// consumer's {Event, Host, Dst, Reason} audit type maps directly.
func TestAuditEventAccessors(t *testing.T) {
	find := func(events []AuditEvent, name string) *AuditEvent {
		for i := range events {
			if events[i].Event == name {
				return &events[i]
			}
		}
		return nil
	}

	// Denial: Reason() reads the "reason" field.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	res, err := Fetch(context.Background(), Spec{URL: srv.URL, EgressAllow: []string{"allowed.example"}})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	deny := find(res.Audit, "egress_deny")
	if deny == nil {
		t.Fatalf("no egress_deny event: %+v", res.Audit)
	}
	if deny.Host() != "127.0.0.1" || !strings.HasPrefix(deny.Dst(), "127.0.0.1:") || deny.Reason() != "not allowlisted" {
		t.Fatalf("deny accessors: host=%q dst=%q reason=%q", deny.Host(), deny.Dst(), deny.Reason())
	}

	// Failure: Reason() falls back to the "error" field. A closed loopback port
	// gives a deterministic upstream (connection-refused) error with no DNS.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatalf("listen: %v", lerr)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()
	res2, err := Fetch(context.Background(), Spec{URL: "https://" + closedAddr + "/", EgressAllow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("Fetch (closed port): %v", err)
	}
	fe := find(res2.Audit, "egress_fetch_error")
	if fe == nil {
		t.Fatalf("no egress_fetch_error event: %+v", res2.Audit)
	}
	if fe.Host() != "127.0.0.1" || fe.Dst() != closedAddr || fe.Reason() == "" {
		t.Fatalf("fetch_error accessors: host=%q dst=%q reason=%q", fe.Host(), fe.Dst(), fe.Reason())
	}

	// A clean allow has no reason.
	res3, err := Fetch(context.Background(), Spec{URL: srv.URL, EgressAllow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("Fetch (allow): %v", err)
	}
	allow := find(res3.Audit, "egress_allow")
	if allow == nil || allow.Reason() != "" {
		t.Fatalf("allow should have empty reason: %+v", allow)
	}
}
