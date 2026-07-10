package egress

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func staticSwapTable(t *testing.T, host string) *SwapTable {
	t.Helper()
	yaml := fmt.Sprintf("swaps:\n  k:\n    type: static\n    domains: [%s]\n    header: Authorization\n    format: 'Bearer {key}'\n    key_ref: env:K\n", host)
	tbl, err := LoadSwapTable([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadSwapTable: %v", err)
	}
	return tbl
}

// The headline parity: a structured (wasm-shaped) caller's request is governed by
// the SAME brain as the microVM path. The real credential is injected host-side
// (cred-blind) — it never appears in the request the caller handed in, and the
// audit records the swap without the secret.
func TestFetchInjectsCredentialHostSideCredBlind(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, "UPSTREAM-OK")
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	log := &BufferLogger{}
	b := &Brain{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          brainPolicy(t, "127.0.0.1"),
		Swaps:           staticSwapTable(t, "127.0.0.1"),
		Resolver:        fakeResolver{"env:K": "REALSECRET"},
		Cache:           newTokenCache(),
		Logger:          log,
		UpstreamRoots:   pool,
	}

	// The caller hands NO credential — only the destination.
	resp, err := b.Fetch(context.Background(), FetchRequest{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "UPSTREAM-OK") {
		t.Fatalf("unexpected response: status=%d body=%q", resp.Status, resp.Body)
	}
	if gotAuth != "Bearer REALSECRET" {
		t.Fatalf("upstream did not receive the host-injected credential: %q", gotAuth)
	}
	// The secret must not be echoed back to the guest anywhere in the response.
	if strings.Contains(string(resp.Body), "REALSECRET") {
		t.Fatalf("secret leaked into the guest-facing response body")
	}
	var sawSwap, sawAllow, sawClose bool
	for _, e := range log.Snapshot() {
		switch e["event"] {
		case "egress_swap":
			sawSwap = true
		case "egress_allow":
			sawAllow = e["shape"] == "fetch"
		case "egress_close":
			sawClose = e["shape"] == "fetch"
		}
		for _, v := range e {
			if s, ok := v.(string); ok && strings.Contains(s, "REALSECRET") {
				t.Fatalf("secret leaked into audit event: %v", e)
			}
		}
	}
	if !sawSwap || !sawAllow || !sawClose {
		t.Fatalf("audit incomplete: swap=%v allow=%v close=%v: %v", sawSwap, sawAllow, sawClose, log.Snapshot())
	}
}

// A denied destination is fail-closed: a 403 with NO upstream dial, audited
// egress_deny. The guest gets a legible response, not a silent success.
func TestFetchDeniedNoUpstreamDial(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	log := &BufferLogger{}
	b := &Brain{Mode: "strict", Policy: brainPolicy(t, "allowed.example"), Logger: log, UpstreamRoots: pool}

	resp, err := b.Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.Denied || resp.Status != 403 {
		t.Fatalf("expected fail-closed 403 denial, got status=%d denied=%v", resp.Status, resp.Denied)
	}
	if hits.Load() != 0 {
		t.Fatalf("denied request still reached upstream %d time(s)", hits.Load())
	}
	var sawDeny bool
	for _, e := range log.Snapshot() {
		if e["event"] == "egress_deny" {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Fatalf("expected egress_deny audit; got %v", log.Snapshot())
	}
}

// A strict-mode unlisted host is denied on the NAME, before any DNS resolution:
// the denial must not depend on the destination being reachable. Regression for a
// resolve-first ordering bug where an unlisted host was resolved before the policy
// check, so offline the resolve failure surfaced a 502 that masked the real 403
// (and resolving a forbidden host is itself unmediated DNS egress).
func TestFetchStrictDeniesUnlistedBeforeResolve(t *testing.T) {
	log := &BufferLogger{}
	// A hostname (not an IP literal) that is NOT on the allowlist. If the code
	// resolved before deciding, this would produce an egress_fetch_error/502.
	b := &Brain{Mode: "strict", Policy: brainPolicy(t, "allowed.example"), Logger: log}

	resp, err := b.Fetch(context.Background(), FetchRequest{URL: "https://blocked.example.com/data"})
	if err != nil {
		t.Fatalf("a policy denial must not be a Go error: %v", err)
	}
	if !resp.Denied || resp.Status != 403 {
		t.Fatalf("want fail-closed 403, got status=%d denied=%v", resp.Status, resp.Denied)
	}
	var sawDeny bool
	for _, e := range log.Snapshot() {
		if e["event"] == "egress_fetch_error" {
			t.Fatalf("unlisted host was resolved before the policy check: %v", e)
		}
		if e["event"] == "egress_deny" {
			sawDeny = true
			if r, _ := e["reason"].(string); r == "" {
				t.Error("egress_deny carries no reason")
			}
		}
	}
	if !sawDeny {
		t.Fatalf("expected egress_deny; got %v", log.Snapshot())
	}
}

// Guarded mode denies an inside/infra destination (loopback) on the resolved IP,
// auditing egress_internal_deny — the same guarded contract as the microVM path.
func TestFetchGuardedDeniesInside(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	log := &BufferLogger{}
	b := &Brain{Mode: "mitm", Policy: brainPolicy(t), Logger: log, UpstreamRoots: pool}

	resp, err := b.Fetch(context.Background(), FetchRequest{URL: srv.URL}) // 127.0.0.1 → inside
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.Denied {
		t.Fatalf("guarded should deny loopback, got status=%d", resp.Status)
	}
	var sawInternalDeny bool
	for _, e := range log.Snapshot() {
		if e["event"] == "egress_internal_deny" {
			sawInternalDeny = true
		}
	}
	if !sawInternalDeny {
		t.Fatalf("expected egress_internal_deny; got %v", log.Snapshot())
	}
}

// Cred-swap is https-only: the brain refuses to send a real credential over
// plaintext http (it would leak on the wire), failing closed without dialing.
func TestFetchRefusesPlaintextCredential(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	log := &BufferLogger{}
	b := &Brain{
		Mode:            "mitm",
		AllowlistLocked: true,
		Policy:          brainPolicy(t, "127.0.0.1"),
		Swaps:           staticSwapTable(t, "127.0.0.1"),
		Resolver:        fakeResolver{"env:K": "REALSECRET"},
		Cache:           newTokenCache(),
		Logger:          log,
	}

	resp, err := b.Fetch(context.Background(), FetchRequest{URL: srv.URL}) // http, not https
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.Denied || !strings.Contains(resp.Reason, "plaintext") {
		t.Fatalf("expected plaintext-credential refusal, got status=%d denied=%v reason=%q", resp.Status, resp.Denied, resp.Reason)
	}
	if hits.Load() != 0 {
		t.Fatalf("plaintext credential request still reached upstream")
	}
}
