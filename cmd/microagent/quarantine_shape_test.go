package main

import (
	"encoding/json"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// TestQuarantineEnvelopeKeepsResponseAtTopLevel locks quarantine's structured
// output shape. Adding the capture fields must be ADDITIVE: every field an
// existing consumer reads stays exactly where it was. Nesting the response
// under a key instead breaks every parser of `quarantine --json` silently —
// which is a mistake this test exists to have already caught once.
func TestQuarantineEnvelopeKeepsResponseAtTopLevel(t *testing.T) {
	env := quarantineEnvelope{
		Response: vmkit.Response{
			OK:      true,
			Backend: "linux-kvm",
			Event: &vmkit.Event{
				Identity: vmkit.Identity{RuntimeID: "agent-1", Backend: "linux-kvm"},
				State:    vmkit.StateQuarantined,
			},
		},
		Captured:   true,
		CaptureTag: "forensic-20260725-030000",
		Incident:   workspace.IncidentReceipt{RuntimeID: "agent-1", SessionID: "session-1", Complete: true},
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// The response must NOT be nested.
	if _, nested := got["response"]; nested {
		t.Fatalf("response is nested under a key; consumers read these at the top level: %s", raw)
	}
	for _, key := range []string{"ok", "backend", "event"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("top-level response field %q is missing: %s", key, raw)
		}
	}
	for _, key := range []string{"captured", "captureTag", "incident"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("capture field %q is missing: %s", key, raw)
		}
	}
	// captureError is omitted when there was no failure, so a successful
	// containment stays byte-identical to what consumers saw before.
	if _, ok := got["captureError"]; ok {
		t.Fatalf("captureError must be omitted when empty: %s", raw)
	}
}
