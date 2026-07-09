package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/superviseunit"
	windowshyperv "github.com/geoffbelknap/microagent/pkg/supervisors/windows_hyperv"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

var (
	version          = "dev"
	outputFormat     string
	globalOutputMode outputMode
	stdinIsTerminal  = defaultStdinIsTerminal
	readConfirmation = defaultReadConfirmation
)

var (
	ensureHostWorkerMediator  = hostworker.EnsureProcess
	releaseHostWorkerMediator = hostworker.ReleaseProcess
)

const (
	defaultWorkspaceImageArm64 = workspace.DefaultWorkspaceImageArm64
	defaultWorkspaceImageAMD64 = workspace.DefaultWorkspaceImageAMD64
	defaultWorkspaceImageOther = workspace.DefaultWorkspaceImageOther
	defaultWorkspaceMemoryMiB  = workspace.DefaultWorkspaceMemoryMiB
	defaultWorkspaceCPUCount   = workspace.DefaultWorkspaceCPUCount
	defaultWorkspaceProfile    = workspace.DefaultWorkspaceProfile
	defaultRestartPolicy       = workspace.DefaultRestartPolicy
	defaultNetworkMode         = workspace.DefaultNetworkMode
	consoleDetachByte          = byte(0x1d) // Ctrl-]
	consoleDetachPrefix        = byte(0x10) // Ctrl-P
	consoleDetachSuffix        = byte(0x11) // Ctrl-Q
	consoleShellExitedMarker   = "microagent-init: console shell exited; closing connect session"
)

const (
	envModelMediation     = "MICROAGENT_MODEL_MEDIATION"
	envModelPolicyURL     = "MICROAGENT_MODEL_POLICY_URL"
	envModelPolicyFile    = "MICROAGENT_MODEL_POLICY_FILE"
	envModelPolicyTimeout = "MICROAGENT_MODEL_POLICY_TIMEOUT"
)

const (
	// networkModeFlagHelp is the single source of truth for the --network flag
	// help shown by every command that exposes a workspace network mode.
	networkModeFlagHelp = "Network mode: user (rootless, unprivileged user namespace; default) or isolated (no network)"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		var exitErr cliExitError
		if errors.As(err, &exitErr) {
			if !exitErr.Silent {
				fmt.Fprintln(os.Stderr, exitErr.Error())
			}
			os.Exit(exitErr.Code)
		}
		if currentOutputMode() == outputModeAX {
			if writeErr := writeAXError(os.Stderr, err); writeErr != nil {
				fmt.Fprintln(os.Stderr, writeErr)
			}
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	outputFormat = ""
	globalOutputMode = ""
	args = parseGlobalFlags(args)
	ctx = contextWithOutputMode(ctx, currentOutputMode())
	if len(args) > 0 && args[0] == "--windows-hyperv-listener" {
		return runWindowsHyperVListener(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "--windows-hyperv-deadman" {
		return runWindowsHyperVDeadman(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "--host-worker-mediator" {
		return runHostWorkerMediator(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "--egress-datapath" {
		return runEgressDatapath(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "help" {
		if len(args) > 1 && args[1] == "all" {
			printFullHelp(stdout)
			return nil
		}
		printHelp(stdout)
		return nil
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		return writeVersion(stdout)
	}
	if args[0] == "rootfs" {
		return runRootFS(ctx, args[1:], stdout)
	}
	if args[0] == "kernel" {
		return runKernel(ctx, args[1:], stdout)
	}
	if args[0] == "host" {
		return runHost(ctx, args[1:], stdout)
	}
	if args[0] == "contract" {
		return runContract(args[1:], stdout)
	}
	if args[0] == "doctor" {
		return runDoctor(ctx, args[1:], stdout)
	}
	if args[0] == "profiles" {
		return runProfiles(args[1:], stdout)
	}
	if args[0] == "image" {
		return runImage(args[1:], stdout)
	}
	if args[0] == "perf" {
		return runPerf(ctx, args[1:], stdout)
	}
	if args[0] == "serve" {
		return runServe(ctx, args[1:], stdout)
	}
	if args[0] == "run" {
		return runWorkspace(ctx, args[1:], stdout)
	}
	if args[0] == "dispatch" {
		return runDispatch(ctx, args[1:], stdout)
	}
	if args[0] == "compose" {
		return fmt.Errorf("compose-style multi-workspace projects are not supported; run one MicroAgent workspace at a time and keep orchestration outside microagent")
	}
	if args[0] == "init" {
		return runInit(args[1:], stdout)
	}
	if args[0] == "create" && wantsHelp(args[1:]) {
		printCreateHelp(stdout)
		return nil
	}
	if args[0] == "create" && hasFlagValue(args[1:], "from-snapshot") {
		return runCreateFromSnapshot(ctx, args[1:], stdout)
	}
	if args[0] == "apply" {
		return runApply(ctx, args[1:], stdout)
	}
	if args[0] == "clone" {
		return runClone(args[1:], stdout)
	}
	if args[0] == "commit" {
		return runCommit(ctx, args[1:], stdout)
	}
	if args[0] == "cp" {
		return runCP(ctx, args[1:], stdout)
	}
	if args[0] == "artifact" {
		return runArtifact(ctx, args[1:], stdout)
	}
	if args[0] == "list" || args[0] == "ls" {
		return runList(ctx, args[1:], stdout)
	}
	if args[0] == "ps" {
		return runPS(ctx, args[1:], stdout)
	}
	if args[0] == "gc" {
		return runGC(ctx, args[1:], stdout)
	}
	if args[0] == "logs" || args[0] == "log" {
		return runLogs(ctx, args[1:], stdout)
	}
	if args[0] == "events" {
		return runEvents(ctx, args[1:], stdout)
	}
	if args[0] == "egress" {
		return runEgress(ctx, args[1:], stdout)
	}
	if args[0] == "stats" {
		return runStats(ctx, args[1:], stdout)
	}
	if args[0] == "snapshot" {
		return runSnapshot(ctx, args[1:], stdout)
	}
	if args[0] == "network" {
		return runNetwork(args[1:], stdout)
	}
	if args[0] == "model" {
		return runModel(args[1:], stdout)
	}
	if args[0] == "volume" {
		return runVolume(ctx, args[1:], stdout)
	}
	if args[0] == "secret" {
		return runSecret(ctx, args[1:], stdout)
	}
	if args[0] == "registry" {
		return runRegistry(args[1:], stdout)
	}
	if args[0] == "result" {
		return runWorkspaceStateCommand(ctx, args[0], args[1:], stdout)
	}
	if args[0] == "status" || args[0] == "halt" || args[0] == "quarantine" || args[0] == "pause" || args[0] == "resume" || args[0] == "stop" || args[0] == "kill" || args[0] == "delete" {
		if wantsHelp(args[1:]) || hasWorkspaceStateTarget(args[1:]) {
			return runWorkspaceStateCommand(ctx, args[0], args[1:], stdout)
		}
	}
	if args[0] == "exec" {
		return runStructuredExec(ctx, args[1:], stdout, os.Stderr)
	}
	if args[0] == "connect" {
		return runConnect(ctx, args[1:], stdout)
	}
	if args[0] == "start" && (wantsHelp(args[1:]) || hasPositionalWorkspaceName(args[1:])) {
		return runStartWorkspace(ctx, args[1:], stdout)
	}
	if args[0] == "supervise" {
		return runSupervise(ctx, args[1:], stdout)
	}
	if args[0] == "create" && shouldUseHighLevelCreate(args[1:]) {
		return runHighLevelCreate(ctx, args[1:], stdout)
	}
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args[1:], "supervisor")
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	req, err := requestForCommand(args[0], fs, reorderFlagArgs(args[1:]))
	if err != nil {
		return err
	}
	if !supervisorExplicit && req.Identity != nil {
		supervisorPath = defaultSupervisorPath(req.Identity.Backend)
	}
	opts, err := workspaceOptionsFromRequest(req, supervisorPath)
	if err != nil {
		return err
	}
	resp, err := dispatchWorkspaceRequest(ctx, opts, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	if encodeErr := writeResponse(stdout, resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runWindowsHyperVListener(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("windows-hyperv-listener", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateDir := fs.String("state-dir", "", "State directory")
	name := fs.String("name", "", "Workspace name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stateDir == "" || *name == "" {
		return fmt.Errorf("usage: microagent --windows-hyperv-listener --state-dir <dir> --name <name>")
	}
	return windowshyperv.RunRuntimeListeners(ctx, windowshyperv.Options{StateDir: *stateDir, Name: *name})
}

func runWindowsHyperVDeadman(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("windows-hyperv-deadman", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateDir := fs.String("state-dir", "", "State directory")
	name := fs.String("name", "", "Workspace name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stateDir == "" || *name == "" {
		return fmt.Errorf("usage: microagent --windows-hyperv-deadman --state-dir <dir> --name <name>")
	}
	return windowshyperv.RunDeadman(ctx, windowshyperv.Options{StateDir: *stateDir, Name: *name})
}

func runHostWorkerMediator(ctx context.Context, args []string, ready io.Writer) error {
	fs := flag.NewFlagSet("host-worker-mediator", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts hostworker.Options
	var mode string
	var logPath string
	fs.StringVar(&opts.TargetBaseURL, "target-base-url", "", "Target worker base URL")
	fs.StringVar(&opts.BindHost, "bind-host", "127.0.0.1", "Bind host")
	fs.IntVar(&opts.BindPort, "bind-port", 0, "Bind port")
	fs.StringVar(&mode, "mode", string(hostworker.ModeLocalAllow), "Mediation mode")
	fs.StringVar(&opts.PolicyURL, "policy-url", "", "Policy endpoint URL")
	fs.StringVar(&opts.PolicyFile, "policy-file", "", "Policy JSON file path")
	fs.DurationVar(&opts.PolicyTimeout, "policy-timeout", 2*time.Second, "Policy timeout")
	fs.StringVar(&opts.WorkspaceID, "workspace-id", "", "Workspace ID")
	fs.StringVar(&opts.Capability, "capability", hostworker.DefaultCapability, "Capability")
	fs.StringVar(&opts.WorkerID, "worker-id", "", "Worker ID")
	fs.DurationVar(&opts.UpstreamTimeout, "upstream-timeout", 180*time.Second, "Upstream timeout")
	fs.StringVar(&logPath, "log-path", "", "JSONL audit log path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(opts.TargetBaseURL) == "" {
		return fmt.Errorf("usage: microagent --host-worker-mediator --target-base-url <url> [--bind-host <host>] [--bind-port <port>] [--mode local-allow|policy] [--policy-url <url>|--policy-file <path>] [--log-path <path>]")
	}
	opts.Mode = hostworker.Mode(mode)
	opts.Ready = ready
	var logger *hostworker.JSONLLogger
	if strings.TrimSpace(logPath) != "" {
		var err error
		logger, err = hostworker.OpenJSONLLogger(logPath)
		if err != nil {
			return err
		}
		defer func() { _ = logger.Close() }()
		opts.Logger = logger
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return hostworker.Run(ctx, opts)
}

func requestForCommand(command string, fs *flag.FlagSet, args []string) (vmkit.Request, error) {
	var jsonPath string
	var dryRun bool
	var identity vmkit.Identity
	var config vmkit.Config
	var vsocks multiFlag
	var publishes multiFlag
	var disks multiFlag
	fs.StringVar(&jsonPath, "json", "", "Read request JSON from path, or '-' for stdin")
	fs.BoolVar(&dryRun, "dry-run", false, "Validate without writing state")
	fs.StringVar(&identity.RuntimeID, "id", "", "Workspace ID")
	fs.StringVar(&identity.RuntimeID, "name", "", "Workspace name")
	fs.StringVar(&identity.RequestID, "request-id", "", "Request ID")
	fs.StringVar((*string)(&identity.Role), "role", string(vmkit.RoleWorkload), "Role")
	fs.StringVar(&identity.Backend, "backend", hostBackend(), "Backend identity (internal; must match this install)")
	fs.StringVar(&config.KernelPath, "kernel", "", "Linux kernel path")
	fs.StringVar(&config.RootfsPath, "rootfs", "", "Rootfs image path")
	fs.StringVar(&config.StateDir, "state-dir", "", "State directory")
	fs.IntVar(&config.MemoryMiB, "memory", 512, "Memory in MiB")
	fs.IntVar(&config.CPUCount, "cpus", 2, "CPU count")
	fs.Var(&disks, "disk", "Attach disk name=path:/mount:ro|rw")
	fs.Var(&vsocks, "vsock", "Vsock mapping port=host:port")
	networkMode := fs.String("network", defaultNetworkMode, networkModeFlagHelp)
	fs.Var(&publishes, "publish", "Forward host[:hostPort]:guestPort[/tcp]")
	if err := fs.Parse(args); err != nil {
		return vmkit.Request{}, err
	}
	args = fs.Args()
	switch command {
	case "doctor":
		if len(args) != 0 {
			return vmkit.Request{}, fmt.Errorf("usage: microagent doctor")
		}
		return vmkit.Request{Command: "host"}, nil
	case "create":
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, disks, vsocks, *networkMode, publishes)
		if err != nil {
			return vmkit.Request{}, err
		}
		if dryRun {
			req.Command = "check"
		} else {
			req.Command = "prepare"
		}
		return req, nil
	case "start":
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, disks, vsocks, *networkMode, publishes)
		if err != nil {
			return vmkit.Request{}, err
		}
		req.Command = "start"
		return req, nil
	case "status", "halt", "quarantine", "pause", "resume", "stop", "kill", "delete":
		req, err := stateRequestFromFlagsOrJSON(command, jsonPath, args, identity, config)
		if err != nil {
			return vmkit.Request{}, err
		}
		req.Command = mapCLICommand(command)
		return req, nil
	default:
		return vmkit.Request{}, fmt.Errorf("unknown command: %s", command)
	}
}

type workspaceOptions = workspace.Options
type workspaceSpec = workspace.Spec
type networkSpec = workspace.NetworkSpec
type workspaceDisk = workspace.Disk
type workspaceOutput = workspace.Output
type workspaceArtifacts = workspace.Artifacts
type workspaceManifest = workspace.Manifest

type workspaceResult = workspace.Result

type applyResult = workspace.ApplyResult

type copyResult = workspace.CopyResult

type artifactsResult struct {
	Workspace string                 `json:"workspace"`
	Artifacts vmkit.RuntimeArtifacts `json:"artifacts"`
}

type workspaceNetworkResult = workspace.NetworkStatus

type guestResult = workspace.GuestResult

type workspaceListEntry = workspace.ListEntry

type resourceConfig = workspace.Resources
type resourceProfile = workspace.Profile

type imageRecord = imagecache.Record
type imagePruneResult = imagecache.PruneResult

var resourceProfiles = workspace.Profiles

func runWorkspace(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRunHelp(stdout)
		return nil
	}
	// Pre-scan --model-token before flag parsing: the token drives pull-time
	// auth only and must never land in Options or any persisted state.
	modelToken, _ := flagValue(args, "model-token")

	opts, err := parseWorkspaceOptions("run", args)
	if err != nil {
		return err
	}
	warnEgressOff(opts.EgressMode)
	if strings.TrimSpace(opts.ExecCommand) == "" && !opts.UseImageCommand {
		return fmt.Errorf("run requires IMAGE [COMMAND...] or --exec")
	}
	if opts.Name == "" {
		opts.Name = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}

	// Model orchestration: resolve, pull if needed, start runner, wire into opts.
	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, modelToken)
	if err != nil {
		return err
	}
	defer releaseModel()

	result, err := workspace.Run(ctx, opts)
	if encodeErr := writeWorkspaceResult(stdout, result); encodeErr != nil {
		return encodeErr
	}
	return err
}

// runDispatch is `run` for delegated, single-use work: it boots a throwaway
// workspace under the chosen egress guardrails, runs the command, and returns
// the guest result together with a summary of what the workspace reached on the
// network (the mediator-written audit). The workspace is torn down before it
// returns. Mirrors runWorkspace's option parsing; the difference is the
// audit-bearing result and the one-shot teardown in workspace.RunDispatch.
func runDispatch(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printDispatchHelp(stdout)
		return nil
	}
	modelToken, _ := flagValue(args, "model-token")

	opts, err := parseWorkspaceOptions("dispatch", args)
	if err != nil {
		return err
	}
	warnEgressOff(opts.EgressMode)
	if strings.TrimSpace(opts.ExecCommand) == "" && !opts.UseImageCommand {
		return fmt.Errorf("dispatch requires IMAGE [COMMAND...] or --exec")
	}
	if opts.Name == "" {
		opts.Name = fmt.Sprintf("dispatch-%d", time.Now().UnixNano())
	}
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}

	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, modelToken)
	if err != nil {
		return err
	}
	defer releaseModel()

	result, err := workspace.RunDispatch(ctx, opts)
	if encodeErr := writeDispatchResult(stdout, result); encodeErr != nil {
		return encodeErr
	}
	return err
}

func writeDispatchResult(stdout *os.File, result workspace.DispatchResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.FinalState != "" {
		fmt.Fprintf(stdout, "State: %s\n", result.FinalState)
	}
	if result.Result != nil {
		fmt.Fprintf(stdout, "Exit: %d\n", result.Result.ExitCode)
		if result.Result.Stdout != "" {
			fmt.Fprint(stdout, result.Result.Stdout)
			if !strings.HasSuffix(result.Result.Stdout, "\n") {
				fmt.Fprintln(stdout)
			}
		}
	}
	// The "what did it do on the network" receipt — mediator-written, so the
	// guest cannot forge it.
	a := result.Audit
	fmt.Fprintf(stdout, "Egress: %d decision(s)\n", a.DecisionCount)
	for host, n := range a.AllowByHost {
		fmt.Fprintf(stdout, "  allow %s (%d)\n", host, n)
	}
	for host, n := range a.DenyByHost {
		fmt.Fprintf(stdout, "  deny  %s (%d)\n", host, n)
	}
	return nil
}

func printDispatchHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent dispatch — run one task in a fresh, isolated, single-use workspace

Usage:
  microagent dispatch IMAGE [COMMAND...] [flags]

Boots a throwaway microVM under the egress guardrails you choose, runs the
command, and returns its result AND a summary of what it reached on the network
— the mediator-written audit, so you can see whether it stayed on-intent — then
tears the workspace down. One-shot: nothing persists.

Common flags (same as run):
  --egress <mode>              guarded (default; deny-the-inside) | strict | off
  --egress-allow <host>        allowlisted destination (repeatable)
  --egress-swap-config <path>  inject a credential host-side; the guest never holds it
  --cred-swap PROVIDER[=ref]   inject a built-in provider API key host-side (e.g. anthropic); reference only
  --secret NAME=<ref>          deliver a secret to the guest tmpfs (repeatable)
  --exec <command>             command to run (alternative to positional COMMAND)
  --json                       machine-readable result + audit

Example:
  microagent dispatch docker.io/library/python:3.12 python -c 'print(2+2)'
`)
}

// ensureModelPairing resolves modelRefRaw, pulls the blob if it is missing from
// the store, ensures a host model runner holding opts.Name, and wires
// opts.Model (canonical ref), opts.ModelTarget, and the guest model env. It
// returns a release func that drops the holder (a no-op when modelRefRaw is
// empty). Callers that outlive the boot (start) ignore the release func; the
// holder is then dropped by the next lifecycle verb.
func ensureModelPairing(ctx context.Context, opts *workspaceOptions, modelRefRaw, modelToken string) (func(), error) {
	if strings.TrimSpace(modelRefRaw) == "" {
		return func() {}, nil
	}
	if modelToken == "" {
		if v := os.Getenv("HF_TOKEN"); v != "" {
			modelToken = v
		} else if v := os.Getenv("HUGGING_FACE_HUB_TOKEN"); v != "" {
			modelToken = v
		}
	}
	canonical, _, err := model.Resolve(modelRefRaw)
	if err != nil {
		return nil, err
	}
	rec, err := model.Find(opts.StateDir, canonical)
	if err != nil {
		// Not in the store — auto-pull (one-shot convenience).
		rec, err = model.Pull(ctx, model.PullOptions{StateDir: opts.StateDir, ModelRef: modelRefRaw, Token: modelToken})
		if err != nil {
			return nil, fmt.Errorf("pull model %s: %w", modelRefRaw, err)
		}
	}
	engine, runnerConfig, err := resolveModelRunner(modelRunnerOverridesFromSpec(opts.ModelRunner))
	if err != nil {
		return nil, err
	}
	runner, err := modelrunner.Ensure(ctx, modelrunner.EnsureOptions{
		StateDir:     opts.StateDir,
		ModelRef:     rec.ModelRef,
		ModelPath:    rec.OutputPath,
		Engine:       engine,
		Holder:       opts.Name,
		ReadyTimeout: 120 * time.Second,
		RunnerConfig: runnerConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("start model runner: %w", err)
	}
	// Activate pairing on the workspace options.
	opts.Model = rec.ModelRef
	runnerTarget := fmt.Sprintf("%s:%d", runner.Host, runner.Port)
	modelTarget := runnerTarget
	mediation, err := modelMediationConfigFromSpec(opts.ModelMediation)
	if err != nil {
		_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
		return nil, err
	}
	var mediator *hostworker.ProcessRecord
	if mediation.Enabled {
		execPath, err := os.Executable()
		if err != nil {
			_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
			return nil, fmt.Errorf("resolve microagent executable for model mediation: %w", err)
		}
		workerID := strings.TrimSpace(runner.Key)
		if workerID == "" {
			workerID = runnerTarget
		}
		mediated, err := ensureHostWorkerMediator(ctx, hostworker.ProcessOptions{
			StateDir:        opts.StateDir,
			WorkspaceID:     opts.Name,
			Capability:      hostworker.DefaultCapability,
			WorkerID:        workerID,
			TargetBaseURL:   "http://" + runnerTarget + "/v1",
			Mode:            mediation.Mode,
			PolicyURL:       mediation.PolicyURL,
			PolicyFile:      mediation.PolicyFile,
			PolicyTimeout:   mediation.PolicyTimeout,
			UpstreamTimeout: 180 * time.Second,
			ExecPath:        execPath,
		})
		if err != nil {
			_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
			return nil, fmt.Errorf("start model mediator: %w", err)
		}
		mediator = &mediated
		modelTarget = fmt.Sprintf("%s:%d", mediated.Host, mediated.Port)
	}
	opts.ModelTarget = modelTarget
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	modelURL := fmt.Sprintf("http://127.0.0.1:%d/v1", workspace.DefaultModelGuestPort)
	opts.Env["MICROAGENT_MODEL_URL"] = modelURL
	opts.Env["OPENAI_BASE_URL"] = modelURL
	if err := appendModelWorkerAttachedEvent(*opts, runner, modelURL, mediator); err != nil {
		if mediator != nil {
			_ = releaseHostWorkerMediator(opts.StateDir, opts.Name, hostworker.DefaultCapability)
		}
		_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
		return nil, err
	}
	stateDir, modelRef, holder, backend := opts.StateDir, rec.ModelRef, opts.Name, opts.Backend
	return func() {
		if mediator != nil {
			_ = releaseHostWorkerMediator(stateDir, holder, hostworker.DefaultCapability)
		}
		_ = modelrunner.Release(stateDir, modelRef, holder)
		_ = appendModelWorkerReleasedEvent(stateDir, holder, backend, modelRef)
	}, nil
}

func appendModelWorkerAttachedEvent(opts workspaceOptions, runner modelrunner.Record, modelURL string, mediator *hostworker.ProcessRecord) error {
	fields := []string{
		"model_ref=" + runner.ModelRef,
		"engine=" + runner.Engine,
		fmt.Sprintf("pid=%d", runner.PID),
		"runner_config_digest=" + runner.RunnerConfigDigest,
		"holder=" + opts.Name,
		"model_url=" + modelURL,
	}
	if mediator == nil {
		fields = append(fields, "mediation=direct")
	} else {
		fields = append(fields,
			"mediation=host-worker",
			"mediation_mode="+string(mediator.Mode),
			fmt.Sprintf("mediator_pid=%d", mediator.PID),
			fmt.Sprintf("mediator_port=%d", mediator.Port),
			"mediator_audit_log="+mediator.AuditLogPath,
		)
	}
	detail := modelWorkerEventDetail("attached", fields)
	return appendModelWorkerEventIfWorkspaceExists(opts.StateDir, opts.Name, opts.Backend, vmkit.StateStarting, detail)
}

func appendModelWorkerReleasedEvent(stateDir, name, backend, modelRef string) error {
	state := latestWorkspaceEventState(stateDir, name)
	if state == vmkit.StateUnknown {
		state = vmkit.StateHalted
	}
	detail := modelWorkerEventDetail("released", []string{
		"model_ref=" + modelRef,
		"holder=" + name,
	})
	return appendModelWorkerEventIfWorkspaceExists(stateDir, name, backend, state, detail)
}

func modelWorkerEventDetail(action string, fields []string) string {
	parts := []string{"model_worker=" + action}
	for _, field := range fields {
		if strings.HasSuffix(field, "=") {
			continue
		}
		parts = append(parts, field)
	}
	return strings.Join(parts, " ")
}

func appendModelWorkerEventIfWorkspaceExists(stateDir, name, backend string, state vmkit.VMState, detail string) error {
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(name) == "" {
		return nil
	}
	workspaceDir := filepath.Join(stateDir, name)
	if _, err := os.Stat(workspaceDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(backend) == "" {
		backend = hostBackend()
	}
	event := workspaceEventFile{
		Identity: vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   backend,
		},
		State:      state,
		Detail:     detail,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return appendWorkspaceEvent(filepath.Join(workspaceDir, "events.json"), event)
}

func latestWorkspaceEventState(stateDir, name string) vmkit.VMState {
	events, err := workspace.ReadEvents(stateDir, name)
	if err != nil || len(events) == 0 {
		return vmkit.StateUnknown
	}
	return events[len(events)-1].State
}

type modelMediationConfig struct {
	Enabled       bool
	Mode          hostworker.Mode
	PolicyURL     string
	PolicyFile    string
	PolicyTimeout time.Duration
}

func modelMediationConfigFromEnv() (modelMediationConfig, error) {
	return modelMediationConfigFromSpec(workspace.ModelMediationSpec{})
}

func modelMediationConfigFromSpec(spec workspace.ModelMediationSpec) (modelMediationConfig, error) {
	rawMode := strings.ToLower(strings.TrimSpace(firstNonEmpty(spec.Mode, os.Getenv(envModelMediation))))
	policyURL := strings.TrimSpace(firstNonEmpty(spec.PolicyURL, os.Getenv(envModelPolicyURL)))
	policyFile := strings.TrimSpace(firstNonEmpty(spec.PolicyFile, os.Getenv(envModelPolicyFile)))
	if rawMode == "" && (policyURL != "" || policyFile != "") {
		rawMode = "policy"
	}
	if rawMode == "" || rawMode == "off" || rawMode == "0" || rawMode == "false" || rawMode == "disabled" {
		return modelMediationConfig{}, nil
	}
	cfg := modelMediationConfig{Enabled: true, PolicyTimeout: 2 * time.Second}
	switch rawMode {
	case "local", "local-allow", "allow":
		cfg.Mode = hostworker.ModeLocalAllow
	case "policy":
		cfg.Mode = hostworker.ModePolicy
	default:
		return modelMediationConfig{}, fmt.Errorf("%s must be off, local-allow, or policy", envModelMediation)
	}
	timeout, err := durationValue(envModelPolicyTimeout, firstNonEmpty(spec.PolicyTimeout, os.Getenv(envModelPolicyTimeout)), cfg.PolicyTimeout)
	if err != nil {
		return modelMediationConfig{}, err
	}
	cfg.PolicyTimeout = timeout
	cfg.PolicyURL = policyURL
	cfg.PolicyFile = policyFile
	if cfg.Mode != hostworker.ModePolicy && (cfg.PolicyURL != "" || cfg.PolicyFile != "") {
		return modelMediationConfig{}, fmt.Errorf("model policy source requires model mediation policy mode")
	}
	if cfg.Mode == hostworker.ModePolicy {
		switch {
		case cfg.PolicyURL != "" && cfg.PolicyFile != "":
			return modelMediationConfig{}, fmt.Errorf("%s and %s are mutually exclusive", envModelPolicyURL, envModelPolicyFile)
		case cfg.PolicyURL == "" && cfg.PolicyFile == "":
			return modelMediationConfig{}, fmt.Errorf("%s=policy requires %s or %s", envModelMediation, envModelPolicyURL, envModelPolicyFile)
		}
	}
	return cfg, nil
}

func durationValue(name, raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		seconds, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("%s must be a Go duration like 250ms or 2s, or a number of seconds", name)
		}
		duration = time.Duration(seconds * float64(time.Second))
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return duration, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mergeModelRunnerSpec(base, override workspace.ModelRunnerSpec) workspace.ModelRunnerSpec {
	out := base
	if strings.TrimSpace(override.Backend) != "" {
		out.Backend = override.Backend
	}
	if strings.TrimSpace(override.GPU) != "" {
		out.GPU = override.GPU
	}
	if strings.TrimSpace(override.BackendModel) != "" {
		out.BackendModel = override.BackendModel
	}
	if strings.TrimSpace(override.ServedModel) != "" {
		out.ServedModel = override.ServedModel
	}
	if len(override.Command) != 0 {
		out.Command = append([]string{}, override.Command...)
	}
	if strings.TrimSpace(override.Name) != "" {
		out.Name = override.Name
	}
	if strings.TrimSpace(override.HealthPath) != "" {
		out.HealthPath = override.HealthPath
	}
	if len(override.Args) != 0 {
		out.Args = append([]string{}, override.Args...)
	}
	if len(override.Env) != 0 {
		out.Env = append([]string{}, override.Env...)
	}
	return out
}

func mergeModelMediationSpec(base, override workspace.ModelMediationSpec) workspace.ModelMediationSpec {
	out := base
	if strings.TrimSpace(override.Mode) != "" {
		out.Mode = override.Mode
	}
	if strings.TrimSpace(override.PolicyURL) != "" {
		out.PolicyURL = override.PolicyURL
	}
	if strings.TrimSpace(override.PolicyFile) != "" {
		out.PolicyFile = override.PolicyFile
	}
	if strings.TrimSpace(override.PolicyTimeout) != "" {
		out.PolicyTimeout = override.PolicyTimeout
	}
	return out
}

type modelRunnerOverrides struct {
	Backend      string
	GPU          string
	BackendModel string
	ServedModel  string
	CommandRaw   string
	Command      []string
	Name         string
	HealthPath   string
	Args         []string
	Env          []string
}

func modelRunnerOverridesFromSpec(spec workspace.ModelRunnerSpec) modelRunnerOverrides {
	return modelRunnerOverrides{
		Backend:      spec.Backend,
		GPU:          spec.GPU,
		BackendModel: spec.BackendModel,
		ServedModel:  spec.ServedModel,
		Command:      append([]string{}, spec.Command...),
		Name:         spec.Name,
		HealthPath:   spec.HealthPath,
		Args:         append([]string{}, spec.Args...),
		Env:          append([]string{}, spec.Env...),
	}
}

func resolveModelRunner(overrides modelRunnerOverrides) (modelrunner.Engine, modelrunner.RunnerConfig, error) {
	command, err := modelrunner.ParseRunnerCommand(os.Getenv(modelrunner.EnvModelRunnerCommand))
	if err != nil {
		return nil, modelrunner.RunnerConfig{}, fmt.Errorf("%s: %w", modelrunner.EnvModelRunnerCommand, err)
	}
	args, err := modelrunner.ParseRunnerArgs(os.Getenv(modelrunner.EnvModelRunnerArgs))
	if err != nil {
		return nil, modelrunner.RunnerConfig{}, fmt.Errorf("%s: %w", modelrunner.EnvModelRunnerArgs, err)
	}
	env, err := modelrunner.ParseRunnerEnv(os.Getenv(modelrunner.EnvModelRunnerEnv))
	if err != nil {
		return nil, modelrunner.RunnerConfig{}, fmt.Errorf("%s: %w", modelrunner.EnvModelRunnerEnv, err)
	}
	config := modelrunner.RunnerConfig{
		Backend:      os.Getenv(modelrunner.EnvModelRunnerBackend),
		GPU:          os.Getenv(modelrunner.EnvModelRunnerGPU),
		BackendModel: os.Getenv(modelrunner.EnvModelRunnerModel),
		ServedModel:  os.Getenv(modelrunner.EnvModelRunnerServedModel),
		Command:      command,
		Name:         os.Getenv(modelrunner.EnvModelRunnerName),
		HealthPath:   os.Getenv(modelrunner.EnvModelRunnerHealthPath),
		Args:         args,
		Env:          env,
	}
	if strings.TrimSpace(overrides.Backend) != "" {
		config.Backend = overrides.Backend
		if strings.ToLower(strings.TrimSpace(overrides.Backend)) != modelrunner.BackendCustom && strings.TrimSpace(overrides.CommandRaw) == "" && len(overrides.Command) == 0 {
			config.Command = nil
			config.Name = ""
			config.HealthPath = ""
		}
	}
	if strings.TrimSpace(overrides.GPU) != "" {
		config.GPU = overrides.GPU
	}
	if strings.TrimSpace(overrides.BackendModel) != "" {
		config.BackendModel = overrides.BackendModel
	}
	if strings.TrimSpace(overrides.ServedModel) != "" {
		config.ServedModel = overrides.ServedModel
	}
	if strings.TrimSpace(overrides.CommandRaw) != "" {
		command, err := modelrunner.ParseRunnerCommand(overrides.CommandRaw)
		if err != nil {
			return nil, modelrunner.RunnerConfig{}, fmt.Errorf("runner command: %w", err)
		}
		config.Command = command
	} else if len(overrides.Command) != 0 {
		config.Command = append([]string{}, overrides.Command...)
	}
	if strings.TrimSpace(overrides.Name) != "" {
		config.Name = overrides.Name
	}
	if strings.TrimSpace(overrides.HealthPath) != "" {
		config.HealthPath = overrides.HealthPath
	}
	config, err = config.WithAdditional(overrides.Args, overrides.Env)
	if err != nil {
		return nil, modelrunner.RunnerConfig{}, err
	}
	engine, err := modelrunner.ResolveEngine(config)
	if err != nil {
		return nil, modelrunner.RunnerConfig{}, err
	}
	return engine, config, nil
}

// pendingModelRelease captures the workspace's paired model ref now (delete
// removes the manifest) and returns a release func to invoke once the
// lifecycle verb has succeeded. Best-effort throughout: a missing manifest or
// runner makes the func a no-op, and a stale holder is reclaimed by the next
// verb.
func pendingModelRelease(stateDir, name, backend string) func() {
	manifest, err := workspace.ReadManifest(stateDir, name)
	if err != nil {
		return func() {
			_ = releaseHostWorkerMediator(stateDir, name, hostworker.DefaultCapability)
		}
	}
	modelRef := strings.TrimSpace(manifest.Model)
	if modelRef == "" {
		return func() {
			_ = releaseHostWorkerMediator(stateDir, name, hostworker.DefaultCapability)
		}
	}
	return func() {
		_ = releaseHostWorkerMediator(stateDir, name, hostworker.DefaultCapability)
		_ = modelrunner.Release(stateDir, modelRef, name)
		_ = appendModelWorkerReleasedEvent(stateDir, name, backend, modelRef)
	}
}

func runList(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected list argument: %s", fs.Arg(0))
	}
	entries, err := workspace.List(opts.StateDir)
	if err != nil {
		return err
	}
	reconcileLiveWorkspaces(ctx, opts.StateDir, entries)
	if entries, err = workspace.List(opts.StateDir); err != nil {
		return err
	}
	return writeWorkspaceList(stdout, entries)
}

func runPS(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected ps argument: %s", fs.Arg(0))
	}
	entries, err := workspace.List(opts.StateDir)
	if err != nil {
		return err
	}
	reconcileLiveWorkspaces(ctx, opts.StateDir, entries)
	if entries, err = workspace.List(opts.StateDir); err != nil {
		return err
	}
	return writeWorkspaceList(stdout, filterRunningWorkspaces(entries))
}

// isLiveRecordedState reports whether a recorded state claims the VM is still
// alive — the states whose truth must be re-checked against the running process
// before status/ps/list report them, since a recorded "running" can be stale.
func isLiveRecordedState(state vmkit.VMState) bool {
	switch state {
	case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined, vmkit.StateStopping:
		return true
	}
	return false
}

func filterRunningWorkspaces(entries []workspaceListEntry) []workspaceListEntry {
	filtered := entries[:0]
	for _, entry := range entries {
		if isLiveRecordedState(vmkit.VMState(entry.State)) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// reconcileLiveWorkspaces reaps any listed workspace recorded as live whose
// firecracker process is gone, persisting its true terminal state, so ps/list
// report reality instead of a stale "running". Best-effort and idempotent:
// reconcile failures and healthy VMs leave the recorded state untouched.
func reconcileLiveWorkspaces(ctx context.Context, stateDir string, entries []workspaceListEntry) {
	supervisorPath := defaultSupervisorPath(hostBackend())
	for _, entry := range entries {
		if !isLiveRecordedState(vmkit.VMState(entry.State)) {
			continue
		}
		backend := entry.Backend
		if backend == "" {
			backend = hostBackend()
		}
		_, _ = workspace.Inspect(ctx, workspaceOptions{
			StateDir:       stateDir,
			Name:           entry.Name,
			Backend:        backend,
			SupervisorPath: supervisorPath,
		})
	}
}

// runGC sweeps the host for VMs recorded as running whose firecracker process
// is gone (crashed, OOM-killed, host-rebooted, or an orphaned supervisor) and
// reaps them — reconciling runtime state and reclaiming lingering companion
// processes + transient network state. It does not touch healthy VMs. This is
// the backstop for the supervisor deadman; safe to run on demand.
func runGC(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&backend, "backend", backend, "Backend identity (internal; must match this install)")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		supervisorPath = defaultSupervisorPath(backend)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected gc argument: %s", fs.Arg(0))
	}
	entries, err := workspace.List(opts.StateDir)
	if err != nil {
		return err
	}
	type gcReap struct {
		Name string `json:"name"`
		Was  string `json:"was"`
	}
	checked := 0
	reaped := []gcReap{}
	for _, entry := range entries {
		if vmkit.VMState(entry.State) != vmkit.StateRunning {
			continue
		}
		checked++
		wopts := workspaceOptions{StateDir: opts.StateDir, Name: entry.Name, Backend: backend, SupervisorPath: supervisorPath}
		resp, err := workspace.Control(ctx, wopts, "gc")
		if err != nil && resp.Error == "" {
			fmt.Fprintf(os.Stderr, "gc %s: %v\n", entry.Name, err)
			continue
		}
		if resp.Event != nil && resp.Event.State == vmkit.StateStopped {
			reaped = append(reaped, gcReap{Name: entry.Name, Was: entry.State})
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Checked int      `json:"checked"`
		Reaped  []gcReap `json:"reaped"`
	}{Checked: checked, Reaped: reaped})
}

func runClone(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: microagent clone <source> <target> [--state-dir <dir>]")
	}
	source := fs.Arg(0)
	target := fs.Arg(1)
	if err := validateWorkspaceName(source); err != nil {
		return err
	}
	if err := validateWorkspaceName(target); err != nil {
		return err
	}
	result, err := workspace.Clone(opts.StateDir, source, target)
	if err != nil {
		return err
	}
	return writeWorkspaceResult(stdout, result)
}

func runProfiles(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("profiles", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected profiles argument: %s", fs.Arg(0))
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"profiles": resourceProfiles})
	}
	fmt.Fprintf(stdout, "%-10s %-10s %-6s %-10s %s\n", "NAME", "MEMORY", "CPUS", "DISK", "DESCRIPTION")
	for _, profile := range resourceProfiles {
		fmt.Fprintf(stdout, "%-10s %-10d %-6d %-10d %s\n",
			profile.Name,
			profile.Resources.MemoryMiB,
			profile.Resources.CPUCount,
			profile.Resources.SizeMiB,
			profile.Description,
		)
	}
	return nil
}

func runWorkspaceStateCommand(ctx context.Context, command string, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	name := ""
	yes := false
	force := false
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&backend, "backend", backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	if command == "delete" {
		fs.BoolVar(&yes, "yes", false, "Confirm workspace deletion without prompting")
		fs.BoolVar(&yes, "y", false, "Confirm workspace deletion without prompting")
		fs.BoolVar(&force, "force", false, "Kill a running workspace before deleting")
		fs.BoolVar(&force, "f", false, "Kill a running workspace before deleting")
	}
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		supervisorPath = defaultSupervisorPath(backend)
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: microagent %s <name> [--state-dir <dir>]", command)
	}
	if fs.NArg() == 1 {
		if name != "" {
			return fmt.Errorf("workspace name specified twice")
		}
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: microagent %s <name> [--state-dir <dir>]", command)
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	req := vmkit.Request{
		Command: mapCLICommand(command),
		Identity: &vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   backend,
		},
		Config: &vmkit.Config{StateDir: opts.StateDir},
	}
	workspaceOpts := workspaceOptions{StateDir: opts.StateDir, Name: name, Backend: backend, SupervisorPath: supervisorPath}
	if command == "status" {
		resp, err := workspace.Status(workspaceOpts)
		if err != nil {
			return err
		}
		// Reconcile a possibly-dead VM (reap if its firecracker is gone) so status
		// reports reality, not a stale "running". Only worth a supervisor round-trip
		// when the recorded state still claims to be live; best-effort otherwise.
		if resp.Event != nil && isLiveRecordedState(resp.Event.State) {
			if _, ierr := workspace.Inspect(ctx, workspaceOpts); ierr == nil {
				if reread, rerr := workspace.Status(workspaceOpts); rerr == nil {
					resp = reread
				}
			}
		}
		return writeResponse(stdout, resp)
	}
	if command == "result" {
		resp, err := workspace.ResultStatus(workspaceOpts)
		if err != nil {
			return err
		}
		return writeResultResponse(stdout, resp)
	}
	// Capture the paired model ref before the verb runs (delete removes the
	// manifest); release the workspace's holder only after the verb succeeds.
	var releaseModel func()
	switch command {
	case "halt", "stop", "kill", "delete":
		releaseModel = pendingModelRelease(opts.StateDir, name, backend)
	default:
		releaseModel = func() {}
	}
	if command == "delete" {
		resp, err := runDeleteWorkspace(ctx, workspaceOpts, yes, force)
		if err != nil {
			if resp.Error == "" {
				return err
			}
		}
		if err == nil && resp.OK {
			releaseModel()
		}
		if encodeErr := writeResponse(stdout, resp); encodeErr != nil {
			return encodeErr
		}
		return err
	}
	resp, err := workspace.Control(ctx, workspaceOpts, req.Command)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	if err == nil && resp.OK {
		releaseModel()
	}
	if encodeErr := writeResponse(stdout, resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runDeleteWorkspace(ctx context.Context, opts workspaceOptions, yes, force bool) (vmkit.Response, error) {
	state, _, err := workspace.LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return vmkit.Response{}, err
	}
	if !yes && !force {
		prompt := fmt.Sprintf("Delete workspace %s and its disk/state?", opts.Name)
		if state == vmkit.StateRunning || state == vmkit.StateStarting {
			prompt = fmt.Sprintf("Workspace %s is %s. Stop and delete it?", opts.Name, state)
		}
		ok, err := confirmAction(prompt)
		if err != nil {
			return vmkit.Response{}, err
		}
		if !ok {
			return vmkit.Response{}, fmt.Errorf("delete cancelled")
		}
	}
	if state == vmkit.StateRunning || state == vmkit.StateStarting {
		control := "stop"
		if force {
			control = "kill"
		}
		if resp, err := controlWorkspaceForDelete(ctx, opts, control); err != nil {
			return resp, err
		}
	}
	resp, err := workspace.Control(ctx, opts, "delete")
	if err != nil && deleteNeedsStopped(err, resp) && (yes || force) {
		control := "stop"
		if force {
			control = "kill"
		}
		if stopResp, stopErr := controlWorkspaceForDelete(ctx, opts, control); stopErr != nil {
			return stopResp, stopErr
		}
		resp, err = workspace.Control(ctx, opts, "delete")
	}
	if err == nil && resp.OK {
		// Release any managed volumes so the registry never shows a volume
		// attached to a deleted workspace. Best-effort: a stale holder is
		// reclaimed on next attach regardless.
		_ = volume.DetachAll(opts.StateDir, opts.Name)
	}
	return resp, err
}

func controlWorkspaceForDelete(ctx context.Context, opts workspaceOptions, control string) (vmkit.Response, error) {
	resp, err := workspace.Control(ctx, opts, control)
	if err != nil || !resp.OK {
		if err != nil {
			return resp, err
		}
		if resp.Error != "" {
			return resp, fmt.Errorf("%s", resp.Error)
		}
		return resp, fmt.Errorf("%s workspace %s failed", control, opts.Name)
	}
	return resp, nil
}

func deleteNeedsStopped(err error, resp vmkit.Response) bool {
	text := ""
	if err != nil {
		text = err.Error()
	}
	if resp.Error != "" {
		if text != "" {
			text += " "
		}
		text += resp.Error
	}
	return strings.Contains(text, "is running") && strings.Contains(text, "before delete")
}

func confirmAction(prompt string) (bool, error) {
	if !stdinIsTerminal() {
		return false, fmt.Errorf("%s pass --yes to confirm", prompt)
	}
	return readConfirmation(prompt)
}

func defaultStdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func defaultReadConfirmation(prompt string) (bool, error) {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func runConnect(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	send := fs.String("send", "", "Write text to the console and exit")
	timeoutSeconds := fs.Int("timeout", 5, "Seconds to wait for output after --send")
	readyTimeoutSeconds := fs.Int("ready-timeout", 10, "Seconds to wait for a shell prompt before --send; 0 disables")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent connect <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	workspace.MarkActivity(workspace.Options{StateDir: opts.StateDir, Name: name})
	if *readyTimeoutSeconds < 0 {
		return fmt.Errorf("connect ready-timeout must not be negative")
	}
	consoleOpts := workspace.ConsoleOptions{
		StateDir:            opts.StateDir,
		Name:                name,
		ReadyTimeout:        time.Duration(*readyTimeoutSeconds) * time.Second,
		SendTimeout:         time.Duration(*timeoutSeconds) * time.Second,
		RequireCommandReady: strings.TrimSpace(*send) != "",
	}
	if strings.TrimSpace(*send) != "" {
		if outputStructured() {
			var buf bytes.Buffer
			if err := workspace.SendConsoleCommand(ctx, consoleOpts, *send, &buf); err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{
				"workspace": name,
				"output":    buf.String(),
			})
		}
		return workspace.SendConsoleCommand(ctx, consoleOpts, *send, stdout)
	}
	if outputStructured() {
		return fmt.Errorf("microagent connect interactive sessions are not supported in AX mode; use connect --send for structured output")
	}
	conn, err := workspace.DialConsole(ctx, consoleOpts)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if stdinIsTerminal() {
		restoreTerminal, err := makeRawTerminal(os.Stdin)
		if err != nil {
			return fmt.Errorf("enable raw terminal mode: %w", err)
		}
		defer restoreTerminal()
	}
	type connectResult struct {
		err error
	}
	results := make(chan connectResult, 2)
	go func() {
		_, err := io.Copy(stdout, conn)
		results <- connectResult{err: err}
	}()
	go func() {
		_, err := copyShellInput(conn, os.Stdin)
		results <- connectResult{err: err}
	}()
	result := <-results
	_ = conn.Close()
	if result.err != nil && !errors.Is(result.err, net.ErrClosed) {
		return result.err
	}
	return nil
}

func runCreateFromSnapshot(ctx context.Context, args []string, stdout *os.File) error {
	backend := hostBackend()
	supervisorExplicit := hasFlagValue(args, "supervisor")
	opts := workspaceOptions{
		Backend:        backend,
		Architecture:   defaultGuestArch(),
		StateDir:       defaultStateDir(),
		SupervisorPath: defaultSupervisorPath(backend),
		ResultPort:     workspace.DefaultResultPort,
		SerialInput:    backendSupportsConsoleInput(backend),
	}
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	kernelExplicit := hasFlagValue(args, "kernel")
	fromSnapshot := ""
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&fromSnapshot, "from-snapshot", "", "Fork from <workspace>:<tag>")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	opts.SerialInput = backendSupportsConsoleInput(opts.Backend)
	opts.KernelExplicit = kernelExplicit
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent create <name> --from-snapshot <workspace>:<tag>")
	}
	opts.Name = fs.Arg(0)
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	source, tag, err := parseForkSnapshotRef(fromSnapshot)
	if err != nil {
		return err
	}
	result, err := workspace.CreateFromSnapshot(ctx, opts, source, tag)
	if err != nil && result.Workspace == "" {
		return err
	}
	if encodeErr := writeCreateResult(stdout, result, err); encodeErr != nil {
		return encodeErr
	}
	return err
}

// parseForkSnapshotRef splits a create --from-snapshot value of the form
// <workspace>:<tag> into its parts.
func parseForkSnapshotRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	source, tag, ok := strings.Cut(ref, ":")
	source = strings.TrimSpace(source)
	tag = strings.TrimSpace(tag)
	if !ok || source == "" || tag == "" {
		return "", "", fmt.Errorf("create --from-snapshot requires <workspace>:<tag>, got %q", ref)
	}
	return source, tag, nil
}

func runStartWorkspace(ctx context.Context, args []string, stdout *os.File) error {
	profileExplicit := hasFlagValue(args, "profile")
	memoryExplicit := hasFlagValue(args, "memory")
	cpusExplicit := hasFlagValue(args, "cpus")
	supervisorExplicit := hasFlagValue(args, "supervisor")
	backend := hostBackend()
	opts := workspaceOptions{
		Backend:        backend,
		Architecture:   defaultGuestArch(),
		Profile:        defaultWorkspaceProfile,
		Network:        vmkit.NetworkConfig{Mode: defaultNetworkMode},
		StateDir:       defaultStateDir(),
		SupervisorPath: defaultSupervisorPath(backend),
		ResultPort:     workspace.DefaultResultPort,
		SerialInput:    backendSupportsConsoleInput(backend),
	}
	if err := applyResourceProfile(&opts, false, false, false); err != nil {
		return err
	}
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	kernelExplicit := hasFlagValue(args, "kernel")
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	var vsocks multiFlag
	fs.Var(&vsocks, "vsock", "Vsock mapping port=host:port")
	fs.StringVar(&opts.FromSnapshot, "from-snapshot", "", "Restore the workspace in place from this snapshot tag")
	fs.IntVar(&opts.LeaseSeconds, "ttl", opts.LeaseSeconds, "Idle TTL in seconds; the VM is reaped after this long with no exec/connect (activity renews). 0 = permanent (preserves a create-time lease)")
	var startModelRunner workspace.ModelRunnerSpec
	var startModelMediation workspace.ModelMediationSpec
	modelRunnerCommand := ""
	var modelRunnerArgs multiFlag
	var modelRunnerEnv multiFlag
	fs.StringVar(&startModelRunner.Backend, "model-runner", "", "Model runner backend override: llamacpp, vllm, or custom")
	fs.StringVar(&startModelRunner.GPU, "model-gpu", "", "Model runner GPU intent override: off, on, or auto")
	fs.StringVar(&startModelRunner.BackendModel, "model-runner-model", "", "Backend model id override for runners such as vLLM")
	fs.StringVar(&startModelRunner.ServedModel, "model-runner-served-model", "", "OpenAI-compatible served model name override for runners such as vLLM")
	fs.StringVar(&modelRunnerCommand, "model-runner-command", "", "Custom host model runner command template override")
	fs.StringVar(&startModelRunner.Name, "model-runner-name", "", "Custom host model runner name override")
	fs.StringVar(&startModelRunner.HealthPath, "model-runner-health-path", "", "Custom host model runner health probe path override")
	fs.Var(&modelRunnerArgs, "model-runner-arg", "Extra model runner argument override (repeatable)")
	fs.Var(&modelRunnerEnv, "model-runner-env", "Extra model runner environment KEY=VALUE for this invocation (repeatable; not persisted)")
	fs.StringVar(&startModelMediation.Mode, "model-mediation", "", "Model mediation mode override: off, local-allow, or policy")
	fs.StringVar(&startModelMediation.PolicyURL, "model-policy-url", "", "Model mediation external policy endpoint URL override")
	fs.StringVar(&startModelMediation.PolicyFile, "model-policy-file", "", "Model mediation policy JSON file path override")
	fs.StringVar(&startModelMediation.PolicyTimeout, "model-policy-timeout", "", "Model mediation policy timeout override")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	opts.SerialInput = backendSupportsConsoleInput(opts.Backend)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent start <name> [--state-dir <dir>]")
	}
	opts.Name = fs.Arg(0)
	if strings.TrimSpace(modelRunnerCommand) != "" {
		command, err := modelrunner.ParseRunnerCommand(modelRunnerCommand)
		if err != nil {
			return fmt.Errorf("model runner command: %w", err)
		}
		startModelRunner.Command = command
	}
	startModelRunner.Args = append([]string{}, modelRunnerArgs...)
	startModelRunner.Env = append([]string{}, modelRunnerEnv...)
	opts.KernelExplicit = kernelExplicit
	opts.ProfileExplicit = profileExplicit
	opts.SpecMemory = memoryExplicit
	opts.SpecCPU = cpusExplicit
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	listeners, err := parseVsockMappings(vsocks)
	if err != nil {
		return err
	}
	opts.VsockListeners = listeners
	// Re-pair with the manifest's model for this boot (auto-pulls a missing
	// blob, like run). Start is detached, so the release func is intentionally
	// ignored: the holder is dropped by the next lifecycle verb
	// (halt/stop/kill/delete). A manifest read error is tolerated;
	// workspace.Start surfaces it properly.
	if manifest, err := workspace.ReadManifest(opts.StateDir, opts.Name); err == nil {
		var manifestRunner workspace.ModelRunnerSpec
		if manifest.ModelRunner != nil {
			manifestRunner = *manifest.ModelRunner
		}
		var manifestMediation workspace.ModelMediationSpec
		if manifest.ModelMediation != nil {
			manifestMediation = *manifest.ModelMediation
		}
		opts.ModelRunner = mergeModelRunnerSpec(manifestRunner, startModelRunner)
		opts.ModelMediation = mergeModelMediationSpec(manifestMediation, startModelMediation)
		if strings.TrimSpace(manifest.Model) != "" {
			release, err := ensureModelPairing(ctx, &opts, manifest.Model, "")
			if err != nil {
				return err
			}
			_ = release
		}
	}
	result, err := workspace.Start(ctx, opts)
	if err != nil && result.Workspace == "" {
		return err
	}
	if encodeErr := writeCreateResult(stdout, result, err); encodeErr != nil {
		return encodeErr
	}
	return err
}

func ensureWorkspaceCanStart(stateDir, name string) error {
	state, pid, err := latestWorkspaceStartState(stateDir, name)
	if err != nil {
		return err
	}
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed:
		return nil
	case vmkit.StateQuarantined:
		if pid > 0 {
			return fmt.Errorf("workspace %s is quarantined with preserved pid %d; halt, stop, or kill it before start", name, pid)
		}
		return fmt.Errorf("workspace %s is quarantined; halt, stop, or kill it before start", name)
	case vmkit.StateStarting, vmkit.StateRunning:
		return fmt.Errorf("workspace %s is already %s", name, state)
	default:
		return fmt.Errorf("workspace %s cannot start from state %s", name, state)
	}
}

func latestWorkspaceStartState(stateDir, name string) (vmkit.VMState, int, error) {
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: stateDir, Name: name})
	if err == nil {
		return state.Event.State, state.PID, nil
	}
	if !os.IsNotExist(err) {
		return "", 0, err
	}
	event, eventErr := readWorkspaceEvent(workspaceOptions{StateDir: stateDir, Name: name})
	if eventErr == nil {
		return event.State, 0, nil
	}
	if os.IsNotExist(eventErr) {
		return "", 0, nil
	}
	return "", 0, eventErr
}

type superviseOptions = workspace.SuperviseOptions
type superviseResult = workspace.SuperviseResult

func runSupervise(ctx context.Context, args []string, stdout *os.File) error {
	opts := superviseOptions{
		StateDir:     defaultStateDir(),
		Backend:      hostBackend(),
		Architecture: defaultGuestArch(),
		Interval:     time.Second,
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.KernelExplicit = hasFlagValue(args, "kernel")
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	intervalSeconds := fs.Int("interval", int(opts.Interval.Seconds()), "Seconds between state checks")
	fs.IntVar(&opts.MaxRestarts, "max-restarts", 0, "Maximum restarts; 0 means unlimited")
	install := fs.Bool("install", false, "Install a boot unit that supervises the workspace, then exit")
	uninstall := fs.Bool("uninstall", false, "Remove the installed boot unit, then exit")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent supervise <name> [--state-dir <dir>]")
	}
	if *install && *uninstall {
		return fmt.Errorf("supervise: --install and --uninstall are mutually exclusive")
	}
	if *install || *uninstall {
		return runSuperviseUnit(fs.Arg(0), opts.StateDir, *uninstall, stdout)
	}
	if *intervalSeconds <= 0 {
		return fmt.Errorf("supervise interval must be positive")
	}
	if opts.MaxRestarts < 0 {
		return fmt.Errorf("supervise max-restarts must not be negative")
	}
	opts.Interval = time.Duration(*intervalSeconds) * time.Second
	opts.Name = fs.Arg(0)
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	// Re-pair the manifest's model before every supervised boot, like the
	// start handler does for a single boot: a policy-driven restart must come
	// back with a live runner and MICROAGENT_MODEL_URL working, not silently
	// unpaired. The release func is ignored for the same reason as start —
	// the holder is dropped by the next lifecycle verb.
	opts.BeforeStart = func(ctx context.Context, wsOpts *workspaceOptions) error {
		manifest, err := workspace.ReadManifest(opts.StateDir, opts.Name)
		if err != nil || strings.TrimSpace(manifest.Model) == "" {
			return nil
		}
		release, err := ensureModelPairing(ctx, wsOpts, manifest.Model, "")
		if err != nil {
			return err
		}
		_ = release
		return nil
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := workspace.Supervise(ctx, opts)
	if result.Workspace != "" {
		if encodeErr := writeSuperviseResult(stdout, result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

func runSuperviseUnit(name, stateDir string, uninstall bool, stdout *os.File) error {
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if !uninstall {
		if _, err := workspace.ReadManifest(stateDir, name); err != nil {
			return fmt.Errorf("workspace %q not found; create it before installing a boot unit: %w", name, err)
		}
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve microagent executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	unit, err := superviseunit.Build(superviseunit.Options{
		Name:     name,
		ExecPath: execPath,
		StateDir: stateDir,
		Home:     home,
		GOOS:     runtime.GOOS,
	})
	if err != nil {
		return err
	}
	if uninstall {
		if err := superviseunit.Uninstall(unit); err != nil {
			return err
		}
		if outputJSON(stdout) {
			return writeJSON(stdout, map[string]any{"uninstalled": unit.Label, "path": unit.Path})
		}
		fmt.Fprintf(stdout, "Removed boot unit %s (%s)\n", unit.Label, unit.Path)
		return nil
	}
	enableErr, err := superviseunit.Install(unit)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		out := map[string]any{"installed": unit.Label, "path": unit.Path, "enabled": enableErr == nil}
		if enableErr != nil {
			out["enable_error"] = enableErr.Error()
			out["enable_command"] = strings.Join(unit.EnableArgs, " ")
		}
		return writeJSON(stdout, out)
	}
	fmt.Fprintf(stdout, "Installed boot unit %s (%s)\n", unit.Label, unit.Path)
	if enableErr != nil {
		fmt.Fprintf(stdout, "Could not register it automatically: %v\nEnable it manually with:\n  %s\n", enableErr, strings.Join(unit.EnableArgs, " "))
	} else {
		fmt.Fprintf(stdout, "Registered to start %q at boot.\n", name)
	}
	return nil
}

func superviseWorkspace(ctx context.Context, opts superviseOptions) (superviseResult, error) {
	return workspace.Supervise(ctx, opts)
}

func shouldRestartWorkspace(policy string, state vmkit.VMState) bool {
	return workspace.ShouldRestart(policy, state)
}

func runHighLevelCreate(ctx context.Context, args []string, stdout *os.File) error {
	opts, err := parseWorkspaceOptions("create", args)
	if err != nil {
		return err
	}
	warnEgressOff(opts.EgressMode)
	opts.Progress = rootfsProgress(stdout, "create")
	if opts.DryRun {
		if opts.Name == "" {
			return fmt.Errorf("create requires a name")
		}
		if err := validateWorkspaceName(opts.Name); err != nil {
			return err
		}
		result := workspaceResult{
			Workspace:  opts.Name,
			StateDir:   opts.StateDir,
			Profile:    opts.Profile,
			Restart:    opts.RestartPolicy,
			Resources:  workspaceResources(opts),
			Network:    networkSpecFromConfig(opts.Network),
			Disks:      opts.Disks,
			Artifacts:  workspaceArtifactsFromOptions(opts),
			KernelPath: opts.KernelPath,
			Response: vmkit.Response{
				OK:      true,
				Backend: opts.Backend,
				Event: &vmkit.Event{
					Identity: vmkit.Identity{
						RequestID: newRequestID(),
						RuntimeID: opts.Name,
						Role:      vmkit.RoleWorkload,
						Backend:   opts.Backend,
					},
					State:      vmkit.StatePrepared,
					Detail:     "dry run validated workspace config",
					ObservedAt: time.Now().UTC(),
				},
			},
		}
		return writeWorkspaceResult(stdout, result)
	}
	// Model orchestration: resolve, pull if needed, and pair the setup boot so
	// the guest env is consistent across boots. The canonical ref is persisted
	// in the manifest; every start re-pairs from it. The setup boot's holder is
	// released when create returns (the workspace is left halted).
	modelToken, _ := flagValue(args, "model-token")
	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, modelToken)
	if err != nil {
		return err
	}
	defer releaseModel()
	result, err := workspace.Create(ctx, opts)
	if err != nil && result.Workspace == "" {
		return err
	}
	if encodeErr := writeCreateResult(stdout, result, err); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runApply(ctx context.Context, args []string, stdout *os.File) error {
	opts := workspaceOptions{
		Backend:      hostBackend(),
		Architecture: defaultGuestArch(),
		StateDir:     defaultStateDir(),
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	specPath := ""
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&specPath, "file", "", "Workspace spec file")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected apply argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(specPath) == "" {
		return fmt.Errorf("apply requires --file path")
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	spec, err := readWorkspaceSpec(specPath)
	if err != nil {
		return err
	}
	result, err := workspace.Apply(ctx, opts, spec)
	if encodeErr := writeApplyResult(stdout, result); encodeErr != nil {
		return encodeErr
	}
	return err
}

type stateCommandOptions struct {
	StateDir string
}

// splitCommaHosts flattens comma-separated host entries into distinct hosts.
// --egress-allow / --egress-passthrough are repeatable, but operators reasonably
// also write a single comma-separated list; without this, "a,b" is stored as one
// literal host that matches nothing and silently denies both. Hostnames never
// contain commas, so splitting is safe; empty fields are dropped.
func splitCommaHosts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, entry := range in {
		for _, h := range strings.Split(entry, ",") {
			if h = strings.TrimSpace(h); h != "" {
				out = append(out, h)
			}
		}
	}
	return out
}

func parseWorkspaceOptions(command string, args []string) (workspaceOptions, error) {
	kernelExplicit := hasFlagValue(args, "kernel")
	memoryExplicit := hasFlagValue(args, "memory")
	cpusExplicit := hasFlagValue(args, "cpus")
	sizeExplicit := hasFlagValue(args, "size-mib")
	specExplicit := hasFlagValue(args, "file")
	supervisorExplicit := hasFlagValue(args, "supervisor")
	opts := workspaceOptions{
		Backend:       hostBackend(),
		Architecture:  defaultGuestArch(),
		Profile:       defaultWorkspaceProfile,
		RestartPolicy: defaultRestartPolicy,
		Network:       vmkit.NetworkConfig{Mode: defaultNetworkMode},
		Timeout:       2 * time.Minute,
		ResultPort:    1024,
	}
	if err := applyResourceProfile(&opts, false, false, false); err != nil {
		return workspaceOptions{}, err
	}
	opts.StateDir = defaultStateDir()
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.Mke2fsPath = defaultMke2fsPath()
	opts.GuestInitPath = defaultGuestInitPath(opts.Architecture)
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	specPath := workspaceSpecPath(command, args)
	if specPath != "" {
		if err := applyWorkspaceSpecFile(&opts, specPath, memoryExplicit, cpusExplicit, sizeExplicit); err != nil {
			return workspaceOptions{}, err
		}
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&specPath, "file", specPath, "Workspace spec file")
	fs.StringVar(&opts.Name, "name", opts.Name, "Workspace name")
	fs.StringVar(&opts.Name, "id", opts.Name, "Workspace ID")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "OCI image reference")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Shell command to run as guest init")
	fs.StringVar(&opts.ServiceCommand, "service-command", opts.ServiceCommand, "Long-running shell command to run as the VM service")
	fs.StringVar(&opts.Entrypoint, "entrypoint", opts.Entrypoint, "Shell command to run when the workspace starts")
	fs.BoolVar(&opts.UseImageCommand, "image-command", opts.UseImageCommand, "Run the image Entrypoint/Cmd when creating a prepared workspace")
	fs.StringVar(&opts.ConsoleShell, "shell", opts.ConsoleShell, "Interactive console shell path")
	fs.StringVar(&opts.Hostname, "hostname", opts.Hostname, "Guest hostname")
	setupCommands := multiFlag(append([]string{}, opts.SetupCommands...))
	fs.Var(&setupCommands, "setup", "Shell command to run before --exec")
	var setupFiles multiFlag
	fs.Var(&setupFiles, "setup-file", "Shell script file to run before --exec")
	var envVars multiFlag
	fs.Var(&envVars, "env", "Guest environment variable KEY=VALUE")
	fs.Var(&envVars, "e", "Guest environment variable KEY=VALUE")
	var secretFlags multiFlag
	fs.Var(&secretFlags, "secret", "Declare a secret NAME=<scheme>:<ref> (repeatable)")
	var secretsEnvFile string
	fs.StringVar(&secretsEnvFile, "secrets-env-file", "", "Load secrets from a dotenv file (plaintext, re-read each start)")
	var secretOnDemandFlags multiFlag
	fs.Var(&secretOnDemandFlags, "secret-on-demand", "Declare an on-demand secret NAME=<scheme>:<ref> (fetched at runtime, never written to tmpfs; repeatable)")
	var secretsAudit bool
	fs.BoolVar(&secretsAudit, "secrets-audit", false, "Append every secret access to the workspace audit log")
	var egressMode string
	fs.StringVar(&egressMode, "egress", "", "Egress mediation: guarded (default; deny the inside, allow public), strict (allowlist), off")
	var egressAllow multiFlag
	fs.Var(&egressAllow, "egress-allow", "Allowlisted egress destination host (repeatable)")
	var egressPassthrough multiFlag
	fs.Var(&egressPassthrough, "egress-passthrough", "Allowed egress host that is not TLS-intercepted (repeatable)")
	var egressPolicy string
	fs.StringVar(&egressPolicy, "egress-policy", "", "Path to an egress policy file (.yaml/.yml/.json) declaring allow[]/passthrough[]; unioned with --egress-allow/--egress-passthrough (requires --egress guarded or strict)")
	var egressSwapConfig string
	fs.StringVar(&egressSwapConfig, "egress-swap-config", "", "Path to a credential-swap config (YAML); the mediator injects the real credential host-side so the guest never holds it (requires --egress guarded or strict)")
	var credSwap multiFlag
	fs.Var(&credSwap, "cred-swap", "Inject a provider API key host-side for a built-in provider: PROVIDER[=env:NAME|file:PATH|vault:PATH] (e.g. anthropic, openai). The guest never holds the key; reference only, never a literal. Repeatable; requires --egress guarded or strict")
	var brokerUpstream, brokerSecret string
	fs.StringVar(&brokerUpstream, "broker-upstream", "", "Egress broker upstream base URL; requests reach it through the broker with the credential injected host-side")
	fs.StringVar(&brokerSecret, "broker-secret", "", "Egress broker credential NAME=<scheme>:<ref>; held host-side only, the guest sends @secret:NAME references (never the value)")
	var brokerEnv multiFlag
	fs.Var(&brokerEnv, "broker-env", "Guest env var pointed at the broker, KEY[=VALUE] (empty VALUE = broker URL; repeatable)")
	var brokerProxy bool
	fs.BoolVar(&brokerProxy, "broker-proxy", false, "Also set HTTPS_PROXY/HTTP_PROXY in the guest to the broker (CONNECT tunneling)")
	var diskFlags multiFlag
	fs.Var(&diskFlags, "disk", "Attach disk name=path:/mount:ro|rw")
	var bundleFlags multiFlag
	fs.Var(&bundleFlags, "bundle", "Build and attach bundle name=tar:/mount:ro|rw")
	var volumeFlags multiFlag
	fs.Var(&volumeFlags, "volume", "Attach a volume SRC:DST[:ro|rw] (managed volume name, tar bundle, or ext4 image)")
	fs.Var(&volumeFlags, "v", "Attach a volume SRC:DST[:ro|rw] (managed volume name, tar bundle, or ext4 image)")
	var outputFlags multiFlag
	fs.Var(&outputFlags, "output", "Declare output artifact name=/guest/path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.GuestInitPath, "guest-init", opts.GuestInitPath, "Guest init path")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.RestartPolicy, "restart", opts.RestartPolicy, "Restart policy: never, on-failure, or always")
	fs.StringVar(&opts.Network.Mode, "network", opts.Network.Mode, networkModeFlagHelp)
	mediationMapping := ""
	fs.StringVar(&mediationMapping, "mediation", "", "Required mediation vsock mapping port=host:port")
	mediationOptional := false
	fs.BoolVar(&mediationOptional, "mediation-optional", false, "Allow workspace to run if mediation is unavailable")
	var publishFlags multiFlag
	fs.Var(&publishFlags, "publish", "Forward host[:hostPort]:guestPort[/tcp]")
	fs.Var(&publishFlags, "p", "Forward host[:hostPort]:guestPort[/tcp]")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	fs.Int64Var(&opts.SizeMiB, "size-mib", opts.SizeMiB, "Rootfs image size in MiB")
	resultPort := uint(opts.ResultPort)
	fs.UintVar(&resultPort, "result-port", resultPort, "Vsock result port")
	var timeoutSeconds int
	fs.IntVar(&timeoutSeconds, "timeout", int(opts.Timeout.Seconds()), "Run timeout in seconds")
	fs.IntVar(&opts.LeaseSeconds, "ttl", opts.LeaseSeconds, "Idle TTL in seconds; the VM is reaped after this long with no exec/connect (activity renews). 0 = permanent")
	fs.BoolVar(&opts.Keep, "keep", false, "Keep workspace state after run (run discards by default)")
	rm := false
	fs.BoolVar(&rm, "rm", false, "Discard workspace state after run (explicit; this is the default for run)")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "Validate without writing state")
	// --model-token is consumed by callers via flagValue pre-scan (it must never
	// land in Options); register it so the flagset accepts it and shows it in
	// --help output.
	var absorbedModelToken string
	fs.StringVar(&opts.Model, "model", opts.Model, "Pair this workspace with a locally-served model (HuggingFace GGUF ref); injects MICROAGENT_MODEL_URL/OPENAI_BASE_URL")
	fs.StringVar(&absorbedModelToken, "model-token", "", "HuggingFace token for model auto-pull (else HF_TOKEN/HUGGING_FACE_HUB_TOKEN)")
	modelRunnerCommand := ""
	modelRunnerArgs := multiFlag(append([]string{}, opts.ModelRunner.Args...))
	var modelRunnerEnv multiFlag
	fs.StringVar(&opts.ModelRunner.Backend, "model-runner", opts.ModelRunner.Backend, "Model runner backend: llamacpp, vllm, or custom")
	fs.StringVar(&opts.ModelRunner.GPU, "model-gpu", opts.ModelRunner.GPU, "Model runner GPU intent: off, on, or auto")
	fs.StringVar(&opts.ModelRunner.BackendModel, "model-runner-model", opts.ModelRunner.BackendModel, "Backend model id for runners such as vLLM")
	fs.StringVar(&opts.ModelRunner.ServedModel, "model-runner-served-model", opts.ModelRunner.ServedModel, "OpenAI-compatible served model name for runners such as vLLM")
	fs.StringVar(&modelRunnerCommand, "model-runner-command", "", "Custom host model runner command template")
	fs.StringVar(&opts.ModelRunner.Name, "model-runner-name", opts.ModelRunner.Name, "Custom host model runner name for state output")
	fs.StringVar(&opts.ModelRunner.HealthPath, "model-runner-health-path", opts.ModelRunner.HealthPath, "Custom host model runner health probe path")
	fs.Var(&modelRunnerArgs, "model-runner-arg", "Extra model runner argument (repeatable)")
	fs.Var(&modelRunnerEnv, "model-runner-env", "Extra model runner environment KEY=VALUE for this invocation (repeatable; not persisted)")
	fs.StringVar(&opts.ModelMediation.Mode, "model-mediation", opts.ModelMediation.Mode, "Model mediation mode: off, local-allow, or policy")
	fs.StringVar(&opts.ModelMediation.PolicyURL, "model-policy-url", opts.ModelMediation.PolicyURL, "Model mediation external policy endpoint URL")
	fs.StringVar(&opts.ModelMediation.PolicyFile, "model-policy-file", opts.ModelMediation.PolicyFile, "Model mediation policy JSON file path")
	fs.StringVar(&opts.ModelMediation.PolicyTimeout, "model-policy-timeout", opts.ModelMediation.PolicyTimeout, "Model mediation policy timeout")
	if err := rejectUnsupportedContainerCompatibilityFlags(args); err != nil {
		return workspaceOptions{}, err
	}
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return workspaceOptions{}, err
	}
	if fs.NArg() != 0 {
		if command == "create" && fs.NArg() == 1 && opts.Name == "" {
			opts.Name = fs.Arg(0)
		} else if command == "run" || command == "dispatch" {
			if err := applyContainerRunArgs(&opts, fs.Args()); err != nil {
				return workspaceOptions{}, err
			}
		} else {
			return workspaceOptions{}, fmt.Errorf("unexpected %s argument: %s", command, fs.Arg(0))
		}
	}
	if err := applyModelRunnerOptionFlags(&opts, modelRunnerCommand, modelRunnerArgs, modelRunnerEnv); err != nil {
		return workspaceOptions{}, err
	}
	if err := applySetupEnvSecretOptionFlags(&opts, setupCommands, setupFiles, envVars, secretFlags, secretsEnvFile, secretOnDemandFlags, secretsAudit); err != nil {
		return workspaceOptions{}, err
	}
	if err := applyEgressOptionFlags(&opts, egressMode, egressAllow, egressPassthrough, egressPolicy, egressSwapConfig, credSwap); err != nil {
		return workspaceOptions{}, err
	}
	if err := applyBrokerOptionFlags(&opts, brokerUpstream, brokerSecret, brokerEnv, brokerProxy); err != nil {
		return workspaceOptions{}, err
	}
	if err := applyStorageOptionFlags(&opts, volumeFlags, diskFlags, bundleFlags, outputFlags); err != nil {
		return workspaceOptions{}, err
	}
	if err := applyNetworkMediationOptionFlags(&opts, publishFlags, mediationMapping, mediationOptional, command); err != nil {
		return workspaceOptions{}, err
	}
	explicit := workspaceOptionExplicitFlags{
		Kernel:     kernelExplicit,
		Memory:     memoryExplicit,
		CPUs:       cpusExplicit,
		Size:       sizeExplicit,
		Spec:       specExplicit,
		Supervisor: supervisorExplicit,
	}
	if err := finalizeWorkspaceOptions(command, &opts, explicit, rm, specPath, resultPort, timeoutSeconds); err != nil {
		return workspaceOptions{}, err
	}
	return opts, nil
}

type workspaceOptionExplicitFlags struct {
	Kernel     bool
	Memory     bool
	CPUs       bool
	Size       bool
	Spec       bool
	Supervisor bool
}

func applyModelRunnerOptionFlags(opts *workspaceOptions, modelRunnerCommand string, modelRunnerArgs, modelRunnerEnv multiFlag) error {
	if strings.TrimSpace(modelRunnerCommand) != "" {
		command, err := modelrunner.ParseRunnerCommand(modelRunnerCommand)
		if err != nil {
			return fmt.Errorf("model runner command: %w", err)
		}
		opts.ModelRunner.Command = command
	}
	opts.ModelRunner.Args = append([]string{}, modelRunnerArgs...)
	opts.ModelRunner.Env = append([]string{}, modelRunnerEnv...)
	return nil
}

func applySetupEnvSecretOptionFlags(opts *workspaceOptions, setupCommands, setupFiles, envVars, secretFlags multiFlag, secretsEnvFile string, secretOnDemandFlags multiFlag, secretsAudit bool) error {
	opts.SetupCommands = append([]string{}, setupCommands...)
	setupFileCommands, err := setupCommandsFromFiles(setupFiles, ".")
	if err != nil {
		return err
	}
	opts.SetupCommands = append(opts.SetupCommands, setupFileCommands...)
	env, err := parseEnvFlags(envVars)
	if err != nil {
		return err
	}
	opts.Env = mergeEnv(opts.Env, env)
	secrets, err := parseSecretFlags(secretFlags)
	if err != nil {
		return err
	}
	opts.Secrets = secrets
	if strings.TrimSpace(secretsEnvFile) != "" {
		opts.SecretEnvFiles = []string{secretsEnvFile}
	}
	onDemand, err := parseSecretFlags(secretOnDemandFlags)
	if err != nil {
		return err
	}
	opts.OnDemandSecrets = onDemand
	opts.SecretsAudit = secretsAudit
	return nil
}

// applyBrokerOptionFlags parses the --broker-* flags into Options.Broker via the
// shared workspace.ParseBrokerConfig, so the CLI and the Agentfile agent.broker
// block validate and build a broker identically. A partial declaration fails
// loudly and a literal secret is rejected at parse time, before any state is
// written (matching --cred-swap).
func applyBrokerOptionFlags(opts *workspaceOptions, brokerUpstream, brokerSecret string, brokerEnv multiFlag, brokerProxy bool) error {
	broker, err := workspace.ParseBrokerConfig(brokerUpstream, brokerSecret, []string(brokerEnv), brokerProxy)
	if err != nil {
		return err
	}
	if broker != nil {
		opts.Broker = broker
	}
	return nil
}

func applyEgressOptionFlags(opts *workspaceOptions, egressMode string, egressAllow, egressPassthrough multiFlag, egressPolicy, egressSwapConfig string, credSwap multiFlag) error {
	// Mode precedence: an explicit --egress flag wins; otherwise keep any value a
	// workspace spec (Agentfile `agent.egress`) already applied; otherwise default
	// to guarded (the secure default, matching parseEgressMode("")).
	if strings.TrimSpace(egressMode) != "" {
		mode, err := parseEgressMode(egressMode)
		if err != nil {
			return err
		}
		opts.EgressMode = mode
	} else if strings.TrimSpace(opts.EgressMode) == "" {
		opts.EgressMode = vmkit.EgressModeGuarded
	}
	mode := opts.EgressMode
	// Allow/passthrough are additive: default-deny means flags, a spec, a policy
	// file, and the manifest can only ADD reachability, never remove it, so they
	// combine by union. Seed with the flag hosts unioned with whatever the spec
	// already applied to Options.
	allowHosts := append(splitCommaHosts([]string(egressAllow)), opts.EgressAllow...)
	passthroughHosts := append(splitCommaHosts([]string(egressPassthrough)), opts.EgressPassthrough...)
	if strings.TrimSpace(egressPolicy) != "" {
		// A policy file only enforces against a running mediator; with mediation
		// off there is nothing to apply it to, so reject rather than silently
		// ignore (which would mislead the operator into believing it took effect).
		if mode == vmkit.EgressModeOff {
			return fmt.Errorf("--egress-policy: an egress policy file requires --egress guarded or strict")
		}
		pf, err := egress.LoadPolicyFile(egressPolicy)
		if err != nil {
			return err
		}
		allowHosts = append(allowHosts, pf.Allow...)
		passthroughHosts = append(passthroughHosts, pf.Passthrough...)
	}
	opts.EgressAllow = egress.DedupeHosts(allowHosts)
	opts.EgressPassthrough = egress.DedupeHosts(passthroughHosts)
	if trimmed := strings.TrimSpace(egressSwapConfig); trimmed != "" {
		// Credential swap injects a real secret host-side at the mediator; with
		// mediation off there is no mediator to inject it, so reject rather than
		// silently ignore (mirroring --egress-policy).
		if mode == vmkit.EgressModeOff {
			return fmt.Errorf("--egress-swap-config: credential swap requires --egress guarded or strict")
		}
		opts.EgressSwapConfigPath = trimmed
	}
	// cred-swap from flags unions with any a spec already declared.
	providers, err := parseCredSwapFlags(credSwap)
	if err != nil {
		return err
	}
	opts.CredSwapProviders = append(opts.CredSwapProviders, providers...)
	if len(opts.CredSwapProviders) > 0 && mode == vmkit.EgressModeOff {
		// cred-swap is performed by the mediator (host-side MITM injection), which
		// only runs in guarded/strict; with egress off there is no mediator to
		// inject the key, so the swap would silently do nothing. Fail loud. Checked
		// against the merged set so a spec-sourced cred-swap is caught too.
		return fmt.Errorf("--cred-swap: credential swap requires --egress guarded or strict")
	}
	return nil
}

// parseCredSwapFlags parses repeatable `--cred-swap PROVIDER[=ref]` specs into
// resolved CredSwapProvider entries via the shared workspace parser (same one the
// Agentfile `agent.cred-swap` block uses). It fails fast: an unknown provider or
// a literal (non-reference) key is rejected here, before any file is written or
// audit entry made, so a secret pasted on the command line never gets processed.
func parseCredSwapFlags(credSwap multiFlag) ([]workspace.CredSwapProvider, error) {
	if len(credSwap) == 0 {
		return nil, nil
	}
	providers := make([]workspace.CredSwapProvider, 0, len(credSwap))
	for _, raw := range credSwap {
		provider, ok, err := workspace.ParseCredSwapProvider(raw)
		if err != nil {
			return nil, err
		}
		if ok {
			providers = append(providers, provider)
		}
	}
	return providers, nil
}

// egressOffWarning returns a one-line operator notice when egress mediation is
// off — unrestricted network, no allowlist, no audit, no cred-swap. It is empty
// for guarded/strict. Printed to stderr at launch so turning mediation off is
// never silent (it's effectively yolo mode).
func egressOffWarning(mode string) string {
	if mode != vmkit.EgressModeOff {
		return ""
	}
	return "⚠ egress off: this workspace has unrestricted network access — no mediation, no audit, no cred-swap (yolo mode)."
}

// warnEgressOff prints the egress-off notice to stderr if applicable. Stderr, so
// it never pollutes the stdout result (including MCP/--mode=ax JSON).
func warnEgressOff(mode string) {
	if w := egressOffWarning(mode); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
}

func applyStorageOptionFlags(opts *workspaceOptions, volumeFlags, diskFlags, bundleFlags, outputFlags multiFlag) error {
	volumes, err := parseWorkspaceVolumes(volumeFlags)
	if err != nil {
		return err
	}
	disks, err := parseWorkspaceDisks(diskFlags, false)
	if err != nil {
		return err
	}
	bundles, err := parseWorkspaceDisks(bundleFlags, true)
	if err != nil {
		return err
	}
	opts.Disks = append(opts.Disks, volumes...)
	opts.Disks = append(opts.Disks, disks...)
	opts.Disks = append(opts.Disks, bundles...)
	outputs, err := parseWorkspaceOutputs(outputFlags)
	if err != nil {
		return err
	}
	opts.Outputs = append(opts.Outputs, outputs...)
	return nil
}

func applyNetworkMediationOptionFlags(opts *workspaceOptions, publishFlags multiFlag, mediationMapping string, mediationOptional bool, command string) error {
	published, err := parsePortForwardMappings(publishFlags)
	if err != nil {
		return err
	}
	opts.Network.PortForwards = append(opts.Network.PortForwards, published...)
	if strings.TrimSpace(mediationMapping) != "" {
		mediation, err := parseMediationMapping(mediationMapping, mediationOptional)
		if err != nil {
			return err
		}
		opts.Mediation = &mediation
	} else if mediationOptional {
		return fmt.Errorf("%s requires --mediation with --mediation-optional", command)
	}
	return nil
}

func finalizeWorkspaceOptions(command string, opts *workspaceOptions, explicit workspaceOptionExplicitFlags, rm bool, specPath string, resultPort uint, timeoutSeconds int) error {
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		if command == "create" {
			opts.ImageRef = defaultWorkspaceImage(opts.Architecture)
		} else {
			return fmt.Errorf("%s requires --image", command)
		}
	}
	if (command == "run" || command == "dispatch") && strings.TrimSpace(opts.ExecCommand) == "" {
		opts.UseImageCommand = true
	}
	if err := validateConsoleShell(opts.ConsoleShell); err != nil {
		return err
	}
	if strings.TrimSpace(opts.Hostname) == "" && strings.TrimSpace(opts.Name) != "" {
		opts.Hostname = workspace.DefaultHostname(opts.Name)
	}
	if err := validateHostname(opts.Hostname); err != nil {
		return err
	}
	if !explicit.Kernel {
		opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	if !explicit.Supervisor {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	opts.KernelExplicit = explicit.Kernel
	if err := validateRestartPolicy(opts.RestartPolicy); err != nil {
		return err
	}
	opts.RestartPolicy = normalizeRestartPolicy(opts.RestartPolicy)
	opts.Network = normalizeNetworkConfig(opts.Network)
	if err := vmkit.ValidateNetworkConfig(opts.Network); err != nil {
		return err
	}
	if command != "create" && strings.TrimSpace(opts.ServiceCommand) != "" {
		return fmt.Errorf("%s does not support --service-command", command)
	}
	if opts.UseImageCommand && strings.TrimSpace(opts.ServiceCommand) != "" {
		return fmt.Errorf("%s cannot use both --image-command and --service-command", command)
	}
	if (command == "run" || command == "dispatch") && rm && opts.Keep {
		return fmt.Errorf("%s cannot use both --rm and --keep", command)
	}
	if command != "run" && command != "dispatch" && rm {
		return fmt.Errorf("%s does not support --rm", command)
	}
	if rm {
		opts.Keep = false // --rm forces discard, authoritative over any prior Keep setting
	}
	opts.SerialInput = backendSupportsConsoleInput(opts.Backend)
	if explicit.Spec && specPath == "" {
		return fmt.Errorf("%s requires --file path", command)
	}
	if err := applyResourceProfile(opts, explicit.Memory || opts.SpecMemory, explicit.CPUs || opts.SpecCPU, explicit.Size || opts.SpecSize); err != nil {
		return err
	}
	if err := validateResourceConfig(workspaceResources(*opts), true); err != nil {
		return err
	}
	if timeoutSeconds <= 0 {
		return fmt.Errorf("%s timeout must be positive", command)
	}
	if resultPort > uint(^uint32(0)) {
		return fmt.Errorf("%s result port is too large", command)
	}
	opts.ResultPort = uint32(resultPort)
	opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	return nil
}

func applyContainerRunArgs(opts *workspaceOptions, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if strings.TrimSpace(opts.ExecCommand) != "" {
		return fmt.Errorf("run cannot use both --exec and positional command arguments")
	}
	if opts.UseImageCommand {
		return fmt.Errorf("run cannot use both --image-command and positional command arguments")
	}
	commandArgs := args
	if strings.TrimSpace(opts.ImageRef) == "" {
		opts.ImageRef = strings.TrimSpace(args[0])
		commandArgs = args[1:]
	}
	if strings.TrimSpace(opts.ImageRef) == "" {
		return fmt.Errorf("run requires IMAGE [COMMAND...] or --image")
	}
	if len(commandArgs) == 0 {
		opts.UseImageCommand = true
		return nil
	}
	opts.ExecCommand = shellCommandFromArgs(commandArgs)
	return nil
}

func shellCommandFromArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellSingleQuote(arg))
	}
	return "exec " + strings.Join(quoted, " ")
}

func ensureWorkspaceKernel(ctx context.Context, opts *workspaceOptions) error {
	if opts.KernelExplicit {
		return nil
	}
	if strings.TrimSpace(opts.KernelPath) == "" {
		opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	if _, err := os.Stat(opts.KernelPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	// No kernel installed yet: fetch + verify + install the latest from the
	// signed manifest. (An already-present kernel is used as-is, above.)
	if _, err := kernel.Install(ctx, kernel.InstallOptions{
		Backend:      opts.Backend,
		Architecture: opts.Architecture,
		OutputPath:   opts.KernelPath,
	}); err != nil {
		return fmt.Errorf("install kernel for %s/%s: %w (or pass --kernel)", opts.Backend, opts.Architecture, err)
	}
	return nil
}

func workspaceSpecPath(command string, args []string) string {
	switch command {
	case "create", "run", "dispatch":
	default:
		return ""
	}
	if value, ok := flagValue(args, "file"); ok {
		return value
	}
	// Auto-discover a workspace spec only for create — the durable, declarative
	// path. run/dispatch require an explicit --file so a stray microagent.yaml in
	// the working directory never silently alters a one-shot invocation.
	if command == "create" {
		if _, err := os.Stat("microagent.yaml"); err == nil {
			return "microagent.yaml"
		}
		if _, err := os.Stat("microagent.yml"); err == nil {
			return "microagent.yml"
		}
	}
	return ""
}

func applyWorkspaceSpecFile(opts *workspaceOptions, path string, memoryExplicit, cpusExplicit, sizeExplicit bool) error {
	return workspace.ApplySpecFile(opts, path, workspace.SpecApplyOptions{
		MemoryExplicit: memoryExplicit,
		CPUExplicit:    cpusExplicit,
		SizeExplicit:   sizeExplicit,
	})
}

func readWorkspaceSpec(path string) (workspaceSpec, error) {
	return workspace.ReadSpec(path)
}

func setupCommandsFromFiles(files []string, baseDir string) ([]string, error) {
	return workspace.SetupCommandsFromFiles(files, baseDir)
}

func parseWorkspaceOutputs(values []string) ([]workspaceOutput, error) {
	outputs := make([]workspaceOutput, 0, len(values))
	for _, raw := range values {
		left, path, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("output must be name=/guest/path")
		}
		output := workspaceOutput{Name: strings.TrimSpace(left), Path: strings.TrimSpace(path)}
		if err := validateWorkspaceOutput(output); err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return validateWorkspaceOutputs(outputs)
}

func validateWorkspaceOutputs(outputs []workspaceOutput) ([]workspaceOutput, error) {
	return workspace.ValidateOutputs(outputs)
}

func validateWorkspaceOutput(output workspaceOutput) error {
	if strings.TrimSpace(output.Name) == "" {
		return fmt.Errorf("output name is required")
	}
	if strings.TrimSpace(output.Path) == "" {
		return fmt.Errorf("output %q path is required", output.Name)
	}
	if !strings.HasPrefix(output.Path, "/") {
		return fmt.Errorf("output %q path must be absolute", output.Name)
	}
	return nil
}

func mergeEnv(base, overrides map[string]string) map[string]string {
	return workspace.MergeEnv(base, overrides)
}

func applyResourceProfile(opts *workspaceOptions, memoryExplicit, cpusExplicit, sizeExplicit bool) error {
	return workspace.ApplyProfile(opts, memoryExplicit, cpusExplicit, sizeExplicit)
}

func lookupResourceProfile(name string) (resourceProfile, bool) {
	return workspace.LookupProfile(name)
}

func workspaceResources(opts workspaceOptions) resourceConfig {
	return workspace.ResourcesFromOptions(opts)
}

func validateResourceConfig(resources resourceConfig, requireDisk bool) error {
	return workspace.ValidateResources(resources, requireDisk)
}

func validateRestartPolicy(policy string) error {
	return workspace.ValidateRestartPolicy(policy)
}

func validateConsoleShell(shellPath string) error {
	shellPath = strings.TrimSpace(shellPath)
	if shellPath == "" {
		return nil
	}
	if !path.IsAbs(shellPath) {
		return fmt.Errorf("shell must be an absolute guest path")
	}
	if path.Clean(shellPath) != shellPath {
		return fmt.Errorf("shell must be a clean absolute guest path")
	}
	return nil
}

func validateHostname(hostname string) error {
	if strings.TrimSpace(hostname) == "" {
		return nil
	}
	return workspace.ValidateHostname(strings.TrimSpace(hostname))
}

func normalizeRestartPolicy(policy string) string {
	return workspace.NormalizeRestartPolicy(policy)
}

func canUseImageBaseline(opts workspaceOptions) bool {
	return opts.PrepareForStart &&
		!workspaceHasGuestCommand(opts) &&
		strings.TrimSpace(opts.ConsoleShell) == "" &&
		strings.TrimSpace(opts.Hostname) == "" &&
		len(opts.Files) == 0 &&
		len(opts.Disks) == 0 &&
		len(opts.Env) == 0 &&
		len(opts.Network.PortForwards) == 0
}

func normalizeNetworkConfig(network vmkit.NetworkConfig) vmkit.NetworkConfig {
	return workspace.NormalizeNetworkConfig(network)
}

func networkSpecFromConfig(network vmkit.NetworkConfig) networkSpec {
	return workspace.NetworkSpecFromConfig(network)
}

func workspaceArtifactsFromOptions(opts workspaceOptions) workspaceArtifacts {
	return workspace.ArtifactsFromOptions(opts)
}

func createWorkspaceRootfs(ctx context.Context, opts workspaceOptions) (workspaceResult, error) {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	rootfsPath := filepath.Join(workspaceDir, "rootfs.ext4")
	if canUseImageBaseline(opts) {
		if record, err := imagecache.Find(opts.StateDir, opts.ImageRef, rootfs.Platform{OS: "linux", Architecture: opts.Architecture}); err == nil {
			if err := workspace.CopyFile(record.OutputPath, rootfsPath, 0o644); err != nil {
				return workspaceResult{}, err
			}
			return workspaceResult{
				Workspace:    opts.Name,
				StateDir:     opts.StateDir,
				Profile:      opts.Profile,
				Restart:      opts.RestartPolicy,
				Resources:    workspaceResources(opts),
				Network:      networkSpecFromConfig(opts.Network),
				ConsoleShell: strings.TrimSpace(opts.ConsoleShell),
				Hostname:     strings.TrimSpace(opts.Hostname),
				RootfsPath:   rootfsPath,
				KernelPath:   opts.KernelPath,
				Artifacts:    workspaceArtifactsFromOptions(opts),
				Image:        imagecache.Provenance(record, rootfsPath),
			}, nil
		}
	}
	command, resultPort := workspaceBuildCommandAndPort(opts)
	mode := ""
	if opts.PrepareForStart && opts.UseImageCommand {
		mode = "service"
	} else if opts.PrepareForStart && strings.TrimSpace(opts.ServiceCommand) != "" && !workspace.HasSetupCommand(opts) && strings.TrimSpace(opts.ExecCommand) == "" {
		mode = "managed-service"
	}
	finalCommand, finalMode, resetFinal := workspace.FinalCommandAndMode(opts)
	req := rootfs.BuildRequest{
		ImageRef:         opts.ImageRef,
		Platform:         rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:       rootfsPath,
		InitPath:         rootfs.DefaultInitPath,
		Command:          command,
		Mode:             mode,
		ConsoleShell:     opts.ConsoleShell,
		Hostname:         opts.Hostname,
		ShellPort:        workspace.ShellPort(opts),
		ExecPort:         workspace.ExecPort(opts),
		InitBinaryPath:   opts.GuestInitPath,
		ResultPort:       resultPort,
		NoImageCommand:   opts.PrepareForStart && !workspaceHasGuestCommand(opts) && !opts.UseImageCommand,
		StateDir:         filepath.Join(opts.StateDir, "build"),
		Mke2fsPath:       opts.Mke2fsPath,
		SizeMiB:          opts.SizeMiB,
		Env:              opts.Env,
		Files:            workspace.RootfsFiles(opts.Files),
		Mounts:           workspaceMounts(opts.Disks),
		HostForwards:     rootfsPortForwards(opts.Network.PortForwards),
		AllowMutable:     true,
		Progress:         opts.Progress,
		ResetFinalConfig: resetFinal,
		FinalCommand:     finalCommand,
		FinalMode:        finalMode,
	}
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	result := workspaceResult{
		Workspace:    opts.Name,
		StateDir:     opts.StateDir,
		Profile:      opts.Profile,
		Restart:      opts.RestartPolicy,
		Resources:    workspaceResources(opts),
		Network:      networkSpecFromConfig(opts.Network),
		ConsoleShell: strings.TrimSpace(opts.ConsoleShell),
		Hostname:     strings.TrimSpace(opts.Hostname),
		RootfsPath:   rootfsPath,
		KernelPath:   opts.KernelPath,
		Artifacts:    workspaceArtifactsFromOptions(opts),
		Image:        provenance,
	}
	if err != nil {
		return result, err
	}
	if err := imagecache.RecordProvenance(opts.StateDir, provenance); err != nil {
		return result, err
	}
	return result, nil
}

func workspaceMounts(disks []workspaceDisk) []rootfs.Mount {
	return workspace.Mounts(disks)
}

func rootfsPortForwards(forwards []vmkit.PortForward) []rootfs.PortForward {
	return workspace.RootfsPortForwards(forwards)
}

func writeWorkspaceManifest(opts workspaceOptions) error {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(workspaceDir, "workspace.json"), workspaceManifest{
		Name:           opts.Name,
		Profile:        opts.Profile,
		Restart:        normalizeRestartPolicy(opts.RestartPolicy),
		Resources:      workspaceResources(opts),
		Network:        networkSpecFromConfig(opts.Network),
		Service:        strings.TrimSpace(opts.ServiceCommand),
		Model:          strings.TrimSpace(opts.Model),
		ModelRunner:    workspaceModelRunnerManifest(opts.ModelRunner),
		ModelMediation: workspaceModelMediationManifest(opts.ModelMediation),
		Mediation:      opts.Mediation,
		Disks:          opts.Disks,
		Artifacts:      workspaceArtifactsFromOptions(opts),
		Verification:   opts.Verification,
	})
}

func workspaceModelRunnerManifest(spec workspace.ModelRunnerSpec) *workspace.ModelRunnerSpec {
	if strings.TrimSpace(spec.Backend) == "" &&
		strings.TrimSpace(spec.GPU) == "" &&
		strings.TrimSpace(spec.BackendModel) == "" &&
		strings.TrimSpace(spec.ServedModel) == "" &&
		len(spec.Command) == 0 &&
		strings.TrimSpace(spec.Name) == "" &&
		strings.TrimSpace(spec.HealthPath) == "" &&
		len(spec.Args) == 0 {
		return nil
	}
	spec.Env = nil
	return &spec
}

func workspaceModelMediationManifest(spec workspace.ModelMediationSpec) *workspace.ModelMediationSpec {
	if strings.TrimSpace(spec.Mode) == "" &&
		strings.TrimSpace(spec.PolicyURL) == "" &&
		strings.TrimSpace(spec.PolicyFile) == "" &&
		strings.TrimSpace(spec.PolicyTimeout) == "" {
		return nil
	}
	return &spec
}

func readWorkspaceManifest(stateDir, name string) (workspaceManifest, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "workspaces", name, "workspace.json"))
	if err != nil {
		return workspaceManifest{}, err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return workspaceManifest{}, err
	}
	return manifest, nil
}

func workspaceRequest(opts workspaceOptions, command, rootfsPath string) (vmkit.Request, error) {
	return workspace.Request(opts, command, rootfsPath, newRequestID())
}

func workspaceOptionsFromRequest(req vmkit.Request, supervisorPath string) (workspaceOptions, error) {
	return workspace.OptionsFromRequest(req, supervisorPath)
}

func workspaceSupervisor(opts workspaceOptions) (vmkit.Supervisor, error) {
	return workspace.Supervisor(opts)
}

func dispatchWorkspaceRequest(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	return workspace.Dispatch(ctx, opts, req)
}

func readWorkspaceRuntimeState(opts workspaceOptions) (workspaceRuntimeState, error) {
	var state workspaceRuntimeState
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "runtime.json"))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func readWorkspaceEvent(opts workspaceOptions) (workspaceEventFile, error) {
	var event workspaceEventFile
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "event.json"))
	if err != nil {
		return event, err
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return event, err
	}
	return event, nil
}

func buildWorkspaceVerification(opts workspaceOptions, result workspaceResult) (vmkit.RuntimeVerification, error) {
	verification := vmkit.RuntimeVerification{
		OK:          true,
		ImageRef:    result.Image.ImageRef,
		ResolvedRef: result.Image.ResolvedRef,
		ImageDigest: result.Image.Digest,
		Kernel:      recordedArtifact(opts.KernelPath),
		Rootfs:      recordedArtifact(result.RootfsPath),
	}
	if opts.GuestInitPath != "" {
		if info, err := os.Stat(opts.GuestInitPath); err == nil && !info.IsDir() {
			verification.Init = recordedArtifact(opts.GuestInitPath)
		}
	}
	for _, artifact := range []struct {
		name     string
		artifact *vmkit.VerifiedArtifact
	}{
		{name: "kernel", artifact: verification.Kernel},
		{name: "rootfs", artifact: verification.Rootfs},
		{name: "init", artifact: verification.Init},
	} {
		if artifact.artifact != nil && artifact.artifact.Error != "" {
			verification.OK = false
			verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{
				Artifact: artifact.name,
				Error:    artifact.artifact.Error,
			})
		}
	}
	if !verification.OK {
		return verification, fmt.Errorf("record workspace verification: %s", verification.Divergence[0].Error)
	}
	return verification, nil
}

func recordedArtifact(path string) *vmkit.VerifiedArtifact {
	artifact := &vmkit.VerifiedArtifact{Path: path}
	if strings.TrimSpace(path) == "" {
		artifact.Error = "path is empty"
		return artifact
	}
	sum, err := workspace.FileSHA256(path)
	if err != nil {
		artifact.Error = err.Error()
		return artifact
	}
	artifact.SHA256 = sum
	return artifact
}

func liveReadinessUnavailableSignal(state vmkit.VMState, observedAt *time.Time) *vmkit.ReadinessSignal {
	if !liveWorkspaceUnavailableState(state) {
		return nil
	}
	return &vmkit.ReadinessSignal{
		Ready:      false,
		ObservedAt: observedAt,
		Detail:     fmt.Sprintf("workspace is %s; live readiness unavailable", state),
	}
}

func liveWorkspaceUnavailableState(state vmkit.VMState) bool {
	return state == vmkit.StatePrepared || state == vmkit.StateHalted || state == vmkit.StateStopped || state == vmkit.StateQuarantined || state == vmkit.StateFailed
}

func workspaceReadinessFromRuntime(state workspaceRuntimeState) vmkit.RuntimeReadiness {
	readiness := vmkit.RuntimeReadiness{}
	if state.StartedAt != "" || state.Event.State == vmkit.StateRunning || state.Event.State == vmkit.StateHalted || state.Event.State == vmkit.StateStopped || state.Event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: firstTime(state.StartedAt, state.Event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(state.Event.State),
		}
	}
	liveUnavailable := liveReadinessUnavailableSignal(state.Event.State, firstTime(state.StartedAt, state.Event.ObservedAt))
	if liveUnavailable != nil {
		readiness.ShellReady = *liveUnavailable
		readiness.ExecReady = *liveUnavailable
		readiness.ResultReady = *liveUnavailable
		if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
			readiness.MediationReady = *liveUnavailable
		}
	}
	if state.Event.State == vmkit.StateRunning && state.SerialInputPath != "" {
		if signal, ok := workspaceShellReadinessFromRuntime(state); ok {
			readiness.ShellReady = signal
		}
	}
	if signal, ok := workspace.ExecReadinessSignal(context.Background(), state, workspace.ExecReadyProbeTimeout); ok {
		readiness.ExecReady = signal
	}
	path := resultPath(workspaceOptions{StateDir: state.Config.StateDir, Name: state.Event.Identity.RuntimeID})
	if _, err := os.Stat(path); err == nil {
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: fileModTime(path),
			Detail:     "guest result is available",
		}
	} else if !os.IsNotExist(err) {
		readiness.ResultReady = vmkit.ReadinessSignal{Error: err.Error()}
	}
	if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
		readiness.MediationReady = vmkit.MediationReadinessSignal(context.Background(), *state.Config.Mediation, state.Event.State, firstTime(state.StartedAt, state.Event.ObservedAt), 150*time.Millisecond)
	}
	return readiness
}

func workspaceShellReadinessFromRuntime(state workspaceRuntimeState) (vmkit.ReadinessSignal, bool) {
	return workspace.ShellReadinessSignalWithMode(context.Background(), state, time.Second, workspace.ShellReadinessProbeCommand)
}

func firstTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed := parseOptionalTime(value); parsed != nil {
			return parsed
		}
	}
	return nil
}

func parseOptionalTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func fileModTime(path string) *time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mod := info.ModTime().UTC()
	return &mod
}

func writeWorkspaceProcessState(opts workspaceOptions, req vmkit.Request, state vmkit.VMState, pid int, errorText string) error {
	if req.Identity == nil || req.Config == nil {
		return fmt.Errorf("workspace request is missing identity or config")
	}
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	event := vmkit.Event{
		Identity:   *req.Identity,
		State:      state,
		Detail:     "serial=" + serialLogPath(opts),
		ObservedAt: time.Now().UTC(),
	}
	fileEvent := workspaceEventFile{
		Identity:   *req.Identity,
		State:      state,
		Detail:     event.Detail,
		ObservedAt: event.ObservedAt.Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "event.json"), fileEvent); err != nil {
		return err
	}
	if err := appendWorkspaceEvent(filepath.Join(dir, "events.json"), fileEvent); err != nil {
		return err
	}
	updatedAt := time.Now().UTC()
	runtimeState := workspaceRuntimeState{
		Event:           fileEvent,
		Config:          *req.Config,
		PID:             pid,
		SerialLogPath:   serialLogPath(opts),
		SerialInputPath: serialInputPath(opts.StateDir, opts.Name),
		UpdatedAt:       updatedAt.Format(time.RFC3339),
		Error:           errorText,
	}
	if state == vmkit.StateStarting || state == vmkit.StateRunning || state == vmkit.StateQuarantined {
		runtimeState.StartedAt = updatedAt.Format(time.RFC3339)
	}
	runtimeState.Readiness = workspaceReadinessFromRuntime(runtimeState)
	return writeJSONFile(filepath.Join(dir, "runtime.json"), runtimeState)
}

func appendWorkspaceEvent(path string, event workspaceEventFile) error {
	const maxEvents = 1024
	var events []workspaceEventFile
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && len(bytes.TrimSpace(data)) != 0 {
		if err := json.Unmarshal(data, &events); err != nil {
			return err
		}
	}
	events = append(events, event)
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	return writeJSONFile(path, events)
}

type workspaceEventFile = workspace.EventFile
type workspaceRuntimeState = workspace.RuntimeState

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func serialLogPath(opts workspaceOptions) string {
	return filepath.Join(opts.StateDir, opts.Name, "serial.log")
}

func serialInputPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "serial.in")
}

func resultPath(opts workspaceOptions) string {
	return filepath.Join(opts.StateDir, opts.Name, "result.json")
}

func cloneWorkspace(stateDir, source, target string) (workspaceResult, error) {
	sourceWorkspaceDir := filepath.Join(stateDir, "workspaces", source)
	targetWorkspaceDir := filepath.Join(stateDir, "workspaces", target)
	if _, err := os.Stat(sourceWorkspaceDir); err != nil {
		return workspaceResult{}, err
	}
	if _, err := os.Stat(targetWorkspaceDir); err == nil {
		return workspaceResult{}, fmt.Errorf("target workspace %q already exists", target)
	} else if !os.IsNotExist(err) {
		return workspaceResult{}, err
	}
	if _, err := os.Stat(filepath.Join(stateDir, target)); err == nil {
		return workspaceResult{}, fmt.Errorf("target workspace state %q already exists", target)
	} else if !os.IsNotExist(err) {
		return workspaceResult{}, err
	}
	if err := ensureWorkspaceCloneable(stateDir, source); err != nil {
		return workspaceResult{}, err
	}
	manifest, err := readWorkspaceManifest(stateDir, source)
	if err != nil {
		return workspaceResult{}, err
	}
	if err := copyDirectory(sourceWorkspaceDir, targetWorkspaceDir); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return workspaceResult{}, err
	}
	manifest.Name = target
	manifest.Disks = rewriteClonedDiskPaths(manifest.Disks, sourceWorkspaceDir, targetWorkspaceDir)
	if err := writeJSONFile(filepath.Join(targetWorkspaceDir, "workspace.json"), manifest); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return workspaceResult{}, err
	}
	event := workspaceEventFile{
		Identity: vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: target,
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		State:      vmkit.StatePrepared,
		Detail:     "cloned_from=" + source,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(filepath.Join(stateDir, target), 0o700); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return workspaceResult{}, err
	}
	if err := writeJSONFile(filepath.Join(stateDir, target, "event.json"), event); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		_ = os.RemoveAll(filepath.Join(stateDir, target))
		return workspaceResult{}, err
	}
	return workspaceResult{
		Workspace:  target,
		StateDir:   stateDir,
		Profile:    manifest.Profile,
		Restart:    nonEmpty(manifest.Restart, defaultRestartPolicy),
		Resources:  manifest.Resources,
		Network:    manifest.Network,
		RootfsPath: filepath.Join(targetWorkspaceDir, "rootfs.ext4"),
		Disks:      manifest.Disks,
		Response: vmkit.Response{
			OK:      true,
			Backend: event.Identity.Backend,
			Event: &vmkit.Event{
				Identity:   event.Identity,
				State:      event.State,
				Detail:     event.Detail,
				ObservedAt: time.Now().UTC(),
			},
		},
	}, nil
}

func ensureWorkspaceCloneable(stateDir, name string) error {
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: stateDir, Name: name})
	if os.IsNotExist(err) {
		event, eventErr := readWorkspaceEvent(workspaceOptions{StateDir: stateDir, Name: name})
		if os.IsNotExist(eventErr) {
			return nil
		}
		if eventErr != nil {
			return eventErr
		}
		return cloneableState(name, event.State)
	}
	if err != nil {
		event, eventErr := readWorkspaceEvent(workspaceOptions{StateDir: stateDir, Name: name})
		if os.IsNotExist(eventErr) {
			return err
		}
		if eventErr != nil {
			return err
		}
		return cloneableState(name, event.State)
	}
	return cloneableState(name, state.Event.State)
}

func cloneableState(name string, state vmkit.VMState) error {
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped:
		return nil
	default:
		return fmt.Errorf("workspace %s must be stopped before cloning; current state is %s", name, state)
	}
}

func rewriteClonedDiskPaths(disks []workspaceDisk, sourceWorkspaceDir, targetWorkspaceDir string) []workspaceDisk {
	if len(disks) == 0 {
		return nil
	}
	out := make([]workspaceDisk, 0, len(disks))
	sourceWorkspaceDir = filepath.Clean(sourceWorkspaceDir)
	for _, disk := range disks {
		if rel, err := filepath.Rel(sourceWorkspaceDir, disk.Path); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			disk.Path = filepath.Join(targetWorkspaceDir, rel)
		}
		out = append(out, disk)
	}
	return out
}

func copyDirectory(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		if info.Mode()&os.ModeType != 0 {
			return fmt.Errorf("cannot clone special file %s", path)
		}
		return copyFile(path, targetPath, info.Mode().Perm())
	})
}

func copyFile(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func validateWorkspaceName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("workspace name is required")
	}
	if !vmkit.SafeIdentifier(name) {
		return fmt.Errorf("invalid workspace name: %s", name)
	}
	return nil
}

func validateSafeBasename(field, value string) error {
	if !vmkit.SafeIdentifier(value) {
		return fmt.Errorf("invalid %s: %s", field, value)
	}
	return nil
}

func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "help" || arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func shouldUseHighLevelCreate(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if wantsHelp(args) {
		return true
	}
	if hasFlagValue(args, "dry-run") && !hasFlagValue(args, "rootfs") && !hasFlagValue(args, "json") && !hasFlagValue(args, "vsock") {
		return true
	}
	if hasLowLevelCreateFlag(args) {
		return false
	}
	if hasFlagValue(args, "image") || hasPositionalWorkspaceName(args) {
		return true
	}
	return hasFlagValue(args, "file") || hasFlagValue(args, "name") || hasFlagValue(args, "id") || hasFlagValue(args, "setup") || hasFlagValue(args, "setup-file") || hasFlagValue(args, "entrypoint") || hasFlagValue(args, "shell") || hasFlagValue(args, "hostname") || hasFlagValue(args, "env") || hasFlagValue(args, "e") || hasFlagValue(args, "disk") || hasFlagValue(args, "bundle") || hasFlagValue(args, "volume") || hasFlagValue(args, "v") || hasFlagValue(args, "output")
}

func hasLowLevelCreateFlag(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--rootfs", "-rootfs", "--json", "-json", "--dry-run", "-dry-run", "--request-id", "-request-id", "--role", "-role", "--vsock", "-vsock":
			return true
		}
		if strings.HasPrefix(arg, "--rootfs=") ||
			strings.HasPrefix(arg, "-rootfs=") ||
			strings.HasPrefix(arg, "--json=") ||
			strings.HasPrefix(arg, "-json=") ||
			strings.HasPrefix(arg, "--request-id=") ||
			strings.HasPrefix(arg, "-request-id=") ||
			strings.HasPrefix(arg, "--role=") ||
			strings.HasPrefix(arg, "-role=") ||
			strings.HasPrefix(arg, "--vsock=") ||
			strings.HasPrefix(arg, "-vsock=") {
			return true
		}
	}
	return false
}

func hostBackend() string {
	return workspace.HostBackend()
}

func defaultGuestArch() string {
	return workspace.GuestArch()
}

func defaultWorkspaceImage(arch string) string {
	return workspace.DefaultImage(arch)
}

func defaultStateDir() string {
	return workspace.StateDir()
}

func defaultKernelPath(backend, arch string) string {
	return workspace.KernelPath(backend, arch)
}

func defaultAppleVFSupervisorPath() string {
	return workspace.AppleVFSupervisorPath()
}

func defaultSupervisorPath(backend string) string {
	if backend == vmkit.BackendAppleVF {
		return defaultAppleVFSupervisorPath()
	}
	return ""
}

func defaultPackagedKernelPathFromExecutable(executable, backend, arch string) string {
	return workspace.PackagedKernelPathFromExecutable(executable, backend, arch)
}

func defaultMke2fsPath() string {
	return workspace.Mke2fsPath()
}

func defaultDebugFSPath() string {
	if path, err := exec.LookPath("debugfs"); err == nil {
		return path
	}
	if _, err := os.Stat("/opt/homebrew/opt/e2fsprogs/sbin/debugfs"); err == nil {
		return "/opt/homebrew/opt/e2fsprogs/sbin/debugfs"
	}
	return "debugfs"
}

func defaultGuestInitPath(arch string) string {
	return workspace.GuestInitPath(arch)
}

func defaultGuestInitPathFromExecutable(executable, arch string) string {
	return workspace.GuestInitPathFromExecutable(executable, arch)
}

func hasFlagValue(args []string, name string) bool {
	_, ok := flagValue(args, name)
	return ok
}

func flagValue(args []string, name string) (string, bool) {
	long := "--" + name
	short := "-" + name
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == long || arg == short {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(arg, long+"=") {
			return strings.TrimPrefix(arg, long+"="), true
		}
		if strings.HasPrefix(arg, short+"=") {
			return strings.TrimPrefix(arg, short+"="), true
		}
	}
	return "", false
}

func hasPositionalWorkspaceName(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			return true
		}
		if arg == "--json" || arg == "-json" || arg == "--rootfs" || arg == "-rootfs" || arg == "--kernel" || arg == "-kernel" || arg == "--name" || arg == "-name" || arg == "--id" || arg == "-id" || arg == "--file" || arg == "-file" || arg == "--entrypoint" || arg == "-entrypoint" || arg == "--shell" || arg == "-shell" || arg == "--hostname" || arg == "-hostname" || arg == "--env" || arg == "-env" {
			return false
		}
	}
	return false
}

func hasWorkspaceStateTarget(args []string) bool {
	for i, arg := range args {
		if arg == "--json" || arg == "-json" {
			return false
		}
		if arg == "--name" || arg == "-name" || arg == "--id" || arg == "-id" {
			return i+1 < len(args)
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return true
	}
	return false
}

func workspaceCommand(opts workspaceOptions) string {
	return workspace.Command(opts)
}

func workspaceBuildCommandAndPort(opts workspaceOptions) ([]string, uint32) {
	return workspace.BuildCommandAndPort(opts)
}

func shellSingleQuote(value string) string {
	return workspace.ShellSingleQuote(value)
}

func workspaceHasGuestCommand(opts workspaceOptions) bool {
	return workspace.HasGuestCommand(opts)
}

func requestFromFlagsOrJSON(jsonPath string, args []string, identity vmkit.Identity, config vmkit.Config, disks []string, vsocks []string, networkMode string, publishes []string) (vmkit.Request, error) {
	if jsonPath != "" {
		if len(args) != 0 {
			return vmkit.Request{}, fmt.Errorf("--json does not accept positional request paths")
		}
		return readRequest(jsonPath)
	}
	if len(args) != 0 {
		return vmkit.Request{}, fmt.Errorf("unexpected argument: %s", args[0])
	}
	if identity.RequestID == "" {
		identity.RequestID = newRequestID()
	}
	listeners, err := parseVsockMappings(vsocks)
	if err != nil {
		return vmkit.Request{}, err
	}
	parsedDisks, err := parseWorkspaceDisks(disks, false)
	if err != nil {
		return vmkit.Request{}, err
	}
	portForwards, err := parsePortForwardMappings(publishes)
	if err != nil {
		return vmkit.Request{}, err
	}
	for _, disk := range parsedDisks {
		config.Disks = append(config.Disks, vmkit.Disk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	config.VsockListeners = listeners
	network := normalizeNetworkConfig(vmkit.NetworkConfig{Mode: networkMode, PortForwards: portForwards})
	if err := vmkit.ValidateNetworkConfig(network); err != nil {
		return vmkit.Request{}, err
	}
	config.Network = &network
	return vmkit.Request{Identity: &identity, Config: &config}, nil
}

func stateRequestFromFlagsOrJSON(command, jsonPath string, args []string, identity vmkit.Identity, config vmkit.Config) (vmkit.Request, error) {
	if jsonPath != "" {
		if len(args) != 0 {
			return vmkit.Request{}, fmt.Errorf("--json does not accept positional request paths")
		}
		return readRequest(jsonPath)
	}
	if len(args) > 1 {
		return vmkit.Request{}, fmt.Errorf("usage: microagent %s [id] --state-dir <dir>", command)
	}
	if len(args) == 1 && identity.RuntimeID == "" {
		identity.RuntimeID = args[0]
	}
	if identity.RequestID == "" {
		identity.RequestID = newRequestID()
	}
	return vmkit.Request{Identity: &identity, Config: &config}, nil
}

func reorderFlagArgs(args []string) []string {
	valueFlags := map[string]bool{
		"-supervisor":                true,
		"-json":                      true,
		"-id":                        true,
		"-name":                      true,
		"-image":                     true,
		"-exec":                      true,
		"-setup-file":                true,
		"-service-command":           true,
		"-entrypoint":                true,
		"-shell":                     true,
		"-hostname":                  true,
		"-file":                      true,
		"-env":                       true,
		"-setup":                     true,
		"-request-id":                true,
		"-role":                      true,
		"-backend":                   true,
		"-kernel":                    true,
		"-rootfs":                    true,
		"-disk":                      true,
		"-bundle":                    true,
		"-volume":                    true,
		"-v":                         true,
		"-output":                    true,
		"-debugfs":                   true,
		"-profile":                   true,
		"-restart":                   true,
		"-network":                   true,
		"-mediation":                 true,
		"-publish":                   true,
		"-p":                         true,
		"-state-dir":                 true,
		"-tag":                       true,
		"-provider":                  true,
		"-dir":                       true,
		"-subnet":                    true,
		"-from-snapshot":             true,
		"-url":                       true,
		"-from":                      true,
		"-sha256":                    true,
		"-out":                       true,
		"-path":                      true,
		"-memory":                    true,
		"-cpus":                      true,
		"-vsock":                     true,
		"-mke2fs":                    true,
		"-guest-init":                true,
		"-arch":                      true,
		"-size-mib":                  true,
		"-timeout":                   true,
		"-ttl":                       true,
		"-ready-timeout":             true,
		"-duration":                  true,
		"-interval":                  true,
		"-max-restarts":              true,
		"-result-port":               true,
		"-send":                      true,
		"-e":                         true,
		"-model":                     true,
		"-model-token":               true,
		"-model-runner":              true,
		"-model-gpu":                 true,
		"-model-runner-model":        true,
		"-model-runner-served-model": true,
		"-model-runner-command":      true,
		"-model-runner-name":         true,
		"-model-runner-health-path":  true,
		"-model-runner-arg":          true,
		"-model-runner-env":          true,
		"-model-mediation":           true,
		"-model-policy-url":          true,
		"-model-policy-file":         true,
		"-model-policy-timeout":      true,
		"-runner":                    true,
		"-runner-gpu":                true,
		"-runner-model":              true,
		"-runner-served-model":       true,
		"-runner-command":            true,
		"-runner-name":               true,
		"-runner-health-path":        true,
		"-runner-arg":                true,
		"-runner-env":                true,
		"-method":                    true,
		"-workspace-id":              true,
		"-capability":                true,
		"-worker-id":                 true,
		"-request-bytes":             true,
		"-text-bytes":                true,
		"-messages":                  true,
		"-max-tokens":                true,
		"-stream":                    true,
		"-tool":                      true,
		"-expect":                    true,
		"-secret":                    true,
		"-secrets-env-file":          true,
		"-secret-on-demand":          true,
		"-egress":                    true,
		"-egress-allow":              true,
		"-egress-passthrough":        true,
		"-egress-policy":             true,
		"-egress-swap-config":        true,
		"-cred-swap":                 true,
	}
	return reorderArgs(args, func(name string) bool { return valueFlags[name] }, isBoolReorderFlag)
}

// reorderArgs hoists recognized flags ahead of positionals so a FlagSet stops at the
// first positional rather than at an interleaved flag. isValueFlag/isBoolFlag report
// which flag names the CALLER's FlagSet knows — a command must pass only its OWN
// flags, never a global set, or it will lift a guest/positional command's flags (e.g.
// `run <image> id -u`) out of the tail and misparse them.
func reorderArgs(args []string, isValueFlag, isBoolFlag func(string) bool) []string {
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			arg = "-" + strings.TrimPrefix(arg, "--")
		}
		flagName := arg
		if name, _, ok := strings.Cut(arg, "="); ok {
			flagName = name
		}
		if strings.Contains(arg, "=") {
			positional = append(positional, args[i])
			continue
		}
		if !isValueFlag(flagName) && !isBoolFlag(flagName) {
			positional = append(positional, args[i])
			continue
		}
		flags = append(flags, arg)
		if isValueFlag(flagName) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

func isBoolReorderFlag(name string) bool {
	switch name {
	case "-json", "-text", "-human", "-keep", "-rm", "-dry-run", "-image-command", "-mediation-optional", "-secrets-audit", "-unsupported", "-delete", "-yes", "-y", "-force", "-f", "-follow", "-images", "-install", "-uninstall", "-push":
		return true
	default:
		return false
	}
}

func readRequest(path string) (vmkit.Request, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return vmkit.Request{}, err
	}
	var req vmkit.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return vmkit.Request{}, err
	}
	return req, nil
}

func mapCLICommand(command string) string {
	if command == "status" {
		return "inspect"
	}
	return command
}

func parseVsock(raw string) (vmkit.VsockListener, error) {
	left, right, ok := strings.Cut(raw, "=")
	if !ok {
		return vmkit.VsockListener{}, fmt.Errorf("vsock mapping must be port=host:port")
	}
	port, err := strconv.ParseUint(left, 10, 32)
	if err != nil || port == 0 {
		return vmkit.VsockListener{}, fmt.Errorf("vsock port must be a positive uint32")
	}
	if strings.TrimSpace(right) == "" {
		return vmkit.VsockListener{}, fmt.Errorf("vsock target is required")
	}
	return vmkit.VsockListener{Port: uint32(port), Target: right}, nil
}

func parseVsockMappings(raw []string) ([]vmkit.VsockListener, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	listeners := make([]vmkit.VsockListener, 0, len(raw))
	for _, entry := range raw {
		listener, err := parseVsock(entry)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func parseMediationMapping(raw string, optional bool) (vmkit.MediationConfig, error) {
	listener, err := parseVsock(raw)
	if err != nil {
		return vmkit.MediationConfig{}, fmt.Errorf("mediation %w", err)
	}
	mediation := vmkit.MediationConfig{
		Enabled:    true,
		Required:   !optional,
		Port:       listener.Port,
		Target:     listener.Target,
		FailClosed: !optional,
	}
	if err := vmkit.ValidateMediationConfig(mediation); err != nil {
		return vmkit.MediationConfig{}, err
	}
	return mediation, nil
}

func parsePortForward(raw string) (vmkit.PortForward, error) {
	protocol := "tcp"
	if before, after, ok := strings.Cut(raw, "/"); ok {
		raw = before
		protocol = strings.TrimSpace(after)
	}
	parts := strings.Split(raw, ":")
	var host string
	var hostPortText string
	var guestPortText string
	switch len(parts) {
	case 2:
		hostPortText = parts[0]
		guestPortText = parts[1]
	case 3:
		host = parts[0]
		hostPortText = parts[1]
		guestPortText = parts[2]
	default:
		return vmkit.PortForward{}, fmt.Errorf("publish mapping must be [host:]hostPort:guestPort[/tcp]")
	}
	hostPort, err := strconv.ParseUint(strings.TrimSpace(hostPortText), 10, 16)
	if err != nil || hostPort == 0 {
		return vmkit.PortForward{}, fmt.Errorf("publish host port must be a positive uint16")
	}
	guestPort, err := strconv.ParseUint(strings.TrimSpace(guestPortText), 10, 16)
	if err != nil || guestPort == 0 {
		return vmkit.PortForward{}, fmt.Errorf("publish guest port must be a positive uint16")
	}
	forward := vmkit.PortForward{
		Protocol:  protocol,
		Host:      strings.TrimSpace(host),
		HostPort:  uint16(hostPort),
		GuestPort: uint16(guestPort),
	}
	if err := vmkit.ValidateNetworkConfig(vmkit.NetworkConfig{Mode: defaultNetworkMode, PortForwards: []vmkit.PortForward{forward}}); err != nil {
		return vmkit.PortForward{}, err
	}
	return normalizeNetworkConfig(vmkit.NetworkConfig{PortForwards: []vmkit.PortForward{forward}}).PortForwards[0], nil
}

func parsePortForwardMappings(raw []string) ([]vmkit.PortForward, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	forwards := make([]vmkit.PortForward, 0, len(raw))
	seen := map[string]bool{}
	for _, entry := range raw {
		forward, err := parsePortForward(entry)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%s/%d", forward.Protocol, forward.Host, forward.HostPort)
		if seen[key] {
			return nil, fmt.Errorf("duplicate published host port %s", entry)
		}
		seen[key] = true
		forwards = append(forwards, forward)
	}
	return forwards, nil
}

func parseWorkspaceDisks(values []string, bundle bool) ([]workspaceDisk, error) {
	if len(values) == 0 {
		return nil, nil
	}
	disks := make([]workspaceDisk, 0, len(values))
	for _, raw := range values {
		disk, err := parseWorkspaceDisk(raw, bundle)
		if err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func parseWorkspaceDisk(raw string, bundle bool) (workspaceDisk, error) {
	name, rest, ok := strings.Cut(raw, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return workspaceDisk{}, fmt.Errorf("disk must be name=path:/mount:ro|rw")
	}
	if name == "rootfs" {
		return workspaceDisk{}, fmt.Errorf("disk name rootfs is reserved")
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 3 {
		return workspaceDisk{}, fmt.Errorf("disk %q must be path:/mount:ro|rw", name)
	}
	mode := strings.TrimSpace(parts[len(parts)-1])
	mountpoint := strings.TrimSpace(parts[len(parts)-2])
	sourcePath := strings.TrimSpace(strings.Join(parts[:len(parts)-2], ":"))
	if sourcePath == "" {
		return workspaceDisk{}, fmt.Errorf("disk %q path is required", name)
	}
	if mountpoint == "" || !strings.HasPrefix(mountpoint, "/") {
		return workspaceDisk{}, fmt.Errorf("disk %q mountpoint must be absolute", name)
	}
	if mode != "ro" && mode != "rw" {
		return workspaceDisk{}, fmt.Errorf("disk %q mode must be ro or rw", name)
	}
	return workspaceDisk{
		Name:       name,
		SourcePath: sourcePath,
		Path:       sourcePath,
		Mountpoint: mountpoint,
		Mode:       mode,
		Bundle:     bundle,
	}, nil
}

func parseWorkspaceVolumes(values []string) ([]workspaceDisk, error) {
	if len(values) == 0 {
		return nil, nil
	}
	disks := make([]workspaceDisk, 0, len(values))
	for _, raw := range values {
		disk, err := parseWorkspaceVolume(raw)
		if err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func parseWorkspaceVolume(raw string) (workspaceDisk, error) {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 {
		return workspaceDisk{}, fmt.Errorf("volume must be SRC:DST[:ro|rw]")
	}
	// Parse from the right: an optional ro|rw mode, then the guest mountpoint.
	// The source may contain its own colon (a Windows drive-letter path such
	// as C:\data), so everything left of the destination is the source.
	mode := "rw"
	last := strings.TrimSpace(parts[len(parts)-1])
	switch {
	case last == "ro" || last == "rw":
		mode = last
		parts = parts[:len(parts)-1]
	case len(parts) >= 3 && !strings.HasPrefix(last, "/") && strings.HasPrefix(strings.TrimSpace(parts[len(parts)-2]), "/"):
		// SRC:/dst:<something> where <something> is not a guest path.
		return workspaceDisk{}, fmt.Errorf("volume mode must be ro or rw")
	}
	if len(parts) < 2 {
		return workspaceDisk{}, fmt.Errorf("volume must be SRC:DST[:ro|rw]")
	}
	mountpoint := strings.TrimSpace(parts[len(parts)-1])
	sourcePath := strings.TrimSpace(strings.Join(parts[:len(parts)-1], ":"))
	if sourcePath == "" {
		return workspaceDisk{}, fmt.Errorf("volume source path is required")
	}
	if mountpoint == "" || !strings.HasPrefix(mountpoint, "/") {
		return workspaceDisk{}, fmt.Errorf("volume destination must be an absolute guest path")
	}
	if mode != "ro" && mode != "rw" {
		return workspaceDisk{}, fmt.Errorf("volume mode must be ro or rw")
	}
	// A bare name (no path separator or extension, per volume.ValidName) refers
	// to a managed named volume, resolved to its backing ext4 disk at prepare
	// time. This is the in-boundary analog of a docker volume.
	if volume.ValidName(sourcePath) {
		return workspaceDisk{
			Name:          sourcePath,
			Mountpoint:    mountpoint,
			Mode:          mode,
			ManagedVolume: true,
		}, nil
	}
	if info, err := os.Stat(sourcePath); err == nil && info.IsDir() {
		return workspaceDisk{}, fmt.Errorf("MicroAgent does not expose host bind mounts yet; use --bundle with a tar archive, --disk with an ext4 image, or copy files with microagent cp")
	}
	name, err := volumeDiskName(mountpoint)
	if err != nil {
		return workspaceDisk{}, err
	}
	lower := strings.ToLower(sourcePath)
	switch {
	case strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return workspaceDisk{
			Name:       name,
			SourcePath: sourcePath,
			Path:       sourcePath,
			Mountpoint: mountpoint,
			Mode:       mode,
			Bundle:     true,
		}, nil
	case strings.HasSuffix(lower, ".ext4") || strings.HasSuffix(lower, ".img"):
		return workspaceDisk{
			Name:       name,
			SourcePath: sourcePath,
			Path:       sourcePath,
			Mountpoint: mountpoint,
			Mode:       mode,
			Bundle:     false,
		}, nil
	default:
		return workspaceDisk{}, fmt.Errorf("unsupported volume source %q; MicroAgent accepts a managed volume name, a tar archive bundle, or an ext4 disk image, not host bind mounts", sourcePath)
	}
}

func volumeDiskName(mountpoint string) (string, error) {
	name := path.Base(path.Clean(mountpoint))
	if name == "." || name == "/" {
		return "", fmt.Errorf("volume destination must include a mount directory name")
	}
	if name == "rootfs" {
		return "", fmt.Errorf("disk name rootfs is reserved")
	}
	if err := validateSafeBasename("volume-derived disk name", name); err != nil {
		return "", err
	}
	return name, nil
}

func rejectUnsupportedContainerCompatibilityFlags(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := splitFlagArg(arg)
		switch name {
		case "--privileged", "-privileged":
			return fmt.Errorf("--privileged is not supported; MicroAgent runs workloads inside a microVM boundary and does not expose privileged host/container mode")
		case "--pod", "-pod", "--pod-id-file", "-pod-id-file":
			return fmt.Errorf("%s is not supported; MicroAgent does not implement pods, so run one workspace per microVM and keep orchestration outside microagent", name)
		case "--mount", "-mount":
			if !hasValue && i+1 < len(args) {
				value = args[i+1]
			}
			if strings.Contains(value, "type=bind") || strings.Contains(value, "bind") {
				return fmt.Errorf("--mount type=bind is not supported; MicroAgent does not expose host bind mounts, so use -v with a tar archive or ext4 image, --bundle, --disk, microagent cp, or declared --output paths")
			}
			return fmt.Errorf("--mount is not supported; use -v SRC:DST[:ro|rw] with a tar archive or ext4 image, --bundle, or --disk")
		case "--cap-add", "-cap-add", "--cap-drop", "-cap-drop", "--security-opt", "-security-opt", "--device", "-device", "--pid", "-pid", "--ipc", "-ipc", "--userns", "-userns":
			return fmt.Errorf("%s is not supported; MicroAgent exposes a microVM boundary rather than namespace, capability, device, or security-opt controls", name)
		}
	}
	return nil
}

func splitFlagArg(arg string) (name, value string, hasValue bool) {
	if before, after, ok := strings.Cut(arg, "="); ok {
		return before, after, true
	}
	return arg, "", false
}

func newRequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func parseEnvFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("env must be KEY=VALUE: %s", raw)
		}
		if !validEnvName(key) {
			return nil, fmt.Errorf("env key is invalid: %s", key)
		}
		env[key] = value
	}
	return env, nil
}

func parseEgressMode(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "guarded":
		return vmkit.EgressModeGuarded, nil
	case "strict":
		return vmkit.EgressModeStrict, nil
	case "off", "open", "disabled":
		return vmkit.EgressModeOff, nil
	default:
		return "", fmt.Errorf("--egress must be guarded, strict, or off: %q", v)
	}
}

func parseSecretFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	secrets := make(map[string]string, len(values))
	for _, raw := range values {
		name, ref, ok := strings.Cut(raw, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("secret must be NAME=<scheme>:<ref>: %s", raw)
		}
		if !secretxfer.ValidName(name) {
			return nil, fmt.Errorf("secret name is invalid: %s", name)
		}
		if strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("secret reference is empty for %s", name)
		}
		if _, dup := secrets[name]; dup {
			return nil, fmt.Errorf("duplicate secret name: %s", name)
		}
		secrets[name] = ref
	}
	return secrets, nil
}

func validEnvName(key string) bool {
	for i, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z' && i > 0:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return key != ""
}

func printHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent

Usage:
  microagent run IMAGE [COMMAND ARG...]
  microagent create NAME --image IMAGE
  microagent start NAME
  microagent exec NAME -- CMD

Commands:
  run                  Run something once and discard state
  dispatch             Run one task in an isolated workspace; return result + egress audit
  create               Create a persistent workspace
  start                Boot a workspace
  exec                 Run a structured command in a workspace
  connect              Open the workspace console
  status               Show one workspace
  list, ls             List saved workspaces
  ps                   List running workspaces
  logs                 Show workspace logs
  halt                 Shut down cleanly and keep disk state
  delete               Delete a workspace
  doctor               Check whether this host can run microVMs

Resources:
  image                Manage reusable rootfs baselines
  volume               Manage named ext4 volumes
  network              Show workspace networking or manage named networks
  model                Manage local GGUF models and runners
  artifact             List or retrieve declared workspace artifacts
  secret check         Validate secret references
  registry             Store credentials for private OCI registries

More:
  microagent <command> --help
  microagent help all

Global options:
  --json               Print JSON output
  --text               Print human-readable output
  --mode <ux|ax>       Select human UX or agent AX output mode
`)
}

func printFullHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent

Commands:
  init                 Scaffold a starter agent project
  run                  Run a command
  dispatch             Run one task in a fresh isolated workspace; return result + egress audit
  create               Create a workspace
  apply                Apply supported workspace spec changes
  clone                Clone a stopped workspace
  commit               Snapshot a stopped workspace rootfs into an OCI image
  cp                   Copy files into or out of a stopped workspace
  artifact             List or retrieve declared workspace artifacts
  network              Inspect workspace network or manage named networks
  model                Pull or manage local HuggingFace model files
  volume               Manage named volumes (create, ls, inspect, rm)
  start                Start a workspace
  supervise            Run host restart supervision for a workspace
  connect              Open the workspace console
  exec                 Run a structured command in a workspace
  list, ls             List saved workspaces
  ps                   List running workspaces
  status               Show workspace state
  result               Show structured workspace result
  logs                 Show workspace logs
  events               Show or stream the lifecycle event history
  egress               Show or stream the egress mediator's audit decisions
  stats                Show or stream workspace resource usage
  snapshot             Create, list, or remove workspace snapshots
  secret check         Resolve and validate secret references
  registry             Store credentials for private OCI registries (login/logout/list)
  profiles             List resource profiles
  image                Manage local image records
  perf                 Measure workspace performance
  halt                 Halt a workspace and preserve disk state
  quarantine           Sever host-side network and mediation
  pause                Pause a running workspace, freezing vCPUs with memory and disk preserved
  resume               Resume a paused workspace
  stop                 Stop a workspace
  kill                 Force stop a workspace
  delete               Delete a workspace
  contract             Show backend-neutral runtime contract
  host                 Report host capabilities
  doctor               Check the host
  rootfs build         Build a rootfs from an OCI image
  version              Print the version
  help                 Show help

Advanced:
  kernel install       Install a custom kernel
  kernel verify        Verify a custom kernel

Options:
  --mode <ux|ax>        Select human UX or agent AX output mode
  --json                Print JSON output
  --text                Print human-readable output
  --output <json|text>  Select output format
  -supervisor <path>    Override the supervisor path
  -json <path|- >       Read request JSON from a file or stdin
  -image <ref>          OCI image
  -image-command        Run the image Entrypoint/Cmd instead of opening a shell
  -service-command <cmd> Long-running command to run as the VM service
  -name <name>          Workspace name
  -id <id>              Workspace ID
  -entrypoint <command> Command to run on start
  -shell <path>         Interactive console shell path
  -hostname <name>      Guest hostname
  -env KEY=VALUE        Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -debugfs <path>       debugfs binary path
  -file <path>          Workspace spec file
  -kernel <path>        Custom kernel path
  -rootfs <path>        Rootfs image path
  -state-dir <dir>      State directory
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
  -network <mode>       Network mode:
                         user (rootless, unprivileged user namespace; default)
                         or isolated (no network)
  -memory <MiB>         Memory in MiB; defaults to 512 for workspaces
  -cpus <n>             CPU count
  -vsock p=host:port    Add a vsock mapping
`)
}

func printRunHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent run

Run a command from an image.

Usage:
  microagent run IMAGE [COMMAND ARG...]
  microagent run --image IMAGE --exec <command>

Options:
  -image <ref>          OCI image
  -exec <command>       Shell command to run
  -setup <command>      Shell command to run before --exec
  -setup-file <path>    Shell script file to run before --exec
  -image-command        Run the image Entrypoint/Cmd
  -entrypoint <command> Command to run on start
  -shell <path>         Interactive console shell path
  -hostname <name>      Guest hostname
  -env KEY=VALUE        Guest environment variable
  -e KEY=VALUE          Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -v SRC:DST[:ro|rw]    Attach a safe tar/ext4 volume
  -volume SRC:DST[:ro|rw]
                         Attach a safe tar/ext4 volume
  -output n=/guest/path Declare an output artifact
  -file <path>          Workspace spec file
  -name <name>          Workspace name; generated when omitted
  -backend <name>       Backend identity override
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -guest-init <path>    Guest init path
  -arch <arch>          Guest architecture
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
  -network <mode>       Network mode:
                         user (rootless, unprivileged user namespace; default)
                         or isolated (no network)
  -p host:guest[/tcp]   Publish a TCP port
  -mediation p=host:port Required mediation vsock mapping
  -mediation-optional Allow workspace to run if mediation is unavailable
  -egress <mode>        Egress mediation: guarded (default, deny inside),
                         strict (deny non-allowlisted), or off
  -egress-allow <host>  Allowlisted egress host (repeatable; .suffix matches subdomains)
  -egress-passthrough <host>
                         Allowed egress host that is not TLS-intercepted (repeatable)
  -egress-policy <path>  Egress allow/passthrough policy file (.yaml/.yml/.json)
  -egress-swap-config <path>
                         Credential-swap config; mediator injects the real secret host-side (guest never holds it)
  -cred-swap PROVIDER[=ref]
                         Inject a built-in provider API key host-side (e.g. anthropic, openai); reference only, never a literal (repeatable)
  -broker-upstream <url>
                         Egress broker upstream; the broker injects the credential host-side and originates its own TLS (guest never holds the key)
  -broker-secret NAME=<scheme>:<ref>
                         Broker credential reference; the guest sends @secret:NAME
  -broker-env KEY[=VALUE]
                         Guest env var pointed at the broker (repeatable)
  -broker-proxy         Set HTTPS_PROXY/HTTP_PROXY in the guest to the broker
  -secret NAME=<scheme>:<ref>
                         Deliver a secret to tmpfs /run/secrets (repeatable)
  -secret-on-demand NAME=<scheme>:<ref>
                         On-demand secret, fetched at runtime, never written to tmpfs
  -secrets-env-file <path>
                         Deliver every key in a dotenv file as a secret
  -secrets-audit        Append every secret access to the workspace audit log
  -memory <MiB>         Memory in MiB; defaults to 512
  -cpus <n>             CPU count
  -size-mib <MiB>       Disk size
  -result-port <port>   Vsock result port
  -timeout <seconds>    Timeout
  -keep                 Keep state
  -rm                   Explicitly remove state after run
  -mke2fs <path>        mke2fs binary path
  -supervisor <path>    Override the supervisor path
  -model <ref>          Pair with a locally-served model (HuggingFace GGUF ref);
                         injects MICROAGENT_MODEL_URL and OPENAI_BASE_URL
  -model-token <token>  HuggingFace token for model auto-pull
                         (defaults to HF_TOKEN or HUGGING_FACE_HUB_TOKEN)
  -model-runner <name>  Model runner backend: llamacpp, vllm, or custom
  -model-gpu <mode>     Model runner GPU intent: off, on, or auto
  -model-mediation <mode> Model mediation: off, local-allow, or policy
  -model-policy-file <path> Model mediation policy file

Container-style examples:
  microagent run alpine echo hello
  microagent run -e FOO=bar -p 8080:80 alpine
  microagent run -v /tmp/config.tar:/config:ro alpine ls /config
  microagent run -v data:/work alpine ls /work   (attach a managed named volume)

Not implemented:
  container-engine APIs, compose projects, pods, privileged mode, namespace flags, devices, and
  host directory bind mounts are not exposed.
`)
}

func printCreateHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent create

Create a workspace from an image.

Options:
  -image <ref>          OCI image; defaults to Python 3.13 slim
  -name <name>          Workspace name
  -setup <command>      Shell command to run before first start
  -setup-file <path>    Shell script file to run before first start
  -service-command <cmd> Long-running command to run as the VM service
  -image-command        Run the image Entrypoint/Cmd when creating a prepared workspace
  -entrypoint <command> Command to run on start
  -shell <path>         Interactive console shell path
  -hostname <name>      Guest hostname
  -env KEY=VALUE        Guest environment variable
  -e KEY=VALUE          Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -v SRC:DST[:ro|rw]    Attach a safe tar/ext4 volume
  -volume SRC:DST[:ro|rw]
                         Attach a safe tar/ext4 volume
  -output n=/guest/path Declare an output artifact
  -file <path>          Workspace spec file
  -backend <name>       Backend identity override
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -guest-init <path>    Guest init path
  -arch <arch>          Guest architecture
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
  -network <mode>       Network mode:
                         user (rootless, unprivileged user namespace; default)
                         or isolated (no network)
  -p host:guest[/tcp]   Publish a TCP port
  -publish host:guest[/tcp]
                         Publish a TCP port
  -mediation p=host:port Required mediation vsock mapping
  -mediation-optional Allow workspace to run if mediation is unavailable
  -egress <mode>        Egress mediation: guarded (default, deny inside),
                         strict (deny non-allowlisted), or off
  -egress-allow <host>  Allowlisted egress host (repeatable; .suffix matches subdomains)
  -egress-passthrough <host>
                         Allowed egress host that is not TLS-intercepted (repeatable)
  -egress-policy <path>  Egress allow/passthrough policy file (.yaml/.yml/.json)
  -egress-swap-config <path>
                         Credential-swap config; mediator injects the real secret host-side (guest never holds it)
  -cred-swap PROVIDER[=ref]
                         Inject a built-in provider API key host-side (e.g. anthropic, openai); reference only, never a literal (repeatable)
  -broker-upstream <url>
                         Egress broker upstream; the broker injects the credential host-side and originates its own TLS (guest never holds the key)
  -broker-secret NAME=<scheme>:<ref>
                         Broker credential reference; the guest sends @secret:NAME
  -broker-env KEY[=VALUE]
                         Guest env var pointed at the broker (repeatable)
  -broker-proxy         Set HTTPS_PROXY/HTTP_PROXY in the guest to the broker
  -secret NAME=<scheme>:<ref>
                         Deliver a secret to tmpfs /run/secrets (repeatable)
  -secret-on-demand NAME=<scheme>:<ref>
                         On-demand secret, fetched at runtime, never written to tmpfs
  -secrets-env-file <path>
                         Deliver every key in a dotenv file as a secret
  -secrets-audit        Append every secret access to the workspace audit log
  -memory <MiB>         Memory in MiB; defaults to 512
  -cpus <n>             CPU count
  -size-mib <MiB>       Disk size
  -result-port <port>   Vsock result port
  -mke2fs <path>        mke2fs binary path
  -supervisor <path>    Override the supervisor path
  -model <ref>          Pair with a locally-served model (HuggingFace GGUF ref);
                         persisted so every start re-pairs; injects
                         MICROAGENT_MODEL_URL and OPENAI_BASE_URL
  -model-token <token>  HuggingFace token for model auto-pull
                         (defaults to HF_TOKEN or HUGGING_FACE_HUB_TOKEN)
  -model-runner <name>  Model runner backend: llamacpp, vllm, or custom
  -model-gpu <mode>     Model runner GPU intent: off, on, or auto
  -model-mediation <mode> Model mediation: off, local-allow, or policy
  -model-policy-file <path> Model mediation policy file
  -dry-run              Validate without writing state
  -json <path|->        Read request JSON from a file or stdin
`)
}
