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
	"strconv"
	"strings"
	"sync"
	"time"
)

var mcpIdempotencyCache sync.Map

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

func handleMCPMessage(ctx context.Context, msg json.RawMessage) (mcpResponse, bool) {
	var req mcpRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		return mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: -32700, Message: "parse error", Data: mapStructuredError(err, newRequestID())},
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
			Error:   &mcpError{Code: -32601, Message: "method not found", Data: mapStructuredError(fmt.Errorf("unsupported MCP method %s", req.Method), newRequestID())},
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
		mcpTool("workspace.exec", "Send a console command to a running workspace.", []string{"name", "command"}, map[string]any{"name": map[string]any{"type": "string"}, "command": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.halt", "Halt a workspace and preserve disk state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.delete", "Delete a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}}),
		mcpTool("workspace.list", "List workspaces.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.inspect", "Inspect workspace state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("images.pull", "Pull a reusable image rootfs.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}}),
		mcpTool("images.list", "List reusable local image records.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
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
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32602, Message: "invalid params", Data: mapStructuredError(err, newRequestID())}}
		}
	}
	if params.Name != "microagent.ping" {
		if params.Name == "microagent.describe" {
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: mcpToolResult(microagentCapabilityManifest())}
		}
		result, err := runMCPTool(ctx, params.Name, params.Arguments)
		if err != nil {
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32602, Message: "tool failed", Data: mapStructuredError(err, newRequestID())}}
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
			"output_schema":      mcpToolOutputSchema(),
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
		"operations":     operations,
	}
}

func mcpToolOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result":            map[string]any{"type": "object"},
			"error":             map[string]any{"type": "object"},
			"timing_ms":         map[string]any{"type": "integer"},
			"principal_context": map[string]any{"type": "object"},
		},
	}
}

func mcpToolSideEffects(name string) []string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.delete", "images.pull", "cp", "artifacts.get":
		return []string{"host_state", "workspace_state"}
	default:
		return nil
	}
}

func mcpToolIdempotency(name string) string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.halt", "workspace.delete", "images.pull":
		return "accepts idempotency_key on MCP arguments when idempotency is enabled"
	case "workspace.list", "workspace.inspect", "images.list", "microagent.describe":
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
	default:
		return []string{"microagent.read"}
	}
}

func mcpToolCostClass(name string) string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.exec", "images.pull":
		return "host_compute_and_storage"
	case "cp", "artifacts.get":
		return "host_io"
	default:
		return "metadata"
	}
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
	cliArgs, err := mcpCLIArgs(name, args)
	if err != nil {
		return nil, err
	}
	result, cliErr := runCLIForMCP(ctx, cliArgs)
	envelope := map[string]any{
		"result":    result,
		"timing_ms": time.Since(start).Milliseconds(),
	}
	if cliErr != nil {
		envelope["error"] = mapStructuredError(cliErr, newRequestID())
	}
	if cacheKey != "" && cliErr == nil {
		mcpIdempotencyCache.Store(cacheKey, cloneMCPMap(envelope))
		envelope["idempotency_replay"] = false
	}
	return envelope, cliErr
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
	case "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.delete", "images.pull", "cp", "artifacts.get":
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
		if err := requireToolArgs(args, name, "name", "command"); err != nil {
			return nil, err
		}
		cli := []string{"--mode=ax", "connect", stringArg(args, "name"), "-send", stringArg(args, "command")}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
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

func boolArg(args map[string]any, name string) bool {
	if args == nil {
		return false
	}
	value, ok := args[name].(bool)
	return ok && value
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
