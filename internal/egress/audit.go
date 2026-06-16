package egress

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Logger records mediator audit events. Implementations must be safe for
// concurrent use. Values are never secrets.
type Logger interface {
	Log(event string, fields map[string]any)
}

// BufferLogger collects events in memory for tests.
type BufferLogger struct {
	mu     sync.Mutex
	Events []map[string]any
}

func (b *BufferLogger) Log(event string, fields map[string]any) {
	row := map[string]any{"event": event}
	for k, v := range fields {
		row[k] = v
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Events = append(b.Events, row)
}

// FileLogger appends one JSON object per event to an audit file.
type FileLogger struct {
	mu  sync.Mutex
	f   *os.File
	now func() time.Time
}

// NewFileLogger opens path for appending (created 0600).
func NewFileLogger(path string) (*FileLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileLogger{f: f, now: time.Now}, nil
}

func (l *FileLogger) Log(event string, fields map[string]any) {
	row := map[string]any{"event": event, "ts": l.now().UTC().Format(time.RFC3339Nano)}
	for k, v := range fields {
		row[k] = v
	}
	data, err := json.Marshal(row)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.f.Write(append(data, '\n'))
}

func (l *FileLogger) Close() error { return l.f.Close() }
