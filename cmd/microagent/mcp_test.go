package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
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

// TestMCPWorkspaceStopCallProducesUnknownToolError pins the structured error
// returned when a caller invokes a tool that is not advertised.
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
		t.Fatalf("error data kind = %#v, want unsupported", data["kind"])
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
	if _, ok := data["meta"].(map[string]any); !ok {
		t.Fatalf("error data missing meta block: %#v", data)
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
		if operation.RequestType == "" || operation.ResultType == "" {
			t.Fatalf("MCP tool %s operation %s has no request/result type", name, operation.ID)
		}
	}
}

func TestMCPToolInventoryMatchesLibraryOperations(t *testing.T) {
	tools := map[string]bool{}
	for _, tool := range mcpTools() {
		name, _ := tool["name"].(string)
		tools[name] = true
	}
	for _, operation := range vmkit.OperationContracts() {
		for _, name := range operation.MCPTools {
			if !tools[name] {
				t.Errorf("library operation %s declares missing MCP tool %q", operation.ID, name)
			}
		}
	}
}

func TestMCPLifecyclePolicyDerivesFromOperationRegistry(t *testing.T) {
	tests := []struct {
		tool        string
		mutation    bool
		idempotency string
		sideEffects []string
	}{
		{"workspace.create", true, "accepts idempotency_key; identical retries by the same principal replay the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
		{"workspace.clone", true, "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
		{"workspace.inspect", false, "read_only", nil},
		{"workspace.wait", false, "read_only", nil},
		{"workspace.list", false, "read_only", nil},
	}
	for _, test := range tests {
		if got := mcpMutationTool(test.tool); got != test.mutation {
			t.Errorf("%s mutation = %v, want %v", test.tool, got, test.mutation)
		}
		if got := mcpToolIdempotency(test.tool); got != test.idempotency {
			t.Errorf("%s idempotency = %q, want %q", test.tool, got, test.idempotency)
		}
		if got := mcpToolSideEffects(test.tool); !reflect.DeepEqual(got, test.sideEffects) {
			t.Errorf("%s side effects = %#v, want %#v", test.tool, got, test.sideEffects)
		}
	}
}

func TestMCPFilePolicyDerivesFromOperationRegistry(t *testing.T) {
	tests := []struct {
		tool        string
		mutation    bool
		idempotency string
		sideEffects []string
	}{
		{"cp", true, "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
		{"artifacts.list", false, "read_only", nil},
		{"artifacts.get", true, "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
		{"workspace.commit", true, "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
	}
	for _, test := range tests {
		if got := mcpMutationTool(test.tool); got != test.mutation {
			t.Errorf("%s mutation = %v, want %v", test.tool, got, test.mutation)
		}
		if got := mcpToolIdempotency(test.tool); got != test.idempotency {
			t.Errorf("%s idempotency = %q, want %q", test.tool, got, test.idempotency)
		}
		if got := mcpToolSideEffects(test.tool); !reflect.DeepEqual(got, test.sideEffects) {
			t.Errorf("%s side effects = %#v, want %#v", test.tool, got, test.sideEffects)
		}
	}
}

func TestMCPSnapshotPolicyDerivesFromOperationRegistry(t *testing.T) {
	tests := []struct {
		tool        string
		mutation    bool
		idempotency string
		sideEffects []string
	}{
		{"workspace.pause", true, "accepts idempotency_key; identical retries by the same principal replay the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
		{"workspace.resume", true, "accepts idempotency_key; identical retries by the same principal replay the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
		{"snapshot.create", true, "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
		{"snapshot.list", false, "read_only", nil},
		{"snapshot.delete", true, "accepts idempotency_key; identical retries by the same principal replay the first completed response for 15 minutes", []string{"host_state", "workspace_state"}},
	}
	for _, test := range tests {
		if got := mcpMutationTool(test.tool); got != test.mutation {
			t.Errorf("%s mutation = %v, want %v", test.tool, got, test.mutation)
		}
		if got := mcpToolIdempotency(test.tool); got != test.idempotency {
			t.Errorf("%s idempotency = %q, want %q", test.tool, got, test.idempotency)
		}
		if got := mcpToolSideEffects(test.tool); !reflect.DeepEqual(got, test.sideEffects) {
			t.Errorf("%s side effects = %#v, want %#v", test.tool, got, test.sideEffects)
		}
	}
}

func TestMCPWorkspacePolicyDerivesFromOperationRegistry(t *testing.T) {
	for _, tool := range []string{
		"workspace.result", "workspace.logs", "workspace.events", "workspace.stats",
		"workspace.egress", "workspace.estimate_cost", "network.inspect",
	} {
		if mcpMutationTool(tool) {
			t.Errorf("%s unexpectedly classified as mutation", tool)
		}
		if got := mcpToolIdempotency(tool); got != "read_only" {
			t.Errorf("%s idempotency = %q, want read_only", tool, got)
		}
		if effects := mcpToolSideEffects(tool); effects != nil {
			t.Errorf("%s side effects = %#v, want nil", tool, effects)
		}
	}
	for _, tool := range []string{"workspace.dispatch", "workspace.exec", "workspace.apply"} {
		if !mcpMutationTool(tool) {
			t.Errorf("%s unexpectedly classified as read", tool)
		}
		if got := mcpToolIdempotency(tool); got != "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes" {
			t.Errorf("%s idempotency = %q", tool, got)
		}
		if effects := mcpToolSideEffects(tool); !reflect.DeepEqual(effects, []string{"host_state", "workspace_state"}) {
			t.Errorf("%s side effects = %#v", tool, effects)
		}
	}
}

func TestMCPResourcePolicyDerivesFromOperationRegistry(t *testing.T) {
	for _, tool := range []string{
		"volume.list", "volume.inspect", "images.list", "models.list",
		"models.runners", "models.policy.validate", "models.policy.evaluate",
	} {
		if mcpMutationTool(tool) {
			t.Errorf("%s unexpectedly classified as mutation", tool)
		}
		if got := mcpToolIdempotency(tool); got != "read_only" {
			t.Errorf("%s idempotency = %q, want read_only", tool, got)
		}
	}
	for _, tool := range []string{
		"volume.create", "volume.delete", "images.pull", "images.push",
		"images.tag", "images.delete", "images.prune", "models.pull",
		"models.remove", "models.prune", "models.serve", "models.stop",
	} {
		if !mcpMutationTool(tool) {
			t.Errorf("%s unexpectedly classified as read", tool)
		}
		operation, ok := vmkit.OperationForMCPTool(tool)
		if !ok || operation.Effect == "" || operation.Idempotency == "" {
			t.Errorf("%s registry policy = %#v", tool, operation)
		}
	}
}

func TestMCPHostPolicyDerivesFromOperationRegistry(t *testing.T) {
	for _, tool := range []string{"kernel.install", "rootfs.build"} {
		if !mcpMutationTool(tool) || !mcpHostMutationTool(tool) {
			t.Errorf("%s missing registered mutation or confirmation policy", tool)
		}
		operation, ok := vmkit.OperationForMCPTool(tool)
		if !ok || operation.Confirmation != vmkit.OperationConfirmationPreview {
			t.Errorf("%s registry policy = %#v", tool, operation)
		}
	}
	for _, tool := range []string{
		"kernel.verify", "host.inspect", "doctor.check", "profiles.list",
		"contract.get", "microagent.describe",
	} {
		if mcpMutationTool(tool) || mcpHostMutationTool(tool) {
			t.Errorf("%s unexpectedly classified as host mutation", tool)
		}
		if got := mcpToolIdempotency(tool); got != "read_only" {
			t.Errorf("%s idempotency = %q, want read_only", tool, got)
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
		if entry["request_type"] != vmkit.OperationTypeID("workspace.pause.request") ||
			entry["result_type"] != vmkit.OperationTypeID("workspace.pause.result") {
			t.Fatalf("workspace.pause types = %#v/%#v", entry["request_type"], entry["result_type"])
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

func TestMCPWorkspaceCreateOptionsUseTypedConfiguration(t *testing.T) {
	opts, err := mcpWorkspaceCreateOptions(map[string]any{
		"name": "demo", "image": "docker.io/library/busybox:1.36",
		"network": "isolated", "dry_run": true,
		"model_runner_args": []any{"--max-model-len", "2048"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Name != "demo" || opts.ImageRef != "docker.io/library/busybox:1.36" ||
		opts.Network.Mode != "isolated" || !opts.DryRun ||
		!reflect.DeepEqual(opts.ModelRunner.Args, []string{"--max-model-len", "2048"}) {
		t.Fatalf("options = %+v", opts)
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

func TestMCPHostDiagnosticsUseTypedHandlers(t *testing.T) {
	oldCheck := mcpDiagnosticsCheck
	oldVerify := mcpKernelVerify
	t.Cleanup(func() {
		mcpDiagnosticsCheck = oldCheck
		mcpKernelVerify = oldVerify
	})

	checkErr := errors.New("host unavailable")
	mcpDiagnosticsCheck = func(_ context.Context, opts diagnostics.Options) (vmkit.Response, error) {
		if opts.Backend != "apple-vf" || opts.Arch != "arm64" || opts.SupervisorPath != "/tmp/helper" {
			t.Fatalf("diagnostics opts = %#v", opts)
		}
		return vmkit.Response{OK: false, Backend: opts.Backend, Error: checkErr.Error()}, checkErr
	}
	result, handled, err := runDirectMCPTool(t.Context(), "host.inspect", map[string]any{
		"backend": "apple-vf", "arch": "arm64", "supervisor": "/tmp/helper",
	})
	if err != nil || !handled || result.(map[string]any)["error"] != checkErr.Error() {
		t.Fatalf("host.inspect: handled=%v err=%v result=%#v", handled, err, result)
	}
	_, handled, err = runDirectMCPTool(t.Context(), "doctor.check", map[string]any{
		"backend": "apple-vf", "arch": "arm64", "supervisor": "/tmp/helper",
	})
	if !handled || !errors.Is(err, checkErr) {
		t.Fatalf("doctor.check: handled=%v err=%v", handled, err)
	}

	mcpKernelVerify = func(opts kernel.VerifyOptions) (kernel.VerifyResult, error) {
		if opts.Path != "/tmp/vmlinux" || opts.SHA256 != "abc" || opts.Backend != "linux-kvm" || opts.Architecture != "amd64" {
			t.Fatalf("verify opts = %#v", opts)
		}
		return kernel.VerifyResult{OK: true, Verified: true, Path: opts.Path, SHA256: opts.SHA256}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "kernel.verify", map[string]any{
		"path": "/tmp/vmlinux", "sha256": "abc", "backend": "linux-kvm", "arch": "amd64",
	})
	if err != nil || !handled || result.(map[string]any)["verified"] != true {
		t.Fatalf("kernel.verify: handled=%v err=%v result=%#v", handled, err, result)
	}
}

func TestMCPKernelInstallUsesTypedHandler(t *testing.T) {
	oldInstall := mcpKernelInstall
	t.Cleanup(func() {
		mcpKernelInstall = oldInstall
	})

	mcpKernelInstall = func(_ context.Context, opts kernel.InstallOptions) (kernel.InstallResult, error) {
		if opts.URL != "https://example.test/vmlinux" || opts.FromPath != "" || opts.SHA256 != "abc" ||
			opts.OutputPath != "/tmp/vmlinux" || opts.Backend != "linux-kvm" || opts.Architecture != "amd64" {
			t.Fatalf("install opts = %#v", opts)
		}
		return kernel.InstallResult{Path: opts.OutputPath, SHA256: opts.SHA256}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "kernel.install", map[string]any{
		"url":     "https://example.test/vmlinux",
		"sha256":  "abc",
		"out":     "/tmp/vmlinux",
		"backend": "linux-kvm",
		"arch":    "amd64",
	})
	if err != nil || !handled || result.(map[string]any)["path"] != "/tmp/vmlinux" {
		t.Fatalf("kernel.install: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpKernelInstall = func(_ context.Context, opts kernel.InstallOptions) (kernel.InstallResult, error) {
		if opts.Backend != hostBackend() || opts.Architecture != defaultGuestArch() {
			t.Fatalf("default install opts = %#v", opts)
		}
		if opts.OutputPath != workspace.WritableKernelPath(opts.Backend, opts.Architecture) {
			t.Fatalf("default output path = %q", opts.OutputPath)
		}
		return kernel.InstallResult{Path: opts.OutputPath}, nil
	}
	_, handled, err = runDirectMCPTool(t.Context(), "kernel.install", map[string]any{})
	if err != nil || !handled {
		t.Fatalf("kernel.install defaults: handled=%v err=%v", handled, err)
	}
}

func TestMCPRootfsBuildUsesTypedHandler(t *testing.T) {
	oldBuild := mcpRootfsBuild
	t.Cleanup(func() {
		mcpRootfsBuild = oldBuild
	})

	mcpRootfsBuild = func(_ context.Context, req rootfs.BuildRequest) (rootfs.Provenance, error) {
		if req.ImageRef != "alpine:3.20" || req.Platform.OS != "linux" || req.Platform.Architecture != "amd64" ||
			req.OutputPath != "/tmp/rootfs.ext4" || req.InitPath != "/init" || req.StateDir != "/tmp/state" ||
			req.Mke2fsPath != "/usr/bin/mke2fs" || req.SizeMiB != 2048 || req.AutoSize ||
			!req.AllowMutable || !req.KeepStage || req.StageSnapshot != "/tmp/stage" {
			t.Fatalf("build req = %#v", req)
		}
		if !reflect.DeepEqual(req.Command, []string{"/bin/sh", "-lc", "echo ready"}) {
			t.Fatalf("build command = %#v", req.Command)
		}
		return rootfs.Provenance{ImageRef: req.ImageRef, OutputPath: req.OutputPath}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "rootfs.build", map[string]any{
		"image":          "alpine:3.20",
		"os":             "linux",
		"arch":           "amd64",
		"out":            "/tmp/rootfs.ext4",
		"init":           "/init",
		"state_dir":      "/tmp/state",
		"mke2fs":         "/usr/bin/mke2fs",
		"size_mib":       float64(2048),
		"exec":           "echo ready",
		"allow_mutable":  true,
		"keep_stage":     true,
		"stage_snapshot": "/tmp/stage",
	})
	if err != nil || !handled || result.(map[string]any)["output_path"] != "/tmp/rootfs.ext4" {
		t.Fatalf("rootfs.build: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpRootfsBuild = func(_ context.Context, req rootfs.BuildRequest) (rootfs.Provenance, error) {
		if req.Platform.OS != "linux" || req.Platform.Architecture != workspace.NormalizeArch(defaultGuestArch()) ||
			req.InitPath != rootfs.DefaultInitPath || req.Mke2fsPath != "mke2fs" ||
			req.SizeMiB != rootfs.DefaultSizeMiB || !req.AutoSize {
			t.Fatalf("default build req = %#v", req)
		}
		return rootfs.Provenance{ImageRef: req.ImageRef}, nil
	}
	_, handled, err = runDirectMCPTool(t.Context(), "rootfs.build", map[string]any{"image": "example@sha256:abc"})
	if err != nil || !handled {
		t.Fatalf("rootfs.build defaults: handled=%v err=%v", handled, err)
	}
}

func TestMCPWorkspaceCloneUsesTypedHandler(t *testing.T) {
	oldClone := mcpWorkspaceClone
	t.Cleanup(func() {
		mcpWorkspaceClone = oldClone
	})

	mcpWorkspaceClone = func(stateDir, source, target string) (workspace.Result, error) {
		if stateDir != "/tmp/state" || source != "demo" || target != "copy" {
			t.Fatalf("clone args: stateDir=%q source=%q target=%q", stateDir, source, target)
		}
		return workspace.Result{Workspace: target, StateDir: stateDir}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "workspace.clone", map[string]any{
		"source": "demo", "target": "copy", "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("workspace.clone: handled=%v err=%v", handled, err)
	}
	object := result.(map[string]any)
	if object["workspace"] != "copy" || object["state_dir"] != "/tmp/state" {
		t.Fatalf("workspace.clone result = %#v", result)
	}
}

func TestMCPWorkspaceApplyUsesTypedHandler(t *testing.T) {
	oldReadSpec := mcpWorkspaceReadSpec
	oldApply := mcpWorkspaceApply
	t.Cleanup(func() {
		mcpWorkspaceReadSpec = oldReadSpec
		mcpWorkspaceApply = oldApply
	})

	mcpWorkspaceReadSpec = func(path string) (workspace.Spec, error) {
		if path != "/tmp/microagent.yaml" {
			t.Fatalf("spec path = %q", path)
		}
		return workspace.Spec{Name: "demo"}, nil
	}
	mcpWorkspaceApply = func(_ context.Context, opts workspace.Options, spec workspace.Spec) (workspace.ApplyResult, error) {
		if opts.StateDir != "/tmp/state" || opts.Backend != "apple-vf" || opts.Architecture != "arm64" || opts.SupervisorPath != "/tmp/helper" {
			t.Fatalf("apply opts = %#v", opts)
		}
		if spec.Name != "demo" {
			t.Fatalf("apply spec = %#v", spec)
		}
		return workspace.ApplyResult{Workspace: spec.Name, Applied: []string{"network"}}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "workspace.apply", map[string]any{
		"file":       "/tmp/microagent.yaml",
		"state_dir":  "/tmp/state",
		"backend":    "apple-vf",
		"arch":       "arm64",
		"supervisor": "/tmp/helper",
	})
	if err != nil || !handled {
		t.Fatalf("workspace.apply: handled=%v err=%v", handled, err)
	}
	object := result.(map[string]any)
	if object["workspace"] != "demo" || len(object["applied"].([]any)) != 1 {
		t.Fatalf("workspace.apply result = %#v", result)
	}

	mcpWorkspaceApply = func(_ context.Context, opts workspace.Options, spec workspace.Spec) (workspace.ApplyResult, error) {
		if opts.Backend != hostBackend() || opts.Architecture != defaultGuestArch() {
			t.Fatalf("default apply opts = %#v", opts)
		}
		if opts.SupervisorPath != defaultSupervisorPath(opts.Backend) {
			t.Fatalf("default supervisor = %q", opts.SupervisorPath)
		}
		return workspace.ApplyResult{Workspace: spec.Name}, nil
	}
	_, handled, err = runDirectMCPTool(t.Context(), "workspace.apply", map[string]any{
		"file": "/tmp/microagent.yaml",
	})
	if err != nil || !handled {
		t.Fatalf("workspace.apply defaults: handled=%v err=%v", handled, err)
	}
}

func TestMCPWorkspaceCommitUsesTypedHandler(t *testing.T) {
	oldCommit := mcpWorkspaceCommit
	oldPush := mcpWorkspaceCommitPush
	t.Cleanup(func() {
		mcpWorkspaceCommit = oldCommit
		mcpWorkspaceCommitPush = oldPush
	})

	const imageRef = "example.com/acme/demo:rc"
	mcpWorkspaceCommit = func(_ context.Context, opts commit.Options) (commit.Result, error) {
		if opts.StateDir != "/tmp/state" || opts.DebugFSPath == "" || opts.Workspace != "demo" ||
			opts.Backend != hostBackend() || opts.Reference != imageRef || opts.Architecture != "arm64" {
			t.Fatalf("commit opts = %#v", opts)
		}
		return commit.Result{
			Reference:  opts.Reference,
			Digest:     "sha256:abc",
			SizeBytes:  42,
			LayoutPath: "/tmp/state/images/oci",
		}, nil
	}
	var pushed bool
	mcpWorkspaceCommitPush = func(_ context.Context, stateDir, ref string) error {
		if stateDir != "/tmp/state" || ref != imageRef {
			t.Fatalf("push args: stateDir=%q ref=%q", stateDir, ref)
		}
		pushed = true
		return nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "workspace.commit", map[string]any{
		"name": "demo", "image": imageRef, "state_dir": "/tmp/state", "arch": "arm64", "push": true,
	})
	if err != nil || !handled {
		t.Fatalf("workspace.commit: handled=%v err=%v", handled, err)
	}
	object := result.(map[string]any)
	if !pushed || object["reference"] != imageRef || object["pushed"] != true || object["size_bytes"] != int64(42) {
		t.Fatalf("workspace.commit pushed=%v result=%#v", pushed, result)
	}

	mcpWorkspaceCommit = func(_ context.Context, opts commit.Options) (commit.Result, error) {
		if opts.Architecture != defaultGuestArch() {
			t.Fatalf("default architecture = %q", opts.Architecture)
		}
		return commit.Result{Reference: opts.Reference}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "workspace.commit", map[string]any{
		"name": "demo", "image": imageRef,
	})
	if err != nil || !handled || result.(map[string]any)["pushed"] != false {
		t.Fatalf("workspace.commit defaults: handled=%v err=%v result=%#v", handled, err, result)
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

}

func TestMCPVolumeMutationsUseTypedHandlers(t *testing.T) {
	oldCreate := mcpVolumeCreate
	oldRemove := mcpVolumeRemove
	t.Cleanup(func() {
		mcpVolumeCreate = oldCreate
		mcpVolumeRemove = oldRemove
	})

	mcpVolumeCreate = func(_ context.Context, stateDir, backend, name string, sizeMiB int64, mke2fsPath string) (volume.Record, error) {
		if stateDir != "/tmp/state" || backend != hostBackend() || name != "data" || sizeMiB != 2048 {
			t.Fatalf("create args: stateDir=%q backend=%q name=%q sizeMiB=%d", stateDir, backend, name, sizeMiB)
		}
		if mke2fsPath == "" {
			t.Fatal("create mke2fs path is empty")
		}
		return volume.Record{Name: name, SizeMiB: sizeMiB}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "volume.create", map[string]any{
		"name": "data", "size_mib": float64(2048), "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("volume.create: handled=%v err=%v", handled, err)
	}
	if result.(map[string]any)["name"] != "data" || result.(map[string]any)["size_mib"] != float64(2048) {
		t.Fatalf("volume.create result = %#v", result)
	}

	var removed bool
	mcpVolumeRemove = func(stateDir, name string, force bool, isRunning func(string) bool) error {
		if stateDir != "/tmp/state" || name != "data" || !force || isRunning == nil {
			t.Fatalf("remove args: stateDir=%q name=%q force=%v isRunningNil=%v", stateDir, name, force, isRunning == nil)
		}
		removed = true
		return nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "volume.delete", map[string]any{
		"name": "data", "force": true, "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("volume.delete: handled=%v err=%v", handled, err)
	}
	if !removed || result.(map[string]any)["removed"] != "data" {
		t.Fatalf("volume.delete removed=%v result=%#v", removed, result)
	}

}

func TestMCPImageManagementUsesTypedHandlers(t *testing.T) {
	oldPull := mcpImagePull
	oldList := mcpImageList
	oldPush := mcpImagePush
	oldTag := mcpImageTag
	oldRemove := mcpImageRemove
	oldPrune := mcpImagePrune
	t.Cleanup(func() {
		mcpImagePull = oldPull
		mcpImageList = oldList
		mcpImagePush = oldPush
		mcpImageTag = oldTag
		mcpImageRemove = oldRemove
		mcpImagePrune = oldPrune
	})

	const imageRef = "example.com/acme/demo:rc"
	mcpImagePull = func(_ context.Context, opts imagecache.PullOptions) (imagecache.Record, error) {
		if opts.StateDir != "/tmp/state" || opts.ImageRef != imageRef || opts.Architecture != "arm64" {
			t.Fatalf("pull opts = %#v", opts)
		}
		return imagecache.Record{ImageRef: opts.ImageRef}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "images.pull", map[string]any{
		"image": imageRef, "arch": "arm64", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["image_ref"] != imageRef {
		t.Fatalf("images.pull: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImageList = func(stateDir string) ([]imagecache.Record, error) {
		if stateDir != "/tmp/state" {
			t.Fatalf("list stateDir = %q", stateDir)
		}
		return []imagecache.Record{{ImageRef: imageRef}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.list", map[string]any{"state_dir": "/tmp/state"})
	if err != nil || !handled || len(result.(map[string]any)["images"].([]any)) != 1 {
		t.Fatalf("images.list: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImagePush = func(_ context.Context, stateDir, image string) error {
		if stateDir != "/tmp/state" || image != imageRef {
			t.Fatalf("push args: stateDir=%q image=%q", stateDir, image)
		}
		return nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.push", map[string]any{
		"image": imageRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["pushed"] != imageRef {
		t.Fatalf("images.push: handled=%v err=%v result=%#v", handled, err, result)
	}

	const targetRef = "example.com/acme/demo:stable"
	mcpImageTag = func(stateDir, source, target string) (imagecache.Record, error) {
		if stateDir != "/tmp/state" || source != imageRef || target != targetRef {
			t.Fatalf("tag args: stateDir=%q source=%q target=%q", stateDir, source, target)
		}
		return imagecache.Record{ImageRef: target}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.tag", map[string]any{
		"source": imageRef, "target": targetRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["image_ref"] != targetRef {
		t.Fatalf("images.tag: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImageRemove = func(stateDir, image string, deleteFiles bool) (imagecache.PruneResult, error) {
		if stateDir != "/tmp/state" || image != imageRef || !deleteFiles {
			t.Fatalf("remove args: stateDir=%q image=%q deleteFiles=%v", stateDir, image, deleteFiles)
		}
		return imagecache.PruneResult{Deleted: []imagecache.Record{{ImageRef: image}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.delete", map[string]any{
		"image": imageRef, "delete_files": true, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["deleted"].([]any)) != 1 {
		t.Fatalf("images.delete: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImagePrune = func(stateDir string, deleteFiles bool) (imagecache.PruneResult, error) {
		if stateDir != "/tmp/state" || !deleteFiles {
			t.Fatalf("prune args: stateDir=%q deleteFiles=%v", stateDir, deleteFiles)
		}
		return imagecache.PruneResult{Removed: []imagecache.Record{{ImageRef: imageRef}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.prune", map[string]any{
		"delete_files": true, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["removed"].([]any)) != 1 {
		t.Fatalf("images.prune: handled=%v err=%v result=%#v", handled, err, result)
	}

}

func TestMCPModelManagementUsesTypedHandlers(t *testing.T) {
	oldPull := mcpModelPull
	oldRemove := mcpModelRemove
	oldPrune := mcpModelPrune
	oldStop := mcpModelStop
	t.Cleanup(func() {
		mcpModelPull = oldPull
		mcpModelRemove = oldRemove
		mcpModelPrune = oldPrune
		mcpModelStop = oldStop
	})

	const modelRef = "acme/demo/model.gguf"
	const canonicalRef = "hf.co/acme/demo@main/model.gguf"
	mcpModelPull = func(_ context.Context, opts model.PullOptions) (model.Record, error) {
		if opts.StateDir != "/tmp/state" || opts.ModelRef != modelRef || opts.Token != "secret" {
			t.Fatalf("pull opts = %#v", opts)
		}
		return model.Record{ModelRef: opts.ModelRef}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "models.pull", map[string]any{
		"model": modelRef, "token": "secret", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["model_ref"] != modelRef {
		t.Fatalf("models.pull: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpModelRemove = func(stateDir, ref string, deleteFiles bool) (model.PruneResult, error) {
		if stateDir != "/tmp/state" || ref != modelRef || !deleteFiles {
			t.Fatalf("remove args: stateDir=%q ref=%q deleteFiles=%v", stateDir, ref, deleteFiles)
		}
		return model.PruneResult{Removed: []model.Record{{ModelRef: ref}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "models.remove", map[string]any{
		"model": modelRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["removed"].([]any)) != 1 {
		t.Fatalf("models.remove: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpModelPrune = func(stateDir string, deleteFiles bool) (model.PruneResult, error) {
		if stateDir != "/tmp/state" || deleteFiles {
			t.Fatalf("prune args: stateDir=%q deleteFiles=%v", stateDir, deleteFiles)
		}
		return model.PruneResult{Removed: []model.Record{{ModelRef: modelRef}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "models.prune", map[string]any{
		"state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["removed"].([]any)) != 1 {
		t.Fatalf("models.prune: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpModelStop = func(stateDir, ref string) (int, error) {
		if stateDir != "/tmp/state" || ref != canonicalRef {
			t.Fatalf("stop args: stateDir=%q ref=%q", stateDir, ref)
		}
		return 2, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "models.stop", map[string]any{
		"model": modelRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["stopped"] != 2 {
		t.Fatalf("models.stop: handled=%v err=%v result=%#v", handled, err, result)
	}

}

func TestMCPFileTransfersUseTypedHandlers(t *testing.T) {
	oldCopy := mcpWorkspaceCopy
	oldGetArtifact := mcpWorkspaceGetArtifact
	t.Cleanup(func() {
		mcpWorkspaceCopy = oldCopy
		mcpWorkspaceGetArtifact = oldGetArtifact
	})

	mcpWorkspaceCopy = func(_ context.Context, stateDir, debugfsPath, source, target string) (workspace.CopyResult, error) {
		if stateDir != "/tmp/state" || debugfsPath == "" || source != "input.txt" || target != "demo:/workspace/input.txt" {
			t.Fatalf("copy args: stateDir=%q debugfsPath=%q source=%q target=%q", stateDir, debugfsPath, source, target)
		}
		return workspace.CopyResult{
			Workspace: "demo",
			Direction: "to-workspace",
			Source:    source,
			Target:    target,
		}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "cp", map[string]any{
		"source": "input.txt", "target": "demo:/workspace/input.txt", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["direction"] != "to-workspace" {
		t.Fatalf("cp: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpWorkspaceGetArtifact = func(_ context.Context, stateDir, debugfsPath, name, artifact, target string) (workspace.CopyResult, error) {
		if stateDir != "/tmp/state" || debugfsPath == "" || name != "demo" || artifact != "report" || target != "report.json" {
			t.Fatalf("artifact args: stateDir=%q debugfsPath=%q name=%q artifact=%q target=%q", stateDir, debugfsPath, name, artifact, target)
		}
		return workspace.CopyResult{
			Artifact:  artifact,
			Workspace: name,
			Direction: "from-workspace",
			Target:    target,
		}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "artifacts.get", map[string]any{
		"name": "demo", "artifact": "report", "target": "report.json", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["artifact"] != "report" {
		t.Fatalf("artifacts.get: handled=%v err=%v result=%#v", handled, err, result)
	}

}

func TestMCPSnapshotMutationsUseTypedHandlers(t *testing.T) {
	oldCreate := mcpSnapshotCreate
	oldDelete := mcpSnapshotDelete
	t.Cleanup(func() {
		mcpSnapshotCreate = oldCreate
		mcpSnapshotDelete = oldDelete
	})

	var createdTag string
	mcpSnapshotCreate = func(_ context.Context, opts workspace.Options, tag string) (vmkit.SnapshotManifest, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("create opts = %#v", opts)
		}
		createdTag = tag
		return vmkit.SnapshotManifest{Tag: "snap-library-default"}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "snapshot.create", map[string]any{
		"name": "demo", "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("snapshot.create: handled=%v err=%v", handled, err)
	}
	if createdTag != "" || result.(map[string]any)["tag"] != "snap-library-default" {
		t.Fatalf("snapshot.create tag=%q result=%#v", createdTag, result)
	}

	var deletedTag string
	mcpSnapshotDelete = func(opts workspace.Options, tag string) error {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("delete opts = %#v", opts)
		}
		deletedTag = tag
		return nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "snapshot.delete", map[string]any{
		"name": "demo", "tag": "before-upgrade", "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("snapshot.delete: handled=%v err=%v", handled, err)
	}
	if deletedTag != "before-upgrade" || result.(map[string]any)["removed"] != deletedTag {
		t.Fatalf("snapshot.delete tag=%q result=%#v", deletedTag, result)
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
	confirmedArgs := map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "confirm_token": token}
	if confirmation, err := requireConfirmedMCPHostMutation("kernel.install", confirmedArgs); err != nil || confirmation != nil {
		t.Fatalf("confirmed mutation: confirmation=%#v err=%v", confirmation, err)
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
