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
	"gopkg.in/yaml.v3"
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
	defaultWorkspaceCPUCount   = 2
	defaultWorkspaceProfile    = "small"
	defaultRestartPolicy       = "never"
	defaultNetworkMode         = "nat"
	consoleDetachByte          = byte(0x1d) // Ctrl-]
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
	if args[0] == "host" {
		return runHost(ctx, args[1:], stdout)
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
	if args[0] == "perf" {
		return runPerf(ctx, args[1:], stdout)
	}
	if args[0] == "run" {
		return runWorkspace(ctx, args[1:], stdout)
	}
	if args[0] == "create" && wantsHelp(args[1:]) {
		printCreateHelp(stdout)
		return nil
	}
	if args[0] == "clone" {
		return runClone(args[1:], stdout)
	}
	if args[0] == "cp" {
		return runCP(args[1:], stdout)
	}
	if args[0] == "ps" {
		return runPS(args[1:], stdout)
	}
	if args[0] == "logs" || args[0] == "log" {
		return runLogs(args[1:], stdout)
	}
	if args[0] == "network" {
		return runNetwork(args[1:], stdout)
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
	if args[0] == "supervise" {
		return runSupervise(ctx, args[1:], stdout)
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

func runHost(ctx context.Context, args []string, stdout *os.File) error {
	opts := doctorOptions{
		Backend:        hostBackend(),
		Arch:           defaultGuestArch(),
		SupervisorPath: os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR"),
	}
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend override")
	fs.StringVar(&opts.Arch, "arch", opts.Arch, "Guest architecture")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected host argument: %s", fs.Arg(0))
	}
	resp, _ := doctorResponse(ctx, opts)
	return writeDoctorResponse(stdout, resp)
}

func doctorResponse(ctx context.Context, opts doctorOptions) (vmkit.Response, error) {
	switch opts.Backend {
	case vmkit.BackendAppleVF:
		resp, err := vmkit.ExecutableSupervisor{Path: opts.SupervisorPath}.Do(ctx, vmkit.Request{Command: "host"})
		if resp.Backend == "" {
			resp.Backend = opts.Backend
		}
		resp.Kernel = defaultKernelSupport(opts.Backend, opts.Arch)
		if err != nil && resp.Error == "" {
			resp.Error = err.Error()
		}
		augmentHostSupport(&resp, opts)
		return resp, err
	case vmkit.BackendFirecracker:
		resp, err := firecrackerDoctorResponse(opts.Backend, opts.Arch, resolveFirecrackerPath, os.Stat, firecrackerVersion)
		augmentHostSupport(&resp, opts)
		return resp, err
	default:
		resp := vmkit.Response{
			OK:      false,
			Backend: opts.Backend,
			Kernel:  defaultKernelSupport(opts.Backend, opts.Arch),
			Error:   fmt.Sprintf("unsupported backend: %s", opts.Backend),
		}
		augmentHostSupport(&resp, opts)
		return resp, fmt.Errorf("%s", resp.Error)
	}
}

func augmentHostSupport(resp *vmkit.Response, opts doctorOptions) {
	if resp.Host == nil {
		resp.Host = &vmkit.HostSupport{
			Backend:      opts.Backend,
			Architecture: opts.Arch,
		}
	}
	if resp.Host.Backend == "" {
		resp.Host.Backend = opts.Backend
	}
	if resp.Host.Architecture == "" {
		resp.Host.Architecture = opts.Arch
	}
	switch opts.Backend {
	case vmkit.BackendAppleVF:
		resp.Host.SupervisorPath = nonEmpty(opts.SupervisorPath, "microagent-applevf-supervisor")
		resp.Host.SupervisorAvailable = resp.Error == ""
		resp.Host.ConsoleAvailable = true
		resp.Host.ConsoleMode = "interactive"
	case vmkit.BackendFirecracker:
		resp.Host.SupervisorPath = firecrackerSupervisorPath(workspaceOptions{SupervisorPath: opts.SupervisorPath})
		resp.Host.SupervisorAvailable = true
		resp.Host.ConsoleAvailable = false
		resp.Host.ConsoleMode = "serial-log"
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
	host.ConsoleAvailable = false
	host.ConsoleMode = "serial-log"
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
	opts.OutputPath = defaultWritableKernelPath(opts.Backend, opts.Architecture)
	outputExplicit := hasFlagValue(args, "out")
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
	if !outputExplicit || opts.OutputPath == "" {
		opts.OutputPath = defaultWritableKernelPath(opts.Backend, opts.Architecture)
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
	var publishes multiFlag
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
	networkMode := fs.String("network", defaultNetworkMode, "Network mode: nat, isolated, or bridged")
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
	Profile         string
	RestartPolicy   string
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
	Network         vmkit.NetworkConfig
	Timeout         time.Duration
	ResultPort      uint32
	Disks           []workspaceDisk
	VsockListeners  []vmkit.VsockListener
	KernelExplicit  bool
	SpecMemory      bool
	SpecCPU         bool
	SpecSize        bool
	Keep            bool
	PrepareForStart bool
	SerialInput     bool
}

type workspaceSpec struct {
	Name       string            `yaml:"name"`
	ImageRef   string            `yaml:"image"`
	Profile    string            `yaml:"profile"`
	Restart    string            `yaml:"restart"`
	Entrypoint string            `yaml:"entrypoint"`
	Setup      []string          `yaml:"setup"`
	Env        map[string]string `yaml:"env"`
	Resources  resourceConfig    `yaml:"resources"`
	Network    networkSpec       `yaml:"network"`
	Disks      []workspaceDisk   `yaml:"disks"`
	Bundles    []workspaceDisk   `yaml:"bundles"`
}

type networkSpec struct {
	Mode         string              `json:"mode,omitempty" yaml:"mode,omitempty"`
	PortForwards []vmkit.PortForward `json:"port_forwards,omitempty" yaml:"forwards,omitempty"`
	DNS          []string            `json:"dns,omitempty" yaml:"dns,omitempty"`
	Routes       []string            `json:"routes,omitempty" yaml:"routes,omitempty"`
	IP           string              `json:"ip,omitempty" yaml:"ip,omitempty"`
}

type workspaceDisk struct {
	Name       string `json:"name" yaml:"name"`
	SourcePath string `json:"source_path,omitempty" yaml:"sourcePath,omitempty"`
	Path       string `json:"path" yaml:"path"`
	Mountpoint string `json:"mountpoint" yaml:"mountpoint"`
	Mode       string `json:"mode" yaml:"mode"`
	Bundle     bool   `json:"bundle,omitempty" yaml:"bundle,omitempty"`
}

type workspaceManifest struct {
	Name      string          `json:"name"`
	Profile   string          `json:"profile,omitempty"`
	Restart   string          `json:"restart"`
	Resources resourceConfig  `json:"resources"`
	Network   networkSpec     `json:"network,omitempty"`
	Disks     []workspaceDisk `json:"disks,omitempty"`
}

type workspaceResult struct {
	Workspace  string            `json:"workspace"`
	StateDir   string            `json:"state_dir"`
	Profile    string            `json:"profile,omitempty"`
	Restart    string            `json:"restart"`
	Resources  resourceConfig    `json:"resources"`
	Network    networkSpec       `json:"network,omitempty"`
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

type copyResult struct {
	Workspace string `json:"workspace"`
	Disk      string `json:"disk"`
	Direction string `json:"direction"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	ImagePath string `json:"image_path"`
	Bytes     int64  `json:"bytes,omitempty"`
}

type workspaceNetworkResult struct {
	Workspace string               `json:"workspace"`
	State     string               `json:"state,omitempty"`
	Backend   string               `json:"backend,omitempty"`
	Network   vmkit.NetworkConfig  `json:"network"`
	Runtime   *vmkit.NetworkConfig `json:"runtime,omitempty"`
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
	Profile    string `json:"profile,omitempty"`
	Restart    string `json:"restart,omitempty"`
	Network    string `json:"network,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
	RootfsPath string `json:"rootfs_path,omitempty"`
	SerialPath string `json:"serial_path,omitempty"`
}

type resourceConfig struct {
	MemoryMiB int   `json:"memory_mib" yaml:"memoryMiB"`
	CPUCount  int   `json:"cpu_count" yaml:"cpuCount"`
	SizeMiB   int64 `json:"size_mib,omitempty" yaml:"sizeMiB,omitempty"`
}

type resourceProfile struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Resources   resourceConfig `json:"resources"`
}

type imageIndex struct {
	Images []imageRecord `json:"images"`
}

type imageRecord struct {
	ImageRef    string          `json:"image_ref"`
	ResolvedRef string          `json:"resolved_ref,omitempty"`
	Digest      string          `json:"digest,omitempty"`
	Platform    rootfs.Platform `json:"platform"`
	OutputPath  string          `json:"output_path,omitempty"`
	SizeBytes   int64           `json:"size_bytes,omitempty"`
	LastUsedAt  string          `json:"last_used_at"`
}

type imagePruneResult struct {
	Removed []imageRecord `json:"removed"`
	Kept    []imageRecord `json:"kept"`
}

type imagePullOptions struct {
	StateDir      string
	ImageRef      string
	Architecture  string
	SizeMiB       int64
	Mke2fsPath    string
	GuestInitPath string
}

type perfBootOptions struct {
	StateDir       string
	ImageRef       string
	Profile        string
	ExecCommand    string
	Iterations     int
	TimeoutSeconds int
	Mke2fsPath     string
	SupervisorPath string
}

type perfReport struct {
	Benchmark  string             `json:"benchmark"`
	Backend    string             `json:"backend"`
	Arch       string             `json:"arch"`
	ImageRef   string             `json:"image_ref"`
	Profile    string             `json:"profile"`
	Iterations []perfIteration    `json:"iterations"`
	Summary    perfSummary        `json:"summary"`
	Host       *vmkit.HostSupport `json:"host,omitempty"`
}

type perfIteration struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type perfSummary struct {
	Count int   `json:"count"`
	MinMs int64 `json:"min_ms"`
	AvgMs int64 `json:"avg_ms"`
	MaxMs int64 `json:"max_ms"`
}

var resourceProfiles = []resourceProfile{
	{
		Name:        "tiny",
		Description: "smoke tests and very small shells",
		Resources:   resourceConfig{MemoryMiB: 256, CPUCount: 1, SizeMiB: 512},
	},
	{
		Name:        "small",
		Description: "default lightweight workspace",
		Resources:   resourceConfig{MemoryMiB: defaultWorkspaceMemoryMiB, CPUCount: defaultWorkspaceCPUCount, SizeMiB: rootfs.DefaultSizeMiB},
	},
	{
		Name:        "medium",
		Description: "package installs and normal agent work",
		Resources:   resourceConfig{MemoryMiB: 2048, CPUCount: 2, SizeMiB: 8192},
	},
	{
		Name:        "large",
		Description: "heavier builds and larger workspaces",
		Resources:   resourceConfig{MemoryMiB: 4096, CPUCount: 4, SizeMiB: 16384},
	},
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
	result, err := cloneWorkspace(opts.StateDir, source, target)
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
	result, err := copyWorkspaceFile(opts.StateDir, debugfsPath, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
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
		images, err := listImageRecords(opts.StateDir)
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
		record, err := pullImage(context.Background(), imagePullOptions{
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
		record, err := tagImageRecord(opts.StateDir, fs.Arg(1), fs.Arg(2))
		if err != nil {
			return err
		}
		return writeImageRecord(stdout, record)
	case "prune":
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: microagent images prune [--state-dir <dir>]")
		}
		result, err := pruneImageRecords(opts.StateDir)
		if err != nil {
			return err
		}
		return writeImagePruneResult(stdout, result)
	default:
		return fmt.Errorf("unknown images command: %s", fs.Arg(0))
	}
}

func runPerf(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printPerfHelp(stdout)
		return nil
	}
	switch args[0] {
	case "boot":
		return runPerfBoot(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown perf command: %s", args[0])
	}
}

func runPerfBoot(ctx context.Context, args []string, stdout *os.File) error {
	opts := perfBootOptions{
		StateDir:       defaultStateDir(),
		ImageRef:       defaultWorkspaceImage(defaultGuestArch()),
		Profile:        defaultWorkspaceProfile,
		ExecCommand:    "true",
		Iterations:     1,
		TimeoutSeconds: 120,
		Mke2fsPath:     defaultMke2fsPath(),
		SupervisorPath: os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR"),
	}
	fs := flag.NewFlagSet("perf boot", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "OCI image reference")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Guest command used to mark boot completion")
	fs.IntVar(&opts.Iterations, "iterations", opts.Iterations, "Number of boot measurements")
	fs.IntVar(&opts.TimeoutSeconds, "timeout", opts.TimeoutSeconds, "Per-iteration timeout in seconds")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "Supervisor path")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected perf boot argument: %s", fs.Arg(0))
	}
	if opts.Iterations <= 0 {
		return fmt.Errorf("perf boot iterations must be positive")
	}
	if opts.TimeoutSeconds <= 0 {
		return fmt.Errorf("perf boot timeout must be positive")
	}
	if strings.TrimSpace(opts.ImageRef) == "" {
		return fmt.Errorf("perf boot requires --image")
	}
	if strings.TrimSpace(opts.ExecCommand) == "" {
		return fmt.Errorf("perf boot requires --exec")
	}
	hostResp, _ := doctorResponse(ctx, doctorOptions{Backend: hostBackend(), Arch: defaultGuestArch(), SupervisorPath: opts.SupervisorPath})
	report := perfReport{
		Benchmark: "boot",
		Backend:   hostBackend(),
		Arch:      defaultGuestArch(),
		ImageRef:  strings.TrimSpace(opts.ImageRef),
		Profile:   strings.TrimSpace(opts.Profile),
		Host:      hostResp.Host,
	}
	for i := 0; i < opts.Iterations; i++ {
		name := fmt.Sprintf("perf-boot-%d-%d", time.Now().UnixNano(), i+1)
		start := time.Now()
		err := runWorkspaceToDiscardedOutput(ctx, perfBootWorkspaceArgs(opts, name))
		duration := time.Since(start)
		result := perfIteration{Name: name, OK: err == nil, DurationMs: duration.Milliseconds()}
		if err != nil {
			result.Error = err.Error()
		}
		report.Iterations = append(report.Iterations, result)
	}
	report.Summary = summarizePerfIterations(report.Iterations)
	return writePerfReport(stdout, report)
}

func perfBootWorkspaceArgs(opts perfBootOptions, name string) []string {
	args := []string{
		"--name", name,
		"--image", strings.TrimSpace(opts.ImageRef),
		"--exec", opts.ExecCommand,
		"--state-dir", opts.StateDir,
		"--profile", strings.TrimSpace(opts.Profile),
		"--timeout", strconv.Itoa(opts.TimeoutSeconds),
		"--mke2fs", opts.Mke2fsPath,
	}
	if strings.TrimSpace(opts.SupervisorPath) != "" {
		args = append(args, "--supervisor", opts.SupervisorPath)
	}
	return args
}

func runWorkspaceToDiscardedOutput(ctx context.Context, args []string) error {
	file, err := os.CreateTemp("", "microagent-perf-*.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	defer file.Close()
	return runWorkspace(ctx, args, file)
}

func summarizePerfIterations(iterations []perfIteration) perfSummary {
	summary := perfSummary{Count: len(iterations)}
	if len(iterations) == 0 {
		return summary
	}
	var total int64
	for i, iteration := range iterations {
		if i == 0 || iteration.DurationMs < summary.MinMs {
			summary.MinMs = iteration.DurationMs
		}
		if iteration.DurationMs > summary.MaxMs {
			summary.MaxMs = iteration.DurationMs
		}
		total += iteration.DurationMs
	}
	summary.AvgMs = total / int64(len(iterations))
	return summary
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
	result, err := inspectWorkspaceNetwork(opts.StateDir, name)
	if err != nil {
		return err
	}
	return writeNetworkResult(stdout, result)
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
	if state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: opts.StateDir, Name: name}); err == nil && state.Event.Identity.Backend == vmkit.BackendFirecracker {
		return fmt.Errorf("firecracker connect is not supported; use microagent logs")
	}
	if *readyTimeoutSeconds < 0 {
		return fmt.Errorf("connect ready-timeout must not be negative")
	}
	inputPath := serialInputPath(opts.StateDir, name)
	logPath := filepath.Join(opts.StateDir, name, "serial.log")
	if strings.TrimSpace(*send) != "" {
		if *timeoutSeconds < 0 {
			return fmt.Errorf("connect timeout must not be negative")
		}
		if err := waitForPath(ctx, inputPath, time.Duration(*timeoutSeconds)*time.Second); err != nil {
			return fmt.Errorf("console input is not ready for workspace %s: %w", name, err)
		}
		if *readyTimeoutSeconds > 0 {
			if err := waitForConsoleReady(ctx, logPath, time.Duration(*readyTimeoutSeconds)*time.Second); err != nil {
				return fmt.Errorf("guest shell is not ready for workspace %s: %w; check microagent logs %s", name, err, name)
			}
		}
		before := fileSize(logPath)
		input, err := openFIFOForWrite(ctx, inputPath, time.Duration(*timeoutSeconds)*time.Second)
		if err != nil {
			return fmt.Errorf("open console input for workspace %s: %w", name, err)
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
		return fmt.Errorf("console input is not ready for workspace %s: %w", name, err)
	}
	if *readyTimeoutSeconds > 0 {
		if err := waitForConsoleReady(ctx, logPath, time.Duration(*readyTimeoutSeconds)*time.Second); err != nil {
			return fmt.Errorf("guest shell is not ready for workspace %s: %w; check microagent logs %s", name, err, name)
		}
	}
	input, err := openFIFOForWrite(ctx, inputPath, 0)
	if err != nil {
		return fmt.Errorf("open console input for workspace %s: %w", name, err)
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
	profileExplicit := hasFlagValue(args, "profile")
	memoryExplicit := hasFlagValue(args, "memory")
	cpusExplicit := hasFlagValue(args, "cpus")
	opts := workspaceOptions{
		Backend:        hostBackend(),
		Architecture:   defaultGuestArch(),
		Profile:        defaultWorkspaceProfile,
		Network:        vmkit.NetworkConfig{Mode: defaultNetworkMode},
		StateDir:       defaultStateDir(),
		SupervisorPath: os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR"),
		SerialInput:    false,
	}
	if err := applyResourceProfile(&opts, false, false, false); err != nil {
		return err
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
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
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
	requestedProfile := opts.Profile
	manifest, err := readWorkspaceManifest(opts.StateDir, opts.Name)
	manifestLoaded := false
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		manifestLoaded = true
		opts.Disks = manifest.Disks
		if manifest.Profile != "" {
			opts.Profile = manifest.Profile
		}
		if manifest.Restart != "" {
			opts.RestartPolicy = normalizeRestartPolicy(manifest.Restart)
		}
		if manifest.Network.Mode != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" {
			opts.Network = networkConfigFromSpec(manifest.Network)
		}
		if manifest.Resources.MemoryMiB != 0 && !memoryExplicit {
			opts.MemoryMiB = manifest.Resources.MemoryMiB
		}
		if manifest.Resources.CPUCount != 0 && !cpusExplicit {
			opts.CPUCount = manifest.Resources.CPUCount
		}
		if manifest.Resources.SizeMiB != 0 {
			opts.SizeMiB = manifest.Resources.SizeMiB
		}
	}
	if profileExplicit || !manifestLoaded {
		if profileExplicit {
			opts.Profile = requestedProfile
		}
		if err := applyResourceProfile(&opts, memoryExplicit, cpusExplicit, true); err != nil {
			return err
		}
	}
	if err := validateResourceConfig(resourceConfig{MemoryMiB: opts.MemoryMiB, CPUCount: opts.CPUCount}, false); err != nil {
		return err
	}
	req := workspaceRequest(opts, "run", rootfsPath)
	resp, err := startWorkspaceDetached(opts, req)
	result := workspaceResult{
		Workspace:  opts.Name,
		StateDir:   opts.StateDir,
		Profile:    opts.Profile,
		Restart:    opts.RestartPolicy,
		Resources:  workspaceResources(opts),
		Network:    networkSpecFromConfig(opts.Network),
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

type superviseOptions struct {
	StateDir       string
	SupervisorPath string
	Backend        string
	Architecture   string
	KernelPath     string
	KernelExplicit bool
	Name           string
	Interval       time.Duration
	MaxRestarts    int
}

type superviseResult struct {
	Workspace  string `json:"workspace"`
	Policy     string `json:"policy"`
	Restarts   int    `json:"restarts"`
	FinalState string `json:"final_state,omitempty"`
	Stopped    bool   `json:"stopped"`
}

func runSupervise(ctx context.Context, args []string, stdout *os.File) error {
	opts := superviseOptions{
		StateDir:       defaultStateDir(),
		SupervisorPath: os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR"),
		Backend:        hostBackend(),
		Architecture:   defaultGuestArch(),
		Interval:       time.Second,
	}
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.KernelExplicit = hasFlagValue(args, "kernel")
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend override")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	intervalSeconds := fs.Int("interval", int(opts.Interval.Seconds()), "Seconds between state checks")
	fs.IntVar(&opts.MaxRestarts, "max-restarts", 0, "Maximum restarts; 0 means unlimited")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
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
	result, err := superviseWorkspace(ctx, opts)
	if result.Workspace != "" {
		if encodeErr := writeSuperviseResult(stdout, result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

func superviseWorkspace(ctx context.Context, opts superviseOptions) (superviseResult, error) {
	workspaceOpts, err := superviseWorkspaceOptions(ctx, opts)
	if err != nil {
		return superviseResult{}, err
	}
	policy := normalizeRestartPolicy(workspaceOpts.RestartPolicy)
	if policy == "never" {
		return superviseResult{Workspace: opts.Name, Policy: policy, Stopped: true}, nil
	}
	result := superviseResult{Workspace: opts.Name, Policy: policy}
	for {
		req := workspaceRequest(workspaceOpts, "run", filepath.Join(workspaceOpts.StateDir, "workspaces", workspaceOpts.Name, "rootfs.ext4"))
		resp, err := startWorkspaceDetached(workspaceOpts, req)
		if err != nil {
			result.FinalState = string(vmkit.StateFailed)
			if !shouldRestartWorkspace(policy, vmkit.StateFailed) {
				result.Stopped = true
				return result, err
			}
			result.Restarts++
			if opts.MaxRestarts > 0 && result.Restarts >= opts.MaxRestarts {
				result.Stopped = true
				return result, nil
			}
			continue
		} else if resp.Event != nil {
			result.FinalState = string(resp.Event.State)
		}
		state, waitErr := waitForSupervisedWorkspace(ctx, workspaceOpts, opts.Interval)
		result.FinalState = string(state)
		if waitErr != nil {
			result.Stopped = true
			return result, waitErr
		}
		if !shouldRestartWorkspace(policy, state) {
			result.Stopped = true
			return result, nil
		}
		result.Restarts++
		if opts.MaxRestarts > 0 && result.Restarts >= opts.MaxRestarts {
			result.Stopped = true
			return result, nil
		}
	}
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
		SerialInput:    opts.Backend == vmkit.BackendAppleVF,
	}
	manifest, err := readWorkspaceManifest(opts.StateDir, opts.Name)
	if err != nil {
		return workspaceOptions{}, err
	}
	if manifest.Profile != "" {
		workspaceOpts.Profile = manifest.Profile
	}
	workspaceOpts.RestartPolicy = normalizeRestartPolicy(manifest.Restart)
	if manifest.Network.Mode != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" {
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
			case vmkit.StateStopped, vmkit.StateFailed:
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
	switch normalizeRestartPolicy(policy) {
	case "always":
		return state == vmkit.StateStopped || state == vmkit.StateFailed
	case "on-failure":
		return state == vmkit.StateFailed
	default:
		return false
	}
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
	memoryExplicit := hasFlagValue(args, "memory")
	cpusExplicit := hasFlagValue(args, "cpus")
	sizeExplicit := hasFlagValue(args, "size-mib")
	specExplicit := hasFlagValue(args, "file")
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
	opts.SupervisorPath = os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR")
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
	fs.StringVar(&opts.Entrypoint, "entrypoint", opts.Entrypoint, "Shell command to run when the workspace starts")
	setupCommands := multiFlag(append([]string{}, opts.SetupCommands...))
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
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.RestartPolicy, "restart", opts.RestartPolicy, "Restart policy: never, on-failure, or always")
	fs.StringVar(&opts.Network.Mode, "network", opts.Network.Mode, "Network mode: nat, isolated, or bridged")
	var publishFlags multiFlag
	fs.Var(&publishFlags, "publish", "Forward host[:hostPort]:guestPort[/tcp]")
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
	opts.Env = mergeEnv(opts.Env, env)
	disks, err := parseWorkspaceDisks(diskFlags, false)
	if err != nil {
		return workspaceOptions{}, err
	}
	bundles, err := parseWorkspaceDisks(bundleFlags, true)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Disks = append(opts.Disks, disks...)
	opts.Disks = append(opts.Disks, bundles...)
	published, err := parsePortForwardMappings(publishFlags)
	if err != nil {
		return workspaceOptions{}, err
	}
	opts.Network.PortForwards = append(opts.Network.PortForwards, published...)
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
	if err := validateRestartPolicy(opts.RestartPolicy); err != nil {
		return workspaceOptions{}, err
	}
	opts.RestartPolicy = normalizeRestartPolicy(opts.RestartPolicy)
	opts.Network = normalizeNetworkConfig(opts.Network)
	if err := vmkit.ValidateNetworkConfig(opts.Network); err != nil {
		return workspaceOptions{}, err
	}
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
	spec, err := readWorkspaceSpec(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(spec.Name) != "" {
		opts.Name = strings.TrimSpace(spec.Name)
	}
	if strings.TrimSpace(spec.ImageRef) != "" {
		opts.ImageRef = strings.TrimSpace(spec.ImageRef)
	}
	if strings.TrimSpace(spec.Profile) != "" {
		opts.Profile = strings.TrimSpace(spec.Profile)
		if err := applyResourceProfile(opts, memoryExplicit, cpusExplicit, sizeExplicit); err != nil {
			return err
		}
	}
	if strings.TrimSpace(spec.Restart) != "" {
		opts.RestartPolicy = normalizeRestartPolicy(spec.Restart)
	}
	if spec.Resources.MemoryMiB != 0 && !memoryExplicit {
		opts.MemoryMiB = spec.Resources.MemoryMiB
		opts.SpecMemory = true
	}
	if spec.Resources.CPUCount != 0 && !cpusExplicit {
		opts.CPUCount = spec.Resources.CPUCount
		opts.SpecCPU = true
	}
	if spec.Resources.SizeMiB != 0 && !sizeExplicit {
		opts.SizeMiB = spec.Resources.SizeMiB
		opts.SpecSize = true
	}
	if spec.Network.Mode != "" || len(spec.Network.PortForwards) != 0 || len(spec.Network.DNS) != 0 || len(spec.Network.Routes) != 0 || spec.Network.IP != "" {
		opts.Network = vmkit.NetworkConfig{
			Mode:         spec.Network.Mode,
			PortForwards: append([]vmkit.PortForward{}, spec.Network.PortForwards...),
			DNS:          append([]string{}, spec.Network.DNS...),
			Routes:       append([]string{}, spec.Network.Routes...),
			IP:           spec.Network.IP,
		}
	}
	if strings.TrimSpace(spec.Entrypoint) != "" {
		opts.Entrypoint = spec.Entrypoint
	}
	if len(spec.Setup) != 0 {
		opts.SetupCommands = append([]string{}, spec.Setup...)
	}
	opts.Env = mergeEnv(opts.Env, spec.Env)
	disks, err := workspaceSpecDisks(spec)
	if err != nil {
		return err
	}
	opts.Disks = append(opts.Disks, disks...)
	return nil
}

func readWorkspaceSpec(path string) (workspaceSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workspaceSpec{}, err
	}
	var spec workspaceSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return workspaceSpec{}, err
	}
	return spec, nil
}

func workspaceSpecDisks(spec workspaceSpec) ([]workspaceDisk, error) {
	disks := make([]workspaceDisk, 0, len(spec.Disks)+len(spec.Bundles))
	for _, disk := range spec.Disks {
		disk.Bundle = false
		if err := validateWorkspaceDisk(disk); err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	for _, disk := range spec.Bundles {
		disk.Bundle = true
		if disk.SourcePath == "" {
			disk.SourcePath = disk.Path
		}
		if err := validateWorkspaceDisk(disk); err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func validateWorkspaceDisk(disk workspaceDisk) error {
	if strings.TrimSpace(disk.Name) == "" {
		return fmt.Errorf("disk name is required")
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

func mergeEnv(base, overrides map[string]string) map[string]string {
	if len(base) == 0 && len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(overrides))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overrides {
		out[key] = value
	}
	return out
}

func applyResourceProfile(opts *workspaceOptions, memoryExplicit, cpusExplicit, sizeExplicit bool) error {
	profile, ok := lookupResourceProfile(opts.Profile)
	if !ok {
		return fmt.Errorf("unknown resource profile %q; choose one of: %s", opts.Profile, strings.Join(resourceProfileNames(), ", "))
	}
	opts.Profile = profile.Name
	if !memoryExplicit {
		opts.MemoryMiB = profile.Resources.MemoryMiB
	}
	if !cpusExplicit {
		opts.CPUCount = profile.Resources.CPUCount
	}
	if !sizeExplicit {
		opts.SizeMiB = profile.Resources.SizeMiB
	}
	return nil
}

func lookupResourceProfile(name string) (resourceProfile, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, profile := range resourceProfiles {
		if profile.Name == name {
			return profile, true
		}
	}
	return resourceProfile{}, false
}

func resourceProfileNames() []string {
	names := make([]string, 0, len(resourceProfiles))
	for _, profile := range resourceProfiles {
		names = append(names, profile.Name)
	}
	return names
}

func workspaceResources(opts workspaceOptions) resourceConfig {
	return resourceConfig{
		MemoryMiB: opts.MemoryMiB,
		CPUCount:  opts.CPUCount,
		SizeMiB:   opts.SizeMiB,
	}
}

func validateResourceConfig(resources resourceConfig, requireDisk bool) error {
	if resources.MemoryMiB <= 0 {
		return fmt.Errorf("memory must be positive")
	}
	if resources.CPUCount <= 0 {
		return fmt.Errorf("cpus must be positive")
	}
	if requireDisk && resources.SizeMiB <= 0 {
		return fmt.Errorf("size-mib must be positive")
	}
	if resources.SizeMiB < 0 {
		return fmt.Errorf("size-mib must not be negative")
	}
	return nil
}

func validateRestartPolicy(policy string) error {
	switch normalizeRestartPolicy(policy) {
	case "never", "on-failure", "always":
		return nil
	default:
		return fmt.Errorf("restart policy must be never, on-failure, or always")
	}
}

func normalizeRestartPolicy(policy string) string {
	if strings.TrimSpace(policy) == "" {
		return defaultRestartPolicy
	}
	return strings.TrimSpace(policy)
}

func canUseImageBaseline(opts workspaceOptions) bool {
	return opts.PrepareForStart &&
		!workspaceHasGuestCommand(opts) &&
		len(opts.Disks) == 0 &&
		len(opts.Env) == 0
}

func normalizeNetworkConfig(network vmkit.NetworkConfig) vmkit.NetworkConfig {
	network.Mode = strings.TrimSpace(network.Mode)
	if network.Mode == "" {
		network.Mode = defaultNetworkMode
	}
	for i := range network.PortForwards {
		network.PortForwards[i].Protocol = strings.TrimSpace(network.PortForwards[i].Protocol)
		if network.PortForwards[i].Protocol == "" {
			network.PortForwards[i].Protocol = "tcp"
		}
		network.PortForwards[i].Host = strings.TrimSpace(network.PortForwards[i].Host)
	}
	return network
}

func networkSpecFromConfig(network vmkit.NetworkConfig) networkSpec {
	network = normalizeNetworkConfig(network)
	return networkSpec{
		Mode:         network.Mode,
		PortForwards: append([]vmkit.PortForward{}, network.PortForwards...),
		DNS:          append([]string{}, network.DNS...),
		Routes:       append([]string{}, network.Routes...),
		IP:           network.IP,
	}
}

func networkConfigFromSpec(spec networkSpec) vmkit.NetworkConfig {
	return normalizeNetworkConfig(vmkit.NetworkConfig{
		Mode:         spec.Mode,
		PortForwards: append([]vmkit.PortForward{}, spec.PortForwards...),
		DNS:          append([]string{}, spec.DNS...),
		Routes:       append([]string{}, spec.Routes...),
		IP:           spec.IP,
	})
}

func networkConfigPtr(network vmkit.NetworkConfig) *vmkit.NetworkConfig {
	normalized := normalizeNetworkConfig(network)
	return &normalized
}

func createWorkspaceRootfs(ctx context.Context, opts workspaceOptions) (workspaceResult, error) {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	rootfsPath := filepath.Join(workspaceDir, "rootfs.ext4")
	if canUseImageBaseline(opts) {
		if record, err := findImageRecord(opts.StateDir, opts.ImageRef, rootfs.Platform{OS: "linux", Architecture: opts.Architecture}); err == nil {
			if err := copyFile(record.OutputPath, rootfsPath, 0o644); err != nil {
				return workspaceResult{}, err
			}
			return workspaceResult{
				Workspace:  opts.Name,
				StateDir:   opts.StateDir,
				Profile:    opts.Profile,
				Restart:    opts.RestartPolicy,
				Resources:  workspaceResources(opts),
				Network:    networkSpecFromConfig(opts.Network),
				RootfsPath: rootfsPath,
				KernelPath: opts.KernelPath,
				Image:      provenanceFromImageRecord(record, rootfsPath),
			}, nil
		}
	}
	command, resultPort := workspaceBuildCommandAndPort(opts)
	req := rootfs.BuildRequest{
		ImageRef:       opts.ImageRef,
		Platform:       rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:     rootfsPath,
		InitPath:       rootfs.DefaultInitPath,
		Command:        command,
		InitBinaryPath: opts.GuestInitPath,
		ResultPort:     resultPort,
		NoImageCommand: opts.PrepareForStart && !workspaceHasGuestCommand(opts),
		StateDir:       filepath.Join(opts.StateDir, "build"),
		Mke2fsPath:     opts.Mke2fsPath,
		SizeMiB:        opts.SizeMiB,
		Env:            opts.Env,
		Mounts:         workspaceMounts(opts.Disks),
		HostForwards:   rootfsPortForwards(opts.Network.PortForwards),
		AllowMutable:   true,
	}
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	result := workspaceResult{
		Workspace:  opts.Name,
		StateDir:   opts.StateDir,
		Profile:    opts.Profile,
		Restart:    opts.RestartPolicy,
		Resources:  workspaceResources(opts),
		Network:    networkSpecFromConfig(opts.Network),
		RootfsPath: rootfsPath,
		KernelPath: opts.KernelPath,
		Image:      provenance,
	}
	if err != nil {
		return result, err
	}
	if err := recordImageProvenance(opts.StateDir, provenance); err != nil {
		return result, err
	}
	return result, nil
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

func rootfsPortForwards(forwards []vmkit.PortForward) []rootfs.PortForward {
	if len(forwards) == 0 {
		return nil
	}
	out := make([]rootfs.PortForward, 0, len(forwards))
	for _, forward := range forwards {
		out = append(out, rootfs.PortForward{
			Protocol:  "tcp",
			HostPort:  forward.HostPort,
			GuestPort: forward.GuestPort,
		})
	}
	return out
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
		Name:      opts.Name,
		Profile:   opts.Profile,
		Restart:   normalizeRestartPolicy(opts.RestartPolicy),
		Resources: workspaceResources(opts),
		Network:   networkSpecFromConfig(opts.Network),
		Disks:     opts.Disks,
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
			Network:        networkConfigPtr(opts.Network),
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
	network := vmkit.NetworkConfig{Mode: defaultNetworkMode}
	if req.Config.Network != nil {
		network = normalizeNetworkConfig(*req.Config.Network)
	}
	return workspaceOptions{
		Name:           req.Identity.RuntimeID,
		Backend:        req.Identity.Backend,
		KernelPath:     req.Config.KernelPath,
		StateDir:       req.Config.StateDir,
		SupervisorPath: supervisorPath,
		RestartPolicy:  defaultRestartPolicy,
		MemoryMiB:      req.Config.MemoryMiB,
		CPUCount:       req.Config.CPUCount,
		Network:        network,
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

func workspaceSupervisor(opts workspaceOptions) (vmkit.Supervisor, error) {
	switch opts.Backend {
	case vmkit.BackendFirecracker:
		return vmkit.ExecutableSupervisor{Path: firecrackerSupervisorPath(opts)}, nil
	case vmkit.BackendAppleVF:
		return vmkit.ExecutableSupervisor{Path: opts.SupervisorPath}, nil
	default:
		return nil, fmt.Errorf("unsupported backend: %s", opts.Backend)
	}
}

func firecrackerSupervisorPath(opts workspaceOptions) string {
	if opts.SupervisorPath != "" {
		return opts.SupervisorPath
	}
	if path := strings.TrimSpace(os.Getenv("MICROAGENT_FIRECRACKER_SUPERVISOR")); path != "" {
		return path
	}
	return "microagent-firecracker-supervisor"
}

func dispatchWorkspaceRequest(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	supervisor, err := workspaceSupervisor(opts)
	if err != nil {
		return vmkit.Response{Backend: opts.Backend, Error: err.Error()}, err
	}
	return supervisor.Do(ctx, req)
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
		resp.Network = &network
	}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
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
	if result.Profile != "" {
		fmt.Fprintf(stdout, "Profile: %s\n", result.Profile)
	}
	if result.Restart != "" {
		fmt.Fprintf(stdout, "Restart: %s\n", result.Restart)
	}
	if result.Network.Mode != "" {
		fmt.Fprintf(stdout, "Network: %s\n", result.Network.Mode)
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

func writeCopyResult(stdout *os.File, result copyResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	fmt.Fprintf(stdout, "Disk: %s\n", result.Disk)
	fmt.Fprintf(stdout, "Direction: %s\n", result.Direction)
	fmt.Fprintf(stdout, "Source: %s\n", result.Source)
	fmt.Fprintf(stdout, "Target: %s\n", result.Target)
	if result.Bytes != 0 {
		fmt.Fprintf(stdout, "Bytes: %d\n", result.Bytes)
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
		fmt.Fprintf(stdout, "Forward: %s %s:%d -> :%d\n", forward.Protocol, host, forward.HostPort, forward.GuestPort)
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
	fmt.Fprintf(stdout, "Kept: %d\n", len(result.Kept))
	return nil
}

func humanOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
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
	return nil
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
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateStopped:
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

func pruneImageRecords(stateDir string) (imagePruneResult, error) {
	idx, err := readImageIndex(stateDir)
	if err != nil {
		return imagePruneResult{}, err
	}
	result := imagePruneResult{}
	for _, image := range idx.Images {
		if image.OutputPath == "" {
			result.Kept = append(result.Kept, image)
			continue
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
	if err := writeImageIndex(stateDir, imageIndex{Images: result.Kept}); err != nil {
		return imagePruneResult{}, err
	}
	return result, nil
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
	return hasFlagValue(args, "file") || hasFlagValue(args, "name") || hasFlagValue(args, "id") || hasFlagValue(args, "setup") || hasFlagValue(args, "entrypoint") || hasFlagValue(args, "env") || hasFlagValue(args, "disk") || hasFlagValue(args, "bundle")
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
	writable := defaultWritableKernelPath(backend, arch)
	if writable == "" {
		return defaultPackagedKernelPath(backend, arch)
	}
	if _, err := os.Stat(writable); err == nil {
		return writable
	}
	legacy := defaultLegacyKernelPath(backend)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	packaged := defaultPackagedKernelPath(backend, arch)
	if packaged != "" {
		if _, err := os.Stat(packaged); err == nil {
			return packaged
		}
	}
	return writable
}

func defaultWritableKernelPath(backend, arch string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".microagent", "kernels", backend, arch, "Image")
}

func defaultLegacyKernelPath(backend string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".microagent", "kernels", backend, "Image")
}

func defaultPackagedKernelPath(backend, arch string) string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return defaultPackagedKernelPathFromExecutable(executable, backend, arch)
}

func defaultPackagedKernelPathFromExecutable(executable, backend, arch string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	dir := filepath.Dir(executable)
	candidates := []string{
		filepath.Join(filepath.Clean(filepath.Join(dir, "..", "libexec")), "kernels", backend, arch, "Image"),
		filepath.Join(dir, "kernels", backend, arch, "Image"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
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
		if arg == "--json" || arg == "-json" || arg == "--rootfs" || arg == "-rootfs" || arg == "--kernel" || arg == "-kernel" || arg == "--name" || arg == "-name" || arg == "--id" || arg == "-id" || arg == "--file" || arg == "-file" || arg == "--entrypoint" || arg == "-entrypoint" || arg == "--env" || arg == "-env" {
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
		lines = append(lines, resetGuestConfigCommand(shellCommand(opts.Entrypoint), opts.Env, 0, workspaceMounts(opts.Disks), rootfsPortForwards(opts.Network.PortForwards)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "set -eu\n" + strings.Join(lines, "\n")
}

func workspaceBuildCommandAndPort(opts workspaceOptions) ([]string, uint32) {
	if opts.PrepareForStart && !workspaceHasGuestCommand(opts) {
		return shellCommand(opts.Entrypoint), 0
	}
	return shellCommand(workspaceCommand(opts)), opts.ResultPort
}

func resetGuestConfigCommand(command []string, env map[string]string, port uint32, mounts []rootfs.Mount, forwards []rootfs.PortForward) string {
	if command == nil {
		command = []string{}
	}
	data, err := json.Marshal(struct {
		Command      []string             `json:"command"`
		Env          []string             `json:"env,omitempty"`
		Port         uint32               `json:"port"`
		Mounts       []rootfs.Mount       `json:"mounts,omitempty"`
		HostForwards []rootfs.PortForward `json:"hostForwards,omitempty"`
	}{
		Command:      command,
		Env:          envList(env),
		Port:         port,
		Mounts:       mounts,
		HostForwards: forwards,
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
			chunk := buffer[:n]
			if idx := bytes.IndexByte(chunk, consoleDetachByte); idx >= 0 {
				written, writeErr := writeConsoleInputChunk(dst, chunk[:idx])
				total += written
				if writeErr != nil {
					return total, writeErr
				}
				return total, nil
			}
			written, writeErr := writeConsoleInputChunk(dst, chunk)
			total += written
			if writeErr != nil {
				return total, writeErr
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

func writeConsoleInputChunk(dst io.Writer, chunk []byte) (int64, error) {
	if len(chunk) == 0 {
		return 0, nil
	}
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
		"-supervisor":    true,
		"-json":          true,
		"-id":            true,
		"-name":          true,
		"-image":         true,
		"-exec":          true,
		"-entrypoint":    true,
		"-file":          true,
		"-env":           true,
		"-setup":         true,
		"-request-id":    true,
		"-role":          true,
		"-backend":       true,
		"-kernel":        true,
		"-rootfs":        true,
		"-disk":          true,
		"-bundle":        true,
		"-debugfs":       true,
		"-profile":       true,
		"-restart":       true,
		"-network":       true,
		"-publish":       true,
		"-state-dir":     true,
		"-url":           true,
		"-from":          true,
		"-sha256":        true,
		"-out":           true,
		"-path":          true,
		"-memory":        true,
		"-cpus":          true,
		"-vsock":         true,
		"-mke2fs":        true,
		"-guest-init":    true,
		"-arch":          true,
		"-size-mib":      true,
		"-timeout":       true,
		"-ready-timeout": true,
		"-interval":      true,
		"-max-restarts":  true,
		"-result-port":   true,
		"-send":          true,
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
  clone                Clone a stopped workspace
  cp                   Copy files into or out of a stopped workspace
  network              Inspect workspace network config
  start                Start a workspace
  supervise            Run host restart supervision for a workspace
  connect              Open the workspace console
  ps                   List workspaces
  status               Show workspace state
  logs                 Show workspace logs
  profiles             List resource profiles
  images               List or prune local image records
  perf                 Measure workspace performance
  stop                 Stop a workspace
  kill                 Force stop a workspace
  delete               Delete a workspace
  host                 Report host capabilities
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
  -debugfs <path>       debugfs binary path
  -file <path>          Workspace spec file
  -kernel <path>        Custom kernel path
  -rootfs <path>        Rootfs image path
  -state-dir <dir>      State directory
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
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

Boot options:
  -image <ref>          OCI image; defaults to the small BusyBox baseline
  -exec <command>       Guest command used to mark boot completion; defaults to true
  -iterations <n>       Number of boot measurements
  -profile <name>       Resource profile: tiny, small, medium, or large
  -state-dir <dir>      State directory
  -timeout <seconds>    Per-iteration timeout
  -mke2fs <path>        mke2fs binary path
  -supervisor <path>    Override the supervisor path
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
  -file <path>          Workspace spec file
  -name <name>          Workspace name; generated when omitted
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
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
  -file <path>          Workspace spec file
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
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
