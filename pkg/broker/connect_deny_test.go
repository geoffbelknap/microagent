package broker

import (
	"bufio"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slices.Contains without the import churn.
func hasSignal(sigs []string, want string) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}

// speakConnect opens a raw CONNECT to a broker httptest server and returns the
// status line.
func speakConnect(t *testing.T, brokerURL, target string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(brokerURL, "http://"), 5*time.Second)
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

// TestHandlerRejectsConnectWhenTunnelDisabled locks the default-off contract: a
// terminate-only endpoint (nil Connect) refuses CONNECT with 405, so a base-URL
// broker is never an open tunnel.
func TestHandlerRejectsConnectWhenTunnelDisabled(t *testing.T) {
	term, err := NewTerminate("https://api.example.com", resolver(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(term, nil))
	defer srv.Close()
	if status := speakConnect(t, srv.URL, "example.com:443"); !strings.Contains(status, "405") {
		t.Fatalf("CONNECT status = %q, want 405", status)
	}
}

// TestConnectPolicyDenyEmitsDeniedSignal: a tunnel refused by policy is a
// governance denial — 403 plus a "denied" signal on the decision record so a
// downstream consumer can halt/quarantine.
func TestConnectPolicyDenyEmitsDeniedSignal(t *testing.T) {
	var rec DecisionRecord
	c := &Connect{
		Policy:     func(TapRecord) (Verdict, error) { return Verdict{Allow: false, Rule: "allowlist"}, nil },
		OnDecision: func(r DecisionRecord) { rec = r },
	}
	srv := httptest.NewServer(Handler(nil, c))
	defer srv.Close()
	if status := speakConnect(t, srv.URL, "blocked.example.com:443"); !strings.Contains(status, "403") {
		t.Fatalf("CONNECT status = %q, want 403", status)
	}
	if rec.Verdict != "deny" || !hasSignal(rec.Signals, SignalDenied) {
		t.Fatalf("decision = %+v, want deny with %q signal", rec, SignalDenied)
	}
}

// TestConnectDialerDenyEmitsDeniedSignal: when the injected dialer refuses with
// ErrTunnelDenied (an inside destination), the tunnel is denied 403 with a
// "denied" signal — distinct from an ordinary upstream failure.
func TestConnectDialerDenyEmitsDeniedSignal(t *testing.T) {
	var rec DecisionRecord
	c := &Connect{
		Dial:       GuardedDialer{IsInside: testInside}.Dial,
		OnDecision: func(r DecisionRecord) { rec = r },
	}
	srv := httptest.NewServer(Handler(nil, c))
	defer srv.Close()
	if status := speakConnect(t, srv.URL, "169.254.169.254:80"); !strings.Contains(status, "403") {
		t.Fatalf("CONNECT status = %q, want 403", status)
	}
	if rec.Verdict != "deny" || !hasSignal(rec.Signals, SignalDenied) {
		t.Fatalf("decision = %+v, want deny with %q signal", rec, SignalDenied)
	}
}

// TestAllowlistPolicyAllowsListedDeniesRest: the allowlist helper permits a
// listed host (matched by name even when the CONNECT target carries a port) and
// denies everything else; an empty allowlist is no policy at all (nil).
func TestAllowlistPolicyAllowsListedDeniesRest(t *testing.T) {
	if AllowlistPolicy(nil) != nil {
		t.Fatal("empty allowlist must yield a nil policy (no restriction)")
	}
	p := AllowlistPolicy([]string{"api.example.com"})
	v, err := p(TapRecord{Host: "api.example.com:443"})
	if err != nil || !v.Allow {
		t.Fatalf("listed host: verdict=%+v err=%v, want allow", v, err)
	}
	v, err = p(TapRecord{Host: "evil.example.com:443"})
	if err != nil || v.Allow {
		t.Fatalf("unlisted host: verdict=%+v err=%v, want deny", v, err)
	}
}
