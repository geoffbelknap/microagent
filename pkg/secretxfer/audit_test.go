package secretxfer

import (
	"path/filepath"
	"testing"
)

func TestAppendAndReadAccessRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets-access.jsonl")
	recs := []AccessRecord{
		{At: "2026-06-02T00:00:00Z", RuntimeID: "ws", Name: "API_KEY", Access: "materialize", Result: "ok"},
		{At: "2026-06-02T00:00:01Z", RuntimeID: "ws", Name: "DB", Access: "on-demand", Result: "ok"},
		{At: "2026-06-02T00:00:02Z", RuntimeID: "ws", Name: "X", Access: "on-demand", Result: "denied"},
	}
	for _, r := range recs {
		if err := AppendAccessRecord(path, r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := ReadAccessRecords(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 || got[1].Name != "DB" || got[2].Result != "denied" {
		t.Fatalf("records did not round-trip: %+v", got)
	}
}

func TestReadAccessRecordsMissingFileIsEmpty(t *testing.T) {
	got, err := ReadAccessRecords(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("read missing: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

func TestAccessLogPath(t *testing.T) {
	if got := AccessLogPath("/state", "ws"); got != filepath.Join("/state", "ws", "secrets-access.jsonl") {
		t.Fatalf("path = %q", got)
	}
}
