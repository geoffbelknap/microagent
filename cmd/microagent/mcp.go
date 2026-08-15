package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

const mcpClientSetupMessage = `microagent serve mcp is launched by MCP clients over stdio; it is not an interactive shell command.

Add it as a stdio MCP server in your client config. For example:
  Codex: codex mcp add microagent -- microagent serve mcp
  Claude Code: claude mcp add --transport stdio --scope user microagent -- microagent serve mcp

For JSON-based clients, configure a stdio server named "microagent":
  command: microagent
  args: ["serve", "mcp"]

Client-specific examples: docs/cli/serve.md#configure-mcp-clients
`

type mcpHostConfig struct {
	StateDir       string
	SupervisorPath string
}

type mcpHostConfigContextKey struct{}

func withMCPHostConfig(ctx context.Context, config mcpHostConfig) context.Context {
	if strings.TrimSpace(config.StateDir) == "" {
		config.StateDir = defaultStateDir()
	}
	return context.WithValue(ctx, mcpHostConfigContextKey{}, config)
}

func mcpHostConfigFor(ctx context.Context) mcpHostConfig {
	if config, ok := ctx.Value(mcpHostConfigContextKey{}).(mcpHostConfig); ok {
		if strings.TrimSpace(config.StateDir) == "" {
			config.StateDir = defaultStateDir()
		}
		return config
	}
	return mcpHostConfig{StateDir: defaultStateDir()}
}

func bindMCPHostConfig(ctx context.Context, clientArgs map[string]any) (map[string]any, error) {
	for _, name := range []string{"state_dir", "supervisor"} {
		if _, ok := clientArgs[name]; ok {
			return nil, operation.New(
				operation.ErrorValidation,
				"%s is configured by microagent serve mcp and cannot be set per tool call",
				name,
			)
		}
	}
	args := make(map[string]any, len(clientArgs)+2)
	for name, value := range clientArgs {
		args[name] = value
	}
	config := mcpHostConfigFor(ctx)
	args["state_dir"] = config.StateDir
	if strings.TrimSpace(config.SupervisorPath) != "" {
		args["supervisor"] = config.SupervisorPath
	}
	return args, nil
}

func applyMCPHostOptions(opts *workspace.Options, args map[string]any) {
	if stateDir := stringArg(args, "state_dir"); stateDir != "" {
		opts.StateDir = stateDir
	}
	if supervisorPath := stringArg(args, "supervisor"); supervisorPath != "" {
		opts.SupervisorPath = supervisorPath
	}
}

func applyMCPCallerContext(opts *workspace.Options, args map[string]any) {
	principal := principalContextArg(args)
	opts.Purpose, _ = principal["purpose"].(string)
	opts.CorrelationID, _ = principal["correlation_id"].(string)
	opts.Caller = vmkit.CallerAttribution{Channel: "mcp", Assurance: "caller_asserted"}
	opts.Caller.Subject, _ = principal["workload_identity"].(string)
	opts.Caller.DelegatedAuthority, _ = principal["delegated_authority"].(string)
}

func applyMCPLifecycleReason(opts *workspace.Options, args map[string]any) error {
	applyMCPCallerContext(opts, args)
	reason := stringArg(args, "reason")
	if reason == "" {
		return nil
	}
	if opts.Purpose != "" && opts.Purpose != reason {
		return operation.New(operation.ErrorValidation, "reason conflicts with principal.purpose; provide one lifecycle purpose")
	}
	opts.Purpose = reason
	return nil
}

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

func runServeMCP(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) (returnErr error) {
	if len(args) > 0 && wantsHelp(args) {
		printServeMCPHelp(stdout)
		return nil
	}
	finishProgress := func(error) {}
	if fileIsTerminal(os.Stderr) {
		progress := newCommandProgressWithOptions(os.Stderr, true, "serve-mcp", "Serve MCP", progressPrinterOptions{
			Delay: defaultProgressDelay, AlwaysPrintCompletion: true,
		})
		progress.print(operation.ProgressEvent{Phase: "serve_starting", Message: "starting MCP stdio server", Indeterminate: true})
		finishProgress = progress.close
		defer func() { finishProgress(returnErr) }()
	}
	config := mcpHostConfig{StateDir: defaultStateDir()}
	fs := newCommandFlagSet("serve mcp")
	fs.StringVar(&config.StateDir, "state-dir", config.StateDir, "State directory exposed through this MCP server")
	fs.StringVar(&config.SupervisorPath, "supervisor", "", "Supervisor executable exposed through this MCP server")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("usage: microagent serve mcp [--state-dir <dir>] [--supervisor <path>]: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent serve mcp [--state-dir <dir>] [--supervisor <path>]")
	}
	if mcpStdioIsInteractive(stdin, stdout) {
		return fmt.Errorf("%s", strings.TrimSpace(mcpClientSetupMessage))
	}
	finishProgress(nil)
	return serveMCP(withMCPHostConfig(ctx, config), stdin, stdout)
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
	arguments, err := bindMCPHostConfig(ctx, params.Arguments)
	if err != nil {
		return mcpResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &mcpError{Code: -32602, Message: "invalid params", Data: mcpErrorData(err, nil)},
		}
	}
	params.Arguments = arguments
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

func applyMCPWorkspaceSecurityOptions(opts *workspace.Options, args map[string]any) error {
	opts.CapabilityRiskAcknowledgement = stringArg(args, "acknowledge_capability_risk")
	egressAllow, err := mcpMultiFlag(args, "egress_allow")
	if err != nil {
		return err
	}
	egressPassthrough, err := mcpMultiFlag(args, "egress_passthrough")
	if err != nil {
		return err
	}
	credSwap, err := mcpMultiFlag(args, "cred_swap")
	if err != nil {
		return err
	}
	opts.EgressAllowlistLocked = boolArg(args, "egress_lock_allowlist")
	opts.AllowGuestSetuid = boolArg(args, "allow_guest_setuid")
	if err := applyEgressOptionFlags(opts, stringArg(args, "egress"), egressAllow,
		egressPassthrough, stringArg(args, "egress_policy"),
		stringArg(args, "egress_swap_config"), credSwap,
		int64Arg(args, "egress_max_bps"), int64Arg(args, "egress_max_total_bytes"), intArg(args, "egress_max_conns")); err != nil {
		return err
	}
	opts.EgressMaxBytesPerSecExplicit = args["egress_max_bps"] != nil
	opts.EgressMaxTotalBytesExplicit = args["egress_max_total_bytes"] != nil
	opts.EgressMaxConcurrentConnsExplicit = args["egress_max_conns"] != nil
	if args["ttl"] != nil {
		opts.LeaseSeconds, opts.LeaseSecondsExplicit = intArg(args, "ttl"), true
	}
	brokerEnv, err := mcpMultiFlag(args, "broker_env")
	if err != nil {
		return err
	}
	brokers, err := mcpMultiFlag(args, "brokers")
	if err != nil {
		return err
	}
	if err := applyBrokerOptionFlags(opts, stringArg(args, "broker_upstream"),
		stringArg(args, "broker_secret"), brokerEnv, boolArg(args, "broker_proxy"),
		boolArg(args, "broker_capture"), stringArg(args, "broker_ca"),
		stringArg(args, "broker_assurance"), stringArg(args, "broker_grant"), brokers); err != nil {
		return err
	}
	secrets, err := mcpMultiFlag(args, "secret")
	if err != nil {
		return err
	}
	onDemand, err := mcpMultiFlag(args, "secret_on_demand")
	if err != nil {
		return err
	}
	return applySetupEnvSecretOptionFlags(opts, nil, nil, nil, secrets,
		stringArg(args, "secrets_env_file"), onDemand, boolArg(args, "secrets_audit"))
}

func runMCPWorkspaceCreate(ctx context.Context, args map[string]any) (any, error) {
	opts, err := mcpWorkspaceCreateOptions(args)
	if err != nil {
		return nil, err
	}
	if fork := stringArg(args, "from_snapshot"); fork != "" {
		source, tag, err := parseForkSnapshotRef(fork)
		if err != nil {
			return nil, err
		}
		return workspace.CreateFromSnapshot(ctx, opts, source, tag)
	}
	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, stringArg(args, "model_token"))
	if err != nil {
		return nil, err
	}
	defer releaseModel()
	wireRootfsBaseline(&opts)
	return workspace.Create(ctx, opts)
}

func mcpWorkspaceCreateOptions(args map[string]any) (workspace.Options, error) {
	if err := requireToolArgs(args, "workspace.create", "name"); err != nil {
		return workspace.Options{}, err
	}
	opts := workspace.DefaultOptions()
	applyMCPHostOptions(&opts, args)
	applyMCPCallerContext(&opts, args)
	opts.Name = stringArg(args, "name")
	opts.ImageRef = stringArg(args, "image")
	opts.ExecCommand = stringArg(args, "exec")
	opts.Model = stringArg(args, "model")
	opts.DryRun = boolArg(args, "dry_run")
	if profile := stringArg(args, "profile"); profile != "" {
		opts.Profile, opts.ProfileExplicit = profile, true
	}
	if network := stringArg(args, "network"); network != "" {
		opts.Network.Mode = network
	}
	runnerArgs, _, err := stringSliceArg(args, "model_runner_args")
	if err != nil {
		return workspace.Options{}, err
	}
	runnerEnv, _, err := stringSliceArg(args, "model_runner_env")
	if err != nil {
		return workspace.Options{}, err
	}
	command, err := modelrunner.ParseRunnerCommand(stringArg(args, "model_runner_command"))
	if err != nil {
		return workspace.Options{}, fmt.Errorf("model runner command: %w", err)
	}
	opts.ModelRunner = workspace.ModelRunnerSpec{
		Backend: stringArg(args, "model_runner"), GPU: stringArg(args, "model_gpu"),
		BackendModel: stringArg(args, "model_runner_model"),
		ServedModel:  stringArg(args, "model_runner_served_model"),
		Command:      command, Name: stringArg(args, "model_runner_name"),
		HealthPath: stringArg(args, "model_runner_health_path"),
		Args:       runnerArgs, Env: runnerEnv,
	}
	opts.ModelMediation = workspace.ModelMediationSpec{
		Mode:          stringArg(args, "model_mediation"),
		PolicyURL:     stringArg(args, "model_policy_url"),
		PolicyFile:    stringArg(args, "model_policy_file"),
		PolicyTimeout: stringArg(args, "model_policy_timeout"),
	}
	if err := applyMCPWorkspaceSecurityOptions(&opts, args); err != nil {
		return workspace.Options{}, err
	}
	if err := finalizeWorkspaceOptions("create", &opts, workspaceOptionExplicitFlags{Supervisor: stringArg(args, "supervisor") != ""},
		false, "", uint(opts.ResultPort), int(opts.Timeout.Seconds())); err != nil {
		return workspace.Options{}, err
	}
	return opts, nil
}

func mcpMultiFlag(args map[string]any, name string) (multiFlag, error) {
	values, _, err := stringSliceArg(args, name)
	return multiFlag(values), err
}

func runMCPWorkspaceDispatch(ctx context.Context, args map[string]any) (workspace.DispatchResult, error) {
	opts, err := mcpWorkspaceDispatchOptions(args)
	if err != nil {
		return workspace.DispatchResult{}, err
	}
	wireRootfsBaseline(&opts)
	return workspace.RunDispatch(ctx, opts)
}

func mcpWorkspaceDispatchOptions(args map[string]any) (workspace.Options, error) {
	if err := requireToolArgs(args, "workspace.dispatch", "image"); err != nil {
		return workspace.Options{}, err
	}
	opts := workspace.DefaultOptions()
	applyMCPHostOptions(&opts, args)
	applyMCPCallerContext(&opts, args)
	opts.ImageRef = stringArg(args, "image")
	opts.ExecCommand = stringArg(args, "exec")
	opts.UseImageCommand = strings.TrimSpace(opts.ExecCommand) == ""
	opts.Name = workspace.RandomName("dispatch")
	if network := stringArg(args, "network"); network != "" {
		opts.Network.Mode = network
	}
	timeout := opts.Timeout
	if raw := stringArg(args, "timeout"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return workspace.Options{}, operation.New(operation.ErrorValidation, "workspace.dispatch timeout must be a positive duration")
		}
		timeout = parsed
	}
	if err := applyMCPWorkspaceSecurityOptions(&opts, args); err != nil {
		return workspace.Options{}, err
	}
	if err := finalizeWorkspaceOptions("dispatch", &opts, workspaceOptionExplicitFlags{Supervisor: stringArg(args, "supervisor") != ""},
		false, "", uint(opts.ResultPort), int(timeout.Seconds())); err != nil {
		return workspace.Options{}, err
	}
	return opts, nil
}

func runMCPWorkspaceStart(ctx context.Context, args map[string]any) (workspace.Result, error) {
	if err := requireToolArgs(args, "workspace.start", "name"); err != nil {
		return workspace.Result{}, err
	}
	opts := workspace.DefaultOptions()
	applyMCPHostOptions(&opts, args)
	opts.Name = stringArg(args, "name")
	opts.FromSnapshot = stringArg(args, "from_snapshot")
	opts.SerialInput = backendSupportsConsoleInput(opts.Backend)

	runnerArgs, _, err := stringSliceArg(args, "model_runner_args")
	if err != nil {
		return workspace.Result{}, err
	}
	runnerEnv, _, err := stringSliceArg(args, "model_runner_env")
	if err != nil {
		return workspace.Result{}, err
	}
	command, err := modelrunner.ParseRunnerCommand(stringArg(args, "model_runner_command"))
	if err != nil {
		return workspace.Result{}, fmt.Errorf("model runner command: %w", err)
	}
	runnerOverride := workspace.ModelRunnerSpec{
		Backend: stringArg(args, "model_runner"), GPU: stringArg(args, "model_gpu"),
		BackendModel: stringArg(args, "model_runner_model"),
		ServedModel:  stringArg(args, "model_runner_served_model"),
		Command:      command, Name: stringArg(args, "model_runner_name"),
		HealthPath: stringArg(args, "model_runner_health_path"),
		Args:       runnerArgs, Env: runnerEnv,
	}
	mediationOverride := workspace.ModelMediationSpec{
		Mode:          stringArg(args, "model_mediation"),
		PolicyURL:     stringArg(args, "model_policy_url"),
		PolicyFile:    stringArg(args, "model_policy_file"),
		PolicyTimeout: stringArg(args, "model_policy_timeout"),
	}
	if manifest, readErr := workspace.ReadManifest(opts.StateDir, opts.Name); readErr == nil {
		var manifestRunner workspace.ModelRunnerSpec
		if manifest.ModelRunner != nil {
			manifestRunner = *manifest.ModelRunner
		}
		var manifestMediation workspace.ModelMediationSpec
		if manifest.ModelMediation != nil {
			manifestMediation = *manifest.ModelMediation
		}
		opts.ModelRunner = mergeModelRunnerSpec(manifestRunner, runnerOverride)
		opts.ModelMediation = mergeModelMediationSpec(manifestMediation, mediationOverride)
		if strings.TrimSpace(manifest.Model) != "" {
			release, pairErr := ensureModelPairing(ctx, &opts, manifest.Model, "")
			if pairErr != nil {
				return workspace.Result{}, pairErr
			}
			_ = release
		}
	}
	return workspace.Start(ctx, opts)
}

func runMCPWorkspaceExec(ctx context.Context, args map[string]any, start time.Time) (map[string]any, error) {
	if err := requireToolArgs(args, "workspace.exec", "name"); err != nil {
		return nil, err
	}
	req, err := mcpExecRequest(args)
	if err != nil {
		return nil, err
	}
	opts := workspace.DefaultOptions()
	applyMCPHostOptions(&opts, args)
	opts.Name = stringArg(args, "name")
	result, retryMeta, err := mcpWorkspaceExec(ctx, opts, req)
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
	printGroupHelpHeader(stdout, "serve")
	printUsageBlock(stdout, "serve", "serve")
	fmt.Fprint(stdout, `
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

Operator options:
  --state-dir <dir>      State directory exposed through this MCP server
  --supervisor <path>    Supervisor executable exposed through this MCP server

Add it as a stdio MCP server in your client config. For example:
  Codex: codex mcp add microagent -- microagent serve mcp
  Claude Code: claude mcp add --transport stdio --scope user microagent -- microagent serve mcp

For JSON-based clients, configure a stdio server named "microagent":
  command: microagent
  args: ["serve", "mcp"]

Client-specific examples: docs/cli/serve.md#configure-mcp-clients
`)
}
