package sandbox

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/geoffbelknap/microagent/internal/egress"
)

// fetchGuest is a WASI module whose ONLY network path is the microagency.fetch
// host capability: it marshals a request, calls fetch, reads the response back
// over linear memory, and prints what it legitimately sees. It never holds a
// credential.
const fetchGuest = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"unsafe"
)

//go:wasmimport microagency fetch
func hostFetch(reqPtr, reqLen uint32) uint64

//go:wasmimport microagency read_response
func hostReadResponse(handle, destPtr, destLen uint32) int32

type req struct {
	Method  string            ` + "`json:\"method\"`" + `
	URL     string            ` + "`json:\"url\"`" + `
	Headers map[string]string ` + "`json:\"headers,omitempty\"`" + `
}
type resp struct {
	Status int    ` + "`json:\"status\"`" + `
	Body   []byte ` + "`json:\"body,omitempty\"`" + `
	Denied bool   ` + "`json:\"denied,omitempty\"`" + `
	Reason string ` + "`json:\"reason,omitempty\"`" + `
}

func ptr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

func main() {
	rb, _ := json.Marshal(req{Method: "GET", URL: os.Getenv("TARGET_URL")})
	packed := hostFetch(ptr(rb), uint32(len(rb)))
	runtime.KeepAlive(rb)
	if packed == 0 {
		fmt.Print("FETCH_FAILED")
		os.Exit(2)
	}
	handle := uint32(packed >> 32)
	length := uint32(packed)
	buf := make([]byte, length)
	n := hostReadResponse(handle, ptr(buf), length)
	runtime.KeepAlive(buf)
	if n < 0 {
		fmt.Print("READ_FAILED")
		os.Exit(3)
	}
	var out resp
	if err := json.Unmarshal(buf[:n], &out); err != nil {
		fmt.Print("UNMARSHAL_FAILED")
		os.Exit(4)
	}
	fmt.Printf("status=%d denied=%v body=%s", out.Status, out.Denied, string(out.Body))
}
`

// The headline: a wasm module's governed egress is mediated by the same brain as
// the microVM path. The real credential is injected host-side (cred-blind) — it
// is in the HOST environment, never in the guest's env, never in the guest's
// output; the upstream sees it, the audit records the swap without it.
func TestSandboxFetchGovernedByBrainCredBlind(t *testing.T) {
	t.Setenv("K", "REALSECRET")
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, "UPSTREAM-OK")
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	wasm := buildWASIBytes(t, fetchGuest)
	log := &egress.BufferLogger{}
	res, err := Run(context.Background(), Config{
		Module: wasm,
		Env:    map[string]string{"TARGET_URL": srv.URL}, // note: K is NOT given to the guest
		Egress: &EgressConfig{
			Allow:         []string{"127.0.0.1"},
			Mode:          "strict",
			Swaps:         staticSwap(t, "127.0.0.1"),
			Logger:        log,
			UpstreamRoots: pool,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("guest exit %d, stderr=%q stdout=%q", res.ExitCode, res.Stderr, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "status=200") || !strings.Contains(res.Stdout, "UPSTREAM-OK") {
		t.Fatalf("guest did not see the governed response: %q", res.Stdout)
	}
	if gotAuth != "Bearer REALSECRET" {
		t.Fatalf("upstream did not receive the host-injected credential: %q", gotAuth)
	}
	if strings.Contains(res.Stdout, "REALSECRET") {
		t.Fatalf("secret reached the guest output (cred-blindness broken): %q", res.Stdout)
	}
	var sawSwap, sawAllow bool
	for _, e := range log.Snapshot() {
		switch e["event"] {
		case "egress_swap":
			sawSwap = true
		case "egress_allow":
			sawAllow = e["shape"] == "fetch"
		}
		for _, v := range e {
			if s, ok := v.(string); ok && strings.Contains(s, "REALSECRET") {
				t.Fatalf("secret leaked into audit: %v", e)
			}
		}
	}
	if !sawSwap || !sawAllow {
		t.Fatalf("audit incomplete: swap=%v allow=%v: %v", sawSwap, sawAllow, log.Snapshot())
	}
}

// A non-allowlisted destination is denied fail-closed: the guest gets a legible
// denied response and the upstream is never contacted.
func TestSandboxFetchDeniedFailClosed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	wasm := buildWASIBytes(t, fetchGuest)
	log := &egress.BufferLogger{}
	res, err := Run(context.Background(), Config{
		Module: wasm,
		Env:    map[string]string{"TARGET_URL": srv.URL},
		Egress: &EgressConfig{Allow: []string{"allowed.example"}, Mode: "strict", Logger: log, UpstreamRoots: pool},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "denied=true") {
		t.Fatalf("expected a denied response, got %q", res.Stdout)
	}
	if hits.Load() != 0 {
		t.Fatalf("denied request still reached upstream")
	}
}

// Without an EgressConfig the module has NO network capability: a guest that
// imports microagency.fetch fails to instantiate (fail-closed by absence).
func TestSandboxNoEgressCapabilityByDefault(t *testing.T) {
	wasm := buildWASIBytes(t, fetchGuest)
	_, err := Run(context.Background(), Config{Module: wasm}) // no Egress
	if err == nil {
		t.Fatal("expected instantiation to fail with no egress capability provided")
	}
}

func staticSwap(t *testing.T, host string) *egress.SwapTable {
	t.Helper()
	yaml := fmt.Sprintf("swaps:\n  k:\n    type: static\n    domains: [%s]\n    header: Authorization\n    format: 'Bearer {key}'\n    key_ref: env:K\n", host)
	tbl, err := egress.LoadSwapTable([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadSwapTable: %v", err)
	}
	return tbl
}
