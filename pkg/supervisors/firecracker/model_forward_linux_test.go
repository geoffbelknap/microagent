//go:build linux

package firecracker

import (
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/modelrunner"
)

// startEchoServer runs a TCP server for the duration of the test that echoes
// back a fixed tag on every connection, so a test can tell which server a
// forwarded connection actually reached.
func startEchoServer(t *testing.T, tag string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = conn.Write([]byte(tag))
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

// TestHandleGuestVsockConnectionResolvesCurrentModelRunner is the regression
// test for the fix: a guest connection forwarded with a stale static target
// (as if captured at workspace start) must still reach the CURRENT runner
// for modelRef, because a runner restart changes its port. Before the fix,
// handleGuestVsockConnection dialed the frozen target unconditionally and
// the guest would reach the old (by now closed) runner or nothing at all.
func TestHandleGuestVsockConnectionResolvesCurrentModelRunner(t *testing.T) {
	dir := t.TempDir()
	const ref = "hf.co/o/r@main/m.gguf"

	// The "old" runner the static target still points at - it must NOT be
	// the one the guest reaches once the index records a different one.
	staleHost, stalePort := startEchoServer(t, "stale")
	staleTarget := net.JoinHostPort(staleHost, strconv.Itoa(stalePort))

	// The "current" runner, recorded in the index as the live one for ref.
	currentHost, currentPort := startEchoServer(t, "current")
	idx := modelrunner.Index{Runners: []modelrunner.Record{
		{Key: "r", ModelRef: ref, Host: currentHost, Port: currentPort, PID: os.Getpid()},
	}}
	if err := modelrunner.WriteIndex(dir, idx); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}

	guestSide, hostSide := net.Pipe()
	defer guestSide.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleGuestVsockConnection(hostSide, staleTarget, ref, dir)
	}()

	if err := guestSide.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	got, err := io.ReadAll(guestSide)
	if err != nil {
		t.Fatalf("read forwarded connection: %v", err)
	}
	if string(got) != "current" {
		t.Fatalf("forwarded connection reached %q, want %q (the runner recorded in the index, not the stale static target)", got, "current")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleGuestVsockConnection did not return")
	}
}

// TestHandleGuestVsockConnectionFallsBackWhenNoRunnerRecorded covers a
// listener with no modelRef (the ordinary vsock-forward case, e.g. a
// user --publish mapping): resolution must be skipped entirely and the
// static target dialed as before.
func TestHandleGuestVsockConnectionFallsBackWhenNoRunnerRecorded(t *testing.T) {
	dir := t.TempDir()
	host, port := startEchoServer(t, "static")
	target := net.JoinHostPort(host, strconv.Itoa(port))

	guestSide, hostSide := net.Pipe()
	defer guestSide.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleGuestVsockConnection(hostSide, target, "", dir)
	}()

	if err := guestSide.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	got, err := io.ReadAll(guestSide)
	if err != nil {
		t.Fatalf("read forwarded connection: %v", err)
	}
	if string(got) != "static" {
		t.Fatalf("forwarded connection reached %q, want %q", got, "static")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleGuestVsockConnection did not return")
	}
}
