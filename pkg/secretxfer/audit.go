package secretxfer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AccessRecord is one secret-access audit entry. It records provenance and
// outcome but NEVER the secret value.
type AccessRecord struct {
	At        string `json:"at"`         // RFC3339Nano
	RuntimeID string `json:"runtime_id"` // workspace name
	Name      string `json:"name"`       // secret name
	Access    string `json:"access"`     // "materialize" | "on-demand"
	Result    string `json:"result"`     // "ok" | "denied" | "error"
}

// AccessLogPath returns the per-workspace audit log path, a sibling of
// events.json.
func AccessLogPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "secrets-access.jsonl")
}

// AppendAccessRecord appends one record as a JSON line. The log is append-only
// (no read-modify-write, no cap) so the audit trail stays complete.
func AppendAccessRecord(path string, rec AccessRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create audit dir: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write audit record: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	return nil
}

// ReadAccessRecords parses the JSONL audit log. A missing file yields no
// records (no access has happened yet).
func ReadAccessRecords(path string) ([]AccessRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []AccessRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec AccessRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("parse audit record: %w", err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
