package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/perf"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	windowshyperv "github.com/geoffbelknap/microagent/pkg/supervisors/windows_hyperv"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

var (
	version          = "dev"
	outputFormat     string
	globalOutputMode outputMode
	stdinIsTerminal  = defaultStdinIsTerminal
	readConfirmation = defaultReadConfirmation
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
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
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
	if args[0] == "images" {
		return runImages(args[1:], stdout)
	}
	if args[0] == "prune" {
		return runPrune(args[1:], stdout)
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
	if args[0] == "create" && wantsHelp(args[1:]) {
		printCreateHelp(stdout)
		return nil
	}
	if args[0] == "apply" {
		return runApply(ctx, args[1:], stdout)
	}
	if args[0] == "clone" {
		return runClone(args[1:], stdout)
	}
	if args[0] == "cp" {
		return runCP(args[1:], stdout)
	}
	if args[0] == "artifacts" {
		return runArtifacts(args[1:], stdout)
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
	if args[0] == "result" {
		return runWorkspaceStateCommand(ctx, args[0], args[1:], stdout)
	}
	if args[0] == "inspect" {
		if outputFormat == "" {
			outputFormat = "json"
		}
		return runWorkspaceStateCommand(ctx, "status", args[1:], stdout)
	}
	if args[0] == "status" || args[0] == "halt" || args[0] == "quarantine" || args[0] == "pause" || args[0] == "resume" || args[0] == "stop" || args[0] == "kill" || args[0] == "delete" {
		if wantsHelp(args[1:]) || hasWorkspaceStateTarget(args[1:]) {
			return runWorkspaceStateCommand(ctx, args[0], args[1:], stdout)
		}
	}
	if args[0] == "rm" {
		return runWorkspaceStateCommand(ctx, "delete", args[1:], stdout)
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

func firecrackerDoctorResponse(backend, arch string, resolveBinary func() (string, error), resolveSupervisor func(diagnostics.Options) (string, error), resolveGuestInit func(diagnostics.Options) (string, error), stat func(string) (os.FileInfo, error), binaryVersion func(string) string, lookPath func(string) (string, error), readFile func(string) ([]byte, error)) (vmkit.Response, error) {
	return diagnostics.CheckFirecracker(
		diagnostics.Options{Backend: backend, Arch: arch},
		diagnostics.FirecrackerProbe{ResolveBinary: resolveBinary, ResolveSupervisor: resolveSupervisor, ResolveGuestInit: resolveGuestInit, Stat: stat, BinaryVersion: binaryVersion, LookPath: lookPath, ReadFile: readFile},
	)
}

func resolveFirecrackerPath() (string, error) {
	return diagnostics.ResolveFirecrackerPath()
}

func defaultFirecrackerPathFromExecutable(executable string) string {
	return diagnostics.DefaultFirecrackerPathFromExecutable(executable)
}

func firecrackerVersion(path string) string {
	return diagnostics.FirecrackerVersion(path)
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

func defaultKernelSupport(backend, arch string) *vmkit.KernelSupport {
	return defaultKernelSupportForPath(backend, arch, defaultKernelPath(backend, arch))
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
type workspaceFile = workspace.File
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

type imagePullOptions struct {
	StateDir      string
	ImageRef      string
	Architecture  string
	SizeMiB       int64
	Mke2fsPath    string
	GuestInitPath string
}

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
	result, err := workspace.Run(ctx, opts)
	if encodeErr := writeWorkspaceResult(stdout, result); encodeErr != nil {
		return encodeErr
	}
	return err
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
	return writeWorkspaceList(stdout, entries)
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

func runCP(args []string, stdout *os.File) error {
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
	result, err := workspace.Copy(opts.StateDir, debugfsPath, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
}

func runArtifacts(args []string, stdout *os.File) error {
	if len(args) > 0 && args[0] == "get" {
		return runArtifactGet(args[1:], stdout)
	}
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("artifacts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent artifacts <name> [--state-dir <dir>]")
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

func runArtifactGet(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	debugfsPath := defaultDebugFSPath()
	fs := flag.NewFlagSet("artifacts get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: microagent artifacts get <name> <artifact> <target> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	result, err := workspace.GetArtifact(opts.StateDir, debugfsPath, name, fs.Arg(1), fs.Arg(2))
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
  microagent snapshot rm <name> <tag> [--state-dir <dir>]
`)
		return nil
	}
	switch args[0] {
	case "create":
		return runSnapshotCreate(ctx, args[1:], stdout)
	case "list", "ls":
		return runSnapshotList(args[1:], stdout)
	case "rm", "remove", "delete":
		return runSnapshotRemove(args[1:], stdout)
	default:
		return fmt.Errorf("unknown snapshot subcommand %q; use create, list, or rm", args[0])
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
			return fmt.Errorf("usage: microagent snapshot rm <name> <tag> [--state-dir <dir>]")
		}
		name = rest[0]
		tag = rest[1]
	} else {
		if len(rest) != 1 {
			return fmt.Errorf("usage: microagent snapshot rm <name> <tag> [--state-dir <dir>]")
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

func runImages(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	guestInitExplicit := hasFlagValue(args, "guest-init")
	fs := flag.NewFlagSet("images", flag.ContinueOnError)
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
			return fmt.Errorf("usage: microagent images list [--state-dir <dir>]")
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
			return fmt.Errorf("usage: microagent images pull <image> [--state-dir <dir>]")
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
	case "tag":
		if fs.NArg() != 3 {
			return fmt.Errorf("usage: microagent images tag <source> <target> [--state-dir <dir>]")
		}
		record, err := imagecache.Tag(opts.StateDir, fs.Arg(1), fs.Arg(2))
		if err != nil {
			return err
		}
		return writeImageRecord(stdout, record)
	case "rm", "remove", "rmi":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: microagent images rm <image> [--delete] [--state-dir <dir>]")
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
			return fmt.Errorf("usage: microagent images prune [--state-dir <dir>]")
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
		return fmt.Errorf("unknown images command: %s", fs.Arg(0))
	}
}

func runPrune(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	deleteImages := fs.Bool("images", false, "Delete reusable local image rootfs files")
	yes := fs.Bool("yes", false, "Confirm destructive cleanup without prompting")
	fs.BoolVar(yes, "y", false, "Confirm destructive cleanup without prompting")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent prune [--images] [--yes] [--state-dir <dir>]")
	}
	if *deleteImages {
		if err := confirmImageCacheDelete(*yes); err != nil {
			return err
		}
	}
	result, err := imagecache.Prune(opts.StateDir, *deleteImages)
	if err != nil {
		return err
	}
	return writeImagePruneResult(stdout, result)
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

func processRSSKiB(pid int) (int64, error) {
	return perf.ProcessRSSKiB(pid)
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

func sampleProcessRSS(ctx context.Context, pid int, duration, interval time.Duration) ([]perfRSSSample, error) {
	return perf.SampleProcessRSS(ctx, pid, duration, interval)
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
			final, _ := writeSerialTail(serialPath, offset, stdout)
			offset += final
			return nil
		}
		select {
		case <-ctx.Done():
			final, _ := writeSerialTail(serialPath, offset, stdout)
			offset += final
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
	defer f.Close()
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
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("network", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent network <name> [--state-dir <dir>]")
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
	if command == "delete" {
		resp, err := runDeleteWorkspace(ctx, workspaceOpts, yes, force)
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
	resp, err := workspace.Control(ctx, workspaceOpts, req.Command)
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
		return workspace.Control(ctx, opts, "delete")
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
	defer conn.Close()
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
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent supervise <name> [--state-dir <dir>]")
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

func superviseWorkspace(ctx context.Context, opts superviseOptions) (superviseResult, error) {
	return workspace.Supervise(ctx, opts)
}

func superviseWorkspaceOptions(ctx context.Context, opts superviseOptions) (workspaceOptions, error) {
	workspaceOpts := workspaceOptions{
		Name:           opts.Name,
		Backend:        opts.Backend,
		Architecture:   opts.Architecture,
		KernelPath:     opts.KernelPath,
		KernelExplicit: opts.KernelExplicit,
		StateDir:       opts.StateDir,
		SupervisorPath: opts.SupervisorPath,
		Profile:        defaultWorkspaceProfile,
		RestartPolicy:  defaultRestartPolicy,
		Network:        vmkit.NetworkConfig{Mode: defaultNetworkMode},
		MemoryMiB:      defaultWorkspaceMemoryMiB,
		CPUCount:       defaultWorkspaceCPUCount,
		SizeMiB:        rootfs.DefaultSizeMiB,
		ResultPort:     workspace.DefaultResultPort,
		SerialInput:    backendSupportsConsoleInput(opts.Backend),
	}
	manifest, err := readWorkspaceManifest(opts.StateDir, opts.Name)
	if err != nil {
		return workspaceOptions{}, err
	}
	if manifest.Profile != "" {
		workspaceOpts.Profile = manifest.Profile
	}
	workspaceOpts.RestartPolicy = normalizeRestartPolicy(manifest.Restart)
	if manifest.Network.Mode != "" || manifest.Network.Interface != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" || manifest.Network.Subnet != "" || manifest.Network.Gateway != "" {
		workspaceOpts.Network = networkConfigFromSpec(manifest.Network)
	}
	if manifest.Resources.MemoryMiB != 0 {
		workspaceOpts.MemoryMiB = manifest.Resources.MemoryMiB
	}
	if manifest.Resources.CPUCount != 0 {
		workspaceOpts.CPUCount = manifest.Resources.CPUCount
	}
	if manifest.Resources.SizeMiB != 0 {
		workspaceOpts.SizeMiB = manifest.Resources.SizeMiB
	}
	workspaceOpts.Disks = manifest.Disks
	workspaceOpts.Mediation = manifest.Mediation
	if err := validateRestartPolicy(workspaceOpts.RestartPolicy); err != nil {
		return workspaceOptions{}, err
	}
	rootfsPath := filepath.Join(opts.StateDir, "workspaces", opts.Name, "rootfs.ext4")
	if _, err := os.Stat(rootfsPath); err != nil {
		return workspaceOptions{}, err
	}
	if err := ensureWorkspaceKernel(ctx, &workspaceOpts); err != nil {
		return workspaceOptions{}, err
	}
	return workspaceOpts, nil
}

func waitForSupervisedWorkspace(ctx context.Context, opts workspaceOptions, interval time.Duration) (vmkit.VMState, error) {
	for {
		resp, err := inspectWorkspace(ctx, opts)
		if err != nil {
			if resp.Event != nil {
				return resp.Event.State, err
			}
			return vmkit.StateUnknown, err
		}
		if resp.Event != nil {
			switch resp.Event.State {
			case vmkit.StateHalted, vmkit.StateQuarantined, vmkit.StateStopped, vmkit.StateFailed:
				return resp.Event.State, nil
			}
		}
		select {
		case <-ctx.Done():
			return vmkit.StateUnknown, ctx.Err()
		case <-time.After(interval):
		}
	}
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
	result, err := workspace.Create(ctx, opts)
	if err != nil && result.Workspace == "" {
		return err
	}
	if encodeErr := writeWorkspaceResult(stdout, result); encodeErr != nil {
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
	var diskFlags multiFlag
	fs.Var(&diskFlags, "disk", "Attach disk name=path:/mount:ro|rw")
	var bundleFlags multiFlag
	fs.Var(&bundleFlags, "bundle", "Build and attach bundle name=tar:/mount:ro|rw")
	var volumeFlags multiFlag
	fs.Var(&volumeFlags, "volume", "Attach a safe container-style volume SRC:DST[:ro|rw]")
	fs.Var(&volumeFlags, "v", "Attach a safe container-style volume SRC:DST[:ro|rw]")
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
	fs.StringVar(&opts.Network.Mode, "network", opts.Network.Mode, "Network mode: user, nat, isolated, or bridged")
	fs.StringVar(&opts.Network.Interface, "network-interface", opts.Network.Interface, "Host interface for bridged network mode")
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

func setupCommandsFromSpec(steps workspace.SetupSteps, setupFiles []string, baseDir string) ([]string, error) {
	return workspace.SetupCommandsFromSpec(steps, setupFiles, baseDir)
}

func setupCommandsFromFiles(files []string, baseDir string) ([]string, error) {
	return workspace.SetupCommandsFromFiles(files, baseDir)
}

func setupCommandFromFile(path, baseDir string) (string, error) {
	return workspace.SetupCommandFromFile(path, baseDir)
}

func workspaceSpecDisks(spec workspaceSpec) ([]workspaceDisk, error) {
	return workspace.SpecDisks(spec)
}

func validateWorkspaceDisk(disk workspaceDisk) error {
	if strings.TrimSpace(disk.Name) == "" {
		return fmt.Errorf("disk name is required")
	}
	if err := validateSafeBasename("disk name", disk.Name); err != nil {
		return err
	}
	if disk.Name == "rootfs" {
		return fmt.Errorf("disk name rootfs is reserved")
	}
	path := disk.Path
	if disk.Bundle {
		path = disk.SourcePath
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("disk %q path is required", disk.Name)
	}
	if strings.TrimSpace(disk.Mountpoint) == "" {
		return fmt.Errorf("disk %q mountpoint is required", disk.Name)
	}
	if !strings.HasPrefix(disk.Mountpoint, "/") {
		return fmt.Errorf("disk %q mountpoint must be absolute", disk.Name)
	}
	if disk.Mode != "ro" && disk.Mode != "rw" {
		return fmt.Errorf("disk %q mode must be ro or rw", disk.Name)
	}
	return nil
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

func validateWorkspaceFiles(files []workspaceFile, baseDir string) ([]workspaceFile, error) {
	return workspace.ValidateFiles(files, baseDir)
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

func resourceProfileNames() []string {
	return workspace.ProfileNames()
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

func networkConfigFromSpec(spec networkSpec) vmkit.NetworkConfig {
	return workspace.NetworkConfigFromSpec(spec)
}

func networkConfigPtr(network vmkit.NetworkConfig) *vmkit.NetworkConfig {
	return workspace.NetworkConfigPtr(network)
}

func workspaceArtifactsFromOptions(opts workspaceOptions) workspaceArtifacts {
	return workspace.ArtifactsFromOptions(opts)
}

func runtimeArtifactsFromManifest(artifacts workspaceArtifacts) vmkit.RuntimeArtifacts {
	return workspace.RuntimeArtifacts(artifacts)
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
	req := rootfs.BuildRequest{
		ImageRef:       opts.ImageRef,
		Platform:       rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:     rootfsPath,
		InitPath:       rootfs.DefaultInitPath,
		Command:        command,
		Mode:           mode,
		ConsoleShell:   opts.ConsoleShell,
		Hostname:       opts.Hostname,
		ShellPort:      workspace.ShellPort(opts),
		ExecPort:       workspace.ExecPort(opts),
		InitBinaryPath: opts.GuestInitPath,
		ResultPort:     resultPort,
		NoImageCommand: opts.PrepareForStart && !workspaceHasGuestCommand(opts) && !opts.UseImageCommand,
		StateDir:       filepath.Join(opts.StateDir, "build"),
		Mke2fsPath:     opts.Mke2fsPath,
		SizeMiB:        opts.SizeMiB,
		Env:            opts.Env,
		Files:          workspace.RootfsFiles(opts.Files),
		Mounts:         workspaceMounts(opts.Disks),
		HostForwards:   rootfsPortForwards(opts.Network.PortForwards),
		AllowMutable:   true,
		Progress:       opts.Progress,
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

func virtioBlockDevice(index int) string {
	return workspace.VirtioBlockDevice(index)
}

func prepareWorkspaceDisks(ctx context.Context, opts workspaceOptions) ([]workspaceDisk, error) {
	if len(opts.Disks) == 0 {
		return nil, nil
	}
	disks := make([]workspaceDisk, 0, len(opts.Disks))
	seenNames := map[string]bool{}
	seenMountpoints := map[string]bool{}
	for _, disk := range opts.Disks {
		if err := validateWorkspaceDisk(disk); err != nil {
			return nil, err
		}
		if seenNames[disk.Name] {
			return nil, fmt.Errorf("duplicate disk name %q", disk.Name)
		}
		seenNames[disk.Name] = true
		if seenMountpoints[disk.Mountpoint] {
			return nil, fmt.Errorf("duplicate disk mountpoint %q", disk.Mountpoint)
		}
		seenMountpoints[disk.Mountpoint] = true
		if disk.Bundle {
			outputPath := filepath.Join(opts.StateDir, "workspaces", opts.Name, "disks", disk.Name+".ext4")
			_, err := rootfs.NewBuilder().BuildBundle(ctx, rootfs.BundleRequest{
				SourcePath: disk.SourcePath,
				OutputPath: outputPath,
				StateDir:   filepath.Join(opts.StateDir, "build"),
				Mke2fsPath: opts.Mke2fsPath,
				SizeMiB:    64,
			})
			if err != nil {
				return nil, err
			}
			disk.Path = outputPath
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func writeWorkspaceManifest(opts workspaceOptions) error {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(workspaceDir, "workspace.json"), workspaceManifest{
		Name:         opts.Name,
		Profile:      opts.Profile,
		Restart:      normalizeRestartPolicy(opts.RestartPolicy),
		Resources:    workspaceResources(opts),
		Network:      networkSpecFromConfig(opts.Network),
		Service:      strings.TrimSpace(opts.ServiceCommand),
		Mediation:    opts.Mediation,
		Disks:        opts.Disks,
		Artifacts:    workspaceArtifactsFromOptions(opts),
		Verification: opts.Verification,
	})
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

func configDisksToWorkspaceDisks(disks []vmkit.Disk) []workspaceDisk {
	return workspace.ConfigDisks(disks)
}

func workspaceSupervisor(opts workspaceOptions) (vmkit.Supervisor, error) {
	return workspace.Supervisor(opts)
}

func firecrackerSupervisorPath(opts workspaceOptions) string {
	return workspace.FirecrackerSupervisorPath(opts)
}

func dispatchWorkspaceRequest(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	return workspace.Dispatch(ctx, opts, req)
}

func runWorkspaceForeground(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	resp, err := dispatchWorkspaceRequest(ctx, opts, req)
	state := vmkit.StateStopped
	errorText := ""
	if opts.Backend == vmkit.BackendFirecracker {
		return resp, err
	}
	if err != nil || !resp.OK {
		state = vmkit.StateFailed
		errorText = resp.Error
		if errorText == "" && err != nil {
			errorText = err.Error()
		}
	}
	if stateErr := writeWorkspaceProcessState(opts, req, state, 0, errorText); stateErr != nil && err == nil {
		return resp, stateErr
	}
	return resp, err
}

func startWorkspaceDetached(opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	if opts.Backend == vmkit.BackendFirecracker {
		req.Command = "start"
		return dispatchWorkspaceRequest(context.Background(), opts, req)
	}
	if opts.Backend != vmkit.BackendAppleVF {
		return dispatchWorkspaceRequest(context.Background(), opts, req)
	}
	path := opts.SupervisorPath
	if path == "" {
		path = "microagent-applevf-supervisor"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return vmkit.Response{}, err
	}
	if err := os.MkdirAll(filepath.Join(opts.StateDir, opts.Name), 0o755); err != nil {
		return vmkit.Response{}, err
	}
	supervisorLogPath := filepath.Join(opts.StateDir, opts.Name, "supervisor.log")
	supervisorLog, err := os.OpenFile(supervisorLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return vmkit.Response{}, err
	}
	defer supervisorLog.Close()
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Stdout = supervisorLog
	cmd.Stderr = supervisorLog
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return vmkit.Response{}, err
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateRunning, cmd.Process.Pid, ""); err != nil {
		_ = cmd.Process.Kill()
		return vmkit.Response{}, err
	}
	_ = cmd.Process.Release()
	event := vmkit.Event{
		Identity:   *req.Identity,
		State:      vmkit.StateRunning,
		Detail:     "serial=" + serialLogPath(opts),
		ObservedAt: time.Now().UTC(),
	}
	return vmkit.Response{OK: true, Backend: opts.Backend, Event: &event}, nil
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

func inspectWorkspaceState(opts workspaceOptions) (vmkit.Response, error) {
	state, err := readWorkspaceRuntimeState(opts)
	if err != nil {
		event, eventErr := readWorkspaceEvent(opts)
		if eventErr != nil {
			return vmkit.Response{}, err
		}
		return responseFromWorkspaceEvent(opts, event, ""), nil
	}
	return responseFromWorkspaceEvent(opts, state.Event, state.Error), nil
}

func responseFromWorkspaceEvent(opts workspaceOptions, eventFile workspaceEventFile, errorText string) vmkit.Response {
	event := vmkit.Event{
		Identity:   eventFile.Identity,
		State:      eventFile.State,
		Detail:     eventFile.Detail,
		ObservedAt: time.Now().UTC(),
	}
	if parsed, err := time.Parse(time.RFC3339, eventFile.ObservedAt); err == nil {
		event.ObservedAt = parsed
	}
	backend := opts.Backend
	if backend == "" {
		backend = eventFile.Identity.Backend
	}
	resp := vmkit.Response{OK: eventFile.State != vmkit.StateFailed, Backend: backend, Event: &event}
	if manifest, err := readWorkspaceManifest(opts.StateDir, eventFile.Identity.RuntimeID); err == nil {
		resp.RestartPolicy = nonEmpty(manifest.Restart, defaultRestartPolicy)
		network := networkConfigFromSpec(manifest.Network)
		if state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: opts.StateDir, Name: eventFile.Identity.RuntimeID}); err == nil && state.Config.Network != nil {
			runtimeNetwork := normalizeNetworkConfig(*state.Config.Network)
			runtimeNetwork.Runtime = nil
			network.Runtime = &runtimeNetwork
		}
		resp.Network = &network
		resp.Mediation = manifest.Mediation
		artifacts := runtimeArtifactsFromManifest(manifest.Artifacts)
		resp.Artifacts = &artifacts
		resp.Verification = workspaceVerificationForStatus(opts, eventFile.Identity.RuntimeID, manifest, eventFile.State)
	}
	readiness := workspaceReadinessForStatus(opts, eventFile)
	resp.Readiness = &readiness
	if result, err := readRuntimeResult(opts, eventFile.Identity); err == nil {
		resp.Result = &result
	}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func inspectWorkspaceResult(opts workspaceOptions) (vmkit.Response, error) {
	resp, err := inspectWorkspaceState(opts)
	if err != nil {
		return resp, err
	}
	if resp.Event == nil {
		err := fmt.Errorf("workspace %s has no state event", opts.Name)
		resp.OK = false
		resp.Error = err.Error()
		return resp, err
	}
	result, resultErr := readRuntimeResult(opts, resp.Event.Identity)
	if resultErr != nil {
		err := fmt.Errorf("workspace %s result is not ready: %w", opts.Name, resultErr)
		resp.OK = false
		resp.Error = err.Error()
		return resp, err
	}
	resp.Result = &result
	return resp, nil
}

func readRuntimeResult(opts workspaceOptions, identity vmkit.Identity) (vmkit.RuntimeResult, error) {
	guest, err := readGuestResult(opts)
	if err != nil {
		return vmkit.RuntimeResult{}, err
	}
	backend := opts.Backend
	if backend == "" {
		backend = identity.Backend
	}
	return vmkit.RuntimeResult{
		Identity:    identity,
		Backend:     backend,
		ResultPath:  resultPath(opts),
		StartedAt:   guest.StartedAt,
		CompletedAt: guest.ExitedAt,
		ExitCode:    guest.ExitCode,
		Stdout:      guest.Stdout,
		Stderr:      guest.Stderr,
		Error:       guest.Error,
	}, nil
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

func workspaceVerificationForStatus(opts workspaceOptions, name string, manifest workspaceManifest, state vmkit.VMState) *vmkit.RuntimeVerification {
	recorded := manifest.Verification
	if recorded == nil {
		if _, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: opts.StateDir, Name: name}); err != nil {
			return nil
		}
	}
	verification := vmkit.RuntimeVerification{OK: true}
	if recorded != nil {
		verification.ImageRef = recorded.ImageRef
		verification.ResolvedRef = recorded.ResolvedRef
		verification.ImageDigest = recorded.ImageDigest
	}
	kernelPath, rootfsPath := "", ""
	if state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: opts.StateDir, Name: name}); err == nil {
		kernelPath = state.Config.KernelPath
		rootfsPath = state.Config.RootfsPath
	}
	if kernelPath == "" && recorded != nil && recorded.Kernel != nil {
		kernelPath = recorded.Kernel.Path
	}
	if rootfsPath == "" && recorded != nil && recorded.Rootfs != nil {
		rootfsPath = recorded.Rootfs.Path
	}
	if rootfsPath == "" {
		rootfsPath = filepath.Join(opts.StateDir, "workspaces", name, "rootfs.ext4")
	}
	verification.Kernel = currentArtifact("kernel", kernelPath, recordedArtifactFor(recorded, "kernel"), &verification, true)
	verification.Rootfs = rootfsArtifactForStatus(rootfsPath, recordedArtifactFor(recorded, "rootfs"), &verification, state)
	if recorded != nil && recorded.Init != nil {
		verification.Init = currentArtifact("init", recorded.Init.Path, recorded.Init, &verification, true)
	}
	verification.OK = len(verification.Divergence) == 0
	return &verification
}

func recordedArtifactFor(recorded *vmkit.RuntimeVerification, name string) *vmkit.VerifiedArtifact {
	if recorded == nil {
		return nil
	}
	switch name {
	case "kernel":
		return recorded.Kernel
	case "rootfs":
		return recorded.Rootfs
	case "init":
		return recorded.Init
	default:
		return nil
	}
}

func shouldCompareRootfs(state vmkit.VMState) bool {
	return state == "" || state == vmkit.StateUnknown
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

func rootfsArtifactForStatus(path string, recorded *vmkit.VerifiedArtifact, verification *vmkit.RuntimeVerification, state vmkit.VMState) *vmkit.VerifiedArtifact {
	if !liveWorkspaceUnavailableState(state) {
		return currentArtifact("rootfs", path, recorded, verification, shouldCompareRootfs(state))
	}
	artifact := &vmkit.VerifiedArtifact{Path: path}
	if recorded != nil {
		artifact.RecordedSHA256 = recorded.SHA256
		artifact.SHA256 = recorded.SHA256
		if artifact.Path == "" {
			artifact.Path = recorded.Path
		}
	}
	if strings.TrimSpace(artifact.Path) == "" {
		artifact.Error = "path is empty"
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{Artifact: "rootfs", Error: artifact.Error})
	}
	return artifact
}

func currentArtifact(name, path string, recorded *vmkit.VerifiedArtifact, verification *vmkit.RuntimeVerification, compare bool) *vmkit.VerifiedArtifact {
	artifact := &vmkit.VerifiedArtifact{Path: path}
	if recorded != nil {
		artifact.RecordedSHA256 = recorded.SHA256
		if artifact.Path == "" {
			artifact.Path = recorded.Path
		}
	}
	if strings.TrimSpace(artifact.Path) == "" {
		artifact.Error = "path is empty"
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{Artifact: name, Error: artifact.Error})
		return artifact
	}
	sum, err := workspace.FileSHA256(artifact.Path)
	if err != nil {
		artifact.Error = err.Error()
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{Artifact: name, Error: err.Error()})
		return artifact
	}
	artifact.SHA256 = sum
	if compare && artifact.RecordedSHA256 != "" && artifact.RecordedSHA256 != artifact.SHA256 {
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{
			Artifact: name,
			Field:    "sha256",
			Expected: artifact.RecordedSHA256,
			Actual:   artifact.SHA256,
		})
	}
	return artifact
}

func workspaceReadinessForStatus(opts workspaceOptions, event workspaceEventFile) vmkit.RuntimeReadiness {
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: opts.StateDir, Name: event.Identity.RuntimeID})
	if err == nil {
		return workspaceReadinessFromRuntime(state)
	}
	readiness := vmkit.RuntimeReadiness{}
	if event.State == vmkit.StateRunning || event.State == vmkit.StateHalted || event.State == vmkit.StateStopped || event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: parseOptionalTime(event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(event.State),
		}
	}
	if _, statErr := os.Stat(resultPath(workspaceOptions{StateDir: opts.StateDir, Name: event.Identity.RuntimeID})); statErr == nil {
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: fileModTime(resultPath(workspaceOptions{StateDir: opts.StateDir, Name: event.Identity.RuntimeID})),
			Detail:     "guest result is available",
		}
	}
	return readiness
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	return os.WriteFile(path, data, 0o644)
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
}

func writeWorkspaceResultWithOptions(stdout *os.File, result workspaceResult, opts workspaceResultOptions) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.Response.Event != nil {
		fmt.Fprintf(stdout, "State: %s\n", result.Response.Event.State)
	} else if result.FinalState != "" {
		fmt.Fprintf(stdout, "State: %s\n", result.FinalState)
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
	})
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func inspectWorkspace(ctx context.Context, opts workspaceOptions) (vmkit.Response, error) {
	req := workspaceRequest(opts, "inspect", filepath.Join(opts.StateDir, "workspaces", opts.Name, "rootfs.ext4"))
	req.Config.RootfsPath = ""
	return dispatchWorkspaceRequest(ctx, opts, req)
}

func waitForWorkspace(ctx context.Context, opts workspaceOptions) (vmkit.Response, error) {
	deadline := time.Now().Add(opts.Timeout)
	for {
		resp, err := inspectWorkspace(ctx, opts)
		if err != nil {
			if resp.Error == "" {
				return resp, err
			}
			return resp, err
		}
		if resp.Event != nil {
			switch resp.Event.State {
			case vmkit.StateStopped:
				if opts.ResultPort != 0 {
					if err := waitForResultFile(ctx, opts, deadline); err != nil {
						return resp, err
					}
				}
				return resp, nil
			case vmkit.StateFailed:
				return resp, fmt.Errorf("workspace %s failed", opts.Name)
			}
		}
		if time.Now().After(deadline) {
			return resp, fmt.Errorf("workspace %s did not stop before timeout", opts.Name)
		}
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func waitForResultFile(ctx context.Context, opts workspaceOptions, deadline time.Time) error {
	for {
		if _, err := os.Stat(resultPath(opts)); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("workspace %s did not report a result before timeout", opts.Name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
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

func readGuestResult(opts workspaceOptions) (guestResult, error) {
	var result guestResult
	data, err := os.ReadFile(resultPath(opts))
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}

func cleanupWorkspaceState(opts workspaceOptions) {
	if validateWorkspaceName(opts.Name) != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(opts.StateDir, "workspaces", opts.Name))
	_ = os.RemoveAll(filepath.Join(opts.StateDir, opts.Name))
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

func workspaceArtifactsResult(stateDir, name string) (artifactsResult, error) {
	manifest, err := readWorkspaceManifest(stateDir, name)
	if err != nil {
		return artifactsResult{}, err
	}
	return artifactsResult{
		Workspace: name,
		Artifacts: runtimeArtifactsFromManifest(manifest.Artifacts),
	}, nil
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
	if err := os.MkdirAll(filepath.Join(stateDir, target), 0o755); err != nil {
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
	defer in.Close()
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

func listWorkspaces(stateDir string) ([]workspaceListEntry, error) {
	names := map[string]bool{}
	workspaceRoot := filepath.Join(stateDir, "workspaces")
	if dirs, err := os.ReadDir(workspaceRoot); err == nil {
		for _, dir := range dirs {
			if dir.IsDir() {
				names[dir.Name()] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if dirs, err := os.ReadDir(stateDir); err == nil {
		for _, dir := range dirs {
			if !dir.IsDir() || dir.Name() == "build" || dir.Name() == "workspaces" {
				continue
			}
			if _, err := os.Stat(filepath.Join(stateDir, dir.Name(), "event.json")); err == nil {
				names[dir.Name()] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	sortedNames := make([]string, 0, len(names))
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	entries := make([]workspaceListEntry, 0, len(sortedNames))
	for _, name := range sortedNames {
		entries = append(entries, workspaceListEntryFor(stateDir, name))
	}
	return entries, nil
}

func workspaceListEntryFor(stateDir, name string) workspaceListEntry {
	entry := workspaceListEntry{
		Name:       name,
		State:      string(vmkit.StateUnknown),
		RootfsPath: filepath.Join(stateDir, "workspaces", name, "rootfs.ext4"),
		SerialPath: filepath.Join(stateDir, name, "serial.log"),
	}
	var event vmkit.Event
	if data, err := os.ReadFile(filepath.Join(stateDir, name, "event.json")); err == nil {
		if err := json.Unmarshal(data, &event); err == nil {
			entry.State = string(event.State)
			entry.Backend = event.Identity.Backend
			entry.ObservedAt = event.ObservedAt.UTC().Format(time.RFC3339)
		}
	}
	if manifest, err := readWorkspaceManifest(stateDir, name); err == nil {
		entry.Profile = manifest.Profile
		entry.Restart = nonEmpty(manifest.Restart, defaultRestartPolicy)
		entry.Network = networkConfigFromSpec(manifest.Network).Mode
	}
	if _, err := os.Stat(entry.RootfsPath); os.IsNotExist(err) {
		entry.RootfsPath = ""
	}
	if _, err := os.Stat(entry.SerialPath); os.IsNotExist(err) {
		entry.SerialPath = ""
	}
	return entry
}

func inspectWorkspaceNetwork(stateDir, name string) (workspaceNetworkResult, error) {
	manifest, err := readWorkspaceManifest(stateDir, name)
	if err != nil {
		return workspaceNetworkResult{}, err
	}
	result := workspaceNetworkResult{
		Workspace: name,
		Network:   networkConfigFromSpec(manifest.Network),
	}
	if state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: stateDir, Name: name}); err == nil {
		result.State = string(state.Event.State)
		result.Backend = state.Event.Identity.Backend
		if state.Config.Network != nil {
			runtimeNetwork := normalizeNetworkConfig(*state.Config.Network)
			result.Runtime = &runtimeNetwork
		}
	} else if event, eventErr := readWorkspaceEvent(workspaceOptions{StateDir: stateDir, Name: name}); eventErr == nil {
		result.State = string(event.State)
		result.Backend = event.Identity.Backend
	}
	return result, nil
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
	if err := os.MkdirAll(filepath.Dir(imageIndexPath(stateDir)), 0o755); err != nil {
		return err
	}
	return writeJSONFile(imageIndexPath(stateDir), idx)
}

func pullImage(ctx context.Context, opts imagePullOptions) (imageRecord, error) {
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		return imageRecord{}, fmt.Errorf("image reference is required")
	}
	if opts.Architecture == "" {
		opts.Architecture = defaultGuestArch()
	}
	if opts.SizeMiB == 0 {
		opts.SizeMiB = rootfs.DefaultSizeMiB
	}
	if opts.SizeMiB < 0 {
		return imageRecord{}, fmt.Errorf("size-mib must not be negative")
	}
	if opts.Mke2fsPath == "" {
		opts.Mke2fsPath = defaultMke2fsPath()
	}
	if opts.GuestInitPath == "" {
		opts.GuestInitPath = defaultGuestInitPath(opts.Architecture)
	}
	outputPath := imageRootfsPath(opts.StateDir, opts.ImageRef, rootfs.Platform{OS: "linux", Architecture: opts.Architecture})
	provenance, err := rootfs.NewBuilder().Build(ctx, rootfs.BuildRequest{
		ImageRef:       opts.ImageRef,
		Platform:       rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:     outputPath,
		InitPath:       rootfs.DefaultInitPath,
		InitBinaryPath: opts.GuestInitPath,
		NoImageCommand: true,
		StateDir:       filepath.Join(opts.StateDir, "images", "build"),
		Mke2fsPath:     opts.Mke2fsPath,
		SizeMiB:        opts.SizeMiB,
		AllowMutable:   true,
	})
	if err != nil {
		return imageRecord{}, err
	}
	record := imageRecordFromProvenance(provenance)
	if err := upsertImageRecord(opts.StateDir, record); err != nil {
		return imageRecord{}, err
	}
	return record, nil
}

func imageRootfsPath(stateDir, imageRef string, platform rootfs.Platform) string {
	sum := sha256.Sum256([]byte(imageRef + "\x00" + platform.OS + "\x00" + platform.Architecture + "\x00" + platform.Variant))
	name := hex.EncodeToString(sum[:])[:24] + ".ext4"
	return filepath.Join(stateDir, "images", "rootfs", name)
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

func provenanceFromImageRecord(record imageRecord, outputPath string) rootfs.Provenance {
	return rootfs.Provenance{
		ImageRef:     record.ImageRef,
		ResolvedRef:  record.ResolvedRef,
		Digest:       record.Digest,
		Platform:     record.Platform,
		OutputPath:   outputPath,
		SizeBytes:    record.SizeBytes,
		Builder:      "microagent-image-store",
		BuilderPhase: "copy-baseline",
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

func findImageRecord(stateDir, ref string, platform rootfs.Platform) (imageRecord, error) {
	idx, err := readImageIndex(stateDir)
	if err != nil {
		return imageRecord{}, err
	}
	for _, image := range idx.Images {
		if !imageMatchesRef(image, ref) {
			continue
		}
		if platform.OS != "" && image.Platform.OS != "" && image.Platform.OS != platform.OS {
			continue
		}
		if platform.Architecture != "" && image.Platform.Architecture != "" && image.Platform.Architecture != platform.Architecture {
			continue
		}
		if image.OutputPath == "" {
			continue
		}
		if _, err := os.Stat(image.OutputPath); err != nil {
			continue
		}
		return image, nil
	}
	return imageRecord{}, fmt.Errorf("image %q not found", ref)
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

func imagePathInRootfsStore(stateDir, path string) bool {
	_, ok := canonicalImageStorePath(stateDir, path)
	return ok
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

func defaultWritableKernelPath(backend, arch string) string {
	return workspace.WritableKernelPath(backend, arch)
}

func defaultLegacyKernelPath(backend string) string {
	return workspace.LegacyKernelPath(backend)
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

func defaultPackagedKernelPath(backend, arch string) string {
	return workspace.PackagedKernelPath(backend, arch)
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

func shellCommand(command string) []string {
	return workspace.ShellCommand(command)
}

func workspaceCommand(opts workspaceOptions) string {
	return workspace.Command(opts)
}

func workspaceBuildCommandAndPort(opts workspaceOptions) ([]string, uint32) {
	return workspace.BuildCommandAndPort(opts)
}

func resetGuestConfigCommand(command []string, env map[string]string, port uint32, mounts []rootfs.Mount, forwards []rootfs.PortForward, consoleShell, hostname string) string {
	return workspace.ResetGuestConfigCommand(command, "", env, port, 0, 0, mounts, forwards, consoleShell, hostname)
}

func envList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		if validEnvName(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

func shellSingleQuote(value string) string {
	return workspace.ShellSingleQuote(value)
}

func workspaceHasGuestCommand(opts workspaceOptions) bool {
	return workspace.HasGuestCommand(opts)
}

func waitForPath(ctx context.Context, path string, timeout time.Duration) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear before timeout", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func openFIFOForWrite(ctx context.Context, path string, timeout time.Duration) (*os.File, error) {
	type openResult struct {
		file *os.File
		err  error
	}
	results := make(chan openResult, 1)
	go func() {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		results <- openResult{file: file, err: err}
	}()
	var timer <-chan time.Time
	if timeout > 0 {
		timer = time.After(timeout)
	}
	select {
	case result := <-results:
		return result.file, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer:
		return nil, fmt.Errorf("serial input is not ready: %s", path)
	}
}

func tailFile(ctx context.Context, path string, stdout *os.File, offset int64) error {
	return tailFileUntil(ctx, path, stdout, offset, 0, "")
}

func tailFileUntil(ctx context.Context, path string, stdout *os.File, offset int64, detectAfter int64, stopText string) error {
	for {
		file, err := os.Open(path)
		if err == nil {
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				_ = file.Close()
				return err
			}
			data, readErr := io.ReadAll(file)
			n, copyErr := stdout.Write(data)
			offset += int64(n)
			if closeErr := file.Close(); closeErr != nil {
				return closeErr
			}
			if readErr != nil {
				return readErr
			}
			if copyErr != nil {
				return copyErr
			}
			if stopText != "" && offset > detectAfter && bytes.Contains(dataAfterOffset(data, offset, detectAfter), []byte(stopText)) {
				return nil
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			if ctx.Err() == context.Canceled || ctx.Err() == context.DeadlineExceeded {
				return nil
			}
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
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
	defer file.Close()
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
		"-supervisor":        true,
		"-json":              true,
		"-id":                true,
		"-name":              true,
		"-image":             true,
		"-exec":              true,
		"-setup-file":        true,
		"-service-command":   true,
		"-entrypoint":        true,
		"-shell":             true,
		"-hostname":          true,
		"-file":              true,
		"-env":               true,
		"-setup":             true,
		"-request-id":        true,
		"-role":              true,
		"-backend":           true,
		"-kernel":            true,
		"-rootfs":            true,
		"-disk":              true,
		"-bundle":            true,
		"-volume":            true,
		"-v":                 true,
		"-output":            true,
		"-debugfs":           true,
		"-profile":           true,
		"-restart":           true,
		"-network":           true,
		"-network-interface": true,
		"-mediation":         true,
		"-publish":           true,
		"-p":                 true,
		"-state-dir":         true,
		"-tag":               true,
		"-url":               true,
		"-from":              true,
		"-sha256":            true,
		"-out":               true,
		"-path":              true,
		"-memory":            true,
		"-cpus":              true,
		"-vsock":             true,
		"-mke2fs":            true,
		"-guest-init":        true,
		"-arch":              true,
		"-size-mib":          true,
		"-timeout":           true,
		"-ready-timeout":     true,
		"-duration":          true,
		"-interval":          true,
		"-max-restarts":      true,
		"-result-port":       true,
		"-send":              true,
		"-e":                 true,
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
	case "-json", "-text", "-human", "-keep", "-rm", "-dry-run", "-image-command", "-mediation-optional", "-delete", "-yes", "-y", "-force", "-f", "-follow", "-images":
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

func normalizeMediationConfig(mediation vmkit.MediationConfig) vmkit.MediationConfig {
	mediation.Target = strings.TrimSpace(mediation.Target)
	if mediation.Port != 0 || mediation.Target != "" || mediation.Required || mediation.FailClosed {
		mediation.Enabled = true
	}
	if mediation.Required {
		mediation.FailClosed = true
	}
	return mediation
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
	if len(parts) < 2 || len(parts) > 3 {
		return workspaceDisk{}, fmt.Errorf("volume must be SRC:DST[:ro|rw]")
	}
	sourcePath := strings.TrimSpace(parts[0])
	mountpoint := strings.TrimSpace(parts[1])
	mode := "rw"
	if len(parts) == 3 {
		mode = strings.TrimSpace(parts[2])
	}
	if sourcePath == "" {
		return workspaceDisk{}, fmt.Errorf("volume source path is required")
	}
	if mountpoint == "" || !strings.HasPrefix(mountpoint, "/") {
		return workspaceDisk{}, fmt.Errorf("volume destination must be an absolute guest path")
	}
	if mode != "ro" && mode != "rw" {
		return workspaceDisk{}, fmt.Errorf("volume mode must be ro or rw")
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
		return workspaceDisk{}, fmt.Errorf("unsupported volume source %q; MicroAgent accepts tar archives as bundles or ext4 disk images, not named volumes or host bind mounts", sourcePath)
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

Commands:
  run                  Run a command
  create               Create a workspace
  apply                Apply supported workspace spec changes
  clone                Clone a stopped workspace
  cp                   Copy files into or out of a stopped workspace
  artifacts            List or retrieve declared workspace artifacts
  network              Inspect workspace network config
  start                Start a workspace
  supervise            Run host restart supervision for a workspace
  connect              Open the workspace console
  exec                 Run a structured command in a workspace
  ps                   List workspaces
  inspect              Alias for status with JSON output
  status               Show workspace state
  result               Show structured workspace result
  logs                 Show workspace logs
  events               Show or stream the lifecycle event history
  stats                Show or stream workspace resource usage
  snapshot             Create, list, or remove workspace snapshots
  profiles             List resource profiles
  images               List or prune local image records
  prune                Prune stale local records and optional image cache files
  perf                 Measure workspace performance
  serve mcp            Serve the MCP stdio endpoint
  halt                 Halt a workspace and preserve disk state
  quarantine           Sever host-side network and mediation
  pause                Pause a running workspace, freezing vCPUs with memory and disk preserved
  resume               Resume a paused workspace
  stop                 Stop a workspace
  kill                 Force stop a workspace
  delete               Delete a workspace
  rm                   Alias for delete
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
  -network <mode>       Network mode: user, nat, isolated, or bridged
  -network-interface <if>
                         Host interface for bridged network mode
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
  -network <mode>       Network mode: user, nat, isolated, or bridged
  -network-interface <if>
                         Host interface for bridged network mode
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

Container-style examples:
  microagent run alpine echo hello
  microagent run -e FOO=bar -p 8080:80 alpine
  microagent run -v /tmp/config.tar:/config:ro alpine ls /config

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
  -network <mode>       Network mode: user, nat, isolated, or bridged
  -network-interface <if>
                         Host interface for bridged network mode
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
