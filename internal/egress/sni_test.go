package egress

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestParseClientHelloSNI(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		_ = tls.Client(c, &tls.Config{ServerName: "api.github.com", InsecureSkipVerify: true}).Handshake()
		_ = c.Close()
	}()
	_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := s.Read(buf)
	if err != nil {
		t.Fatalf("read ClientHello: %v", err)
	}
	sni, ok := parseClientHelloSNI(buf[:n])
	if !ok || sni != "api.github.com" {
		t.Fatalf("parseClientHelloSNI = %q,%v; want api.github.com,true", sni, ok)
	}
}

func TestParseClientHelloSNIRejectsNonHandshake(t *testing.T) {
	if _, ok := parseClientHelloSNI([]byte{0x47, 0x45, 0x54, 0x20}); ok { // "GET "
		t.Fatal("expected ok=false for non-TLS bytes")
	}
}
