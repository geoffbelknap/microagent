package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

var (
	version          = "dev"
	outputFormat     string
	noColorFlag      bool
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
	os.Exit(runMain(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// runMain executes one CLI invocation and returns the process exit code. It is
// the testable seam over run: it owns the exit-code and error-rendering policy
// main used to inline, so tests can assert main-level behavior against captured
// streams.
func runMain(ctx context.Context, args []string, stdout, stderr *os.File) int {
	err := run(ctx, args, stdout)
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var exitErr cliExitError
	if errors.As(err, &exitErr) {
		if !exitErr.Silent {
			fmt.Fprintln(stderr, exitErr.Error())
		}
		return exitErr.Code
	}
	return renderCLIError(stderr, err)
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	outputFormat = ""
	noColorFlag = false
	args = parseGlobalFlags(args)
	if len(args) > 0 && args[0] == "--host-worker-mediator" {
		return runHostWorkerMediator(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "--egress-datapath" {
		return runEgressDatapath(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "--broker-serve" {
		return runBrokerServe(ctx, args[1:])
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
	if spec, ok := lookupCommand(args[0]); ok {
		return spec.Run(ctx, args[1:], stdout)
	}
	if near := nearestCommandName(args[0]); near != "" {
		return fmt.Errorf("unknown command %q (did you mean %q?); run 'microagent help all' for the full list", args[0], near)
	}
	return fmt.Errorf("unknown command %q; run 'microagent help all' for the full list", args[0])
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

func requestForCommand(command string, fs *flag.FlagSet, stdout *os.File, args []string) (vmkit.Request, error) {
	var jsonPath string
	var dryRun bool
	var identity vmkit.Identity
	var config vmkit.Config
	var vsocks multiFlag
	var publishes multiFlag
	var disks multiFlag
	fs.StringVar(&jsonPath, "request-json", "", "Read request JSON from path, or '-' for stdin")
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
	if err := parseCommandFlags(fs, stdout, args); err != nil {
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
	case "status", "halt", "quarantine", "pause", "resume", "kill", "delete":
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
