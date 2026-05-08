package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/kernel"
	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
	"github.com/geoffbelknap/microagent-kit/pkg/workspace"
)

type Options struct {
	Backend        string
	Arch           string
	SupervisorPath string
}

type FirecrackerProbe struct {
	ResolveBinary func() (string, error)
	Stat          func(string) (os.FileInfo, error)
	BinaryVersion func(string) string
	LookPath      func(string) (string, error)
	ReadFile      func(string) ([]byte, error)
}

func Check(ctx context.Context, opts Options) (vmkit.Response, error) {
	if opts.Backend == "" {
		opts.Backend = workspace.HostBackend()
	}
	if opts.Arch == "" {
		opts.Arch = workspace.GuestArch()
	}
	switch opts.Backend {
	case vmkit.BackendAppleVF:
		resp, err := vmkit.ExecutableSupervisor{Path: opts.SupervisorPath}.Do(ctx, vmkit.Request{Command: "host"})
		if resp.Backend == "" {
			resp.Backend = opts.Backend
		}
		resp.Kernel = kernel.Support(opts.Backend, opts.Arch)
		if err != nil && resp.Error == "" {
			resp.Error = err.Error()
		}
		AugmentHostSupport(&resp, opts)
		return resp, err
	case vmkit.BackendFirecracker:
		resp, err := CheckFirecracker(opts, FirecrackerProbe{
			ResolveBinary: ResolveFirecrackerPath,
			Stat:          os.Stat,
			BinaryVersion: FirecrackerVersion,
		})
		AugmentHostSupport(&resp, opts)
		return resp, err
	default:
		resp := vmkit.Response{
			OK:      false,
			Backend: opts.Backend,
			Kernel:  kernel.Support(opts.Backend, opts.Arch),
			Error:   fmt.Sprintf("unsupported backend: %s", opts.Backend),
		}
		AugmentHostSupport(&resp, opts)
		return resp, fmt.Errorf("%s", resp.Error)
	}
}

func CheckFirecracker(opts Options, probe FirecrackerProbe) (vmkit.Response, error) {
	if probe.ResolveBinary == nil {
		probe.ResolveBinary = ResolveFirecrackerPath
	}
	if probe.Stat == nil {
		probe.Stat = os.Stat
	}
	if probe.BinaryVersion == nil {
		probe.BinaryVersion = FirecrackerVersion
	}
	if probe.LookPath == nil {
		probe.LookPath = exec.LookPath
	}
	if probe.ReadFile == nil {
		probe.ReadFile = os.ReadFile
	}
	host := &vmkit.HostSupport{
		Backend:      opts.Backend,
		Architecture: opts.Arch,
	}
	var issues []string
	if path, err := probe.ResolveBinary(); err == nil {
		host.BinaryPath = path
		host.BinaryVersion = probe.BinaryVersion(path)
		host.FrameworkAvailable = true
	} else {
		issues = append(issues, err.Error())
	}
	if _, err := probe.Stat("/dev/kvm"); err == nil {
		host.KVMAvailable = true
		host.VirtualizationSupported = true
	} else {
		issues = append(issues, "/dev/kvm is not available")
	}
	if _, err := probe.Stat("/dev/vhost-vsock"); err == nil {
		host.VsockAvailable = true
	}
	if _, err := probe.Stat("/dev/net/tun"); err == nil {
		host.TunAvailable = true
	} else {
		issues = append(issues, "/dev/net/tun is not available for Firecracker user networking")
	}
	if path, err := probe.LookPath("pasta"); err == nil {
		host.UserNetworkingAvailable = true
		host.UserNetworkingBinary = path
	} else if path, err := probe.LookPath("slirp4netns"); err == nil {
		host.UserNetworkingBinary = path
		issues = append(issues, "pasta is not installed; install passt (for example, apt install passt)")
	} else {
		issues = append(issues, "pasta is not installed; install passt (for example, apt install passt)")
	}
	usernsOK, usernsIssue := checkUserNamespaces(probe.ReadFile)
	host.UserNamespacesAvailable = usernsOK
	if usernsIssue != "" {
		issues = append(issues, usernsIssue)
	}
	host.ConsoleAvailable = true
	host.ConsoleMode = "interactive"
	resp := vmkit.Response{
		OK:      len(issues) == 0,
		Backend: opts.Backend,
		Host:    host,
		Kernel:  kernel.Support(opts.Backend, opts.Arch),
	}
	if len(issues) > 0 {
		resp.Error = strings.Join(issues, "; ")
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

func checkUserNamespaces(readFile func(string) ([]byte, error)) (bool, string) {
	if data, err := readFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) != "1" {
			return false, "unprivileged user namespaces are disabled; set kernel.unprivileged_userns_clone=1"
		}
	}
	if data, err := readFile("/proc/sys/user/max_user_namespaces"); err == nil {
		value := strings.TrimSpace(string(data))
		if value == "" || value == "0" {
			return false, "user namespaces are disabled; set user.max_user_namespaces above 0"
		}
	}
	return true, ""
}

func AugmentHostSupport(resp *vmkit.Response, opts Options) {
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
		resp.Host.SupervisorPath = workspace.FirecrackerSupervisorPath(workspace.Options{SupervisorPath: opts.SupervisorPath})
		resp.Host.SupervisorAvailable = true
		resp.Host.ConsoleAvailable = true
		resp.Host.ConsoleMode = "interactive"
	}
}

func ResolveFirecrackerPath() (string, error) {
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
		path := DefaultFirecrackerPathFromExecutable(exe)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("firecracker binary not found")
}

func DefaultFirecrackerPathFromExecutable(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "libexec", "firecracker"))
}

func FirecrackerVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return FirstOutputLine(string(output))
}

func FirstOutputLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
