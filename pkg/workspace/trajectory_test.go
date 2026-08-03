package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	events := []EventFile{{EventID: "event-life", Identity: vmkit.Identity{RequestID: "req-1", RuntimeID: name, SessionID: "session-1", Purpose: "test task", CorrelationID: "corr-1"}, State: vmkit.StateRunning, Detail: "guest filesystem sync completed", ObservedAt: "2026-06-16T00:00:00Z"}}
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
	constraintDir := filepath.Dir(ConstraintHistoryPath(dir, name))
	if err := os.MkdirAll(constraintDir, 0o700); err != nil {
		t.Fatal(err)
	}
	constraints := []ConstraintRevision{{ConstraintRevisionRef: vmkit.ConstraintRevisionRef{
		EventID: "event-constraint", RequestID: "req-constraint", RuntimeID: name,
		Purpose: "test task", CorrelationID: "corr-1", Trigger: "apply",
		ManifestSHA256: "manifest-hash", ObservedAt: mustParseTime(t, "2026-06-16T00:00:01.500Z"),
	}, Manifest: &Manifest{Name: name, Restart: "no"}}}
	constraintData, err := json.Marshal(constraints)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConstraintHistoryPath(dir, name), constraintData, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadTrajectory(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Source != "lifecycle" || got[1].Source != "secret" || got[2].Source != "constraint" || got[3].Source != "egress" {
		t.Fatalf("trajectory = %#v", got)
	}
	if got[0].State != "running" || got[0].Detail != "guest filesystem sync completed" {
		t.Fatalf("lifecycle compatibility fields = %#v", got[0])
	}
	if got[0].Purpose != "test task" || got[0].CorrelationID != "corr-1" {
		t.Fatalf("caller context = %#v", got[0])
	}
	if got[2].Event != "constraint_apply" || got[2].RequestID != "req-constraint" || got[2].Raw["manifest"] == nil {
		t.Fatalf("constraint trajectory record = %#v", got[2])
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
