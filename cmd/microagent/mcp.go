package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

var mcpIdempotencyCache = newMCPIdempotencyStore(mcpIdempotencyTTL, mcpIdempotencyMaxEntries)

var mcpWorkspaceExec = workspace.ExecWithMetadata
var mcpWorkspaceControl = workspace.Control
var mcpWorkspaceQuarantine = workspace.Quarantine
var mcpWorkspaceDelete = workspace.Delete
var mcpSnapshotCreate = workspace.Snapshot
var mcpSnapshotForensic = workspace.SnapshotForensic
var mcpSnapshotDelete = workspace.SnapshotRemove

const mcpClientSetupMessage = `microagent serve mcp is launched by MCP clients over stdio; it is not an interactive shell command.

Add it as a stdio MCP server in your client config. For example:
  Codex: codex mcp add microagent -- microagent serve mcp
  Claude Code: claude mcp add --transport stdio --scope user microagent -- microagent serve mcp

For JSON-based clients, configure a stdio server named "microagent":
  command: microagent
  args: ["serve", "mcp"]

Client-specific examples: docs/cli/serve.md#configure-mcp-clients
`

func runServe(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printServeHelp(stdout)
		return nil
	}
	switch args[0] {
	case "mcp":
		return runServeMCP(ctx, args[1:], os.Stdin, stdout)
	default:
		return fmt.Errorf("unknown serve command: %s\n\nAvailable serve command: mcp", args[0])
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
		return fmt.Errorf("%s", strings.TrimSpace(mcpClientSetupMessage))
	}
	return serveMCP(ctx, stdin, stdout)
}

func mcpStdioIsInteractive(stdin io.Reader, stdout io.Writer) bool {
	inFile, inOK := stdin.(*os.File)
	outFile, outOK := stdout.(*os.File)
	return inOK && outOK && fileIsTerminal(inFile) && fileIsTerminal(outFile)
}

func serveMCP(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	server := &mcpStdioServer{in: bufio.NewReader(stdin), out: stdout}
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
	in      *bufio.Reader
	out     io.Writer
	framing mcpStdioFraming
	decoder *json.Decoder
}

type mcpStdioFraming int

const (
	mcpStdioFramingUnknown mcpStdioFraming = iota
	mcpStdioFramingHeader
	mcpStdioFramingRawJSON
)

func (s *mcpStdioServer) readMessage() (json.RawMessage, error) {
	if s.framing == mcpStdioFramingRawJSON {
		return s.readRawJSONMessage()
	}
	first, err := s.in.Peek(1)
	if err != nil {
		return nil, err
	}
	if first[0] == '{' || first[0] == '[' {
		s.framing = mcpStdioFramingRawJSON
		s.decoder = json.NewDecoder(s.in)
		return s.readRawJSONMessage()
	}
	s.framing = mcpStdioFramingHeader
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

func (s *mcpStdioServer) readRawJSONMessage() (json.RawMessage, error) {
	if s.decoder == nil {
		s.decoder = json.NewDecoder(s.in)
	}
	var msg json.RawMessage
	if err := s.decoder.Decode(&msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (s *mcpStdioServer) writeMessage(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if s.framing == mcpStdioFramingRawJSON {
		_, err = fmt.Fprintf(s.out, "%s\n", data)
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

type mcpInitializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
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

// structuredErrorMap renders a structuredError as a plain JSON object so the
// MCP layer can attach a sibling `meta` transport block to it inside a
// JSON-RPC error.data payload.
func structuredErrorMap(e structuredError) map[string]any {
	data, err := json.Marshal(e)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

// mcpErrorData builds a JSON-RPC error.data payload from a raw error: it
// classifies err into a structuredError and attaches an optional sibling `meta`
// transport block. Protocol-level errors (parse, method not found, invalid
// params) pass a nil meta.
func mcpErrorData(err error, meta map[string]any) map[string]any {
	return mcpStructuredErrorData(mapStructuredError(err, newRequestID()), meta)
}

// mcpStructuredErrorData renders an already-classified structuredError plus an
// optional meta transport block as a JSON-RPC error.data payload.
func mcpStructuredErrorData(e structuredError, meta map[string]any) map[string]any {
	data := structuredErrorMap(e)
	if len(meta) > 0 {
		data["meta"] = meta
	}
	return data
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
			Error:   &mcpError{Code: -32700, Message: "parse error", Data: mcpErrorData(err, nil)},
		}, true
	}
	if req.ID == nil {
		return mcpResponse{}, false
	}
	switch req.Method {
	case "initialize":
		return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: mcpInitializeResult(req.Params)}, true
	case "tools/list":
		return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": mcpTools()}}, true
	case "tools/call":
		return handleMCPToolCall(ctx, req), true
	default:
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: "method not found", Data: mcpErrorData(operation.New(operation.ErrorUnsupported, "unsupported MCP method %s", req.Method), nil)},
		}, true
	}
}

var mcpSupportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

func mcpInitializeResult(params json.RawMessage) map[string]any {
	return map[string]any{
		"protocolVersion": mcpProtocolVersion(params),
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "microagent",
			"version": version,
		},
	}
}

func mcpProtocolVersion(params json.RawMessage) string {
	requested := ""
	if len(params) > 0 {
		var initParams mcpInitializeParams
		if err := json.Unmarshal(params, &initParams); err == nil {
			requested = initParams.ProtocolVersion
		}
	}
	for _, supported := range mcpSupportedProtocolVersions {
		if requested == supported {
			return requested
		}
	}
	return mcpSupportedProtocolVersions[0]
}

func mcpTools() []map[string]any {
	return []map[string]any{
		mcpTool("microagent.ping", "Test tool for validating the microagent MCP transport.", nil, nil),
		mcpTool("microagent.describe", "Return the machine-readable microagent MCP capability manifest.", nil, nil),
		mcpTool("workspace.create", "Create or dry-run a workspace, including snapshot forks.", []string{"name"}, map[string]any{
			"name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}, "exec": map[string]any{"type": "string"},
			"state_dir": map[string]any{"type": "string"}, "profile": map[string]any{"type": "string"}, "dry_run": map[string]any{"type": "boolean"},
			"from_snapshot": map[string]any{"type": "string", "description": "Fork from <workspace>:<tag> instead of creating from an image"},
			"model":         map[string]any{"type": "string"}, "model_token": map[string]any{"type": "string"},
			"model_runner":              map[string]any{"type": "string", "enum": []string{"llamacpp", "vllm", "custom"}},
			"model_gpu":                 map[string]any{"type": "string", "enum": []string{"off", "on", "auto"}},
			"model_runner_model":        map[string]any{"type": "string"},
			"model_runner_served_model": map[string]any{"type": "string"},
			"model_runner_command":      map[string]any{"type": "string"},
			"model_runner_name":         map[string]any{"type": "string"},
			"model_runner_health_path":  map[string]any{"type": "string"},
			"model_runner_args":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_runner_env":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_mediation":           map[string]any{"type": "string", "enum": []string{"off", "local-allow", "policy"}},
			"model_policy_url":          map[string]any{"type": "string"},
			"model_policy_file":         map[string]any{"type": "string"},
			"model_policy_timeout":      map[string]any{"type": "string"},
			"network":                   map[string]any{"type": "string", "enum": []string{"user", "nat", "isolated"}},
			"egress":                    map[string]any{"type": "string", "enum": []string{"broker", "mitm", "off"}, "description": "Egress mediation mode (default broker)"},
			"egress_allow":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Allowlisted egress hosts; .suffix matches subdomains. In broker/mitm this ADDS to the allow-broad default — set egress_lock_allowlist to restrict egress to only these"},
			"egress_lock_allowlist":     map[string]any{"type": "boolean", "description": "Restrict egress to the allowlisted hosts only (drop the allow-broad default) in broker/mitm mode"},
			"egress_passthrough":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Egress hosts allowed without TLS interception"},
			"egress_policy":             map[string]any{"type": "string", "description": "Path to an egress policy file (.yaml/.yml/.json)"},
			"egress_swap_config":        map[string]any{"type": "string", "description": "Credential-swap config path; mediator injects the real secret host-side so the guest never holds it"},
			"cred_swap":                 map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Inject a built-in provider's API key host-side: PROVIDER[=env:NAME|file:PATH|vault:PATH] (e.g. anthropic, openai). Guest never holds the key; reference only, never a literal"},
			"broker_upstream":           map[string]any{"type": "string", "description": "Egress broker upstream base URL; the broker injects the credential host-side and originates its own TLS, so the guest never holds the key"},
			"broker_secret":             map[string]any{"type": "string", "description": "Broker credential NAME=scheme:ref; held host-side only, the guest sends @secret:NAME references. Requires broker_upstream"},
			"broker_env":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Guest env vars pointed at the broker, each KEY[=VALUE] (empty VALUE = broker URL)"},
			"broker_proxy":              map[string]any{"type": "boolean", "description": "Also set HTTPS_PROXY/HTTP_PROXY in the guest to the broker (CONNECT tunneling)"},
			"broker_capture":            map[string]any{"type": "boolean", "description": "Opt in to raw capture of pre-swap broker requests to an owner-only file; default is the minimized decision stream"},
			"broker_ca":                 map[string]any{"type": "string", "description": "PEM bundle path the broker upstream TLS client trusts; empty = system roots"},
			"brokers":                   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Declare multiple broker endpoints instead of a single broker_*; each item is ;-separated key=value pairs: upstream=<url>;secret=NAME=<scheme>:<ref>;base-url-env=KEY[=VALUE];ca=<path>;proxy;capture. Cannot combine with broker_upstream/broker_secret/etc"},
			"secret":                    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Secrets delivered to tmpfs /run/secrets, each NAME=scheme:ref"},
			"secret_on_demand":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "On-demand secrets NAME=scheme:ref; fetched at runtime, never written to tmpfs"},
			"secrets_env_file":          map[string]any{"type": "string", "description": "Dotenv file whose keys are delivered as secrets"},
			"secrets_audit":             map[string]any{"type": "boolean", "description": "Append every secret access to the workspace audit log"},
		}),
		mcpTool("workspace.start", "Start a prepared workspace, optionally restoring from a snapshot.", []string{"name"}, map[string]any{
			"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"},
			"from_snapshot":             map[string]any{"type": "string", "description": "Restore the workspace in place from this snapshot tag"},
			"model_runner":              map[string]any{"type": "string", "enum": []string{"llamacpp", "vllm", "custom"}},
			"model_gpu":                 map[string]any{"type": "string", "enum": []string{"off", "on", "auto"}},
			"model_runner_model":        map[string]any{"type": "string"},
			"model_runner_served_model": map[string]any{"type": "string"},
			"model_runner_command":      map[string]any{"type": "string"},
			"model_runner_name":         map[string]any{"type": "string"},
			"model_runner_health_path":  map[string]any{"type": "string"},
			"model_runner_args":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_runner_env":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_mediation":           map[string]any{"type": "string", "enum": []string{"off", "local-allow", "policy"}},
			"model_policy_url":          map[string]any{"type": "string"},
			"model_policy_file":         map[string]any{"type": "string"},
			"model_policy_timeout":      map[string]any{"type": "string"},
		}),
		mcpTool("workspace.wait", "Block until a workspace reaches a terminal state (stopped, halted, failed, quarantined, or prepared) and report it, replacing workspace.inspect polling loops.", []string{"name"}, map[string]any{
			"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"},
			"timeout":  map[string]any{"type": "string", "description": "Give up after this long (Go duration, e.g. 30s, 5m); empty or 0 waits until the client cancels"},
			"interval": map[string]any{"type": "string", "description": "Delay between state checks (Go duration; default 1s)"},
		}),
		mcpTool("workspace.exec", "Run a structured command in a running workspace.", []string{"name"}, workspaceExecInputSchema()),
		mcpTool("workspace.dispatch", "Run one task in a fresh, isolated, single-use workspace under egress guardrails, then tear it down. Returns the guest result plus a mediator-written summary of what the workspace reached on the network, so a caller can judge whether the task stayed on-intent. Ideal for delegating untrusted or parallel work to its own machine.", []string{"image"}, map[string]any{
			"image":                 map[string]any{"type": "string", "description": "OCI image to boot"},
			"exec":                  map[string]any{"type": "string", "description": "Command to run in the workspace"},
			"state_dir":             map[string]any{"type": "string"},
			"timeout":               map[string]any{"type": "string", "description": "Max wall-clock for the task (e.g. 5m)"},
			"network":               map[string]any{"type": "string", "enum": []string{"user", "nat", "isolated"}},
			"egress":                map[string]any{"type": "string", "enum": []string{"broker", "mitm", "off"}, "description": "Egress mediation mode (default broker)"},
			"egress_allow":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Allowlisted egress hosts; .suffix matches subdomains. In broker/mitm this ADDS to the allow-broad default — set egress_lock_allowlist to restrict egress to only these"},
			"egress_lock_allowlist": map[string]any{"type": "boolean", "description": "Restrict egress to the allowlisted hosts only (drop the allow-broad default) in broker/mitm mode"},
			"egress_passthrough":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Egress hosts allowed without TLS interception"},
			"egress_policy":         map[string]any{"type": "string", "description": "Path to an egress policy file (.yaml/.yml/.json)"},
			"egress_swap_config":    map[string]any{"type": "string", "description": "Credential-swap config path; mediator injects the real secret host-side so the guest never holds it"},
			"cred_swap":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Inject a built-in provider's API key host-side: PROVIDER[=env:NAME|file:PATH|vault:PATH] (e.g. anthropic, openai). Guest never holds the key; reference only, never a literal"},
			"broker_upstream":       map[string]any{"type": "string", "description": "Egress broker upstream base URL; the broker injects the credential host-side and originates its own TLS, so the guest never holds the key"},
			"broker_secret":         map[string]any{"type": "string", "description": "Broker credential NAME=scheme:ref; held host-side only, the guest sends @secret:NAME references. Requires broker_upstream"},
			"broker_env":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Guest env vars pointed at the broker, each KEY[=VALUE] (empty VALUE = broker URL)"},
			"broker_proxy":          map[string]any{"type": "boolean", "description": "Also set HTTPS_PROXY/HTTP_PROXY in the guest to the broker (CONNECT tunneling)"},
			"broker_capture":        map[string]any{"type": "boolean", "description": "Opt in to raw capture of pre-swap broker requests to an owner-only file; default is the minimized decision stream"},
			"broker_ca":             map[string]any{"type": "string", "description": "PEM bundle path the broker upstream TLS client trusts; empty = system roots"},
			"brokers":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Declare multiple broker endpoints instead of a single broker_*; each item is ;-separated key=value pairs: upstream=<url>;secret=NAME=<scheme>:<ref>;base-url-env=KEY[=VALUE];ca=<path>;proxy;capture. Cannot combine with broker_upstream/broker_secret/etc"},
			"secret":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Secrets delivered to tmpfs /run/secrets, each NAME=scheme:ref"},
			"secret_on_demand":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "On-demand secrets NAME=scheme:ref; never written to tmpfs"},
			"secrets_env_file":      map[string]any{"type": "string", "description": "Dotenv file whose keys are delivered as secrets"},
			"secrets_audit":         map[string]any{"type": "boolean", "description": "Append every secret access to the workspace audit log"},
		}),
		mcpTool("workspace.halt", "Halt a workspace and preserve disk state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
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
		mcpTool("workspace.egress", "Read the egress mediator's audit decisions (allow/deny/MITM/DNS/UDP) for a workspace. Egress mediation is on by default; an absent audit log returns an empty list, not an error.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.clone", "Clone a stopped workspace to a new workspace name.", []string{"source", "target"}, map[string]any{"source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.apply", "Apply supported changes from a workspace spec file.", []string{"file"}, map[string]any{"file": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("workspace.commit", "Commit a stopped workspace rootfs into a local OCI image, optionally pushing it.", []string{"name", "image"}, map[string]any{"name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "push": map[string]any{"type": "boolean"}}),
		mcpTool("workspace.estimate_cost", "Estimate workspace resource consumption before creating or starting it.", nil, map[string]any{"profile": map[string]any{"type": "string"}, "memory_mib": map[string]any{"type": "integer"}, "cpus": map[string]any{"type": "integer"}, "size_mib": map[string]any{"type": "integer"}, "price_per_hour": map[string]any{"type": "number"}}),
		mcpTool("artifacts.list", "List declared workspace artifacts.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("snapshot.create", "Create a backend snapshot for a workspace when supported. Set forensic to capture for investigation: guest secrets are retained and the capture is not restorable.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "tag": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "forensic": map[string]any{"type": "boolean"}}),
		mcpTool("snapshot.list", "List snapshots for a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("snapshot.delete", "Delete a workspace snapshot.", []string{"name", "tag"}, map[string]any{"name": map[string]any{"type": "string"}, "tag": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("network.inspect", "Inspect a workspace's network.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
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
		mcpTool("models.pull", "Pull a GGUF model from HuggingFace into the local store.", []string{"model"}, map[string]any{"model": map[string]any{"type": "string"}, "token": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.list", "List locally stored models.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.remove", "Remove a model from the local store.", []string{"model"}, map[string]any{"model": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.prune", "Prune local model records whose blobs are missing.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.serve", "Start or reuse a local host model server for a stored or pulled model.", []string{"model"},
			map[string]any{
				"model":               map[string]any{"type": "string"},
				"dedicated":           map[string]any{"type": "boolean"},
				"runner":              map[string]any{"type": "string", "enum": []string{"llamacpp", "vllm", "custom"}},
				"runner_gpu":          map[string]any{"type": "string", "enum": []string{"off", "on", "auto"}},
				"runner_model":        map[string]any{"type": "string"},
				"runner_served_model": map[string]any{"type": "string"},
				"runner_command":      map[string]any{"type": "string"},
				"runner_name":         map[string]any{"type": "string"},
				"runner_health_path":  map[string]any{"type": "string"},
				"runner_args":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"runner_env":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"token":               map[string]any{"type": "string"},
				"state_dir":           map[string]any{"type": "string"},
			}),
		mcpTool("models.stop", "Stop local host model server instances for a model.", []string{"model"},
			map[string]any{"model": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.runners", "List running local model servers.", nil,
			map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.policy.validate", "Validate a structured model mediation policy file.", []string{"policy_file"},
			map[string]any{"policy_file": map[string]any{"type": "string"}}),
		mcpTool("models.policy.evaluate", "Dry-run a structured model mediation policy file against request metadata.", []string{"policy_file"},
			map[string]any{
				"policy_file":   map[string]any{"type": "string"},
				"method":        map[string]any{"type": "string"},
				"request_path":  map[string]any{"type": "string"},
				"workspace_id":  map[string]any{"type": "string"},
				"capability":    map[string]any{"type": "string"},
				"worker_id":     map[string]any{"type": "string"},
				"model":         map[string]any{"type": "string"},
				"request_bytes": map[string]any{"type": "integer"},
				"text_bytes":    map[string]any{"type": "integer"},
				"messages":      map[string]any{"type": "integer"},
				"max_tokens":    map[string]any{"type": "integer"},
				"stream":        map[string]any{"type": "boolean"},
				"tools":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"expect":        map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
			}),
		mcpTool("profiles.list", "List resource profiles.", nil, nil),
		mcpTool("host.inspect", "Report host capabilities for the selected backend.", nil, map[string]any{"backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("doctor.check", "Run host diagnostics for the selected backend.", nil, map[string]any{"backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
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
	inputSchema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": true,
	}
	if len(required) > 0 {
		inputSchema["required"] = required
	}
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": inputSchema,
	}
}

func handleMCPToolCall(ctx context.Context, req mcpRequest) mcpResponse {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(req.Params) != 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32602, Message: "invalid params", Data: mcpErrorData(err, nil)}}
		}
	}
	if params.Name != "microagent.ping" {
		if params.Name == "microagent.describe" {
			start := time.Now()
			envelope := mcpSuccessEnvelope(microagentCapabilityManifest(), mcpMeta(params.Arguments, start))
			return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: mcpToolResult(envelope)}
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

// mcpToolCallErrorData maps a failed tool envelope ({ok:false, error, meta})
// onto a JSON-RPC error.data payload: the structuredError fields flattened at
// the top with the transport `meta` block (timing_ms, principal_context, retry
// metadata) attached as a sibling.
func mcpToolCallErrorData(err error, envelope map[string]any) any {
	var (
		structured structuredError
		haveError  bool
		meta       map[string]any
	)
	if envelope != nil {
		structured, haveError = envelope["error"].(structuredError)
		if m, ok := envelope["meta"].(map[string]any); ok {
			meta = m
		}
	}
	if !haveError {
		structured = mapStructuredError(err, newRequestID())
	}
	return mcpStructuredErrorData(structured, meta)
}

func microagentCapabilityManifest() map[string]any {
	operations := make([]map[string]any, 0, len(mcpTools()))
	for _, tool := range mcpTools() {
		name, _ := tool["name"].(string)
		if name == "microagent.ping" {
			continue
		}
		operation, _ := vmkit.OperationForMCPTool(name)
		operations = append(operations, map[string]any{
			"operation_id":          operation.ID,
			"feature_id":            operation.FeatureID,
			"required_capabilities": operation.RequiredCapabilities,
			"name":                  name,
			"description":           tool["description"],
			"input_schema":          tool["inputSchema"],
			"output_schema":         mcpToolOutputSchema(name),
			"side_effects":          mcpToolSideEffects(name),
			"idempotency":           mcpToolIdempotency(name),
			"principal_scope":       mcpToolPrincipalScope(name),
			"cost_class":            mcpToolCostClass(name),
			"structured_errors":     []string{string(errorKindTransient), string(errorKindPermanent), string(errorKindConflict), string(errorKindNotFound), string(errorKindResourceExhausted), string(errorKindUnsupported), string(errorKindPolicyDenied)},
			"correlation_id_key":    "error.data.correlation_id",
		})
	}
	return map[string]any{
		"schema_version":    "2026-07-22",
		"service":           "microagent",
		"version":           version,
		"transport":         "mcp_stdio",
		"response_envelope": mcpResponseEnvelopeSchema(),
		"agent_experience": map[string]any{
			"defaults": []string{
				"parse every tool payload as the unified envelope {ok, result, meta}: read the answer from result, transport facts (timing_ms, principal_context, idempotency_replay, retry metadata) from meta",
				"read failures from the JSON-RPC error.data object: the structuredError fields (kind, message, remediation, retryable, correlation_id) with the same meta block attached as a sibling",
				"use compact summary outputs for repeated state checks",
				"request format=full only when a complete log, event, or inspect payload is needed",
				"use tail_lines and after_index for bounded stream polling instead of long-running follow calls",
				"use preview=true before destructive delete operations when user confirmation is still pending",
				"use the preview confirmation_token for host-mutating install/setup/build operations",
				"use idempotency_key on retryable mutation calls",
			},
			"evidence": "agent-experience harness runs showed lower token waste when agents used compact structured MCP state instead of scraping prose or repeatedly requesting full state",
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

// mcpToolOutputSchema describes the successful tool payload: the unified
// envelope {ok:true, result, meta}. Failures are not part of the tool payload;
// they arrive as a JSON-RPC error whose data follows response_envelope.error
// (see mcpResponseEnvelopeSchema).
func mcpToolOutputSchema(name string) map[string]any {
	if name == "workspace.exec" {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ok":     map[string]any{"type": "boolean"},
				"result": execResultSchema(),
				"meta":   mcpMetaSchema(true),
			},
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
			"result": map[string]any{"type": "object", "properties": map[string]any{
				"readiness": map[string]any{"type": "object", "properties": map[string]any{
					"guestReady":     readinessSignalSchema(),
					"shellReady":     readinessSignalSchema(),
					"execReady":      readinessSignalSchema(),
					"resultReady":    readinessSignalSchema(),
					"mediationReady": readinessSignalSchema(),
				}},
			}},
			"meta": mcpMetaSchema(false),
		},
	}
}

// mcpMetaSchema describes the transport `meta` block attached to every MCP
// response (success payload and error.data alike). withRetry adds the exec
// retry-metadata fields.
func mcpMetaSchema(withRetry bool) map[string]any {
	props := map[string]any{
		"timing_ms":          map[string]any{"type": "integer"},
		"principal_context":  map[string]any{"type": "object"},
		"idempotency_replay": map[string]any{"type": "boolean"},
	}
	if withRetry {
		props["retry_count"] = map[string]any{"type": "integer"}
		props["retry_wall_clock_ms"] = map[string]any{"type": "integer"}
		props["retry_exhausted"] = map[string]any{"type": "boolean"}
	}
	return map[string]any{"type": "object", "properties": props}
}

// mcpStructuredErrorSchema describes the structuredError object carried in
// JSON-RPC error.data.
func mcpStructuredErrorSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":           map[string]any{"type": "string", "enum": []string{string(errorKindTransient), string(errorKindPermanent), string(errorKindConflict), string(errorKindNotFound), string(errorKindResourceExhausted), string(errorKindUnsupported), string(errorKindPolicyDenied)}},
			"message":        map[string]any{"type": "string"},
			"remediation":    map[string]any{"type": "string"},
			"retryable":      map[string]any{"type": "boolean"},
			"retry_after_ms": map[string]any{"type": "integer"},
			"partial_output": map[string]any{"type": "string"},
			"correlation_id": map[string]any{"type": "string"},
		},
	}
}

// mcpResponseEnvelopeSchema documents the two response shapes for a tool call:
// a success payload {ok:true, result, meta} returned inside the MCP tool
// content, and a failure surfaced as a JSON-RPC error whose data is the
// structuredError with a sibling meta block.
func mcpResponseEnvelopeSchema() map[string]any {
	errData := mcpStructuredErrorSchema()
	errProps, _ := errData["properties"].(map[string]any)
	if errProps != nil {
		errProps["meta"] = mcpMetaSchema(true)
	}
	return map[string]any{
		"discriminator": "ok",
		"success": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ok":     map[string]any{"const": true},
				"result": map[string]any{"description": "operation payload; see each operation's output_schema"},
				"meta":   mcpMetaSchema(true),
			},
		},
		"error": map[string]any{
			"description": "delivered as a JSON-RPC error; error.data carries these fields",
			"data":        errData,
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
	case "kernel.install", "rootfs.build":
		return []string{"host_state"}
	case "workspace.dispatch", "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "cp", "artifacts.get":
		return []string{"host_state", "workspace_state"}
	case "models.pull", "models.remove", "models.prune", "models.serve", "models.stop":
		return []string{"host_state"}
	default:
		return nil
	}
}

func mcpToolIdempotency(name string) string {
	switch name {
	case "workspace.create", "workspace.start", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "models.pull", "snapshot.delete":
		return "accepts idempotency_key; identical retries by the same principal replay the first completed response for 15 minutes"
	case "workspace.dispatch", "workspace.exec", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "models.remove", "models.prune", "models.serve", "models.stop", "kernel.install", "rootfs.build", "cp", "artifacts.get":
		return "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes"
	case "workspace.list", "workspace.inspect", "workspace.wait", "workspace.result", "workspace.stats", "workspace.logs", "workspace.events", "workspace.egress", "workspace.estimate_cost", "artifacts.list", "snapshot.list", "network.inspect", "volume.list", "volume.inspect", "images.list", "models.list", "models.runners", "models.policy.validate", "models.policy.evaluate", "profiles.list", "host.inspect", "doctor.check", "contract.get", "kernel.verify", "microagent.describe":
		return "read_only"
	default:
		return "not_idempotent"
	}
}

func mcpToolPrincipalScope(name string) []string {
	switch name {
	case "workspace.dispatch", "workspace.create", "workspace.start", "workspace.wait", "workspace.exec", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.list", "workspace.inspect", "workspace.result", "workspace.stats", "workspace.logs", "workspace.events", "workspace.egress", "workspace.clone", "workspace.apply", "workspace.commit":
		return []string{"workspace.lifecycle"}
	case "snapshot.create", "snapshot.list", "snapshot.delete":
		return []string{"workspace.snapshot"}
	case "network.inspect":
		return []string{"network.read"}
	case "volume.create", "volume.list", "volume.inspect", "volume.delete":
		return []string{"volume.read", "volume.write"}
	case "images.pull", "images.list", "images.push", "images.tag", "images.delete", "images.prune":
		return []string{"images.read", "images.write"}
	case "artifacts.list", "cp", "artifacts.get":
		return []string{"workspace.files"}
	case "models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners", "models.policy.validate", "models.policy.evaluate":
		return []string{"models.read", "models.write"}
	case "kernel.install", "rootfs.build":
		return []string{"host.write"}
	case "host.inspect", "doctor.check", "contract.get", "kernel.verify", "profiles.list":
		return []string{"host.read"}
	default:
		return []string{"microagent.read"}
	}
}

func mcpToolCostClass(name string) string {
	switch name {
	case "workspace.dispatch", "workspace.create", "workspace.start", "workspace.exec", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "models.pull", "models.serve", "kernel.install", "rootfs.build":
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
	return mcpSuccessEnvelope(estimate, mcpZeroMeta(args))
}

// mcpMeta builds the transport `meta` block carried by every MCP tool envelope:
// wall-clock timing plus the caller's principal context.
func mcpMeta(args map[string]any, start time.Time) map[string]any {
	return map[string]any{
		"timing_ms":         time.Since(start).Milliseconds(),
		"principal_context": principalContextArg(args),
	}
}

// mcpZeroMeta is mcpMeta for responses produced without doing timed work
// (previews, cost estimates): timing_ms is reported as 0.
func mcpZeroMeta(args map[string]any) map[string]any {
	return map[string]any{
		"timing_ms":         int64(0),
		"principal_context": principalContextArg(args),
	}
}

// mcpSuccessEnvelope is the unified success envelope: {ok:true, result, meta}.
func mcpSuccessEnvelope(result any, meta map[string]any) map[string]any {
	return map[string]any{"ok": true, "result": result, "meta": meta}
}

// mcpErrorEnvelope is the unified failure envelope: {ok:false, error, meta}.
// The transport meta rides alongside the structuredError so both surface
// through the JSON-RPC error.data path (see mcpToolCallErrorData).
func mcpErrorEnvelope(e structuredError, meta map[string]any) map[string]any {
	return map[string]any{"ok": false, "error": e, "meta": meta}
}

// mcpMarkReplay returns a copy of envelope whose meta block records the
// idempotency replay flag, cloning the meta map so the cached original is never
// mutated (the cache stores replay-flag-free envelopes; the flag is stamped per
// response).
func mcpMarkReplay(envelope map[string]any, replay bool) map[string]any {
	out := cloneMCPMap(envelope)
	meta := map[string]any{}
	if existing, ok := out["meta"].(map[string]any); ok {
		meta = cloneMCPMap(existing)
	}
	meta["idempotency_replay"] = replay
	out["meta"] = meta
	return out
}

func mcpMarkReplayForArgs(envelope map[string]any, replay bool, args map[string]any) map[string]any {
	out := mcpMarkReplay(envelope, replay)
	meta := cloneMCPMap(out["meta"].(map[string]any))
	meta["principal_context"] = principalContextArg(args)
	out["meta"] = meta
	return out
}

func runMCPTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	start := time.Now()
	if preview := previewDestructiveMCPTool(name, args); preview != nil {
		return preview, nil
	}
	if preview, err := requireConfirmedMCPHostMutation(name, args); preview != nil || err != nil {
		return preview, err
	}
	if mcpIdempotencyCacheKey(name, args) != "" {
		envelope, err, replay := mcpIdempotencyCache.Do(ctx, name, args, func() (map[string]any, error) {
			return runMCPToolOnce(ctx, name, args, start)
		})
		if envelope == nil {
			envelope = mcpErrorEnvelope(mcpStructuredErrorFor(err), mcpMeta(args, start))
		}
		return mcpMarkReplayForArgs(envelope, replay, args), err
	}
	return runMCPToolOnce(ctx, name, args, start)
}

func runMCPToolOnce(ctx context.Context, name string, args map[string]any, start time.Time) (map[string]any, error) {
	if name == "workspace.exec" {
		return runMCPWorkspaceExec(ctx, args, start)
	}
	if result, handled, directErr := runDirectMCPTool(ctx, name, args); handled {
		meta := mcpMeta(args, start)
		var envelope map[string]any
		if directErr != nil {
			envelope = mcpErrorEnvelope(mcpStructuredErrorFor(directErr), meta)
		} else {
			envelope = mcpSuccessEnvelope(result, meta)
		}
		return envelope, directErr
	}
	cliArgs, err := mcpCLIArgs(name, args)
	if err != nil {
		return nil, err
	}
	result, cliErr := runCLIForMCP(ctx, cliArgs)
	// Some tools signal an unclean/nonzero *task outcome* through a silent nonzero
	// CLI exit while still writing the full structured result to stdout: wait
	// (final state failed/quarantined) and dispatch (the guest task exited
	// nonzero, but the result carries the guest output plus the mediator egress
	// summary the caller needs to judge on-intent). Report those as data
	// (ok=false in the payload), not as a JSON-RPC tool error that would discard
	// the result.
	if mcpToolReportsExitAsResult(name) {
		var exitErr cliExitError
		if errors.As(cliErr, &exitErr) && exitErr.Silent {
			cliErr = nil
		}
	}
	if cliErr == nil && name == "workspace.create" {
		result = summarizeWorkspaceLifecycle(result, "created")
	}
	if cliErr == nil && name == "workspace.inspect" && !strings.EqualFold(stringArg(args, "format"), "full") {
		inspectStateDir := stringArg(args, "state_dir")
		if inspectStateDir == "" {
			inspectStateDir = defaultStateDir()
		}
		result = summarizeWorkspaceInspect(result, inspectStateDir, stringArg(args, "name"))
	}
	if cliErr == nil && name == "workspace.logs" && !strings.EqualFold(stringArg(args, "format"), "full") {
		result = summarizeWorkspaceLogs(result, intArg(args, "tail_lines"))
	}
	if cliErr == nil && name == "workspace.events" && !strings.EqualFold(stringArg(args, "format"), "full") {
		result = summarizeWorkspaceEvents(result, intArg(args, "limit"), intArg(args, "after_index"))
	}
	meta := mcpMeta(args, start)
	var envelope map[string]any
	if cliErr != nil {
		envelope = mcpErrorEnvelope(mcpStructuredErrorFor(cliErr), meta)
	} else {
		envelope = mcpSuccessEnvelope(result, meta)
	}
	return envelope, cliErr
}

// runDirectMCPTool contains agent-facing operations whose inputs map directly
// onto typed library calls. These handlers deliberately bypass CLI parsing,
// rendering, output modes, temporary files, and exit-code policy. The
// remaining runCLIForMCP path is a compatibility bridge while host-management
// mutations are moved behind equivalent typed application operations.
func runDirectMCPTool(ctx context.Context, name string, args map[string]any) (any, bool, error) {
	stateDir := stringArg(args, "state_dir")
	if stateDir == "" {
		stateDir = defaultStateDir()
	}
	workspaceName := stringArg(args, "name")
	opts := workspace.DefaultOptions()
	opts.StateDir = stateDir
	opts.Name = workspaceName

	switch name {
	case "workspace.halt", "workspace.kill":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		command := strings.TrimPrefix(name, "workspace.")
		releaseModel := pendingModelRelease(stateDir, workspaceName, opts.Backend)
		result, err := mcpWorkspaceControl(ctx, opts, command)
		if err == nil && result.OK {
			releaseModel()
		}
		return jsonCompatible(result), true, err
	case "workspace.quarantine":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceQuarantine(ctx, opts, workspace.QuarantineOptions{})
		return jsonCompatible(result), true, err
	case "workspace.pause", "workspace.resume":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		command := strings.TrimPrefix(name, "workspace.")
		result, err := mcpWorkspaceControl(ctx, opts, command)
		return jsonCompatible(result), true, err
	case "workspace.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		releaseModel := pendingModelRelease(stateDir, workspaceName, opts.Backend)
		result, err := mcpWorkspaceDelete(ctx, opts, workspace.DeleteOptions{Force: boolArg(args, "force")})
		if err == nil && result.OK {
			releaseModel()
		}
		return jsonCompatible(result), true, err
	case "workspace.list":
		entries, err := workspace.List(stateDir)
		if err == nil {
			reconcileLiveWorkspaces(ctx, stateDir, entries)
			entries, err = workspace.List(stateDir)
		}
		return map[string]any{"workspaces": jsonCompatible(entries)}, true, err
	case "workspace.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		resp, err := workspace.Status(opts)
		if err == nil && resp.Event != nil && isLiveRecordedState(resp.Event.State) {
			if _, inspectErr := workspace.Inspect(ctx, opts); inspectErr == nil {
				resp, err = workspace.Status(opts)
			}
		}
		result := jsonCompatible(resp)
		if err == nil && !strings.EqualFold(stringArg(args, "format"), "full") {
			result = summarizeWorkspaceInspect(result, stateDir, workspaceName)
		}
		return result, true, err
	case "workspace.wait":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		timeout, err := optionalMCPDuration(args, "timeout")
		if err != nil {
			return nil, true, err
		}
		interval, err := optionalMCPDuration(args, "interval")
		if err != nil {
			return nil, true, err
		}
		result, err := workspace.Wait(ctx, opts, workspace.WaitOptions{Timeout: timeout, Interval: interval})
		return jsonCompatible(result), true, err
	case "workspace.result":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.ResultStatus(opts)
		return jsonCompatible(result), true, err
	case "workspace.stats":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.SampleStats(stateDir, workspaceName)
		return jsonCompatible(result), true, err
	case "workspace.logs":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		data, err := workspace.ReadLogs(stateDir, workspaceName)
		result := map[string]any{"workspace": workspaceName, "logs": string(data)}
		if err == nil && !strings.EqualFold(stringArg(args, "format"), "full") {
			return summarizeWorkspaceLogs(result, intArg(args, "tail_lines")), true, nil
		}
		return result, true, err
	case "workspace.events":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		events, err := workspace.ReadEvents(stateDir, workspaceName)
		result := map[string]any{"workspace": workspaceName, "events": jsonCompatible(events)}
		if err == nil && !strings.EqualFold(stringArg(args, "format"), "full") {
			return summarizeWorkspaceEvents(result, intArg(args, "limit"), intArg(args, "after_index")), true, nil
		}
		return result, true, err
	case "workspace.egress":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		mediator, err := workspace.ReadEgressAudit(stateDir, workspaceName)
		if err != nil {
			return nil, true, err
		}
		brokered, err := workspace.ReadBrokerAccess(stateDir, workspaceName)
		return map[string]any{
			"workspace": workspaceName,
			"egress":    jsonCompatible(workspace.MergeEgressEvents(mediator, brokered)),
		}, true, err
	case "network.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.Network(stateDir, workspaceName)
		return jsonCompatible(result), true, err
	case "artifacts.list":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.ArtifactsFor(stateDir, workspaceName)
		return jsonCompatible(artifactsResult{Workspace: workspaceName, Artifacts: result}), true, err
	case "snapshot.list":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := vmkit.ListSnapshots(stateDir, workspaceName)
		return map[string]any{"workspace": workspaceName, "snapshots": jsonCompatible(result)}, true, err
	case "snapshot.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		tag := stringArg(args, "tag")
		create := mcpSnapshotCreate
		if boolArg(args, "forensic") {
			create = mcpSnapshotForensic
		}
		result, err := create(ctx, opts, tag)
		return jsonCompatible(result), true, err
	case "snapshot.delete":
		if err := requireToolArgs(args, name, "name", "tag"); err != nil {
			return nil, true, err
		}
		tag := stringArg(args, "tag")
		err := mcpSnapshotDelete(opts, tag)
		return map[string]any{"workspace": workspaceName, "removed": tag}, true, err
	case "volume.list":
		result, err := volume.List(stateDir)
		return map[string]any{"volumes": jsonCompatible(result)}, true, err
	case "volume.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := volume.Get(stateDir, workspaceName)
		return jsonCompatible(result), true, err
	case "images.list":
		result, err := imagecache.List(stateDir)
		return map[string]any{"images": jsonCompatible(result)}, true, err
	case "models.list":
		result, err := model.List(stateDir)
		return map[string]any{"models": jsonCompatible(result)}, true, err
	case "models.runners":
		result, err := modelrunner.List(stateDir)
		return map[string]any{"runners": jsonCompatible(result)}, true, err
	case "profiles.list":
		return map[string]any{"profiles": jsonCompatible(resourceProfiles)}, true, nil
	case "contract.get":
		return jsonCompatible(vmkit.NewRuntimeContract()), true, nil
	default:
		return nil, false, nil
	}
}

func optionalMCPDuration(args map[string]any, name string) (time.Duration, error) {
	raw := stringArg(args, name)
	if raw == "" {
		return 0, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, operation.New(operation.ErrorValidation, "%s must be a positive Go duration such as 250ms or 5m", name)
	}
	return value, nil
}

// jsonCompatible gives MCP summaries their natural map/slice representation
// while keeping the operation boundary typed.
func jsonCompatible(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return value
	}
	return result
}

// mcpStructuredErrorFor maps an operation failure into the agent-facing MCP
// error contract. MCP owns this classification; it does not inherit a CLI
// rendering profile.
func mcpStructuredErrorFor(err error) structuredError {
	return mapStructuredError(err, newRequestID())
}

func requireConfirmedMCPHostMutation(name string, args map[string]any) (map[string]any, error) {
	if !mcpHostMutationTool(name) {
		return nil, nil
	}
	token := mcpConfirmationToken(name, args)
	if boolArg(args, "preview") {
		return mcpSuccessEnvelope(map[string]any{
			"preview":            true,
			"tool":               name,
			"actions":            mcpHostMutationActions(name, args),
			"confirmation_token": token,
			"confirm_with":       "call the same tool with confirm_token set to confirmation_token and preview omitted or false",
		}, mcpZeroMeta(args)), nil
	}
	if stringArg(args, "confirm_token") != token {
		return nil, operation.New(operation.ErrorPolicyDenied, "%s requires preview confirmation; call with preview=true and retry with the returned confirm_token", name)
	}
	return nil, nil
}

func mcpHostMutationTool(name string) bool {
	switch name {
	case "kernel.install", "rootfs.build":
		return true
	default:
		return false
	}
}

func mcpHostMutationActions(name string, args map[string]any) []string {
	switch name {
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
	result, retryMeta, err := mcpWorkspaceExec(ctx, workspace.Options{Name: stringArg(args, "name"), StateDir: stateDir}, req)
	meta := mcpMeta(args, start)
	meta["retry_count"] = retryMeta.Count
	meta["retry_wall_clock_ms"] = retryMeta.WallClockMilliseconds()
	if retryMeta.Exhausted {
		meta["retry_exhausted"] = true
	}
	if err != nil {
		return mcpErrorEnvelope(mapStructuredError(err, newRequestID()), meta), err
	}
	return mcpSuccessEnvelope(result, meta), nil
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
	return nil, operation.New(operation.ErrorValidation, "workspace.exec requires argv or command")
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
		return mcpSuccessEnvelope(map[string]any{
			"preview":   true,
			"tool":      name,
			"workspace": stringArg(args, "name"),
			"actions":   []string{action, "remove workspace disk and state"},
		}, mcpZeroMeta(args))
	case "volume.delete":
		return mcpSuccessEnvelope(map[string]any{
			"preview": true,
			"tool":    name,
			"name":    stringArg(args, "name"),
			"actions": []string{"delete " + strings.TrimSuffix(name, ".delete")},
			"force":   boolArg(args, "force"),
		}, mcpZeroMeta(args))
	case "snapshot.delete":
		return mcpSuccessEnvelope(map[string]any{
			"preview": true,
			"tool":    name,
			"name":    stringArg(args, "name"),
			"tag":     stringArg(args, "tag"),
			"actions": []string{"delete snapshot"},
		}, mcpZeroMeta(args))
	case "images.delete", "images.prune":
		actions := []string{"delete stale image records"}
		if name == "images.delete" {
			actions = []string{"delete image record"}
		}
		if boolArg(args, "delete_files") {
			actions = append(actions, "delete cached rootfs files")
		}
		return mcpSuccessEnvelope(map[string]any{
			"preview":      true,
			"tool":         name,
			"image":        stringArg(args, "image"),
			"delete_files": boolArg(args, "delete_files"),
			"actions":      actions,
		}, mcpZeroMeta(args))
	default:
		return nil
	}
}

func summarizeWorkspaceInspect(result any, stateDir, name string) any {
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
			if name == "" {
				if id, ok := identity["runtimeID"].(string); ok {
					name = id
				}
			}
		}
	}
	if eg := egressSummary(stateDir, name); eg != nil {
		summary["egress_summary"] = eg
	}
	summary["next_decision_points"] = workspaceNextDecisionPoints(fmt.Sprint(summary["state"]))
	return summary
}

// egressSummary reads the egress mediator's audit log and folds it into a
// compact overview: a total decision count, a count for each event type, and a
// per-host allow-vs-deny tally. It returns nil when the audit log is absent or
// empty (mediation off / no decision yet) so the inspect summary omits the
// egress_summary key cleanly rather than carrying an empty object. The counts
// stay generic over the mediator's open-ended event vocabulary — every event
// type the log contains is tallied under by_event, allow/deny are recognized by
// suffix so DNS/UDP allow/deny variants fold into the per-host verdict view.
func egressSummary(stateDir, name string) map[string]any {
	if name == "" {
		return nil
	}
	events, err := workspace.ReadEgressAudit(stateDir, name)
	if err != nil || len(events) == 0 {
		return nil
	}
	byEvent := map[string]int{}
	allowByHost := map[string]int{}
	denyByHost := map[string]int{}
	for _, ev := range events {
		byEvent[ev.Event]++
		host := ev.Host
		if host == "" {
			host = ev.Dst
		}
		if host == "" {
			continue
		}
		switch {
		case strings.HasSuffix(ev.Event, "_allow"):
			allowByHost[host]++
		case strings.HasSuffix(ev.Event, "_deny"):
			denyByHost[host]++
		}
	}
	summary := map[string]any{
		"decision_count": len(events),
		"by_event":       byEvent,
	}
	if len(allowByHost) > 0 {
		summary["allow_by_host"] = allowByHost
	}
	if len(denyByHost) > 0 {
		summary["deny_by_host"] = denyByHost
	}
	return summary
}

func summarizeWorkspaceLifecycle(result any, outcome string) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	response, _ := resp["response"].(map[string]any)
	summary := map[string]any{
		"format":    "summary",
		"outcome":   outcome,
		"ok":        response["ok"],
		"backend":   response["backend"],
		"workspace": resp["workspace"],
		"state":     resp["final_state"],
		"error":     response["error"],
		"error_cnt": 0,
	}
	if text, ok := response["error"].(string); ok && strings.TrimSpace(text) != "" {
		summary["error_cnt"] = 1
	}
	if event, ok := response["event"].(map[string]any); ok {
		summary["state"] = event["state"]
		if identity, ok := event["identity"].(map[string]any); ok && summary["workspace"] == nil {
			summary["workspace"] = identity["runtimeID"]
		}
		if detail, ok := event["detail"].(string); ok && strings.TrimSpace(detail) != "" {
			summary["detail"] = detail
		}
	}
	if summary["ok"] == true && outcome == "created" && fmt.Sprint(summary["state"]) == "stopped" {
		summary["ready"] = true
		summary["state_meaning"] = "created and ready to start"
	}
	if rootfs, ok := resp["rootfs_path"].(string); ok && strings.TrimSpace(rootfs) != "" {
		summary["rootfs_path"] = rootfs
	}
	summary["next_decision_points"] = workspaceNextDecisionPoints(fmt.Sprint(summary["state"]))
	return summary
}

func workspaceNextDecisionPoints(state string) []string {
	switch state {
	case "running", "starting":
		return []string{"workspace.exec", "workspace.halt", "workspace.delete"}
	case "prepared", "halted", "stopped":
		return []string{"workspace.start", "workspace.delete"}
	case "failed", "quarantined":
		return []string{"workspace.inspect", "workspace.delete"}
	default:
		return []string{"workspace.inspect"}
	}
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
	return key
}

func mcpMutationTool(name string) bool {
	switch name {
	case "workspace.dispatch", "workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "models.pull", "models.remove", "models.prune", "models.serve", "models.stop", "kernel.install", "rootfs.build", "cp", "artifacts.get":
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
		cli := []string{"--json", "create", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-image", stringArg(args, "image"))
		cli = appendOptionalFlag(cli, "-from-snapshot", stringArg(args, "from_snapshot"))
		cli = appendOptionalFlag(cli, "-exec", stringArg(args, "exec"))
		cli = appendOptionalFlag(cli, "-profile", stringArg(args, "profile"))
		cli = appendOptionalFlag(cli, "-network", stringArg(args, "network"))
		cli = appendOptionalFlag(cli, "-model", stringArg(args, "model"))
		cli = appendOptionalFlag(cli, "-model-token", stringArg(args, "model_token"))
		var err error
		cli, err = appendMCPWorkspaceModelFlags(cli, args)
		if err != nil {
			return nil, err
		}
		cli, err = appendMCPWorkspaceEgressSecretFlags(cli, args)
		if err != nil {
			return nil, err
		}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "dry_run") {
			cli = append(cli, "-dry-run")
		}
		return cli, nil
	case "workspace.start":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "start", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-from-snapshot", stringArg(args, "from_snapshot"))
		var err error
		cli, err = appendMCPWorkspaceModelFlags(cli, args)
		if err != nil {
			return nil, err
		}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "workspace.wait":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "wait", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-timeout", stringArg(args, "timeout"))
		cli = appendOptionalFlag(cli, "-interval", stringArg(args, "interval"))
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "workspace.dispatch":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "dispatch", stringArg(args, "image")}
		cli = appendOptionalFlag(cli, "-exec", stringArg(args, "exec"))
		cli = appendOptionalFlag(cli, "-network", stringArg(args, "network"))
		cli = appendOptionalFlag(cli, "-timeout", stringArg(args, "timeout"))
		var err error
		cli, err = appendMCPWorkspaceEgressSecretFlags(cli, args)
		if err != nil {
			return nil, err
		}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "workspace.exec":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		argv, err := mcpExecArgv(args)
		if err != nil {
			return nil, err
		}
		cli := []string{"--json", "exec", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = append(cli, "--")
		cli = append(cli, argv...)
		return cli, nil
	case "workspace.list":
		return appendOptionalFlag([]string{"--json", "list"}, "-state-dir", stateDir), nil
	case "workspace.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "status", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.result":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "result", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.stats":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "stats", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.logs":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "logs", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.events":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "events", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.egress":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "egress", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "workspace.clone":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "clone", stringArg(args, "source"), stringArg(args, "target")}, "-state-dir", stateDir), nil
	case "workspace.apply":
		if err := requireToolArgs(args, name, "file"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "apply", "-file", stringArg(args, "file")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		cli = appendOptionalFlag(cli, "-supervisor", stringArg(args, "supervisor"))
		return cli, nil
	case "workspace.commit":
		if err := requireToolArgs(args, name, "name", "image"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "commit", stringArg(args, "name"), stringArg(args, "image")}
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
		return appendOptionalFlag([]string{"--json", "artifact", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "network.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "network", "status", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "volume.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "volume", "create", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if size := int64Arg(args, "size_mib"); size > 0 {
			cli = append(cli, "-size-mib", strconv.FormatInt(size, 10))
		}
		return cli, nil
	case "volume.list":
		return appendOptionalFlag([]string{"--json", "volume", "list"}, "-state-dir", stateDir), nil
	case "volume.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "volume", "status", stringArg(args, "name")}, "-state-dir", stateDir), nil
	case "volume.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "volume", "delete", stringArg(args, "name")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "force") {
			cli = append(cli, "-force")
		}
		return cli, nil
	case "images.pull":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "image", "pull", stringArg(args, "image")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		return cli, nil
	case "images.list":
		return appendOptionalFlag([]string{"--json", "image", "list"}, "-state-dir", stateDir), nil
	case "images.push":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "image", "push", stringArg(args, "image")}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "images.tag":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, err
		}
		return appendOptionalFlag([]string{"--json", "image", "tag", stringArg(args, "source"), stringArg(args, "target")}, "-state-dir", stateDir), nil
	case "images.delete":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "image", "delete", stringArg(args, "image")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "delete_files") {
			cli = append(cli, "-purge", "-yes")
		}
		return cli, nil
	case "images.prune":
		cli := []string{"--json", "image", "prune"}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		if boolArg(args, "delete_files") {
			cli = append(cli, "-purge", "-yes")
		}
		return cli, nil
	case "profiles.list":
		return []string{"--json", "profiles"}, nil
	case "host.inspect":
		cli := []string{"--json", "host"}
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		cli = appendOptionalFlag(cli, "-supervisor", stringArg(args, "supervisor"))
		return cli, nil
	case "doctor.check":
		cli := []string{"--json", "doctor"}
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		cli = appendOptionalFlag(cli, "-supervisor", stringArg(args, "supervisor"))
		return cli, nil
	case "contract.get":
		return []string{"--json", "contract"}, nil
	case "kernel.verify":
		cli := []string{"--json", "kernel", "verify"}
		cli = appendOptionalFlag(cli, "-path", stringArg(args, "path"))
		cli = appendOptionalFlag(cli, "-sha256", stringArg(args, "sha256"))
		cli = appendOptionalFlag(cli, "-backend", stringArg(args, "backend"))
		cli = appendOptionalFlag(cli, "-arch", stringArg(args, "arch"))
		return cli, nil
	case "kernel.install":
		cli := []string{"--json", "kernel", "install"}
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
		cli := []string{"--json", "rootfs", "build", "-image", stringArg(args, "image")}
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
	case "models.pull":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "model", "pull", stringArg(args, "model")}
		cli = appendOptionalFlag(cli, "-token", stringArg(args, "token"))
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.list":
		return appendOptionalFlag([]string{"--json", "model", "ls"}, "-state-dir", stateDir), nil
	case "models.remove":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "model", "rm", stringArg(args, "model")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.prune":
		return appendOptionalFlag([]string{"--json", "model", "prune"}, "-state-dir", stateDir), nil
	case "models.serve":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "model", "serve", stringArg(args, "model")}
		if boolArg(args, "dedicated") {
			cli = append(cli, "-dedicated")
		}
		cli = appendOptionalFlag(cli, "-runner", stringArg(args, "runner"))
		cli = appendOptionalFlag(cli, "-runner-gpu", stringArg(args, "runner_gpu"))
		cli = appendOptionalFlag(cli, "-runner-model", stringArg(args, "runner_model"))
		cli = appendOptionalFlag(cli, "-runner-served-model", stringArg(args, "runner_served_model"))
		cli = appendOptionalFlag(cli, "-runner-command", stringArg(args, "runner_command"))
		cli = appendOptionalFlag(cli, "-runner-name", stringArg(args, "runner_name"))
		cli = appendOptionalFlag(cli, "-runner-health-path", stringArg(args, "runner_health_path"))
		if runnerArgs, ok, err := stringSliceArg(args, "runner_args"); err != nil {
			return nil, err
		} else if ok {
			for _, arg := range runnerArgs {
				cli = append(cli, "-runner-arg", arg)
			}
		}
		if runnerEnv, ok, err := stringSliceArg(args, "runner_env"); err != nil {
			return nil, err
		} else if ok {
			for _, entry := range runnerEnv {
				cli = append(cli, "-runner-env", entry)
			}
		}
		cli = appendOptionalFlag(cli, "-token", stringArg(args, "token"))
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.stop":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "model", "stop", stringArg(args, "model")}
		cli = appendOptionalFlag(cli, "-state-dir", stateDir)
		return cli, nil
	case "models.runners":
		return appendOptionalFlag([]string{"--json", "model", "runners"}, "-state-dir", stateDir), nil
	case "models.policy.validate":
		if err := requireToolArgs(args, name, "policy_file"); err != nil {
			return nil, err
		}
		return []string{"--json", "model", "policy", "validate", stringArg(args, "policy_file")}, nil
	case "models.policy.evaluate":
		if err := requireToolArgs(args, name, "policy_file"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "model", "policy", "evaluate", stringArg(args, "policy_file")}
		cli = appendOptionalFlag(cli, "-method", stringArg(args, "method"))
		cli = appendOptionalFlag(cli, "-path", stringArg(args, "request_path"))
		cli = appendOptionalFlag(cli, "-workspace-id", stringArg(args, "workspace_id"))
		cli = appendOptionalFlag(cli, "-capability", stringArg(args, "capability"))
		cli = appendOptionalFlag(cli, "-worker-id", stringArg(args, "worker_id"))
		cli = appendOptionalFlag(cli, "-model", stringArg(args, "model"))
		if value := int64Arg(args, "request_bytes"); value > 0 {
			cli = append(cli, "-request-bytes", strconv.FormatInt(value, 10))
		}
		if value := int64Arg(args, "text_bytes"); value > 0 {
			cli = append(cli, "-text-bytes", strconv.FormatInt(value, 10))
		}
		if value := int64Arg(args, "messages"); value > 0 {
			cli = append(cli, "-messages", strconv.FormatInt(value, 10))
		}
		if value := int64Arg(args, "max_tokens"); value > 0 {
			cli = append(cli, "-max-tokens", strconv.FormatInt(value, 10))
		}
		if _, ok := args["stream"]; ok {
			cli = append(cli, "-stream", strconv.FormatBool(boolArg(args, "stream")))
		}
		if tools, ok, err := stringSliceArg(args, "tools"); err != nil {
			return nil, err
		} else if ok {
			for _, tool := range tools {
				cli = append(cli, "-tool", tool)
			}
		}
		cli = appendOptionalFlag(cli, "-expect", stringArg(args, "expect"))
		return cli, nil
	case "cp":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "cp", stringArg(args, "source"), stringArg(args, "target")}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	case "artifacts.get":
		if err := requireToolArgs(args, name, "name", "artifact", "target"); err != nil {
			return nil, err
		}
		cli := []string{"--json", "artifact", "get", stringArg(args, "name"), stringArg(args, "artifact"), stringArg(args, "target")}
		return appendOptionalFlag(cli, "-state-dir", stateDir), nil
	default:
		return nil, operation.New(operation.ErrorUnsupported, "unsupported MCP tool %s", name)
	}
}

// mcpToolReportsExitAsResult reports whether a silent nonzero CLI exit for this
// tool is a task-outcome signal (surfaced as a structured result with ok=false)
// rather than a tool failure. wait and dispatch both write the full result to
// stdout alongside the silent exit; treating that exit as a JSON-RPC error would
// discard exactly the artifacts (guest output, egress audit) the caller needs.
func mcpToolReportsExitAsResult(name string) bool {
	return name == "workspace.wait" || name == "workspace.dispatch"
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
		// This is a temporary bridge for operations that have not yet moved to
		// direct typed MCP handlers. It requests ordinary CLI JSON, not the
		// deprecated AX profile. MCP owns its envelope, errors, summaries, and
		// agent guidance instead of nesting a second agent protocol.
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
				return nil, true, operation.New(operation.ErrorValidation, "%s[%d] must be a string", name, i)
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

func appendMCPWorkspaceModelFlags(cli []string, args map[string]any) ([]string, error) {
	cli = appendOptionalFlag(cli, "-model-runner", stringArg(args, "model_runner"))
	cli = appendOptionalFlag(cli, "-model-gpu", stringArg(args, "model_gpu"))
	cli = appendOptionalFlag(cli, "-model-runner-model", stringArg(args, "model_runner_model"))
	cli = appendOptionalFlag(cli, "-model-runner-served-model", stringArg(args, "model_runner_served_model"))
	cli = appendOptionalFlag(cli, "-model-runner-command", stringArg(args, "model_runner_command"))
	cli = appendOptionalFlag(cli, "-model-runner-name", stringArg(args, "model_runner_name"))
	cli = appendOptionalFlag(cli, "-model-runner-health-path", stringArg(args, "model_runner_health_path"))
	if runnerArgs, ok, err := stringSliceArg(args, "model_runner_args"); err != nil {
		return nil, err
	} else if ok {
		for _, arg := range runnerArgs {
			cli = append(cli, "-model-runner-arg", arg)
		}
	}
	if runnerEnv, ok, err := stringSliceArg(args, "model_runner_env"); err != nil {
		return nil, err
	} else if ok {
		for _, entry := range runnerEnv {
			cli = append(cli, "-model-runner-env", entry)
		}
	}
	cli = appendOptionalFlag(cli, "-model-mediation", stringArg(args, "model_mediation"))
	cli = appendOptionalFlag(cli, "-model-policy-url", stringArg(args, "model_policy_url"))
	cli = appendOptionalFlag(cli, "-model-policy-file", stringArg(args, "model_policy_file"))
	cli = appendOptionalFlag(cli, "-model-policy-timeout", stringArg(args, "model_policy_timeout"))
	return cli, nil
}

func appendMCPWorkspaceEgressSecretFlags(cli []string, args map[string]any) ([]string, error) {
	cli = appendOptionalFlag(cli, "-egress", stringArg(args, "egress"))
	cli = appendOptionalFlag(cli, "-egress-policy", stringArg(args, "egress_policy"))
	cli = appendOptionalFlag(cli, "-egress-swap-config", stringArg(args, "egress_swap_config"))
	cli = appendOptionalFlag(cli, "-broker-upstream", stringArg(args, "broker_upstream"))
	cli = appendOptionalFlag(cli, "-broker-secret", stringArg(args, "broker_secret"))
	cli = appendOptionalFlag(cli, "-broker-ca", stringArg(args, "broker_ca"))
	cli = appendOptionalFlag(cli, "-secrets-env-file", stringArg(args, "secrets_env_file"))
	if boolArg(args, "broker_proxy") {
		cli = append(cli, "-broker-proxy")
	}
	if boolArg(args, "broker_capture") {
		cli = append(cli, "-broker-capture")
	}
	if boolArg(args, "secrets_audit") {
		cli = append(cli, "-secrets-audit")
	}
	if boolArg(args, "egress_lock_allowlist") {
		cli = append(cli, "-egress-lock-allowlist")
	}
	for _, spec := range []struct {
		arg  string
		flag string
	}{
		{"egress_allow", "-egress-allow"},
		{"egress_passthrough", "-egress-passthrough"},
		{"cred_swap", "-cred-swap"},
		{"broker_env", "-broker-env"},
		{"brokers", "-broker-endpoint"},
		{"secret", "-secret"},
		{"secret_on_demand", "-secret-on-demand"},
	} {
		values, ok, err := stringSliceArg(args, spec.arg)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, value := range values {
			cli = append(cli, spec.flag, value)
		}
	}
	return cli, nil
}

func requireToolArgs(args map[string]any, tool string, names ...string) error {
	for _, name := range names {
		if stringArg(args, name) == "" {
			return operation.New(operation.ErrorValidation, "%s requires %s", tool, name)
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
  mcp                 Serve the MCP stdio endpoint

MCP clients can launch `+"`microagent serve mcp`"+` as a stdio server. See:
docs/cli/serve.md#configure-mcp-clients
`)
}

func printServeMCPHelp(stdout io.Writer) {
	fmt.Fprint(stdout, `microagent serve mcp

Serve the microagent MCP stdio endpoint.

This command is launched by MCP clients over stdio. It is not an interactive
shell command.

Add it as a stdio MCP server in your client config. For example:
  Codex: codex mcp add microagent -- microagent serve mcp
  Claude Code: claude mcp add --transport stdio --scope user microagent -- microagent serve mcp

For JSON-based clients, configure a stdio server named "microagent":
  command: microagent
  args: ["serve", "mcp"]

Client-specific examples: docs/cli/serve.md#configure-mcp-clients
`)
}
