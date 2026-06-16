package egress

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
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
