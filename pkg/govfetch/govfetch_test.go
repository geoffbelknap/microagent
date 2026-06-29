package govfetch

import (
	"context"
	"crypto/x509"
	"fmt"
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
