//go:build linux

package firecracker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// brokerTestClient returns an HTTP client whose every connection dials the
// workspace's broker vsock UDS — the host-side stand-in for the guest bridge.
func brokerTestClient(opts Options, port uint32) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", firecrackerGuestVsockPath(opts, port))
		},
	}}
}

func TestStartVsockListenersServesBroker(t *testing.T) {
	const live = "live-broker-secret-9f83a1c7"
	t.Setenv("MA_BROKER_TOK", live)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+live {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		Broker: &vmkit.BrokerConfig{
			Upstream: upstream.URL,
			Secret:   vmkit.SecretRef{Name: "api", Ref: "env:MA_BROKER_TOK"},
		},
		VsockListeners: []vmkit.VsockListener{{Port: 1032, Target: broker.ListenerTarget}},
	}
	set, err := startVsockListeners(opts, cfg)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	defer set.Close()

	// The broker socket can spend the workspace credential: owner-only.
	info, err := os.Stat(firecrackerGuestVsockPath(opts, 1032))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("broker socket mode = %v, want 0600", info.Mode().Perm())
	}

	req, err := http.NewRequest(http.MethodGet, "http://broker/v1/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer @secret:api")
	resp, err := brokerTestClient(opts, 1032).Do(req)
	if err != nil {
		t.Fatalf("broker request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "upstream-ok" {
		t.Fatalf("status %d body %q — upstream did not see the live credential", resp.StatusCode, body)
	}

	// The default trail is the minimized decision stream: verdict + metadata,
	// the NAMES of the references used — no headers, no path, and never the
	// live secret (absent by construction, not by redaction).
	trail, err := os.ReadFile(brokerAccessLogPath(dir, "ws"))
	if err != nil {
		t.Fatalf("broker access log: %v", err)
	}
	if !strings.Contains(string(trail), "broker_request_allow") {
		t.Fatalf("access log missing the decision record: %s", trail)
	}
	if !strings.Contains(string(trail), `"secret_refs":["api"]`) {
		t.Fatalf("access log missing the credential-use metadata: %s", trail)
	}
	for _, banned := range []string{"@secret:api", "/v1/ping", `"headers"`, live} {
		if strings.Contains(string(trail), banned) {
			t.Fatalf("default trail must be minimized metadata, found %q: %s", banned, trail)
		}
	}
	// Capture was not opted in: no capture file may exist.
	if _, err := os.Stat(brokerCaptureLogPath(dir, "ws")); !os.IsNotExist(err) {
		t.Fatalf("capture file exists without opt-in: %v", err)
	}
}

func TestStartVsockListenersBrokerCaptureOptIn(t *testing.T) {
	const live = "live-broker-secret-capture-4e2b"
	t.Setenv("MA_BROKER_TOK", live)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		Broker: &vmkit.BrokerConfig{
			Upstream: upstream.URL,
			Secret:   vmkit.SecretRef{Name: "api", Ref: "env:MA_BROKER_TOK"},
			Capture:  true,
		},
		VsockListeners: []vmkit.VsockListener{{Port: 1032, Target: broker.ListenerTarget}},
	}
	set, err := startVsockListeners(opts, cfg)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	defer set.Close()

	req, _ := http.NewRequest(http.MethodPost, "http://broker/v1/messages", strings.NewReader("capture me"))
	req.Header.Set("Authorization", "Bearer @secret:api")
	resp, err := brokerTestClient(opts, 1032).Do(req)
	if err != nil {
		t.Fatalf("broker request: %v", err)
	}
	resp.Body.Close()

	// The capture file exists, is owner-only, and holds the pre-swap request:
	// reference verbatim, path, body — never the live secret.
	capPath := brokerCaptureLogPath(dir, "ws")
	info, err := os.Stat(capPath)
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("capture file mode = %v, want 0600", info.Mode().Perm())
	}
	captured, _ := os.ReadFile(capPath)
	for _, want := range []string{"@secret:api", "/v1/messages"} {
		if !strings.Contains(string(captured), want) {
			t.Fatalf("capture missing %q: %s", want, captured)
		}
	}

	// Companion-level invariant: the live secret is absent from EVERY file in
	// the workspace state dir.
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil // sockets etc.
		}
		if strings.Contains(string(b), live) {
			t.Fatalf("INVARIANT VIOLATION: live secret present in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStartVsockListenersBrokerUnknownRefFailsRequest(t *testing.T) {
	const live = "live-broker-secret-2"
	t.Setenv("MA_BROKER_TOK", live)
	var sawUpstream bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUpstream = true
	}))
	defer upstream.Close()

	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		Broker: &vmkit.BrokerConfig{
			Upstream: upstream.URL,
			Secret:   vmkit.SecretRef{Name: "api", Ref: "env:MA_BROKER_TOK"},
		},
		VsockListeners: []vmkit.VsockListener{{Port: 1032, Target: broker.ListenerTarget}},
	}
	set, err := startVsockListeners(opts, cfg)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	defer set.Close()

	req, err := http.NewRequest(http.MethodGet, "http://broker/v1/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer @secret:other")
	resp, err := brokerTestClient(opts, 1032).Do(req)
	if err != nil {
		t.Fatalf("broker request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (unknown reference fails closed)", resp.StatusCode)
	}
	if sawUpstream {
		t.Fatal("request with unresolved reference must not reach upstream")
	}
}

func TestStartVsockListenersBrokerUnresolvableSecretFailsClosed(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		Broker: &vmkit.BrokerConfig{
			Upstream: "https://api.example.com",
			Secret:   vmkit.SecretRef{Name: "api", Ref: "env:MA_BROKER_DEFINITELY_MISSING"},
		},
		VsockListeners: []vmkit.VsockListener{{Port: 1032, Target: broker.ListenerTarget}},
	}
	if _, err := startVsockListeners(opts, cfg); err == nil {
		t.Fatal("unresolvable broker secret must fail the start")
	}
	if _, err := os.Stat(firecrackerGuestVsockPath(opts, 1032)); !os.IsNotExist(err) {
		t.Fatalf("broker socket left behind after failed start: %v", err)
	}
}

func TestStartVsockListenersBrokerListenerWithoutConfigFails(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		VsockListeners: []vmkit.VsockListener{{Port: 1032, Target: broker.ListenerTarget}},
	}
	if _, err := startVsockListeners(opts, cfg); err == nil {
		t.Fatal("broker listener without broker config must fail the start")
	}
}
