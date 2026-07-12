//go:build linux

package firecracker

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestBrokerForPortSelectsByPort asserts brokerForPort finds the Brokers
// entry whose VsockPort matches, and falls back to the legacy single Broker
// field when Brokers is empty (state persisted before multi-endpoint
// brokers existed).
func TestBrokerForPortSelectsByPort(t *testing.T) {
	a := &vmkit.BrokerConfig{Upstream: "https://a.example.com", VsockPort: 1032}
	b := &vmkit.BrokerConfig{Upstream: "https://b.example.com", VsockPort: 1033}
	cfg := &vmkit.Config{Brokers: []*vmkit.BrokerConfig{a, b}}

	if got := brokerForPort(cfg, 1032); got != a {
		t.Fatalf("brokerForPort(1032) = %+v, want endpoint a", got)
	}
	if got := brokerForPort(cfg, 1033); got != b {
		t.Fatalf("brokerForPort(1033) = %+v, want endpoint b", got)
	}
	if got := brokerForPort(cfg, 9999); got != nil {
		t.Fatalf("brokerForPort(9999) = %+v, want nil (no match)", got)
	}

	// Back-compat: when Brokers is empty, the legacy scheme only ever
	// registered one broker listener, so the fallback to config.Broker does
	// not need to (and does not) match by port.
	legacy := &vmkit.BrokerConfig{Upstream: "https://legacy.example.com", VsockPort: 1032}
	legacyCfg := &vmkit.Config{Broker: legacy}
	if got := brokerForPort(legacyCfg, 1032); got != legacy {
		t.Fatalf("brokerForPort fallback to legacy Broker = %+v, want legacy", got)
	}
}

// TestStartVsockListenersServesTwoBrokerEndpoints proves N broker endpoints
// each run their own terminate listener, keyed to their own upstream and
// secret, and share the single per-workspace access log — distinguished by
// DecisionRecord.Host, per endpoint upstream.
func TestStartVsockListenersServesTwoBrokerEndpoints(t *testing.T) {
	const liveA = "live-broker-secret-a-1f2e"
	const liveB = "live-broker-secret-b-3c4d"
	t.Setenv("MA_BROKER_TOK_A", liveA)
	t.Setenv("MA_BROKER_TOK_B", liveB)

	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+liveA {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "a-ok")
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+liveB {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "b-ok")
	}))
	defer upstreamB.Close()

	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		Brokers: []*vmkit.BrokerConfig{
			{Upstream: upstreamA.URL, Secret: vmkit.SecretRef{Name: "a", Ref: "env:MA_BROKER_TOK_A"}, VsockPort: 1032},
			{Upstream: upstreamB.URL, Secret: vmkit.SecretRef{Name: "b", Ref: "env:MA_BROKER_TOK_B"}, VsockPort: 1033},
		},
		VsockListeners: []vmkit.VsockListener{
			{Port: 1032, Target: broker.ListenerTarget},
			{Port: 1033, Target: broker.ListenerTarget},
		},
	}
	set, err := startVsockListeners(opts, cfg)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	defer set.Close()

	reqA, _ := http.NewRequest(http.MethodGet, "http://broker/v1/ping", nil)
	reqA.Header.Set("Authorization", "Bearer @secret:a")
	respA, err := brokerTestClient(opts, 1032).Do(reqA)
	if err != nil {
		t.Fatalf("endpoint a request: %v", err)
	}
	defer respA.Body.Close()
	bodyA, _ := io.ReadAll(respA.Body)
	if respA.StatusCode != http.StatusOK || string(bodyA) != "a-ok" {
		t.Fatalf("endpoint a: status %d body %q", respA.StatusCode, bodyA)
	}

	reqB, _ := http.NewRequest(http.MethodGet, "http://broker/v1/ping", nil)
	reqB.Header.Set("Authorization", "Bearer @secret:b")
	respB, err := brokerTestClient(opts, 1033).Do(reqB)
	if err != nil {
		t.Fatalf("endpoint b request: %v", err)
	}
	defer respB.Body.Close()
	bodyB, _ := io.ReadAll(respB.Body)
	if respB.StatusCode != http.StatusOK || string(bodyB) != "b-ok" {
		t.Fatalf("endpoint b: status %d body %q", respB.StatusCode, bodyB)
	}

	// A cross-wired secret must not authenticate against the other endpoint's
	// upstream: endpoint b never resolves reference "a".
	crossReq, _ := http.NewRequest(http.MethodGet, "http://broker/v1/ping", nil)
	crossReq.Header.Set("Authorization", "Bearer @secret:a")
	crossResp, err := brokerTestClient(opts, 1033).Do(crossReq)
	if err != nil {
		t.Fatalf("cross-wired request: %v", err)
	}
	defer crossResp.Body.Close()
	if crossResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("endpoint b resolved endpoint a's secret reference: status %d", crossResp.StatusCode)
	}

	// Both endpoints' decisions land in the ONE shared access log, one JSON
	// record per line, distinguished by Host.
	trail := readFileWhenLines(t, brokerAccessLogPath(dir, "ws"), 3)
	hostA := strings.TrimPrefix(upstreamA.URL, "http://")
	hostB := strings.TrimPrefix(upstreamB.URL, "http://")
	if !strings.Contains(string(trail), hostA) {
		t.Fatalf("access log missing endpoint a's host %q: %s", hostA, trail)
	}
	if !strings.Contains(string(trail), hostB) {
		t.Fatalf("access log missing endpoint b's host %q: %s", hostB, trail)
	}
	lines := strings.Split(strings.TrimRight(string(trail), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 decision records (2 allow + 1 deny), got %d: %s", len(lines), trail)
	}
}
