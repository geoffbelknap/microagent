package egress

import (
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

// The brain is transport-agnostic: the byte-stream mediator (Handler) forwards,
// denies, and audits over a HOST-SUPPLIED destination with no netns, no TPROXY,
// no SO_ORIGINAL_DST — driven directly over an in-memory net.Pipe with the
// destination injected via OrigDst. This is the exact move the apple-vf host-fd
// datapath makes (cmd/microagent/egress_datapath.go) and the structural reason a
// wasm sandbox can drive the same brain: the transport supplies the destination,
// the brain rules on it. Proven here over a real loopback upstream.
func TestBrainServesHostSuppliedDst(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "UPSTREAM-OK")
	}))
	defer upstream.Close()
	upAddr := netip.MustParseAddrPort(strings.TrimPrefix(upstream.URL, "http://"))

	logger := &BufferLogger{}
	policy, err := NewPolicy([]string{"allowed.example"})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	newHandler := func(dst netip.AddrPort) *Handler {
		return &Handler{
			Mode:            "mitm",
			AllowlistLocked: true,
			Policy:          policy,
			Logger:          logger,
			OrigDst:         func(net.Conn) (netip.AddrPort, error) { return dst, nil },
			Dial:            net.Dial,
		}
	}

	t.Run("allow_forwards_to_upstream", func(t *testing.T) {
		client, server := net.Pipe()
		go newHandler(upAddr).Handle(server)
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		go func() {
			_, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: allowed.example\r\nConnection: close\r\n\r\n"))
		}()
		body, _ := io.ReadAll(client)
		if !strings.Contains(string(body), "UPSTREAM-OK") {
			t.Fatalf("allowlisted request did not reach upstream over host-supplied dst: %q", string(body))
		}
	})

	t.Run("deny_blocks_and_audits", func(t *testing.T) {
		client, server := net.Pipe()
		go newHandler(upAddr).Handle(server) // denied on host; dst resolves to loopback (inside)
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		go func() {
			_, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: blocked.example\r\nConnection: close\r\n\r\n"))
		}()
		_, _ = io.ReadAll(client) // denied → conn closed, no upstream dial
		found := false
		for _, e := range logger.Snapshot() {
			// The non-allowlisted host resolves to loopback, so an allow-broad-family
			// mode classifies it inside — denied as internal (the SSRF-shaped case).
			if e["event"] == "egress_internal_deny" && e["host"] == "blocked.example" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected an egress_internal_deny audit for blocked.example; got %v", logger.Snapshot())
		}
	})
}
