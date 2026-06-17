package egress

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBufferLoggerRecordsEvents(t *testing.T) {
	var b BufferLogger
	b.Log("egress_deny", map[string]any{"host": "evil.com"})
	if len(b.Events) != 1 || b.Events[0]["event"] != "egress_deny" || b.Events[0]["host"] != "evil.com" {
		t.Fatalf("events = %+v", b.Events)
	}
}

func TestFileLoggerAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.jsonl")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	l.now = func() time.Time { return time.Unix(0, 0).UTC() }
	l.Log("egress_allow", map[string]any{"host": "api.github.com"})
	_ = l.Close()

	f, _ := os.Open(path)
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		t.Fatal("no line written")
	}
	var row map[string]any
	if err := json.Unmarshal(s.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row["event"] != "egress_allow" || row["host"] != "api.github.com" || row["ts"] != "1970-01-01T00:00:00Z" {
		t.Fatalf("row = %+v", row)
	}
}

// TestRotatingFileLoggerRotatesAtMaxSize proves the size-bounded rotating logger:
// once a write would exceed maxBytes the active file is rolled to path.1 (with
// older backups shifting up and anything beyond maxBackups pruned) and a fresh
// active file is reopened. It bounds the on-disk audit footprint per ASK tenet 8.
func TestRotatingFileLoggerRotatesAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egress.jsonl")
	// Small cap so a handful of records forces several rotations; keep 2 backups.
	l, err := NewRotatingFileLogger(path, 200, 2)
	if err != nil {
		t.Fatalf("NewRotatingFileLogger: %v", err)
	}
	l.now = func() time.Time { return time.Unix(0, 0).UTC() }
	// Each record is ~80-90 bytes; ~10 of them blow well past 200 bytes and force
	// multiple rotations, exceeding the 2-backup cap so the oldest is pruned.
	for i := 0; i < 10; i++ {
		l.Log("egress_allow", map[string]any{"host": "api.github.com", "n": i})
	}
	_ = l.Close()

	// The active file exists and is under (or at) the cap by itself.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("active file missing: %v", err)
	}
	if info.Size() > 200 {
		t.Fatalf("active file %d bytes exceeds maxBytes 200 — rotation did not fire", info.Size())
	}

	// At least one rotation happened: path.1 exists.
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated backup %s.1: %v", path, err)
	}

	// No more than maxBackups rotated files exist; the oldest was pruned.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filepath.Base(path)+".") {
			backups++
		}
	}
	if backups > 2 {
		t.Fatalf("found %d rotated backups, want at most maxBackups=2 (oldest must be pruned)", backups)
	}
	// path.3 must never exist (maxBackups=2 prunes it).
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("path.3 exists but maxBackups=2 should have pruned it: %v", err)
	}
}

// TestRotatingFileLoggerUnboundedWhenMaxBytesZero proves maxBytes<=0 behaves
// unbounded: it never rotates regardless of how much is written, byte-identical
// to the plain FileLogger (the zero/default = unlimited contract).
func TestRotatingFileLoggerUnboundedWhenMaxBytesZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egress.jsonl")
	l, err := NewRotatingFileLogger(path, 0, 3)
	if err != nil {
		t.Fatalf("NewRotatingFileLogger: %v", err)
	}
	l.now = func() time.Time { return time.Unix(0, 0).UTC() }
	for i := 0; i < 50; i++ {
		l.Log("egress_allow", map[string]any{"host": "api.github.com", "n": i})
	}
	_ = l.Close()
	// No rotation ever: path.1 must not exist.
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("unbounded logger rotated (path.1 exists) despite maxBytes<=0: %v", err)
	}
	// All 50 records are in the single active file.
	f, _ := os.Open(path)
	defer f.Close()
	s := bufio.NewScanner(f)
	lines := 0
	for s.Scan() {
		lines++
	}
	if lines != 50 {
		t.Fatalf("active file has %d lines, want 50 (unbounded must not rotate)", lines)
	}
}
