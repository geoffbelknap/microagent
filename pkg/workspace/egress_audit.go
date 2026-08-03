package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EgressEvent is one decision recorded by the egress mediator's audit log
// (<state-dir>/<name>/egress-access.jsonl). The mediator's event vocabulary is
// broad and still growing (egress_listen, egress_allow, egress_deny,
// egress_dns_*, egress_swap, egress_mitm_*, egress_cap_exceeded, ...), so the
// reader stays deliberately generic: it promotes only the handful of common keys
// into typed fields for convenient display and keeps every field — known or not —
// in Raw. New event types and fields surface without code changes here.
type EgressEvent struct {
	Event       string         `json:"event"`
	TS          string         `json:"ts,omitempty"`
	RuntimeID   string         `json:"runtime_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	EventID     string         `json:"event_id,omitempty"`
	OperationID string         `json:"operation_id,omitempty"`
	Host        string         `json:"host,omitempty"`
	Dst         string         `json:"dst,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Raw         map[string]any `json:"raw,omitempty"`
}

// AuditIntegrityError reports a malformed audit record without discarding the
// complete records that preceded it. Callers may inspect the returned prefix,
// but must not present it as a complete history.
type AuditIntegrityError struct {
	Path string
	Line int
	Err  error
}

func (e AuditIntegrityError) Error() string {
	return fmt.Sprintf("audit stream %s has malformed record at line %d: %v", e.Path, e.Line, e.Err)
}

func (e AuditIntegrityError) Unwrap() error { return e.Err }

// EgressAuditPath is the per-workspace egress mediator audit log: one JSON
// object per decision, appended by the mediator's FileLogger/RotatingFileLogger.
func EgressAuditPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "egress-access.jsonl")
}

// BrokerAccessPath is the per-workspace broker decision stream: one JSON
// object per brokered request, appended by the broker's host companion.
func BrokerAccessPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "broker-access.jsonl")
}

// ReadEgressAudit returns the egress mediator's recorded decisions for a
// workspace, oldest first. The audit log is line-delimited JSON, so it is read
// line by line rather than as a single document.
//
// An absent file is not an error: mediation may be off, or no decision has been
// made yet — both return an empty (non-nil) slice. A malformed/partial line
// returns the complete prefix plus AuditIntegrityError, so interrupted writes
// are loss-detectable rather than silently omitted. Records can be large (a
// MITM/swap row may exceed the default 64KB scanner token), so the scanner
// buffer is bumped to 1MB.
func ReadEgressAudit(stateDir, name string) ([]EgressEvent, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	return readEventRecords(EgressAuditPath(stateDir, name))
}

// ReadBrokerAccess returns the broker's per-request decision records for a
// workspace, oldest first. Broker records share the mediator's event-record
// shape (event/ts/host + record-specific keys kept in Raw), so the same
// tolerant reader serves both streams; absent-file and truncated-line
// semantics match ReadEgressAudit.
func ReadBrokerAccess(stateDir, name string) ([]EgressEvent, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	return readEventRecords(BrokerAccessPath(stateDir, name))
}

// MergeEgressEvents returns a stable chronological view. It parses RFC3339
// timestamps rather than comparing their encodings, which may use different
// precision or offsets while representing the same timeline.
func MergeEgressEvents(a, b []EgressEvent) []EgressEvent {
	merged := append(append(make([]EgressEvent, 0, len(a)+len(b)), a...), b...)
	sort.SliceStable(merged, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339Nano, merged[i].TS)
		right, rightErr := time.Parse(time.RFC3339Nano, merged[j].TS)
		return leftErr == nil && rightErr == nil && left.Before(right)
	})
	return merged
}

func readEventRecords(path string) ([]EgressEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []EgressEvent{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	events := []EgressEvent{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			return events, AuditIntegrityError{Path: path, Line: lineNumber, Err: err}
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
		Event:       egressString(raw, "event"),
		TS:          egressString(raw, "ts"),
		RuntimeID:   egressString(raw, "runtime_id"),
		SessionID:   egressString(raw, "session_id"),
		EventID:     egressString(raw, "event_id"),
		OperationID: egressString(raw, "operation_id"),
		Host:        egressString(raw, "host"),
		Dst:         egressString(raw, "dst"),
		Reason:      egressString(raw, "reason"),
		Raw:         raw,
	}
}

func egressString(raw map[string]any, key string) string {
	if v, ok := raw[key].(string); ok {
		return v
	}
	return ""
}

// EgressAuditSummary is a high-level rollup of a workspace's egress mediator
// decisions — enough for a caller to judge whether a dispatched task stayed
// on-intent (what it reached, what was denied) without reading the raw log.
// Because the underlying audit is written by the mediator, outside the guest's
// control, the summary is a trustworthy record the guest cannot forge.
type EgressAuditSummary struct {
	DecisionCount int            `json:"decision_count"`
	ByEvent       map[string]int `json:"by_event,omitempty"`
	AllowByHost   map[string]int `json:"allow_by_host,omitempty"`
	DenyByHost    map[string]int `json:"deny_by_host,omitempty"`
}

// SummarizeEgressAudit rolls a workspace's egress decisions into an
// EgressAuditSummary. allow/deny are recognized by event-name suffix so the
// TCP/UDP/DNS variants fold into one per-host verdict view; the destination
// host (or dst when no hostname was seen) is the key.
func SummarizeEgressAudit(events []EgressEvent) EgressAuditSummary {
	s := EgressAuditSummary{DecisionCount: len(events), ByEvent: map[string]int{}}
	allow := map[string]int{}
	deny := map[string]int{}
	for _, ev := range events {
		s.ByEvent[ev.Event]++
		host := ev.Host
		if host == "" {
			host = ev.Dst
		}
		if host == "" {
			continue
		}
		switch {
		case strings.HasSuffix(ev.Event, "_allow"):
			allow[host]++
		case strings.HasSuffix(ev.Event, "_deny"):
			deny[host]++
		}
	}
	if len(allow) > 0 {
		s.AllowByHost = allow
	}
	if len(deny) > 0 {
		s.DenyByHost = deny
	}
	return s
}
