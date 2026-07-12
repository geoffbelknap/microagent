//go:build linux

package firecracker

import (
	"bufio"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func connectStatus(t *testing.T, srvURL, target string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(srvURL, "http://"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT status: %v", err)
	}
	return status
}

func newTerm(t *testing.T) *broker.Terminate {
	t.Helper()
	term, err := broker.NewTerminate("https://api.example.com", func(string) (string, bool) { return "", false }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return term
}

// TestBrokerHandlerTerminateOnlyRejectsConnect: an endpoint with no proxy
// enabled is a base-URL/terminate broker only — it must answer CONNECT with 405,
// never tunnel.
func TestBrokerHandlerTerminateOnlyRejectsConnect(t *testing.T) {
	bc := &vmkit.BrokerConfig{Upstream: "https://api.example.com"} // Proxy: false
	srv := httptest.NewServer(brokerHandler(bc, newTerm(t), nil))
	defer srv.Close()
	if status := connectStatus(t, srv.URL, "example.com:443"); !strings.Contains(status, "405") {
		t.Fatalf("terminate-only CONNECT status = %q, want 405", status)
	}
}

// TestBrokerHandlerLockedAllowlistDeniesOffAllowlistAndInside: a proxy-enabled
// endpoint with a locked CONNECT allowlist must deny both an off-allowlist host
// and an inside/metadata destination.
func TestBrokerHandlerLockedAllowlistDeniesOffAllowlistAndInside(t *testing.T) {
	bc := &vmkit.BrokerConfig{
		Upstream:         "https://api.example.com",
		Proxy:            true,
		ConnectAllowlist: []string{"api.example.com"},
	}
	srv := httptest.NewServer(brokerHandler(bc, newTerm(t), nil))
	defer srv.Close()

	if status := connectStatus(t, srv.URL, "evil.example.com:443"); !strings.Contains(status, "403") {
		t.Fatalf("off-allowlist CONNECT status = %q, want 403", status)
	}
	// Inside/metadata is refused even if the guest tried to allowlist it by name
	// — the guarded dialer classifies the resolved IP. A literal makes the test
	// resolver-independent.
	if status := connectStatus(t, srv.URL, "169.254.169.254:80"); !strings.Contains(status, "403") {
		t.Fatalf("inside CONNECT status = %q, want 403", status)
	}
}
