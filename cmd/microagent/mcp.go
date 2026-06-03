package main

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

var mcpIdempotencyCache sync.Map

const (
	mcpExecMaxTransientRetries = 3
	mcpExecRetryBackoff        = 750 * time.Millisecond
	mcpExecRetryJitterWindow   = 100 * time.Millisecond
	mcpExecRetryBudget         = 3 * time.Second
)

var (
	mcpWorkspaceExec   = workspace.Exec
	mcpExecRetryJitter = randomMCPExecRetryJitter
	mcpExecRetrySleep  = sleepMCPExecRetry
	mcpExecRetryNow    = time.Now
	mcpExecRetrySince  = time.Since
)

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
	globalOutputMode = outputModeAX
	return serveMCP(ctx, stdin, stdout)
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
		mcpTool("workspace.delete", "Delete a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("workspace.list", "List workspaces.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.inspect", "Inspect workspace state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "format": map[string]any{"type": "string", "enum": []string{"summary", "full"}}}),
		mcpTool("workspace.estimate_cost", "Estimate workspace resource consumption before creating or starting it.", nil, map[string]any{"profile": map[string]any{"type": "string"}, "memory_mib": map[string]any{"type": "integer"}, "cpus": map[string]any{"type": "integer"}, "size_mib": map[string]any{"type": "integer"}, "price_per_hour": map[string]any{"type": "number"}}),
		mcpTool("images.pull", "Pull a reusable image rootfs.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}}),
		mcpTool("images.list", "List reusable local image records.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.pull", "Pull a GGUF model from HuggingFace into the local store.", []string{"model"}, map[string]any{"model": map[string]any{"type": "string"}, "token": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.list", "List locally stored models.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.remove", "Remove a model from the local store.", []string{"model"}, map[string]any{"model": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.prune", "Prune local model records whose blobs are missing.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.serve", "Start (or reuse) a local host model server for a stored/pulled model.", []string{"model"},
			map[string]any{"model": map[string]any{"type": "string"}, "dedicated": map[string]any{"type": "boolean"}, "token": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.stop", "Stop the local host model server(s) for a model.", []string{"model"},
			map[string]any{"model": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.runners", "List running local model servers.", nil,
			map[string]any{"state_dir": map[string]any{"type": "string"}}),
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
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.delete", "images.pull", "cp", "artifacts.get":
		return []string{"host_state", "workspace_state"}
	case "models.pull", "models.remove", "models.prune", "models.serve", "models.stop":
		return []string{"host_state"}
	default:
		return nil
	}
}

func mcpToolIdempotency(name string) string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.halt", "workspace.delete", "images.pull", "models.pull":
		return "accepts idempotency_key on MCP arguments when idempotency is enabled"
	case "workspace.list", "workspace.inspect", "workspace.estimate_cost", "images.list", "microagent.describe", "models.list", "models.runners":
		return "read_only"
	default:
		return "not_idempotent"
	}
}

func mcpToolPrincipalScope(name string) []string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.delete":
		return []string{"workspace.lifecycle"}
	case "images.pull", "images.list":
		return []string{"images.read", "images.write"}
	case "cp", "artifacts.get":
		return []string{"workspace.files"}
	case "models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners":
		return []string{"models.read", "models.write"}
	default:
		return []string{"microagent.read"}
	}
}

func mcpToolCostClass(name string) string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "images.pull", "models.pull", "models.serve":
		return "host_compute_and_storage"
	case "cp", "artifacts.get", "models.remove", "models.prune", "models.stop":
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
	if cliErr == nil && name == "workspace.inspect" && strings.EqualFold(stringArg(args, "format"), "summary") {
		result = summarizeWorkspaceInspect(result)
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
	result, err, retryMeta := runMCPWorkspaceExecWithRetry(ctx, workspace.Options{Name: stringArg(args, "name"), StateDir: stateDir}, req)
	envelope := map[string]any{
		"result":              result,
		"timing_ms":           time.Since(start).Milliseconds(),
		"retry_count":         retryMeta.count,
		"retry_wall_clock_ms": retryMeta.wallClock.Milliseconds(),
		"metadata": map[string]any{
			"retry_count":         retryMeta.count,
			"retry_wall_clock_ms": retryMeta.wallClock.Milliseconds(),
		},
		"principal_context": principalContextArg(args),
	}
	if retryMeta.exhausted {
		envelope["retry_exhausted"] = true
	}
	if err != nil {
		envelope["error"] = mapMCPStructuredError(err, newRequestID())
	}
	return envelope, err
}

type mcpExecRetryMetadata struct {
	count     int
	wallClock time.Duration
	exhausted bool
}

type mcpExecRetryExhaustedError struct {
	Retries   int
	WallClock time.Duration
	LastErr   error
}

func (err mcpExecRetryExhaustedError) Error() string {
	return fmt.Sprintf("structured exec transient connection failure persisted after %d retries over %s retry window: %v", err.Retries, err.WallClock.Round(time.Millisecond), err.LastErr)
}

func (err mcpExecRetryExhaustedError) Unwrap() error {
	return err.LastErr
}

func runMCPWorkspaceExecWithRetry(ctx context.Context, opts workspace.Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, error, mcpExecRetryMetadata) {
	var meta mcpExecRetryMetadata
	var retryStart time.Time
	for {
		result, err := mcpWorkspaceExec(ctx, opts, req)
		if err == nil || !isRetryableMCPExecTransient(err) {
			if meta.count > 0 {
				meta.wallClock = mcpExecRetrySince(retryStart)
			}
			return result, err, meta
		}
		if retryStart.IsZero() {
			retryStart = mcpExecRetryNow()
		}
		meta.wallClock = mcpExecRetrySince(retryStart)
		if meta.count >= mcpExecMaxTransientRetries {
			meta.exhausted = true
			return result, mcpExecRetryExhaustedError{Retries: meta.count, WallClock: meta.wallClock, LastErr: err}, meta
		}
		backoff := mcpExecRetryBackoff + mcpExecRetryJitter()
		if backoff < 0 {
			backoff = 0
		}
		if meta.wallClock+backoff > mcpExecRetryBudget {
			meta.exhausted = true
			return result, mcpExecRetryExhaustedError{Retries: meta.count, WallClock: meta.wallClock, LastErr: err}, meta
		}
		meta.count++
		if err := mcpExecRetrySleep(ctx, backoff); err != nil {
			if meta.count > 0 {
				meta.wallClock = mcpExecRetrySince(retryStart)
			}
			return result, err, meta
		}
	}
}

func isRetryableMCPExecTransient(err error) bool {
	var unreachable execclient.UnreachableError
	if errors.As(err, &unreachable) {
		return isMCPExecConnectionRefused(unreachable.Err) || isMCPExecConnectionTimeout(unreachable.Err) || isMCPExecConnectionReset(unreachable.Err)
	}
	var protocolErr execclient.ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Op == "decode response" && isMCPExecConnectionReset(protocolErr.Err)
	}
	return false
}

func isMCPExecConnectionRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "connection refused")
}

func isMCPExecConnectionReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || strings.Contains(strings.ToLower(err.Error()), "connection reset by peer")
}

func isMCPExecConnectionTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func randomMCPExecRetryJitter() time.Duration {
	windowMS := int64(mcpExecRetryJitterWindow / time.Millisecond)
	if windowMS <= 0 {
		return 0
	}
	var b [1]byte
	if _, err := crand.Read(b[:]); err != nil {
		return 0
	}
	offset := int64(b[0]) % (2*windowMS + 1)
	return time.Duration(offset-windowMS) * time.Millisecond
}

func sleepMCPExecRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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

func mcpIdempotencyCacheKey(name string, args map[string]any) string {
	key := stringArg(args, "idempotency_key")
	if key == "" || !mcpMutationTool(name) {
		return ""
	}
	return name + ":" + key
}

func mcpMutationTool(name string) bool {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.delete", "images.pull", "cp", "artifacts.get", "models.pull", "models.remove", "models.prune", "models.serve", "models.stop":
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
	case "models.pull":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "model", "pull", stringArg(args, "model")}
		cli = appendOptionalFlag(cli, "-token", stringArg(args, "token"))
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.list":
		return appendOptionalFlag([]string{"--mode=ax", "model", "ls"}, "-state-dir", stateDir), nil
	case "models.remove":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "model", "rm", stringArg(args, "model")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.prune":
		return appendOptionalFlag([]string{"--mode=ax", "model", "prune"}, "-state-dir", stateDir), nil
	case "models.serve":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "model", "serve", stringArg(args, "model")}
		if boolArg(args, "dedicated") {
			cli = append(cli, "-dedicated")
		}
		cli = appendOptionalFlag(cli, "-token", stringArg(args, "token"))
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.stop":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "model", "stop", stringArg(args, "model")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.runners":
		return appendOptionalFlag([]string{"--mode=ax", "model", "runners"}, "-state-dir", stateDir), nil
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
`)
}
