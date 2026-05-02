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

var (
	version      = "dev"
	outputFormat string
)

const (
	defaultWorkspaceImageArm64 = "docker.io/library/busybox@sha256:bd44eb136a95dcc8dc58995e43abc40a413f2e8e3d4a2aae6bccbe94686acb05"
	defaultWorkspaceImageAMD64 = "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
	defaultWorkspaceImageOther = "docker.io/library/busybox:1.36.1"
	defaultWorkspaceMemoryMiB  = 512
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	outputFormat = ""
	args = parseGlobalFlags(args)
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
	if args[0] == "create" && wantsHelp(args[1:]) {
		printCreateHelp(stdout)
		return nil
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
	if args[0] == "create" && shouldUseHighLevelCreate(args[1:]) {
		return runHighLevelCreate(ctx, args[1:], stdout)
	}
	supervisorPath := os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR")
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	req, err := requestForCommand(args[0], fs, reorderFlagArgs(args[1:]))
	if err != nil {
		return err
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

type doctorOptions struct {
	Backend        string
	Arch           string
	SupervisorPath string
}

func runDoctor(ctx context.Context, args []string, stdout *os.File) error {
	opts := doctorOptions{
		Backend:        hostBackend(),
		Arch:           defaultGuestArch(),
		SupervisorPath: os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR"),
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend override")
	fs.StringVar(&opts.Arch, "arch", opts.Arch, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected doctor argument: %s", fs.Arg(0))
	}
	resp, err := doctorResponse(ctx, opts)
	if encodeErr := writeDoctorResponse(stdout, resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

func doctorResponse(ctx context.Context, opts doctorOptions) (vmkit.Response, error) {
	switch opts.Backend {
	case vmkit.BackendAppleVF:
		resp, err := vmkit.SupervisorClient{Path: opts.SupervisorPath}.Do(ctx, vmkit.Request{Command: "host"})
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
	return firstOutputLine(string(output))
}

func firstOutputLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
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
	opts := kernelOptions{Backend: hostBackend(), Architecture: defaultGuestArch()}
	opts.OutputPath = defaultKernelPath(opts.Backend, opts.Architecture)
	fs := flag.NewFlagSet("kernel install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.URL, "url", "", "Kernel URL")
	fs.StringVar(&opts.FromPath, "from", "", "Local kernel path")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.OutputPath, "out", opts.OutputPath, "Output path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend override")
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
	opts := kernelOptions{Backend: hostBackend(), Architecture: defaultGuestArch()}
	opts.OutputPath = defaultKernelPath(opts.Backend, opts.Architecture)
	fs := flag.NewFlagSet("kernel verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.OutputPath, "path", opts.OutputPath, "Kernel path")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend override")
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
	{
		Backend:      vmkit.BackendFirecracker,
		Architecture: "amd64",
		URL:          "https://github.com/geoffbelknap/microagent-kernels/releases/download/kernels-6.1.155-r2/microagent-kernel-6.1.155-firecracker-amd64",
		SHA256:       "4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0",
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
	var disks multiFlag
	fs.StringVar(&jsonPath, "json", "", "Read request JSON from path, or '-' for stdin")
	fs.BoolVar(&dryRun, "dry-run", false, "Validate without writing state")
	fs.StringVar(&identity.RuntimeID, "id", "", "Workspace ID")
	fs.StringVar(&identity.RuntimeID, "name", "", "Workspace name")
	fs.StringVar(&identity.RequestID, "request-id", "", "Request ID")
	fs.StringVar((*string)(&identity.Role), "role", string(vmkit.RoleWorkload), "Role")
	fs.StringVar(&identity.Backend, "backend", hostBackend(), "Backend override")
	fs.StringVar(&config.KernelPath, "kernel", "", "Linux kernel path")
	fs.StringVar(&config.RootfsPath, "rootfs", "", "Rootfs image path")
	fs.StringVar(&config.StateDir, "state-dir", "", "State directory")
	fs.IntVar(&config.MemoryMiB, "memory", 512, "Memory in MiB")
	fs.IntVar(&config.CPUCount, "cpus", 2, "CPU count")
	fs.Var(&disks, "disk", "Attach disk name=path:/mount:ro|rw")
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
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, disks, vsocks)
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
		req, err := requestFromFlagsOrJSON(jsonPath, args, identity, config, disks, vsocks)
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
	Entrypoint      string
	SetupCommands   []string
	Env             map[string]string
	Backend         string
	KernelPath      string
	StateDir        string
	SupervisorPath  string
	GuestInitPath   string
	Mke2fsPath      string
	Architecture    string
	MemoryMiB       int
	CPUCount        int
	SizeMiB         int64
	Timeout         time.Duration
	ResultPort      uint32
	Disks           []workspaceDisk
	VsockListeners  []vmkit.VsockListener
	KernelExplicit  bool
	Keep            bool
	PrepareForStart bool
	SerialInput     bool
}

type workspaceDisk struct {
	Name       string `json:"name"`
	SourcePath string `json:"source_path,omitempty"`
	Path       string `json:"path"`
	Mountpoint string `json:"mountpoint"`
	Mode       string `json:"mode"`
	Bundle     bool   `json:"bundle,omitempty"`
}

type workspaceManifest struct {
	Name  string          `json:"name"`
	Disks []workspaceDisk `json:"disks,omitempty"`
}

type workspaceResult struct {
	Workspace  string            `json:"workspace"`
	StateDir   string            `json:"state_dir"`
	RootfsPath string            `json:"rootfs_path"`
	KernelPath string            `json:"kernel_path"`
	Disks      []workspaceDisk   `json:"disks,omitempty"`
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
	disks, err := prepareWorkspaceDisks(ctx, opts)
	if err != nil {
		return err
	}
	opts.Disks = disks
	result, err := createWorkspaceRootfs(ctx, opts)
	if err != nil {
		return err
	}
	result.Disks = disks
	if err := writeWorkspaceManifest(opts); err != nil {
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
	entries, err := listWorkspaces(opts.StateDir)
	if err != nil {
		return err
	}
	return writeWorkspaceList(stdout, entries)
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
	supervisorPath := os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR")
	backend := hostBackend()
	name := ""
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&backend, "backend", backend, "Backend override")
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
	workspaceOpts := workspaceOptions{StateDir: opts.StateDir, Name: name, Backend: backend, SupervisorPath: supervisorPath}
	if command == "status" {
		resp, err := inspectWorkspaceState(workspaceOpts)
		if err != nil {
			return err
		}
		return writeResponse(stdout, resp)
	}
	resp, err := dispatchWorkspaceRequest(ctx, workspaceOpts, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	if command == "delete" && resp.OK {
		cleanupWorkspaceState(workspaceOptions{StateDir: opts.StateDir, Name: name})
	}
	if encodeErr := writeResponse(stdout, resp); encodeErr != nil {
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
	if state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: opts.StateDir, Name: name}); err == nil && state.Event.Identity.Backend == vmkit.BackendFirecracker {
		return fmt.Errorf("firecracker connect is not supported; use microagent logs")
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
		Backend:        hostBackend(),
		Architecture:   defaultGuestArch(),
		MemoryMiB:      defaultWorkspaceMemoryMiB,
		CPUCount:       2,
		StateDir:       defaultStateDir(),
		SupervisorPath: os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR"),
		SerialInput:    false,
	}
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	kernelExplicit := hasFlagValue(args, "kernel")
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend override")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	var vsocks multiFlag
	fs.Var(&vsocks, "vsock", "Vsock mapping port=host:port")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	opts.SerialInput = opts.Backend == vmkit.BackendAppleVF
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
	listeners, err := parseVsockMappings(vsocks)
	if err != nil {
		return err
	}
	opts.VsockListeners = listeners
	if err := ensureWorkspaceKernel(ctx, &opts); err != nil {
		return err
	}
	rootfsPath := filepath.Join(opts.StateDir, "workspaces", opts.Name, "rootfs.ext4")
	if _, err := os.Stat(rootfsPath); err != nil {
		return err
	}
	manifest, err := readWorkspaceManifest(opts.StateDir, opts.Name)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		opts.Disks = manifest.Disks
	}
	req := workspaceRequest(opts, "run", rootfsPath)
	resp, err := startWorkspaceDetached(opts, req)
	result := workspaceResult{
		Workspace:  opts.Name,
		StateDir:   opts.StateDir,
		RootfsPath: rootfsPath,
		KernelPath: opts.KernelPath,
		Disks:      opts.Disks,
		SerialPath: serialLogPath(opts),
		Response:   resp,
	}
	if encodeErr := writeWorkspaceResult(stdout, result); encodeErr != nil {
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
		return fmt.Errorf("create requires a name")
	}
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	if err := ensureWorkspaceKernel(ctx, &opts); err != nil {
		return err
	}
	disks, err := prepareWorkspaceDisks(ctx, opts)
	if err != nil {
		return err
	}
	opts.Disks = disks
	result, err := createWorkspaceRootfs(ctx, opts)
	if err != nil {
		return err
	}
	result.Disks = disks
	if err := writeWorkspaceManifest(opts); err != nil {
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
		resp, err := dispatchWorkspaceRequest(ctx, opts, req)
		if err != nil {
			if resp.Error == "" {
				return err
			}
		}
		result.Response = resp
	}
	if encodeErr := writeWorkspaceResult(stdout, result); encodeErr != nil {
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
		Backend:      hostBackend(),
		Architecture: defaultGuestArch(),
		MemoryMiB:    defaultWorkspaceMemoryMiB,
		CPUCount:     2,
		SizeMiB:      rootfs.DefaultSizeMiB,
		Timeout:      2 * time.Minute,
		ResultPort:   1024,
	}
	opts.StateDir = defaultStateDir()
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.Mke2fsPath = defaultMke2fsPath()
	opts.GuestInitPath = defaultGuestInitPath(opts.Architecture)
	opts.SupervisorPath = os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR")
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Name, "name", "", "Workspace name")
	fs.StringVar(&opts.Name, "id", "", "Workspace ID")
	fs.StringVar(&opts.ImageRef, "image", "", "OCI image reference")
	fs.StringVar(&opts.ExecCommand, "exec", "", "Shell command to run as guest init")
	fs.StringVar(&opts.Entrypoint, "entrypoint", "", "Shell command to run when the workspace starts")
	var setupCommands multiFlag
	fs.Var(&setupCommands, "setup", "Shell command to run before --exec")
	var envVars multiFlag
	fs.Var(&envVars, "env", "Guest environment variable KEY=VALUE")
	var diskFlags multiFlag
	fs.Var(&diskFlags, "disk", "Attach disk name=path:/mount:ro|rw")
	var bundleFlags multiFlag
	fs.Var(&bundleFlags, "bundle", "Build and attach bundle name=tar:/mount:ro|rw")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend override")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
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
		if command == "create" && fs.NArg() == 1 && opts.Name == "" {
			opts.Name = fs.Arg(0)
		} else {
			return workspaceOptions{}, fmt.Errorf("unexpected %s argument: %s", command, fs.Arg(0))
		}
	}
	opts.SetupCommands = append([]string{}, setupCommands...)
	env, err := parseEnvFlags(envVars)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Env = env
	disks, err := parseWorkspaceDisks(diskFlags, false)
	if err != nil {
		return workspaceOptions{}, err
	}
	bundles, err := parseWorkspaceDisks(bundleFlags, true)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Disks = append(disks, bundles...)
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		if command == "create" {
			opts.ImageRef = defaultWorkspaceImage(opts.Architecture)
		} else {
			return workspaceOptions{}, fmt.Errorf("%s requires --image", command)
		}
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
		Env:            opts.Env,
		Mounts:         workspaceMounts(opts.Disks),
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

func workspaceMounts(disks []workspaceDisk) []rootfs.Mount {
	if len(disks) == 0 {
		return nil
	}
	mounts := make([]rootfs.Mount, 0, len(disks))
	for idx, disk := range disks {
		mounts = append(mounts, rootfs.Mount{
			Device:     virtioBlockDevice(idx + 1),
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	return mounts
}

func virtioBlockDevice(index int) string {
	if index < 0 {
		index = 0
	}
	name := ""
	for {
		name = string(rune('a'+(index%26))) + name
		index = index/26 - 1
		if index < 0 {
			break
		}
	}
	return "/dev/vd" + name
}

func prepareWorkspaceDisks(ctx context.Context, opts workspaceOptions) ([]workspaceDisk, error) {
	if len(opts.Disks) == 0 {
		return nil, nil
	}
	disks := make([]workspaceDisk, 0, len(opts.Disks))
	seenNames := map[string]bool{}
	seenMountpoints := map[string]bool{}
	for _, disk := range opts.Disks {
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
		Name:  opts.Name,
		Disks: opts.Disks,
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
	var listeners []vmkit.VsockListener
	if opts.ResultPort != 0 {
		listeners = []vmkit.VsockListener{{Port: opts.ResultPort, Target: resultPath(opts)}}
	}
	listeners = append(listeners, opts.VsockListeners...)
	disks := make([]vmkit.Disk, 0, len(opts.Disks))
	for _, disk := range opts.Disks {
		disks = append(disks, vmkit.Disk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
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
			Disks:          disks,
			VsockListeners: listeners,
			SerialInput:    opts.SerialInput,
		},
	}
}

func workspaceOptionsFromRequest(req vmkit.Request, supervisorPath string) (workspaceOptions, error) {
	if req.Identity == nil {
		return workspaceOptions{}, fmt.Errorf("identity is required")
	}
	if req.Config == nil {
		return workspaceOptions{}, fmt.Errorf("config is required")
	}
	return workspaceOptions{
		Name:           req.Identity.RuntimeID,
		Backend:        req.Identity.Backend,
		KernelPath:     req.Config.KernelPath,
		StateDir:       req.Config.StateDir,
		SupervisorPath: supervisorPath,
		MemoryMiB:      req.Config.MemoryMiB,
		CPUCount:       req.Config.CPUCount,
		Disks:          configDisksToWorkspaceDisks(req.Config.Disks),
	}, nil
}

func configDisksToWorkspaceDisks(disks []vmkit.Disk) []workspaceDisk {
	if len(disks) == 0 {
		return nil
	}
	out := make([]workspaceDisk, 0, len(disks))
	for _, disk := range disks {
		out = append(out, workspaceDisk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	return out
}

func dispatchWorkspaceRequest(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	switch opts.Backend {
	case vmkit.BackendFirecracker:
		return dispatchFirecrackerRequest(ctx, opts, req)
	case vmkit.BackendAppleVF:
		return vmkit.SupervisorClient{Path: opts.SupervisorPath}.Do(ctx, req)
	default:
		return vmkit.Response{Backend: opts.Backend, Error: fmt.Sprintf("unsupported backend: %s", opts.Backend)}, fmt.Errorf("unsupported backend: %s", opts.Backend)
	}
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
		return startFirecrackerDetached(opts, req)
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

type firecrackerConfig struct {
	BootSource firecrackerBootSource    `json:"boot-source"`
	Drives     []firecrackerDrive       `json:"drives"`
	Machine    firecrackerMachineConfig `json:"machine-config"`
}

type firecrackerBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type firecrackerDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type firecrackerMachineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	SMT        bool `json:"smt"`
}

func runFirecrackerForeground(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	return startFirecrackerProcess(ctx, opts, req, false)
}

func startFirecrackerDetached(opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	return startFirecrackerProcess(context.Background(), opts, req, true)
}

func dispatchFirecrackerRequest(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	if err := vmkit.ValidateRequest(req); err != nil {
		return vmkit.Response{}, err
	}
	switch req.Command {
	case "check":
		return vmkit.Response{OK: true, Backend: vmkit.BackendFirecracker}, nil
	case "prepare":
		if err := prepareFirecrackerWorkspace(opts, req); err != nil {
			return failedFirecrackerResponse(req, err.Error()), err
		}
		return preparedFirecrackerResponse(req), nil
	case "run":
		return runFirecrackerForeground(ctx, opts, req)
	case "start":
		return startFirecrackerDetached(opts, req)
	case "inspect":
		return inspectFirecrackerWorkspace(opts)
	case "stop":
		return stopFirecrackerWorkspace(ctx, opts, req, syscall.SIGTERM)
	case "kill":
		return stopFirecrackerWorkspace(ctx, opts, req, syscall.SIGKILL)
	case "delete":
		if err := ensureFirecrackerWorkspaceCanDelete(opts); err != nil {
			return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
		}
		cleanupWorkspaceState(opts)
		return firecrackerEventResponse(req, vmkit.StateStopped, ""), nil
	default:
		err := fmt.Errorf("unknown firecracker command %q", req.Command)
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
}

func prepareFirecrackerWorkspace(opts workspaceOptions, req vmkit.Request) error {
	if err := vmkit.ValidateRequest(req); err != nil {
		return err
	}
	if opts.Name == "" && req.Identity != nil {
		opts.Name = req.Identity.RuntimeID
	}
	if opts.StateDir == "" && req.Config != nil {
		opts.StateDir = req.Config.StateDir
	}
	if err := writeFirecrackerConfig(opts, req); err != nil {
		_ = writeWorkspaceProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return err
	}
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o755); err != nil {
		return err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := serialLog.Close(); err != nil {
		return err
	}
	return writeWorkspaceProcessState(opts, req, vmkit.StatePrepared, 0, "")
}

func stopFirecrackerWorkspace(ctx context.Context, opts workspaceOptions, req vmkit.Request, signal syscall.Signal) (vmkit.Response, error) {
	state, err := readWorkspaceRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.PID == 0 {
		if err := writeWorkspaceProcessState(opts, runtimeStateRequest(req, state), vmkit.StateStopped, 0, ""); err != nil {
			return vmkit.Response{}, err
		}
		return stoppedFirecrackerResponse(req), nil
	}
	active, err := processActive(state.PID)
	if err != nil {
		return vmkit.Response{}, err
	}
	if active {
		if err := signalProcessGroup(state.PID, signal); err != nil && err != syscall.ESRCH {
			errorText := err.Error()
			_ = writeWorkspaceProcessState(opts, runtimeStateRequest(req, state), vmkit.StateFailed, state.PID, errorText)
			return failedFirecrackerResponse(req, errorText), err
		}
		if err := waitForProcessExit(ctx, state.PID, 5*time.Second); err != nil {
			errorText := err.Error()
			_ = writeWorkspaceProcessState(opts, runtimeStateRequest(req, state), vmkit.StateFailed, state.PID, errorText)
			return failedFirecrackerResponse(req, errorText), err
		}
	}
	if err := writeWorkspaceProcessState(opts, runtimeStateRequest(req, state), vmkit.StateStopped, 0, ""); err != nil {
		return vmkit.Response{}, err
	}
	return stoppedFirecrackerResponse(req), nil
}

func ensureFirecrackerWorkspaceCanDelete(opts workspaceOptions) error {
	state, err := readWorkspaceRuntimeState(opts)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if state.PID == 0 {
		return nil
	}
	active, err := processActive(state.PID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("firecracker workspace %s is running; stop or kill it before delete", opts.Name)
	}
	return nil
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, signal); err == nil || err != syscall.ESRCH {
		return err
	}
	return syscall.Kill(pid, signal)
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

func runtimeStateRequest(req vmkit.Request, state workspaceRuntimeState) vmkit.Request {
	if req.Identity == nil {
		identity := state.Event.Identity
		req.Identity = &identity
	}
	if req.Config == nil {
		config := state.Config
		req.Config = &config
	} else {
		req.Config.KernelPath = state.Config.KernelPath
		req.Config.RootfsPath = state.Config.RootfsPath
		req.Config.MemoryMiB = state.Config.MemoryMiB
		req.Config.CPUCount = state.Config.CPUCount
		req.Config.Disks = state.Config.Disks
	}
	return req
}

func processActive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			return false, nil
		}
		return false, err
	}
	if state, err := linuxProcessState(pid); err == nil && state == "Z" {
		return false, nil
	}
	return true, nil
}

func waitForProcessExit(ctx context.Context, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		active, err := processActive(pid)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process %d did not exit before timeout", pid)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func linuxProcessState(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return "", fmt.Errorf("invalid proc stat for pid %d", pid)
	}
	return fields[2], nil
}

func startFirecrackerProcess(ctx context.Context, opts workspaceOptions, req vmkit.Request, detached bool) (vmkit.Response, error) {
	if err := vmkit.ValidateRequest(req); err != nil {
		return vmkit.Response{}, err
	}
	firecrackerPath, err := resolveFirecrackerPath()
	if err != nil {
		_ = writeWorkspaceProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedFirecrackerResponse(req, err.Error()), err
	}
	if err := writeFirecrackerConfig(opts, req); err != nil {
		_ = writeWorkspaceProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedFirecrackerResponse(req, err.Error()), err
	}
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o755); err != nil {
		return vmkit.Response{}, err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return vmkit.Response{}, err
	}
	if req.Config.SerialInput {
		_ = serialLog.Close()
		return vmkit.Response{}, fmt.Errorf("firecracker serial input is not supported")
	}
	cmd := exec.CommandContext(ctx, firecrackerPath, "--no-api", "--config-file", firecrackerConfigPath(opts))
	cmd.Stdout = serialLog
	cmd.Stderr = serialLog
	if detached {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		_ = serialLog.Close()
		_ = writeWorkspaceProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedFirecrackerResponse(req, err.Error()), err
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateRunning, cmd.Process.Pid, ""); err != nil {
		_ = cmd.Process.Kill()
		_ = serialLog.Close()
		return vmkit.Response{}, err
	}
	if detached {
		_ = serialLog.Close()
		_ = cmd.Process.Release()
		return runningFirecrackerResponse(req), nil
	}
	waitErr := cmd.Wait()
	closeErr := serialLog.Close()
	state := vmkit.StateStopped
	errorText := ""
	if waitErr != nil {
		state = vmkit.StateFailed
		errorText = waitErr.Error()
	}
	if closeErr != nil && errorText == "" {
		state = vmkit.StateFailed
		errorText = closeErr.Error()
	}
	if err := writeWorkspaceProcessState(opts, req, state, 0, errorText); err != nil && waitErr == nil && closeErr == nil {
		return vmkit.Response{}, err
	}
	if errorText != "" {
		return failedFirecrackerResponse(req, errorText), fmt.Errorf("%s", errorText)
	}
	return stoppedFirecrackerResponse(req), nil
}

func writeFirecrackerConfig(opts workspaceOptions, req vmkit.Request) error {
	cfg := firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: req.Config.KernelPath,
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/sbin/microagent-init",
		},
		Drives: []firecrackerDrive{
			{
				DriveID:      "rootfs",
				PathOnHost:   req.Config.RootfsPath,
				IsRootDevice: true,
				IsReadOnly:   false,
			},
		},
		Machine: firecrackerMachineConfig{
			VCPUCount:  req.Config.CPUCount,
			MemSizeMiB: req.Config.MemoryMiB,
			SMT:        false,
		},
	}
	for _, disk := range req.Config.Disks {
		cfg.Drives = append(cfg.Drives, firecrackerDrive{
			DriveID:      disk.Name,
			PathOnHost:   disk.Path,
			IsRootDevice: false,
			IsReadOnly:   disk.Mode == "ro",
		})
	}
	if err := os.MkdirAll(filepath.Dir(firecrackerConfigPath(opts)), 0o755); err != nil {
		return err
	}
	return writeJSONFile(firecrackerConfigPath(opts), cfg)
}

func inspectFirecrackerWorkspace(opts workspaceOptions) (vmkit.Response, error) {
	return inspectWorkspaceState(opts)
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
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func runningFirecrackerResponse(req vmkit.Request) vmkit.Response {
	return firecrackerEventResponse(req, vmkit.StateRunning, "")
}

func preparedFirecrackerResponse(req vmkit.Request) vmkit.Response {
	return firecrackerEventResponse(req, vmkit.StatePrepared, "")
}

func stoppedFirecrackerResponse(req vmkit.Request) vmkit.Response {
	return firecrackerEventResponse(req, vmkit.StateStopped, "")
}

func failedFirecrackerResponse(req vmkit.Request, errorText string) vmkit.Response {
	return firecrackerEventResponse(req, vmkit.StateFailed, errorText)
}

func firecrackerEventResponse(req vmkit.Request, state vmkit.VMState, errorText string) vmkit.Response {
	event := &vmkit.Event{State: state, ObservedAt: time.Now().UTC()}
	if req.Identity != nil {
		event.Identity = *req.Identity
	}
	if req.Config != nil && req.Identity != nil {
		event.Detail = "serial=" + filepath.Join(req.Config.StateDir, req.Identity.RuntimeID, "serial.log")
	}
	resp := vmkit.Response{OK: state != vmkit.StateFailed, Backend: vmkit.BackendFirecracker, Event: event}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func firecrackerConfigPath(opts workspaceOptions) string {
	return filepath.Join(opts.StateDir, opts.Name, "firecracker.json")
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

func writeJSON(stdout *os.File, value any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func outputJSON(stdout *os.File) bool {
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

func parseGlobalFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
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
		if resp.Event.Detail != "" {
			fmt.Fprintf(stdout, "Detail: %s\n", resp.Event.Detail)
		}
	}
	if resp.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
	}
	return nil
}

func writeWorkspaceResult(stdout *os.File, result workspaceResult) error {
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
	if result.KernelPath != "" {
		fmt.Fprintf(stdout, "Kernel: %s\n", result.KernelPath)
	}
	if result.SerialPath != "" {
		fmt.Fprintf(stdout, "Console log: %s\n", result.SerialPath)
	}
	if result.Result != nil {
		fmt.Fprintf(stdout, "Exit code: %d\n", result.Result.ExitCode)
		if strings.TrimSpace(result.Result.Stdout) != "" {
			fmt.Fprintf(stdout, "\n%s", result.Result.Stdout)
			if !strings.HasSuffix(result.Result.Stdout, "\n") {
				fmt.Fprintln(stdout)
			}
		}
		if strings.TrimSpace(result.Result.Stderr) != "" {
			fmt.Fprintf(stdout, "\nStderr:\n%s", result.Result.Stderr)
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

func writeWorkspaceList(stdout *os.File, entries []workspaceListEntry) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspaces": entries})
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No workspaces.")
		return nil
	}
	fmt.Fprintf(stdout, "%-24s %-12s %s\n", "NAME", "STATE", "BACKEND")
	for _, entry := range entries {
		fmt.Fprintf(stdout, "%-24s %-12s %s\n", entry.Name, entry.State, entry.Backend)
	}
	return nil
}

func humanOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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
	if hasLowLevelCreateFlag(args) {
		return false
	}
	if hasFlagValue(args, "image") || hasPositionalWorkspaceName(args) {
		return true
	}
	return hasFlagValue(args, "name") || hasFlagValue(args, "id") || hasFlagValue(args, "setup") || hasFlagValue(args, "entrypoint") || hasFlagValue(args, "env") || hasFlagValue(args, "disk") || hasFlagValue(args, "bundle")
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
	if runtime.GOOS == "darwin" {
		return vmkit.BackendAppleVF
	}
	return vmkit.BackendFirecracker
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

func defaultWorkspaceImage(arch string) string {
	switch strings.TrimSpace(arch) {
	case "arm64", "aarch64":
		return defaultWorkspaceImageArm64
	case "amd64", "x86_64":
		return defaultWorkspaceImageAMD64
	default:
		return defaultWorkspaceImageOther
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
		if arg == "--json" || arg == "-json" || arg == "--rootfs" || arg == "-rootfs" || arg == "--kernel" || arg == "-kernel" || arg == "--name" || arg == "-name" || arg == "--id" || arg == "-id" || arg == "--entrypoint" || arg == "-entrypoint" || arg == "--env" || arg == "-env" {
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
		lines = append(lines, resetGuestConfigCommand(shellCommand(opts.Entrypoint), opts.Env, 0, workspaceMounts(opts.Disks)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "set -eu\n" + strings.Join(lines, "\n")
}

func resetGuestConfigCommand(command []string, env map[string]string, port uint32, mounts []rootfs.Mount) string {
	if command == nil {
		command = []string{}
	}
	data, err := json.Marshal(struct {
		Command []string       `json:"command"`
		Env     []string       `json:"env,omitempty"`
		Port    uint32         `json:"port"`
		Mounts  []rootfs.Mount `json:"mounts,omitempty"`
	}{
		Command: command,
		Env:     envList(env),
		Port:    port,
		Mounts:  mounts,
	})
	if err != nil {
		panic(err)
	}
	return "printf '%s\\n' " + shellSingleQuote(string(data)) + " > /etc/microagent/run.json"
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

func requestFromFlagsOrJSON(jsonPath string, args []string, identity vmkit.Identity, config vmkit.Config, disks []string, vsocks []string) (vmkit.Request, error) {
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
	for _, disk := range parsedDisks {
		config.Disks = append(config.Disks, vmkit.Disk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	config.VsockListeners = listeners
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
		"-supervisor":  true,
		"-json":        true,
		"-id":          true,
		"-name":        true,
		"-image":       true,
		"-exec":        true,
		"-entrypoint":  true,
		"-env":         true,
		"-setup":       true,
		"-request-id":  true,
		"-role":        true,
		"-backend":     true,
		"-kernel":      true,
		"-rootfs":      true,
		"-disk":        true,
		"-bundle":      true,
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
  --json                Print JSON output
  --text                Print human-readable output
  --output <json|text>  Select output format
  -supervisor <path>    Override the supervisor path
  -json <path|- >       Read request JSON from a file or stdin
  -image <ref>          OCI image
  -name <name>          Workspace name
  -id <id>              Workspace ID
  -entrypoint <command> Command to run on start
  -env KEY=VALUE        Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -kernel <path>        Custom kernel path
  -rootfs <path>        Rootfs image path
  -state-dir <dir>      State directory
  -memory <MiB>         Memory in MiB; defaults to 512 for workspaces
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
  -entrypoint <command> Command to run on start
  -env KEY=VALUE        Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -name <name>          Workspace name; generated when omitted
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -memory <MiB>         Memory in MiB; defaults to 512
  -cpus <n>             CPU count
  -size-mib <MiB>       Disk size
  -timeout <seconds>    Timeout
  -keep                 Keep state
  -mke2fs <path>        mke2fs binary path
  -supervisor <path>    Override the supervisor path
`)
}

func printCreateHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent create

Create a workspace from an image.

Options:
  -image <ref>          OCI image; defaults to a small BusyBox image
  -name <name>          Workspace name
  -setup <command>      Shell command to run before first start
  -entrypoint <command> Command to run on start
  -env KEY=VALUE        Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -memory <MiB>         Memory in MiB; defaults to 512
  -cpus <n>             CPU count
  -size-mib <MiB>       Disk size
  -mke2fs <path>        mke2fs binary path
  -supervisor <path>    Override the supervisor path
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
