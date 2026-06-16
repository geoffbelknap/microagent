package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunMediatesAcceptedConn(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() { c, err := up.Accept(); if err == nil { io.Copy(c, c); c.Close() } }()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Allow:   []string{"api.github.com"},
			Logger:  &BufferLogger{},
			OrigDst: func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
			Ready:   &strings.Builder{},
		})
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil { t.Fatalf("dial mediator: %v", err) }
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: api.github.com\r\n\r\n"))
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "GET / HTTP/1.1\r\n" {
		t.Fatalf("echo = %q err=%v", line, err)
	}
	conn.Close(); cancel(); <-done
}

func TestServeLoadsCA(t *testing.T) {
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
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Options{
			Allow: []string{"api.github.com"}, Passthrough: []string{"raw.example.com"},
			CACertPath: certPath, CAKeyPath: keyPath,
			Logger:  &BufferLogger{},
			OrigDst: func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
		})
	}()
	// connect once so we know it is serving, then shut down
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

func TestServeBadCAPathFailsClosed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	err := Serve(context.Background(), ln, Options{
		Allow: []string{"x.com"}, CACertPath: "/nonexistent/ca.pem", CAKeyPath: "/nonexistent/key.pem",
		Logger:  &BufferLogger{},
		OrigDst: func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("127.0.0.1:9"), nil },
	})
	if err == nil {
		t.Fatal("expected Serve to fail on missing CA files")
	}
}
