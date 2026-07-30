package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestMCPStructuredErrorRetryableClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		kind      structuredErrorKind
		retryable bool
	}{
		{name: "permanent", err: fmt.Errorf("usage: workspace.exec requires name"), kind: errorKindPermanent, retryable: false},
		{name: "conflict", err: fmt.Errorf("workspace demo is already running"), kind: errorKindConflict, retryable: false},
		{name: "not found", err: workspace.WorkspaceNotFoundError{Name: "missing"}, kind: errorKindNotFound, retryable: false},
		{name: "resource exhausted", err: fmt.Errorf("requested disk size exceeds limit"), kind: errorKindResourceExhausted, retryable: true},
		{name: "unsupported", err: fmt.Errorf("microagent exec is not supported"), kind: errorKindUnsupported, retryable: false},
		{name: "policy denied", err: fmt.Errorf("permission denied"), kind: errorKindPolicyDenied, retryable: false},
		{name: "transient", err: fmt.Errorf("connection refused"), kind: errorKindTransient, retryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapStructuredError(tt.err, "req-test")
			if got.Kind != tt.kind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.kind)
			}
			if got.Retryable != tt.retryable {
				t.Fatalf("Retryable = %v, want %v", got.Retryable, tt.retryable)
			}
			if got.Message != tt.err.Error() {
				t.Fatalf("Message = %q, want %q", got.Message, tt.err.Error())
			}
			if got.CorrelationID != "req-test" {
				t.Fatalf("CorrelationID = %q, want req-test", got.CorrelationID)
			}
			if strings.TrimSpace(got.Remediation) == "" {
				t.Fatalf("Remediation is empty")
			}
		})
	}
}

func TestMCPStructuredErrorRetryableAlwaysMarshaled(t *testing.T) {
	for _, err := range []error{
		workspace.WorkspaceNotFoundError{Name: "missing"},
		fmt.Errorf("connection refused"),
	} {
		data, marshalErr := json.Marshal(mapStructuredError(err, "req-test"))
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"kind", "message", "remediation", "correlation_id", "retryable"} {
			if _, ok := decoded[field]; !ok {
				t.Fatalf("error %q missing field %q after marshal: %s", err, field, data)
			}
		}
	}
}

func TestMCPWorkspaceExecErrorKinds(t *testing.T) {
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name":      "missing",
		"state_dir": t.TempDir(),
		"argv":      []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want not found")
	}
	structured, ok := envelope["error"].(structuredError)
	if !ok {
		t.Fatalf("error type = %T", envelope["error"])
	}
	if structured.Kind != errorKindNotFound {
		t.Fatalf("kind = %q, want not_found", structured.Kind)
	}
	if structured.Retryable {
		t.Fatalf("retryable = true, want false")
	}
}

func TestMCPWorkspaceExecToolCallErrorMapsKind(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "workspace.exec",
			"arguments": map[string]any{
				"name": "missing",
				"argv": []string{"true"},
			},
		},
	}))
	var output bytes.Buffer
	ctx := withMCPHostConfig(context.Background(), mcpHostConfig{StateDir: t.TempDir()})
	if err := serveMCP(ctx, input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %#v, want JSON-RPC error", responses[0])
	}
	data, ok := errObj["data"].(map[string]any)
	if !ok || data["kind"] != string(errorKindNotFound) {
		t.Fatalf("error data = %#v", errObj["data"])
	}
	if data["message"] == "" || data["remediation"] == "" || data["correlation_id"] == "" {
		t.Fatalf("error data missing required fields: %#v", data)
	}
	retryable, ok := data["retryable"].(bool)
	if !ok {
		t.Fatalf("retryable missing or non-boolean: %#v", data)
	}
	if retryable {
		t.Fatalf("retryable = true, want false: %#v", data)
	}
}

// TestMCPEnvelopeShapeSuccess pins the unified success envelope: a cheap
// read-only tool returns {ok:true, result, meta{timing_ms, principal_context}}
// with the transport fields under meta, not beside result.
func TestMCPEnvelopeShapeSuccess(t *testing.T) {
	envelope, err := runMCPTool(context.Background(), "images.list", map[string]any{
		"state_dir": t.TempDir(),
		"principal": map[string]any{"workload_identity": "agent-1"},
	})
	if err != nil {
		t.Fatalf("runMCPTool images.list: %v", err)
	}
	if envelope["ok"] != true {
		t.Fatalf("ok = %#v, want true", envelope["ok"])
	}
	if _, hasResult := envelope["result"]; !hasResult {
		t.Fatalf("envelope missing result: %#v", envelope)
	}
	if _, hasErr := envelope["error"]; hasErr {
		t.Fatalf("success envelope carries error: %#v", envelope)
	}
	// Transport concerns live under meta, not beside result.
	if _, beside := envelope["timing_ms"]; beside {
		t.Fatalf("timing_ms must live under meta, not top-level: %#v", envelope)
	}
	if _, beside := envelope["principal_context"]; beside {
		t.Fatalf("principal_context must live under meta, not top-level: %#v", envelope)
	}
	meta := mcpEnvelopeMeta(t, envelope)
	if _, ok := meta["timing_ms"]; !ok {
		t.Fatalf("meta missing timing_ms: %#v", meta)
	}
	principal, ok := meta["principal_context"].(map[string]any)
	if !ok {
		t.Fatalf("meta.principal_context type = %T", meta["principal_context"])
	}
	if principal["workload_identity"] != "agent-1" {
		t.Fatalf("meta.principal_context = %#v", principal)
	}
}

// TestMCPEnvelopeShapeError pins the unified error envelope over MCP: an unknown
// workspace surfaces as a JSON-RPC error whose data is the structuredError
// (kind + fields) with the transport meta block attached as a sibling.
func TestMCPEnvelopeShapeError(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "workspace.inspect",
			"arguments": map[string]any{
				"name": "does-not-exist",
			},
		},
	}))
	var output bytes.Buffer
	ctx := withMCPHostConfig(context.Background(), mcpHostConfig{StateDir: t.TempDir()})
	if err := serveMCP(ctx, input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %#v, want JSON-RPC error", responses[0])
	}
	data, ok := errObj["data"].(map[string]any)
	if !ok {
		t.Fatalf("error data = %#v", errObj["data"])
	}
	if data["kind"] != string(errorKindNotFound) {
		t.Fatalf("error data kind = %#v, want not_found", data["kind"])
	}
	for _, field := range []string{"message", "remediation", "correlation_id", "retryable"} {
		if _, ok := data[field]; !ok {
			t.Fatalf("error data missing structuredError field %q: %#v", field, data)
		}
	}
	meta, ok := data["meta"].(map[string]any)
	if !ok {
		t.Fatalf("error data missing meta block: %#v", data)
	}
	if _, ok := meta["timing_ms"]; !ok {
		t.Fatalf("error meta missing timing_ms: %#v", meta)
	}
	if _, ok := meta["principal_context"]; !ok {
		t.Fatalf("error meta missing principal_context: %#v", meta)
	}
}

func TestMCPDescribeWorkspaceExecStructuredSchema(t *testing.T) {
	manifest := microagentCapabilityManifest()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"workspace.exec", "argv", "exit_code", "stdout_truncated", "contentEncoding"} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q: %s", want, text)
		}
	}
}

func TestMCPWorkspaceExecLegacyCommandStringUsesExplicitShell(t *testing.T) {
	req, err := mcpExecRequest(map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(req.Argv, " ") != "sh -lc echo hello" {
		t.Fatalf("argv = %#v", req.Argv)
	}
}

func TestMCPPrincipalContext(t *testing.T) {
	got := principalContextArg(map[string]any{"principal": map[string]any{
		"workload_identity":   "agent-1",
		"delegated_authority": "user",
		"purpose":             "test",
		"correlation_id":      "corr-1",
		"ignored":             "value",
	}})
	if got["workload_identity"] != "agent-1" || got["correlation_id"] != "corr-1" {
		t.Fatalf("principal = %#v", got)
	}
	if _, ok := got["ignored"]; ok {
		t.Fatalf("principal retained unexpected field: %#v", got)
	}
}

func TestMCPPingTool(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params":  map[string]any{"name": "microagent.ping"},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	data, err := json.Marshal(responses[0]["result"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pong") {
		t.Fatalf("tool call response = %s, want pong", data)
	}
}

func decodeMCPTestResponses(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	reader := mcpStdioServer{in: bufio.NewReader(bytes.NewReader(data))}
	var responses []map[string]any
	for {
		msg, err := reader.readMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var response map[string]any
		if err := json.Unmarshal(msg, &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		responses = append(responses, response)
	}
	return responses
}

// TestMCPEgressLockAllowlistFlag is the B10 guard: the workspace.create and
// workspace.dispatch tools can express egress_lock_allowlist (so an agent that
// sets it actually locks the allowlist), and omit the flag when it is not set.
func TestMCPEgressLockAllowlistFlag(t *testing.T) {
	createOpts, err := mcpWorkspaceCreateOptions(map[string]any{
		"name": "demo", "egress": "broker",
		"egress_allow": []any{"api.anthropic.com"}, "egress_lock_allowlist": true,
	})
	if err != nil {
		t.Fatalf("workspace.create options: %v", err)
	}
	if !createOpts.EgressAllowlistLocked {
		t.Fatal("workspace.create did not lock the allowlist")
	}
	opts, err := mcpWorkspaceDispatchOptions(map[string]any{
		"image": "docker.io/library/alpine:3.20", "egress": "broker",
		"egress_allow": []any{"api.anthropic.com"}, "egress_lock_allowlist": true,
	})
	if err != nil {
		t.Fatalf("workspace.dispatch options: %v", err)
	}
	if !opts.EgressAllowlistLocked {
		t.Fatal("workspace.dispatch did not lock the allowlist")
	}
}
