package egress

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"testing"
)

type fakeResolver map[string]string

func (f fakeResolver) Resolve(_ context.Context, ref string) ([]byte, error) {
	if v, ok := f[ref]; ok {
		return []byte(v), nil
	}
	return nil, errNoSecret
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
	for _, ev := range log.Events {
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
