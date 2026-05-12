//go:build windows

package windows_hyperv

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestWindowsHyperVVsockTargetValidationAllowsTCPAndResultOnly(t *testing.T) {
	stateDir := t.TempDir()
	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-1"},
		Config:   &vmkit.Config{StateDir: stateDir},
	}

	if !isAllowedHVSockTarget(req, filepath.Join(stateDir, "agent-1", "result.json")) {
		t.Fatalf("result target rejected")
	}
	if !isAllowedHVSockTarget(req, "127.0.0.1:9900") {
		t.Fatalf("tcp target rejected")
	}
	if isAllowedHVSockTarget(req, filepath.Join(stateDir, "agent-1", "not-result.json")) {
		t.Fatalf("non-result file target accepted")
	}
}

func TestHandleHVSockConnectionWritesResultAtomically(t *testing.T) {
	target := filepath.Join(t.TempDir(), "agent-1", "result.json")
	hostConn, guestConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleHVSockConnection(hostConn, target)
		close(done)
	}()

	if _, err := guestConn.Write([]byte(`{"exitCode":0}`)); err != nil {
		t.Fatalf("write guest result: %v", err)
	}
	if err := guestConn.Close(); err != nil {
		t.Fatalf("close guest pipe: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("result handler did not finish")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data) != `{"exitCode":0}` {
		t.Fatalf("result data = %q", data)
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary result remains: %v", err)
	}
}

func TestHandleHVSockConnectionProxiesTCP(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp target: %v", err)
	}
	defer tcpListener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := tcpListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		_, _ = conn.Write([]byte("pong"))
	}()

	hostConn, guestConn := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		handleHVSockConnection(hostConn, tcpListener.Addr().String())
		close(handlerDone)
	}()
	if _, err := guestConn.Write([]byte("ping")); err != nil {
		t.Fatalf("write guest tcp payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(guestConn, buf); err != nil {
		t.Fatalf("read proxied response: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("proxied response = %q", buf)
	}
	if err := guestConn.Close(); err != nil {
		t.Fatalf("close guest pipe: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tcp proxy handler did not finish")
	}
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tcp target did not finish")
	}
}

func TestServePublishedPortForwardProxiesTCPToHostForwardHVSockPort(t *testing.T) {
	oldDial := dialHVSockPortHook
	t.Cleanup(func() { dialHVSockPortHook = oldDial })
	guestHostConn, guestVMConn := net.Pipe()
	dialedPort := uint32(0)
	dialHVSockPortHook = func(ctx context.Context, vmID guid.GUID, port uint32) (net.Conn, error) {
		dialedPort = port
		return guestHostConn, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()
	var vmID guid.GUID
	go servePublishedPortForward(listener, vmID, vmkit.PortForward{HostPort: 18080, GuestPort: 8080})

	guestDone := make(chan struct{})
	go func() {
		defer close(guestDone)
		defer guestVMConn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(guestVMConn, buf); err != nil {
			return
		}
		_, _ = guestVMConn.Write([]byte("pong"))
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial published listener: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write client payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read client response: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("client response = %q", buf)
	}
	if dialedPort != 18080 {
		t.Fatalf("dialed hvsock port = %d, want host forward port 18080", dialedPort)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	select {
	case <-guestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("guest hvsock side did not finish")
	}
}
