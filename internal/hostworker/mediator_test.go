package hostworker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func eventString(event map[string]any) string {
	data, _ := json.Marshal(event)
	return string(data)
}
