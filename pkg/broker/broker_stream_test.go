package broker

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// chunkedBody is an io.ReadCloser that yields a fixed sequence of chunks, one
// per Read call, so a test can drive a "streaming" upstream response without
// any timing dependence.
type chunkedBody struct {
	chunks [][]byte
	i      int
}

func (c *chunkedBody) Read(p []byte) (int, error) {
	if c.i >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.i])
	c.i++
	return n, nil
}

func (c *chunkedBody) Close() error { return nil }

// fixedRoundTripper returns a canned response for every request, standing in
// for a real upstream so the response body's chunking is fully deterministic.
type fixedRoundTripper struct{ resp *http.Response }

func (f *fixedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return f.resp, nil
}

// flushRecorder is an http.ResponseWriter + http.Flusher that records the
// sequence of Write and Flush calls, so a test can prove the relay flushes
// after each chunk instead of buffering until the handler returns.
type flushRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	events []string
}

func (f *flushRecorder) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}

func (f *flushRecorder) WriteHeader(status int) { f.status = status }

func (f *flushRecorder) Write(p []byte) (int, error) {
	f.events = append(f.events, fmt.Sprintf("w:%d", len(p)))
	return f.body.Write(p)
}

func (f *flushRecorder) Flush() { f.events = append(f.events, "flush") }

// noFlushRecorder is an http.ResponseWriter without http.Flusher, used to
// verify the relay still works (and still reports the correct byte count)
// when the writer doesn't support flushing.
type noFlushRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *noFlushRecorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *noFlushRecorder) WriteHeader(status int)      { r.status = status }
func (r *noFlushRecorder) Write(p []byte) (int, error) { return r.body.Write(p) }

func newStreamingTerminate(chunks [][]byte) *Terminate {
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       &chunkedBody{chunks: chunks},
	}
	term, _ := NewTerminate("https://x.invalid", resolver(nil), nil)
	term.Client = &http.Client{Transport: &fixedRoundTripper{resp: resp}}
	return term
}

// TestTerminateFlushesEachChunk is the deterministic proof that the terminate
// relay streams the upstream response instead of buffering it: with a
// response body that arrives across several Reads, the ResponseWriter must
// see a Flush after each Write, not one buffered write at the end.
func TestTerminateFlushesEachChunk(t *testing.T) {
	chunks := [][]byte{
		[]byte("event: a\n\n"),
		[]byte("event: b\n\n"),
		[]byte("event: c\n\n"),
	}
	term := newStreamingTerminate(chunks)

	var decision DecisionRecord
	term.OnDecision = func(d DecisionRecord) { decision = d }

	rec := &flushRecorder{}
	req := httptest.NewRequest("GET", "/v1/stream", nil)
	term.ServeHTTP(rec, req)

	if rec.status != 200 {
		t.Fatalf("status = %d, want 200", rec.status)
	}
	var want bytes.Buffer
	for _, c := range chunks {
		want.Write(c)
	}
	if rec.body.String() != want.String() {
		t.Fatalf("relayed body = %q, want %q", rec.body.String(), want.String())
	}

	// Every write must be followed by a flush — proof the client can see each
	// chunk as it arrives rather than after the whole body has buffered.
	var flushCount int
	for i, ev := range rec.events {
		if ev == "flush" {
			flushCount++
			continue
		}
		if i+1 >= len(rec.events) || rec.events[i+1] != "flush" {
			t.Fatalf("write %q at index %d not immediately followed by a flush: events = %v", ev, i, rec.events)
		}
	}
	if flushCount != len(chunks) {
		t.Fatalf("flush count = %d, want %d (one per chunk): events = %v", flushCount, len(chunks), rec.events)
	}

	// The existing decision record must still report the full relayed size.
	if want := int64(want.Len()); decision.BytesIn != want {
		t.Fatalf("decision.BytesIn = %d, want %d", decision.BytesIn, want)
	}
}

// TestTerminateStreamsWithoutFlusher verifies the fail-safe fallback: when
// the ResponseWriter doesn't implement http.Flusher, the relay still copies
// the full body and still reports the correct byte count.
func TestTerminateStreamsWithoutFlusher(t *testing.T) {
	chunks := [][]byte{[]byte("aaa"), []byte("bb"), []byte("c")}
	term := newStreamingTerminate(chunks)

	var decision DecisionRecord
	term.OnDecision = func(d DecisionRecord) { decision = d }

	rec := &noFlushRecorder{}
	req := httptest.NewRequest("GET", "/v1/stream", nil)
	term.ServeHTTP(rec, req)

	if rec.body.String() != "aaabbc" {
		t.Fatalf("relayed body = %q, want %q", rec.body.String(), "aaabbc")
	}
	if decision.BytesIn != int64(len("aaabbc")) {
		t.Fatalf("decision.BytesIn = %d, want %d", decision.BytesIn, len("aaabbc"))
	}
}
