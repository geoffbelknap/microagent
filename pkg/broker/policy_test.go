package broker

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPolicyDenyBlocksBeforeUpstream: a policy deny returns 403 and emits a
// deny record with the policy's rule; nothing reaches the upstream — the
// content was evaluated inside the boundary and only the verdict left.
func TestPolicyDenyBlocksBeforeUpstream(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()

	var recs []DecisionRecord
	var sawPath string
	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"k": liveSecret}), nil)
	term.Client = upstream.Client()
	term.OnDecision = func(r DecisionRecord) { recs = append(recs, r) }
	term.Policy = func(tap TapRecord) (Verdict, error) {
		sawPath = tap.Path // the policy sees content-adjacent detail in-boundary
		return Verdict{Allow: false, Rule: "test-block"}, nil
	}
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("POST", broker.URL+"/v1/messages", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if called {
		t.Fatal("upstream was called despite a policy deny")
	}
	if sawPath != "/v1/messages" {
		t.Fatalf("policy saw path %q, want the pre-swap request", sawPath)
	}
	if len(recs) != 1 || recs[0].Verdict != "deny" || recs[0].Rule != "test-block" {
		t.Fatalf("records = %+v, want one deny with the policy rule", recs)
	}
}

// TestPolicyLabelsFlowIntoAllowRecord: an allowing policy annotates the
// decision stream via labels — classification without content.
func TestPolicyLabelsFlowIntoAllowRecord(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer upstream.Close()

	var rec DecisionRecord
	term, _ := NewTerminate(upstream.URL, resolver(nil), nil)
	term.Client = upstream.Client()
	term.OnDecision = func(r DecisionRecord) { rec = r }
	term.Policy = func(tap TapRecord) (Verdict, error) {
		return Verdict{Allow: true, Rule: "model-endpoint", Labels: []string{"model-api"}}, nil
	}
	broker := httptest.NewServer(term)
	defer broker.Close()

	resp, err := http.Get(broker.URL + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if rec.Verdict != "allow" || rec.Rule != "model-endpoint" {
		t.Fatalf("verdict/rule = %q/%q", rec.Verdict, rec.Rule)
	}
	if len(rec.Labels) != 1 || rec.Labels[0] != "model-api" {
		t.Fatalf("labels = %v, want [model-api]", rec.Labels)
	}
}

// TestPolicyErrorDeniesFailClosed: a policy error can never widen reach — the
// request is denied.
func TestPolicyErrorDeniesFailClosed(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()

	var recs []DecisionRecord
	term, _ := NewTerminate(upstream.URL, resolver(nil), nil)
	term.Client = upstream.Client()
	term.OnDecision = func(r DecisionRecord) { recs = append(recs, r) }
	term.Policy = func(tap TapRecord) (Verdict, error) { return Verdict{}, errors.New("evaluator down") }
	broker := httptest.NewServer(term)
	defer broker.Close()

	resp, err := http.Get(broker.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden || called {
		t.Fatalf("status = %d, upstream called = %v; want 403 and no upstream", resp.StatusCode, called)
	}
	if len(recs) != 1 || recs[0].Verdict != "deny" || recs[0].Rule != "policy-error" {
		t.Fatalf("records = %+v, want one deny with rule policy-error", recs)
	}
}

// TestPolicyPanicDeniesFailClosed: even a panicking policy denies rather than
// letting the request through.
func TestPolicyPanicDeniesFailClosed(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()

	term, _ := NewTerminate(upstream.URL, resolver(nil), nil)
	term.Client = upstream.Client()
	term.Policy = func(tap TapRecord) (Verdict, error) { panic("boom") }
	broker := httptest.NewServer(term)
	defer broker.Close()

	resp, err := http.Get(broker.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden || called {
		t.Fatalf("status = %d, upstream called = %v; want 403 and no upstream", resp.StatusCode, called)
	}
}

// TestPolicyDenyOnConnect: the seam guards tunnels too — a denied CONNECT
// never dials.
func TestPolicyDenyOnConnect(t *testing.T) {
	dialed := false
	var recs []DecisionRecord
	conn := &Connect{
		OnDecision: func(r DecisionRecord) { recs = append(recs, r) },
		Policy:     func(tap TapRecord) (Verdict, error) { return Verdict{Allow: false, Rule: "no-tunnels"}, nil },
		Dial: func(network, addr string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unreachable")
		},
	}
	broker := httptest.NewServer(Handler(nil, conn))
	defer broker.Close()

	req, _ := http.NewRequest(http.MethodConnect, broker.URL, nil)
	req.Host = "example.com:443"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden || dialed {
		t.Fatalf("status = %d, dialed = %v; want 403 and no dial", resp.StatusCode, dialed)
	}
	if len(recs) != 1 || recs[0].Verdict != "deny" || recs[0].Rule != "no-tunnels" {
		t.Fatalf("records = %+v, want one deny with rule no-tunnels", recs)
	}
}
