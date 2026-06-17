package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEgressAuditParsesRowsInOrder(t *testing.T) {
	stateDir := t.TempDir()
	name := "research"
	dir := filepath.Join(stateDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One JSON object per line, mixing the broad event vocabulary the mediator
	// emits. The reader must stay generic: it keeps every field in Raw and only
	// promotes the common keys into typed fields.
	lines := []string{
		`{"event":"egress_listen","ts":"2026-06-16T00:00:00Z","addr":"127.0.0.1:0"}`,
		`{"event":"egress_allow","ts":"2026-06-16T00:00:01Z","host":"api.github.com","dst":"140.82.0.1:443","mitm":true}`,
		`{"event":"egress_deny","ts":"2026-06-16T00:00:02Z","host":"evil.example","reason":"not allowlisted","dst":"203.0.113.5:443"}`,
		`{"event":"egress_dns_deny","ts":"2026-06-16T00:00:03Z","qname":"tracker.example","reason":"blocked"}`,
		`{"event":"egress_swap","ts":"2026-06-16T00:00:04Z","detail":"reloaded allowlist"}`,
	}
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "egress-access.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEgressAudit(stateDir, name)
	if err != nil {
		t.Fatalf("ReadEgressAudit: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 5", len(events))
	}
	// Ordering preserved (oldest first).
	if events[0].Event != "egress_listen" || events[4].Event != "egress_swap" {
		t.Fatalf("ordering wrong: first=%q last=%q", events[0].Event, events[4].Event)
	}
	// Typed field extraction from common keys.
	if events[1].Event != "egress_allow" || events[1].Host != "api.github.com" || events[1].Dst != "140.82.0.1:443" {
		t.Fatalf("allow row fields = %+v", events[1])
	}
	if events[1].TS != "2026-06-16T00:00:01Z" {
		t.Fatalf("ts = %q", events[1].TS)
	}
	if events[2].Reason != "not allowlisted" || events[2].Host != "evil.example" {
		t.Fatalf("deny row fields = %+v", events[2])
	}
	// Raw retains everything, including keys with no typed field.
	if events[1].Raw["mitm"] != true {
		t.Fatalf("Raw lost mitm: %+v", events[1].Raw)
	}
	if events[3].Raw["qname"] != "tracker.example" {
		t.Fatalf("Raw lost qname: %+v", events[3].Raw)
	}
}

func TestReadEgressAuditAbsentFileReturnsEmpty(t *testing.T) {
	stateDir := t.TempDir()
	events, err := ReadEgressAudit(stateDir, "never-mediated")
	if err != nil {
		t.Fatalf("ReadEgressAudit: %v", err)
	}
	if events == nil {
		t.Fatal("absent file should return a non-nil empty slice")
	}
	if len(events) != 0 {
		t.Fatalf("event count = %d, want 0", len(events))
	}
}

func TestReadEgressAuditSkipsTruncatedFinalLine(t *testing.T) {
	stateDir := t.TempDir()
	name := "research"
	dir := filepath.Join(stateDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A partial trailing line (writer crashed mid-append) must be skipped, not
	// fail the whole read; earlier complete rows are still returned.
	content := `{"event":"egress_allow","ts":"2026-06-16T00:00:00Z","host":"api.github.com"}` + "\n" +
		`{"event":"egress_deny","ts":"2026-06-16T00:00:01Z","host":"evil.exam`
	if err := os.WriteFile(filepath.Join(dir, "egress-access.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEgressAudit(stateDir, name)
	if err != nil {
		t.Fatalf("ReadEgressAudit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1 (truncated line skipped)", len(events))
	}
	if events[0].Event != "egress_allow" || events[0].Host != "api.github.com" {
		t.Fatalf("surviving row = %+v", events[0])
	}
}

func TestReadEgressAuditRejectsInvalidName(t *testing.T) {
	if _, err := ReadEgressAudit(t.TempDir(), "../escape"); err == nil {
		t.Fatal("expected validation error for invalid workspace name")
	}
}
