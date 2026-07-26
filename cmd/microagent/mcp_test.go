package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
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
		if operation.Effect == "" || operation.Idempotency == "" {
			t.Fatalf("MCP tool %s operation %s has incomplete effect/idempotency policy", name, operation.ID)
		}
		if got := mcpMutationTool(name); got != (operation.Effect != vmkit.OperationEffectRead) {
			t.Fatalf("MCP tool %s mutation classification drifted from operation %s", name, operation.ID)
		}
		if got := mcpHostMutationTool(name); got != (operation.Confirmation == vmkit.OperationConfirmationPreview) {
			t.Fatalf("MCP tool %s confirmation classification drifted from operation %s", name, operation.ID)
		}
	}
}

func TestUnknownMCPToolsHaveNoImplicitPolicy(t *testing.T) {
	const name = "unknown.tool"
	if mcpMutationTool(name) || mcpHostMutationTool(name) {
		t.Fatal("unknown MCP tool received an implicit mutation policy")
	}
	if effects := mcpToolSideEffects(name); effects != nil {
		t.Fatalf("unknown MCP tool side effects = %#v, want nil", effects)
	}
	if got := mcpToolIdempotency(name); got != "not_idempotent" {
		t.Fatalf("unknown MCP tool idempotency = %q, want not_idempotent", got)
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
