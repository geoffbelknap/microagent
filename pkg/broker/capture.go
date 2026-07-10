package broker

import (
	"bytes"
	"net/http"
	"time"
)

// DefaultCaptureBodyLimit bounds how much of a request body a capture record
// retains when no explicit limit is set.
const DefaultCaptureBodyLimit = 1 << 20 // 1 MiB

// CaptureRecord is the governed raw-capture emission: the full pre-swap
// request — path, headers with credential references verbatim, and a bounded
// prefix of the body. It exists only when an operator opts in; the default
// emission is the DecisionRecord. Capture is request-only: requests are
// pre-swap so the live credential is absent by construction, while responses
// have no swap point (an upstream could echo the injected credential back),
// so they are never captured.
type CaptureRecord struct {
	TS        time.Time   `json:"ts"`
	Mode      string      `json:"mode"`
	Host      string      `json:"host"`
	Method    string      `json:"method"`
	Path      string      `json:"path"`
	Headers   http.Header `json:"headers"`             // pre-swap: references, never live secrets
	Body      []byte      `json:"body,omitempty"`      // base64 in JSON, bounded
	Truncated bool        `json:"truncated,omitempty"` // body exceeded the limit
}

// OnCapture receives one CaptureRecord per terminate request when capture is
// enabled. CONNECT tunnels are opaque and produce no capture.
type OnCapture func(CaptureRecord)

// captureBuffer retains the first limit bytes written through it and flags
// any overflow; it never blocks or fails the write path.
type captureBuffer struct {
	limit     int64
	buf       bytes.Buffer
	truncated bool
}

func (c *captureBuffer) Write(p []byte) (int, error) {
	if room := c.limit - int64(c.buf.Len()); room > 0 {
		if int64(len(p)) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}
