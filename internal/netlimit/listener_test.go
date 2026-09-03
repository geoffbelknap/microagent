package netlimit

import (
	"net"
	"testing"
	"time"
)

func TestListenerRefusesExcessAndClosesActiveConnections(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := New(base, 2)
	accepted := make(chan net.Conn, 2)
	done := make(chan error, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			accepted <- conn
		}
	}()

	dial := func() net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", base.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	clients := []net.Conn{dial(), dial()}
	defer func() {
		for _, conn := range clients {
			_ = conn.Close()
		}
	}()
	for range clients {
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatal("listener did not accept connection within limit")
		}
	}

	excess := dial()
	defer func() { _ = excess.Close() }()
	_ = excess.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := excess.Read(make([]byte, 1)); err == nil {
		t.Fatal("excess connection remained open")
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	for _, client := range clients {
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := client.Read(make([]byte, 1)); err == nil {
			t.Fatal("active connection survived listener shutdown")
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not stop")
	}
}
