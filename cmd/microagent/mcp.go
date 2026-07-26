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
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/kernel"
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
var mcpWorkspaceClone = workspace.Clone
var mcpWorkspaceApply = workspace.Apply
var mcpWorkspaceReadSpec = workspace.ReadSpec
var mcpWorkspaceCommit = commit.Commit
var mcpWorkspaceCommitPush = commit.Push
var mcpSnapshotCreate = workspace.Snapshot
var mcpSnapshotForensic = workspace.SnapshotForensic
var mcpSnapshotDelete = workspace.SnapshotRemove
var mcpVolumeCreate = volume.Create
var mcpVolumeRemove = volume.Remove
var mcpImagePull = imagecache.Pull
var mcpImageList = imagecache.List
var mcpImagePush = commit.Push
var mcpImageTag = imagecache.Tag
var mcpImageRemove = imagecache.Remove
var mcpImagePrune = imagecache.Prune
var mcpModelPull = model.Pull
var mcpModelServe = model.Serve
var mcpPolicyValidate = hostworker.ValidateFilePolicy
var mcpPolicyEvaluate = hostworker.EvaluateFilePolicy
var mcpModelRemove = model.Remove
var mcpModelPrune = model.Prune
var mcpModelStop = modelrunner.Stop
var mcpWorkspaceCopy = workspace.Copy
var mcpWorkspaceGetArtifact = workspace.GetArtifact
var mcpDiagnosticsCheck = diagnostics.Check
var mcpKernelVerify = kernel.Verify
var mcpKernelInstall = kernel.Install
var mcpRootfsBuild = func(ctx context.Context, req rootfs.BuildRequest) (rootfs.Provenance, error) {
	return rootfs.NewBuilder().Build(ctx, req)
}

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
	if name == "workspace.start" {
		result, err := runMCPWorkspaceStart(ctx, args)
		meta := mcpMeta(args, start)
		if err != nil {
			return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
		}
		return mcpSuccessEnvelope(result, meta), nil
	}
	if name == "workspace.dispatch" {
		result, err := runMCPWorkspaceDispatch(ctx, args)
		meta := mcpMeta(args, start)
		if err != nil {
			return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
		}
		return mcpSuccessEnvelope(result, meta), nil
	}
	if name == "workspace.create" {
		result, err := runMCPWorkspaceCreate(ctx, args)
		meta := mcpMeta(args, start)
		if err != nil {
			return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
		}
		return mcpSuccessEnvelope(result, meta), nil
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
	err := operation.New(operation.ErrorUnsupported, "unsupported MCP tool %s", name)
	meta := mcpMeta(args, start)
	return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
}

func applyMCPWorkspaceSecurityOptions(opts *workspace.Options, args map[string]any) error {
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
	if err := applyEgressOptionFlags(opts, stringArg(args, "egress"), egressAllow,
		egressPassthrough, stringArg(args, "egress_policy"),
		stringArg(args, "egress_swap_config"), credSwap); err != nil {
		return err
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
		boolArg(args, "broker_capture"), stringArg(args, "broker_ca"), brokers); err != nil {
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
	if opts.DryRun {
		return workspaceResult{
			Workspace: opts.Name, StateDir: opts.StateDir, Profile: opts.Profile,
			Restart: opts.RestartPolicy, Resources: workspaceResources(opts),
			Network: networkSpecFromConfig(opts.Network), Disks: opts.Disks,
			Artifacts: workspaceArtifactsFromOptions(opts), KernelPath: opts.KernelPath,
			Response: vmkit.Response{
				OK: true, Backend: opts.Backend,
				Event: &vmkit.Event{
					Identity: vmkit.Identity{RequestID: newRequestID(), RuntimeID: opts.Name,
						Role: vmkit.RoleWorkload, Backend: opts.Backend},
					State: vmkit.StatePrepared, Detail: "dry run validated workspace config",
					ObservedAt: time.Now().UTC(),
				},
			},
		}, nil
	}
	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, stringArg(args, "model_token"))
	if err != nil {
		return nil, err
	}
	defer releaseModel()
	opts.RootfsBaseline = func(rootfsPath string) (string, rootfs.Provenance, bool) {
		rec, findErr := imagecache.Find(opts.StateDir, opts.ImageRef,
			rootfs.Platform{OS: "linux", Architecture: opts.Architecture})
		if findErr != nil {
			return "", rootfs.Provenance{}, false
		}
		return rec.OutputPath, imagecache.Provenance(rec, rootfsPath), true
	}
	return workspace.Create(ctx, opts)
}

func mcpWorkspaceCreateOptions(args map[string]any) (workspace.Options, error) {
	if err := requireToolArgs(args, "workspace.create", "name"); err != nil {
		return workspace.Options{}, err
	}
	opts := workspace.DefaultOptions()
	opts.Name = stringArg(args, "name")
	opts.ImageRef = stringArg(args, "image")
	opts.ExecCommand = stringArg(args, "exec")
	opts.Model = stringArg(args, "model")
	opts.DryRun = boolArg(args, "dry_run")
	if stateDir := stringArg(args, "state_dir"); stateDir != "" {
		opts.StateDir = stateDir
	}
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
	if err := finalizeWorkspaceOptions("create", &opts, workspaceOptionExplicitFlags{},
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
	return workspace.RunDispatch(ctx, opts)
}

func mcpWorkspaceDispatchOptions(args map[string]any) (workspace.Options, error) {
	if err := requireToolArgs(args, "workspace.dispatch", "image"); err != nil {
		return workspace.Options{}, err
	}
	opts := workspace.DefaultOptions()
	opts.ImageRef = stringArg(args, "image")
	opts.ExecCommand = stringArg(args, "exec")
	opts.UseImageCommand = strings.TrimSpace(opts.ExecCommand) == ""
	opts.Name = workspace.RandomName("dispatch")
	if stateDir := stringArg(args, "state_dir"); stateDir != "" {
		opts.StateDir = stateDir
	}
	if network := stringArg(args, "network"); network != "" {
		opts.Network.Mode = network
	}
	timeout := opts.Timeout
	if raw := stringArg(args, "timeout"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return workspace.Options{}, fmt.Errorf("workspace.dispatch timeout must be a positive duration")
		}
		timeout = parsed
	}
	if err := applyMCPWorkspaceSecurityOptions(&opts, args); err != nil {
		return workspace.Options{}, err
	}
	if err := finalizeWorkspaceOptions("dispatch", &opts, workspaceOptionExplicitFlags{},
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
	opts.Name = stringArg(args, "name")
	if stateDir := stringArg(args, "state_dir"); stateDir != "" {
		opts.StateDir = stateDir
	}
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

// runDirectMCPTool contains agent-facing operations whose inputs map directly
// onto typed library calls. These handlers deliberately bypass CLI parsing,
// rendering, output modes, temporary files, and exit-code policy.
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
	case "workspace.clone":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceClone(
			stateDir,
			stringArg(args, "source"),
			stringArg(args, "target"),
		)
		return jsonCompatible(result), true, err
	case "workspace.apply":
		if err := requireToolArgs(args, name, "file"); err != nil {
			return nil, true, err
		}
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		supervisorPath := stringArg(args, "supervisor")
		if supervisorPath == "" {
			supervisorPath = defaultSupervisorPath(backend)
		}
		spec, err := mcpWorkspaceReadSpec(stringArg(args, "file"))
		if err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceApply(ctx, workspace.Options{
			Backend:        backend,
			Architecture:   architecture,
			StateDir:       stateDir,
			SupervisorPath: supervisorPath,
		}, spec)
		return jsonCompatible(result), true, err
	case "workspace.commit":
		if err := requireToolArgs(args, name, "name", "image"); err != nil {
			return nil, true, err
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		result, err := mcpWorkspaceCommit(ctx, commit.Options{
			StateDir:     stateDir,
			DebugFSPath:  defaultDebugFSPath(),
			Workspace:    workspaceName,
			Backend:      hostBackend(),
			Reference:    stringArg(args, "image"),
			Architecture: architecture,
		})
		if err != nil {
			return nil, true, err
		}
		pushed := false
		if boolArg(args, "push") {
			if err := mcpWorkspaceCommitPush(ctx, stateDir, result.Reference); err != nil {
				return nil, true, err
			}
			pushed = true
		}
		return map[string]any{
			"reference":   result.Reference,
			"digest":      result.Digest,
			"size_bytes":  result.SizeBytes,
			"layout_path": result.LayoutPath,
			"pushed":      pushed,
		}, true, nil
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
	case "cp":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceCopy(
			ctx,
			stateDir,
			defaultDebugFSPath(),
			stringArg(args, "source"),
			stringArg(args, "target"),
		)
		return jsonCompatible(result), true, err
	case "artifacts.get":
		if err := requireToolArgs(args, name, "name", "artifact", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceGetArtifact(
			ctx,
			stateDir,
			defaultDebugFSPath(),
			workspaceName,
			stringArg(args, "artifact"),
			stringArg(args, "target"),
		)
		return jsonCompatible(result), true, err
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
	case "volume.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := mcpVolumeCreate(
			ctx,
			stateDir,
			hostBackend(),
			workspaceName,
			int64Arg(args, "size_mib"),
			defaultMke2fsPath(),
		)
		return jsonCompatible(result), true, err
	case "volume.list":
		result, err := volume.List(stateDir)
		return map[string]any{"volumes": jsonCompatible(result)}, true, err
	case "volume.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := volume.Get(stateDir, workspaceName)
		return jsonCompatible(result), true, err
	case "volume.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		err := mcpVolumeRemove(
			stateDir,
			workspaceName,
			boolArg(args, "force"),
			workspaceRunningPredicate(stateDir),
		)
		return map[string]any{"removed": workspaceName}, true, err
	case "images.pull":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		result, err := mcpImagePull(ctx, imagecache.PullOptions{
			StateDir:     stateDir,
			ImageRef:     stringArg(args, "image"),
			Architecture: stringArg(args, "arch"),
		})
		return jsonCompatible(result), true, err
	case "images.list":
		result, err := mcpImageList(stateDir)
		return map[string]any{"images": jsonCompatible(result)}, true, err
	case "images.push":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		image := stringArg(args, "image")
		err := mcpImagePush(ctx, stateDir, image)
		return map[string]any{"pushed": image}, true, err
	case "images.tag":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpImageTag(stateDir, stringArg(args, "source"), stringArg(args, "target"))
		return jsonCompatible(result), true, err
	case "images.delete":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		result, err := mcpImageRemove(stateDir, stringArg(args, "image"), boolArg(args, "delete_files"))
		return jsonCompatible(result), true, err
	case "images.prune":
		result, err := mcpImagePrune(stateDir, boolArg(args, "delete_files"))
		return jsonCompatible(result), true, err
	case "models.pull":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		result, err := mcpModelPull(ctx, model.PullOptions{
			StateDir: stateDir,
			ModelRef: stringArg(args, "model"),
			Token:    stringArg(args, "token"),
		})
		return jsonCompatible(result), true, err
	case "models.list":
		result, err := model.List(stateDir)
		return map[string]any{"models": jsonCompatible(result)}, true, err
	case "models.remove":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		result, err := mcpModelRemove(stateDir, stringArg(args, "model"), true)
		return jsonCompatible(result), true, err
	case "models.prune":
		result, err := mcpModelPrune(stateDir, false)
		return jsonCompatible(result), true, err
	case "models.serve":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		runnerArgs, _, err := stringSliceArg(args, "runner_args")
		if err != nil {
			return nil, true, err
		}
		runnerEnv, _, err := stringSliceArg(args, "runner_env")
		if err != nil {
			return nil, true, err
		}
		result, err := mcpModelServe(ctx, model.ServeOptions{
			StateDir: stateDir, ModelRef: stringArg(args, "model"),
			Token: stringArg(args, "token"), Dedicated: boolArg(args, "dedicated"),
			Runner: modelrunner.RunnerOverrides{
				Backend: stringArg(args, "runner"), GPU: stringArg(args, "runner_gpu"),
				BackendModel: stringArg(args, "runner_model"),
				ServedModel:  stringArg(args, "runner_served_model"),
				CommandRaw:   stringArg(args, "runner_command"),
				Name:         stringArg(args, "runner_name"),
				HealthPath:   stringArg(args, "runner_health_path"),
				Args:         runnerArgs, Env: runnerEnv,
			},
		})
		return result, true, err
	case "models.stop":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		canonical, _, err := model.Resolve(stringArg(args, "model"))
		if err != nil {
			return nil, true, err
		}
		stopped, err := mcpModelStop(stateDir, canonical)
		return map[string]any{"stopped": stopped}, true, err
	case "models.runners":
		result, err := modelrunner.List(stateDir)
		return map[string]any{"runners": jsonCompatible(result)}, true, err
	case "models.policy.validate":
		result, err := mcpPolicyValidate(stringArg(args, "policy_file"))
		return result, true, err
	case "models.policy.evaluate":
		var maxTokens *int
		if _, ok := args["max_tokens"]; ok {
			value := intArg(args, "max_tokens")
			maxTokens = &value
		}
		var stream *bool
		if _, ok := args["stream"]; ok {
			value := boolArg(args, "stream")
			stream = &value
		}
		tools, _, err := stringSliceArg(args, "tools")
		if err != nil {
			return nil, true, err
		}
		result, err := mcpPolicyEvaluate(stringArg(args, "policy_file"), hostworker.FilePolicyEvaluationOptions{
			Method: stringArg(args, "method"), Path: stringArg(args, "request_path"),
			WorkspaceID: stringArg(args, "workspace_id"), Capability: stringArg(args, "capability"),
			WorkerID: stringArg(args, "worker_id"), Model: stringArg(args, "model"),
			RequestBytes: int64Arg(args, "request_bytes"), TextBytes: int64Arg(args, "text_bytes"),
			Messages: intArg(args, "messages"), MaxTokens: maxTokens, Stream: stream,
			Tools: tools, Expect: stringArg(args, "expect"),
		})
		if err == nil && !result.MatchedExpect {
			err = fmt.Errorf("policy decision %s did not match expected %s", result.Decision, result.Expected)
		}
		return result, true, err
	case "profiles.list":
		return map[string]any{"profiles": jsonCompatible(resourceProfiles)}, true, nil
	case "contract.get":
		return jsonCompatible(vmkit.NewRuntimeContract()), true, nil
	case "host.inspect", "doctor.check":
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		supervisorPath := stringArg(args, "supervisor")
		if supervisorPath == "" {
			supervisorPath = defaultSupervisorPath(backend)
		}
		result, err := mcpDiagnosticsCheck(ctx, diagnostics.Options{
			Backend:        backend,
			Arch:           architecture,
			SupervisorPath: supervisorPath,
		})
		if name == "host.inspect" {
			err = nil
		}
		return jsonCompatible(result), true, err
	case "kernel.verify":
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		path := stringArg(args, "path")
		if path == "" {
			path = defaultKernelPath(backend, architecture)
		}
		result, err := mcpKernelVerify(kernel.VerifyOptions{
			Path:         path,
			SHA256:       stringArg(args, "sha256"),
			Backend:      backend,
			Architecture: architecture,
		})
		return jsonCompatible(result), true, err
	case "kernel.install":
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		outputPath := stringArg(args, "out")
		if outputPath == "" {
			outputPath = workspace.WritableKernelPath(backend, architecture)
		}
		result, err := mcpKernelInstall(ctx, kernel.InstallOptions{
			URL:          stringArg(args, "url"),
			FromPath:     stringArg(args, "from"),
			SHA256:       stringArg(args, "sha256"),
			OutputPath:   outputPath,
			Backend:      backend,
			Architecture: architecture,
		})
		return jsonCompatible(result), true, err
	case "rootfs.build":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		sizeMiB := int64(rootfs.DefaultSizeMiB)
		autoSize := true
		if value := int64Arg(args, "size_mib"); value > 0 {
			sizeMiB = value
			autoSize = false
		}
		req := rootfs.BuildRequest{
			ImageRef: stringArg(args, "image"),
			Platform: rootfs.Platform{
				OS:           firstNonEmpty(stringArg(args, "os"), "linux"),
				Architecture: workspace.NormalizeArch(architecture),
			},
			OutputPath:    stringArg(args, "out"),
			InitPath:      firstNonEmpty(stringArg(args, "init"), rootfs.DefaultInitPath),
			StateDir:      stringArg(args, "state_dir"),
			Mke2fsPath:    firstNonEmpty(stringArg(args, "mke2fs"), "mke2fs"),
			SizeMiB:       sizeMiB,
			AutoSize:      autoSize,
			AllowMutable:  boolArg(args, "allow_mutable"),
			KeepStage:     boolArg(args, "keep_stage"),
			StageSnapshot: stringArg(args, "stage_snapshot"),
		}
		if command := stringArg(args, "exec"); command != "" {
			req.Command = []string{"/bin/sh", "-lc", command}
		}
		result, err := mcpRootfsBuild(ctx, req)
		return jsonCompatible(result), true, err
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
	if operation, ok := vmkit.OperationForMCPTool(name); ok {
		return operation.Confirmation == vmkit.OperationConfirmationPreview
	}
	return false
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

func mcpIdempotencyCacheKey(name string, args map[string]any) string {
	key := stringArg(args, "idempotency_key")
	if key == "" || !mcpMutationTool(name) {
		return ""
	}
	return key
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
