//go:build linux

package firecracker

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDialGuestVsockUsesFirecrackerConnectHandshake(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err.Error()
			return
		}
		defer conn.Close()
		buf := make([]byte, len("CONNECT 8080\n"))
		if _, err := io.ReadFull(conn, buf); err != nil {
			done <- err.Error()
			return
		}
		if _, err := conn.Write([]byte("OK 1234\npayload")); err != nil {
			done <- err.Error()
			return
		}
		done <- string(buf)
	}()
	conn, reader, err := dialGuestVsock(socketPath, 8080)
	if err != nil {
		t.Fatalf("dialGuestVsock: %v", err)
	}
	defer conn.Close()
	if got := <-done; got != "CONNECT 8080\n" {
		t.Fatalf("handshake = %q", got)
	}
	payload := make([]byte, len("payload"))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q", payload)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatal(err)
	}
}
