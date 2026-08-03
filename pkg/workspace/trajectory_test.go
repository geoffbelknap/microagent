package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestReadTrajectoryJoinsAuditStreamsByParsedTime(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	workspaceDir := filepath.Join(dir, name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []EventFile{{EventID: "event-life", Identity: vmkit.Identity{RequestID: "req-1", RuntimeID: name, SessionID: "session-1"}, State: vmkit.StateRunning, ObservedAt: "2026-06-16T00:00:00Z"}}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "events.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EgressAuditPath(dir, name), []byte(`{"event":"egress_allow","ts":"2026-06-16T00:00:02Z","runtime_id":"agent","session_id":"session-1","event_id":"event-egress","operation_id":"operation-egress"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secretxfer.AppendAccessRecord(secretxfer.AccessLogPath(dir, name), secretxfer.AccessRecord{At: "2026-06-16T00:00:01Z", RuntimeID: name, SessionID: "session-1", EventID: "event-secret", OperationID: "operation-secret", Name: "TOKEN", Access: "on-demand", Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	got, err := ReadTrajectory(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Source != "lifecycle" || got[1].Source != "secret" || got[2].Source != "egress" {
		t.Fatalf("trajectory = %#v", got)
	}
}
