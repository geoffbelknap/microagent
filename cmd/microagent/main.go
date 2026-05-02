package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
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
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected rootfs argument: %s", fs.Arg(0))
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
	fs.StringVar(&identity.RuntimeID, "id", "", "Runtime ID")
	fs.StringVar(&identity.RequestID, "request-id", "", "Request ID")
	fs.StringVar((*string)(&identity.Role), "role", string(vmkit.RoleWorkload), "Runtime role")
	fs.StringVar(&identity.Backend, "backend", vmkit.BackendAppleVF, "Runtime backend")
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
		"-request-id": true,
		"-role":       true,
		"-backend":    true,
		"-kernel":     true,
		"-rootfs":     true,
		"-state-dir":  true,
		"-memory":     true,
		"-cpus":       true,
		"-vsock":      true,
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
  doctor               Diagnose local Apple VF support
  create               Create runtime state
  start                Validate and attempt VM start
  status               Read persisted runtime state
  stop                 Mark a runtime stopped
  kill                 Mark a runtime forcibly stopped
  delete               Delete persisted runtime state
  rootfs build         Build an ext4 rootfs from an OCI image
  version              Print version information
  help                 Show this help

Options:
  -helper <path>        Override the Apple VF helper path
  -json <path|- >       Read request JSON from a file or stdin
  -id <id>              Runtime ID
  -kernel <path>        Linux kernel path
  -rootfs <path>        Rootfs image path
  -state-dir <dir>      Runtime state directory
  -memory <MiB>         Memory in MiB
  -cpus <n>             CPU count
  -vsock p=host:port    Add a vsock listener mapping
`)
}

func printRootFSHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent rootfs

Commands:
  build                Build an ext4 rootfs from an OCI image

Build options:
  -image <ref>         OCI image reference
  -out <path>          Output rootfs path
  -os <os>             Target OS
  -arch <arch>         Target architecture
  -size-mib <MiB>      Rootfs image size
  -mke2fs <path>       mke2fs binary path
  -allow-mutable       Allow tag references
`)
}
