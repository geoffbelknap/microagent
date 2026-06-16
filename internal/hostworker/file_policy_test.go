package hostworker

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFilePolicyConformance(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		path         string
		body         string
		wantStatus   int
		wantUpstream int32
		wantReason   string
	}{
		{
			name:         "allow models path",
			method:       http.MethodGet,
			path:         "/v1/models",
			wantStatus:   http.StatusOK,
			wantUpstream: 1,
		},
		{
			name:         "allow chat under limits",
			method:       http.MethodPost,
			path:         "/v1/chat/completions",
			body:         `{"model":"tiny","stream":false,"max_tokens":8,"messages":[{"role":"user","content":"short"}],"tools":[{"type":"function","function":{"name":"shell"}}]}`,
			wantStatus:   http.StatusOK,
			wantUpstream: 1,
		},
		{
			name:       "deny max tokens",
			method:     http.MethodPost,
			path:       "/v1/chat/completions",
			body:       `{"model":"tiny","stream":false,"max_tokens":64,"messages":[{"role":"user","content":"short"}]}`,
			wantStatus: http.StatusForbidden,
			wantReason: "file_policy_limit_max_tokens",
		},
		{
			name:       "deny oversized text",
			method:     http.MethodPost,
			path:       "/v1/chat/completions",
			body:       `{"model":"tiny","stream":false,"max_tokens":8,"messages":[{"role":"user","content":"this prompt is longer than the policy text limit"}]}`,
			wantStatus: http.StatusForbidden,
			wantReason: "file_policy_limit_text_bytes",
		},
		{
			name:       "deny unknown tool",
			method:     http.MethodPost,
			path:       "/v1/chat/completions",
			body:       `{"model":"tiny","stream":false,"max_tokens":8,"messages":[{"role":"user","content":"short"}],"tools":[{"type":"function","function":{"name":"network"}}]}`,
			wantStatus: http.StatusForbidden,
			wantReason: "file_policy_limit_tool_name",
		},
		{
			name:       "deny streaming",
			method:     http.MethodPost,
			path:       "/v1/chat/completions",
			body:       `{"model":"tiny","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"short"}]}`,
			wantStatus: http.StatusForbidden,
			wantReason: "file_policy_limit_stream",
		},
		{
			name:       "deny unmatched model",
			method:     http.MethodPost,
			path:       "/v1/chat/completions",
			body:       `{"model":"other","stream":false,"max_tokens":8,"messages":[{"role":"user","content":"short"}]}`,
			wantStatus: http.StatusForbidden,
			wantReason: "file_policy_default_deny",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamHits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()
			logger := &BufferLogger{}
			handler, err := NewHandler(Options{
				TargetBaseURL: upstream.URL,
				Mode:          ModePolicy,
				PolicyFile:    conformancePolicyFile(t),
				WorkspaceID:   "ws",
				WorkerID:      "worker",
				Logger:        logger,
			})
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}

			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = bytes.NewBuffer(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if upstreamHits.Load() != tc.wantUpstream {
				t.Fatalf("upstream hits = %d, want %d", upstreamHits.Load(), tc.wantUpstream)
			}
			if tc.wantReason != "" {
				assertLogField(t, logger, "mediation_decision_deny", "mediation_reason", tc.wantReason)
			}
			for _, event := range logger.Events {
				if strings.Contains(eventString(event), "this prompt") || strings.Contains(eventString(event), "short") {
					t.Fatalf("audit event leaked request body: %+v", event)
				}
			}
		})
	}
}

func TestFilePolicyInvalidOrMissingFileFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{name: "missing", path: t.TempDir() + "/missing.json"},
		{name: "invalid json", body: `{"schema_version":`},
		{name: "invalid schema", body: `{"schema_version":"wrong","default":"allow"}`},
		{name: "unknown field", body: `{"schema_version":"microagent.model_policy.v1","default":"allow","surprise":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = writePolicyFile(t, tc.body)
			}
			_, err := NewHandler(Options{
				TargetBaseURL: "http://127.0.0.1:9000/v1",
				Mode:          ModePolicy,
				PolicyFile:    path,
			})
			if err == nil {
				t.Fatalf("NewHandler succeeded for invalid policy %q", tc.name)
			}
		})
	}
}

func conformancePolicyFile(t *testing.T) string {
	t.Helper()
	return writePolicyFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny",
		"rules": [
			{
				"id": "models",
				"effect": "allow",
				"match": {
					"workspace_ids": ["ws"],
					"capabilities": ["model.openai"],
					"worker_ids": ["worker"],
					"methods": ["GET"],
					"paths": ["/v1/models"]
				}
			},
			{
				"id": "chat",
				"effect": "allow",
				"match": {
					"workspace_ids": ["ws"],
					"capabilities": ["model.openai"],
					"worker_ids": ["worker"],
					"methods": ["POST"],
					"paths": ["/v1/chat/completions"],
					"models": ["tiny"]
				},
				"limits": {
					"max_request_bytes": 4096,
					"max_text_bytes": 16,
					"max_messages": 2,
					"max_tokens": 16,
					"stream": false,
					"allowed_tool_names": ["shell"]
				}
			}
		]
	}`)
}
