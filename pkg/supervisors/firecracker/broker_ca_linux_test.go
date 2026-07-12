//go:build linux

package firecracker

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// writeCAFile PEM-encodes an httptest TLS server's leaf certificate to a temp
// file, standing in for an operator-supplied upstream CA bundle without hand
// rolling certificate generation.
func writeCAFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	path := filepath.Join(t.TempDir(), "upstream-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUpstreamClientWithCATrustsTheGivenCA(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "trusted")
	}))
	defer upstream.Close()
	caFile := writeCAFile(t, upstream)

	client, err := upstreamClientWithCA(caFile)
	if err != nil {
		t.Fatalf("upstreamClientWithCA: %v", err)
	}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("request with CA-trusting client: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "trusted" {
		t.Fatalf("body = %q, want %q", body, "trusted")
	}

	// The system-default client must NOT trust this self-signed cert — proves
	// the trust came from the CA file, not from ambient system roots.
	if _, err := http.DefaultClient.Get(upstream.URL); err == nil {
		t.Fatal("http.DefaultClient unexpectedly trusted the self-signed upstream cert")
	}
}

func TestUpstreamClientWithCANonexistentPathFailsClosed(t *testing.T) {
	if _, err := upstreamClientWithCA(filepath.Join(t.TempDir(), "does-not-exist.pem")); err == nil {
		t.Fatal("upstreamClientWithCA accepted a nonexistent path")
	}
}

func TestUpstreamClientWithCAGarbageFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := upstreamClientWithCA(path); err == nil {
		t.Fatal("upstreamClientWithCA accepted a file with no valid PEM certificate")
	}
}

// TestStartBrokerListenerConsumesUpstreamCAFile is the end-to-end proof: a
// broker endpoint whose upstream presents a private (self-signed) cert only
// succeeds when its UpstreamCAFile names a bundle that trusts it. Without it,
// the terminate client's default transport rejects the upstream TLS and the
// request fails at the broker (502), never silently falling back to system
// roots.
func TestStartBrokerListenerConsumesUpstreamCAFile(t *testing.T) {
	const live = "live-broker-secret-ca-7a1c"
	t.Setenv("MA_BROKER_CA_TOK", live)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+live {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()
	caFile := writeCAFile(t, upstream)

	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		Brokers: []*vmkit.BrokerConfig{{
			Upstream:       upstream.URL,
			Secret:         vmkit.SecretRef{Name: "api", Ref: "env:MA_BROKER_CA_TOK"},
			VsockPort:      1032,
			UpstreamCAFile: caFile,
		}},
		VsockListeners: []vmkit.VsockListener{{Port: 1032, Target: broker.ListenerTarget}},
	}
	set, err := startVsockListeners(opts, cfg)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	defer set.Close()

	req, _ := http.NewRequest(http.MethodGet, "http://broker/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer @secret:api")
	resp, err := brokerTestClient(opts, 1032).Do(req)
	if err != nil {
		t.Fatalf("broker request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "upstream-ok" {
		t.Fatalf("status %d body %q — broker did not trust the upstream CA", resp.StatusCode, body)
	}
}

// TestStartBrokerListenerUnreadableUpstreamCAFileFailsRequest asserts a
// broker endpoint whose UpstreamCAFile does not exist fails the listener
// start rather than booting with a client that silently trusts system roots.
func TestStartBrokerListenerUnreadableUpstreamCAFileFailsRequest(t *testing.T) {
	t.Setenv("MA_BROKER_CA_TOK2", "unused")
	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		Brokers: []*vmkit.BrokerConfig{{
			Upstream:       "https://api.example.com",
			Secret:         vmkit.SecretRef{Name: "api", Ref: "env:MA_BROKER_CA_TOK2"},
			VsockPort:      1032,
			UpstreamCAFile: filepath.Join(dir, "missing-ca.pem"),
		}},
		VsockListeners: []vmkit.VsockListener{{Port: 1032, Target: broker.ListenerTarget}},
	}
	if _, err := startVsockListeners(opts, cfg); err == nil {
		t.Fatal("unreadable UpstreamCAFile must fail the listener start")
	}
	if _, err := os.Stat(firecrackerGuestVsockPath(opts, 1032)); !os.IsNotExist(err) {
		t.Fatalf("broker socket left behind after failed start: %v", err)
	}
}
