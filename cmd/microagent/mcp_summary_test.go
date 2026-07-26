package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPSummarizeWorkspaceInspect(t *testing.T) {
	// No egress audit log under this state dir, so egress_summary is omitted.
	summary, ok := summarizeWorkspaceInspect(map[string]any{
		"ok":      true,
		"backend": "linux-kvm",
		"event": map[string]any{
			"state":    "running",
			"identity": map[string]any{"runtimeID": "demo"},
		},
	}, t.TempDir(), "demo").(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["workspace"] != "demo" || summary["state"] != "running" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, present := summary["egress_summary"]; present {
		t.Fatalf("egress_summary should be omitted when no audit log exists: %#v", summary)
	}
	points, ok := summary["next_decision_points"].([]string)
	if !ok || len(points) == 0 {
		t.Fatalf("next_decision_points = %#v", summary["next_decision_points"])
	}
}

func TestMCPSummarizeWorkspaceInspectIncludesEgressSummary(t *testing.T) {
	stateDir := t.TempDir()
	dir := filepath.Join(stateDir, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"event":"egress_allow","ts":"2026-06-16T00:00:00Z","host":"api.github.com"}` + "\n" +
		`{"event":"egress_allow","ts":"2026-06-16T00:00:01Z","host":"api.github.com"}` + "\n" +
		`{"event":"egress_deny","ts":"2026-06-16T00:00:02Z","host":"evil.example","reason":"blocked"}` + "\n" +
		`{"event":"egress_dns_deny","ts":"2026-06-16T00:00:03Z","qname":"tracker.example"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "egress-access.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, ok := summarizeWorkspaceInspect(map[string]any{
		"ok":      true,
		"backend": "linux-kvm",
		"event": map[string]any{
			"state":    "running",
			"identity": map[string]any{"runtimeID": "demo"},
		},
	}, stateDir, "demo").(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	eg, ok := summary["egress_summary"].(map[string]any)
	if !ok {
		t.Fatalf("egress_summary missing or wrong type: %#v", summary["egress_summary"])
	}
	if eg["decision_count"] != 4 {
		t.Fatalf("decision_count = %#v, want 4", eg["decision_count"])
	}
	byEvent, ok := eg["by_event"].(map[string]int)
	if !ok || byEvent["egress_allow"] != 2 || byEvent["egress_deny"] != 1 || byEvent["egress_dns_deny"] != 1 {
		t.Fatalf("by_event = %#v", eg["by_event"])
	}
	allow, ok := eg["allow_by_host"].(map[string]int)
	if !ok || allow["api.github.com"] != 2 {
		t.Fatalf("allow_by_host = %#v", eg["allow_by_host"])
	}
	deny, ok := eg["deny_by_host"].(map[string]int)
	if !ok || deny["evil.example"] != 1 {
		t.Fatalf("deny_by_host = %#v", eg["deny_by_host"])
	}
}

func TestMCPSummarizeWorkspaceCreateLifecycle(t *testing.T) {
	summary, ok := summarizeWorkspaceLifecycle(map[string]any{
		"workspace":   "demo",
		"rootfs_path": "/tmp/rootfs.ext4",
		"response": map[string]any{
			"ok":      true,
			"backend": "linux-kvm",
			"event": map[string]any{
				"state":  "stopped",
				"detail": "workspace created",
			},
		},
	}, "created").(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["outcome"] != "created" || summary["workspace"] != "demo" || summary["ready"] != true {
		t.Fatalf("summary = %#v", summary)
	}
	if summary["state_meaning"] != "created and ready to start" {
		t.Fatalf("state_meaning = %#v", summary["state_meaning"])
	}
	points, ok := summary["next_decision_points"].([]string)
	if !ok || len(points) == 0 || points[0] != "workspace.start" {
		t.Fatalf("next_decision_points = %#v", summary["next_decision_points"])
	}
}

func TestMCPSummarizeWorkspaceLogs(t *testing.T) {
	summary, ok := summarizeWorkspaceLogs(map[string]any{
		"workspace": "demo",
		"logs":      "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n",
	}, 3).(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["workspace"] != "demo" || summary["line_count"] != 9 {
		t.Fatalf("summary = %#v", summary)
	}
	tail, ok := summary["tail_lines"].([]string)
	if !ok || len(tail) != 3 || tail[0] != "seven" || tail[2] != "nine" {
		t.Fatalf("tail_lines = %#v", summary["tail_lines"])
	}
}

func TestMCPSummarizeWorkspaceEvents(t *testing.T) {
	summary, ok := summarizeWorkspaceEvents(map[string]any{
		"workspace": "demo",
		"events": []any{
			map[string]any{"state": "prepared", "observedAt": "2026-06-04T00:00:00Z"},
			map[string]any{"state": "running", "observedAt": "2026-06-04T00:00:01Z", "detail": "serial=serial.log"},
			map[string]any{"state": "halted", "observedAt": "2026-06-04T00:00:02Z"},
		},
	}, 2, 1).(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["workspace"] != "demo" || summary["event_count"] != 3 || summary["latest_state"] != "halted" {
		t.Fatalf("summary = %#v", summary)
	}
	recent, ok := summary["recent"].([]any)
	if !ok || len(recent) != 2 {
		t.Fatalf("recent = %#v", summary["recent"])
	}
	if summary["next_after_index"] != 3 || summary["after_index"] != 1 {
		t.Fatalf("cursor fields = %#v", summary)
	}
}

// TestMCPDescribeTool is F3: microagent.describe's response is now the same
// unified {ok, result, meta} envelope as every other tool - the manifest
// moves under .result, alongside the transport meta block (timing_ms,
// principal_context) instead of being the bare manifest object (see
// MIGRATION.md).
func TestMCPDescribeTool(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params":  map[string]any{"name": "microagent.describe", "arguments": map[string]any{"principal": map[string]any{"workload_identity": "agent-1"}}},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	envelope := decodeMCPToolResultEnvelope(t, responses[0])
	if envelope["ok"] != true {
		t.Fatalf("envelope.ok = %#v, want true", envelope["ok"])
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope.result type = %T, want the manifest object", envelope["result"])
	}
	if _, ok := result["schema_version"]; !ok {
		t.Fatalf("envelope.result missing schema_version (manifest fields): %#v", result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "workspace.create") {
		t.Fatalf("describe manifest = %s", data)
	}
	meta, ok := envelope["meta"].(map[string]any)
	if !ok {
		t.Fatalf("envelope.meta type = %T", envelope["meta"])
	}
	if _, ok := meta["timing_ms"]; !ok {
		t.Fatalf("envelope.meta missing timing_ms: %#v", meta)
	}
	principal, ok := meta["principal_context"].(map[string]any)
	if !ok {
		t.Fatalf("envelope.meta.principal_context type = %T", meta["principal_context"])
	}
	if principal["workload_identity"] != "agent-1" {
		t.Fatalf("envelope.meta.principal_context = %#v", principal)
	}
}

// decodeMCPToolResultEnvelope decodes a tools/call response's
// content[0].text (the JSON-encoded {ok, result, meta}/{ok, error, meta}
// envelope every tool now returns) into a map.
func decodeMCPToolResultEnvelope(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response = %#v, want a tool result", response)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result.content = %#v, want at least one entry", result["content"])
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] = %#v", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content[0].text type = %T", first["text"])
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("decode envelope %q: %v", text, err)
	}
	return envelope
}
