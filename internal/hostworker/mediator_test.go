package hostworker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediatorLocalAllowForwardsOpenAIPath(t *testing.T) {
	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.RequestURI()
		if r.Header.Get(requestIDHeader) == "" {
			t.Errorf("upstream request missing mediation request id")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"stub"}]}`))
	}))
	defer upstream.Close()
	logger := &BufferLogger{}
	handler, err := NewHandler(Options{
		TargetBaseURL: upstream.URL + "/v1",
		Mode:          ModeLocalAllow,
		WorkspaceID:   "ws",
		WorkerID:      "worker-1",
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamPath != "/v1/models" {
		t.Fatalf("upstream path = %q, want /v1/models", upstreamPath)
	}
	assertLogEvent(t, logger, "mediation_decision_allow")
	assertLogEvent(t, logger, "upstream_headers")
	assertLogEvent(t, logger, "request_end")
}

// TestMediatorFollowsResolvedUpstream covers the mediator's half of runner
// restart survival. The guest's vsock forward is pinned to the mediator, so a
// runner that moves to a new port has to be picked up here: the first request
// goes to the address the mediator started with, and once the resolver reports
// a new one, later requests follow it.
func TestMediatorFollowsResolvedUpstream(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer second.Close()

	var resolved atomic.Value
	resolved.Store("")
	logger := &BufferLogger{}
	handler, err := NewHandler(Options{
		TargetBaseURL:       first.URL + "/v1",
		Mode:                ModeLocalAllow,
		WorkspaceID:         "ws",
		Logger:              logger,
		ResolveUpstreamHost: func() string { return resolved.Load().(string) },
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// An empty resolution (no live runner recorded) must not fail the request:
	// the mediator keeps the address it started with.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/models", nil))
	if firstHits.Load() != 1 || secondHits.Load() != 0 {
		t.Fatalf("first request hits: first=%d second=%d", firstHits.Load(), secondHits.Load())
	}

	resolved.Store(strings.TrimPrefix(second.URL, "http://"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if secondHits.Load() != 1 {
		t.Fatalf("restarted runner not reached: first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
	assertLogEvent(t, logger, "upstream_target_changed")
	assertLogEvent(t, logger, "request_end")
}

func TestMediatorPolicyDenyFailsClosedBeforeUpstream(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope DecisionEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode policy envelope: %v", err)
		}
		if envelope.Workspace.ID != "ws" || envelope.Capability != DefaultCapability || envelope.Request.Path != "/chat/completions" {
			t.Errorf("unexpected envelope: %+v", envelope)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"deny","reason":"blocked","audit_event_id":"audit-1"}`))
	}))
	defer policy.Close()
	logger := &BufferLogger{}
	handler, err := NewHandler(Options{
		TargetBaseURL: upstream.URL + "/v1",
		Mode:          ModePolicy,
		PolicyURL:     policy.URL,
		WorkspaceID:   "ws",
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	body := bytes.NewBufferString(`{"messages":[{"role":"user","content":"secret prompt"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("upstream was called %d time(s)", upstreamHits.Load())
	}
	assertLogEvent(t, logger, "mediation_decision_deny")
	assertLogEvent(t, logger, "request_denied")
	for _, event := range logger.Events {
		if strings.Contains(eventString(event), "secret prompt") {
			t.Fatalf("audit event leaked request body: %+v", event)
		}
	}
}

func TestMediatorPolicyUnavailableFailsClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called when policy is unavailable")
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	policy.Close()
	handler, err := NewHandler(Options{
		TargetBaseURL: upstream.URL + "/v1",
		Mode:          ModePolicy,
		PolicyURL:     policy.URL,
		PolicyTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMediatorFilePolicyAllowInspectsStructuredRequest(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	policyFile := writePolicyFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny",
		"rules": [
			{
				"id": "chat-small",
				"effect": "allow",
				"match": {
					"workspace_ids": ["ws"],
					"capabilities": ["model.openai"],
					"methods": ["POST"],
					"paths": ["/chat/completions"],
					"models": ["tiny"]
				},
				"limits": {
					"max_request_bytes": 4096,
					"max_text_bytes": 128,
					"max_messages": 2,
					"max_tokens": 32,
					"stream": false,
					"allowed_tool_names": ["shell"]
				}
			}
		]
	}`)
	logger := &BufferLogger{}
	handler, err := NewHandler(Options{
		TargetBaseURL: upstream.URL + "/v1",
		Mode:          ModePolicy,
		PolicyFile:    policyFile,
		WorkspaceID:   "ws",
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	body := bytes.NewBufferString(`{"model":"tiny","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"short prompt"}],"tools":[{"type":"function","function":{"name":"shell"}}]}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits.Load())
	}
	assertLogEvent(t, logger, "mediation_decision_allow")
	assertLogField(t, logger, "mediation_decision_allow", "mediation_policy_rule_id", "chat-small")
	assertLogField(t, logger, "mediation_decision_request", "request_model", "tiny")
	for _, event := range logger.Events {
		if strings.Contains(eventString(event), "short prompt") {
			t.Fatalf("audit event leaked request body: %+v", event)
		}
	}
}

func TestMediatorFilePolicyDeniesExceededLimitBeforeUpstream(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	policyFile := writePolicyFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny",
		"rules": [
			{
				"id": "chat-cap",
				"effect": "allow",
				"match": {
					"methods": ["POST"],
					"paths": ["/chat/completions"],
					"models": ["tiny"]
				},
				"limits": {
					"max_tokens": 8
				}
			}
		]
	}`)
	logger := &BufferLogger{}
	handler, err := NewHandler(Options{
		TargetBaseURL: upstream.URL + "/v1",
		Mode:          ModePolicy,
		PolicyFile:    policyFile,
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	body := bytes.NewBufferString(`{"model":"tiny","max_tokens":16,"messages":[{"role":"user","content":"do not log this"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("upstream hits = %d", upstreamHits.Load())
	}
	assertLogEvent(t, logger, "mediation_decision_deny")
	assertLogField(t, logger, "mediation_decision_deny", "mediation_reason", "file_policy_limit_max_tokens")
	for _, event := range logger.Events {
		if strings.Contains(eventString(event), "do not log this") {
			t.Fatalf("audit event leaked request body: %+v", event)
		}
	}
}

func TestMediatorPolicyRejectsMultiplePolicySources(t *testing.T) {
	policyFile := writePolicyFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny"
	}`)
	_, err := NewHandler(Options{
		TargetBaseURL: "http://127.0.0.1:9000/v1",
		Mode:          ModePolicy,
		PolicyURL:     "http://127.0.0.1:9001/decision",
		PolicyFile:    policyFile,
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseEndpointRejectsCredentialsAndHTTPS(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1:8000/v1", "http://user:pass@127.0.0.1:8000/v1"} {
		if _, err := parseEndpointURL(raw, "target"); err == nil {
			t.Fatalf("parseEndpointURL(%q) succeeded", raw)
		}
	}
}

func assertLogEvent(t *testing.T, logger *BufferLogger, event string) {
	t.Helper()
	for _, row := range logger.Events {
		if row["event"] == event {
			return
		}
	}
	t.Fatalf("event %q not found in %+v", event, logger.Events)
}

func assertLogField(t *testing.T, logger *BufferLogger, event, field string, want any) {
	t.Helper()
	for _, row := range logger.Events {
		if row["event"] != event {
			continue
		}
		if row[field] != want {
			t.Fatalf("%s.%s = %#v, want %#v in %+v", event, field, row[field], want, row)
		}
		return
	}
	t.Fatalf("event %q not found in %+v", event, logger.Events)
}

func eventString(event map[string]any) string {
	data, _ := json.Marshal(event)
	return string(data)
}

func writePolicyFile(t *testing.T, body string) string {
	t.Helper()
	path := t.TempDir() + "/policy.json"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write policy file: %v", err)
	}
	return path
}
