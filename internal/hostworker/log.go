package hostworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type JSONLLogger struct {
	mu   sync.Mutex
	file *os.File
}

func OpenJSONLLogger(path string) (*JSONLLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLLogger{file: file}, nil
}

func (l *JSONLLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *JSONLLogger) Log(event string, fields map[string]any) {
	if l == nil || l.file == nil {
		return
	}
	row := map[string]any{
		"event":      event,
		"host_epoch": float64(time.Now().UnixNano()) / 1e9,
	}
	for key, value := range fields {
		row[key] = value
	}
	data, err := json.Marshal(row)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(data, '\n'))
}
