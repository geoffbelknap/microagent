package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
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
