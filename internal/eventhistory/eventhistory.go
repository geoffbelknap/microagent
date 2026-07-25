// Package eventhistory owns persistence for bounded lifecycle event histories.
package eventhistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
)

const DefaultMaxEvents = 1024

var writeFileAtomic = fsutil.WriteFileAtomic

// Options controls compatibility and retention for an event history.
type Options struct {
	MaxEvents      int
	AllowJSONLines bool
}

// IntegrityError reports an existing history that cannot be decoded safely.
// Append never replaces a malformed history.
type IntegrityError struct {
	Path string
	Err  error
}

func (e IntegrityError) Error() string {
	return fmt.Sprintf("event history %s is malformed: %v", e.Path, e.Err)
}

func (e IntegrityError) Unwrap() error {
	return e.Err
}

// Append serializes writers across processes, appends event, applies bounded
// retention, and atomically replaces the JSON-array view. Array order is commit
// order. Duplicate events are retained because distinct lifecycle observations
// can legitimately have identical payloads.
func Append[T any](path string, event T, options Options) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	release, err := fsutil.Lock(path + ".lock")
	if err != nil {
		return fmt.Errorf("lock event history %s: %w", path, err)
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = fmt.Errorf("unlock event history %s: %w", path, releaseErr)
		}
	}()

	events, err := Read[T](path, options)
	if err != nil {
		return err
	}
	events = append(events, event)
	maxEvents := options.MaxEvents
	if maxEvents <= 0 {
		maxEvents = DefaultMaxEvents
	}
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	return write(path, events)
}

// Read returns a complete JSON-array history. Empty and absent files are empty
// histories. Malformed or interrupted records are reported as IntegrityError.
func Read[T any](path string, options Options) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var events []T
	if err := json.Unmarshal(data, &events); err == nil {
		return events, nil
	} else if !options.AllowJSONLines {
		return nil, IntegrityError{Path: path, Err: err}
	}
	events, lineErr := readJSONLines[T](data)
	if lineErr != nil {
		return nil, IntegrityError{Path: path, Err: lineErr}
	}
	return events, nil
}

func readJSONLines[T any](data []byte) ([]T, error) {
	var events []T
	for index, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event T
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("legacy JSON line %d: %w", index+1, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func write[T any](path string, events []T) error {
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write event history %s: %w", path, err)
	}
	return nil
}
