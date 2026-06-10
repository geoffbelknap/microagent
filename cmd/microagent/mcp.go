package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

var mcpIdempotencyCache sync.Map

var mcpWorkspaceExec = workspace.ExecWithMetadata

func runServe(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printServeHelp(stdout)
		return nil
	}
	switch args[0] {
	case "mcp":
		return runServeMCP(ctx, args[1:], os.Stdin, stdout)
	default:
		return fmt.Errorf("unknown serve command: %s", args[0])
	}
}

func runServeMCP(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) > 0 && wantsHelp(args) {
		printServeMCPHelp(stdout)
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: microagent serve mcp")
	}
	if mcpStdioIsInteractive(stdin, stdout) {
		return fmt.Errorf("microagent serve mcp uses MCP stdio and runs in the foreground under an MCP client; configure the client to launch this command instead of running it interactively")
	}
	globalOutputMode = outputModeAX
	return serveMCP(ctx, stdin, stdout)
}

func mcpStdioIsInteractive(stdin io.Reader, stdout io.Writer) bool {
	inFile, inOK := stdin.(*os.File)
	outFile, outOK := stdout.(*os.File)
	return inOK && outOK && fileIsTerminal(inFile) && fileIsTerminal(outFile)
}

func serveMCP(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	server := mcpStdioServer{in: bufio.NewReader(stdin), out: stdout}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		msg, err := server.readMessage()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		resp, ok := handleMCPMessage(ctx, msg)
		if !ok {
			continue
		}
		if err := server.writeMessage(resp); err != nil {
			return err
		}
	}
}

type mcpStdioServer struct {
	in  *bufio.Reader
	out io.Writer
}

func (s mcpStdioServer) readMessage() (json.RawMessage, error) {
	var contentLength int
	for {
		line, err := s.in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("invalid MCP header: %s", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 {
				return nil, fmt.Errorf("invalid MCP content length: %s", strings.TrimSpace(value))
			}
			contentLength = length
		}
	}
	if contentLength == 0 {
		return nil, fmt.Errorf("missing MCP content length")
	}
	msg := make([]byte, contentLength)
	if _, err := io.ReadFull(s.in, msg); err != nil {
		return nil, err
	}
	return json.RawMessage(msg), nil
}

func (s mcpStdioServer) writeMessage(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpStructuredError struct {
	structuredError
	Retryable bool `json:"retryable"`
}

func mapMCPStructuredError(err error, correlationID string) mcpStructuredError {
	mapped := mapStructuredError(err, correlationID)
	return mcpStructuredError{
		structuredError: mapped,
		Retryable:       structuredErrorKindRetryable(mapped.Kind),
	}
}

func structuredErrorKindRetryable(kind structuredErrorKind) bool {
	switch kind {
	case errorKindTransient, errorKindResourceExhausted:
		return true
	default:
		return false
	}
}

func handleMCPMessage(ctx context.Context, msg json.RawMessage) (mcpResponse, bool) {
	var req mcpRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		return mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: -32700, Message: "parse error", Data: mapMCPStructuredError(err, newRequestID())},
		}, true
	}
	if req.ID == nil {
		return mcpResponse{}, false
	}
	switch req.Method {
	case "initialize":
		return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: mcpInitializeResult()}, true
	case "tools/list":
		return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": mcpTools()}}, true
	case "tools/call":
		return handleMCPToolCall(ctx, req), true
	default:
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: "method not found", Data: mapMCPStructuredError(fmt.Errorf("unsupported MCP method %s", req.Method), newRequestID())},
		}, true
	}
}

func mcpInitializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "microagent",
			"version": version,
		},
	}
}

func mcpTools() []map[string]any {
	return []map[string]any{
		mcpTool("microagent.ping", "Test tool for validating the microagent MCP transport.", nil, nil),
		mcpTool("microagent.describe", "Return the machine-readable microagent MCP capability manifest.", nil, nil),
		mcpTool("workspace.create", "Create or dry-run a workspace.", []string{"name"}, map[string]any{
			"name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}, "exec": map[string]any{"type": "string"},
			"state_dir": map[string]any{"type": "string"}, "profile": map[string]any{"type": "string"}, "dry_run": map[string]any{"type": "boolean"},
		}),
		mcpTool("workspace.start", "Start a prepared workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.exec", "Run a structured command in a running workspace.", []string{"name"}, workspaceExecInputSchema()),
		mcpTool("workspace.halt", "Halt a workspace and preserve disk state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.stop", "Stop a workspace runtime.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.kill", "Force stop a workspace runtime.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.quarantine", "Sever host-side network and mediation for a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.pause", "Pause a running workspace when the backend supports pause/resume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.resume", "Resume a paused workspace when the backend supports pause/resume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.delete", "Delete a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("workspace.list", "List workspaces.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.inspect", "Inspect workspace state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "format": map[string]any{"type": "string", "enum": []string{"summary", "full"}}}),
		mcpTool("workspace.result", "Read the structured workspace result.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.stats", "Sample current workspace resource usage.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.logs", "Read workspace serial logs. Defaults to a compact tail summary; pass format=full for the complete log buffer.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "format": map[string]any{"type": "string", "enum": []string{"summary", "full"}}, "tail_lines": map[string]any{"type": "integer"}}),
		mcpTool("workspace.events", "Read workspace lifecycle events. Defaults to a compact recent-event summary; pass format=full for all events.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "format": map[string]any{"type": "string", "enum": []string{"summary", "full"}}, "limit": map[string]any{"type": "integer"}, "after_index": map[string]any{"type": "integer"}}),
		mcpTool("workspace.clone", "Clone a stopped workspace to a new workspace name.", []string{"source", "target"}, map[string]any{"source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.apply", "Apply supported changes from a workspace spec file.", []string{"file"}, map[string]any{"file": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("workspace.commit", "Commit a stopped workspace rootfs into a local OCI image, optionally pushing it.", []string{"name", "image"}, map[string]any{"name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "push": map[string]any{"type": "boolean"}}),
		mcpTool("workspace.estimate_cost", "Estimate workspace resource consumption before creating or starting it.", nil, map[string]any{"profile": map[string]any{"type": "string"}, "memory_mib": map[string]any{"type": "integer"}, "cpus": map[string]any{"type": "integer"}, "size_mib": map[string]any{"type": "integer"}, "price_per_hour": map[string]any{"type": "number"}}),
		mcpTool("artifacts.list", "List declared workspace artifacts.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("snapshot.create", "Create a backend snapshot for a workspace when supported.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "tag": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("snapshot.list", "List snapshots for a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("snapshot.delete", "Delete a workspace snapshot.", []string{"name", "tag"}, map[string]any{"name": map[string]any{"type": "string"}, "tag": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("network.inspect", "Inspect a named microVM network record.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("network.create", "Create a named microVM network record.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "subnet": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("network.list", "List named microVM network records.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("network.delete", "Delete a named microVM network record.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("volume.create", "Create a named managed ext4 volume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "size_mib": map[string]any{"type": "integer"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("volume.list", "List named managed volumes.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("volume.inspect", "Inspect one named managed volume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("volume.delete", "Delete a named managed volume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("images.pull", "Pull a reusable image rootfs.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}}),
		mcpTool("images.list", "List reusable local image records.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("images.push", "Push a locally committed OCI image to its registry.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("images.tag", "Tag a local image record.", []string{"source", "target"}, map[string]any{"source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("images.delete", "Delete a local image record, optionally deleting cached rootfs files.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "delete_files": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("images.prune", "Prune stale local image records, optionally deleting cached rootfs files.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}, "delete_files": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("profiles.list", "List resource profiles.", nil, nil),
		mcpTool("host.inspect", "Report host capabilities for the selected backend.", nil, map[string]any{"backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("doctor.check", "Run host diagnostics for the selected backend.", nil, map[string]any{"backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("host.networking.setup", "Apply or revert Linux privileged networking setup after preview confirmation.", nil, map[string]any{"action": map[string]any{"type": "string", "enum": []string{"apply", "revert"}}, "preview": map[string]any{"type": "boolean"}, "confirm_token": map[string]any{"type": "string"}}),
		mcpTool("contract.get", "Return the backend-neutral runtime contract.", nil, nil),
		mcpTool("kernel.verify", "Verify the configured or supplied kernel artifact.", nil, map[string]any{"path": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string"}, "backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}}),
		mcpTool("kernel.install", "Install a kernel artifact after preview confirmation.", nil, map[string]any{"url": map[string]any{"type": "string"}, "from": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string"}, "out": map[string]any{"type": "string"}, "backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "preview": map[string]any{"type": "boolean"}, "confirm_token": map[string]any{"type": "string"}}),
		mcpTool("rootfs.build", "Build a rootfs from an OCI image after preview confirmation.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "os": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "out": map[string]any{"type": "string"}, "init": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "mke2fs": map[string]any{"type": "string"}, "size_mib": map[string]any{"type": "integer"}, "exec": map[string]any{"type": "string"}, "allow_mutable": map[string]any{"type": "boolean"}, "keep_stage": map[string]any{"type": "boolean"}, "stage_snapshot": map[string]any{"type": "string"}, "preview": map[string]any{"type": "boolean"}, "confirm_token": map[string]any{"type": "string"}}),
		mcpTool("cp", "Copy files into or out of a stopped workspace.", []string{"source", "target"}, map[string]any{"source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("artifacts.get", "Copy a declared workspace artifact out to the host.", []string{"name", "artifact", "target"}, map[string]any{"name": map[string]any{"type": "string"}, "artifact": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
	}
}

func mcpTool(name, description string, required []string, properties map[string]any) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if mcpMutationTool(name) {
		properties["idempotency_key"] = map[string]any{"type": "string"}
	}
	properties["principal"] = principalContextSchema()
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": true,
		},
	}
}

func handleMCPToolCall(ctx context.Context, req mcpRequest) mcpResponse {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(req.Params) != 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32602, Message: "invalid params", Data: mapMCPStructuredError(err, newRequestID())}}
		}
	}
	if params.Name != "microagent.ping" {
		if params.Name == "microagent.describe" {
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: mcpToolResult(microagentCapabilityManifest())}
		}
		if params.Name == "workspace.estimate_cost" {
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: mcpToolResult(estimateWorkspaceCost(params.Arguments))}
		}
		result, err := runMCPTool(ctx, params.Name, params.Arguments)
		if err != nil {
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32602, Message: "tool failed", Data: mcpToolCallErrorData(err, result)}}
		}
		return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: mcpToolResult(result)}
	}
	return mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "pong"},
			},
		},
	}
}

func mcpToolCallErrorData(err error, envelope map[string]any) any {
	if envelope == nil {
		return mapMCPStructuredError(err, newRequestID())
	}
	structured, ok := envelope["error"].(mcpStructuredError)
	if !ok {
		return mapMCPStructuredError(err, newRequestID())
	}
	data := map[string]any{
		"kind":           structured.Kind,
		"message":        structured.Message,
		"correlation_id": structured.CorrelationID,
		"retryable":      structured.Retryable,
	}
	if structured.Remediation != "" {
		data["remediation"] = structured.Remediation
	}
	if structured.RetryAfterMS != 0 {
		data["retry_after_ms"] = structured.RetryAfterMS
	}
	if structured.PartialOutput != "" {
		data["partial_output"] = structured.PartialOutput
	}
	if retryExhausted, ok := envelope["retry_exhausted"].(bool); ok && retryExhausted {
		data["retry_exhausted"] = retryExhausted
		if retryCount, ok := envelope["retry_count"]; ok {
			data["retry_count"] = retryCount
		}
		if retryWallClockMS, ok := envelope["retry_wall_clock_ms"]; ok {
			data["retry_wall_clock_ms"] = retryWallClockMS
		}
	}
	return data
}

func microagentCapabilityManifest() map[string]any {
	operations := make([]map[string]any, 0, len(mcpTools()))
	for _, tool := range mcpTools() {
		name, _ := tool["name"].(string)
		if name == "microagent.ping" {
			continue
		}
		operations = append(operations, map[string]any{
			"name":               name,
			"description":        tool["description"],
			"input_schema":       tool["inputSchema"],
			"output_schema":      mcpToolOutputSchema(name),
			"side_effects":       mcpToolSideEffects(name),
			"idempotency":        mcpToolIdempotency(name),
			"principal_scope":    mcpToolPrincipalScope(name),
			"cost_class":         mcpToolCostClass(name),
			"structured_errors":  []string{string(errorKindTransient), string(errorKindPermanent), string(errorKindConflict), string(errorKindNotFound), string(errorKindResourceExhausted), string(errorKindUnsupported), string(errorKindPolicyDenied)},
			"correlation_id_key": "error.correlation_id",
		})
	}
	return map[string]any{
		"schema_version": "2026-05-19",
		"service":        "microagent",
		"version":        version,
		"transport":      "mcp_stdio",
		"output_mode":    string(outputModeAX),
		"agent_experience": map[string]any{
			"defaults": []string{
				"use compact summary outputs for repeated state checks",
				"request format=full only when a complete log, event, or inspect payload is needed",
				"use tail_lines and after_index for bounded stream polling instead of long-running follow calls",
				"use preview=true before destructive delete operations when user confirmation is still pending",
				"use the preview confirmation_token for host-mutating install/setup/build operations",
				"use idempotency_key on retryable mutation calls",
			},
			"evidence": "external AX harness runs showed lower token waste when agents used compact structured MCP state instead of scraping prose or repeatedly requesting full state",
		},
		"readiness_signals": []map[string]string{
			{"name": "guestReady", "description": "workspace reached a started terminal or runtime state"},
			{"name": "shellReady", "description": "interactive console shell is reachable and command round-trip works"},
			{"name": "execReady", "description": "structured exec service is reachable and a no-op exec succeeds end-to-end"},
			{"name": "resultReady", "description": "structured guest result is available"},
			{"name": "mediationReady", "description": "declared mediation channel target is live reachable for a running workspace"},
		},
		"operations": operations,
	}
}

func mcpToolOutputSchema(name string) map[string]any {
	if name == "workspace.exec" {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"result":            execResultSchema(),
				"error":             map[string]any{"type": "object"},
				"timing_ms":         map[string]any{"type": "integer"},
				"principal_context": map[string]any{"type": "object"},
			},
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result": map[string]any{"type": "object", "properties": map[string]any{
				"readiness": map[string]any{"type": "object", "properties": map[string]any{
					"guestReady":     readinessSignalSchema(),
					"shellReady":     readinessSignalSchema(),
					"execReady":      readinessSignalSchema(),
					"resultReady":    readinessSignalSchema(),
					"mediationReady": readinessSignalSchema(),
				}},
			}},
			"error":             map[string]any{"type": "object"},
			"timing_ms":         map[string]any{"type": "integer"},
			"principal_context": map[string]any{"type": "object"},
		},
	}
}

func workspaceExecInputSchema() map[string]any {
	return map[string]any{
		"name":                      map[string]any{"type": "string"},
		"command":                   map[string]any{"description": "Legacy shell command string, or argv array.", "oneOf": []map[string]any{{"type": "string"}, {"type": "array", "items": map[string]any{"type": "string"}}}},
		"argv":                      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"env":                       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"cwd":                       map[string]any{"type": "string"},
		"stdin":                     map[string]any{"type": "string", "description": "Bounded stdin bytes represented as a JSON string."},
		"timeout_ms":                map[string]any{"type": "integer"},
		"output_limit_bytes_stdout": map[string]any{"type": "integer"},
		"output_limit_bytes_stderr": map[string]any{"type": "integer"},
		"state_dir":                 map[string]any{"type": "string"},
	}
}

func execResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"protocol_version": map[string]any{"type": "string"},
			"started_at":       map[string]any{"type": "string"},
			"completed_at":     map[string]any{"type": "string"},
			"exit_code":        map[string]any{"type": "integer"},
			"stdout":           map[string]any{"type": "string", "contentEncoding": "base64"},
			"stderr":           map[string]any{"type": "string", "contentEncoding": "base64"},
			"stdout_truncated": map[string]any{"type": "boolean"},
			"stderr_truncated": map[string]any{"type": "boolean"},
			"status":           map[string]any{"type": "string", "enum": []string{string(execprotocol.ExecStatusExited), string(execprotocol.ExecStatusSignaled), string(execprotocol.ExecStatusTimedOut), string(execprotocol.ExecStatusFailedToStart)}},
			"error":            map[string]any{"type": "object"},
		},
	}
}

func readinessSignalSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ready":      map[string]any{"type": "boolean"},
			"observedAt": map[string]any{"type": "string"},
			"detail":     map[string]any{"type": "string"},
			"error":      map[string]any{"type": "string"},
		},
	}
}

func mcpToolSideEffects(name string) []string {
	switch name {
	case "host.networking.setup", "kernel.install", "rootfs.build":
		return []string{"host_state"}
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.stop", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.delete", "network.create", "network.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "cp", "artifacts.get":
		return []string{"host_state", "workspace_state"}
	default:
		return nil
	}
}

func mcpToolIdempotency(name string) string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.halt", "workspace.stop", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "network.create", "network.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "snapshot.delete":
		return "accepts idempotency_key on MCP arguments when idempotency is enabled"
	case "workspace.exec", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "host.networking.setup", "kernel.install", "rootfs.build", "cp", "artifacts.get":
		return "not inherently idempotent; idempotency_key can replay the first successful MCP envelope for a client-supplied key"
	case "workspace.list", "workspace.inspect", "workspace.result", "workspace.stats", "workspace.logs", "workspace.events", "workspace.estimate_cost", "artifacts.list", "snapshot.list", "network.inspect", "network.list", "volume.list", "volume.inspect", "images.list", "profiles.list", "host.inspect", "doctor.check", "contract.get", "kernel.verify", "microagent.describe":
		return "read_only"
	default:
		return "not_idempotent"
	}
}

func mcpToolPrincipalScope(name string) []string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.stop", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.list", "workspace.inspect", "workspace.result", "workspace.stats", "workspace.logs", "workspace.events", "workspace.clone", "workspace.apply", "workspace.commit":
		return []string{"workspace.lifecycle"}
	case "snapshot.create", "snapshot.list", "snapshot.delete":
		return []string{"workspace.snapshot"}
	case "network.inspect", "network.create", "network.list", "network.delete":
		return []string{"network.read", "network.write"}
	case "volume.create", "volume.list", "volume.inspect", "volume.delete":
		return []string{"volume.read", "volume.write"}
	case "images.pull", "images.list", "images.push", "images.tag", "images.delete", "images.prune":
		return []string{"images.read", "images.write"}
	case "artifacts.list", "cp", "artifacts.get":
		return []string{"workspace.files"}
	case "host.networking.setup", "kernel.install", "rootfs.build":
		return []string{"host.write"}
	case "host.inspect", "doctor.check", "contract.get", "kernel.verify", "profiles.list":
		return []string{"host.read"}
	default:
		return []string{"microagent.read"}
	}
}

func mcpToolCostClass(name string) string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "kernel.install", "rootfs.build":
		return "host_compute_and_storage"
	case "host.networking.setup":
		return "host_privileged"
	case "cp", "artifacts.get":
		return "host_io"
	default:
		return "metadata"
	}
}

func estimateWorkspaceCost(args map[string]any) map[string]any {
	resources := resourceConfig{MemoryMiB: defaultWorkspaceMemoryMiB, CPUCount: defaultWorkspaceCPUCount, SizeMiB: rootfs.DefaultSizeMiB}
	profileName := stringArg(args, "profile")
	if profileName == "" {
		profileName = defaultWorkspaceProfile
	}
	if profile, ok := lookupResourceProfile(profileName); ok {
		resources = profile.Resources
	}
	if memory := intArg(args, "memory_mib"); memory > 0 {
		resources.MemoryMiB = memory
	}
	if cpus := intArg(args, "cpus"); cpus > 0 {
		resources.CPUCount = cpus
	}
	if size := int64Arg(args, "size_mib"); size > 0 {
		resources.SizeMiB = size
	}
	pricePerHour := floatArg(args, "price_per_hour")
	estimate := map[string]any{
		"profile":             profileName,
		"memory_mib":          resources.MemoryMiB,
		"cpus":                resources.CPUCount,
		"disk_mib":            resources.SizeMiB,
		"estimated_boot_ms":   0,
		"price_per_hour":      pricePerHour,
		"estimated_cost_hour": float64(0),
	}
	if pricePerHour > 0 {
		estimate["estimated_cost_hour"] = pricePerHour
	}
	return map[string]any{"result": estimate, "timing_ms": int64(0), "principal_context": principalContextArg(args)}
}

func runMCPTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	start := time.Now()
	cacheKey := mcpIdempotencyCacheKey(name, args)
	if cacheKey != "" {
		if cached, ok := mcpIdempotencyCache.Load(cacheKey); ok {
			result := cloneMCPMap(cached.(map[string]any))
			result["idempotency_replay"] = true
			return result, nil
		}
	}
	if preview := previewDestructiveMCPTool(name, args); preview != nil {
		return preview, nil
	}
	if preview, err := requireConfirmedMCPHostMutation(name, args); preview != nil || err != nil {
		return preview, err
	}
	if name == "workspace.exec" {
		envelope, err := runMCPWorkspaceExec(ctx, args, start)
		if cacheKey != "" && err == nil {
			mcpIdempotencyCache.Store(cacheKey, cloneMCPMap(envelope))
			envelope["idempotency_replay"] = false
		}
		return envelope, err
	}
	cliArgs, err := mcpCLIArgs(name, args)
	if err != nil {
		return nil, err
	}
	result, cliErr := runCLIForMCP(ctx, cliArgs)
	if cliErr == nil && name == "workspace.inspect" && !strings.EqualFold(stringArg(args, "format"), "full") {
		result = summarizeWorkspaceInspect(result)
	}
	if cliErr == nil && name == "workspace.logs" && !strings.EqualFold(stringArg(args, "format"), "full") {
		result = summarizeWorkspaceLogs(result, intArg(args, "tail_lines"))
	}
	if cliErr == nil && name == "workspace.events" && !strings.EqualFold(stringArg(args, "format"), "full") {
		result = summarizeWorkspaceEvents(result, intArg(args, "limit"), intArg(args, "after_index"))
	}
	envelope := map[string]any{
		"result":            result,
		"timing_ms":         time.Since(start).Milliseconds(),
		"principal_context": principalContextArg(args),
	}
	if cliErr != nil {
		envelope["error"] = mapMCPStructuredError(cliErr, newRequestID())
	}
	if cacheKey != "" && cliErr == nil {
		mcpIdempotencyCache.Store(cacheKey, cloneMCPMap(envelope))
		envelope["idempotency_replay"] = false
	}
	return envelope, cliErr
}

func requireConfirmedMCPHostMutation(name string, args map[string]any) (map[string]any, error) {
	if !mcpHostMutationTool(name) {
		return nil, nil
	}
	token := mcpConfirmationToken(name, args)
	if boolArg(args, "preview") {
		return map[string]any{
			"result": map[string]any{
				"preview":            true,
				"tool":               name,
				"actions":            mcpHostMutationActions(name, args),
				"confirmation_token": token,
				"confirm_with":       "call the same tool with confirm_token set to confirmation_token and preview omitted or false",
			},
			"timing_ms":         int64(0),
			"principal_context": principalContextArg(args),
		}, nil
	}
	if stringArg(args, "confirm_token") != token {
		return nil, fmt.Errorf("%s requires preview confirmation; call with preview=true and retry with the returned confirm_token", name)
	}
	return nil, nil
}

func mcpHostMutationTool(name string) bool {
	switch name {
	case "host.networking.setup", "kernel.install", "rootfs.build":
		return true
	default:
		return false
	}
}

func mcpHostMutationActions(name string, args map[string]any) []string {
	switch name {
	case "host.networking.setup":
		action := stringArg(args, "action")
		if action == "" {
			action = "apply"
		}
		if action == "revert" {
			return []string{"revert Linux privileged networking setup"}
		}
		return []string{"enable Linux ip_forward", "grant CAP_NET_ADMIN to the supervisor binary"}
	case "kernel.install":
		return []string{"download or copy kernel artifact", "write kernel artifact to host path", "verify sha256 when supplied or defaulted"}
	case "rootfs.build":
		return []string{"pull OCI image layers when needed", "build ext4 rootfs", "write rootfs output path"}
	default:
		return nil
	}
}

func mcpConfirmationToken(name string, args map[string]any) string {
	clean := map[string]any{}
	for key, value := range args {
		switch key {
		case "preview", "confirm_token", "idempotency_key", "principal":
			continue
		default:
			clean[key] = value
		}
	}
	payload, _ := json.Marshal(map[string]any{"tool": name, "arguments": clean})
	sum := sha256.Sum256(payload)
	return "mcp-confirm-" + hex.EncodeToString(sum[:8])
}

func runMCPWorkspaceExec(ctx context.Context, args map[string]any, start time.Time) (map[string]any, error) {
	if err := requireToolArgs(args, "workspace.exec", "name"); err != nil {
		return nil, err
	}
	req, err := mcpExecRequest(args)
	if err != nil {
		return nil, err
	}
	stateDir := stringArg(args, "state_dir")
	if stateDir == "" {
		stateDir = defaultStateDir()
	}
	result, err, retryMeta := mcpWorkspaceExec(ctx, workspace.Options{Name: stringArg(args, "name"), StateDir: stateDir}, req)
	envelope := map[string]any{
		"result":              result,
		"timing_ms":           time.Since(start).Milliseconds(),
		"retry_count":         retryMeta.Count,
		"retry_wall_clock_ms": retryMeta.WallClockMilliseconds(),
		"metadata": map[string]any{
			"retry_count":         retryMeta.Count,
			"retry_wall_clock_ms": retryMeta.WallClockMilliseconds(),
		},
		"principal_context": principalContextArg(args),
	}
	if retryMeta.Exhausted {
		envelope["retry_exhausted"] = true
	}
	if err != nil {
		envelope["error"] = mapMCPStructuredError(err, newRequestID())
	}
	return envelope, err
}

func mcpExecRequest(args map[string]any) (execprotocol.ExecRequest, error) {
	argv, err := mcpExecArgv(args)
	if err != nil {
		return execprotocol.ExecRequest{}, err
	}
	req := execprotocol.NewExecRequest(argv)
	req.Env = mcpStringMapArg(args, "env")
	req.Cwd = stringArg(args, "cwd")
	req.Stdin = []byte(rawStringArg(args, "stdin"))
	req.TimeoutMS = int64Arg(args, "timeout_ms")
	req.OutputLimitBytesStdout = int64Arg(args, "output_limit_bytes_stdout")
	req.OutputLimitBytesStderr = int64Arg(args, "output_limit_bytes_stderr")
	if err := req.Validate(); err != nil {
		return execprotocol.ExecRequest{}, err
	}
	return req, nil
}

func mcpExecArgv(args map[string]any) ([]string, error) {
	if argv, ok, err := stringSliceArg(args, "argv"); ok || err != nil {
		return argv, err
	}
	if argv, ok, err := stringSliceArg(args, "command"); ok || err != nil {
		return argv, err
	}
	command := stringArg(args, "command")
	if command != "" {
		return []string{"sh", "-lc", command}, nil
	}
	return nil, fmt.Errorf("workspace.exec requires argv or command")
}

func previewDestructiveMCPTool(name string, args map[string]any) map[string]any {
	if !boolArg(args, "preview") {
		return nil
	}
	switch name {
	case "workspace.delete":
		force := boolArg(args, "force")
		action := "delete"
		if force {
			action = "force-delete"
		}
		return map[string]any{
			"result": map[string]any{
				"preview":   true,
				"tool":      name,
				"workspace": stringArg(args, "name"),
				"actions":   []string{action, "remove workspace disk and state"},
			},
			"timing_ms":         int64(0),
			"principal_context": principalContextArg(args),
		}
	case "network.delete", "volume.delete":
		return map[string]any{
			"result": map[string]any{
				"preview": true,
				"tool":    name,
				"name":    stringArg(args, "name"),
				"actions": []string{"delete " + strings.TrimSuffix(name, ".delete")},
				"force":   boolArg(args, "force"),
			},
			"timing_ms":         int64(0),
			"principal_context": principalContextArg(args),
		}
	case "snapshot.delete":
		return map[string]any{
			"result": map[string]any{
				"preview": true,
				"tool":    name,
				"name":    stringArg(args, "name"),
				"tag":     stringArg(args, "tag"),
				"actions": []string{"delete snapshot"},
			},
			"timing_ms":         int64(0),
			"principal_context": principalContextArg(args),
		}
	case "images.delete", "images.prune":
		actions := []string{"delete stale image records"}
		if name == "images.delete" {
			actions = []string{"delete image record"}
		}
		if boolArg(args, "delete_files") {
			actions = append(actions, "delete cached rootfs files")
		}
		return map[string]any{
			"result": map[string]any{
				"preview":      true,
				"tool":         name,
				"image":        stringArg(args, "image"),
				"delete_files": boolArg(args, "delete_files"),
				"actions":      actions,
			},
			"timing_ms":         int64(0),
			"principal_context": principalContextArg(args),
		}
	default:
		return nil
	}
}

func summarizeWorkspaceInspect(result any) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	summary := map[string]any{
		"format":    "summary",
		"ok":        resp["ok"],
		"backend":   resp["backend"],
		"error":     resp["error"],
		"error_cnt": 0,
	}
	if text, ok := resp["error"].(string); ok && strings.TrimSpace(text) != "" {
		summary["error_cnt"] = 1
	}
	if event, ok := resp["event"].(map[string]any); ok {
		summary["state"] = event["state"]
		if identity, ok := event["identity"].(map[string]any); ok {
			summary["workspace"] = identity["runtimeID"]
		}
	}
	var next []string
	switch fmt.Sprint(summary["state"]) {
	case "running", "starting":
		next = []string{"workspace.exec", "workspace.halt", "workspace.delete"}
	case "prepared", "halted", "stopped":
		next = []string{"workspace.start", "workspace.delete"}
	case "failed", "quarantined":
		next = []string{"workspace.inspect", "workspace.delete"}
	default:
		next = []string{"workspace.inspect"}
	}
	summary["next_decision_points"] = next
	return summary
}

func summarizeWorkspaceLogs(result any, tailLimit int) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	if tailLimit <= 0 {
		tailLimit = 8
	}
	logs, _ := resp["logs"].(string)
	lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	tail := lines
	if len(tail) > tailLimit {
		tail = tail[len(tail)-tailLimit:]
	}
	return map[string]any{
		"format":      "summary",
		"workspace":   resp["workspace"],
		"byte_count":  len(logs),
		"line_count":  len(lines),
		"tail_count":  len(tail),
		"tail_lines":  tail,
		"full_output": "call workspace.logs with format=full to retrieve the complete serial log buffer",
	}
}

func summarizeWorkspaceEvents(result any, limit int, afterIndex int) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	if limit <= 0 {
		limit = 5
	}
	events, _ := resp["events"].([]any)
	startIndex := 0
	if afterIndex > 0 && afterIndex < len(events) {
		startIndex = afterIndex
	}
	recent := events[startIndex:]
	if len(recent) > limit {
		recent = recent[len(recent)-limit:]
	}
	summary := map[string]any{
		"format":             "summary",
		"workspace":          resp["workspace"],
		"event_count":        len(events),
		"after_index":        afterIndex,
		"next_after_index":   len(events),
		"returned_count":     len(recent),
		"recent":             recent,
		"full_output":        "call workspace.events with format=full to retrieve all lifecycle events",
		"polling_contract":   "pass next_after_index as after_index on the next call to fetch newer events without a long-running follow call",
		"truncated_by_limit": len(events[startIndex:]) > len(recent),
	}
	if len(events) > 0 {
		if latest, ok := events[len(events)-1].(map[string]any); ok {
			summary["latest_state"] = latest["state"]
			summary["latest_observed_at"] = latest["observedAt"]
			summary["latest_detail"] = latest["detail"]
		}
	}
	return summary
}

func mcpIdempotencyCacheKey(name string, args map[string]any) string {
	key := stringArg(args, "idempotency_key")
	if key == "" || !mcpMutationTool(name) {
		return ""
	}
	return name + ":" + key
}

func mcpMutationTool(name string) bool {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.stop", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.delete", "network.create", "network.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "host.networking.setup", "kernel.install", "rootfs.build", "cp", "artifacts.get":
		return true
	default:
		return false
	}
}

func cloneMCPMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func principalContextArg(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	raw, ok := args["principal"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	for _, key := range []string{"workload_identity", "delegated_authority", "purpose", "correlation_id"} {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	return out
}

func principalContextSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workload_identity":   map[string]any{"type": "string"},
			"delegated_authority": map[string]any{"type": "string"},
			"purpose":             map[string]any{"type": "string"},
			"correlation_id":      map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

func mcpCLIArgs(name string, args map[string]any) ([]string, error) {
	stateDir := stringArg(args, "state_dir")
	switch name {
	case "workspace.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "create", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-image", stringArg(args, "image"))
		cli = appendOptionalFlag(cli, "-exec", stringArg(args, "exec"))
		cli = appendOptionalFlag(cli, "-profile", stringArg(args, "profile"))
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "dry_run") {
			cli = append(cli, "-dry-run")
		}
		return cli, nil
	case "workspace.start":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "start", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.exec":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		argv, err := mcpExecArgv(args)
		if err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "exec", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = append(cli, "--")
		cli = append(cli, argv...)
		return cli, nil
	case "workspace.halt":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "halt", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.stop":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "stop", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.kill":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "kill", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.quarantine":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "quarantine", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.pause":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "pause", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.resume":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "resume", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "delete", stringArg(args, "name"), "-yes"}
		if boolArg(args, "force") {
			cli = append(cli, "-force")
		}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "workspace.list":
		return appendOptionalFlag([]string{"--mode=ax", "ps"}, "-state-dir", stateDir), nil
	case "workspace.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "status", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.result":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "result", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.stats":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "stats", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.logs":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "logs", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.events":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "events", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.clone":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "clone", stringArg(args, "source"), stringArg(args, "target")}, "-state-dir", stateDir), nil
	case "workspace.apply":
		if err := requireToolArgs(args, name, "file"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "apply", "-file", stringArg(args, "file")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		cli = appendOptionalFlag(cli, "-supervisor", stringArg(args, "supervisor"))
		return cli, nil
	case "workspace.commit":
		if err := requireToolArgs(args, name, "name", "image"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "commit", stringArg(args, "name"), stringArg(args, "image")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		if boolArg(args, "push") {
			cli = append(cli, "-push")
		}
		return cli, nil
	case "artifacts.list":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "artifacts", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "snapshot.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "snapshot", "create", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-tag", stringArg(args, "tag"))
		return cli, nil
	case "snapshot.list":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "snapshot", "list", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "snapshot.delete":
		if err := requireToolArgs(args, name, "name", "tag"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "snapshot", "rm", stringArg(args, "name"), stringArg(args, "tag")}, "-state-dir", stateDir), nil
	case "network.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "network", "inspect", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "network.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "network", "create", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-subnet", stringArg(args, "subnet"))
		return cli, nil
	case "network.list":
		return appendOptionalFlag([]string{"--mode=ax", "network", "ls"}, "-state-dir", stateDir), nil
	case "network.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "network", "rm", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "force") {
			cli = append(cli, "-force")
		}
		return cli, nil
	case "volume.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "volume", "create", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if size := int64Arg(args, "size_mib"); size > 0 {
			cli = append(cli, "-size-mib", strconv.FormatInt(size, 10))
		}
		return cli, nil
	case "volume.list":
		return appendOptionalFlag([]string{"--mode=ax", "volume", "ls"}, "-state-dir", stateDir), nil
	case "volume.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "volume", "inspect", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "volume.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "volume", "rm", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "force") {
			cli = append(cli, "-force")
		}
		return cli, nil
	case "images.pull":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "images", "pull", stringArg(args, "image")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		return cli, nil
	case "images.list":
		return appendOptionalFlag([]string{"--mode=ax", "images", "list"}, "-state-dir", stateDir), nil
	case "images.push":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "images", "push", stringArg(args, "image")}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "images.tag":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--mode=ax", "images", "tag", stringArg(args, "source"), stringArg(args, "target")}, "-state-dir", stateDir), nil
	case "images.delete":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "images", "rm", stringArg(args, "image")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "delete_files") {
			cli = append(cli, "-delete", "-yes")
		}
		return cli, nil
	case "images.prune":
		cli := []string{"--mode=ax", "images", "prune"}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "delete_files") {
			cli = append(cli, "-delete", "-yes")
		}
		return cli, nil
	case "profiles.list":
		return []string{"--mode=ax", "profiles"}, nil
	case "host.inspect":
		cli := []string{"--mode=ax", "host"}
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		cli = appendOptionalFlag(cli, "-supervisor", stringArg(args, "supervisor"))
		return cli, nil
	case "doctor.check":
		cli := []string{"--mode=ax", "doctor"}
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		cli = appendOptionalFlag(cli, "-supervisor", stringArg(args, "supervisor"))
		return cli, nil
	case "host.networking.setup":
		cli := []string{"--mode=ax", "host", "setup-networking"}
		switch stringArg(args, "action") {
		case "", "apply":
		case "revert":
			cli = append(cli, "-revert")
		default:
			return nil, fmt.Errorf("host.networking.setup action must be apply or revert")
		}
		return cli, nil
	case "contract.get":
		return []string{"--mode=ax", "contract"}, nil
	case "kernel.verify":
		cli := []string{"--mode=ax", "kernel", "verify"}
		cli = appendOptionalFlag(cli, "-path", stringArg(args, "path"))
		cli = appendOptionalFlag(cli, "-sha256", stringArg(args, "sha256"))
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		return cli, nil
	case "kernel.install":
		cli := []string{"--mode=ax", "kernel", "install"}
		cli = appendOptionalFlag(cli, "-url", stringArg(args, "url"))
		cli = appendOptionalFlag(cli, "-from", stringArg(args, "from"))
		cli = appendOptionalFlag(cli, "-sha256", stringArg(args, "sha256"))
		cli = appendOptionalFlag(cli, "-out", stringArg(args, "out"))
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		return cli, nil
	case "rootfs.build":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "rootfs", "build", "-image", stringArg(args, "image")}
		cli = appendOptionalFlag(cli, "-os", stringArg(args, "os"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		cli = appendOptionalFlag(cli, "-out", stringArg(args, "out"))
		cli = appendOptionalFlag(cli, "-init", stringArg(args, "init"))
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-mke2fs", stringArg(args, "mke2fs"))
		if size := int64Arg(args, "size_mib"); size > 0 {
			cli = append(cli, "-size-mib", strconv.FormatInt(size, 10))
		}
		cli = appendOptionalFlag(cli, "-exec", stringArg(args, "exec"))
		if boolArg(args, "allow_mutable") {
			cli = append(cli, "-allow-mutable")
		}
		if boolArg(args, "keep_stage") {
			cli = append(cli, "-keep-stage")
		}
		cli = appendOptionalFlag(cli, "-stage-snapshot", stringArg(args, "stage_snapshot"))
		return cli, nil
	case "cp":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "cp", stringArg(args, "source"), stringArg(args, "target")}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "artifacts.get":
		if err := requireToolArgs(args, name, "name", "artifact", "target"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "artifacts", "get", stringArg(args, "name"), stringArg(args, "artifact"), stringArg(args, "target")}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	default:
		return nil, fmt.Errorf("unsupported MCP tool %s", name)
	}
}

func runCLIForMCP(ctx context.Context, args []string) (any, error) {
	dir, err := os.MkdirTemp("", "microagent-mcp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	err = run(ctx, args, stdout)
	closeErr := stdout.Close()
	if err == nil {
		err = closeErr
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil && err == nil {
		err = readErr
	}
	var parsed any
	if len(bytes.TrimSpace(data)) != 0 && json.Unmarshal(data, &parsed) == nil {
		return parsed, err
	}
	return map[string]any{"output": string(data)}, err
}

func mcpToolResult(value any) map[string]any {
	data, _ := json.Marshal(value)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}}
}

func stringArg(args map[string]any, name string) string {
	if args == nil {
		return ""
	}
	if value, ok := args[name].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func rawStringArg(args map[string]any, name string) string {
	if args == nil {
		return ""
	}
	if value, ok := args[name].(string); ok {
		return value
	}
	return ""
}

func stringSliceArg(args map[string]any, name string) ([]string, bool, error) {
	if args == nil {
		return nil, false, nil
	}
	raw, ok := args[name]
	if !ok || raw == nil {
		return nil, false, nil
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...), true, nil
	case []any:
		out := make([]string, 0, len(value))
		for i, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, true, fmt.Errorf("%s[%d] must be a string", name, i)
			}
			out = append(out, text)
		}
		return out, true, nil
	default:
		return nil, false, nil
	}
}

func mcpStringMapArg(args map[string]any, name string) map[string]string {
	if args == nil {
		return nil
	}
	raw, ok := args[name].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

func boolArg(args map[string]any, name string) bool {
	if args == nil {
		return false
	}
	value, ok := args[name].(bool)
	return ok && value
}

func intArg(args map[string]any, name string) int {
	if args == nil {
		return 0
	}
	switch value := args[name].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func int64Arg(args map[string]any, name string) int64 {
	return int64(intArg(args, name))
}

func floatArg(args map[string]any, name string) float64 {
	if args == nil {
		return 0
	}
	switch value := args[name].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	default:
		return 0
	}
}

func appendOptionalFlag(args []string, name, value string) []string {
	if strings.TrimSpace(value) == "" {
		return args
	}
	return append(args, name, value)
}

func requireToolArgs(args map[string]any, tool string, names ...string) error {
	for _, name := range names {
		if stringArg(args, name) == "" {
			return fmt.Errorf("%s requires %s", tool, name)
		}
	}
	return nil
}

func encodeMCPTestMessage(value any) []byte {
	data, _ := json.Marshal(value)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return buf.Bytes()
}

func printServeHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent serve

Commands:
  mcp                 Serve the microagent MCP stdio endpoint
`)
}

func printServeMCPHelp(stdout io.Writer) {
	fmt.Fprint(stdout, `microagent serve mcp

Serve the microagent MCP stdio endpoint.

This command is a foreground stdio transport for MCP clients. Configure the
client to launch it as the MCP server command; do not run it as a background
daemon from an interactive shell.
`)
}
