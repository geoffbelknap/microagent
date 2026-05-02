package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/rootfs"
	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

var version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		fmt.Fprintf(stdout, "microagent %s\n", version)
		return nil
	}
	if args[0] == "rootfs" {
		return runRootFS(ctx, args[1:], stdout)
	}
	if args[0] == "kernel" {
		return runKernel(ctx, args[1:], stdout)
	}
	if args[0] == "doctor" {
		return runDoctor(ctx, args[1:], stdout)
	}
	if args[0] == "run" {
		return runWorkspace(ctx, args[1:], stdout)
	}
	if args[0] == "ps" {
		return runPS(args[1:], stdout)
	}
	if args[0] == "logs" || args[0] == "log" {
		return runLogs(args[1:], stdout)
	}
	if args[0] == "status" || args[0] == "stop" || args[0] == "kill" || args[0] == "delete" {
		if hasWorkspaceStateTarget(args[1:]) {
			return runWorkspaceStateCommand(ctx, args[0], args[1:], stdout)
		}
	}
	if args[0] == "connect" {
		return runConnect(ctx, args[1:], stdout)
	}
	if args[0] == "start" && hasPositionalWorkspaceName(args[1:]) {
		return runStartWorkspace(ctx, args[1:], stdout)
	}
	if args[0] == "create" && hasFlagValue(args[1:], "image") {
		return runHighLevelCreate(ctx, args[1:], stdout)
	}
	helperPath := os.Getenv("MICROAGENT_APPLEVF_HELPER")
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&helperPath, "helper", helperPath, "Apple VF helper path")
	req, err := requestForCommand(args[0], fs, reorderFlagArgs(args[1:]))
	if err != nil {
		return err
	}
	resp, err := vmkit.HelperClient{Path: helperPath}.Do(ctx, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encodeErr := enc.Encode(resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

type doctorOptions struct {
	Backend    string
	Arch       string
	HelperPath string
}

func runDoctor(ctx context.Context, args []string, stdout *os.File) error {
	opts := doctorOptions{
		Backend:    defaultBackend(),
		Arch:       defaultGuestArch(),
		HelperPath: os.Getenv("MICROAGENT_APPLEVF_HELPER"),
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.HelperPath, "helper", opts.HelperPath, "Apple VF helper path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "VM backend")
	fs.StringVar(&opts.Arch, "arch", opts.Arch, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected doctor argument: %s", fs.Arg(0))
	}
	resp, err := doctorResponse(ctx, opts)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encodeErr := enc.Encode(resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

func doctorResponse(ctx context.Context, opts doctorOptions) (vmkit.Response, error) {
	switch opts.Backend {
	case vmkit.BackendAppleVF:
		resp, err := vmkit.HelperClient{Path: opts.HelperPath}.Do(ctx, vmkit.Request{Command: "host"})
		if resp.Backend == "" {
			resp.Backend = opts.Backend
		}
		resp.Kernel = defaultKernelSupport(opts.Backend, opts.Arch)
		return resp, err
	case vmkit.BackendFirecracker:
		return firecrackerDoctorResponse(opts.Backend, opts.Arch, resolveFirecrackerPath, os.Stat, firecrackerVersion)
	default:
		resp := vmkit.Response{
			OK:      false,
			Backend: opts.Backend,
			Kernel:  defaultKernelSupport(opts.Backend, opts.Arch),
			Error:   fmt.Sprintf("unsupported backend: %s", opts.Backend),
		}
		return resp, fmt.Errorf("%s", resp.Error)
	}
}

func firecrackerDoctorResponse(backend, arch string, resolveBinary func() (string, error), stat func(string) (os.FileInfo, error), binaryVersion func(string) string) (vmkit.Response, error) {
	host := &vmkit.HostSupport{
		Backend:      backend,
		Architecture: arch,
	}
	var issues []string
	if path, err := resolveBinary(); err == nil {
		host.BinaryPath = path
		host.BinaryVersion = binaryVersion(path)
		host.FrameworkAvailable = true
	} else {
		issues = append(issues, err.Error())
	}
	if _, err := stat("/dev/kvm"); err == nil {
		host.KVMAvailable = true
		host.VirtualizationSupported = true
	} else {
		issues = append(issues, "/dev/kvm is not available")
	}
	if _, err := stat("/dev/vhost-vsock"); err == nil {
		host.VsockAvailable = true
	}
	resp := vmkit.Response{
		OK:      len(issues) == 0,
		Backend: backend,
		Host:    host,
		Kernel:  defaultKernelSupport(backend, arch),
	}
	if len(issues) > 0 {
		resp.Error = strings.Join(issues, "; ")
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

func resolveFirecrackerPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("MICROAGENT_FIRECRACKER")); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("MICROAGENT_FIRECRACKER is not usable: %s", err)
		}
		return path, nil
	}
	if path, err := exec.LookPath("firecracker"); err == nil {
		return path, nil
	}
	if exe, err := os.Executable(); err == nil {
		path := defaultFirecrackerPathFromExecutable(exe)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("firecracker binary not found")
}

func defaultFirecrackerPathFromExecutable(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "libexec", "firecracker"))
}

func firecrackerVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
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
	opts := kernelOptions{Backend: defaultBackend(), Architecture: defaultGuestArch()}
	opts.OutputPath = defaultKernelPath(opts.Backend, opts.Architecture)
	fs := flag.NewFlagSet("kernel install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.URL, "url", "", "Kernel URL")
	fs.StringVar(&opts.FromPath, "from", "", "Local kernel path")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.OutputPath, "out", opts.OutputPath, "Output path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "VM backend")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected kernel install argument: %s", fs.Arg(0))
	}
	if opts.OutputPath == "" {
		opts.OutputPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	if opts.URL == "" && opts.FromPath == "" && opts.SHA256 == "" {
		kernel, ok := defaultKernel(opts.Backend, opts.Architecture)
		if !ok {
			return fmt.Errorf("no default kernel for %s/%s; use --url or --from", opts.Backend, opts.Architecture)
		}
		opts.URL = kernel.URL
		opts.SHA256 = kernel.SHA256
	}
	if err := installKernel(ctx, opts); err != nil {
		return err
	}
	sum, err := fileSHA256(opts.OutputPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]string{
		"path":   opts.OutputPath,
		"sha256": sum,
	})
}

func runKernelVerify(args []string, stdout *os.File) error {
	opts := kernelOptions{Backend: defaultBackend(), Architecture: defaultGuestArch()}
	opts.OutputPath = defaultKernelPath(opts.Backend, opts.Architecture)
	fs := flag.NewFlagSet("kernel verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.OutputPath, "path", opts.OutputPath, "Kernel path")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "VM backend")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected kernel verify argument: %s", fs.Arg(0))
	}
	if opts.OutputPath == "" {
		opts.OutputPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	sum, err := fileSHA256(opts.OutputPath)
	if err != nil {
		return err
	}
	ok := opts.SHA256 == "" || strings.EqualFold(opts.SHA256, sum)
	if !ok {
		return fmt.Errorf("kernel sha256 = %s, want %s", sum, opts.SHA256)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"ok":     true,
		"path":   opts.OutputPath,
		"sha256": sum,
	})
}

type kernelOptions struct {
	URL          string
	FromPath     string
	SHA256       string
	OutputPath   string
	Backend      string
	Architecture string
}

type kernelManifestEntry struct {
	Backend      string
	Architecture string
	URL          string
	SHA256       string
}

var defaultKernels = []kernelManifestEntry{
	{
		Backend:      vmkit.BackendAppleVF,
		Architecture: "arm64",
		URL:          "https://github.com/geoffbelknap/microagent-kit/releases/download/kernels-6.12.22-r1/microagent-kernel-6.12.22-apple-vf-arm64",
		SHA256:       "73fe78e51a8ce348e69311d376a02114440eee6b60bf2e91af54bdf2dfb405ec",
	},
}

func defaultKernel(backend, arch string) (kernelManifestEntry, bool) {
	for _, kernel := range defaultKernels {
		if kernel.Backend == backend && kernel.Architecture == arch {
			return kernel, true
		}
	}
	return kernelManifestEntry{}, false
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

func installKernel(ctx context.Context, opts kernelOptions) error {
	if (opts.URL == "") == (opts.FromPath == "") {
		return fmt.Errorf("kernel install requires exactly one of --url or --from")
	}
	if err := os.MkdirAll(filepath.Dir(opts.OutputPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(opts.OutputPath), ".kernel-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if opts.FromPath != "" {
		in, err := os.Open(opts.FromPath)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		_, err = io.Copy(tmp, in)
		closeErr := in.Close()
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if closeErr != nil {
			_ = tmp.Close()
			return closeErr
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if token := githubToken(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			_ = tmp.Close()
			return fmt.Errorf("download kernel: %s", resp.Status)
		}
		if _, err := io.Copy(tmp, resp.Body); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	sum, err := fileSHA256(tmpPath)
	if err != nil {
		return err
	}
	if opts.SHA256 != "" && !strings.EqualFold(opts.SHA256, sum) {
		return fmt.Errorf("kernel sha256 = %s, want %s", sum, opts.SHA256)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, opts.OutputPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func githubToken() string {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
	fs.StringVar(&jsonPath, "json", "", "Read request JSON from path, or '-' for stdin")
	fs.BoolVar(&dryRun, "dry-run", false, "Validate without writing state")
	fs.StringVar(&identity.RuntimeID, "id", "", "Workspace ID")
	fs.StringVar(&identity.RuntimeID, "name", "", "Workspace name")
	fs.StringVar(&identity.RequestID, "request-id", "", "Request ID")
	fs.StringVar((*string)(&identity.Role), "role", string(vmkit.RoleWorkload), "Role")
	fs.StringVar(&identity.Backend, "backend", vmkit.BackendAppleVF, "VM backend")
	fs.StringVar(&config.KernelPath, "kernel", "", "Linux kernel path")
	fs.StringVar(&config.RootfsPath, "rootfs", "", "Rootfs image path")
	fs.StringVar(&config.StateDir, "state-dir", "", "Runtime state directory")
	fs.IntVar(&config.MemoryMiB, "memory", 512, "Memory in MiB")
	fs.IntVar(&config.CPUCount, "cpus", 2, "CPU count")
	fs.Var(&vsocks, "vsock", "Vsock mapping port=host:port")
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
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, vsocks)
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
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, vsocks)
		if err != nil {
			return vmkit.Request{}, err
		}
		req.Command = "start"
		return req, nil
	case "status", "stop", "kill", "delete":
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

type workspaceOptions struct {
	Name            string
	ImageRef        string
	ExecCommand     string
	SetupCommands   []string
	Backend         string
	KernelPath      string
	StateDir        string
	HelperPath      string
	GuestInitPath   string
	Mke2fsPath      string
	Architecture    string
	MemoryMiB       int
	CPUCount        int
	SizeMiB         int64
	Timeout         time.Duration
	ResultPort      uint32
	KernelExplicit  bool
	Keep            bool
	PrepareForStart bool
	SerialInput     bool
}

type workspaceResult struct {
	Workspace  string            `json:"workspace"`
	StateDir   string            `json:"state_dir"`
	RootfsPath string            `json:"rootfs_path"`
	KernelPath string            `json:"kernel_path"`
	SerialPath string            `json:"serial_path,omitempty"`
	SerialLog  string            `json:"serial_log,omitempty"`
	FinalState string            `json:"final_state,omitempty"`
	Result     *guestResult      `json:"result,omitempty"`
	Image      rootfs.Provenance `json:"image"`
	Response   vmkit.Response    `json:"response"`
}

type guestResult struct {
	StartedAt string `json:"started_at"`
	ExitedAt  string `json:"exited_at"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
}

type workspaceListEntry struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Backend    string `json:"backend,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
	RootfsPath string `json:"rootfs_path,omitempty"`
	SerialPath string `json:"serial_path,omitempty"`
}

func runWorkspace(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRunHelp(stdout)
		return nil
	}
	opts, err := parseWorkspaceOptions("run", args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.ExecCommand) == "" {
		return fmt.Errorf("run requires --exec")
	}
	if opts.Name == "" {
		opts.Name = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	if err := ensureWorkspaceKernel(ctx, &opts); err != nil {
		return err
	}
	result, err := createWorkspaceRootfs(ctx, opts)
	if err != nil {
		return err
	}
	req := workspaceRequest(opts, "run", result.RootfsPath)
	resp, err := runWorkspaceForeground(ctx, opts, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	result.Response = resp
	result.SerialPath = serialLogPath(opts)
	if err == nil && resp.OK {
		finalResp, waitErr := inspectWorkspace(ctx, opts)
		if finalResp.Event != nil {
			result.FinalState = string(finalResp.Event.State)
			result.Response = finalResp
		}
		if serial, readErr := os.ReadFile(result.SerialPath); readErr == nil {
			result.SerialLog = string(serial)
		}
		if guest, readErr := readGuestResult(opts); readErr == nil {
			result.Result = &guest
		}
		if waitErr != nil {
			return waitErr
		}
		if !opts.Keep {
			cleanupWorkspaceState(opts)
			result.SerialPath = ""
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encodeErr := enc.Encode(result); encodeErr != nil {
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
	entries, err := listWorkspaces(opts.StateDir)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"workspaces": entries})
}

func runLogs(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent logs <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(opts.StateDir, name, "serial.log"))
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

func runWorkspaceStateCommand(ctx context.Context, command string, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	helperPath := os.Getenv("MICROAGENT_APPLEVF_HELPER")
	backend := defaultBackend()
	name := ""
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&helperPath, "helper", helperPath, "Apple VF helper path")
	fs.StringVar(&backend, "backend", backend, "VM backend")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
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
	resp, err := vmkit.HelperClient{Path: helperPath}.Do(ctx, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	if command == "delete" && resp.OK {
		cleanupWorkspaceState(workspaceOptions{StateDir: opts.StateDir, Name: name})
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encodeErr := enc.Encode(resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runConnect(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	send := fs.String("send", "", "Write text to the console and exit")
	timeoutSeconds := fs.Int("timeout", 2, "Seconds to wait for output after --send")
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
	inputPath := serialInputPath(opts.StateDir, name)
	logPath := filepath.Join(opts.StateDir, name, "serial.log")
	if strings.TrimSpace(*send) != "" {
		if *timeoutSeconds < 0 {
			return fmt.Errorf("connect timeout must not be negative")
		}
		if err := waitForPath(ctx, inputPath, time.Duration(*timeoutSeconds)*time.Second); err != nil {
			return err
		}
		if err := waitForConsoleReady(ctx, logPath, time.Duration(*timeoutSeconds)*time.Second); err != nil {
			return err
		}
		before := fileSize(logPath)
		input, err := openFIFOForWrite(ctx, inputPath, time.Duration(*timeoutSeconds)*time.Second)
		if err != nil {
			return err
		}
		text := *send
		text = strings.ReplaceAll(text, "\n", "\r")
		if !strings.HasSuffix(text, "\r") {
			text += "\r"
		}
		if _, err := io.WriteString(input, text); err != nil {
			_ = input.Close()
			return err
		}
		if err := input.Close(); err != nil {
			return err
		}
		tailCtx, cancel := context.WithTimeout(ctx, time.Duration(*timeoutSeconds)*time.Second)
		defer cancel()
		return tailFile(tailCtx, logPath, stdout, before)
	}
	if err := waitForPath(ctx, inputPath, 0); err != nil {
		return err
	}
	input, err := openFIFOForWrite(ctx, inputPath, 0)
	if err != nil {
		return err
	}
	defer input.Close()
	tailCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 1)
	go func() {
		errs <- tailFile(tailCtx, logPath, stdout, 0)
	}()
	if _, err := copyConsoleInput(input, os.Stdin); err != nil {
		cancel()
		<-errs
		return err
	}
	cancel()
	<-errs
	return nil
}

func runStartWorkspace(ctx context.Context, args []string, stdout *os.File) error {
	opts := workspaceOptions{
		Backend:      defaultBackend(),
		Architecture: defaultGuestArch(),
		MemoryMiB:    512,
		CPUCount:     2,
		StateDir:     defaultStateDir(),
		HelperPath:   os.Getenv("MICROAGENT_APPLEVF_HELPER"),
		SerialInput:  true,
	}
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	kernelExplicit := hasFlagValue(args, "kernel")
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.HelperPath, "helper", opts.HelperPath, "Apple VF helper path")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "VM backend")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent start <name> [--state-dir <dir>]")
	}
	opts.Name = fs.Arg(0)
	opts.KernelExplicit = kernelExplicit
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	if !opts.KernelExplicit {
		opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	if err := ensureWorkspaceKernel(ctx, &opts); err != nil {
		return err
	}
	rootfsPath := filepath.Join(opts.StateDir, "workspaces", opts.Name, "rootfs.ext4")
	if _, err := os.Stat(rootfsPath); err != nil {
		return err
	}
	req := workspaceRequest(opts, "run", rootfsPath)
	resp, err := startWorkspaceDetached(opts, req)
	result := workspaceResult{
		Workspace:  opts.Name,
		StateDir:   opts.StateDir,
		RootfsPath: rootfsPath,
		KernelPath: opts.KernelPath,
		SerialPath: serialLogPath(opts),
		Response:   resp,
	}
	if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runHighLevelCreate(ctx context.Context, args []string, stdout *os.File) error {
	opts, err := parseWorkspaceOptions("create", args)
	if err != nil {
		return err
	}
	opts.PrepareForStart = true
	if opts.Name == "" {
		return fmt.Errorf("create requires --name")
	}
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	if err := ensureWorkspaceKernel(ctx, &opts); err != nil {
		return err
	}
	result, err := createWorkspaceRootfs(ctx, opts)
	if err != nil {
		return err
	}
	if workspaceHasGuestCommand(opts) {
		startReq := workspaceRequest(opts, "run", result.RootfsPath)
		startResp, startErr := runWorkspaceForeground(ctx, opts, startReq)
		result.Response = startResp
		result.SerialPath = serialLogPath(opts)
		if startErr != nil {
			if startResp.Error == "" {
				return startErr
			}
			return startErr
		}
		finalResp, waitErr := inspectWorkspace(ctx, opts)
		if finalResp.Event != nil {
			result.FinalState = string(finalResp.Event.State)
			result.Response = finalResp
		}
		if serial, readErr := os.ReadFile(result.SerialPath); readErr == nil {
			result.SerialLog = string(serial)
		}
		if guest, readErr := readGuestResult(opts); readErr == nil {
			result.Result = &guest
		}
		if waitErr != nil {
			return waitErr
		}
	} else {
		req := workspaceRequest(opts, "prepare", result.RootfsPath)
		resp, err := vmkit.HelperClient{Path: opts.HelperPath}.Do(ctx, req)
		if err != nil {
			if resp.Error == "" {
				return err
			}
		}
		result.Response = resp
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encodeErr := enc.Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}

type stateCommandOptions struct {
	StateDir string
}

func parseWorkspaceOptions(command string, args []string) (workspaceOptions, error) {
	kernelExplicit := hasFlagValue(args, "kernel")
	opts := workspaceOptions{
		Backend:      defaultBackend(),
		Architecture: defaultGuestArch(),
		MemoryMiB:    512,
		CPUCount:     2,
		SizeMiB:      rootfs.DefaultSizeMiB,
		Timeout:      2 * time.Minute,
		ResultPort:   1024,
	}
	opts.StateDir = defaultStateDir()
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.Mke2fsPath = defaultMke2fsPath()
	opts.GuestInitPath = defaultGuestInitPath(opts.Architecture)
	opts.HelperPath = os.Getenv("MICROAGENT_APPLEVF_HELPER")
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Name, "name", "", "Workspace name")
	fs.StringVar(&opts.Name, "id", "", "Workspace ID")
	fs.StringVar(&opts.ImageRef, "image", "", "OCI image reference")
	fs.StringVar(&opts.ExecCommand, "exec", "", "Shell command to run as guest init")
	var setupCommands multiFlag
	fs.Var(&setupCommands, "setup", "Shell command to run before --exec")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "VM backend")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "Microagent state directory")
	fs.StringVar(&opts.HelperPath, "helper", opts.HelperPath, "Apple VF helper path")
	fs.StringVar(&opts.GuestInitPath, "guest-init", opts.GuestInitPath, "Guest init path")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	fs.Int64Var(&opts.SizeMiB, "size-mib", opts.SizeMiB, "Rootfs image size in MiB")
	resultPort := uint(opts.ResultPort)
	fs.UintVar(&resultPort, "result-port", resultPort, "Vsock result port")
	var timeoutSeconds int
	fs.IntVar(&timeoutSeconds, "timeout", int(opts.Timeout.Seconds()), "Run timeout in seconds")
	fs.BoolVar(&opts.Keep, "keep", false, "Keep workspace state after run")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return workspaceOptions{}, err
	}
	if fs.NArg() != 0 {
		return workspaceOptions{}, fmt.Errorf("unexpected %s argument: %s", command, fs.Arg(0))
	}
	opts.SetupCommands = append([]string{}, setupCommands...)
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		return workspaceOptions{}, fmt.Errorf("%s requires --image", command)
	}
	if !kernelExplicit {
		opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	opts.KernelExplicit = kernelExplicit
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
	kernel, ok := defaultKernel(opts.Backend, opts.Architecture)
	if !ok {
		return fmt.Errorf("no default kernel for %s/%s; pass --kernel", opts.Backend, opts.Architecture)
	}
	return installKernel(ctx, kernelOptions{
		URL:        kernel.URL,
		SHA256:     kernel.SHA256,
		OutputPath: opts.KernelPath,
	})
}

func createWorkspaceRootfs(ctx context.Context, opts workspaceOptions) (workspaceResult, error) {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	rootfsPath := filepath.Join(workspaceDir, "rootfs.ext4")
	req := rootfs.BuildRequest{
		ImageRef:       opts.ImageRef,
		Platform:       rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:     rootfsPath,
		InitPath:       rootfs.DefaultInitPath,
		Command:        shellCommand(workspaceCommand(opts)),
		InitBinaryPath: opts.GuestInitPath,
		ResultPort:     opts.ResultPort,
		StateDir:       filepath.Join(opts.StateDir, "build"),
		Mke2fsPath:     opts.Mke2fsPath,
		SizeMiB:        opts.SizeMiB,
		AllowMutable:   true,
	}
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	return workspaceResult{
		Workspace:  opts.Name,
		StateDir:   opts.StateDir,
		RootfsPath: rootfsPath,
		KernelPath: opts.KernelPath,
		Image:      provenance,
	}, err
}

func workspaceRequest(opts workspaceOptions, command, rootfsPath string) vmkit.Request {
	var listeners []vmkit.VsockListener
	if opts.ResultPort != 0 {
		listeners = []vmkit.VsockListener{{Port: opts.ResultPort, Target: resultPath(opts)}}
	}
	return vmkit.Request{
		Command: command,
		Identity: &vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   opts.Backend,
		},
		Config: &vmkit.Config{
			KernelPath:     opts.KernelPath,
			RootfsPath:     rootfsPath,
			StateDir:       opts.StateDir,
			MemoryMiB:      opts.MemoryMiB,
			CPUCount:       opts.CPUCount,
			VsockListeners: listeners,
			SerialInput:    opts.SerialInput,
		},
	}
}

func runWorkspaceForeground(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateStarting, 0, ""); err != nil {
		return vmkit.Response{}, err
	}
	resp, err := vmkit.HelperClient{Path: opts.HelperPath}.Do(ctx, req)
	state := vmkit.StateStopped
	errorText := ""
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
	path := opts.HelperPath
	if path == "" {
		path = "microagent-applevf-helper"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return vmkit.Response{}, err
	}
	if err := os.MkdirAll(filepath.Join(opts.StateDir, opts.Name), 0o755); err != nil {
		return vmkit.Response{}, err
	}
	helperLogPath := filepath.Join(opts.StateDir, opts.Name, "helper.log")
	helperLog, err := os.OpenFile(helperLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return vmkit.Response{}, err
	}
	defer helperLog.Close()
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Stdout = helperLog
	cmd.Stderr = helperLog
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return vmkit.Response{}, err
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateStarting, cmd.Process.Pid, ""); err != nil {
		_ = cmd.Process.Kill()
		return vmkit.Response{}, err
	}
	_ = cmd.Process.Release()
	event := vmkit.Event{
		Identity:   *req.Identity,
		State:      vmkit.StateStarting,
		Detail:     "serial=" + serialLogPath(opts),
		ObservedAt: time.Now().UTC(),
	}
	return vmkit.Response{OK: true, Backend: opts.Backend, Event: &event}, nil
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
	if state == vmkit.StateStarting || state == vmkit.StateRunning {
		runtimeState.StartedAt = updatedAt.Format(time.RFC3339)
	}
	return writeJSONFile(filepath.Join(dir, "runtime.json"), runtimeState)
}

type workspaceEventFile struct {
	Identity   vmkit.Identity `json:"identity"`
	State      vmkit.VMState  `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ObservedAt string         `json:"observedAt"`
}

type workspaceRuntimeState struct {
	Event           workspaceEventFile `json:"event"`
	Config          vmkit.Config       `json:"config"`
	PID             int                `json:"pid,omitempty"`
	SerialLogPath   string             `json:"serialLogPath"`
	SerialInputPath string             `json:"serialInputPath,omitempty"`
	StartedAt       string             `json:"startedAt,omitempty"`
	UpdatedAt       string             `json:"updatedAt"`
	Error           string             `json:"error,omitempty"`
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func inspectWorkspace(ctx context.Context, opts workspaceOptions) (vmkit.Response, error) {
	req := workspaceRequest(opts, "inspect", filepath.Join(opts.StateDir, "workspaces", opts.Name, "rootfs.ext4"))
	req.Config.RootfsPath = ""
	return vmkit.HelperClient{Path: opts.HelperPath}.Do(ctx, req)
}

func waitForWorkspace(ctx context.Context, opts workspaceOptions) (vmkit.Response, error) {
	deadline := time.Now().Add(opts.Timeout)
	req := workspaceRequest(opts, "inspect", filepath.Join(opts.StateDir, "workspaces", opts.Name, "rootfs.ext4"))
	req.Config.RootfsPath = ""
	for {
		resp, err := vmkit.HelperClient{Path: opts.HelperPath}.Do(ctx, req)
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
	_ = os.RemoveAll(filepath.Join(opts.StateDir, "workspaces", opts.Name))
	_ = os.RemoveAll(filepath.Join(opts.StateDir, opts.Name))
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
	if _, err := os.Stat(entry.RootfsPath); os.IsNotExist(err) {
		entry.RootfsPath = ""
	}
	if _, err := os.Stat(entry.SerialPath); os.IsNotExist(err) {
		entry.SerialPath = ""
	}
	return entry
}

func validateWorkspaceName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("workspace name is required")
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("invalid workspace name: %s", name)
	}
	return nil
}

func defaultBackend() string {
	if runtime.GOOS == "darwin" {
		return vmkit.BackendAppleVF
	}
	return "firecracker"
}

func defaultGuestArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), "microagent")
	}
	return filepath.Join(home, ".microagent")
}

func defaultKernelPath(backend, arch string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	path := filepath.Join(home, ".microagent", "kernels", backend, arch, "Image")
	legacy := filepath.Join(home, ".microagent", "kernels", backend, "Image")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return path
}

func defaultMke2fsPath() string {
	if path, err := exec.LookPath("mke2fs"); err == nil {
		return path
	}
	if _, err := os.Stat("/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"); err == nil {
		return "/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
	}
	return "mke2fs"
}

func defaultGuestInitPath(arch string) string {
	executable, err := os.Executable()
	if err != nil {
		return "microagent-guestinit"
	}
	return defaultGuestInitPathFromExecutable(executable, arch)
}

func defaultGuestInitPathFromExecutable(executable, arch string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	dir := filepath.Dir(executable)
	libexecDir := filepath.Clean(filepath.Join(dir, "..", "libexec"))
	candidates := []string{
		filepath.Join(libexecDir, "microagent-guestinit-"+arch),
		filepath.Join(libexecDir, "microagent-guestinit"),
		filepath.Join(dir, "microagent-guestinit-"+arch),
		filepath.Join(dir, "microagent-guestinit"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

func hasFlagValue(args []string, name string) bool {
	long := "--" + name
	short := "-" + name
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == long || arg == short {
			return i+1 < len(args)
		}
		if strings.HasPrefix(arg, long+"=") || strings.HasPrefix(arg, short+"=") {
			return true
		}
	}
	return false
}

func hasPositionalWorkspaceName(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			return true
		}
		if arg == "--json" || arg == "-json" || arg == "--rootfs" || arg == "-rootfs" || arg == "--kernel" || arg == "-kernel" || arg == "--name" || arg == "-name" || arg == "--id" || arg == "-id" {
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
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return []string{"/bin/sh", "-lc", command}
}

func workspaceCommand(opts workspaceOptions) string {
	var lines []string
	for _, command := range opts.SetupCommands {
		command = strings.TrimSpace(command)
		if command != "" {
			lines = append(lines, command)
		}
	}
	execCommand := strings.TrimSpace(opts.ExecCommand)
	if execCommand != "" {
		lines = append(lines, execCommand)
	}
	if opts.PrepareForStart {
		lines = append(lines, resetGuestConfigCommand(0))
	}
	if len(lines) == 0 {
		return ""
	}
	return "set -eu\n" + strings.Join(lines, "\n")
}

func resetGuestConfigCommand(port uint32) string {
	data, err := json.Marshal(map[string]any{
		"command": []string{},
		"port":    port,
	})
	if err != nil {
		panic(err)
	}
	return "printf '%s\\n' " + shellSingleQuote(string(data)) + " > /etc/microagent/run.json"
}

func shellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func workspaceHasGuestCommand(opts workspaceOptions) bool {
	if strings.TrimSpace(opts.ExecCommand) != "" {
		return true
	}
	for _, command := range opts.SetupCommands {
		if strings.TrimSpace(command) != "" {
			return true
		}
	}
	return false
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
	for {
		file, err := os.Open(path)
		if err == nil {
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				_ = file.Close()
				return err
			}
			n, copyErr := io.Copy(stdout, file)
			offset += n
			if closeErr := file.Close(); closeErr != nil {
				return closeErr
			}
			if copyErr != nil {
				return copyErr
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

func waitForConsoleReady(ctx context.Context, path string, timeout time.Duration) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		data, err := os.ReadFile(path)
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

func consoleLooksReady(output string) bool {
	return strings.Contains(output, "# ") ||
		strings.Contains(output, "$ ") ||
		strings.Contains(strings.ToLower(output), "login:")
}

func copyConsoleInput(dst io.Writer, src io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 4096)
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := bytes.ReplaceAll(buffer[:n], []byte("\n"), []byte("\r"))
			written, writeErr := dst.Write(chunk)
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != len(chunk) {
				return total, io.ErrShortWrite
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

func requestFromFlagsOrJSON(jsonPath string, args []string, identity vmkit.Identity, config vmkit.Config, vsocks []string) (vmkit.Request, error) {
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
	config.VsockListeners = nil
	for _, raw := range vsocks {
		listener, err := parseVsock(raw)
		if err != nil {
			return vmkit.Request{}, err
		}
		config.VsockListeners = append(config.VsockListeners, listener)
	}
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
		"-helper":      true,
		"-json":        true,
		"-id":          true,
		"-name":        true,
		"-image":       true,
		"-exec":        true,
		"-setup":       true,
		"-request-id":  true,
		"-role":        true,
		"-backend":     true,
		"-kernel":      true,
		"-rootfs":      true,
		"-state-dir":   true,
		"-url":         true,
		"-from":        true,
		"-sha256":      true,
		"-out":         true,
		"-path":        true,
		"-memory":      true,
		"-cpus":        true,
		"-vsock":       true,
		"-mke2fs":      true,
		"-guest-init":  true,
		"-arch":        true,
		"-size-mib":    true,
		"-timeout":     true,
		"-result-port": true,
		"-send":        true,
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
		flags = append(flags, arg)
		if valueFlags[arg] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
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

func printHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent

Commands:
  run                  Run a command
  create               Create a workspace
  start                Start a workspace
  connect              Open the workspace console
  ps                   List workspaces
  status               Show workspace state
  logs                 Show workspace logs
  stop                 Stop a workspace
  kill                 Force stop a workspace
  delete               Delete a workspace
  doctor               Check the host
  rootfs build         Build a rootfs from an OCI image
  version              Print the version
  help                 Show help

Advanced:
  kernel install       Install a custom kernel
  kernel verify        Verify a custom kernel

Options:
  -helper <path>        Override the Apple VF helper path
  -json <path|- >       Read request JSON from a file or stdin
  -image <ref>          OCI image
  -name <name>          Workspace name
  -id <id>              Workspace ID
  -kernel <path>        Custom kernel path
  -rootfs <path>        Rootfs image path
  -state-dir <dir>      State directory
  -memory <MiB>         Memory in MiB
  -cpus <n>             CPU count
  -vsock p=host:port    Add a vsock mapping
`)
}

func printRunHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent run

Run a command from an image.

Options:
  -image <ref>          OCI image
  -exec <command>       Shell command to run
  -setup <command>      Shell command to run before --exec
  -name <name>          Workspace name; generated when omitted
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -memory <MiB>         Memory in MiB
  -cpus <n>             CPU count
  -size-mib <MiB>       Disk size
  -timeout <seconds>    Timeout
  -keep                 Keep state
  -mke2fs <path>        mke2fs binary path
  -helper <path>        Override the Apple VF helper path
`)
}

func printKernelHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent kernel

Advanced kernel commands. Most users can start with microagent run --image ...
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
