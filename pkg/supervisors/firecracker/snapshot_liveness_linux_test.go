//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// startRestoreExecVsockServer runs a fake Firecracker vsock UDS for the
// duration of the test: it accepts the "CONNECT <port>\n" handshake
// dialGuestVsock sends, acks it, then serves the exec protocol over the same
// connection. Returns the socket path and the guest port to probe.
func startRestoreExecVsockServer(t *testing.T, exitCode int) (string, uint16) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	const guestPort = 8080
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				want := []byte(fmt.Sprintf("CONNECT %d\n", guestPort))
				buf := make([]byte, len(want))
				if _, err := io.ReadFull(conn, buf); err != nil {
					return
				}
				if string(buf) != string(want) {
					return
				}
				if _, err := conn.Write([]byte("OK 0\n")); err != nil {
					return
				}
				var req execprotocol.ExecRequest
				if err := execprotocol.DecodeMessage(conn, &req); err != nil {
					return
				}
				code := exitCode
				result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
				result.ExitCode = &code
				_ = execprotocol.EncodeMessage(conn, result)
			}()
		}
	}()
	return socketPath, guestPort
}

func TestWaitForRestoreLivenessDetectsProcessExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	serialPath := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(serialPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := waitForRestoreLiveness(context.Background(), cmd, serialPath, "", 0)
	if err == nil || !strings.Contains(err.Error(), "guest did not survive snapshot resume") {
		t.Fatalf("waitForRestoreLiveness = %v, want guest-did-not-survive error", err)
	}
}

func TestWaitForRestoreLivenessDetectsGuestHalt(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	serialPath := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(serialPath, []byte("Kernel panic - not syncing\nreboot: System halted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := waitForRestoreLiveness(context.Background(), cmd, serialPath, "", 0)
	if err == nil || !strings.Contains(err.Error(), "guest halted immediately after snapshot resume") {
		t.Fatalf("waitForRestoreLiveness = %v, want guest-halted error", err)
	}
}

func TestWaitForRestoreLivenessAcceptsExecReady(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	serialPath := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(serialPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath, guestPort := startRestoreExecVsockServer(t, 0)

	saved := restoreLivenessWait
	restoreLivenessWait = 4 * time.Second
	t.Cleanup(func() { restoreLivenessWait = saved })

	start := time.Now()
	err := waitForRestoreLiveness(context.Background(), cmd, serialPath, socketPath, guestPort)
	if err != nil {
		t.Fatalf("waitForRestoreLiveness = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed >= restoreLivenessWait {
		t.Fatalf("waitForRestoreLiveness took %s, want to return before the %s window elapses once exec answers", elapsed, restoreLivenessWait)
	}
}

// TestWaitForRestoreLivenessFailsClosedWhenExecNeverAnswers is the
// regression case for a guest that survives long enough for Firecracker to
// still be running when the window elapses - i.e. it dies (or never
// answers) strictly after the liveness window, not within it - but never
// gets its exec service up. A gate that treats "process still alive, no
// exec probe succeeded yet" as a pass at the deadline reports a dead-in-
// practice restore as running; this must fail instead.
func TestWaitForRestoreLivenessFailsClosedWhenExecNeverAnswers(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	serialPath := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(serialPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Nothing listens on this socket - the exec probe can never succeed.
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")

	saved := restoreLivenessWait
	restoreLivenessWait = 50 * time.Millisecond
	t.Cleanup(func() { restoreLivenessWait = saved })

	err := waitForRestoreLiveness(context.Background(), cmd, serialPath, socketPath, 8080)
	if err == nil || !strings.Contains(err.Error(), "guest liveness unverified") {
		t.Fatalf("waitForRestoreLiveness = %v, want guest-liveness-unverified error", err)
	}
}

// TestWaitForRestoreLivenessFailsWhenNoExecPortConfigured covers the case
// with no probe available at all: the gate cannot obtain positive proof of
// life, so it must not pass a restore it never verified.
func TestWaitForRestoreLivenessFailsWhenNoExecPortConfigured(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	serialPath := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(serialPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	saved := restoreLivenessWait
	restoreLivenessWait = 50 * time.Millisecond
	t.Cleanup(func() { restoreLivenessWait = saved })

	err := waitForRestoreLiveness(context.Background(), cmd, serialPath, "", 0)
	if err == nil || !strings.Contains(err.Error(), "guest liveness unverified") {
		t.Fatalf("waitForRestoreLiveness = %v, want guest-liveness-unverified error", err)
	}
}

// TestWaitForRestoreLivenessFailsClosedOnContextCancellation covers the
// third fail-open branch the timeout fix would otherwise leave behind: a
// canceled context must not be read as "no evidence of death."
func TestWaitForRestoreLivenessFailsClosedOnContextCancellation(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	serialPath := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(serialPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	saved := restoreLivenessWait
	restoreLivenessWait = 30 * time.Second
	t.Cleanup(func() { restoreLivenessWait = saved })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForRestoreLiveness(ctx, cmd, serialPath, "", 0)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("waitForRestoreLiveness = %v, want canceled error", err)
	}
}

func TestRestoreExecProbeRejectsNonZeroExit(t *testing.T) {
	socketPath, guestPort := startRestoreExecVsockServer(t, 7)
	if restoreExecProbe(context.Background(), socketPath, guestPort) {
		t.Fatal("restoreExecProbe = true, want false for non-zero exit")
	}
}

func TestRestoreExecProbeRejectsUnreachable(t *testing.T) {
	// Socket path with nothing listening.
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	if restoreExecProbe(context.Background(), socketPath, 8080) {
		t.Fatal("restoreExecProbe = true, want false when unreachable")
	}
}
