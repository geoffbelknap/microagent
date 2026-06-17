package egress

import (
	"encoding/json"
	"fmt"
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

// RotatingFileLogger is a size-bounded FileLogger: it appends one JSON object per
// event like FileLogger, but caps the on-disk audit footprint (ASK tenet 8 —
// bounded retention). When a write would carry the active file past maxBytes, it
// renames path->path.1 (shifting path.1->path.2 ... and dropping anything past
// maxBackups) and reopens a fresh active path before writing the record. The
// total on-disk footprint is therefore bounded by roughly maxBytes*(maxBackups+1).
//
// When maxBytes<=0 it behaves unbounded — it never rotates and is byte-identical
// to FileLogger — so a zero/default cap is the current (uncapped) behavior.
type RotatingFileLogger struct {
	mu         sync.Mutex
	f          *os.File
	path       string
	maxBytes   int64
	maxBackups int
	size       int64 // bytes written to the current active file
	now        func() time.Time
}

// NewRotatingFileLogger opens path for appending (created 0600) and bounds it to
// maxBytes per active file, keeping at most maxBackups rotated files. A maxBytes
// <= 0 disables rotation (unbounded, like FileLogger). It seeds the byte counter
// from the file's current size so an existing file is rotated correctly.
func NewRotatingFileLogger(path string, maxBytes int64, maxBackups int) (*RotatingFileLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	var size int64
	if info, serr := f.Stat(); serr == nil {
		size = info.Size()
	}
	return &RotatingFileLogger{
		f:          f,
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
		size:       size,
		now:        time.Now,
	}, nil
}

func (l *RotatingFileLogger) Log(event string, fields map[string]any) {
	row := map[string]any{"event": event, "ts": l.now().UTC().Format(time.RFC3339Nano)}
	for k, v := range fields {
		row[k] = v
	}
	data, err := json.Marshal(row)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	// Rotate when this write would carry the active file past the cap (but always
	// allow the very first write of a record so a single oversized record still
	// lands rather than rotating into an endless empty-file loop). maxBytes<=0
	// disables rotation entirely (unbounded).
	if l.maxBytes > 0 && l.size > 0 && l.size+int64(len(data)) > l.maxBytes {
		l.rotateLocked()
	}
	if l.f == nil {
		return // active file could not be reopened after a rotation; drop the record
	}
	n, _ := l.f.Write(data)
	l.size += int64(n)
}

// rotateLocked shifts the backup chain (path.(maxBackups-1)->path.maxBackups,
// dropping path.maxBackups; ... ; path.1->path.2; path->path.1) and reopens a
// fresh active file. Caller holds l.mu. On any failure it leaves the current
// active file in place (best-effort: the audit log must never wedge the mediator).
func (l *RotatingFileLogger) rotateLocked() {
	// Close the active file before renaming it.
	_ = l.f.Close()
	// Drop the oldest backup beyond the cap.
	if l.maxBackups > 0 {
		_ = os.Remove(fmt.Sprintf("%s.%d", l.path, l.maxBackups))
		// Shift path.(n-1) -> path.n down to path.1 -> path.2.
		for i := l.maxBackups - 1; i >= 1; i-- {
			from := fmt.Sprintf("%s.%d", l.path, i)
			to := fmt.Sprintf("%s.%d", l.path, i+1)
			_ = os.Rename(from, to)
		}
		_ = os.Rename(l.path, l.path+".1")
	} else {
		// maxBackups<=0: keep no history, just truncate by removing the active file.
		_ = os.Remove(l.path)
	}
	// Reopen a fresh active file. If reopen fails l.f is nil and Log skips the
	// write (the audit *sink* degrades, never the egress decision — fail-closed
	// applies to egress, not to the log file).
	f, _ := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	l.f = f
	l.size = 0
}

func (l *RotatingFileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	return l.f.Close()
}
