package workspace

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

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
