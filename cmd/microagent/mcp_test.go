package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestMCPInitializeAndToolsList(t *testing.T) {
	input := bytes.NewBuffer(nil)
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}))
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want 2", len(responses))
	}
	if responses[0]["result"] == nil {
		t.Fatalf("initialize response missing result: %#v", responses[0])
	}
	result, ok := responses[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result = %#v", responses[1]["result"])
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) < 12 {
		t.Fatalf("tools = %#v, want initial tool set", result["tools"])
	}
	names := map[string]bool{}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool = %#v", raw)
		}
		names[tool["name"].(string)] = true
	}
	for _, name := range []string{"microagent.ping", "microagent.describe", "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.delete", "workspace.list", "workspace.inspect", "workspace.estimate_cost", "images.pull", "images.list", "cp", "artifacts.get"} {
		if !names[name] {
			t.Fatalf("tools missing %s: %#v", name, names)
		}
	}
}

func TestMCPWorkspaceEstimateCost(t *testing.T) {
	result := estimateWorkspaceCost(map[string]any{"profile": "tiny", "price_per_hour": 0.25})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "estimated_cost_hour") || !strings.Contains(string(data), "memory_mib") {
		t.Fatalf("estimate = %s", data)
	}
}

func TestMCPDeletePreview(t *testing.T) {
	result, err := runMCPTool(context.Background(), "workspace.delete", map[string]any{"name": "demo", "preview": true})
	if err != nil {
		t.Fatalf("runMCPTool preview: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"preview":true`) || !strings.Contains(string(data), "remove workspace disk and state") {
		t.Fatalf("preview = %s", data)
	}
}

func TestMCPSummarizeWorkspaceInspect(t *testing.T) {
	summary, ok := summarizeWorkspaceInspect(map[string]any{
		"ok":      true,
		"backend": "firecracker",
		"event": map[string]any{
			"state":    "running",
			"identity": map[string]any{"runtimeID": "demo"},
		},
	}).(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["workspace"] != "demo" || summary["state"] != "running" {
		t.Fatalf("summary = %#v", summary)
	}
	points, ok := summary["next_decision_points"].([]string)
	if !ok || len(points) == 0 {
		t.Fatalf("next_decision_points = %#v", summary["next_decision_points"])
	}
}

func TestMCPDescribeTool(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params":  map[string]any{"name": "microagent.describe"},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	data, err := json.Marshal(responses[0]["result"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "schema_version") || !strings.Contains(string(data), "workspace.create") {
		t.Fatalf("describe response = %s", data)
	}
}

func TestMCPIdempotencyCache(t *testing.T) {
	t.Cleanup(func() { mcpIdempotencyCache = sync.Map{} })
	key := "workspace.create:test-key"
	mcpIdempotencyCache.Store(key, map[string]any{"result": map[string]any{"workspace": "cached"}})
	result, err := runMCPTool(context.Background(), "workspace.create", map[string]any{"name": "demo", "idempotency_key": "test-key"})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if result["idempotency_replay"] != true {
		t.Fatalf("idempotency_replay = %#v", result["idempotency_replay"])
	}
}

func TestMCPWorkspaceExecReturnsStructuredResult(t *testing.T) {
	seen := make(chan execprotocol.ExecRequest, 1)
	_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		seen <- req
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stdout = []byte("Linux demo\n")
		return result
	})
	defer stop()
	stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, port)
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name":      "research",
		"state_dir": stateDir,
		"argv":      []any{"uname", "-a"},
		"env":       map[string]any{"TEST_VAR": "hello"},
		"cwd":       "/tmp",
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	result, ok := envelope["result"].(execprotocol.ExecResult)
	if !ok {
		t.Fatalf("result type = %T", envelope["result"])
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || string(result.Stdout) != "Linux demo\n" || result.Status != execprotocol.ExecStatusExited {
		t.Fatalf("result = %#v", result)
	}
	req := <-seen
	if strings.Join(req.Argv, " ") != "uname -a" || req.Env["TEST_VAR"] != "hello" || req.Cwd != "/tmp" {
		t.Fatalf("request = %#v", req)
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
}

func TestMCPWorkspaceExecToolCallErrorMapsKind(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "workspace.exec",
			"arguments": map[string]any{
				"name":      "missing",
				"state_dir": t.TempDir(),
				"argv":      []string{"true"},
			},
		},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
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
