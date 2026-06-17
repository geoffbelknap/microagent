package egress

import (
	"bufio"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestHTTPHostHeader(t *testing.T) {
	req := "GET /x HTTP/1.1\r\nHost: api.github.com:443\r\nAccept: */*\r\n\r\n"
	if got := httpHostHeader([]byte(req)); got != "api.github.com" {
		t.Fatalf("httpHostHeader = %q, want api.github.com", got)
	}
	if got := httpHostHeader([]byte("GET / HTTP/1.1\r\n\r\n")); got != "" {
		t.Fatalf("httpHostHeader = %q, want empty", got)
	}
}

func TestSniffHostHTTPReturnsBeforeDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go client.Write([]byte("GET / HTTP/1.1\r\nHost: api.github.com\r\n\r\n"))
	br := bufio.NewReader(server)
	dst := netip.MustParseAddrPort("10.0.0.1:80")
	gotc := make(chan string, 1)
	go func() {
		host, _ := sniffHost(br, dst, server.SetReadDeadline, time.Now().Add(10*time.Second))
		gotc <- host
	}()
	select {
	case got := <-gotc:
		if got != "api.github.com" {
			t.Fatalf("sniffHost = %q, want api.github.com", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sniffHost blocked until the deadline for a short HTTP request (stall regression)")
	}
}
