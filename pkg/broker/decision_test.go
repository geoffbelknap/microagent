package broker

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestTerminateEmitsAllowDecision: a successful terminate request emits one
// decision record carrying verdict + minimized metadata — method, host,
// status, byte counts both ways, timing, and the NAMES of the references it
// swapped.
func TestTerminateEmitsAllowDecision(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "pong")
	}))
	defer upstream.Close()

	var recs []DecisionRecord
	term, err := NewTerminate(upstream.URL, resolver(map[string]string{"anthropic-key": liveSecret}), nil)
	if err != nil {
		t.Fatal(err)
	}
	term.Client = upstream.Client()
	term.OnDecision = func(r DecisionRecord) { recs = append(recs, r) }
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("POST", broker.URL+"/v1/messages", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"anthropic-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if len(recs) != 1 {
		t.Fatalf("decision records = %d, want exactly 1", len(recs))
	}
	rec := recs[0]
	if rec.Event != "broker_request_allow" || rec.Verdict != "allow" {
		t.Fatalf("event/verdict = %q/%q, want broker_request_allow/allow", rec.Event, rec.Verdict)
	}
	if rec.Mode != "terminate" || rec.Method != "POST" {
		t.Fatalf("mode/method = %q/%q", rec.Mode, rec.Method)
	}
	if want := strings.TrimPrefix(upstream.URL, "http://"); rec.Host != want {
		t.Fatalf("host = %q, want %q", rec.Host, want)
	}
	if rec.Status != 200 {
		t.Fatalf("status = %d, want 200", rec.Status)
	}
	if rec.BytesOut != int64(len("hello")) {
		t.Fatalf("bytes_out = %d, want %d", rec.BytesOut, len("hello"))
	}
	if rec.BytesIn != int64(len("pong")) {
		t.Fatalf("bytes_in = %d, want %d", rec.BytesIn, len("pong"))
	}
	if rec.TS.IsZero() {
		t.Fatal("ts is zero")
	}
	if len(rec.SecretRefs) != 1 || rec.SecretRefs[0] != "anthropic-key" {
		t.Fatalf("secret_refs = %v, want [anthropic-key]", rec.SecretRefs)
	}
}

// TestDecisionRecordIsMinimized: the serialized record carries no path, no
// headers, no bodies, and never the live secret — the default emission is
// metadata only, by schema, not by scrubbing.
func TestDecisionRecordIsMinimized(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer upstream.Close()

	var rec DecisionRecord
	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"k": liveSecret}), nil)
	term.Client = upstream.Client()
	term.OnDecision = func(r DecisionRecord) { rec = r }
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("POST", broker.URL+"/super/secret/path", strings.NewReader("secret body"))
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"k")
	req.Header.Set("X-Private", "private-header-value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	blob, _ := json.Marshal(rec)
	s := string(blob)
	for _, banned := range []string{liveSecret, "/super/secret/path", "private-header-value", "secret body", `"path"`, `"headers"`} {
		if strings.Contains(s, banned) {
			t.Fatalf("decision record carries %q — must be metadata only: %s", banned, s)
		}
	}
}

// TestConnectEmitsDecisionAtTunnelClose: a CONNECT tunnel emits exactly one
// record when it closes, with byte counts for both directions — metadata about
// an opaque stream, never its contents.
func TestConnectEmitsDecisionAtTunnelClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()

	got := make(chan DecisionRecord, 1)
	broker := httptest.NewServer(Handler(nil, &Connect{OnDecision: func(r DecisionRecord) { got <- r }}))
	defer broker.Close()

	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(broker.URL, "http://"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "CONNECT "+ln.Addr().String()+" HTTP/1.1\r\nHost: "+ln.Addr().String()+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	if status, err := br.ReadString('\n'); err != nil || !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status = %q, %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("tunnel echo: %v", err)
	}
	conn.Close()

	var rec DecisionRecord
	select {
	case rec = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no decision record emitted at tunnel close")
	}
	if rec.Event != "broker_request_allow" || rec.Verdict != "allow" {
		t.Fatalf("event/verdict = %q/%q", rec.Event, rec.Verdict)
	}
	if rec.Mode != "connect" || rec.Method != "CONNECT" || rec.Host != ln.Addr().String() {
		t.Fatalf("mode/method/host = %q/%q/%q", rec.Mode, rec.Method, rec.Host)
	}
	if rec.BytesOut != 4 || rec.BytesIn != 4 {
		t.Fatalf("bytes out/in = %d/%d, want 4/4", rec.BytesOut, rec.BytesIn)
	}
	if rec.Status != 0 || len(rec.SecretRefs) != 0 {
		t.Fatalf("tunnel record carries terminate-only fields: %+v", rec)
	}
}

// TestConnectEmitsDenyOnDialFailure: a refused upstream dial is a recorded
// deny, same rule vocabulary as terminate.
func TestConnectEmitsDenyOnDialFailure(t *testing.T) {
	var recs []DecisionRecord
	conn := &Connect{
		OnDecision: func(r DecisionRecord) { recs = append(recs, r) },
		Dial:       func(network, addr string) (net.Conn, error) { return nil, errors.New("refused") },
	}
	broker := httptest.NewServer(Handler(nil, conn))
	defer broker.Close()

	req, _ := http.NewRequest(http.MethodConnect, broker.URL, nil)
	req.Host = "203.0.113.7:443"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if len(recs) != 1 || recs[0].Verdict != "deny" || recs[0].Rule != "upstream-error" {
		t.Fatalf("records = %+v, want one deny with rule upstream-error", recs)
	}
	if recs[0].Mode != "connect" || recs[0].Host != "203.0.113.7:443" {
		t.Fatalf("mode/host = %q/%q", recs[0].Mode, recs[0].Host)
	}
}

// TestTerminateEmitsDenyOnUpstreamError: an unreachable upstream is recorded
// too — the stream has no silent gaps.
func TestTerminateEmitsDenyOnUpstreamError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close() // nothing listens here anymore

	var recs []DecisionRecord
	term, _ := NewTerminate(dead.URL, resolver(nil), nil)
	term.OnDecision = func(r DecisionRecord) { recs = append(recs, r) }
	broker := httptest.NewServer(term)
	defer broker.Close()

	resp, err := http.Get(broker.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if len(recs) != 1 || recs[0].Verdict != "deny" || recs[0].Rule != "upstream-error" {
		t.Fatalf("records = %+v, want one deny with rule upstream-error", recs)
	}
}

// TestTerminateEmitsDenyOnUnresolvedRef: the fail-closed path is itself a
// decision — an unresolvable reference emits a deny record with the rule and
// a workload-error signal, and nothing reaches the upstream.
func TestTerminateEmitsDenyOnUnresolvedRef(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()

	var recs []DecisionRecord
	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"known": liveSecret}), nil)
	term.Client = upstream.Client()
	term.OnDecision = func(r DecisionRecord) { recs = append(recs, r) }
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("GET", broker.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"unknown")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if called {
		t.Fatal("upstream was called despite an unresolved reference")
	}
	if len(recs) != 1 {
		t.Fatalf("decision records = %d, want exactly 1", len(recs))
	}
	rec := recs[0]
	if rec.Event != "broker_request_deny" || rec.Verdict != "deny" {
		t.Fatalf("event/verdict = %q/%q, want broker_request_deny/deny", rec.Event, rec.Verdict)
	}
	if rec.Rule != "unresolved-secret-ref" {
		t.Fatalf("rule = %q, want unresolved-secret-ref", rec.Rule)
	}
	if len(rec.Signals) != 1 || rec.Signals[0] != "unresolved-secret-ref" {
		t.Fatalf("signals = %v, want [unresolved-secret-ref]", rec.Signals)
	}
}
