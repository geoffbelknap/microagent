package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	if args[0] == "version" {
		fmt.Fprintf(stdout, "microagent %s\n", version)
		return nil
	}
	if args[0] == "rootfs" {
		return runRootFS(ctx, args[1:], stdout)
	}
	if args[0] == "run" {
		return runWorkspace(ctx, args[1:], stdout)
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
	Name         string
	ImageRef     string
	ExecCommand  string
	Backend      string
	KernelPath   string
	StateDir     string
	HelperPath   string
	Mke2fsPath   string
	Architecture string
	MemoryMiB    int
	CPUCount     int
	SizeMiB      int64
	Timeout      time.Duration
	Keep         bool
}

type workspaceResult struct {
	Workspace  string            `json:"workspace"`
	StateDir   string            `json:"state_dir"`
	RootfsPath string            `json:"rootfs_path"`
	KernelPath string            `json:"kernel_path"`
	SerialPath string            `json:"serial_path,omitempty"`
	SerialLog  string            `json:"serial_log,omitempty"`
	FinalState string            `json:"final_state,omitempty"`
	Image      rootfs.Provenance `json:"image"`
	Response   vmkit.Response    `json:"response"`
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
	result, err := createWorkspaceRootfs(ctx, opts)
	if err != nil {
		return err
	}
	req := workspaceRequest(opts, "start", result.RootfsPath)
	resp, err := vmkit.HelperClient{Path: opts.HelperPath}.Do(ctx, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	result.Response = resp
	result.SerialPath = serialLogPath(opts)
	if err == nil && resp.OK {
		finalResp, waitErr := waitForWorkspace(ctx, opts)
		if finalResp.Event != nil {
			result.FinalState = string(finalResp.Event.State)
			result.Response = finalResp
		}
		if serial, readErr := os.ReadFile(result.SerialPath); readErr == nil {
			result.SerialLog = string(serial)
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

func runHighLevelCreate(ctx context.Context, args []string, stdout *os.File) error {
	opts, err := parseWorkspaceOptions("create", args)
	if err != nil {
		return err
	}
	if opts.Name == "" {
		return fmt.Errorf("create requires --name")
	}
	result, err := createWorkspaceRootfs(ctx, opts)
	if err != nil {
		return err
	}
	req := workspaceRequest(opts, "prepare", result.RootfsPath)
	resp, err := vmkit.HelperClient{Path: opts.HelperPath}.Do(ctx, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	result.Response = resp
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encodeErr := enc.Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
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
	}
	opts.StateDir = defaultStateDir()
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.Mke2fsPath = defaultMke2fsPath()
	opts.HelperPath = os.Getenv("MICROAGENT_APPLEVF_HELPER")
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Name, "name", "", "Workspace name")
	fs.StringVar(&opts.Name, "id", "", "Workspace ID")
	fs.StringVar(&opts.ImageRef, "image", "", "OCI image reference")
	fs.StringVar(&opts.ExecCommand, "exec", "", "Shell command to run as guest init")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "VM backend")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "Microagent state directory")
	fs.StringVar(&opts.HelperPath, "helper", opts.HelperPath, "Apple VF helper path")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	fs.Int64Var(&opts.SizeMiB, "size-mib", opts.SizeMiB, "Rootfs image size in MiB")
	var timeoutSeconds int
	fs.IntVar(&timeoutSeconds, "timeout", int(opts.Timeout.Seconds()), "Run timeout in seconds")
	fs.BoolVar(&opts.Keep, "keep", false, "Keep workspace state after run")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return workspaceOptions{}, err
	}
	if fs.NArg() != 0 {
		return workspaceOptions{}, fmt.Errorf("unexpected %s argument: %s", command, fs.Arg(0))
	}
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		return workspaceOptions{}, fmt.Errorf("%s requires --image", command)
	}
	if !kernelExplicit {
		opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	if timeoutSeconds <= 0 {
		return workspaceOptions{}, fmt.Errorf("%s timeout must be positive", command)
	}
	opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	return opts, nil
}

func createWorkspaceRootfs(ctx context.Context, opts workspaceOptions) (workspaceResult, error) {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	rootfsPath := filepath.Join(workspaceDir, "rootfs.ext4")
	req := rootfs.BuildRequest{
		ImageRef:     opts.ImageRef,
		Platform:     rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:   rootfsPath,
		InitPath:     rootfs.DefaultInitPath,
		Command:      shellCommand(opts.ExecCommand),
		StateDir:     filepath.Join(opts.StateDir, "build"),
		Mke2fsPath:   opts.Mke2fsPath,
		SizeMiB:      opts.SizeMiB,
		AllowMutable: true,
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
	return vmkit.Request{
		Command: command,
		Identity: &vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   opts.Backend,
		},
		Config: &vmkit.Config{
			KernelPath: opts.KernelPath,
			RootfsPath: rootfsPath,
			StateDir:   opts.StateDir,
			MemoryMiB:  opts.MemoryMiB,
			CPUCount:   opts.CPUCount,
		},
	}
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

func serialLogPath(opts workspaceOptions) string {
	return filepath.Join(opts.StateDir, opts.Name, "serial.log")
}

func cleanupWorkspaceState(opts workspaceOptions) {
	_ = os.RemoveAll(filepath.Join(opts.StateDir, "workspaces", opts.Name))
	_ = os.RemoveAll(filepath.Join(opts.StateDir, opts.Name))
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

func shellCommand(command string) []string {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return []string{"/bin/sh", "-lc", command}
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
		"-helper":     true,
		"-json":       true,
		"-id":         true,
		"-name":       true,
		"-image":      true,
		"-exec":       true,
		"-request-id": true,
		"-role":       true,
		"-backend":    true,
		"-kernel":     true,
		"-rootfs":     true,
		"-state-dir":  true,
		"-memory":     true,
		"-cpus":       true,
		"-vsock":      true,
		"-mke2fs":     true,
		"-arch":       true,
		"-size-mib":   true,
		"-timeout":    true,
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
  status               Show workspace state
  stop                 Stop a workspace
  kill                 Force stop a workspace
  delete               Delete a workspace
  doctor               Check the host
  rootfs build         Build a rootfs from an OCI image
  version              Print the version
  help                 Show help

Options:
  -helper <path>        Override the Apple VF helper path
  -json <path|- >       Read request JSON from a file or stdin
  -image <ref>          OCI image
  -name <name>          Workspace name
  -id <id>              Workspace ID
  -kernel <path>        Kernel path
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
  -name <name>          Workspace name; generated when omitted
  -kernel <path>        Kernel path
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
