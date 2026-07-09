package broker

import (
	"io"
	"time"
)

// Decision event names. The _allow/_deny suffix matches the egress mediator's
// event vocabulary, so per-host verdict rollups fold broker records in without
// special cases.
const (
	EventRequestAllow = "broker_request_allow"
	EventRequestDeny  = "broker_request_deny"
)

// DecisionRecord is the broker's default emission: one record per brokered
// request, verdict plus minimized metadata. It deliberately has no field for
// path, headers, or bodies — the default stream is safe to tee, persist, and
// export because content cannot appear in it by schema, and the live secret
// cannot appear in it by construction (SecretRefs carries reference names,
// never values).
type DecisionRecord struct {
	Event      string    `json:"event"`   // broker_request_allow | broker_request_deny
	TS         time.Time `json:"ts"`
	Mode       string    `json:"mode"`    // terminate | connect
	Host       string    `json:"host"`    // upstream host
	Method     string    `json:"method"`  // request method (CONNECT for tunnels)
	Verdict    string    `json:"verdict"` // allow | deny
	Rule       string    `json:"rule,omitempty"`
	Status     int       `json:"status,omitempty"` // upstream status (terminate only)
	BytesOut   int64     `json:"bytes_out"`        // body bytes toward the upstream
	BytesIn    int64     `json:"bytes_in"`         // body bytes from the upstream
	DurationMs int64     `json:"duration_ms"`
	SecretRefs []string  `json:"secret_refs,omitempty"` // reference NAMES swapped, never values
	Signals    []string  `json:"signals,omitempty"`     // non-cooperation / workload-error signals
	Labels     []string  `json:"labels,omitempty"`      // classification labels from the policy verdict
}

// OnDecision receives one DecisionRecord per request, at request completion
// (a deny completes immediately).
type OnDecision func(DecisionRecord)

// countingReader counts the bytes read through it, for the decision record's
// byte totals.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
