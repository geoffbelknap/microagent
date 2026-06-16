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
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/network"
	"github.com/geoffbelknap/microagent/pkg/perf"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/scaffold"
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
	if len(args) > 0 && args[0] == "--host-worker-mediator" {
		return runHostWorkerMediator(ctx, args[1:], stdout)
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
		return runList(args[1:], stdout)
	}
	if args[0] == "ps" {
		return runPS(args[1:], stdout)
	}
	if args[0] == "logs" || args[0] == "log" {
		return runLogs(ctx, args[1:], stdout)
	}
	if args[0] == "events" {
		return runEvents(ctx, args[1:], stdout)
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

type doctorOptions struct {
	Backend        string
	Arch           string
	SupervisorPath string
}

func runContract(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("contract", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent contract")
	}
	return writeRuntimeContract(stdout, vmkit.NewRuntimeContract())
}

func runDoctor(ctx context.Context, args []string, stdout *os.File) error {
	opts := doctorOptions{
		Backend: hostBackend(),
		Arch:    defaultGuestArch(),
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Arch, "arch", opts.Arch, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected doctor argument: %s", fs.Arg(0))
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	resp, err := doctorResponse(ctx, opts)
	if encodeErr := writeDoctorResponse(stdout, resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runHost(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "setup-networking":
			return runHostSetupNetworking(args[1:], stdout)
		default:
			return fmt.Errorf("unknown host command: %s", args[0])
		}
	}
	opts := doctorOptions{
		Backend: hostBackend(),
		Arch:    defaultGuestArch(),
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Arch, "arch", opts.Arch, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected host argument: %s", fs.Arg(0))
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	if err := workspace.ValidateHostBackend(opts.Backend); err != nil {
		return err
	}
	resp, _ := doctorResponse(ctx, opts)
	return writeDoctorResponse(stdout, resp)
}

func doctorResponse(ctx context.Context, opts doctorOptions) (vmkit.Response, error) {
	return diagnostics.Check(ctx, diagnostics.Options{Backend: opts.Backend, Arch: opts.Arch, SupervisorPath: opts.SupervisorPath})
}

func augmentHostSupport(resp *vmkit.Response, opts doctorOptions) {
	diagnostics.AugmentHostSupport(resp, diagnostics.Options{Backend: opts.Backend, Arch: opts.Arch, SupervisorPath: opts.SupervisorPath})
}

func backendSupportsConsoleInput(backend string) bool {
	return workspace.BackendSupportsConsoleInput(backend)
}

func firecrackerDoctorResponse(backend, arch string, resolveBinary func() (string, error), resolveSupervisor func(diagnostics.Options) (string, error), resolveGuestInit func(diagnostics.Options) (string, error), stat func(string) (os.FileInfo, error), binaryVersion func(string) string, lookPath func(string) (string, error), readFile func(string) ([]byte, error), probeUserNamespaces func() error) (vmkit.Response, error) {
	return diagnostics.CheckFirecracker(
		diagnostics.Options{Backend: backend, Arch: arch},
		diagnostics.FirecrackerProbe{ResolveBinary: resolveBinary, ResolveSupervisor: resolveSupervisor, ResolveGuestInit: resolveGuestInit, Stat: stat, BinaryVersion: binaryVersion, LookPath: lookPath, ReadFile: readFile, ProbeUserNamespaces: probeUserNamespaces},
	)
}

func resolveFirecrackerPath() (string, error) {
	return diagnostics.ResolveFirecrackerPath()
}

func defaultFirecrackerPathFromExecutable(executable string) string {
	return diagnostics.DefaultFirecrackerPathFromExecutable(executable)
}

func firstOutputLine(output string) string {
	return diagnostics.FirstOutputLine(output)
}

func runKernel(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printKernelHelp(stdout)
		return nil
	}
	switch args[0] {
	case "install":
		return runKernelInstall(ctx, args[1:], stdout)
	case "verify":
		return runKernelVerify(args[1:], stdout)
	default:
		return fmt.Errorf("unknown kernel command: %s", args[0])
	}
}

func runKernelInstall(ctx context.Context, args []string, stdout *os.File) error {
	opts := kernel.InstallOptions{Backend: hostBackend(), Architecture: defaultGuestArch()}
	opts.OutputPath = workspace.WritableKernelPath(opts.Backend, opts.Architecture)
	outputExplicit := hasFlagValue(args, "out")
	fs := flag.NewFlagSet("kernel install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.URL, "url", "", "Kernel URL")
	fs.StringVar(&opts.FromPath, "from", "", "Local kernel path")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.OutputPath, "out", opts.OutputPath, "Output path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected kernel install argument: %s", fs.Arg(0))
	}
	if !outputExplicit || opts.OutputPath == "" {
		opts.OutputPath = workspace.WritableKernelPath(opts.Backend, opts.Architecture)
	}
	result, err := kernel.Install(ctx, opts)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func runKernelVerify(args []string, stdout *os.File) error {
	opts := kernel.VerifyOptions{Backend: hostBackend(), Architecture: defaultGuestArch()}
	opts.Path = defaultKernelPath(opts.Backend, opts.Architecture)
	fs := flag.NewFlagSet("kernel verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Path, "path", opts.Path, "Kernel path")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected kernel verify argument: %s", fs.Arg(0))
	}
	result, err := kernel.Verify(opts)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func defaultKernelSupportForPath(backend, arch, path string) *vmkit.KernelSupport {
	support := &vmkit.KernelSupport{
		Backend:      backend,
		Architecture: arch,
		Path:         path,
		Status:       "unavailable",
	}
	if support.Path != "" {
		if _, err := os.Stat(support.Path); err == nil {
			support.Status = "present"
		} else if !os.IsNotExist(err) {
			support.Status = "error"
			support.Error = err.Error()
			return support
		}
	}
	if kernel, ok := defaultKernel(backend, arch); ok {
		support.SHA256 = kernel.SHA256
		if support.Status == "unavailable" {
			support.Status = "downloadable"
		}
	}
	return support
}

type kernelManifestEntry = kernel.ManifestEntry

var defaultKernels = kernel.Defaults

func defaultKernel(backend, arch string) (kernelManifestEntry, bool) {
	for _, kernel := range defaultKernels {
		if kernel.Backend == backend && kernel.Architecture == arch {
			return kernel, true
		}
	}
	return kernelManifestEntry{}, false
}

func runRootFS(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRootFSHelp(stdout)
		return nil
	}
	if args[0] != "build" {
		return fmt.Errorf("unknown rootfs command: %s", args[0])
	}
	var req rootfs.BuildRequest
	fs := flag.NewFlagSet("rootfs build", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&req.ImageRef, "image", "", "OCI image reference")
	fs.StringVar(&req.Platform.OS, "os", "linux", "target operating system")
	fs.StringVar(&req.Platform.Architecture, "arch", "arm64", "target architecture")
	fs.StringVar(&req.OutputPath, "out", "", "output rootfs path")
	fs.StringVar(&req.InitPath, "init", rootfs.DefaultInitPath, "guest init path to inject")
	fs.StringVar(&req.StateDir, "state-dir", "", "builder state directory")
	fs.StringVar(&req.Mke2fsPath, "mke2fs", "mke2fs", "mke2fs binary path")
	fs.Int64Var(&req.SizeMiB, "size-mib", rootfs.DefaultSizeMiB, "rootfs image size in MiB")
	fs.BoolVar(&req.KeepStage, "keep-stage", false, "keep temporary unpacked stage directory")
	fs.StringVar(&req.StageSnapshot, "stage-snapshot", "", "copy unpacked stage directory to this path before ext4 creation")
	fs.BoolVar(&req.AllowMutable, "allow-mutable", false, "allow mutable image references")
	var execCommand string
	fs.StringVar(&execCommand, "exec", "", "shell command to run as guest init")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected rootfs argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(execCommand) != "" {
		req.Command = []string{"/bin/sh", "-lc", execCommand}
	}
	req.Progress = rootfsProgress(stdout, "rootfs")
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	if provenance.ImageRef != "" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if encodeErr := enc.Encode(provenance); encodeErr != nil {
			return encodeErr
		}
	}
	return err
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
	networkMode := fs.String("network", defaultNetworkMode, "Network mode: user, nat, isolated, or bridged")
	networkInterface := fs.String("network-interface", "", "Host interface for bridged network mode")
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
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, disks, vsocks, *networkMode, *networkInterface, publishes)
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
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, disks, vsocks, *networkMode, *networkInterface, publishes)
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

type imageIndex = imagecache.Index
type imageRecord = imagecache.Record
type imagePruneResult = imagecache.PruneResult
type perfBootOptions = perf.BootOptions
type perfReport = perf.BootReport
type perfIteration = perf.Iteration
type perfSummary = perf.Summary
type perfFootprintReport = perf.FootprintReport
type perfSteadyReport = perf.SteadyReport
type perfRSSSample = perf.RSSSample
type perfRSSSummary = perf.RSSSummary

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

func runList(args []string, stdout *os.File) error {
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
	return writeWorkspaceList(stdout, entries)
}

func runPS(args []string, stdout *os.File) error {
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
	return writeWorkspaceList(stdout, filterRunningWorkspaces(entries))
}

func filterRunningWorkspaces(entries []workspaceListEntry) []workspaceListEntry {
	filtered := entries[:0]
	for _, entry := range entries {
		switch vmkit.VMState(entry.State) {
		case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined, vmkit.StateStopping:
			filtered = append(filtered, entry)
		}
	}
	return filtered
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

func runCP(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	debugfsPath := defaultDebugFSPath()
	fs := flag.NewFlagSet("cp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: microagent cp <source> <target> [--state-dir <dir>]")
	}
	result, err := workspace.Copy(ctx, opts.StateDir, debugfsPath, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
}

func runArtifact(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) > 0 && args[0] == "get" {
		return runArtifactGet(ctx, args[1:], stdout)
	}
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("artifact", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent artifact <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	artifacts, err := workspace.ArtifactsFor(opts.StateDir, name)
	if err != nil {
		return err
	}
	result := artifactsResult{Workspace: name, Artifacts: artifacts}
	return writeArtifactsResult(stdout, result)
}

func runArtifactGet(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	debugfsPath := defaultDebugFSPath()
	fs := flag.NewFlagSet("artifact get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: microagent artifact get <name> <artifact> <target> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	result, err := workspace.GetArtifact(ctx, opts.StateDir, debugfsPath, name, fs.Arg(1), fs.Arg(2))
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
}

func runSnapshot(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || wantsHelp(args) {
		fmt.Fprint(stdout, `microagent snapshot — create, list, or remove workspace snapshots

  microagent snapshot create <name> [--tag <tag>] [--state-dir <dir>]
  microagent snapshot list <name> [--state-dir <dir>]
  microagent snapshot delete <name> <tag> [--state-dir <dir>]
`)
		return nil
	}
	switch args[0] {
	case "create":
		return runSnapshotCreate(ctx, args[1:], stdout)
	case "list":
		return runSnapshotList(args[1:], stdout)
	case "delete":
		return runSnapshotRemove(args[1:], stdout)
	default:
		return fmt.Errorf("unknown snapshot subcommand %q; use create, list, or delete", args[0])
	}
}

func runSnapshotCreate(ctx context.Context, args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	name := ""
	tag := ""
	fs := flag.NewFlagSet("snapshot create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&backend, "backend", backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	fs.StringVar(&tag, "tag", "", "Snapshot tag (defaults to a timestamp)")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if !supervisorExplicit {
		supervisorPath = defaultSupervisorPath(backend)
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: microagent snapshot create <name> [--tag <tag>] [--state-dir <dir>]")
	}
	if fs.NArg() == 1 {
		if name != "" {
			return fmt.Errorf("workspace name specified twice")
		}
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: microagent snapshot create <name> [--tag <tag>] [--state-dir <dir>]")
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if strings.TrimSpace(tag) == "" {
		tag = "snap-" + time.Now().UTC().Format("20060102-150405")
	}
	opts := workspaceOptions{StateDir: stateDir, Name: name, Backend: backend, SupervisorPath: supervisorPath}
	manifest, err := workspace.Snapshot(ctx, opts, tag)
	if err != nil {
		return err
	}
	return writeSnapshotManifestResult(stdout, manifest)
}

func runSnapshotList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	name := ""
	fs := flag.NewFlagSet("snapshot list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: microagent snapshot list <name> [--state-dir <dir>]")
	}
	if fs.NArg() == 1 {
		if name != "" {
			return fmt.Errorf("workspace name specified twice")
		}
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: microagent snapshot list <name> [--state-dir <dir>]")
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	infos, err := workspace.SnapshotList(workspaceOptions{StateDir: stateDir, Name: name})
	if err != nil {
		return err
	}
	return writeSnapshotListResult(stdout, name, infos)
}

func runSnapshotRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	name := ""
	fs := flag.NewFlagSet("snapshot rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	rest := fs.Args()
	tag := ""
	if name == "" {
		if len(rest) != 2 {
			return fmt.Errorf("usage: microagent snapshot delete <name> <tag> [--state-dir <dir>]")
		}
		name = rest[0]
		tag = rest[1]
	} else {
		if len(rest) != 1 {
			return fmt.Errorf("usage: microagent snapshot delete <name> <tag> [--state-dir <dir>]")
		}
		tag = rest[0]
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if err := workspace.SnapshotRemove(workspaceOptions{StateDir: stateDir, Name: name}, tag); err != nil {
		return err
	}
	return writeSnapshotRemoveResult(stdout, name, tag)
}

func writeSnapshotManifestResult(stdout *os.File, manifest vmkit.SnapshotManifest) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, manifest)
	}
	fmt.Fprintf(stdout, "snapshot %s created (%d MiB RAM, %d vCPU) at %s\n", manifest.Tag, manifest.MemoryMiB, manifest.VCPUCount, manifest.CreatedAt)
	return nil
}

func writeSnapshotListResult(stdout *os.File, name string, infos []vmkit.SnapshotInfo) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspace": name, "snapshots": infos})
	}
	if len(infos) == 0 {
		fmt.Fprintf(stdout, "no snapshots for %s\n", name)
		return nil
	}
	fmt.Fprintf(stdout, "%-24s %-12s %-21s %s\n", "TAG", "SIZE", "CREATED", "IMAGE")
	for _, info := range infos {
		fmt.Fprintf(stdout, "%-24s %-12s %-21s %s\n", info.Tag, formatBytes(info.SizeBytes), info.CreatedAt, info.ImageRef)
	}
	return nil
}

func writeSnapshotRemoveResult(stdout *os.File, name, tag string) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspace": name, "removed": tag})
	}
	fmt.Fprintf(stdout, "removed snapshot %s of %s\n", tag, name)
	return nil
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

func runInit(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printInitHelp(stdout)
		return nil
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	provider := fs.String("provider", string(scaffold.DefaultProvider), "Body provider: anthropic, openai, or gemini")
	dir := fs.String("dir", "", "Target directory (defaults to ./<name>)")
	force := fs.Bool("force", false, "Overwrite existing files")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: microagent init <name> [--provider anthropic|openai|gemini] [--dir <path>] [--force]")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("unexpected init argument: %s", fs.Arg(1))
	}
	result, err := scaffold.Generate(scaffold.Options{
		Name:     fs.Arg(0),
		Dir:      *dir,
		Provider: scaffold.Provider(*provider),
		Force:    *force,
	})
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Scaffolded %s agent %q in %s\n", result.Provider, result.Name, result.Dir)
	for _, f := range result.Files {
		fmt.Fprintf(stdout, "  %s\n", f)
	}
	fmt.Fprintf(stdout, "\nNext:\n")
	fmt.Fprintf(stdout, "  cd %s\n", result.Dir)
	fmt.Fprintf(stdout, "  microagent create --file microagent.yaml --env %s=$%s\n", result.APIKey, result.APIKey)
	fmt.Fprintf(stdout, "  microagent cp demo/input-001.json %s:/workspace/input.json\n", result.Name)
	fmt.Fprintf(stdout, "  microagent start %s\n", result.Name)
	return nil
}

func runCommit(ctx context.Context, args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printCommitHelp(stdout)
		return nil
	}
	stateDir := defaultStateDir()
	backend := hostBackend()
	debugfsPath := defaultDebugFSPath()
	arch := defaultGuestArch()
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&backend, "backend", backend, "Backend identity override")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	fs.StringVar(&arch, "arch", arch, "OCI image architecture")
	push := fs.Bool("push", false, "Push the committed image to its registry after committing")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: microagent commit <workspace> <image-ref> [--push] [--arch <arch>] [--debugfs <path>] [--state-dir <dir>]")
	}
	if err := validateWorkspaceName(fs.Arg(0)); err != nil {
		return err
	}
	result, err := commit.Commit(ctx, commit.Options{
		StateDir:     stateDir,
		DebugFSPath:  debugfsPath,
		Workspace:    fs.Arg(0),
		Backend:      backend,
		Reference:    fs.Arg(1),
		Architecture: arch,
	})
	if err != nil {
		return err
	}
	pushed := false
	if *push {
		if err := commit.Push(ctx, stateDir, result.Reference); err != nil {
			return err
		}
		pushed = true
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{
			"reference": result.Reference, "digest": result.Digest,
			"size_bytes": result.SizeBytes, "layout_path": result.LayoutPath, "pushed": pushed,
		})
	}
	fmt.Fprintf(stdout, "Committed %s\n  digest: %s\n  layer:  %d bytes\n  layout: %s\n", result.Reference, result.Digest, result.SizeBytes, result.LayoutPath)
	if pushed {
		fmt.Fprintf(stdout, "Pushed %s\n", result.Reference)
	} else {
		fmt.Fprintf(stdout, "Push it with: microagent image push %s\n", result.Reference)
	}
	return nil
}

func printCommitHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent commit

Snapshot a stopped workspace's rootfs into an OCI image, stored in the local
image layout. Closes the OCI->rootfs loop; push it with `+"`microagent image push`"+`.

Usage:
  microagent commit <workspace> <image-ref> [options]

Options:
  --push                Push to the registry immediately after committing
  --arch <arch>         OCI image architecture (defaults to the guest arch)
  --debugfs <path>      debugfs binary path used to extract the rootfs
  --backend <name>      Backend identity override
  --state-dir <dir>     State directory
`)
}

func printInitHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent init

Scaffold a starter agent: a microagent.yaml spec, a provider-specific
agent, the shared agent protocol, and a runnable demo request. The generated
project is consumed by the normal create/cp/start flow.

Usage:
  microagent init <name> [options]

Options:
  --provider <name>     Model provider: anthropic (default), openai, or gemini
  --dir <path>          Target directory (defaults to ./<name>)
  --force               Overwrite existing files
`)
}

func runImage(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	guestInitExplicit := hasFlagValue(args, "guest-init")
	fs := flag.NewFlagSet("image", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	arch := fs.String("arch", defaultGuestArch(), "Image architecture")
	sizeMiB := fs.Int64("size-mib", rootfs.DefaultSizeMiB, "Rootfs image size in MiB")
	mke2fsPath := fs.String("mke2fs", defaultMke2fsPath(), "mke2fs binary path")
	guestInitPath := fs.String("guest-init", defaultGuestInitPath(*arch), "Guest init path")
	deleteFiles := fs.Bool("delete", false, "Delete reusable local image rootfs files during prune")
	yes := fs.Bool("yes", false, "Confirm destructive image cache cleanup without prompting")
	fs.BoolVar(yes, "y", false, "Confirm destructive image cache cleanup without prompting")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if !guestInitExplicit {
		*guestInitPath = defaultGuestInitPath(*arch)
	}
	if fs.NArg() == 0 || fs.Arg(0) == "list" {
		if fs.NArg() > 1 {
			return fmt.Errorf("usage: microagent image list [--state-dir <dir>]")
		}
		images, err := imagecache.List(opts.StateDir)
		if err != nil {
			return err
		}
		return writeImageList(stdout, images)
	}
	switch fs.Arg(0) {
	case "pull":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: microagent image pull <image> [--state-dir <dir>]")
		}
		record, err := imagecache.Pull(context.Background(), imagecache.PullOptions{
			StateDir:      opts.StateDir,
			ImageRef:      fs.Arg(1),
			Architecture:  *arch,
			SizeMiB:       *sizeMiB,
			Mke2fsPath:    *mke2fsPath,
			GuestInitPath: *guestInitPath,
		})
		if err != nil {
			return err
		}
		return writeImageRecord(stdout, record)
	case "push":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: microagent image push <image> [--state-dir <dir>]")
		}
		if err := commit.Push(context.Background(), opts.StateDir, fs.Arg(1)); err != nil {
			return err
		}
		if outputJSON(stdout) {
			return writeJSON(stdout, map[string]any{"pushed": fs.Arg(1)})
		}
		fmt.Fprintf(stdout, "Pushed %s\n", fs.Arg(1))
		return nil
	case "tag":
		if fs.NArg() != 3 {
			return fmt.Errorf("usage: microagent image tag <source> <target> [--state-dir <dir>]")
		}
		record, err := imagecache.Tag(opts.StateDir, fs.Arg(1), fs.Arg(2))
		if err != nil {
			return err
		}
		return writeImageRecord(stdout, record)
	case "delete":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: microagent image delete <image> [--delete] [--state-dir <dir>]")
		}
		if *deleteFiles {
			if err := confirmImageCacheDelete(*yes); err != nil {
				return err
			}
		}
		result, err := imagecache.Remove(opts.StateDir, fs.Arg(1), *deleteFiles)
		if err != nil {
			return err
		}
		return writeImagePruneResult(stdout, result)
	case "prune":
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: microagent image prune [--state-dir <dir>]")
		}
		if *deleteFiles {
			if err := confirmImageCacheDelete(*yes); err != nil {
				return err
			}
		}
		result, err := imagecache.Prune(opts.StateDir, *deleteFiles)
		if err != nil {
			return err
		}
		return writeImagePruneResult(stdout, result)
	default:
		return fmt.Errorf("unknown image command: %s", fs.Arg(0))
	}
}

func confirmImageCacheDelete(yes bool) error {
	if yes {
		return nil
	}
	ok, err := confirmAction("Delete reusable image cache rootfs files under the local image store? Workspace disks will not be deleted.")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("prune cancelled")
	}
	return nil
}

func runPerf(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printPerfHelp(stdout)
		return nil
	}
	switch args[0] {
	case "boot":
		return runPerfBoot(ctx, args[1:], stdout)
	case "footprint":
		return runPerfFootprint(args[1:], stdout)
	case "steady":
		return runPerfSteady(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown perf command: %s", args[0])
	}
}

func runPerfBoot(ctx context.Context, args []string, stdout *os.File) error {
	opts := defaultPerfBootOptions()
	fs := flag.NewFlagSet("perf boot", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "OCI image reference")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Guest command used to mark boot completion")
	fs.IntVar(&opts.Iterations, "iterations", opts.Iterations, "Number of boot measurements")
	timeoutSeconds := int(opts.Timeout.Seconds())
	fs.IntVar(&timeoutSeconds, "timeout", timeoutSeconds, "Per-iteration timeout in seconds")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "Supervisor path")
	fs.StringVar(&opts.NetworkMode, "network", opts.NetworkMode, "Network mode for measured boots (user, nat, isolated, bridged); empty uses the backend default")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected perf boot argument: %s", fs.Arg(0))
	}
	if timeoutSeconds <= 0 {
		return fmt.Errorf("perf boot timeout must be positive")
	}
	opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	hostResp, _ := doctorResponse(ctx, doctorOptions{Backend: hostBackend(), Arch: defaultGuestArch(), SupervisorPath: opts.SupervisorPath})
	opts.Host = hostResp.Host
	report, err := perf.Boot(ctx, opts)
	if err != nil {
		return err
	}
	return writePerfReport(stdout, report)
}

func defaultPerfBootOptions() perfBootOptions {
	return perfBootOptions{
		StateDir:       defaultStateDir(),
		ImageRef:       defaultWorkspaceImage(defaultGuestArch()),
		Profile:        defaultWorkspaceProfile,
		ExecCommand:    "true",
		Iterations:     1,
		Timeout:        120 * time.Second,
		Mke2fsPath:     defaultMke2fsPath(),
		SupervisorPath: defaultSupervisorPath(hostBackend()),
		Backend:        hostBackend(),
		Architecture:   defaultGuestArch(),
	}
}

func summarizePerfIterations(iterations []perfIteration) perfSummary {
	return perf.SummarizeIterations(iterations)
}

func writePerfReport(stdout *os.File, report perfReport) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "Benchmark: %s\n", report.Benchmark)
	fmt.Fprintf(stdout, "Backend: %s\n", report.Backend)
	fmt.Fprintf(stdout, "Arch: %s\n", report.Arch)
	fmt.Fprintf(stdout, "Image: %s\n", report.ImageRef)
	fmt.Fprintf(stdout, "Profile: %s\n", report.Profile)
	fmt.Fprintf(stdout, "Iterations: %d\n", report.Summary.Count)
	fmt.Fprintf(stdout, "Boot ms: min=%d avg=%d max=%d\n", report.Summary.MinMs, report.Summary.AvgMs, report.Summary.MaxMs)
	for _, iteration := range report.Iterations {
		status := "ok"
		if !iteration.OK {
			status = "failed"
		}
		fmt.Fprintf(stdout, "%-28s %-8s %d", iteration.Name, status, iteration.DurationMs)
		if iteration.Error != "" {
			fmt.Fprintf(stdout, " %s", iteration.Error)
		}
		fmt.Fprintln(stdout)
	}
	return nil
}

func runPerfFootprint(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("perf footprint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent perf footprint <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	report, err := perf.Footprint(opts.StateDir, name)
	if err != nil {
		return err
	}
	return writePerfFootprintReport(stdout, report)
}

func parseRSSKiB(output []byte) (int64, error) {
	return perf.ParseRSSKiB(output)
}

func writePerfFootprintReport(stdout *os.File, report perfFootprintReport) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "Benchmark: %s\n", report.Benchmark)
	fmt.Fprintf(stdout, "Workspace: %s\n", report.Workspace)
	fmt.Fprintf(stdout, "Backend: %s\n", report.Backend)
	fmt.Fprintf(stdout, "State: %s\n", report.State)
	fmt.Fprintf(stdout, "PID: %d\n", report.PID)
	fmt.Fprintf(stdout, "RSS KiB: %d\n", report.RSSKiB)
	return nil
}

func runPerfSteady(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	durationSeconds := 10
	intervalSeconds := 1
	fs := flag.NewFlagSet("perf steady", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.IntVar(&durationSeconds, "duration", durationSeconds, "Sampling duration in seconds")
	fs.IntVar(&intervalSeconds, "interval", intervalSeconds, "Sampling interval in seconds")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent perf steady <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	report, err := perf.Steady(ctx, opts.StateDir, name, time.Duration(durationSeconds)*time.Second, time.Duration(intervalSeconds)*time.Second)
	if err != nil {
		return err
	}
	return writePerfSteadyReport(stdout, report)
}

func summarizeRSSSamples(samples []perfRSSSample) perfRSSSummary {
	return perf.SummarizeRSSSamples(samples)
}

func writePerfSteadyReport(stdout *os.File, report perfSteadyReport) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "Benchmark: %s\n", report.Benchmark)
	fmt.Fprintf(stdout, "Workspace: %s\n", report.Workspace)
	fmt.Fprintf(stdout, "Backend: %s\n", report.Backend)
	fmt.Fprintf(stdout, "State: %s\n", report.State)
	fmt.Fprintf(stdout, "PID: %d\n", report.PID)
	fmt.Fprintf(stdout, "Samples: %d\n", report.Summary.Count)
	fmt.Fprintf(stdout, "RSS KiB: min=%d avg=%d max=%d\n", report.Summary.MinKiB, report.Summary.AvgKiB, report.Summary.MaxKiB)
	return nil
}

func runLogs(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	follow := false
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.BoolVar(&follow, "follow", false, "Stream the serial buffer and new output until the workspace stops or interrupted")
	fs.BoolVar(&follow, "f", false, "Alias for --follow")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent logs <name> [--follow] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if follow {
		if outputStructured() {
			return fmt.Errorf("logs --follow is not supported with --json/--output json; omit --follow to capture the buffer once")
		}
		return followLogs(ctx, opts.StateDir, name, stdout)
	}
	data, err := workspace.ReadLogs(opts.StateDir, name)
	if err != nil {
		return err
	}
	if outputStructured() {
		return writeJSON(stdout, map[string]any{
			"workspace": name,
			"logs":      string(data),
		})
	}
	_, err = stdout.Write(data)
	return err
}

// followLogs prints the captured serial buffer, then streams new output as it
// is appended, until the workspace leaves the running state or the caller
// interrupts (Ctrl-C). It is the streaming counterpart to ReadLogs.
func followLogs(ctx context.Context, stateDir, name string, stdout *os.File) error {
	// Surface the same "no such workspace" error as the non-follow path before
	// entering the stream loop.
	if _, err := workspace.ReadLogs(stateDir, name); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	serialPath := workspace.SerialLogPath(stateDir, name)
	var offset int64
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		n, err := writeSerialTail(serialPath, offset, stdout)
		if err != nil {
			return err
		}
		offset += n

		// Once the workspace is no longer running, drain any final bytes and stop
		// so the command does not hang on a stopped workspace.
		state, _, stateErr := workspace.LatestStartState(stateDir, name)
		if stateErr != nil || state != vmkit.StateRunning {
			_, _ = writeSerialTail(serialPath, offset, stdout)
			return nil
		}
		select {
		case <-ctx.Done():
			_, _ = writeSerialTail(serialPath, offset, stdout)
			return nil
		case <-ticker.C:
		}
	}
}

// writeSerialTail copies bytes from path starting at offset to stdout and
// returns the number of bytes written. A not-yet-created serial log is treated
// as empty rather than an error so callers can poll a workspace that is still
// booting.
func writeSerialTail(path string, offset int64, stdout *os.File) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(stdout, f)
}

func runEvents(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	follow := false
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.BoolVar(&follow, "follow", false, "Stream new lifecycle events until the workspace stops or interrupted")
	fs.BoolVar(&follow, "f", false, "Alias for --follow")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent events <name> [--follow] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	events, err := workspace.ReadEvents(opts.StateDir, name)
	if err != nil {
		return err
	}
	if follow {
		if outputStructured() {
			return fmt.Errorf("events --follow is not supported with --json/--output json; omit --follow for a one-shot snapshot")
		}
		return followEvents(ctx, opts.StateDir, name, events, stdout)
	}
	if outputStructured() {
		return writeJSON(stdout, map[string]any{"workspace": name, "events": events})
	}
	for _, event := range events {
		writeEventLine(stdout, event)
	}
	return nil
}

func writeEventLine(stdout *os.File, event workspace.EventFile) {
	line := fmt.Sprintf("%s  %s", event.ObservedAt, event.State)
	if event.Detail != "" {
		line += "  " + event.Detail
	}
	fmt.Fprintln(stdout, line)
}

// eventFollowComplete reports whether the latest event is a terminal lifecycle
// state, so events --follow returns instead of polling forever. Quarantined is
// not terminal: a quarantined runtime may still be running.
func eventFollowComplete(events []workspace.EventFile) bool {
	if len(events) == 0 {
		return false
	}
	switch events[len(events)-1].State {
	case vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed:
		return true
	default:
		return false
	}
}

func runStats(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	follow := false
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.BoolVar(&follow, "follow", false, "Stream resource samples until the workspace stops or interrupted")
	fs.BoolVar(&follow, "f", false, "Alias for --follow")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent stats <name> [--follow] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if follow {
		if outputStructured() {
			return fmt.Errorf("stats --follow is not supported with --json/--output json; omit --follow for a single sample")
		}
		return followStats(ctx, opts.StateDir, name, stdout)
	}
	stats, err := workspace.SampleStats(opts.StateDir, name)
	if err != nil {
		return err
	}
	if outputStructured() {
		return writeJSON(stdout, stats)
	}
	fmt.Fprintln(stdout, formatStatsLine(stats))
	return nil
}

func formatStatsLine(stats workspace.Stats) string {
	const mib = 1024 * 1024
	return fmt.Sprintf("pid=%d  cpu=%.1f%%  mem=%.1f MiB  io_read=%.1f MiB  io_write=%.1f MiB",
		stats.PID,
		stats.CPUPercent,
		float64(stats.MemoryBytes)/mib,
		float64(stats.IOReadBytes)/mib,
		float64(stats.IOWriteBytes)/mib,
	)
}

func followStats(ctx context.Context, stateDir, name string, stdout *os.File) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		stats, err := workspace.SampleStats(stateDir, name)
		if err != nil {
			// Stop quietly once the workspace is no longer running; surface any
			// other error.
			if state, _, stateErr := workspace.LatestStartState(stateDir, name); stateErr == nil && state != vmkit.StateRunning {
				return nil
			}
			return err
		}
		fmt.Fprintln(stdout, formatStatsLine(stats))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(800 * time.Millisecond):
		}
	}
}

// followEvents prints the recorded events, then streams newly appended events
// as the workspace changes state, returning when the workspace reaches a
// terminal state or the caller interrupts. events.json is rewritten wholesale
// on each change, so new entries are detected by a growing event count.
func followEvents(ctx context.Context, stateDir, name string, seen []workspace.EventFile, stdout *os.File) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, event := range seen {
		writeEventLine(stdout, event)
	}
	count := len(seen)
	if eventFollowComplete(seen) {
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := workspace.ReadEvents(stateDir, name)
		if err != nil {
			return err
		}
		if len(events) > count {
			for _, event := range events[count:] {
				writeEventLine(stdout, event)
			}
			count = len(events)
		}
		if eventFollowComplete(events) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runNetwork(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printNetworkHelp(stdout)
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "create":
			return runNetworkCreate(args[1:], stdout)
		case "list":
			return runNetworkList(args[1:], stdout)
		case "delete":
			return runNetworkRemove(args[1:], stdout)
		case "status":
			return runNetworkInspect(args[1:], stdout)
		}
	}
	return runNetworkInspect(args, stdout)
}

func runNetworkInspect(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("network", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent network <workspace> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	result, err := workspace.Network(opts.StateDir, name)
	if err != nil {
		return err
	}
	return writeNetworkResult(stdout, result)
}

func runNetworkCreate(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("network create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	subnet := fs.String("subnet", "", "Subnet CIDR (auto-allocated from 10.44.0.0/16 when omitted)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent network create <name> [--subnet <cidr>] [--state-dir <dir>]")
	}
	record, err := network.Create(stateDir, fs.Arg(0), *subnet)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Created network %q (%s, gateway %s)\n", record.Name, record.Subnet, record.Gateway)
	return nil
}

func runNetworkList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("network list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent network list [--state-dir <dir>]")
	}
	records, err := network.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"networks": records})
	}
	fmt.Fprintf(stdout, "%-20s %-18s %-15s %s\n", "NAME", "SUBNET", "GATEWAY", "MEMBERS")
	for _, r := range records {
		fmt.Fprintf(stdout, "%-20s %-18s %-15s %d\n", r.Name, r.Subnet, r.Gateway, len(r.Members))
	}
	return nil
}

func runNetworkRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	force := false
	fs := flag.NewFlagSet("network delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&force, "force", false, "Remove even if the network still has members")
	fs.BoolVar(&force, "f", false, "Remove even if the network still has members")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent network delete <name> [--force] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := network.Remove(stateDir, name, force); err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"removed": name})
	}
	fmt.Fprintf(stdout, "Removed network %q\n", name)
	return nil
}

func printNetworkHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent network

Inspect a workspace's network, or manage user-defined named networks.

Usage:
  microagent network <workspace>              Show a workspace's network
  microagent network status <workspace>       Show a workspace's network
  microagent network create <name> [options]  Create a named network
  microagent network list                      List named networks
  microagent network delete <name> [options]       Remove a named network

Options:
  --subnet <cidr>       Subnet for create; auto-allocated from 10.44.0.0/16 when omitted
  --force               Remove a network even if it still has members
  --state-dir <dir>     State directory

Join a workspace to a named network with create/run --network-name <name>:
members get a stable IP from the subnet, share a managed bridge (HNS network on
windows-hyperv), and resolve each other by name. Workspace attachment is
implemented by Firecracker on Linux (privileged) and windows-hyperv (elevated,
as a private HNS network); Apple VF does not currently implement
network.mode=named.
`)
}

func runModel(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		fmt.Fprintln(stdout, "usage: microagent model <pull|list|delete|prune|serve|stop|runners|policy> ...")
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "pull":
			return runModelPull(args[1:], stdout)
		case "list":
			return runModelList(args[1:], stdout)
		case "delete":
			return runModelRemove(args[1:], stdout)
		case "prune":
			return runModelPrune(args[1:], stdout)
		case "serve":
			return runModelServe(args[1:], stdout)
		case "stop":
			return runModelStop(args[1:], stdout)
		case "runners":
			return runModelRunners(args[1:], stdout)
		case "policy":
			return runModelPolicy(args[1:], stdout)
		}
	}
	return fmt.Errorf("usage: microagent model <pull|list|delete|prune|serve|stop|runners|policy> [args]")
}

func runModelPolicy(args []string, stdout *os.File) error {
	if wantsHelp(args) || len(args) == 0 {
		printModelPolicyHelp(stdout)
		return nil
	}
	switch args[0] {
	case "validate":
		return runModelPolicyValidate(args[1:], stdout)
	case "evaluate", "eval":
		return runModelPolicyEvaluate(args[1:], stdout)
	default:
		return fmt.Errorf("usage: microagent model policy <validate|evaluate> args")
	}
}

type modelPolicyValidationOutput struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	SchemaVersion string `json:"schema_version"`
	Default       string `json:"default"`
	Rules         int    `json:"rules"`
}

type modelPolicyEvaluationOutput struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	RuleID        string `json:"rule_id,omitempty"`
	AuditEventID  string `json:"audit_event_id,omitempty"`
	Expected      string `json:"expected,omitempty"`
	MatchedExpect bool   `json:"matched_expect,omitempty"`
}

func runModelPolicyValidate(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("model policy validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model policy validate <policy.json>")
	}
	policy, source, err := hostworker.LoadFilePolicy(fs.Arg(0))
	if err != nil {
		return err
	}
	out := modelPolicyValidationOutput{
		OK:            true,
		Path:          source.Path,
		SHA256:        source.SHA256,
		SchemaVersion: policy.SchemaVersion,
		Default:       policy.Default,
		Rules:         len(policy.Rules),
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, out)
	}
	fmt.Fprintf(stdout, "Policy valid: %s (%d rule(s), sha256 %s)\n", out.Path, out.Rules, out.SHA256)
	return nil
}

func runModelPolicyEvaluate(args []string, stdout *os.File) error {
	maxTokensSet := hasFlagValue(args, "max-tokens")
	streamSet := hasFlagValue(args, "stream")
	fs := flag.NewFlagSet("model policy evaluate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	method := fs.String("method", http.MethodGet, "Request method")
	requestPath := fs.String("path", "/v1/models", "Request path as seen by the mediator")
	workspaceID := fs.String("workspace-id", "", "Workspace ID")
	capability := fs.String("capability", hostworker.DefaultCapability, "Capability")
	workerID := fs.String("worker-id", "policy-evaluate", "Worker ID")
	modelName := fs.String("model", "", "Declared request model")
	requestBytes := fs.Int64("request-bytes", 0, "Request body size in bytes")
	textBytes := fs.Int64("text-bytes", 0, "Aggregate prompt/message text byte count")
	messages := fs.Int("messages", 0, "Message count")
	maxTokens := fs.Int("max-tokens", 0, "Declared max_tokens value")
	streamRaw := fs.String("stream", "", "Declared stream mode: true or false")
	expect := fs.String("expect", "", "Expected decision: allow or deny")
	var tools multiFlag
	fs.Var(&tools, "tool", "Declared tool/function name (repeatable)")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model policy evaluate <policy.json> [--method <method>] [--path <path>] [--model <model>] [--max-tokens <n>] [--stream true|false] [--tool <name>] [--expect allow|deny]")
	}
	policy, source, err := hostworker.LoadFilePolicy(fs.Arg(0))
	if err != nil {
		return err
	}
	body := &hostworker.DecisionRequestBody{
		Model:        strings.TrimSpace(*modelName),
		MessageCount: *messages,
		TextBytes:    *textBytes,
		ToolNames:    append([]string{}, tools...),
	}
	if streamSet {
		parsed, err := strconv.ParseBool(strings.TrimSpace(*streamRaw))
		if err != nil {
			return fmt.Errorf("--stream must be true or false")
		}
		body.Stream = &parsed
	}
	if maxTokensSet {
		value := *maxTokens
		body.MaxTokens = &value
	}
	envelope := hostworker.DecisionEnvelope{
		SchemaVersion: 1,
		RequestID:     "policy-evaluate",
		Workspace:     hostworker.DecisionWorkspace{ID: strings.TrimSpace(*workspaceID)},
		Capability:    strings.TrimSpace(*capability),
		Worker: hostworker.DecisionWorker{
			ID:       strings.TrimSpace(*workerID),
			Protocol: "openai-compatible",
		},
		Request: hostworker.DecisionRequest{
			Method: strings.ToUpper(strings.TrimSpace(*method)),
			Path:   strings.TrimSpace(*requestPath),
			Bytes:  *requestBytes,
			Body:   body,
		},
	}
	decision := policy.Decide(envelope, source, "policy-evaluate")
	expected := strings.ToLower(strings.TrimSpace(*expect))
	if expected != "" && expected != "allow" && expected != "deny" {
		return fmt.Errorf("--expect must be allow or deny")
	}
	out := modelPolicyEvaluationOutput{
		OK:            true,
		Path:          source.Path,
		SHA256:        source.SHA256,
		Decision:      decision.Decision,
		Reason:        decision.Reason,
		RuleID:        decision.PolicyRuleID,
		AuditEventID:  decision.AuditEventID,
		Expected:      expected,
		MatchedExpect: expected == "" || expected == decision.Decision,
	}
	if outputJSON(stdout) {
		if err := writeJSON(stdout, out); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "%s\t%s", out.Decision, out.Reason)
		if out.RuleID != "" {
			fmt.Fprintf(stdout, "\t%s", out.RuleID)
		}
		fmt.Fprintln(stdout)
	}
	if !out.MatchedExpect {
		return fmt.Errorf("policy decision %s did not match expected %s", out.Decision, expected)
	}
	return nil
}

func printModelPolicyHelp(stdout io.Writer) {
	fmt.Fprint(stdout, `microagent model policy

Validate or dry-run experimental model mediation policy files.

Usage:
  microagent model policy validate <policy.json>
  microagent model policy evaluate <policy.json> [options]

Evaluate options:
  --method <method>       Request method (default GET)
  --path <path>           Request path as seen by the mediator (default /v1/models)
  --workspace-id <id>     Workspace ID
  --capability <name>     Capability (default model.openai)
  --worker-id <id>        Worker ID
  --model <model>         Declared request model
  --request-bytes <n>     Request body byte count
  --text-bytes <n>        Aggregate prompt/message text byte count
  --messages <n>          Message count
  --max-tokens <n>        Declared max_tokens value
  --stream true|false     Declared stream mode
  --tool <name>           Declared tool/function name (repeatable)
  --expect allow|deny     Fail if the evaluated decision differs
`)
}

func runModelPull(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	token := fs.String("token", "", "HuggingFace bearer token (else HF_TOKEN/HUGGING_FACE_HUB_TOKEN)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model pull <hf-ref> [--token <t>] [--state-dir <dir>]")
	}
	record, err := model.Pull(context.Background(), model.PullOptions{StateDir: stateDir, ModelRef: fs.Arg(0), Token: *token})
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Pulled %s (%d bytes, %s)\n", record.ModelRef, record.SizeBytes, record.Digest)
	return nil
}

func runModelList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model ls", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	list, err := model.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"models": list})
	}
	for _, m := range list {
		fmt.Fprintf(stdout, "%s\t%d\t%s\n", m.ModelRef, m.SizeBytes, m.Digest)
	}
	return nil
}

func runModelRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	keepFiles := fs.Bool("keep-files", false, "Remove the index entry but keep the blob on disk")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model delete <ref> [--keep-files] [--state-dir <dir>]")
	}
	res, err := model.Remove(stateDir, fs.Arg(0), !*keepFiles)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, res)
	}
	fmt.Fprintf(stdout, "Removed %d model(s)\n", len(res.Removed))
	return nil
}

func runModelPrune(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	deleteFiles := fs.Bool("delete-files", false, "Also delete blob files for pruned entries")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	res, err := model.Prune(stateDir, *deleteFiles)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, res)
	}
	fmt.Fprintf(stdout, "Pruned %d model(s)\n", len(res.Removed))
	return nil
}

func runModelServe(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printModelServeHelp(stdout)
		return nil
	}
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dedicated := fs.Bool("dedicated", false, "Start a dedicated runner instead of sharing one")
	token := fs.String("token", "", "HuggingFace token for auto-pull (else HF_TOKEN/HUGGING_FACE_HUB_TOKEN)")
	runnerBackend := fs.String("runner", "", "Model runner backend: llamacpp, vllm, or custom")
	runnerGPU := fs.String("runner-gpu", "", "Model runner GPU intent: off, on, or auto")
	runnerModel := fs.String("runner-model", "", "Backend model id for runners such as vLLM")
	runnerServedModel := fs.String("runner-served-model", "", "OpenAI-compatible served model name for runners such as vLLM")
	runnerCommand := fs.String("runner-command", "", "Host model runner command template")
	runnerName := fs.String("runner-name", "", "Host model runner name for state output")
	runnerHealthPath := fs.String("runner-health-path", "", "Host model runner health probe path")
	var runnerArgs multiFlag
	var runnerEnv multiFlag
	fs.Var(&runnerArgs, "runner-arg", "Extra model runner argument (repeatable)")
	fs.Var(&runnerEnv, "runner-env", "Extra model runner environment KEY=VALUE (repeatable)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model serve <hf-ref> [--dedicated] [--runner <llamacpp|vllm|custom>] [--runner-gpu <off|on|auto>] [--runner-model <id>] [--runner-served-model <name>] [--runner-command <template>] [--runner-name <name>] [--runner-health-path <path>] [--runner-arg <arg>] [--runner-env KEY=VALUE] [--token <t>] [--state-dir <dir>]")
	}
	ref := fs.Arg(0)
	canonical, _, err := model.Resolve(ref)
	if err != nil {
		return err
	}
	rec, err := model.Find(stateDir, canonical)
	if err != nil {
		// Not in the store yet — auto-pull, like run does for images.
		rec, err = model.Pull(context.Background(), model.PullOptions{StateDir: stateDir, ModelRef: ref, Token: *token})
		if err != nil {
			return err
		}
	}
	engine, runnerConfig, err := resolveModelRunner(modelRunnerOverrides{
		Backend:      *runnerBackend,
		GPU:          *runnerGPU,
		BackendModel: *runnerModel,
		ServedModel:  *runnerServedModel,
		CommandRaw:   *runnerCommand,
		Name:         *runnerName,
		HealthPath:   *runnerHealthPath,
		Args:         runnerArgs,
		Env:          runnerEnv,
	})
	if err != nil {
		return err
	}
	runner, err := modelrunner.Ensure(context.Background(), modelrunner.EnsureOptions{
		StateDir:     stateDir,
		ModelRef:     rec.ModelRef,
		ModelPath:    rec.OutputPath,
		Engine:       engine,
		Pinned:       true,
		Dedicated:    *dedicated,
		RunnerConfig: runnerConfig,
	})
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, runner)
	}
	fmt.Fprintf(stdout, "Serving %s on %s:%d (pid %d)\n", runner.ModelRef, runner.Host, runner.Port, runner.PID)
	return nil
}

func printModelServeHelp(stdout io.Writer) {
	fmt.Fprint(stdout, `microagent model serve <hf-ref>
microagent model serve <hf-ref>

Start or reuse a pinned host model runner process for a HuggingFace GGUF model.

Options:
  --dedicated                    Start a dedicated runner instead of sharing one
  --runner <backend>             Model runner backend: llamacpp, vllm, or custom
  --runner-gpu <mode>            Model runner GPU intent: off, on, or auto
  --runner-model <id>            Backend model id for runners such as vLLM
  --runner-served-model <name>   OpenAI-compatible served model name
  --runner-command <template>    Host model runner command template
  --runner-name <name>           Host model runner name for state output
  --runner-health-path <path>    Host model runner health probe path
  --runner-arg <arg>             Extra model runner argument (repeatable)
  --runner-env KEY=VALUE         Extra model runner environment override (repeatable)
  --token <t>                    HuggingFace token for auto-pull
  --state-dir <dir>              State directory
`)
}

func runModelStop(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model stop <hf-ref> [--state-dir <dir>]")
	}
	canonical, _, err := model.Resolve(fs.Arg(0))
	if err != nil {
		return err
	}
	n, err := modelrunner.Stop(stateDir, canonical)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"stopped": n})
	}
	fmt.Fprintf(stdout, "Stopped %d runner(s)\n", n)
	return nil
}

func runModelRunners(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model runners", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	list, err := modelrunner.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"runners": list})
	}
	for _, r := range list {
		fmt.Fprintf(stdout, "%s\t%s:%d\tpid=%d\tholders=%d\tpinned=%t\n", r.ModelRef, r.Host, r.Port, r.PID, len(r.Holders), r.Pinned)
	}
	return nil
}

func runVolume(ctx context.Context, args []string, stdout *os.File) error {
	if wantsHelp(args) || len(args) == 0 {
		printVolumeHelp(stdout)
		return nil
	}
	switch args[0] {
	case "create":
		return runVolumeCreate(ctx, args[1:], stdout)
	case "list":
		return runVolumeList(args[1:], stdout)
	case "delete":
		return runVolumeRemove(args[1:], stdout)
	case "status":
		return runVolumeInspect(args[1:], stdout)
	}
	return fmt.Errorf("unknown volume command %q; see microagent volume --help", args[0])
}

func runVolumeCreate(ctx context.Context, args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	var sizeMiB int64
	fs := flag.NewFlagSet("volume create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Int64Var(&sizeMiB, "size-mib", 0, "Volume size in MiB (default 1024)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume create <name> [--size-mib <n>] [--state-dir <dir>]")
	}
	record, err := volume.Create(ctx, stateDir, hostBackend(), fs.Arg(0), sizeMiB, defaultMke2fsPath())
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Created volume %q (%d MiB)\n", record.Name, record.SizeMiB)
	return nil
}

func runVolumeList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("volume list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent volume list [--state-dir <dir>]")
	}
	records, err := volume.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"volumes": records})
	}
	fmt.Fprintf(stdout, "%-20s %-10s %s\n", "NAME", "SIZE-MIB", "ATTACHED")
	for _, r := range records {
		attached := r.AttachedTo
		if attached == "" {
			attached = "-"
		}
		fmt.Fprintf(stdout, "%-20s %-10d %s\n", r.Name, r.SizeMiB, attached)
	}
	return nil
}

func runVolumeRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	force := false
	fs := flag.NewFlagSet("volume delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&force, "force", false, "Remove even if the volume is attached")
	fs.BoolVar(&force, "f", false, "Remove even if the volume is attached")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume delete <name> [--force] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := volume.Remove(stateDir, name, force, workspaceRunningPredicate(stateDir)); err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"removed": name})
	}
	fmt.Fprintf(stdout, "Removed volume %q\n", name)
	return nil
}

func runVolumeInspect(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("volume inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume inspect <name> [--state-dir <dir>]")
	}
	record, err := volume.Get(stateDir, fs.Arg(0))
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	attached := record.AttachedTo
	if attached == "" {
		attached = "-"
	}
	fmt.Fprintf(stdout, "Name:     %s\n", record.Name)
	fmt.Fprintf(stdout, "Size:     %d MiB\n", record.SizeMiB)
	fmt.Fprintf(stdout, "Created:  %s\n", record.CreatedAt)
	fmt.Fprintf(stdout, "Attached: %s\n", attached)
	fmt.Fprintf(stdout, "Path:     %s\n", volume.DiskPath(stateDir, hostBackend(), record.Name))
	return nil
}

// workspaceRunningPredicate reports whether a workspace is in a state that
// still holds its volumes (it could be using the disk). A workspace with no
// event, or one that is stopped/halted/failed, is reclaimable.
func workspaceRunningPredicate(stateDir string) func(string) bool {
	return func(name string) bool {
		event, err := workspace.ReadEvent(workspace.Options{StateDir: stateDir, Name: name})
		if err != nil {
			return false
		}
		switch event.State {
		case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined:
			return true
		default:
			return false
		}
	}
}

func printVolumeHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent volume

Manage user-defined named volumes: VM-independent ext4 disks attached by name.

Usage:
  microagent volume create <name> [options]  Create a named volume
  microagent volume list                      List named volumes
  microagent volume inspect <name>            Show one volume
  microagent volume delete <name> [options]       Remove a named volume

Attach a volume to a workspace by name with --volume <name>:/mount, e.g.
  microagent run IMAGE --volume data:/work

A volume is single-attach: at most one running workspace holds it at a time.

Options:
  --size-mib <n>        Volume size in MiB for create (default 1024)
  --force               Remove a volume even if it is attached
  --state-dir <dir>     State directory
`)
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
		// Leave any named networks so a deleted workspace frees its address.
		_ = network.LeaveAll(opts.StateDir, opts.Name)
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
	fs.StringVar(&opts.ExecCommand, "exec", "", "Shell command to run as guest init")
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
	fs.StringVar(&opts.Network.Mode, "network", opts.Network.Mode, "Network mode: user, nat, isolated, bridged, or named")
	fs.StringVar(&opts.Network.Interface, "network-interface", opts.Network.Interface, "Host interface for bridged network mode")
	fs.StringVar(&opts.Network.Name, "network-name", opts.Network.Name, "Join a user-defined named network by name")
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
	fs.BoolVar(&opts.Keep, "keep", false, "Keep workspace state after run")
	rm := false
	fs.BoolVar(&rm, "rm", false, "Remove workspace state after run")
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
		} else if command == "run" {
			if err := applyContainerRunArgs(&opts, fs.Args()); err != nil {
				return workspaceOptions{}, err
			}
		} else {
			return workspaceOptions{}, fmt.Errorf("unexpected %s argument: %s", command, fs.Arg(0))
		}
	}
	if strings.TrimSpace(modelRunnerCommand) != "" {
		command, err := modelrunner.ParseRunnerCommand(modelRunnerCommand)
		if err != nil {
			return workspaceOptions{}, fmt.Errorf("model runner command: %w", err)
		}
		opts.ModelRunner.Command = command
	}
	opts.ModelRunner.Args = append([]string{}, modelRunnerArgs...)
	opts.ModelRunner.Env = append([]string{}, modelRunnerEnv...)
	opts.SetupCommands = append([]string{}, setupCommands...)
	setupFileCommands, err := setupCommandsFromFiles(setupFiles, ".")
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.SetupCommands = append(opts.SetupCommands, setupFileCommands...)
	env, err := parseEnvFlags(envVars)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Env = mergeEnv(opts.Env, env)
	secrets, err := parseSecretFlags(secretFlags)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Secrets = secrets
	if strings.TrimSpace(secretsEnvFile) != "" {
		opts.SecretEnvFiles = []string{secretsEnvFile}
	}
	onDemand, err := parseSecretFlags(secretOnDemandFlags)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.OnDemandSecrets = onDemand
	opts.SecretsAudit = secretsAudit
	volumes, err := parseWorkspaceVolumes(volumeFlags)
	if err != nil {
		return workspaceOptions{}, err
	}
	disks, err := parseWorkspaceDisks(diskFlags, false)
	if err != nil {
		return workspaceOptions{}, err
	}
	bundles, err := parseWorkspaceDisks(bundleFlags, true)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Disks = append(opts.Disks, volumes...)
	opts.Disks = append(opts.Disks, disks...)
	opts.Disks = append(opts.Disks, bundles...)
	outputs, err := parseWorkspaceOutputs(outputFlags)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Outputs = append(opts.Outputs, outputs...)
	published, err := parsePortForwardMappings(publishFlags)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Network.PortForwards = append(opts.Network.PortForwards, published...)
	if strings.TrimSpace(mediationMapping) != "" {
		mediation, err := parseMediationMapping(mediationMapping, mediationOptional)
		if err != nil {
			return workspaceOptions{}, err
		}
		opts.Mediation = &mediation
	} else if mediationOptional {
		return workspaceOptions{}, fmt.Errorf("%s requires --mediation with --mediation-optional", command)
	}
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		if command == "create" {
			opts.ImageRef = defaultWorkspaceImage(opts.Architecture)
		} else {
			return workspaceOptions{}, fmt.Errorf("%s requires --image", command)
		}
	}
	if command == "run" && strings.TrimSpace(opts.ExecCommand) == "" {
		opts.UseImageCommand = true
	}
	if err := validateConsoleShell(opts.ConsoleShell); err != nil {
		return workspaceOptions{}, err
	}
	if strings.TrimSpace(opts.Hostname) == "" && strings.TrimSpace(opts.Name) != "" {
		opts.Hostname = workspace.DefaultHostname(opts.Name)
	}
	if err := validateHostname(opts.Hostname); err != nil {
		return workspaceOptions{}, err
	}
	if !kernelExplicit {
		opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	opts.KernelExplicit = kernelExplicit
	if err := validateRestartPolicy(opts.RestartPolicy); err != nil {
		return workspaceOptions{}, err
	}
	opts.RestartPolicy = normalizeRestartPolicy(opts.RestartPolicy)
	// --network-name selects a user-defined named network; it implies named mode
	// unless the operator explicitly chose a different mode (which is a conflict).
	if strings.TrimSpace(opts.Network.Name) != "" {
		switch strings.TrimSpace(opts.Network.Mode) {
		case "", defaultNetworkMode, "named":
			opts.Network.Mode = "named"
		default:
			return workspaceOptions{}, fmt.Errorf("--network-name cannot be combined with --network %s; named networks use their own managed bridge", opts.Network.Mode)
		}
	}
	opts.Network = normalizeNetworkConfig(opts.Network)
	if err := vmkit.ValidateNetworkConfig(opts.Network); err != nil {
		return workspaceOptions{}, err
	}
	if command != "create" && strings.TrimSpace(opts.ServiceCommand) != "" {
		return workspaceOptions{}, fmt.Errorf("%s does not support --service-command", command)
	}
	if opts.UseImageCommand && strings.TrimSpace(opts.ServiceCommand) != "" {
		return workspaceOptions{}, fmt.Errorf("%s cannot use both --image-command and --service-command", command)
	}
	if command == "run" && rm && opts.Keep {
		return workspaceOptions{}, fmt.Errorf("run cannot use both --rm and --keep")
	}
	if command != "run" && rm {
		return workspaceOptions{}, fmt.Errorf("%s does not support --rm", command)
	}
	opts.SerialInput = backendSupportsConsoleInput(opts.Backend)
	if specExplicit && specPath == "" {
		return workspaceOptions{}, fmt.Errorf("%s requires --file path", command)
	}
	if err := applyResourceProfile(&opts, memoryExplicit || opts.SpecMemory, cpusExplicit || opts.SpecCPU, sizeExplicit || opts.SpecSize); err != nil {
		return workspaceOptions{}, err
	}
	if err := validateResourceConfig(workspaceResources(opts), true); err != nil {
		return workspaceOptions{}, err
	}
	if timeoutSeconds <= 0 {
		return workspaceOptions{}, fmt.Errorf("%s timeout must be positive", command)
	}
	if resultPort > uint(^uint32(0)) {
		return workspaceOptions{}, fmt.Errorf("%s result port is too large", command)
	}
	opts.ResultPort = uint32(resultPort)
	opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	return opts, nil
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
	defaultKernel, ok := defaultKernel(opts.Backend, opts.Architecture)
	if !ok {
		return fmt.Errorf("no default kernel for %s/%s; pass --kernel", opts.Backend, opts.Architecture)
	}
	_, err := kernel.Install(ctx, kernel.InstallOptions{
		URL:        defaultKernel.URL,
		SHA256:     defaultKernel.SHA256,
		OutputPath: opts.KernelPath,
	})
	return err
}

func workspaceSpecPath(command string, args []string) string {
	if command != "create" {
		return ""
	}
	if value, ok := flagValue(args, "file"); ok {
		return value
	}
	if _, err := os.Stat("microagent.yaml"); err == nil {
		return "microagent.yaml"
	}
	if _, err := os.Stat("microagent.yml"); err == nil {
		return "microagent.yml"
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

func workspaceRequest(opts workspaceOptions, command, rootfsPath string) vmkit.Request {
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

func writeJSON(stdout *os.File, value any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeVersion(stdout *os.File) error {
	if outputStructured() {
		return writeJSON(stdout, map[string]any{
			"name":    "microagent",
			"version": version,
		})
	}
	fmt.Fprintf(stdout, "microagent %s\n", version)
	return nil
}

func outputStructured() bool {
	return currentOutputMode() == outputModeAX || outputFormat == "json"
}

func outputJSON(stdout *os.File) bool {
	if currentOutputMode() == outputModeAX {
		return true
	}
	switch outputFormat {
	case "json":
		return true
	case "text":
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MICROAGENT_OUTPUT"))) {
	case "json":
		return true
	case "text", "human":
		return false
	}
	info, err := stdout.Stat()
	if err != nil {
		return true
	}
	return info.Mode()&os.ModeCharDevice == 0
}

func rootfsProgress(stdout *os.File, prefix string) rootfs.ProgressFunc {
	if outputJSON(stdout) {
		return nil
	}
	printer := &progressPrinter{
		out:         os.Stderr,
		prefix:      prefix,
		interactive: fileIsTerminal(os.Stderr),
	}
	return printer.print
}

type progressPrinter struct {
	out         *os.File
	prefix      string
	interactive bool
	active      bool
}

func (p *progressPrinter) print(event rootfs.ProgressEvent) {
	line := fmt.Sprintf("%s: %s", p.prefix, formatProgressEvent(event))
	if !p.interactive {
		fmt.Fprintln(p.out, line)
		return
	}
	if isProgressEvent(event) {
		fmt.Fprintf(p.out, "\r\033[2K%s", line)
		p.active = true
		if event.Phase == "complete" {
			fmt.Fprintln(p.out)
			p.active = false
		}
		return
	}
	if p.active {
		fmt.Fprintln(p.out)
		p.active = false
	}
	fmt.Fprintln(p.out, line)
}

func isProgressEvent(event rootfs.ProgressEvent) bool {
	return event.Indeterminate || event.Total > 0 || event.TotalBytes > 0
}

func formatProgressEvent(event rootfs.ProgressEvent) string {
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = event.Phase
	}
	if event.Indeterminate {
		elapsed := event.Current
		if elapsed < 0 {
			elapsed = 0
		}
		spinner := []string{"|", "/", "-", "\\"}
		if elapsed > 0 {
			return fmt.Sprintf("[%s] %s (%s)", spinner[elapsed%int64(len(spinner))], message, formatElapsed(elapsed))
		}
		return fmt.Sprintf("[%s] %s", spinner[0], message)
	}
	if event.Total <= 0 && event.TotalBytes <= 0 {
		return message
	}
	var done, total int64
	if event.TotalBytes > 0 {
		done = event.Bytes
		total = event.TotalBytes
	} else {
		done = event.Current
		total = event.Total
	}
	if total <= 0 {
		return message
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	bar := progressBar(done, total, 20)
	if event.TotalBytes > 0 {
		if event.Total > 0 {
			return fmt.Sprintf("%s %s %s/%s (layer %d/%d)", bar, message, formatBytes(done), formatBytes(total), event.Current, event.Total)
		}
		return fmt.Sprintf("%s %s %s/%s", bar, message, formatBytes(done), formatBytes(total))
	}
	return fmt.Sprintf("%s %s %d/%d", bar, message, event.Current, event.Total)
}

func progressBar(done, total int64, width int) string {
	if width <= 0 {
		width = 20
	}
	filled := 0
	if total > 0 {
		filled = int(done * int64(width) / total)
	}
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", width-filled) + "]"
}

func formatElapsed(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	for _, suffix := range units {
		size /= unit
		if size < unit {
			return fmt.Sprintf("%.1f%s", size, suffix)
		}
	}
	return fmt.Sprintf("%.1fPiB", size/unit)
}

func fileIsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func parseGlobalFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--mode":
			if i+1 < len(args) {
				globalOutputMode = normalizeOutputMode(args[i+1])
				i++
			} else {
				out = append(out, args[i])
			}
		case "--json":
			outputFormat = "json"
		case "--text", "--human":
			outputFormat = "text"
		case "--output":
			if i+1 < len(args) {
				outputFormat = normalizeOutputFormat(args[i+1])
				i++
			} else {
				out = append(out, args[i])
			}
		default:
			if strings.HasPrefix(args[i], "--mode=") {
				globalOutputMode = normalizeOutputMode(strings.TrimPrefix(args[i], "--mode="))
				continue
			}
			if strings.HasPrefix(args[i], "--output=") {
				outputFormat = normalizeOutputFormat(strings.TrimPrefix(args[i], "--output="))
				continue
			}
			out = append(out, args[i:]...)
			return out
		}
	}
	return out
}

func normalizeOutputFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json":
		return "json"
	case "text", "human":
		return "text"
	default:
		return ""
	}
}

func writeDoctorResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	fmt.Fprintf(stdout, "Backend: %s\n", nonEmpty(resp.Backend, "unknown"))
	fmt.Fprintf(stdout, "Status: %s\n", humanOK(resp.OK))
	if resp.Host != nil {
		fmt.Fprintf(stdout, "Host: %s", nonEmpty(resp.Host.Architecture, "unknown"))
		if resp.Host.SupervisorPath != "" {
			fmt.Fprintf(stdout, ", supervisor=%s", resp.Host.SupervisorPath)
		}
		if resp.Host.SupervisorAvailable {
			fmt.Fprint(stdout, ", supervisor available")
		}
		if resp.Host.FrameworkAvailable {
			fmt.Fprint(stdout, ", framework available")
		}
		if resp.Host.VirtualizationSupported {
			fmt.Fprint(stdout, ", virtualization supported")
		}
		if resp.Host.KVMAvailable {
			fmt.Fprint(stdout, ", KVM available")
		}
		if resp.Host.VsockAvailable {
			fmt.Fprint(stdout, ", vsock available")
		}
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "Console: %s", availability(resp.Host.ConsoleAvailable))
		if resp.Host.ConsoleMode != "" {
			fmt.Fprintf(stdout, " (%s)", resp.Host.ConsoleMode)
		}
		fmt.Fprintln(stdout)
		printNetworkingSection(stdout, resp.Host)
	}
	if resp.Kernel != nil {
		fmt.Fprintf(stdout, "Kernel: %s", nonEmpty(resp.Kernel.Status, "unknown"))
		if resp.Kernel.Path != "" {
			fmt.Fprintf(stdout, " (%s)", resp.Kernel.Path)
		}
		fmt.Fprintln(stdout)
	}
	if resp.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
	}
	return nil
}

func printNetworkingSection(stdout *os.File, host *vmkit.HostSupport) {
	if host == nil {
		return
	}
	ready := func(b bool) string {
		if b {
			return "ready"
		}
		return "unavailable"
	}
	fmt.Fprintf(stdout, "Networking: isolated %s, user %s, nat/bridged/named %s\n",
		ready(host.IsolatedNetworkReady),
		ready(host.UserNetworkReady),
		ready(host.PrivilegedNetworkReady))
	if hint := diagnostics.NetworkRemediation(host); hint != "" {
		fmt.Fprintf(stdout, "  %s\n", hint)
	}
}

func writeRuntimeContract(stdout *os.File, contract vmkit.RuntimeContract) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, contract)
	}
	fmt.Fprintf(stdout, "Contract: %s\n", contract.Version)
	fmt.Fprintf(stdout, "Backends: %s\n", strings.Join(contract.Backends, ", "))
	fmt.Fprintf(stdout, "Commands: %s\n", strings.Join(contractItemNames(contract.Commands), ", "))
	fmt.Fprintf(stdout, "States: %s\n", strings.Join(contractStateNames(contract.States), ", "))
	fmt.Fprintf(stdout, "Readiness: %s\n", strings.Join(contractItemNames(contract.ReadinessSignals), ", "))
	fmt.Fprintf(stdout, "Result: %s\n", strings.Join(contractItemNames(contract.ResultFields), ", "))
	fmt.Fprintf(stdout, "Artifacts: %s\n", strings.Join(contractItemNames(contract.ArtifactChannels), ", "))
	fmt.Fprintf(stdout, "Mediation: %s\n", contract.Mediation.Primitive)
	return nil
}

func writeResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	fmt.Fprintf(stdout, "Status: %s\n", humanOK(resp.OK))
	if resp.Backend != "" {
		fmt.Fprintf(stdout, "Backend: %s\n", resp.Backend)
	}
	if resp.Event != nil {
		fmt.Fprintf(stdout, "Workspace: %s\n", resp.Event.Identity.RuntimeID)
		fmt.Fprintf(stdout, "State: %s\n", resp.Event.State)
		if resp.RestartPolicy != "" {
			fmt.Fprintf(stdout, "Restart: %s\n", resp.RestartPolicy)
		}
		if resp.Network != nil && resp.Network.Mode != "" {
			fmt.Fprintf(stdout, "Network: %s\n", resp.Network.Mode)
		}
		if resp.Mediation != nil && resp.Mediation.Enabled {
			fmt.Fprintf(stdout, "Mediation: required=%t failClosed=%t port=%d target=%s\n", resp.Mediation.Required, resp.Mediation.FailClosed, resp.Mediation.Port, resp.Mediation.Target)
		}
		if resp.Verification != nil {
			fmt.Fprintf(stdout, "Verification: %s\n", humanOK(resp.Verification.OK))
		}
		if resp.Readiness != nil {
			mediation := "disabled"
			if resp.Mediation != nil && resp.Mediation.Enabled {
				mediation = humanReady(resp.Readiness.MediationReady.Ready)
			}
			fmt.Fprintf(stdout, "Readiness: guest=%s shell=%s result=%s mediation=%s\n",
				humanReady(resp.Readiness.GuestReady.Ready),
				humanReady(resp.Readiness.ShellReady.Ready),
				humanReady(resp.Readiness.ResultReady.Ready),
				mediation,
			)
		}
		if resp.Artifacts != nil {
			fmt.Fprintf(stdout, "Artifacts: ingress=%d egress=%d\n", len(resp.Artifacts.Ingress), len(resp.Artifacts.Egress))
		}
		if resp.Result != nil {
			fmt.Fprintf(stdout, "Exit code: %d\n", resp.Result.ExitCode)
			if resp.Result.CompletedAt != "" {
				fmt.Fprintf(stdout, "Completed: %s\n", resp.Result.CompletedAt)
			}
		}
		if resp.Event.Detail != "" {
			fmt.Fprintf(stdout, "Detail: %s\n", resp.Event.Detail)
		}
	}
	if resp.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
	}
	return nil
}

func contractItemNames(items []vmkit.ContractItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func contractStateNames(states []vmkit.ContractState) []string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, string(state.Name))
	}
	return names
}

func writeResultResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	if resp.Result == nil {
		if resp.Error != "" {
			fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
		}
		return nil
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", resp.Result.Identity.RuntimeID)
	if resp.Result.Backend != "" {
		fmt.Fprintf(stdout, "Backend: %s\n", resp.Result.Backend)
	}
	fmt.Fprintf(stdout, "Exit code: %d\n", resp.Result.ExitCode)
	if resp.Result.StartedAt != "" {
		fmt.Fprintf(stdout, "Started: %s\n", resp.Result.StartedAt)
	}
	if resp.Result.CompletedAt != "" {
		fmt.Fprintf(stdout, "Completed: %s\n", resp.Result.CompletedAt)
	}
	if resp.Result.ResultPath != "" {
		fmt.Fprintf(stdout, "Result: %s\n", resp.Result.ResultPath)
	}
	if strings.TrimSpace(resp.Result.Stdout) != "" {
		fmt.Fprintf(stdout, "\n%s", sanitizeHumanOutput(resp.Result.Stdout))
		if !strings.HasSuffix(resp.Result.Stdout, "\n") {
			fmt.Fprintln(stdout)
		}
	}
	if strings.TrimSpace(resp.Result.Stderr) != "" {
		fmt.Fprintf(stdout, "\nStderr:\n%s", sanitizeHumanOutput(resp.Result.Stderr))
		if !strings.HasSuffix(resp.Result.Stderr, "\n") {
			fmt.Fprintln(stdout)
		}
	}
	if resp.Result.Error != "" {
		fmt.Fprintf(stdout, "Result error: %s\n", resp.Result.Error)
	}
	if resp.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
	}
	return nil
}

func writeWorkspaceResult(stdout *os.File, result workspaceResult) error {
	return writeWorkspaceResultWithOptions(stdout, result, workspaceResultOptions{})
}

type workspaceResultOptions struct {
	SuppressSuccessfulResult bool
	CreatedSummary           bool
}

func writeWorkspaceResultWithOptions(stdout *os.File, result workspaceResult, opts workspaceResultOptions) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	if opts.CreatedSummary {
		fmt.Fprintf(stdout, "Created workspace: %s\n", result.Workspace)
	} else {
		fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	}
	if result.Response.Event != nil {
		fmt.Fprintf(stdout, "State: %s\n", humanWorkspaceState(result.Response.Event.State, opts))
	} else if result.FinalState != "" {
		fmt.Fprintf(stdout, "State: %s\n", humanWorkspaceState(vmkit.VMState(result.FinalState), opts))
	}
	if result.RootfsPath != "" {
		fmt.Fprintf(stdout, "Rootfs: %s\n", result.RootfsPath)
	}
	if result.Profile != "" {
		fmt.Fprintf(stdout, "Profile: %s\n", result.Profile)
	}
	if result.Restart != "" {
		fmt.Fprintf(stdout, "Restart: %s\n", result.Restart)
	}
	if result.Network.Mode != "" {
		fmt.Fprintf(stdout, "Network: %s\n", result.Network.Mode)
	}
	if strings.TrimSpace(result.ConsoleShell) != "" {
		fmt.Fprintf(stdout, "Shell: %s\n", strings.TrimSpace(result.ConsoleShell))
	}
	if strings.TrimSpace(result.Hostname) != "" {
		fmt.Fprintf(stdout, "Hostname: %s\n", strings.TrimSpace(result.Hostname))
	}
	if len(result.Artifacts.Ingress) != 0 || len(result.Artifacts.Egress) != 0 {
		fmt.Fprintf(stdout, "Artifacts: ingress=%d egress=%d\n", len(result.Artifacts.Ingress), len(result.Artifacts.Egress))
	}
	if result.Resources.MemoryMiB != 0 || result.Resources.CPUCount != 0 || result.Resources.SizeMiB != 0 {
		fmt.Fprintf(stdout, "Resources: memory=%dMiB cpus=%d", result.Resources.MemoryMiB, result.Resources.CPUCount)
		if result.Resources.SizeMiB != 0 {
			fmt.Fprintf(stdout, " disk=%dMiB", result.Resources.SizeMiB)
		}
		fmt.Fprintln(stdout)
	}
	if result.KernelPath != "" {
		fmt.Fprintf(stdout, "Kernel: %s\n", result.KernelPath)
	}
	if result.SerialPath != "" {
		fmt.Fprintf(stdout, "Console log: %s\n", result.SerialPath)
	}
	if result.Result != nil && !(opts.SuppressSuccessfulResult && result.Result.ExitCode == 0 && strings.TrimSpace(result.Result.Error) == "") {
		fmt.Fprintf(stdout, "Exit code: %d\n", result.Result.ExitCode)
		if strings.TrimSpace(result.Result.Stdout) != "" {
			fmt.Fprintf(stdout, "\n%s", sanitizeHumanOutput(result.Result.Stdout))
			if !strings.HasSuffix(result.Result.Stdout, "\n") {
				fmt.Fprintln(stdout)
			}
		}
		if strings.TrimSpace(result.Result.Stderr) != "" {
			fmt.Fprintf(stdout, "\nStderr:\n%s", sanitizeHumanOutput(result.Result.Stderr))
			if !strings.HasSuffix(result.Result.Stderr, "\n") {
				fmt.Fprintln(stdout)
			}
		}
	}
	if result.Response.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", result.Response.Error)
	}
	return nil
}

func writeCreateResult(stdout *os.File, result workspaceResult, err error) error {
	return writeWorkspaceResultWithOptions(stdout, result, workspaceResultOptions{
		SuppressSuccessfulResult: err == nil,
		CreatedSummary:           err == nil,
	})
}

func humanWorkspaceState(state vmkit.VMState, opts workspaceResultOptions) string {
	if opts.CreatedSummary && state == vmkit.StateStopped {
		return "ready (stopped)"
	}
	return string(state)
}

func writeApplyResult(stdout *os.File, result applyResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.State != "" {
		fmt.Fprintf(stdout, "State: %s\n", result.State)
	}
	if len(result.Applied) != 0 {
		fmt.Fprintf(stdout, "Applied: %s\n", strings.Join(result.Applied, ", "))
	}
	if result.Network.Mode != "" {
		fmt.Fprintf(stdout, "Network: %s\n", result.Network.Mode)
	}
	for _, forward := range result.Network.PortForwards {
		host := strings.TrimSpace(forward.Host)
		if host == "" {
			host = "127.0.0.1"
		}
		protocol := strings.TrimSpace(forward.Protocol)
		if protocol == "" {
			protocol = "tcp"
		}
		fmt.Fprintf(stdout, "Forward: %s:%d -> %d/%s\n", host, forward.HostPort, forward.GuestPort, protocol)
	}
	if result.Reloaded {
		fmt.Fprintln(stdout, "Reloaded: port forwards")
	}
	if result.Response != nil && result.Response.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", result.Response.Error)
	}
	return nil
}

func sanitizeHumanOutput(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

func writeCopyResult(stdout *os.File, result copyResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.Artifact != "" {
		fmt.Fprintf(stdout, "Artifact: %s\n", result.Artifact)
	}
	fmt.Fprintf(stdout, "Disk: %s\n", result.Disk)
	fmt.Fprintf(stdout, "Direction: %s\n", result.Direction)
	fmt.Fprintf(stdout, "Source: %s\n", result.Source)
	fmt.Fprintf(stdout, "Target: %s\n", result.Target)
	if result.Bytes != 0 {
		fmt.Fprintf(stdout, "Bytes: %d\n", result.Bytes)
	}
	return nil
}

func writeArtifactsResult(stdout *os.File, result artifactsResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	fmt.Fprintf(stdout, "Ingress: %d\n", len(result.Artifacts.Ingress))
	for _, artifact := range result.Artifacts.Ingress {
		fmt.Fprintf(stdout, "  %s %s %s\n", artifact.Name, artifact.Kind, artifact.Mountpoint)
	}
	fmt.Fprintf(stdout, "Egress: %d\n", len(result.Artifacts.Egress))
	for _, artifact := range result.Artifacts.Egress {
		fmt.Fprintf(stdout, "  %s %s\n", artifact.Name, artifact.Path)
	}
	return nil
}

func writeNetworkResult(stdout *os.File, result workspaceNetworkResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.State != "" {
		fmt.Fprintf(stdout, "State: %s\n", result.State)
	}
	if result.Backend != "" {
		fmt.Fprintf(stdout, "Backend: %s\n", result.Backend)
	}
	writeNetworkConfig(stdout, "Network", result.Network)
	if result.Runtime != nil {
		writeNetworkConfig(stdout, "Runtime network", *result.Runtime)
	}
	return nil
}

func writeNetworkConfig(stdout *os.File, label string, network vmkit.NetworkConfig) {
	fmt.Fprintf(stdout, "%s: %s\n", label, network.Mode)
	if network.Interface != "" {
		fmt.Fprintf(stdout, "Interface: %s\n", network.Interface)
	}
	if network.IP != "" {
		fmt.Fprintf(stdout, "IP: %s\n", network.IP)
	}
	if len(network.DNS) != 0 {
		fmt.Fprintf(stdout, "DNS: %s\n", strings.Join(network.DNS, ", "))
	}
	if len(network.Routes) != 0 {
		fmt.Fprintf(stdout, "Routes: %s\n", strings.Join(network.Routes, ", "))
	}
	for _, forward := range network.PortForwards {
		host := forward.Host
		if host == "" {
			host = "*"
		}
		protocol := strings.TrimSpace(forward.Protocol)
		if protocol == "" {
			protocol = "tcp"
		}
		fmt.Fprintf(stdout, "Forward: %s %s:%d -> guest:%d\n", protocol, host, forward.HostPort, forward.GuestPort)
	}
}

func writeSuperviseResult(stdout *os.File, result superviseResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	fmt.Fprintf(stdout, "Policy: %s\n", result.Policy)
	fmt.Fprintf(stdout, "Restarts: %d\n", result.Restarts)
	if result.FinalState != "" {
		fmt.Fprintf(stdout, "Final state: %s\n", result.FinalState)
	}
	return nil
}

func writeWorkspaceList(stdout *os.File, entries []workspaceListEntry) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspaces": entries})
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No workspaces.")
		return nil
	}
	fmt.Fprintf(stdout, "%-24s %-12s %-12s %-12s %-10s %s\n", "NAME", "STATE", "BACKEND", "PROFILE", "NETWORK", "RESTART")
	for _, entry := range entries {
		fmt.Fprintf(stdout, "%-24s %-12s %-12s %-12s %-10s %s\n", entry.Name, entry.State, entry.Backend, entry.Profile, entry.Network, entry.Restart)
	}
	return nil
}

func writeImageList(stdout *os.File, images []imageRecord) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"images": images})
	}
	if len(images) == 0 {
		fmt.Fprintln(stdout, "No images.")
		return nil
	}
	fmt.Fprintf(stdout, "%-48s %-72s %-16s %-10s %s\n", "IMAGE", "DIGEST", "PLATFORM", "SIZE", "LAST USED")
	for _, image := range images {
		platform := image.Platform.OS + "/" + image.Platform.Architecture
		if image.Platform.Variant != "" {
			platform += "/" + image.Platform.Variant
		}
		fmt.Fprintf(stdout, "%-48s %-72s %-16s %-10d %s\n", image.ImageRef, image.Digest, platform, image.SizeBytes, image.LastUsedAt)
	}
	return nil
}

func writeImageRecord(stdout *os.File, record imageRecord) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Image: %s\n", record.ImageRef)
	if record.ResolvedRef != "" {
		fmt.Fprintf(stdout, "Resolved: %s\n", record.ResolvedRef)
	}
	if record.Digest != "" {
		fmt.Fprintf(stdout, "Digest: %s\n", record.Digest)
	}
	platform := record.Platform.OS + "/" + record.Platform.Architecture
	if record.Platform.Variant != "" {
		platform += "/" + record.Platform.Variant
	}
	fmt.Fprintf(stdout, "Platform: %s\n", platform)
	if record.OutputPath != "" {
		fmt.Fprintf(stdout, "Rootfs: %s\n", record.OutputPath)
	}
	if record.SizeBytes != 0 {
		fmt.Fprintf(stdout, "Size: %d\n", record.SizeBytes)
	}
	return nil
}

func writeImagePruneResult(stdout *os.File, result imagePruneResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Removed: %d\n", len(result.Removed))
	fmt.Fprintf(stdout, "Deleted: %d\n", len(result.Deleted))
	fmt.Fprintf(stdout, "Kept: %d\n", len(result.Kept))
	return nil
}

func humanOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}

func humanReady(ready bool) string {
	if ready {
		return "ready"
	}
	return "not-ready"
}

func availability(ok bool) string {
	if ok {
		return "available"
	}
	return "unavailable"
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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

func copyWorkspaceFile(stateDir, debugfsPath, source, target string) (copyResult, error) {
	sourceRemote, sourceIsRemote, err := parseRemoteCopyEndpoint(source)
	if err != nil {
		return copyResult{}, err
	}
	targetRemote, targetIsRemote, err := parseRemoteCopyEndpoint(target)
	if err != nil {
		return copyResult{}, err
	}
	if sourceIsRemote == targetIsRemote {
		return copyResult{}, fmt.Errorf("exactly one cp endpoint must be workspace:path")
	}
	if sourceIsRemote {
		return copyFromWorkspace(stateDir, debugfsPath, sourceRemote, target)
	}
	return copyToWorkspace(stateDir, debugfsPath, source, targetRemote)
}

func getWorkspaceArtifact(stateDir, debugfsPath, name, artifactName, target string) (copyResult, error) {
	manifest, err := readWorkspaceManifest(stateDir, name)
	if err != nil {
		return copyResult{}, err
	}
	output, err := findWorkspaceOutput(manifest.Artifacts.Egress, artifactName)
	if err != nil {
		return copyResult{}, err
	}
	remote := outputRemoteEndpoint(name, output, manifest.Disks)
	if err := validateRemoteCopyPath(remote.Path); err != nil {
		return copyResult{}, err
	}
	result, err := copyFromWorkspace(stateDir, debugfsPath, remote, target)
	if err != nil {
		return copyResult{}, err
	}
	result.Artifact = output.Name
	return result, nil
}

func findWorkspaceOutput(outputs []workspaceOutput, name string) (workspaceOutput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return workspaceOutput{}, fmt.Errorf("artifact name is required")
	}
	for _, output := range outputs {
		if output.Name == name {
			return output, nil
		}
	}
	return workspaceOutput{}, fmt.Errorf("output artifact %q is not declared", name)
}

func outputRemoteEndpoint(workspace string, output workspaceOutput, disks []workspaceDisk) remoteCopyEndpoint {
	disk := "rootfs"
	path := output.Path
	longestMount := ""
	for _, candidate := range disks {
		mount := strings.TrimRight(candidate.Mountpoint, "/")
		if mount == "" {
			continue
		}
		if output.Path == mount || strings.HasPrefix(output.Path, mount+"/") {
			if len(mount) > len(longestMount) {
				longestMount = mount
				disk = candidate.Name
				path = strings.TrimPrefix(output.Path, mount)
				if path == "" {
					path = "/"
				}
			}
		}
	}
	if path == "" || !strings.HasPrefix(path, "/") {
		path = "/" + strings.TrimLeft(path, "/")
	}
	raw := workspace + ":" + path
	if disk != "rootfs" {
		raw = workspace + ":" + disk + ":" + path
	}
	return remoteCopyEndpoint{Workspace: workspace, Disk: disk, Path: path, Raw: raw}
}

type remoteCopyEndpoint struct {
	Workspace string
	Disk      string
	Path      string
	Raw       string
}

func parseRemoteCopyEndpoint(raw string) (remoteCopyEndpoint, bool, error) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 {
		return remoteCopyEndpoint{}, false, nil
	}
	if len(parts) == 2 {
		workspace := strings.TrimSpace(parts[0])
		path := parts[1]
		if workspace == "" || !strings.HasPrefix(path, "/") {
			return remoteCopyEndpoint{}, false, nil
		}
		if err := validateWorkspaceName(workspace); err != nil {
			return remoteCopyEndpoint{}, true, err
		}
		if err := validateRemoteCopyPath(path); err != nil {
			return remoteCopyEndpoint{}, true, err
		}
		return remoteCopyEndpoint{Workspace: workspace, Disk: "rootfs", Path: path, Raw: raw}, true, nil
	}
	workspace := strings.TrimSpace(parts[0])
	disk := strings.TrimSpace(parts[1])
	path := parts[2]
	if workspace == "" || disk == "" || !strings.HasPrefix(path, "/") {
		return remoteCopyEndpoint{}, false, nil
	}
	if err := validateWorkspaceName(workspace); err != nil {
		return remoteCopyEndpoint{}, true, err
	}
	if err := validateDiskName(disk); err != nil {
		return remoteCopyEndpoint{}, true, err
	}
	if err := validateRemoteCopyPath(path); err != nil {
		return remoteCopyEndpoint{}, true, err
	}
	return remoteCopyEndpoint{Workspace: workspace, Disk: disk, Path: path, Raw: raw}, true, nil
}

func validateRemoteCopyPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("workspace path must be absolute: %s", path)
	}
	if path == "/" || strings.HasSuffix(path, "/") {
		return fmt.Errorf("workspace path must name a file: %s", path)
	}
	if strings.ContainsAny(path, "\x00\n\r") {
		return fmt.Errorf("workspace path contains unsupported characters")
	}
	if strings.Contains(path, " ") || strings.Contains(path, "\t") {
		return fmt.Errorf("workspace path must not contain whitespace")
	}
	return nil
}

func validateDiskName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("disk name is required")
	}
	if strings.ContainsAny(name, `/\:`) || name == "." || name == ".." {
		return fmt.Errorf("invalid disk name: %s", name)
	}
	return nil
}

func copyFromWorkspace(stateDir, debugfsPath string, remote remoteCopyEndpoint, localTarget string) (copyResult, error) {
	if err := ensureWorkspaceCloneable(stateDir, remote.Workspace); err != nil {
		return copyResult{}, err
	}
	imagePath, err := workspaceImagePath(stateDir, remote)
	if err != nil {
		return copyResult{}, err
	}
	target, err := localCopyTarget(localTarget, filepath.Base(remote.Path))
	if err != nil {
		return copyResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return copyResult{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".microagent-cp-*")
	if err != nil {
		return copyResult{}, err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return copyResult{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := runDebugFS(debugfsPath, imagePath, false, "dump "+remote.Path+" "+tmpPath); err != nil {
		return copyResult{}, err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return copyResult{}, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return copyResult{}, err
	}
	cleanup = false
	return copyResult{
		Workspace: remote.Workspace,
		Disk:      remote.Disk,
		Direction: "from-workspace",
		Source:    remote.Raw,
		Target:    target,
		ImagePath: imagePath,
		Bytes:     info.Size(),
	}, nil
}

func copyToWorkspace(stateDir, debugfsPath, localSource string, remote remoteCopyEndpoint) (copyResult, error) {
	if err := ensureWorkspaceCloneable(stateDir, remote.Workspace); err != nil {
		return copyResult{}, err
	}
	info, err := os.Stat(localSource)
	if err != nil {
		return copyResult{}, err
	}
	if !info.Mode().IsRegular() {
		return copyResult{}, fmt.Errorf("source must be a regular file: %s", localSource)
	}
	if strings.ContainsAny(localSource, "\x00\n\r\t ") {
		return copyResult{}, fmt.Errorf("local source path must not contain whitespace")
	}
	imagePath, err := workspaceImagePath(stateDir, remote)
	if err != nil {
		return copyResult{}, err
	}
	if err := runDebugFS(debugfsPath, imagePath, true, "write "+localSource+" "+remote.Path); err != nil {
		return copyResult{}, err
	}
	return copyResult{
		Workspace: remote.Workspace,
		Disk:      remote.Disk,
		Direction: "to-workspace",
		Source:    localSource,
		Target:    remote.Raw,
		ImagePath: imagePath,
		Bytes:     info.Size(),
	}, nil
}

func workspaceImagePath(stateDir string, remote remoteCopyEndpoint) (string, error) {
	if remote.Disk == "" || remote.Disk == "rootfs" {
		path := filepath.Join(stateDir, "workspaces", remote.Workspace, "rootfs.ext4")
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return path, nil
	}
	manifest, err := readWorkspaceManifest(stateDir, remote.Workspace)
	if err != nil {
		return "", err
	}
	for _, disk := range manifest.Disks {
		if disk.Name == remote.Disk {
			if _, err := os.Stat(disk.Path); err != nil {
				return "", err
			}
			return disk.Path, nil
		}
	}
	return "", fmt.Errorf("workspace %s has no disk %q", remote.Workspace, remote.Disk)
}

func localCopyTarget(target, fallbackName string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("local target is required")
	}
	if strings.ContainsAny(target, "\x00\n\r") {
		return "", fmt.Errorf("local target path contains unsupported characters")
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return filepath.Join(target, fallbackName), nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return target, nil
}

func runDebugFS(debugfsPath, imagePath string, write bool, command string) error {
	args := []string{}
	if write {
		args = append(args, "-w")
	}
	args = append(args, "-R", command, imagePath)
	cmd := exec.Command(debugfsPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs %s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	if debugFSOutputFailed(output) {
		return fmt.Errorf("debugfs %s failed: %s", command, strings.TrimSpace(string(output)))
	}
	return nil
}

func debugFSOutputFailed(output []byte) bool {
	text := strings.ToLower(string(output))
	for _, marker := range []string{
		"file not found",
		"not found by ext2_lookup",
		"ext2fs_open2",
		"permission denied",
		"no such file",
		"usage:",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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

func imageIndexPath(stateDir string) string {
	return filepath.Join(stateDir, "images", "index.json")
}

func readImageIndex(stateDir string) (imageIndex, error) {
	data, err := os.ReadFile(imageIndexPath(stateDir))
	if os.IsNotExist(err) {
		return imageIndex{}, nil
	}
	if err != nil {
		return imageIndex{}, err
	}
	var idx imageIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return imageIndex{}, err
	}
	return idx, nil
}

func writeImageIndex(stateDir string, idx imageIndex) error {
	if err := os.MkdirAll(filepath.Dir(imageIndexPath(stateDir)), 0o700); err != nil {
		return err
	}
	return writeJSONFile(imageIndexPath(stateDir), idx)
}

func imageRecordFromProvenance(provenance rootfs.Provenance) imageRecord {
	return imageRecord{
		ImageRef:    provenance.ImageRef,
		ResolvedRef: provenance.ResolvedRef,
		Digest:      provenance.Digest,
		Platform:    provenance.Platform,
		OutputPath:  provenance.OutputPath,
		SizeBytes:   provenance.SizeBytes,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}
}

func recordImageProvenance(stateDir string, provenance rootfs.Provenance) error {
	if provenance.ImageRef == "" || provenance.Digest == "" {
		return nil
	}
	return upsertImageRecord(stateDir, imageRecordFromProvenance(provenance))
}

func upsertImageRecord(stateDir string, record imageRecord) error {
	idx, err := readImageIndex(stateDir)
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range idx.Images {
		if existing.ImageRef == record.ImageRef && existing.Platform == record.Platform {
			idx.Images[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		idx.Images = append(idx.Images, record)
	}
	sortImageRecords(idx.Images)
	return writeImageIndex(stateDir, idx)
}

func tagImageRecord(stateDir, source, target string) (imageRecord, error) {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if source == "" {
		return imageRecord{}, fmt.Errorf("source image is required")
	}
	if target == "" {
		return imageRecord{}, fmt.Errorf("target image is required")
	}
	if strings.ContainsAny(target, "\x00\n\r") {
		return imageRecord{}, fmt.Errorf("target image contains unsupported characters")
	}
	idx, err := readImageIndex(stateDir)
	if err != nil {
		return imageRecord{}, err
	}
	for _, image := range idx.Images {
		if imageMatchesRef(image, source) {
			tagged := image
			tagged.ImageRef = target
			tagged.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
			if err := upsertImageRecord(stateDir, tagged); err != nil {
				return imageRecord{}, err
			}
			return tagged, nil
		}
	}
	return imageRecord{}, fmt.Errorf("image %q not found", source)
}

func imageMatchesRef(image imageRecord, ref string) bool {
	return image.ImageRef == ref || image.ResolvedRef == ref || image.Digest == ref
}

func removeImageRecords(stateDir, ref string, deleteFiles bool) (imagePruneResult, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return imagePruneResult{}, fmt.Errorf("image reference is required")
	}
	idx, err := readImageIndex(stateDir)
	if err != nil {
		return imagePruneResult{}, err
	}
	result := imagePruneResult{}
	var matched []imageRecord
	keptPaths := map[string]bool{}
	for _, image := range idx.Images {
		if imageMatchesRef(image, ref) {
			matched = append(matched, image)
			continue
		}
		result.Kept = append(result.Kept, image)
		if image.OutputPath != "" {
			if canonical, ok := canonicalImageStorePath(stateDir, image.OutputPath); ok {
				keptPaths[canonical] = true
			}
		}
	}
	if len(matched) == 0 {
		return imagePruneResult{}, fmt.Errorf("image %q not found", ref)
	}
	deletedPaths := map[string]bool{}
	for _, image := range matched {
		cleanPath, ok := canonicalImageStorePath(stateDir, image.OutputPath)
		if deleteFiles && image.OutputPath != "" && ok && !keptPaths[cleanPath] {
			if deletedPaths[cleanPath] {
				result.Deleted = append(result.Deleted, image)
				continue
			}
			if err := os.Remove(cleanPath); err == nil {
				deletedPaths[cleanPath] = true
				result.Deleted = append(result.Deleted, image)
				continue
			} else if !os.IsNotExist(err) {
				return imagePruneResult{}, err
			}
		}
		result.Removed = append(result.Removed, image)
	}
	sortImageRecords(result.Kept)
	sortImageRecords(result.Removed)
	sortImageRecords(result.Deleted)
	if err := writeImageIndex(stateDir, imageIndex{Images: result.Kept}); err != nil {
		return imagePruneResult{}, err
	}
	return result, nil
}

func listImageRecords(stateDir string) ([]imageRecord, error) {
	idx, err := readImageIndex(stateDir)
	if err != nil {
		return nil, err
	}
	images := append([]imageRecord{}, idx.Images...)
	sortImageRecords(images)
	return images, nil
}

func pruneImageRecords(stateDir string, deleteFiles bool) (imagePruneResult, error) {
	idx, err := readImageIndex(stateDir)
	if err != nil {
		return imagePruneResult{}, err
	}
	result := imagePruneResult{}
	deletedPaths := map[string]bool{}
	for _, image := range idx.Images {
		if image.OutputPath == "" {
			result.Kept = append(result.Kept, image)
			continue
		}
		cleanPath, ok := canonicalImageStorePath(stateDir, image.OutputPath)
		if deleteFiles && ok {
			if deletedPaths[cleanPath] {
				result.Deleted = append(result.Deleted, image)
				continue
			}
			if err := os.Remove(cleanPath); err == nil {
				deletedPaths[cleanPath] = true
				result.Deleted = append(result.Deleted, image)
				continue
			} else if os.IsNotExist(err) {
				result.Removed = append(result.Removed, image)
				continue
			} else {
				return imagePruneResult{}, err
			}
		}
		if _, err := os.Stat(image.OutputPath); err == nil {
			result.Kept = append(result.Kept, image)
		} else if os.IsNotExist(err) {
			result.Removed = append(result.Removed, image)
		} else {
			return imagePruneResult{}, err
		}
	}
	sortImageRecords(result.Kept)
	sortImageRecords(result.Removed)
	sortImageRecords(result.Deleted)
	if err := writeImageIndex(stateDir, imageIndex{Images: result.Kept}); err != nil {
		return imagePruneResult{}, err
	}
	return result, nil
}

func canonicalImageStorePath(stateDir, path string) (string, bool) {
	storeDir, err := filepath.Abs(filepath.Join(stateDir, "images", "rootfs"))
	if err != nil {
		return "", false
	}
	storeDir, err = filepath.EvalSymlinks(storeDir)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		return "", false
	}
	absPath = filepath.Join(parent, filepath.Base(absPath))
	rel, err := filepath.Rel(storeDir, absPath)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return absPath, true
}

func sortImageRecords(images []imageRecord) {
	sort.Slice(images, func(i, j int) bool {
		if images[i].ImageRef != images[j].ImageRef {
			return images[i].ImageRef < images[j].ImageRef
		}
		if images[i].Platform.Architecture != images[j].Platform.Architecture {
			return images[i].Platform.Architecture < images[j].Platform.Architecture
		}
		return images[i].Digest < images[j].Digest
	})
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

func dataAfterOffset(data []byte, currentOffset, detectAfter int64) []byte {
	startOffset := currentOffset - int64(len(data))
	if detectAfter <= startOffset {
		return data
	}
	if detectAfter >= currentOffset {
		return nil
	}
	return data[detectAfter-startOffset:]
}

func waitForConsoleReady(ctx context.Context, path string, timeout time.Duration) error {
	const maxConsoleReadyBytes int64 = 64 * 1024
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		data, err := readFileTail(path, maxConsoleReadyBytes)
		if err == nil && consoleLooksReady(string(data)) {
			return nil
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("console did not become ready before timeout: %s", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func readFileTail(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if info.Size() > maxBytes {
		offset = info.Size() - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, maxBytes))
}

func consoleLooksReady(output string) bool {
	return strings.Contains(output, "# ") ||
		strings.Contains(output, "$ ") ||
		strings.Contains(strings.ToLower(output), "login:")
}

func copyConsoleInput(dst io.Writer, src io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 4096)
	state := consoleInputState{}
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			filtered, detach := filterConsoleInput(chunk, &state)
			written, writeErr := writeConsoleInputChunk(dst, filtered)
			total += written
			if writeErr != nil {
				return total, writeErr
			}
			if detach {
				return total, nil
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

func copyShellInput(dst io.Writer, src io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 4096)
	state := consoleInputState{}
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			filtered, detach := filterConsoleInput(chunk, &state)
			written, writeErr := dst.Write(filtered)
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if detach {
				return total, nil
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

type consoleInputState struct {
	sawDetachPrefix bool
}

func filterConsoleInput(chunk []byte, state *consoleInputState) ([]byte, bool) {
	if len(chunk) == 0 {
		return chunk, false
	}
	var out []byte
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]
		if state.sawDetachPrefix {
			state.sawDetachPrefix = false
			if b == consoleDetachSuffix {
				return out, true
			}
			out = append(out, consoleDetachPrefix)
		}
		if b == consoleDetachByte {
			return out, true
		}
		if b == consoleDetachPrefix {
			state.sawDetachPrefix = true
			continue
		}
		out = append(out, b)
	}
	return out, false
}

func writeConsoleInputChunk(dst io.Writer, chunk []byte) (int64, error) {
	if len(chunk) == 0 {
		return 0, nil
	}
	chunk = stripBracketedPasteMarkers(chunk)
	normalized := bytes.ReplaceAll(chunk, []byte("\n"), []byte("\r"))
	written, err := dst.Write(normalized)
	if err != nil {
		return int64(written), err
	}
	if written != len(normalized) {
		return int64(written), io.ErrShortWrite
	}
	return int64(written), nil
}

func stripBracketedPasteMarkers(chunk []byte) []byte {
	chunk = bytes.ReplaceAll(chunk, []byte("\x1b[200~"), nil)
	chunk = bytes.ReplaceAll(chunk, []byte("\x1b[201~"), nil)
	return chunk
}

func requestFromFlagsOrJSON(jsonPath string, args []string, identity vmkit.Identity, config vmkit.Config, disks []string, vsocks []string, networkMode string, networkInterface string, publishes []string) (vmkit.Request, error) {
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
	network := normalizeNetworkConfig(vmkit.NetworkConfig{Mode: networkMode, Interface: networkInterface, PortForwards: portForwards})
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
		"-network-interface":         true,
		"-network-name":              true,
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
	}
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
		if !valueFlags[flagName] && !isBoolReorderFlag(flagName) {
			positional = append(positional, args[i])
			continue
		}
		flags = append(flags, arg)
		if valueFlags[flagName] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

func isBoolReorderFlag(name string) bool {
	switch name {
	case "-json", "-text", "-human", "-keep", "-rm", "-dry-run", "-image-command", "-mediation-optional", "-secrets-audit", "-delete", "-yes", "-y", "-force", "-f", "-follow", "-images", "-install", "-uninstall", "-push":
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
  stats                Show or stream workspace resource usage
  snapshot             Create, list, or remove workspace snapshots
  secret check         Resolve and validate secret references
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
  host setup-networking  Enable nat/bridged/named networking (Linux; needs root). --check / --revert
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
  -network <mode>       Network mode: user, nat, isolated, bridged, or named
  -network-interface <if>
                         Host interface for bridged network mode
  -network-name <name>  Join a user-defined named network by name
  -memory <MiB>         Memory in MiB; defaults to 512 for workspaces
  -cpus <n>             CPU count
  -vsock p=host:port    Add a vsock mapping
`)
}

func printPerfHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent perf

Measure workspace performance.

Commands:
  boot                 Measure disposable workspace boot time
  footprint            Report host process RSS for a running workspace
  steady               Sample host process RSS over time

Boot options:
  -image <ref>          OCI image; defaults to Python 3.13 slim
  -exec <command>       Guest command used to mark boot completion; defaults to true
  -iterations <n>       Number of boot measurements
  -profile <name>       Resource profile: tiny, small, medium, or large
  -state-dir <dir>      State directory
  -timeout <seconds>    Per-iteration timeout
  -mke2fs <path>        mke2fs binary path
  -supervisor <path>    Override the supervisor path
  -network <mode>       Network mode for measured boots; empty uses the backend default

Footprint options:
  -state-dir <dir>      State directory

Steady options:
  -duration <seconds>   Sampling duration
  -interval <seconds>   Sampling interval
  -state-dir <dir>      State directory
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
  -network <mode>       Network mode: user, nat, isolated, bridged, or named
  -network-interface <if>
                         Host interface for bridged network mode
  -network-name <name>  Join a user-defined named network by name
  -p host:guest[/tcp]   Publish a TCP port
  -mediation p=host:port Required mediation vsock mapping
  -mediation-optional Allow workspace to run if mediation is unavailable
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
  -network <mode>       Network mode: user, nat, isolated, bridged, or named
  -network-interface <if>
                         Host interface for bridged network mode
  -network-name <name>  Join a user-defined named network by name
  -p host:guest[/tcp]   Publish a TCP port
  -publish host:guest[/tcp]
                         Publish a TCP port
  -mediation p=host:port Required mediation vsock mapping
  -mediation-optional Allow workspace to run if mediation is unavailable
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

func printKernelHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent kernel

Advanced kernel commands. Most users can start with microagent run IMAGE ...
and skip this.

Commands:
  install              Install a custom kernel
  verify               Verify a custom kernel

Install options:
  With no options, install Microagent's default kernel for this Mac.
  -url <url>           Download URL
  -from <path>         Local kernel path
  -sha256 <sha256>     Expected SHA-256
  -out <path>          Output path

Verify options:
  -path <path>         Kernel path
  -sha256 <sha256>     Expected SHA-256
`)
}

func printRootFSHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent rootfs

Commands:
  build                Build a rootfs from an OCI image

Build options:
  -image <ref>         OCI image
  -out <path>          Output rootfs path
  -os <os>             Target OS
  -arch <arch>         Target architecture
  -size-mib <MiB>      Disk size
  -mke2fs <path>       mke2fs binary path
  -exec <command>      Shell command to run as guest init
  -allow-mutable       Allow tag references
`)
}
