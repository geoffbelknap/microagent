package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestRunListListsWorkspaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	eventDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := vmkit.Event{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		State:      vmkit.StateStopped,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", RestartPolicy: "on-failure", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "list.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runList(context.Background(), []string{"--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"name": "research"`) || !strings.Contains(string(got), `"state": "stopped"`) || !strings.Contains(string(got), `"restart": "on-failure"`) {
		t.Fatalf("list output = %s", got)
	}
}

func TestRunListCanPrintHumanOutput(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "text")
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", RestartPolicy: "on-failure", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	eventDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := vmkit.Event{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		State:      vmkit.StateStopped,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "list.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runList(context.Background(), []string{"--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "NAME") || !strings.Contains(string(got), "research") || strings.Contains(string(got), `"workspaces"`) {
		t.Fatalf("list human output = %s", got)
	}
}

func TestRunListHidesTerminalRuntimeOnlyRecords(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "text")
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := vmkit.Event{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		State:      vmkit.StateStopped,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "list.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runList(context.Background(), []string{"--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "research") || !strings.Contains(string(got), "No workspaces.") {
		t.Fatalf("list human output = %s", got)
	}
}

func TestRunDispatchesLSAlias(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "text")
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	eventDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := vmkit.Event{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		State:      vmkit.StateStopped,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "ls.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"ls", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run ls: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "research") {
		t.Fatalf("ls output = %s", got)
	}
}

func TestRunDispatchesLogAlias(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "text")
	dir := t.TempDir()
	logDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "serial.log"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "log.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"log", "research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run log: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("log output = %q", got)
	}
}

func TestRunPSFiltersStoppedWorkspaces(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "text")
	dir := t.TempDir()
	writeTestEvent := func(name string, state vmkit.VMState) {
		t.Helper()
		eventDir := filepath.Join(dir, name)
		if err := os.MkdirAll(eventDir, 0o755); err != nil {
			t.Fatal(err)
		}
		event := vmkit.Event{
			Identity:   vmkit.Identity{RequestID: "req-" + name, RuntimeID: name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
			State:      state,
			ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTestEvent("live", vmkit.StateRunning)
	writeTestEvent("parked", vmkit.StateStopped)
	stdoutPath := filepath.Join(dir, "ps.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPS(context.Background(), []string{"--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPS: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "live") || strings.Contains(string(got), "parked") {
		t.Fatalf("ps output = %s", got)
	}
}

func TestRunLogsPrintsSerialLog(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "serial.log"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "logs.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runLogs(context.Background(), []string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("logs = %q", got)
	}
}

func TestWriteSerialTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serial.log")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	n, err := writeSerialTail(path, 3, out)
	if err != nil {
		t.Fatalf("writeSerialTail: %v", err)
	}
	if n != 3 {
		t.Fatalf("wrote %d bytes, want 3", n)
	}
	// A not-yet-created serial log reads as empty, not an error.
	missing, err := writeSerialTail(filepath.Join(dir, "nope.log"), 0, out)
	if err != nil || missing != 0 {
		t.Fatalf("missing file: n=%d err=%v", missing, err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "def" {
		t.Fatalf("tail = %q, want %q", got, "def")
	}
}

func TestFollowLogsExitsWhenNotRunning(t *testing.T) {
	dir := t.TempDir()
	name := "research"
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "serial.log"), []byte("boot output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- followLogs(context.Background(), dir, name, out) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followLogs: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("followLogs did not return for a workspace that is not running")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "boot output") {
		t.Fatalf("followLogs output missing serial content: %q", got)
	}
}

func TestRunEventsSnapshotAndFollow(t *testing.T) {
	dir := t.TempDir()
	name := "research"
	wsDir := filepath.Join(dir, name)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	eventsJSON := `[
	  {"identity":{},"state":"running","detail":"runtime is started","observedAt":"2026-06-01T00:00:01Z"},
	  {"identity":{},"state":"halted","detail":"clean shutdown","observedAt":"2026-06-01T00:00:09Z"}
	]`
	if err := os.WriteFile(filepath.Join(wsDir, "events.json"), []byte(eventsJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	events, err := workspace.ReadEvents(dir, name)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 2 || events[0].State != vmkit.StateRunning || events[1].State != vmkit.StateHalted {
		t.Fatalf("events = %#v", events)
	}

	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- followEvents(context.Background(), dir, name, nil, out) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followEvents: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("followEvents did not return for a terminal workspace")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "running") || !strings.Contains(string(got), "halted") {
		t.Fatalf("followEvents output = %q", got)
	}
}

func TestReadEventsMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	events, err := workspace.ReadEvents(dir, "absent")
	if err != nil || events != nil {
		t.Fatalf("missing events: events=%v err=%v", events, err)
	}
	wsDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "events.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadEvents(dir, "broken"); err == nil {
		t.Fatal("expected error for malformed events.json")
	}
}

// setTextOutputForTest forces human (non-structured) output and restores the
// global output state afterward, so a prior --json invocation in the same
// package cannot leak into outputStructured().
func setTextOutputForTest(t *testing.T) {
	t.Helper()
	prevFormat := outputFormat
	outputFormat = "text"
	t.Setenv("MICROAGENT_OUTPUT", "text")
	t.Cleanup(func() {
		outputFormat = prevFormat
	})
}

func runMainCapture(t *testing.T, args ...string) (stdout, stderr []byte, code int) {
	t.Helper()
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout")
	stderrPath := filepath.Join(dir, "stderr")
	outFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	code = runMain(t.Context(), args, outFile, errFile)
	if err := outFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errFile.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, err = os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err = os.ReadFile(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	return stdout, stderr, code
}

func TestRunEgressSnapshotHumanAndJSON(t *testing.T) {
	dir := t.TempDir()
	name := "research"
	wsDir := filepath.Join(dir, name)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"event":"egress_listen","ts":"2026-06-16T00:00:00Z","addr":"127.0.0.1:0"}` + "\n" +
		`{"event":"egress_allow","ts":"2026-06-16T00:00:01Z","host":"api.github.com","dst":"140.82.0.1:443"}` + "\n" +
		`{"event":"egress_deny","ts":"2026-06-16T00:00:02Z","host":"evil.example","reason":"not allowlisted"}` + "\n"
	if err := os.WriteFile(filepath.Join(wsDir, "egress-access.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// Human output: one line per decision.
	t.Run("human", func(t *testing.T) {
		setTextOutputForTest(t)
		outPath := filepath.Join(dir, "egress-human.txt")
		out, err := os.Create(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := runEgress(context.Background(), []string{name, "--state-dir", dir}, out); err != nil {
			t.Fatalf("runEgress human: %v", err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		text := string(got)
		if !strings.Contains(text, "egress_allow") || !strings.Contains(text, "api.github.com") ||
			!strings.Contains(text, "egress_deny") || !strings.Contains(text, "not allowlisted") {
			t.Fatalf("human output = %q", text)
		}
		if lines := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1; lines != 3 {
			t.Fatalf("expected 3 decision lines, got %d: %q", lines, text)
		}
	})

	// Structured JSON via the global --json dispatch path.
	t.Run("json", func(t *testing.T) {
		outPath := filepath.Join(dir, "egress-json.txt")
		out, err := os.Create(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), []string{"--json", "egress", name, "--state-dir", dir}, out); err != nil {
			t.Fatalf("run --json egress: %v", err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Workspace string `json:"workspace"`
			Egress    []struct {
				Event  string `json:"event"`
				Host   string `json:"host"`
				Reason string `json:"reason"`
			} `json:"egress"`
		}
		if err := json.Unmarshal(got, &payload); err != nil {
			t.Fatalf("unmarshal egress JSON: %v (%q)", err, got)
		}
		if payload.Workspace != name || len(payload.Egress) != 3 {
			t.Fatalf("payload = %#v", payload)
		}
		if payload.Egress[1].Event != "egress_allow" || payload.Egress[1].Host != "api.github.com" {
			t.Fatalf("egress[1] = %#v", payload.Egress[1])
		}
	})
}

func TestRunEgressAbsentAuditIsEmptyAndSucceeds(t *testing.T) {
	setTextOutputForTest(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	// Workspace name with no audit log: mediation off / no decision yet.
	if err := runEgress(context.Background(), []string{"never-mediated", "--state-dir", dir}, out); err != nil {
		t.Fatalf("runEgress absent: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "" {
		t.Fatalf("absent audit should produce no output, got %q", got)
	}
}
