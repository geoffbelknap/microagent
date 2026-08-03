package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestPauseAndResumeDispatchControlCommands(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "agent-1",
		StateDir:       dir,
		Backend:        vmkit.BackendLinuxKVM,
		SupervisorPath: filepath.Join(dir, "no-such-supervisor"),
	}
	// With a missing supervisor binary, both calls fail at dispatch — but they
	// must get past Control's command whitelist, proving pause/resume are wired
	// through as supervisor commands rather than rejected as unsupported.
	if _, err := Pause(context.Background(), opts); err == nil || strings.Contains(err.Error(), "unsupported workspace control command") {
		t.Fatalf("Pause not wired to a pause control command: %v", err)
	}
	if _, err := Resume(context.Background(), opts); err == nil || strings.Contains(err.Error(), "unsupported workspace control command") {
		t.Fatalf("Resume not wired to a resume control command: %v", err)
	}
}

func TestCleanStopSyncsGuestBeforeDispatchAndRecordsOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell supervisor is POSIX-only")
	}
	for _, command := range []string{"halt", "stop"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			opts := Options{Name: "agent-1", StateDir: dir, Backend: HostBackend(), SupervisorPath: writeFakeControlSupervisor(t, dir, "running", filepath.Join(dir, "unused"))}
			req, err := Request(opts, "start", filepath.Join(dir, "rootfs.ext4"), "req-1")
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
				t.Fatal(err)
			}

			called := false
			previous := executeCleanStopSync
			executeCleanStopSync = func(ctx context.Context, _ Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
				called = true
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > CleanStopSyncTimeout {
					t.Fatalf("sync context deadline = %v, want bounded by %s", deadline, CleanStopSyncTimeout)
				}
				if len(req.Argv) != 1 || req.Argv[0] != "sync" || req.TimeoutMS != CleanStopSyncTimeout.Milliseconds() {
					t.Fatalf("sync request = %#v", req)
				}
				exitCode := 0
				result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
				result.ExitCode = &exitCode
				return result, nil
			}
			defer func() { executeCleanStopSync = previous }()

			if _, err := Control(context.Background(), opts, command); err != nil {
				t.Fatalf("Control(%s): %v", command, err)
			}
			if !called {
				t.Fatal("guest filesystem sync was not attempted")
			}
			events, err := ReadEvents(dir, "agent-1")
			if err != nil {
				t.Fatal(err)
			}
			if len(events) < 2 || !strings.Contains(events[len(events)-1].Detail, "sync completed") {
				t.Fatalf("events = %#v, want recorded sync completion", events)
			}
		})
	}
}

func TestCleanStopProceedsWhenGuestSyncFails(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent", StateDir: dir, Backend: HostBackend()}
	state := RuntimeState{Event: EventFile{Identity: vmkit.Identity{RuntimeID: "agent", Backend: HostBackend()}, State: vmkit.StateRunning}}
	if err := writeJSONFile(filepath.Join(dir, "agent", "runtime.json"), state); err != nil {
		t.Fatal(err)
	}
	previous := executeCleanStopSync
	executeCleanStopSync = func(context.Context, Options, execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
		return execprotocol.ExecResult{}, errors.New("guest unavailable")
	}
	defer func() { executeCleanStopSync = previous }()

	prepareCleanStop(context.Background(), opts)
	events, err := ReadEvents(dir, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !strings.Contains(events[0].Detail, "sync failed: guest unavailable") {
		t.Fatalf("events = %#v, want recorded bounded sync failure", events)
	}
}

func TestDeleteBlockedByStateOnlyBlocksLiveStates(t *testing.T) {
	want := map[vmkit.VMState]bool{
		vmkit.StateRunning:     true,
		vmkit.StateStarting:    true,
		vmkit.StatePaused:      true,
		vmkit.StateStopped:     false,
		vmkit.StateHalted:      false,
		vmkit.StateFailed:      false,
		vmkit.StateStopping:    false,
		vmkit.StateQuarantined: false,
		vmkit.StatePrepared:    false,
		vmkit.StateUnknown:     false,
	}
	for state, blocked := range want {
		if got := deleteBlockedByState(state); got != blocked {
			t.Errorf("deleteBlockedByState(%s) = %v, want %v", state, got, blocked)
		}
	}
}

// writeFakeControlSupervisor writes an executable stub supervisor that answers
// inspect with the given state and records each delete into deleteLog. It lets a
// test drive the shared control-layer delete guard without a real backend.
func writeFakeControlSupervisor(t *testing.T, dir, inspectState, deleteLog string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-supervisor")
	backend := HostBackend()
	event := func(state string) string {
		return `{"ok":true,"backend":"` + backend + `","event":{"identity":{"requestID":"r","runtimeID":"agent-1","role":"workload","backend":"` + backend + `"},"state":"` + state + `","observedAt":"2026-01-01T00:00:00Z"}}`
	}
	body := "#!/bin/sh\nreq=$(cat)\ncase \"$req\" in\n" +
		"  *'\"command\":\"inspect\"'*) printf '%s' '" + event(inspectState) + "' ;;\n" +
		"  *'\"command\":\"delete\"'*) printf x >> " + shellQuoteForTest(deleteLog) + "; printf '%s' '" + event("stopped") + "' ;;\n" +
		"  *) printf '%s' '{\"ok\":true,\"backend\":\"linux-kvm\"}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestControlDeleteRefusesLiveWorkspaceBeforeDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell supervisor is POSIX-only")
	}
	dir := t.TempDir()
	deleteLog := filepath.Join(dir, "delete.log")

	// A live workspace: delete is refused and never reaches the supervisor. Use
	// the host backend (both linux-kvm and apple-vf route delete through the same
	// shared control guard) so the fake supervisor passes ValidateHostBackend.
	opts := Options{
		Name:           "agent-1",
		StateDir:       dir,
		Backend:        HostBackend(),
		SupervisorPath: writeFakeControlSupervisor(t, dir, "running", deleteLog),
	}
	resp, err := Control(context.Background(), opts, "delete")
	if err == nil || !strings.Contains(err.Error(), "stop or kill it before delete") {
		t.Fatalf("delete of running workspace err=%v resp=%#v, want refusal", err, resp)
	}
	if _, statErr := os.Stat(deleteLog); !os.IsNotExist(statErr) {
		t.Fatalf("supervisor delete was dispatched for a running workspace")
	}

	// A stopped workspace: delete proceeds to the supervisor.
	opts.SupervisorPath = writeFakeControlSupervisor(t, dir, "stopped", deleteLog)
	if resp, err := Control(context.Background(), opts, "delete"); err != nil || !resp.OK {
		t.Fatalf("delete of stopped workspace err=%v resp=%#v, want success", err, resp)
	}
	if _, statErr := os.Stat(deleteLog); statErr != nil {
		t.Fatalf("supervisor delete was not dispatched for a stopped workspace: %v", statErr)
	}
}

func TestDeleteNeedsStoppedRecognizesLiveStates(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"workspace agent-1 is running; stop or kill it before delete", true},
		{"workspace agent-1 is paused; stop or kill it before delete", true},
		{"workspace agent-1 is starting; stop or kill it before delete", true},
		{"firecracker workspace agent-1 is running; stop or kill it before delete", true},
		{"workspace agent-1 is quarantined; stop it before delete", false},
		{"workspace agent-1 not found", false},
		{"some unrelated failure", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := deleteNeedsStopped(errors.New(tc.text), vmkit.Response{}); got != tc.want {
			t.Errorf("deleteNeedsStopped(%q) = %v, want %v", tc.text, got, tc.want)
		}
		if got := deleteNeedsStopped(nil, vmkit.Response{Error: tc.text}); got != tc.want {
			t.Errorf("deleteNeedsStopped(resp=%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestPauseAndResumeUseDedicatedCapability(t *testing.T) {
	resp, err := unsupportedControlCapability(vmkit.BackendAppleVF, "pause")
	if err != nil || resp.Error != "" {
		t.Fatalf("Apple VF pause err=%v resp=%#v, want supported", err, resp)
	}
	resp, err = unsupportedControlCapability(vmkit.BackendAppleVF, "resume")
	if err != nil || resp.Error != "" {
		t.Fatalf("Apple VF resume err=%v resp=%#v, want supported", err, resp)
	}
	if resp, err := unsupportedControlCapability(vmkit.BackendLinuxKVM, "pause"); err != nil || resp.Error != "" {
		t.Fatalf("Linux pause capability err=%v resp=%#v, want supported", err, resp)
	}
}
