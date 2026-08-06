package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// sunPathMax is the tighter of the two platform sockaddr_un.sun_path limits
// (104 on darwin, 108 on linux). Holding every test socket under the smaller
// one keeps a path that binds on the linux runner binding on the macOS one too.
const sunPathMax = 104

// shortStateDir returns a state dir short enough that the vsock socket path
// derived from it (StateDir/Name/vsock.sock) still fits sunPathMax. t.TempDir
// bakes the test's own name into the path, which is what overran the limit on
// the macOS runner while linux stayed green on its extra four bytes. Cleanup
// matches t.TempDir's: the directory goes when the test does.
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ma")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startClockSyncVsockServer runs a fake Firecracker vsock multiplexer at the
// exact path clockSyncVsockClient derives from opts (StateDir/Name/vsock.sock):
// it accepts the "CONNECT <guestPort>\n" handshake dialClockSyncVsock sends,
// acks it, then hands the connection to handle. Mirrors
// pkg/supervisors/firecracker/snapshot_liveness_linux_test.go's
// startRestoreExecVsockServer, which tests the sibling fix (mw#592) this one
// extends to the clock-sync path.
func startClockSyncVsockServer(t *testing.T, opts Options, guestPort uint16, handle func(net.Conn)) func() {
	t.Helper()
	socketPath := filepath.Join(opts.StateDir, opts.Name, "vsock.sock")
	// sockaddr_un.sun_path holds 104 bytes on darwin and 108 on linux. Over that,
	// bind fails as an opaque "invalid argument" that names neither the limit nor
	// the path, so check first and say which it was. Callers keep the path short
	// with shortStateDir; this guard is what tells a future caller that it must.
	if len(socketPath) >= sunPathMax {
		t.Fatalf("vsock socket path is %d bytes, over the %d-byte sockaddr_un limit: %s", len(socketPath), sunPathMax, socketPath)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				want := []byte(fmt.Sprintf("CONNECT %d\n", guestPort))
				buf := make([]byte, len(want))
				if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != string(want) {
					return
				}
				if _, err := conn.Write([]byte("OK 0\n")); err != nil {
					return
				}
				handle(conn)
			}()
		}
	}()
	return func() {
		_ = ln.Close()
		<-done
	}
}

func stubExecReady(t *testing.T) {
	t.Helper()
	saved := execReadinessProbe
	t.Cleanup(func() { execReadinessProbe = saved })
	execReadinessProbe = func(context.Context, RuntimeState, time.Duration) (vmkit.ReadinessSignal, bool) {
		return vmkit.ReadinessSignal{Ready: true}, true
	}
}

func stubClockSyncNow(t *testing.T, epoch int64) {
	t.Helper()
	saved := clockSyncNow
	t.Cleanup(func() { clockSyncNow = saved })
	clockSyncNow = func() time.Time { return time.Unix(epoch, 0) }
}

func readEventDetails(t *testing.T, opts Options) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "events.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read events.json: %v", err)
	}
	var events []EventFile
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatalf("decode events.json: %v", err)
	}
	details := make([]string, 0, len(events))
	for _, event := range events {
		details = append(details, event.Detail)
	}
	return details
}

func TestSyncGuestClockAfterResumeSendsHostEpoch(t *testing.T) {
	stubExecReady(t)
	stubClockSyncNow(t, 1785600000)

	// The state-write helper probes readiness against the same server, so
	// capture every request and pick out the settime one.
	requests := make(chan execprotocol.ExecRequest, 8)
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		if err := execprotocol.DecodeMessage(conn, &req); err != nil {
			return
		}
		select {
		case requests <- req:
		default:
		}
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stop()

	opts := writeExecRuntimeState(t, vmkit.BackendLinuxKVM, vmkit.StateRunning, port)
	syncGuestClockAfterResume(context.Background(), opts)

	want := []string{"date", "-u", "-s", "@1785600000"}
	var got []string
collect:
	for {
		select {
		case req := <-requests:
			if len(req.Argv) > 0 && req.Argv[0] == "date" {
				got = req.Argv
				break collect
			}
		default:
			break collect
		}
	}
	if len(got) != len(want) {
		t.Fatalf("settime argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("settime argv = %v, want %v", got, want)
		}
	}

	details := readEventDetails(t, opts)
	last := details[len(details)-1]
	if !strings.Contains(last, "guest clock synced") || !strings.Contains(last, "1785600000") {
		t.Fatalf("last event detail = %q, want synced with epoch", last)
	}
}

func TestSyncGuestClockAfterResumeRecordsFailure(t *testing.T) {
	stubExecReady(t)
	stubClockSyncNow(t, 1785600000)

	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		_ = execprotocol.DecodeMessage(conn, &req)
		code := 1
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stop()

	opts := writeExecRuntimeState(t, vmkit.BackendLinuxKVM, vmkit.StateRunning, port)
	syncGuestClockAfterResume(context.Background(), opts)

	details := readEventDetails(t, opts)
	last := details[len(details)-1]
	if !strings.Contains(last, "guest clock sync failed") {
		t.Fatalf("last event detail = %q, want failure record", last)
	}
}

func TestSyncGuestClockAfterResumeUnreachableIsBestEffort(t *testing.T) {
	stubExecReady(t)
	stubClockSyncNow(t, 1785600000)

	port := unusedTCPPort(t)
	opts := writeExecRuntimeState(t, vmkit.BackendLinuxKVM, vmkit.StateRunning, port)
	// Must return promptly and leave a skipped record, never an error or panic.
	syncGuestClockAfterResume(context.Background(), opts)

	details := readEventDetails(t, opts)
	last := details[len(details)-1]
	if !strings.Contains(last, "guest clock sync skipped") {
		t.Fatalf("last event detail = %q, want skipped record", last)
	}
}

func TestSyncGuestClockAfterResumeNoExecPortIsSilent(t *testing.T) {
	stubExecReady(t)
	// Request derives a hashed default exec port, so a truly port-less
	// runtime state has to be written directly.
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	runtimeState := RuntimeState{
		Event: EventFile{
			Identity: vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendLinuxKVM},
			State:    vmkit.StateRunning,
		},
		Config: vmkit.Config{StateDir: dir},
	}
	if err := writeJSONFile(filepath.Join(dir, opts.Name, "runtime.json"), runtimeState); err != nil {
		t.Fatalf("write runtime.json: %v", err)
	}
	syncGuestClockAfterResume(context.Background(), opts)
	if details := readEventDetails(t, opts); len(details) != 0 {
		t.Fatalf("events appended without an exec port: %v", details)
	}
}

// TestSyncGuestClockAfterResumeUsesVsockOnLinuxKVM proves the fix actually
// reaches the guest over vsock, not merely that a TCP-based fallback exists.
// The exec port handed to writeExecRuntimeState is a real listener that
// closes immediately -- it is never used to actually connect (it only exists
// so Request has a nonzero ExecPort to persist) -- so if the vsock attempt
// were skipped or fell through to the TCP path for any reason, this would
// fail the same way TestSyncGuestClockAfterResumeUnreachableIsBestEffort
// does (a "skipped" record, not a "synced" one), not silently pass.
func TestSyncGuestClockAfterResumeUsesVsockOnLinuxKVM(t *testing.T) {
	stubClockSyncNow(t, 1785600000)

	deadTCPPort := unusedTCPPort(t)
	// This test binds a real unix socket under the state dir, so it needs a dir
	// short enough for sockaddr_un -- t.TempDir's is not (see shortStateDir).
	opts := writeExecRuntimeStateIn(t, shortStateDir(t), vmkit.BackendLinuxKVM, vmkit.StateRunning, deadTCPPort)

	requests := make(chan execprotocol.ExecRequest, 8)
	stopVsock := startClockSyncVsockServer(t, opts, deadTCPPort, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		if err := execprotocol.DecodeMessage(conn, &req); err != nil {
			return
		}
		select {
		case requests <- req:
		default:
		}
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stopVsock()

	syncGuestClockAfterResume(context.Background(), opts)

	var got []string
collect:
	for {
		select {
		case req := <-requests:
			if len(req.Argv) > 0 && req.Argv[0] == "date" {
				got = req.Argv
				break collect
			}
		default:
			break collect
		}
	}
	want := []string{"date", "-u", "-s", "@1785600000"}
	if len(got) != len(want) {
		t.Fatalf("settime argv over vsock = %v, want %v (never reached the fake vsock server)", got, want)
	}

	details := readEventDetails(t, opts)
	last := details[len(details)-1]
	if !strings.Contains(last, "guest clock synced") {
		t.Fatalf("last event detail = %q, want synced (reached over vsock, not the dead TCP port)", last)
	}
}

// TestSyncGuestClockAfterResumeFallsBackWithoutVsockSocket proves linux-kvm
// does not regress below today's behavior when the vsock socket genuinely
// does not exist (e.g. an unexpected environment): it falls back to the
// TCP-forward poll rather than hanging or silently failing.
func TestSyncGuestClockAfterResumeFallsBackWithoutVsockSocket(t *testing.T) {
	stubExecReady(t)
	stubClockSyncNow(t, 1785600000)

	requests := make(chan execprotocol.ExecRequest, 8)
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		if err := execprotocol.DecodeMessage(conn, &req); err != nil {
			return
		}
		select {
		case requests <- req:
		default:
		}
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stop()

	// No vsock.sock is ever created at this opts' derived path.
	opts := writeExecRuntimeState(t, vmkit.BackendLinuxKVM, vmkit.StateRunning, port)
	syncGuestClockAfterResume(context.Background(), opts)

	details := readEventDetails(t, opts)
	last := details[len(details)-1]
	if !strings.Contains(last, "guest clock synced") {
		t.Fatalf("last event detail = %q, want synced via the TCP fallback", last)
	}
}
