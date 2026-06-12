//go:build windows

package windows_hyperv

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

func TestStartRuntimeListenersServesExecBridge(t *testing.T) {
	oldDial := dialHVSockPortHook
	t.Cleanup(func() { dialHVSockPortHook = oldDial })
	guestHostConn, guestVMConn := net.Pipe()
	dialed := make(chan uint32, 1)
	dialHVSockPortHook = func(ctx context.Context, vmID guid.GUID, port uint32) (net.Conn, error) {
		dialed <- port
		return guestHostConn, nil
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	execPort := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-1"},
		Config: &vmkit.Config{
			StateDir: t.TempDir(),
			ExecPort: execPort,
		},
	}
	handle := computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}
	set, err := startRuntimeListeners(context.Background(), handle, req)
	if err != nil {
		t.Fatalf("startRuntimeListeners: %v", err)
	}
	if set == nil {
		t.Fatal("startRuntimeListeners returned no listener set for an exec bridge")
	}
	t.Cleanup(func() { _ = set.Close() })

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

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(execPort))))
	if err != nil {
		t.Fatalf("dial exec bridge: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write exec payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read exec response: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("exec response = %q", buf)
	}
	select {
	case port := <-dialed:
		if port != uint32(execPort) {
			t.Fatalf("dialed guest exec hvsock port = %d, want %d", port, execPort)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec bridge did not dial the guest")
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

func TestStartRuntimeListenersExecBridgeDialsGuestExecPort(t *testing.T) {
	oldDial := dialHVSockPortHook
	t.Cleanup(func() { dialHVSockPortHook = oldDial })
	dialed := make(chan uint32, 1)
	dialHVSockPortHook = func(ctx context.Context, vmID guid.GUID, port uint32) (net.Conn, error) {
		dialed <- port
		host, vm := net.Pipe()
		_ = vm.Close()
		return host, nil
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	execPort := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-1"},
		Config: &vmkit.Config{
			StateDir:      t.TempDir(),
			ExecPort:      execPort,
			GuestExecPort: 42001,
		},
	}
	handle := computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}
	set, err := startRuntimeListeners(context.Background(), handle, req)
	if err != nil {
		t.Fatalf("startRuntimeListeners: %v", err)
	}
	if set == nil {
		t.Fatal("startRuntimeListeners returned no listener set for an exec bridge")
	}
	t.Cleanup(func() { _ = set.Close() })

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(execPort))))
	if err != nil {
		t.Fatalf("dial exec bridge: %v", err)
	}
	defer client.Close()
	select {
	case port := <-dialed:
		if port != 42001 {
			t.Fatalf("dialed guest exec hvsock port = %d, want guest exec port 42001", port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec bridge did not dial the guest")
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
