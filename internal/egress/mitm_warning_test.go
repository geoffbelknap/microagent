package egress

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// serveOnceCollectingAudit starts a mediator with the given mode, connects once
// so it is serving, then cancels and returns the audit events it logged.
func serveOnceCollectingAudit(t *testing.T, mode string) []map[string]any {
	t.Helper()
	log := &BufferLogger{}
	opts := Options{
		Mode:      mode,
		Allow:     []string{"api.github.com"},
		Logger:    log,
		OrigDst:   func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
		UDPListen: plainUDPListen(t),
	}
	// mitm forges certificates, so it needs a CA loaded.
	if mode == egressModeMITM {
		ca, err := NewCA("test-ca", time.Hour)
		if err != nil {
			t.Fatalf("NewCA: %v", err)
		}
		dir := t.TempDir()
		certPath := filepath.Join(dir, "ca.pem")
		keyPath := filepath.Join(dir, "ca-key.pem")
		keyPEM, _ := ca.KeyPEM()
		if err := os.WriteFile(certPath, ca.CertPEM(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		opts.CACertPath, opts.CAKeyPath = certPath, keyPath
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, opts) }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	return log.Snapshot()
}

// TestMITMModeEmitsEnabledWarning: enabling the mitm mode is never silent — the
// mediator logs an egress_mitm_enabled audit record at load time.
func TestMITMModeEmitsEnabledWarning(t *testing.T) {
	events := serveOnceCollectingAudit(t, egressModeMITM)
	found := false
	for _, e := range events {
		if e["event"] == "egress_mitm_enabled" {
			found = true
			if w, _ := e["warning"].(string); w == "" {
				t.Errorf("egress_mitm_enabled carries no warning text: %v", e)
			}
		}
	}
	if !found {
		t.Fatalf("mitm mode did not emit egress_mitm_enabled: %v", events)
	}
}

// TestBrokerModeDoesNotWarn: the default broker mode forges nothing, so it emits
// no mitm warning.
func TestBrokerModeDoesNotWarn(t *testing.T) {
	events := serveOnceCollectingAudit(t, egressModeBroker)
	for _, e := range events {
		if e["event"] == "egress_mitm_enabled" {
			t.Fatalf("broker mode emitted a mitm warning: %v", e)
		}
	}
}
