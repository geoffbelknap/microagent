package workspace

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
)

// TrajectoryRecord is the common identity and ordering envelope for one
// lifecycle, egress, broker, or secret-access audit record. Raw preserves the
// source record without forcing unrelated streams into one content schema.
type TrajectoryRecord struct {
	Source      string         `json:"source"`
	Timestamp   string         `json:"timestamp"`
	RuntimeID   string         `json:"runtime_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	RequestID   string         `json:"request_id,omitempty"`
	EventID     string         `json:"event_id,omitempty"`
	OperationID string         `json:"operation_id,omitempty"`
	Event       string         `json:"event"`
	Raw         map[string]any `json:"raw"`
}

// ReadTrajectory reads all host-owned audit streams for a workspace and
// returns one stable chronological view.
func ReadTrajectory(stateDir, name string) ([]TrajectoryRecord, error) {
	lifecycle, err := ReadEvents(stateDir, name)
	if err != nil {
		return nil, err
	}
	mediator, err := ReadEgressAudit(stateDir, name)
	if err != nil {
		return nil, err
	}
	brokered, err := ReadBrokerAccess(stateDir, name)
	if err != nil {
		return nil, err
	}
	secrets, err := secretxfer.ReadAccessRecords(secretxfer.AccessLogPath(stateDir, name))
	if err != nil {
		return nil, err
	}

	records := make([]TrajectoryRecord, 0, len(lifecycle)+len(mediator)+len(brokered)+len(secrets))
	for _, event := range lifecycle {
		records = append(records, TrajectoryRecord{Source: "lifecycle", Timestamp: event.ObservedAt,
			RuntimeID: event.Identity.RuntimeID, SessionID: event.Identity.SessionID,
			RequestID: event.Identity.RequestID, EventID: event.EventID,
			Event: string(event.State), Raw: rawRecord(event)})
	}
	for _, stream := range []struct {
		source string
		events []EgressEvent
	}{{"egress", mediator}, {"broker", brokered}} {
		for _, event := range stream.events {
			records = append(records, TrajectoryRecord{Source: stream.source, Timestamp: event.TS,
				RuntimeID: event.RuntimeID, SessionID: event.SessionID, EventID: event.EventID,
				OperationID: event.OperationID, Event: event.Event, Raw: event.Raw})
		}
	}
	for _, event := range secrets {
		records = append(records, TrajectoryRecord{Source: "secret", Timestamp: event.At,
			RuntimeID: event.RuntimeID, SessionID: event.SessionID, EventID: event.EventID,
			OperationID: event.OperationID, Event: "secret_" + event.Access + "_" + event.Result,
			Raw: rawRecord(event)})
	}
	sort.SliceStable(records, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339Nano, records[i].Timestamp)
		right, rightErr := time.Parse(time.RFC3339Nano, records[j].Timestamp)
		return leftErr == nil && rightErr == nil && left.Before(right)
	})
	return records, nil
}

func rawRecord(value any) map[string]any {
	data, _ := json.Marshal(value)
	raw := map[string]any{}
	_ = json.Unmarshal(data, &raw)
	return raw
}
