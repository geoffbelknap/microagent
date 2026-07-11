package broker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCaptureRecordsPreSwapRequest: with capture enabled, the full pre-swap
// request is recorded — path, headers with the reference verbatim, body —
// while the upstream still receives the swapped header and the intact body.
func TestCaptureRecordsPreSwapRequest(t *testing.T) {
	var upstreamAuth, upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	var caps []CaptureRecord
	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"k": liveSecret}), nil)
	term.Client = upstream.Client()
	term.OnCapture = func(r CaptureRecord) { caps = append(caps, r) }
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("POST", broker.URL+"/v1/messages", strings.NewReader("hello body"))
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if upstreamAuth != "Bearer "+liveSecret || upstreamBody != "hello body" {
		t.Fatalf("upstream saw auth=%q body=%q — capture must not disturb the request", upstreamAuth, upstreamBody)
	}
	if len(caps) != 1 {
		t.Fatalf("capture records = %d, want exactly 1", len(caps))
	}
	c := caps[0]
	if c.Mode != "terminate" || c.Method != "POST" || c.Path != "/v1/messages" {
		t.Fatalf("capture metadata = %+v", c)
	}
	if got := c.Headers.Get("Authorization"); got != "Bearer "+RefPrefix+"k" {
		t.Fatalf("captured Authorization = %q, want the pre-swap reference", got)
	}
	if string(c.Body) != "hello body" || c.Truncated {
		t.Fatalf("captured body = %q (truncated=%v), want the full body", c.Body, c.Truncated)
	}
}

// TestCaptureTruncatesAtLimit: the capture buffer is bounded — the record
// keeps the first limit bytes and flags truncation, while the upstream still
// receives every byte.
func TestCaptureTruncatesAtLimit(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	var cap1 CaptureRecord
	term, _ := NewTerminate(upstream.URL, resolver(nil), nil)
	term.Client = upstream.Client()
	term.OnCapture = func(r CaptureRecord) { cap1 = r }
	term.CaptureBodyLimit = 8
	broker := httptest.NewServer(term)
	defer broker.Close()

	body := "0123456789abcdefghij" // 20 bytes
	resp, err := http.Post(broker.URL+"/x", "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if upstreamBody != body {
		t.Fatalf("upstream body = %q, want all %d bytes", upstreamBody, len(body))
	}
	if string(cap1.Body) != "01234567" || !cap1.Truncated {
		t.Fatalf("captured = %q (truncated=%v), want first 8 bytes + truncated flag", cap1.Body, cap1.Truncated)
	}
}

// TestCaptureNeverHoldsLiveSecret: the capture is pre-swap, so the live
// secret is absent from the serialized record by construction — the reference
// is what appears.
func TestCaptureNeverHoldsLiveSecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer upstream.Close()

	var rec CaptureRecord
	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"k": liveSecret}), nil)
	term.Client = upstream.Client()
	term.OnCapture = func(r CaptureRecord) { rec = r }
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("POST", broker.URL+"/x", strings.NewReader("body"))
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"k")
	req.Header.Set("X-Api-Key", RefPrefix+"k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	blob, _ := json.Marshal(rec)
	if strings.Contains(string(blob), liveSecret) {
		t.Fatalf("live secret leaked into the capture record: %s", blob)
	}
	if !strings.Contains(string(blob), RefPrefix+"k") {
		t.Fatalf("capture should carry the reference verbatim: %s", blob)
	}
}
