package workspace

import (
	"errors"
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

func TestReadEgressAuditReportsTruncatedFinalLine(t *testing.T) {
	stateDir := t.TempDir()
	name := "research"
	dir := filepath.Join(stateDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A partial trailing line (writer crashed mid-append) must be reported while
	// earlier complete rows remain available.
	content := `{"event":"egress_allow","ts":"2026-06-16T00:00:00Z","host":"api.github.com"}` + "\n" +
		`{"event":"egress_deny","ts":"2026-06-16T00:00:01Z","host":"evil.exam`
	if err := os.WriteFile(filepath.Join(dir, "egress-access.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEgressAudit(stateDir, name)
	var integrityErr AuditIntegrityError
	if !errors.As(err, &integrityErr) {
		t.Fatalf("ReadEgressAudit error = %v, want AuditIntegrityError", err)
	}
	if integrityErr.Line != 2 {
		t.Fatalf("integrity error line = %d, want 2", integrityErr.Line)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1 complete prefix", len(events))
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

// TestReadBrokerAccessParsesDecisionRecords: broker decision records share the
// mediator's event-record shape, so the same tolerant generic reader serves
// broker-access.jsonl — event/ts/host promoted, verdict/rule/bytes in Raw.
func TestReadBrokerAccessParsesDecisionRecords(t *testing.T) {
	stateDir := t.TempDir()
	name := "research"
	dir := filepath.Join(stateDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"event":"broker_request_allow","ts":"2026-06-16T00:00:01Z","mode":"terminate","host":"api.anthropic.com","method":"POST","verdict":"allow","status":200,"bytes_out":42,"bytes_in":512,"duration_ms":180,"secret_refs":["api"]}` + "\n" +
		`{"event":"broker_request_deny","ts":"2026-06-16T00:00:02Z","mode":"connect","host":"203.0.113.7:443","method":"CONNECT","verdict":"deny","rule":"no-tunnels"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "broker-access.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := ReadBrokerAccess(stateDir, name)
	if err != nil {
		t.Fatalf("ReadBrokerAccess: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if events[0].Event != "broker_request_allow" || events[0].Host != "api.anthropic.com" {
		t.Fatalf("allow row = %+v", events[0])
	}
	if events[0].Raw["verdict"] != "allow" || events[0].Raw["status"] != float64(200) {
		t.Fatalf("Raw lost decision fields: %+v", events[0].Raw)
	}
	if events[1].Event != "broker_request_deny" || events[1].Raw["rule"] != "no-tunnels" {
		t.Fatalf("deny row = %+v", events[1])
	}
}

func TestReadBrokerAccessAbsentFileReturnsEmpty(t *testing.T) {
	events, err := ReadBrokerAccess(t.TempDir(), "never-brokered")
	if err != nil {
		t.Fatalf("ReadBrokerAccess: %v", err)
	}
	if events == nil || len(events) != 0 {
		t.Fatalf("want non-nil empty slice, got %#v", events)
	}
}

// TestMergeEgressEvents: the mediator and broker streams merge into one
// time-ordered view; ties keep the first slice's records first (stable).
func TestMergeEgressEvents(t *testing.T) {
	mediator := []EgressEvent{
		{Event: "egress_allow", TS: "2026-06-16T00:00:01Z"},
		{Event: "egress_deny", TS: "2026-06-16T00:00:04Z"},
	}
	broker := []EgressEvent{
		{Event: "broker_request_allow", TS: "2026-06-16T00:00:02Z"},
		{Event: "broker_request_deny", TS: "2026-06-16T00:00:04Z"},
	}
	merged := MergeEgressEvents(mediator, broker)
	got := make([]string, len(merged))
	for i, e := range merged {
		got[i] = e.Event
	}
	want := []string{"egress_allow", "broker_request_allow", "egress_deny", "broker_request_deny"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged order = %v, want %v", got, want)
		}
	}
}

// TestSummarizeFoldsBrokerVerdicts: the existing per-host rollup folds broker
// records in via the shared _allow/_deny event-name suffixes — the vocabulary
// was chosen so this needs no special case.
func TestSummarizeFoldsBrokerVerdicts(t *testing.T) {
	events := []EgressEvent{
		{Event: "egress_allow", Host: "api.github.com"},
		{Event: "broker_request_allow", Host: "api.anthropic.com"},
		{Event: "broker_request_allow", Host: "api.anthropic.com"},
		{Event: "broker_request_deny", Host: "203.0.113.7:443"},
	}
	s := SummarizeEgressAudit(events)
	if s.AllowByHost["api.anthropic.com"] != 2 {
		t.Fatalf("AllowByHost = %+v", s.AllowByHost)
	}
	if s.DenyByHost["203.0.113.7:443"] != 1 {
		t.Fatalf("DenyByHost = %+v", s.DenyByHost)
	}
}
