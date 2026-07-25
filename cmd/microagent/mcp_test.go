package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
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
	if !ok || len(tools) < 19 {
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
	for _, name := range []string{
		"microagent.ping", "microagent.describe",
		"workspace.create", "workspace.start", "workspace.wait", "workspace.exec", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.list", "workspace.inspect", "workspace.result", "workspace.stats", "workspace.logs", "workspace.events", "workspace.egress", "workspace.clone", "workspace.apply", "workspace.commit", "workspace.estimate_cost",
		"artifacts.list", "artifacts.get",
		"snapshot.create", "snapshot.list", "snapshot.delete",
		"network.inspect",
		"volume.create", "volume.list", "volume.inspect", "volume.delete",
		"images.pull", "images.list", "images.push", "images.tag", "images.delete", "images.prune",
		"models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners", "models.policy.validate", "models.policy.evaluate",
		"profiles.list", "host.inspect", "doctor.check", "contract.get", "kernel.verify", "kernel.install", "rootfs.build",
		"cp",
	} {
		if !names[name] {
			t.Fatalf("tools missing %s: %#v", name, names)
		}
	}
	if names["workspace.stop"] {
		t.Fatalf("workspace.stop must not be an MCP tool (folded into workspace.halt): %#v", names)
	}
}

// TestMCPWorkspaceStopFoldedIntoHalt is Plan 3 Task 2: workspace.stop is
// removed from the MCP surface (breaking; see MIGRATION.md). workspace.halt
// is the sole graceful-shutdown MCP tool going forward. This pins its
// absence from both tools/list and the microagent.describe manifest, and
// pins the presence of workspace.halt in both.
func TestMCPWorkspaceStopFoldedIntoHalt(t *testing.T) {
	manifest := microagentCapabilityManifest()
	operations, ok := manifest["operations"].([]map[string]any)
	if !ok {
		t.Fatalf("manifest operations type = %T", manifest["operations"])
	}
	opNames := map[string]bool{}
	for _, op := range operations {
		name, _ := op["name"].(string)
		opNames[name] = true
	}
	if opNames["workspace.stop"] {
		t.Fatalf("describe manifest must not list workspace.stop: %#v", opNames)
	}
	if !opNames["workspace.halt"] {
		t.Fatalf("describe manifest missing workspace.halt: %#v", opNames)
	}

	input := bytes.NewBuffer(nil)
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}))
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	result, ok := responses[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result = %#v", responses[1]["result"])
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %#v", result["tools"])
	}
	toolNames := map[string]bool{}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool = %#v", raw)
		}
		toolNames[tool["name"].(string)] = true
	}
	if toolNames["workspace.stop"] {
		t.Fatalf("tools/list must not list workspace.stop: %#v", toolNames)
	}
	if !toolNames["workspace.halt"] {
		t.Fatalf("tools/list missing workspace.halt: %#v", toolNames)
	}
}

// TestMCPWorkspaceStopCallProducesUnknownToolError pins the observed shape of
// calling the removed workspace.stop tool: it is not a special-cased
// tools/call error. It falls through mcpCLIArgs's default case
// (fmt.Errorf("unsupported MCP tool %s", name)), which runMCPTool returns
// before any `meta` block is ever computed, so mcpToolCallErrorData renders
// it as a bare structuredError with NO sibling `meta` (unlike tool errors
// that fail after CLI execution, which do carry timing_ms/principal_context)
// — this is the same envelope gap every mcpCLIArgs-time validation error
// already has (e.g. a missing required "name" argument), not something new.
// mapStructuredError's substring classifier tail matches "unsupported" in
// the message (the typed checks above it all miss), so kind comes back
// "unsupported", retryable false, with the pattern's fixed remediation text.
func TestMCPWorkspaceStopCallProducesUnknownToolError(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "workspace.stop",
			"arguments": map[string]any{"name": "does-not-matter"},
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
	if errObj["code"] != float64(-32602) {
		t.Fatalf("error code = %#v, want -32602", errObj["code"])
	}
	data, ok := errObj["data"].(map[string]any)
	if !ok {
		t.Fatalf("error data = %#v", errObj["data"])
	}
	if data["kind"] != string(errorKindUnsupported) {
		t.Fatalf("error data kind = %#v, want unsupported (substring classifier match on \"unsupported MCP tool\")", data["kind"])
	}
	message, _ := data["message"].(string)
	if !strings.Contains(message, "workspace.stop") {
		t.Fatalf("error data message = %#v, want it to name the unknown tool", data["message"])
	}
	if data["retryable"] != false {
		t.Fatalf("error data retryable = %#v, want false", data["retryable"])
	}
	if _, ok := data["remediation"]; !ok {
		t.Fatalf("error data missing remediation (substring classifier rule sets one): %#v", data)
	}
	if _, ok := data["correlation_id"]; !ok {
		t.Fatalf("error data missing correlation_id: %#v", data)
	}
	// mcpCLIArgs-time errors (including this unsupported-tool error) return
	// before runMCPTool ever computes a meta block, so unlike CLI-execution
	// failures there is no sibling meta here. Pin that gap rather than assert
	// a meta block that does not exist.
	if _, ok := data["meta"]; ok {
		t.Fatalf("error data unexpectedly has a meta block: %#v", data)
	}
}

func TestMCPInitializeEchoesSupportedRequestedProtocolVersion(t *testing.T) {
	input := bytes.NewBuffer(nil)
	input.Write(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"protocolVersion": "2025-03-26"},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result = %#v", responses[0]["result"])
	}
	if got := result["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("protocolVersion = %#v, want 2025-03-26", got)
	}
}

func TestRunServeMCPAllowsNonInteractiveStdio(t *testing.T) {
	input := bytes.NewBuffer(nil)
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}))
	var output bytes.Buffer
	if err := runServeMCP(context.Background(), nil, input, &output); err != nil {
		t.Fatalf("runServeMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	if responses[0]["result"] == nil {
		t.Fatalf("initialize response missing result: %#v", responses[0])
	}
}

func TestMCPRawJSONStdioInitializeAndToolsList(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
`)
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	decoder := json.NewDecoder(&output)
	var initResp map[string]any
	if err := decoder.Decode(&initResp); err != nil {
		t.Fatalf("decode initialize response: %v\n%s", err, output.String())
	}
	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result = %#v", initResp["result"])
	}
	if got := result["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("protocolVersion = %#v, want 2025-06-18", got)
	}
	var toolsResp map[string]any
	if err := decoder.Decode(&toolsResp); err != nil {
		t.Fatalf("decode tools/list response: %v\n%s", err, output.String())
	}
	result, ok = toolsResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result = %#v", toolsResp["result"])
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools = %#v, want non-empty list", result["tools"])
	}
	if strings.Contains(output.String(), "Content-Length:") {
		t.Fatalf("raw JSON response used Content-Length framing:\n%s", output.String())
	}
}

func TestMCPToolSchemasDoNotEmitNullRequired(t *testing.T) {
	for _, tool := range mcpTools() {
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s inputSchema = %#v", tool["name"], tool["inputSchema"])
		}
		required, ok := schema["required"]
		if !ok {
			continue
		}
		if _, ok := required.([]string); !ok {
			t.Fatalf("%s required = %#v, want string slice or omitted", tool["name"], required)
		}
	}
}

func TestMCPToolSchemasExposeSnapshotRestoreAndFork(t *testing.T) {
	for _, toolName := range []string{"workspace.create", "workspace.start"} {
		t.Run(toolName, func(t *testing.T) {
			schema := mcpToolInputSchema(t, toolName)
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s properties = %#v", toolName, schema["properties"])
			}
			if _, ok := properties["from_snapshot"]; !ok {
				t.Fatalf("%s schema missing from_snapshot: %#v", toolName, properties)
			}
		})
	}
}

func mcpToolInputSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, tool := range mcpTools() {
		if tool["name"] != name {
			continue
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s inputSchema = %#v", name, tool["inputSchema"])
		}
		return schema
	}
	t.Fatalf("missing MCP tool %s", name)
	return nil
}

func TestMCPToolsHaveLibraryFeatureContracts(t *testing.T) {
	for _, tool := range mcpTools() {
		name, ok := tool["name"].(string)
		if !ok || name == "" {
			t.Fatalf("tool missing name: %#v", tool)
		}
		if name == "microagent.ping" {
			continue
		}
		feature, ok := vmkit.FeatureForMCPTool(name)
		if !ok {
			t.Fatalf("MCP tool %s has no library feature contract", name)
		}
		if feature.OwnerPackage == "" {
			t.Fatalf("MCP tool %s maps to feature %s without owner package", name, feature.ID)
		}
		operation, ok := vmkit.OperationForMCPTool(name)
		if !ok {
			t.Fatalf("MCP tool %s has no library operation contract", name)
		}
		if operation.FeatureID != feature.ID {
			t.Fatalf("MCP tool %s operation feature = %s, want %s", name, operation.FeatureID, feature.ID)
		}
	}
}

func TestMCPManifestUsesLibraryOperationRegistry(t *testing.T) {
	manifest := microagentCapabilityManifest()
	operations := manifest["operations"].([]map[string]any)
	for _, entry := range operations {
		if entry["name"] != "workspace.pause" {
			continue
		}
		if entry["operation_id"] != vmkit.OperationWorkspacePause {
			t.Fatalf("workspace.pause operation ID = %#v", entry["operation_id"])
		}
		if entry["feature_id"] != "workspace.snapshot" {
			t.Fatalf("workspace.pause feature ID = %#v", entry["feature_id"])
		}
		capabilities, ok := entry["required_capabilities"].([]vmkit.FeatureCapability)
		if !ok || len(capabilities) != 1 || capabilities[0] != vmkit.FeatureCapabilityPauseResume {
			t.Fatalf("workspace.pause capabilities = %#v", entry["required_capabilities"])
		}
		return
	}
	t.Fatal("manifest missing workspace.pause")
}

func TestPrintServeMCPHelpPointsToClientSetup(t *testing.T) {
	var output bytes.Buffer
	printServeMCPHelp(&output)
	got := output.String()
	for _, want := range []string{
		"not an interactive",
		"Codex: codex mcp add microagent -- microagent serve mcp",
		"Claude Code: claude mcp add --transport stdio --scope user microagent -- microagent serve mcp",
		`stdio server named "microagent"`,
		"command: microagent",
		`args: ["serve", "mcp"]`,
		"docs/cli/serve.md#configure-mcp-clients",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("help missing %q:\n%s", want, got)
		}
	}
}

func TestPrintServeHelpTreatsMCPAsClientIntegration(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	printServeHelp(writeFile)
	if err := writeFile.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(readFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "mcp                 Serve the MCP stdio endpoint") {
		t.Fatalf("serve help missing mcp command:\n%s", got)
	}
	if strings.Contains(got, "model               ") {
		t.Fatalf("serve help advertises model command:\n%s", got)
	}
	if !strings.Contains(got, "MCP clients can launch") {
		t.Fatalf("serve help missing MCP integration note:\n%s", got)
	}
}

func TestRunServeRejectsModelServeAlias(t *testing.T) {
	err := runServe(context.Background(), []string{"model"}, nil)
	if err == nil {
		t.Fatal("runServe model succeeded, want unknown command error")
	}
	if !strings.Contains(err.Error(), "unknown serve command: model") {
		t.Fatalf("runServe model error = %q", err)
	}
}

func TestMCPManagementToolCLIArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{
			name: "workspace.create",
			args: map[string]any{"name": "demo", "image": "docker.io/library/python:3.13-slim", "model": "unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf", "model_token": "hf_test", "dry_run": true},
			want: []string{"--json", "create", "demo", "-image", "docker.io/library/python:3.13-slim", "-model", "unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf", "-model-token", "hf_test", "-dry-run"},
		},
		{
			name: "workspace.create",
			args: map[string]any{"name": "demo-fork", "from_snapshot": "demo:before-upgrade", "state_dir": "/tmp/state"},
			want: []string{"--json", "create", "demo-fork", "-from-snapshot", "demo:before-upgrade", "-state-dir", "/tmp/state"},
		},
		{
			// network mode maps through to create (t.Run disambiguates with #01)
			name: "workspace.create",
			args: map[string]any{"name": "demo", "image": "docker.io/library/busybox:1.36", "network": "isolated"},
			want: []string{"--json", "create", "demo", "-image", "docker.io/library/busybox:1.36", "-network", "isolated"},
		},
		{
			name: "workspace.create",
			args: map[string]any{
				"name":                      "demo",
				"model":                     "org/repo/model.gguf",
				"model_runner":              "vllm",
				"model_gpu":                 "auto",
				"model_runner_model":        "Qwen/Qwen2.5-0.5B-Instruct",
				"model_runner_served_model": "local-chat",
				"model_runner_args":         []any{"--max-model-len", "2048"},
				"model_runner_env":          []any{"CUDA_VISIBLE_DEVICES=0"},
				"model_mediation":           "policy",
				"model_policy_file":         "/tmp/model-policy.json",
				"model_policy_timeout":      "250ms",
			},
			want: []string{"--json", "create", "demo", "-model", "org/repo/model.gguf", "-model-runner", "vllm", "-model-gpu", "auto", "-model-runner-model", "Qwen/Qwen2.5-0.5B-Instruct", "-model-runner-served-model", "local-chat", "-model-runner-arg", "--max-model-len", "-model-runner-arg", "2048", "-model-runner-env", "CUDA_VISIBLE_DEVICES=0", "-model-mediation", "policy", "-model-policy-file", "/tmp/model-policy.json", "-model-policy-timeout", "250ms"},
		},
		{
			// egress + secret config maps through to create (t.Run disambiguates duplicate names)
			name: "workspace.create",
			args: map[string]any{
				"name":               "demo",
				"egress":             "mitm",
				"egress_allow":       []any{"api.anthropic.com", ".pypi.org"},
				"egress_passthrough": []any{"pinned.example.com"},
				"egress_policy":      "/tmp/egress.yaml",
				"egress_swap_config": "/tmp/swaps.yaml",
				"cred_swap":          []any{"anthropic", "openai=env:MY_OPENAI"},
				"secret":             []any{"ANTHROPIC_API_KEY=env:KEY"},
				"secret_on_demand":   []any{"DB=dotenv:/tmp/app.env#DB"},
				"secrets_env_file":   "/tmp/app.env",
				"secrets_audit":      true,
			},
			want: []string{"--json", "create", "demo", "-egress", "mitm", "-egress-policy", "/tmp/egress.yaml", "-egress-swap-config", "/tmp/swaps.yaml", "-secrets-env-file", "/tmp/app.env", "-secrets-audit", "-egress-allow", "api.anthropic.com", "-egress-allow", ".pypi.org", "-egress-passthrough", "pinned.example.com", "-cred-swap", "anthropic", "-cred-swap", "openai=env:MY_OPENAI", "-secret", "ANTHROPIC_API_KEY=env:KEY", "-secret-on-demand", "DB=dotenv:/tmp/app.env#DB"},
		},
		{
			// broker config maps through to create
			name: "workspace.create",
			args: map[string]any{
				"name":            "demo",
				"broker_upstream": "https://api.example.com",
				"broker_secret":   "api=env:MY_TOKEN",
				"broker_env":      []any{"EXAMPLE_BASE_URL", "OTHER_BASE_URL=http://127.0.0.1:18888/v1"},
				"broker_proxy":    true,
				"broker_capture":  true,
				"broker_ca":       "/etc/ssl/broker-ca.pem",
			},
			want: []string{"--json", "create", "demo", "-broker-upstream", "https://api.example.com", "-broker-secret", "api=env:MY_TOKEN", "-broker-ca", "/etc/ssl/broker-ca.pem", "-broker-proxy", "-broker-capture", "-broker-env", "EXAMPLE_BASE_URL", "-broker-env", "OTHER_BASE_URL=http://127.0.0.1:18888/v1"},
		},
		{
			// multi-endpoint brokers array maps through to create as repeated
			// -broker-endpoint flags (same grammar the CLI --broker-endpoint takes).
			name: "workspace.create",
			args: map[string]any{
				"name": "demo",
				"brokers": []any{
					"upstream=https://a.example.com;secret=a=env:A_TOKEN;base-url-env=A_BASE_URL",
					"upstream=https://b.example.com;secret=b=env:B_TOKEN;proxy",
				},
			},
			want: []string{"--json", "create", "demo", "-broker-endpoint", "upstream=https://a.example.com;secret=a=env:A_TOKEN;base-url-env=A_BASE_URL", "-broker-endpoint", "upstream=https://b.example.com;secret=b=env:B_TOKEN;proxy"},
		},
		{
			name: "workspace.start",
			args: map[string]any{
				"name":            "demo",
				"state_dir":       "/tmp/state",
				"from_snapshot":   "before-upgrade",
				"model_runner":    "llamacpp",
				"model_gpu":       "on",
				"model_mediation": "local-allow",
			},
			want: []string{"--json", "start", "demo", "-from-snapshot", "before-upgrade", "-model-runner", "llamacpp", "-model-gpu", "on", "-model-mediation", "local-allow", "-state-dir", "/tmp/state"},
		},
		{
			name: "workspace.wait",
			args: map[string]any{"name": "demo", "timeout": "5m", "interval": "2s", "state_dir": "/tmp/state"},
			want: []string{"--json", "wait", "demo", "-timeout", "5m", "-interval", "2s", "-state-dir", "/tmp/state"},
		},
		{
			name: "workspace.wait",
			args: map[string]any{"name": "demo"},
			want: []string{"--json", "wait", "demo"},
		},
		{
			name: "workspace.logs",
			args: map[string]any{"name": "demo", "state_dir": "/tmp/state"},
			want: []string{"--json", "logs", "demo", "-state-dir", "/tmp/state"},
		},
		{
			name: "workspace.events",
			args: map[string]any{"name": "demo"},
			want: []string{"--json", "events", "demo"},
		},
		{
			name: "workspace.egress",
			args: map[string]any{"name": "demo", "state_dir": "/tmp/state"},
			want: []string{"--json", "egress", "demo", "-state-dir", "/tmp/state"},
		},
		{
			name: "workspace.result",
			args: map[string]any{"name": "demo"},
			want: []string{"--json", "result", "demo"},
		},
		{
			name: "workspace.stats",
			args: map[string]any{"name": "demo"},
			want: []string{"--json", "stats", "demo"},
		},
		{
			name: "workspace.clone",
			args: map[string]any{"source": "demo", "target": "copy"},
			want: []string{"--json", "clone", "demo", "copy"},
		},
		{
			name: "workspace.apply",
			args: map[string]any{"file": "/tmp/microagent.yaml", "state_dir": "/tmp/state", "backend": "applevf", "arch": "arm64", "supervisor": "/tmp/helper"},
			want: []string{"--json", "apply", "-file", "/tmp/microagent.yaml", "-state-dir", "/tmp/state", "-backend", "applevf", "-arch", "arm64", "-supervisor", "/tmp/helper"},
		},
		{
			name: "workspace.commit",
			args: map[string]any{"name": "demo", "image": "example.com/acme/demo:rc", "state_dir": "/tmp/state", "arch": "arm64", "push": true},
			want: []string{"--json", "commit", "demo", "example.com/acme/demo:rc", "-state-dir", "/tmp/state", "-arch", "arm64", "-push"},
		},
		{
			name: "artifacts.list",
			args: map[string]any{"name": "demo", "state_dir": "/tmp/state"},
			want: []string{"--json", "artifact", "demo", "-state-dir", "/tmp/state"},
		},
		{
			name: "models.serve",
			args: map[string]any{
				"model":               "org/repo/model.gguf",
				"dedicated":           true,
				"runner":              "custom",
				"runner_gpu":          "auto",
				"runner_model":        "ignored/custom-model",
				"runner_served_model": "local-chat",
				"runner_command":      "runner serve {model} --listen {addr}",
				"runner_name":         "runner",
				"runner_health_path":  "/ready",
				"runner_args":         []any{"--gpu", "auto"},
				"runner_env":          []any{"CUDA_VISIBLE_DEVICES=0"},
				"state_dir":           "/tmp/state",
			},
			want: []string{"--json", "model", "serve", "org/repo/model.gguf", "-dedicated", "-runner", "custom", "-runner-gpu", "auto", "-runner-model", "ignored/custom-model", "-runner-served-model", "local-chat", "-runner-command", "runner serve {model} --listen {addr}", "-runner-name", "runner", "-runner-health-path", "/ready", "-runner-arg", "--gpu", "-runner-arg", "auto", "-runner-env", "CUDA_VISIBLE_DEVICES=0", "-state-dir", "/tmp/state"},
		},
		{
			name: "models.policy.evaluate",
			args: map[string]any{
				"policy_file":   "/tmp/policy.json",
				"method":        "POST",
				"request_path":  "/v1/chat/completions",
				"workspace_id":  "ws",
				"capability":    "model.openai",
				"worker_id":     "worker",
				"model":         "tiny",
				"request_bytes": float64(512),
				"text_bytes":    float64(128),
				"messages":      float64(1),
				"max_tokens":    float64(32),
				"stream":        false,
				"tools":         []any{"shell"},
				"expect":        "allow",
			},
			want: []string{"--json", "model", "policy", "evaluate", "/tmp/policy.json", "-method", "POST", "-path", "/v1/chat/completions", "-workspace-id", "ws", "-capability", "model.openai", "-worker-id", "worker", "-model", "tiny", "-request-bytes", "512", "-text-bytes", "128", "-messages", "1", "-max-tokens", "32", "-stream", "false", "-tool", "shell", "-expect", "allow"},
		},
		{
			name: "snapshot.create",
			args: map[string]any{"name": "demo", "tag": "before-upgrade"},
			want: []string{"--json", "snapshot", "create", "demo", "-tag", "before-upgrade"},
		},
		{
			name: "snapshot.delete",
			args: map[string]any{"name": "demo", "tag": "before-upgrade", "state_dir": "/tmp/state"},
			want: []string{"--json", "snapshot", "delete", "demo", "before-upgrade", "-state-dir", "/tmp/state"},
		},
		{
			name: "volume.create",
			args: map[string]any{"name": "data", "size_mib": float64(2048)},
			want: []string{"--json", "volume", "create", "data", "-size-mib", "2048"},
		},
		{
			name: "volume.delete",
			args: map[string]any{"name": "data", "force": true},
			want: []string{"--json", "volume", "delete", "data", "-force"},
		},
		{
			name: "images.push",
			args: map[string]any{"image": "example.com/acme/demo:rc", "state_dir": "/tmp/state"},
			want: []string{"--json", "image", "push", "example.com/acme/demo:rc", "-state-dir", "/tmp/state"},
		},
		{
			name: "images.tag",
			args: map[string]any{"source": "example.com/acme/demo:rc", "target": "example.com/acme/demo:stable"},
			want: []string{"--json", "image", "tag", "example.com/acme/demo:rc", "example.com/acme/demo:stable"},
		},
		{
			name: "images.delete",
			args: map[string]any{"image": "example.com/acme/demo:old", "delete_files": true},
			want: []string{"--json", "image", "delete", "example.com/acme/demo:old", "-purge", "-yes"},
		},
		{
			name: "images.prune",
			args: map[string]any{"state_dir": "/tmp/state", "delete_files": true},
			want: []string{"--json", "image", "prune", "-state-dir", "/tmp/state", "-purge", "-yes"},
		},
		{
			name: "profiles.list",
			args: map[string]any{},
			want: []string{"--json", "profiles"},
		},
		{
			name: "host.inspect",
			args: map[string]any{"backend": "applevf", "arch": "arm64", "supervisor": "/tmp/helper"},
			want: []string{"--json", "host", "-backend", "applevf", "-arch", "arm64", "-supervisor", "/tmp/helper"},
		},
		{
			name: "doctor.check",
			args: map[string]any{"backend": "linux-kvm"},
			want: []string{"--json", "doctor", "-backend", "linux-kvm"},
		},
		{
			name: "contract.get",
			args: map[string]any{},
			want: []string{"--json", "contract"},
		},
		{
			name: "kernel.verify",
			args: map[string]any{"path": "/tmp/vmlinux", "sha256": "abc", "backend": "linux-kvm", "arch": "amd64"},
			want: []string{"--json", "kernel", "verify", "-path", "/tmp/vmlinux", "-sha256", "abc", "-backend", "linux-kvm", "-arch", "amd64"},
		},
		{
			name: "kernel.install",
			args: map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "out": "/tmp/vmlinux", "backend": "linux-kvm", "arch": "amd64"},
			want: []string{"--json", "kernel", "install", "-url", "https://example.test/vmlinux", "-sha256", "abc", "-out", "/tmp/vmlinux", "-backend", "linux-kvm", "-arch", "amd64"},
		},
		{
			name: "rootfs.build",
			args: map[string]any{"image": "alpine:3.20", "os": "linux", "arch": "amd64", "out": "/tmp/rootfs.ext4", "state_dir": "/tmp/state", "size_mib": float64(2048), "allow_mutable": true},
			want: []string{"--json", "rootfs", "build", "-image", "alpine:3.20", "-os", "linux", "-arch", "amd64", "-out", "/tmp/rootfs.ext4", "-state-dir", "/tmp/state", "-size-mib", "2048", "-allow-mutable"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mcpCLIArgs(tt.name, tt.args)
			if err != nil {
				t.Fatalf("mcpCLIArgs: %v", err)
			}
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("args = %#v, want %#v", got, tt.want)
			}
		})
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

func TestMCPManagementDeletePreview(t *testing.T) {
	for _, tool := range []string{"volume.delete", "snapshot.delete", "images.delete", "images.prune"} {
		t.Run(tool, func(t *testing.T) {
			args := map[string]any{"name": "demo", "tag": "snap", "image": "example.com/acme/demo:old", "preview": true, "force": true, "delete_files": true}
			result, err := runMCPTool(context.Background(), tool, args)
			if err != nil {
				t.Fatalf("runMCPTool preview: %v", err)
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"preview":true`) {
				t.Fatalf("preview = %s", data)
			}
		})
	}
}

func TestMCPReadPathsUseTypedHandlers(t *testing.T) {
	stateDir := t.TempDir()
	tests := []struct {
		name string
		args map[string]any
		key  string
	}{
		{name: "workspace.list", args: map[string]any{"state_dir": stateDir}, key: "workspaces"},
		{name: "volume.list", args: map[string]any{"state_dir": stateDir}, key: "volumes"},
		{name: "images.list", args: map[string]any{"state_dir": stateDir}, key: "images"},
		{name: "models.list", args: map[string]any{"state_dir": stateDir}, key: "models"},
		{name: "profiles.list", args: map[string]any{}, key: "profiles"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, handled, err := runDirectMCPTool(t.Context(), tc.name, tc.args)
			if err != nil {
				t.Fatalf("runDirectMCPTool: %v", err)
			}
			if !handled {
				t.Fatal("runDirectMCPTool handled = false")
			}
			object, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("result type = %T, want map", result)
			}
			if _, ok := object[tc.key]; !ok {
				t.Fatalf("result = %#v, want key %q", object, tc.key)
			}
		})
	}
}

func TestMCPLifecycleMutationsUseTypedHandlers(t *testing.T) {
	oldControl := mcpWorkspaceControl
	oldQuarantine := mcpWorkspaceQuarantine
	oldDelete := mcpWorkspaceDelete
	t.Cleanup(func() {
		mcpWorkspaceControl = oldControl
		mcpWorkspaceQuarantine = oldQuarantine
		mcpWorkspaceDelete = oldDelete
	})

	var commands []string
	mcpWorkspaceControl = func(_ context.Context, opts workspace.Options, command string) (vmkit.Response, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("control opts = %#v", opts)
		}
		commands = append(commands, command)
		return vmkit.Response{OK: true, Backend: opts.Backend}, nil
	}
	mcpWorkspaceQuarantine = func(_ context.Context, opts workspace.Options, qopts workspace.QuarantineOptions) (workspace.QuarantineResult, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" || qopts.SkipCapture {
			t.Fatalf("quarantine opts = %#v qopts=%#v", opts, qopts)
		}
		return workspace.QuarantineResult{
			Response: vmkit.Response{OK: true, Backend: opts.Backend},
			Captured: true,
		}, nil
	}
	var deleteForce bool
	mcpWorkspaceDelete = func(_ context.Context, opts workspace.Options, deleteOpts workspace.DeleteOptions) (vmkit.Response, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("delete opts = %#v", opts)
		}
		deleteForce = deleteOpts.Force
		return vmkit.Response{OK: true, Backend: opts.Backend}, nil
	}

	for _, tool := range []string{"workspace.halt", "workspace.kill", "workspace.pause", "workspace.resume"} {
		result, handled, err := runDirectMCPTool(t.Context(), tool, map[string]any{"name": "demo", "state_dir": "/tmp/state"})
		if err != nil || !handled {
			t.Fatalf("%s: handled=%v err=%v", tool, handled, err)
		}
		if result.(map[string]any)["ok"] != true {
			t.Fatalf("%s result = %#v", tool, result)
		}
	}
	wantCommands := []string{"halt", "kill", "pause", "resume"}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", commands, wantCommands)
	}

	result, handled, err := runDirectMCPTool(t.Context(), "workspace.quarantine", map[string]any{"name": "demo", "state_dir": "/tmp/state"})
	if err != nil || !handled {
		t.Fatalf("quarantine: handled=%v err=%v", handled, err)
	}
	if result.(map[string]any)["captured"] != true {
		t.Fatalf("quarantine result = %#v", result)
	}

	_, handled, err = runDirectMCPTool(t.Context(), "workspace.delete", map[string]any{
		"name": "demo", "state_dir": "/tmp/state", "force": true,
	})
	if err != nil || !handled {
		t.Fatalf("delete: handled=%v err=%v", handled, err)
	}
	if !deleteForce {
		t.Fatal("delete force = false")
	}

	for _, tool := range []string{"workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete"} {
		if _, err := mcpCLIArgs(tool, map[string]any{"name": "demo"}); err == nil {
			t.Fatalf("%s still has an MCP-to-CLI mapping", tool)
		}
	}
}

func TestServeMCPDoesNotActivateDeprecatedCLIAXMode(t *testing.T) {
	oldMode := globalOutputMode
	t.Cleanup(func() { globalOutputMode = oldMode })
	globalOutputMode = ""
	if err := runServeMCP(t.Context(), nil, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("runServeMCP: %v", err)
	}
	if globalOutputMode != "" {
		t.Fatalf("globalOutputMode = %q, want MCP independent of CLI output modes", globalOutputMode)
	}
}

func TestMCPHostMutationPreviewAndConfirmation(t *testing.T) {
	args := map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "preview": true}
	preview, err := runMCPTool(context.Background(), "kernel.install", args)
	if err != nil {
		t.Fatalf("runMCPTool preview: %v", err)
	}
	result, ok := preview["result"].(map[string]any)
	if !ok {
		t.Fatalf("preview result type = %T", preview["result"])
	}
	token, ok := result["confirmation_token"].(string)
	if !ok || token == "" {
		t.Fatalf("confirmation_token = %#v", result["confirmation_token"])
	}
	if _, err := mcpCLIArgs("kernel.install", map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "confirm_token": token}); err != nil {
		t.Fatalf("mcpCLIArgs confirmed: %v", err)
	}
	if _, err := runMCPTool(context.Background(), "kernel.install", map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc"}); err == nil {
		t.Fatal("runMCPTool without confirm_token err = nil, want confirmation error")
	}
}

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
	meta := mcpEnvelopeMeta(t, envelope)
	if meta["retry_count"] != 0 {
		t.Fatalf("meta.retry_count = %#v, want 0", meta["retry_count"])
	}
	if meta["retry_wall_clock_ms"] != int64(0) {
		t.Fatalf("meta.retry_wall_clock_ms = %#v, want 0", meta["retry_wall_clock_ms"])
	}
}

// mcpEnvelopeMeta returns the transport meta block of an MCP tool envelope.
func mcpEnvelopeMeta(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	meta, ok := envelope["meta"].(map[string]any)
	if !ok {
		t.Fatalf("envelope meta type = %T (%#v)", envelope["meta"], envelope)
	}
	return meta
}

func TestMCPWorkspaceExecRetriesTCPReset(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("Linux demo\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"uname", "-a"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 1 {
		t.Fatalf("meta.retry_count = %#v, want 1", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
	result := envelope["result"].(execprotocol.ExecResult)
	if string(result.Stdout) != "Linux demo\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func TestMCPWorkspaceExecRetriesConnectionRefused(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("ok\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 1 {
		t.Fatalf("meta.retry_count = %#v, want 1", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func TestMCPWorkspaceExecRetriesConnectionTimeout(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("ok\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 1 {
		t.Fatalf("meta.retry_count = %#v, want 1", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func TestMCPWorkspaceExecRetryExhaustionReturnsStructuredError(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		err := workspace.ExecRetryExhaustedError{Retries: 3, WallClock: time.Millisecond, LastErr: execclient.UnreachableError{Addr: "127.0.0.1:45000", Err: syscall.ECONNREFUSED}}
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{Count: 3, WallClock: time.Millisecond, Exhausted: true}, err
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want retry-exhausted error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	meta := mcpEnvelopeMeta(t, envelope)
	if meta["retry_count"] != 3 {
		t.Fatalf("meta.retry_count = %#v, want 3", meta["retry_count"])
	}
	if meta["retry_exhausted"] != true {
		t.Fatalf("meta.retry_exhausted = %#v, want true", meta["retry_exhausted"])
	}
	structured, ok := envelope["error"].(structuredError)
	if !ok {
		t.Fatalf("error type = %T", envelope["error"])
	}
	if structured.Kind != errorKindTransient {
		t.Fatalf("kind = %q, want transient", structured.Kind)
	}
	if !structured.Retryable {
		t.Fatalf("retryable = false, want true")
	}
	if !strings.Contains(structured.Message, "persisted after 3 retries") {
		t.Fatalf("message = %q, want retry exhaustion detail", structured.Message)
	}
}

func TestMCPWorkspaceExecRetryExhaustionIncludesErrorEnvelopeMetadata(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		err := workspace.ExecRetryExhaustedError{Retries: 3, WallClock: time.Millisecond, LastErr: execclient.UnreachableError{Addr: "127.0.0.1:45000", Err: syscall.ECONNREFUSED}}
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{Count: 3, WallClock: time.Millisecond, Exhausted: true}, err
	})
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "workspace.exec",
			"arguments": map[string]any{
				"name": "research",
				"argv": []string{"true"},
			},
		},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
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
	if data["kind"] != string(errorKindTransient) || data["retryable"] != true {
		t.Fatalf("error data classification = %#v", data)
	}
	meta, ok := data["meta"].(map[string]any)
	if !ok {
		t.Fatalf("error data meta = %#v", data["meta"])
	}
	if meta["retry_count"] != float64(3) {
		t.Fatalf("meta.retry_count = %#v, want 3", meta["retry_count"])
	}
	if meta["retry_exhausted"] != true {
		t.Fatalf("meta.retry_exhausted = %#v, want true", meta["retry_exhausted"])
	}
	if _, ok := meta["retry_wall_clock_ms"].(float64); !ok {
		t.Fatalf("meta.retry_wall_clock_ms missing or non-number: %#v", meta)
	}
}

func TestMCPWorkspaceExecDoesNotRetryExecCompletedErrors(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		code := 127
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stderr = []byte("command not found\n")
		return result, workspace.ExecRetryMetadata{}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"missing-command"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 0 {
		t.Fatalf("meta.retry_count = %#v, want 0", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func TestMCPWorkspaceExecDoesNotRetryWorkspaceNotRunning(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{}, fmt.Errorf("workspace research is not running; structured exec is unavailable in state stopped")
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want workspace-not-running error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 0 {
		t.Fatalf("meta.retry_count = %#v, want 0", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func stubMCPWorkspaceExec(t *testing.T, fn func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error)) {
	t.Helper()
	originalExec := mcpWorkspaceExec
	mcpWorkspaceExec = fn
	t.Cleanup(func() {
		mcpWorkspaceExec = originalExec
	})
}

func successfulMCPExecResult(stdout string) execprotocol.ExecResult {
	code := 0
	result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
	result.ExitCode = &code
	result.Stdout = []byte(stdout)
	return result
}

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
				"name":      "does-not-exist",
				"state_dir": t.TempDir(),
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

// TestMCPToolReportsExitAsResult guards which tools surface a silent nonzero CLI
// exit as structured result data (a task outcome) versus a JSON-RPC tool error.
// dispatch must join wait: its nonzero guest exit carries the guest output and
// mediator egress summary the caller needs, so it must not be discarded as an
// error.
func TestMCPToolReportsExitAsResult(t *testing.T) {
	for _, n := range []string{"workspace.wait", "workspace.dispatch"} {
		if !mcpToolReportsExitAsResult(n) {
			t.Errorf("%s: a silent nonzero exit must be reported as result data", n)
		}
	}
	for _, n := range []string{"workspace.create", "workspace.start", "workspace.commit", "images.pull", "cp", "snapshot.create"} {
		if mcpToolReportsExitAsResult(n) {
			t.Errorf("%s: a silent nonzero exit must remain a tool error, not result data", n)
		}
	}
}

// TestMCPEgressLockAllowlistFlag is the B10 guard: the workspace.create and
// workspace.dispatch tools can express egress_lock_allowlist (so an agent that
// sets it actually locks the allowlist), and omit the flag when it is not set.
func TestMCPEgressLockAllowlistFlag(t *testing.T) {
	for _, tool := range []string{"workspace.create", "workspace.dispatch"} {
		args := map[string]any{
			"egress":                "broker",
			"egress_allow":          []any{"api.anthropic.com"},
			"egress_lock_allowlist": true,
		}
		if tool == "workspace.create" {
			args["name"] = "demo"
		} else {
			args["image"] = "docker.io/library/alpine:3.20"
		}
		cli, err := mcpCLIArgs(tool, args)
		if err != nil {
			t.Fatalf("%s: mcpCLIArgs: %v", tool, err)
		}
		if !mcpArgsContain(cli, "-egress-lock-allowlist") {
			t.Fatalf("%s: CLI args %v missing -egress-lock-allowlist", tool, cli)
		}

		delete(args, "egress_lock_allowlist")
		cli, err = mcpCLIArgs(tool, args)
		if err != nil {
			t.Fatalf("%s: mcpCLIArgs (unset): %v", tool, err)
		}
		if mcpArgsContain(cli, "-egress-lock-allowlist") {
			t.Fatalf("%s: CLI args %v emitted -egress-lock-allowlist when not requested", tool, cli)
		}
	}
}

func mcpArgsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
