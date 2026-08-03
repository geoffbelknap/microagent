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

func TestBuildIncidentReceiptScopesEffectsToSession(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	workspaceDir := filepath.Join(dir, name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []EventFile{
		{Identity: vmkit.Identity{RuntimeID: name, SessionID: "old"}, State: vmkit.StateRunning, ObservedAt: "2026-08-03T00:00:00Z"},
		{Identity: vmkit.Identity{RuntimeID: name, SessionID: "current"}, State: vmkit.StateRunning, ObservedAt: "2026-08-03T01:00:00Z"},
		{Identity: vmkit.Identity{RuntimeID: name, SessionID: "current"}, State: vmkit.StateQuarantined, ObservedAt: "2026-08-03T01:05:00Z"},
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "events.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	writeAuditLines(t, EgressAuditPath(dir, name),
		`{"event":"egress_allow","ts":"2026-08-03T01:01:00Z","runtime_id":"agent","session_id":"current","host":"api.example"}`,
		`{"event":"egress_deny","ts":"2026-08-03T00:01:00Z","runtime_id":"agent","session_id":"old","host":"old.example"}`)
	writeAuditLines(t, BrokerAccessPath(dir, name),
		`{"event":"broker_request_allow","ts":"2026-08-03T01:02:00Z","runtime_id":"agent","session_id":"current","host":"upload.example","verdict":"allow","bytes_out":12,"bytes_in":34,"secret_refs":["API_TOKEN"]}`)
	if err := secretxfer.AppendAccessRecord(secretxfer.AccessLogPath(dir, name), secretxfer.AccessRecord{
		At: "2026-08-03T01:03:00Z", RuntimeID: name, SessionID: "current", Name: "API_TOKEN", Access: "on-demand", Result: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	got := buildIncidentReceipt(dir, name, "current", "notify the operator", "task-42", "2026-08-03T01:00:00Z", time.Date(2026, 8, 3, 1, 6, 0, 0, time.UTC))
	if !got.Complete || got.LifecycleEvents != 2 || got.Egress.DecisionCount != 1 {
		t.Fatalf("receipt = %#v", got)
	}
	if got.Purpose != "notify the operator" || got.CorrelationID != "task-42" {
		t.Fatalf("caller context = %#v", got)
	}
	if got.Egress.AllowByHost["api.example"] != 1 || got.Egress.DenyByHost["old.example"] != 0 {
		t.Fatalf("egress = %#v", got.Egress)
	}
	if got.Broker.RequestCount != 1 || got.Broker.BytesOut != 12 || got.Broker.BytesIn != 34 {
		t.Fatalf("broker = %#v", got.Broker)
	}
	if len(got.Broker.SecretRefs) != 1 || got.Broker.SecretRefs[0] != "API_TOKEN" {
		t.Fatalf("broker secret refs = %#v", got.Broker.SecretRefs)
	}
	if got.Secrets.AccessCount != 1 || got.Secrets.ByName["API_TOKEN"] != 1 || got.Secrets.ByResult["ok"] != 1 {
		t.Fatalf("secrets = %#v", got.Secrets)
	}
}

func TestBuildIncidentReceiptReportsIncompleteAudit(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAuditLines(t, EgressAuditPath(dir, name), `{not-json}`)
	got := buildIncidentReceipt(dir, name, "session", "", "", "", time.Now())
	if got.Complete || len(got.Errors) != 1 {
		t.Fatalf("receipt = %#v", got)
	}
}

func writeAuditLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	data := []byte{}
	for _, line := range lines {
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
