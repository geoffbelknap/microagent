package modelservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/internal/eventhistory"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestPendingModelRelease(t *testing.T) {
	dir := t.TempDir()
	var releasedMediators []string
	prevReleaseMediator := releaseModelService
	releaseModelService = func(stateDir, workspaceID string) error {
		releasedMediators = append(releasedMediators, stateDir+"|"+workspaceID)
		return nil
	}
	t.Cleanup(func() { releaseModelService = prevReleaseMediator })

	// Missing manifest must yield a silent no-op.
	PendingRelease(dir, "ghost", vmkit.BackendLinuxKVM)()

	opts := workspace.DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = dir
	opts.Model = "hf.co/org/repo@main/m.gguf"
	if err := workspace.WriteManifest(opts); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	idx := modelrunner.Index{Runners: []modelrunner.Record{{
		Key:      "hf.co/org/repo@main/m.gguf",
		ModelRef: "hf.co/org/repo@main/m.gguf",
		PID:      99999999, // dead PID: release stops it best-effort
		Holders:  []string{"ws"},
	}}}
	if err := modelrunner.WriteIndex(dir, idx); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := eventhistory.Append(filepath.Join(dir, "ws", "events.json"), workspace.EventFile{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "ws", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		State:      vmkit.StateRunning,
		ObservedAt: "2026-06-15T00:00:00Z",
	}, eventhistory.Options{}); err != nil {
		t.Fatal(err)
	}
	// The ref is captured at call time: removing the manifest afterwards (as
	// delete does) must not stop the release.
	release := PendingRelease(dir, "ws", vmkit.BackendLinuxKVM)
	if err := os.RemoveAll(filepath.Join(dir, "workspaces", "ws")); err != nil {
		t.Fatal(err)
	}
	release()
	after, err := modelrunner.ReadIndex(dir)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(after.Runners) != 0 {
		t.Fatalf("runner not released: %+v", after.Runners)
	}
	if !containsTestString(releasedMediators, dir+"|ws") {
		t.Fatalf("mediator release not called: %#v", releasedMediators)
	}
	events, err := workspace.ReadEvents(dir, "ws")
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 2 || !strings.Contains(events[1].Detail, "model_worker=released") || events[1].State != vmkit.StateRunning {
		t.Fatalf("events = %+v", events)
	}
}

func TestAppendModelWorkerEventIfWorkspaceExists(t *testing.T) {
	dir := t.TempDir()
	if err := appendModelWorkerEventIfWorkspaceExists(dir, "missing", vmkit.BackendLinuxKVM, vmkit.StateStarting, "model_worker=attached"); err != nil {
		t.Fatalf("missing workspace event: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing workspace event created state: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := modelrunner.Record{
		ModelRef:           "hf.co/org/repo@main/m.gguf",
		Engine:             "runner-x",
		PID:                1234,
		RunnerConfigDigest: "digest123",
	}
	if err := appendModelWorkerAttachedEvent(workspace.Options{StateDir: dir, Name: "ws", Backend: vmkit.BackendLinuxKVM}, runner, "http://127.0.0.1:11434/v1", nil); err != nil {
		t.Fatalf("append attached event: %v", err)
	}
	events, err := workspace.ReadEvents(dir, "ws")
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	for _, want := range []string{"model_worker=attached", "model_ref=hf.co/org/repo@main/m.gguf", "engine=runner-x", "runner_config_digest=digest123", "model_url=http://127.0.0.1:11434/v1", "mediation=direct"} {
		if !strings.Contains(event.Detail, want) {
			t.Fatalf("event detail %q missing %q", event.Detail, want)
		}
	}
	if event.State != vmkit.StateStarting || event.Identity.RuntimeID != "ws" || event.Identity.Backend != vmkit.BackendLinuxKVM {
		t.Fatalf("event = %+v", event)
	}
	if err := appendModelWorkerAttachedEvent(workspace.Options{StateDir: dir, Name: "ws", Backend: vmkit.BackendLinuxKVM}, runner, "http://127.0.0.1:11434/v1", &Attachment{
		Mode:         "local-allow",
		PID:          5678,
		Port:         12345,
		AuditLogPath: "/tmp/mediator.jsonl",
	}); err != nil {
		t.Fatalf("append mediated attached event: %v", err)
	}
	events, err = workspace.ReadEvents(dir, "ws")
	if err != nil {
		t.Fatalf("read mediated events: %v", err)
	}
	mediatedDetail := events[len(events)-1].Detail
	for _, want := range []string{"mediation=host-worker", "mediation_mode=local-allow", "mediator_pid=5678", "mediator_port=12345", "mediator_audit_log=/tmp/mediator.jsonl"} {
		if !strings.Contains(mediatedDetail, want) {
			t.Fatalf("mediated event detail %q missing %q", mediatedDetail, want)
		}
	}
}

func containsTestString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
