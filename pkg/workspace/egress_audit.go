package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// EgressEvent is one decision recorded by the egress mediator's audit log
// (<state-dir>/<name>/egress-access.jsonl). The mediator's event vocabulary is
// broad and still growing (egress_listen, egress_allow, egress_deny,
// egress_dns_*, egress_swap, egress_mitm_*, egress_cap_exceeded, ...), so the
// reader stays deliberately generic: it promotes only the handful of common keys
// into typed fields for convenient display and keeps every field — known or not —
// in Raw. New event types and fields surface without code changes here.
type EgressEvent struct {
	Event  string         `json:"event"`
	TS     string         `json:"ts,omitempty"`
	Host   string         `json:"host,omitempty"`
	Dst    string         `json:"dst,omitempty"`
	Reason string         `json:"reason,omitempty"`
	Raw    map[string]any `json:"raw,omitempty"`
}

// EgressAuditPath is the per-workspace egress mediator audit log: one JSON
// object per decision, appended by the mediator's FileLogger/RotatingFileLogger.
func EgressAuditPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "egress-access.jsonl")
}

// ReadEgressAudit returns the egress mediator's recorded decisions for a
// workspace, oldest first. The audit log is line-delimited JSON, so it is read
// line by line rather than as a single document.
//
// An absent file is not an error: mediation may be off, or no decision has been
// made yet — both return an empty (non-nil) slice. A malformed/partial line
// (e.g. a trailing record from a writer that crashed mid-append) is skipped
// rather than failing the whole read, so a live tail never wedges on the last
// record. Records can be large (a MITM/swap row may exceed the default 64KB
// scanner token), so the scanner buffer is bumped to 1MB.
func ReadEgressAudit(stateDir, name string) ([]EgressEvent, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	f, err := os.Open(EgressAuditPath(stateDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return []EgressEvent{}, nil
		}
		return nil, err
	}
	defer f.Close()

	events := []EgressEvent{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			// Skip an unparseable (e.g. truncated trailing) line rather than
			// failing the whole read.
			continue
		}
		events = append(events, egressEventFromRaw(raw))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// egressEventFromRaw promotes the common string keys into typed fields while
// keeping the full record in Raw.
func egressEventFromRaw(raw map[string]any) EgressEvent {
	return EgressEvent{
		Event:  egressString(raw, "event"),
		TS:     egressString(raw, "ts"),
		Host:   egressString(raw, "host"),
		Dst:    egressString(raw, "dst"),
		Reason: egressString(raw, "reason"),
		Raw:    raw,
	}
}

func egressString(raw map[string]any, key string) string {
	if v, ok := raw[key].(string); ok {
		return v
	}
	return ""
}
