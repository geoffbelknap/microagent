package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/kernel"
	windowshyperv "github.com/geoffbelknap/microagent/pkg/supervisors/windows_hyperv"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

type Options struct {
	Backend        string
	Arch           string
	SupervisorPath string
}

type FirecrackerProbe struct {
	ResolveBinary          func() (string, error)
	ResolveSupervisor      func(Options) (string, error)
	ResolveGuestInit       func(Options) (string, error)
	Stat                   func(string) (os.FileInfo, error)
	BinaryVersion          func(string) string
	LookPath               func(string) (string, error)
	ReadFile               func(string) ([]byte, error)
	ReadBinaryCapabilities func(path string) (bool, error)
	ProbeUserNamespaces    func() error
}

type WindowsHyperVProbe struct {
	HostResponse        func() vmkit.Response
	KernelSupport       func(string, string) *vmkit.KernelSupport
	ResolveGuestInit    func(Options) (string, error)
	ProbeHCSAccess      func(context.Context) error
	ProbeHCNAccess      func(context.Context) error
	ProbeHvSocketAccess func(context.Context) error
}

func Check(ctx context.Context, opts Options) (vmkit.Response, error) {
	if opts.Backend == "" {
		opts.Backend = workspace.HostBackend()
	}
	if opts.Arch == "" {
		opts.Arch = workspace.GuestArch()
	}
	if err := workspace.ValidateHostBackend(opts.Backend); err != nil {
		resp := vmkit.Response{
			OK:      false,
			Backend: opts.Backend,
			Kernel:  kernel.Support(opts.Backend, opts.Arch),
			Error:   err.Error(),
		}
		AugmentHostSupport(&resp, opts)
		return resp, err
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
			ResolveBinary:          ResolveFirecrackerPath,
			Stat:                   os.Stat,
			BinaryVersion:          FirecrackerVersion,
			ReadBinaryCapabilities: BinaryHasNetAdmin,
		})
		AugmentHostSupport(&resp, opts)
		return resp, err
	case vmkit.BackendWindowsHyperV:
		resp, err := CheckWindowsHyperV(ctx, opts, WindowsHyperVProbe{})
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

func CheckWindowsHyperV(ctx context.Context, opts Options, probe WindowsHyperVProbe) (vmkit.Response, error) {
	if probe.HostResponse == nil {
		probe.HostResponse = windowshyperv.HostResponse
	}
	if probe.KernelSupport == nil {
		probe.KernelSupport = kernel.Support
	}
	if probe.ResolveGuestInit == nil {
		probe.ResolveGuestInit = ResolveGuestInitPath
	}
	if probe.ProbeHCSAccess == nil {
		probe.ProbeHCSAccess = windowshyperv.ProbeHCSAccess
	}
	if probe.ProbeHCNAccess == nil {
		probe.ProbeHCNAccess = windowshyperv.ProbeHCNAccess
	}
	if probe.ProbeHvSocketAccess == nil {
		probe.ProbeHvSocketAccess = windowshyperv.ProbeHvSocketAccess
	}
	if opts.Backend == "" {
		opts.Backend = vmkit.BackendWindowsHyperV
	}
	resp := probe.HostResponse()
	if resp.Backend == "" {
		resp.Backend = opts.Backend
	}
	resp.Kernel = probe.KernelSupport(opts.Backend, opts.Arch)
	AugmentHostSupport(&resp, opts)
	var issues []string
	if resp.Error != "" {
		issues = append(issues, resp.Error)
	}
	if resp.Host == nil || !resp.Host.FrameworkAvailable {
		issues = append(issues, "Windows Host Compute Service is not available")
	}
	if resp.Host == nil || !resp.Host.VirtualizationSupported {
		issues = append(issues, "Hyper-V/WHP virtualization support is not available")
	}
	if resp.Kernel == nil || resp.Kernel.Status != "present" {
		if resp.Kernel != nil && resp.Kernel.Error != "" {
			issues = append(issues, fmt.Sprintf("windows-hyperv kernel is %s: %s", resp.Kernel.Status, resp.Kernel.Error))
		} else {
			status := "unavailable"
			if resp.Kernel != nil && resp.Kernel.Status != "" {
				status = resp.Kernel.Status
			}
			issues = append(issues, fmt.Sprintf("windows-hyperv kernel is %s", status))
		}
	}
	if path, err := probe.ResolveGuestInit(opts); err == nil {
		resp.Host.GuestInitPath = path
		resp.Host.GuestInitAvailable = true
	} else {
		resp.Host.GuestInitPath = workspace.GuestInitPath(opts.Arch)
		issues = append(issues, err.Error())
	}
	if err := probe.ProbeHCSAccess(ctx); err != nil {
		issues = append(issues, err.Error())
	}
	if err := probe.ProbeHCNAccess(ctx); err != nil {
		resp.Host.UserNetworkingAvailable = false
		issues = append(issues, err.Error())
	} else {
		resp.Host.UserNetworkingAvailable = true
	}
	if err := probe.ProbeHvSocketAccess(ctx); err != nil {
		resp.Host.VsockAvailable = false
		resp.Host.ConsoleAvailable = false
		resp.Host.ConsoleMode = "unavailable"
		issues = append(issues, err.Error())
	} else {
		resp.Host.VsockAvailable = true
		resp.Host.ConsoleAvailable = true
		resp.Host.ConsoleMode = "hvsock"
	}
	if len(issues) > 0 {
		resp.OK = false
		resp.Error = strings.Join(dedupeIssues(issues), "; ")
		return resp, fmt.Errorf("%s", resp.Error)
	}
	resp.OK = true
	resp.Error = ""
	return resp, nil
}

func CheckFirecracker(opts Options, probe FirecrackerProbe) (vmkit.Response, error) {
	if probe.ResolveBinary == nil {
		probe.ResolveBinary = ResolveFirecrackerPath
	}
	if probe.ResolveSupervisor == nil {
		probe.ResolveSupervisor = ResolveFirecrackerSupervisorPath
	}
	if probe.ResolveGuestInit == nil {
		probe.ResolveGuestInit = ResolveGuestInitPath
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
	if probe.ProbeUserNamespaces == nil {
		probe.ProbeUserNamespaces = defaultUserNamespaceProbe
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
	if path, err := probe.ResolveSupervisor(opts); err == nil {
		host.SupervisorPath = path
		host.SupervisorAvailable = true
	} else {
		host.SupervisorPath = workspace.FirecrackerSupervisorPath(workspace.Options{SupervisorPath: opts.SupervisorPath})
		issues = append(issues, err.Error())
	}
	if path, err := probe.ResolveGuestInit(opts); err == nil {
		host.GuestInitPath = path
		host.GuestInitAvailable = true
	} else {
		host.GuestInitPath = workspace.GuestInitPath(opts.Arch)
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
	usernsOK, usernsIssue := checkUserNamespaces(probe.ReadFile, probe.ProbeUserNamespaces)
	host.UserNamespacesAvailable = usernsOK
	if usernsIssue != "" {
		issues = append(issues, usernsIssue)
	}
	if probe.ReadFile != nil {
		if data, err := probe.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
			host.IPForwardEnabled = strings.TrimSpace(string(data)) == "1"
		}
	}
	if probe.ReadBinaryCapabilities != nil && host.SupervisorPath != "" {
		if ok, err := probe.ReadBinaryCapabilities(host.SupervisorPath); err == nil {
			host.SupervisorNetAdminCapable = ok
		}
	}
	DeriveNetworkReadiness(host)
	host.ConsoleAvailable = true
	host.ConsoleMode = "interactive"
	host.PauseResumeAvailable = true
	host.SnapshotAvailable = true
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

// checkUserNamespaces reports whether the current user can create unprivileged
// user namespaces (which pasta needs for Firecracker user-mode networking).
// The live clone probe is authoritative when available; the sysctl reads exist
// to turn a probe failure into the most specific remediation. Sysctls alone
// are not trusted for a positive verdict because policy layers such as
// AppArmor's kernel.apparmor_restrict_unprivileged_userns deny the clone at
// runtime while the classic userns sysctls still look permissive.
func checkUserNamespaces(readFile func(string) ([]byte, error), probeUserns func() error) (bool, string) {
	var probeErr error
	if probeUserns != nil {
		if probeErr = probeUserns(); probeErr == nil {
			return true, ""
		}
	}
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
	if data, err := readFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil && strings.TrimSpace(string(data)) == "1" {
		message := "unprivileged user namespaces are restricted by AppArmor; set kernel.apparmor_restrict_unprivileged_userns=0 or grant the microagent binaries an AppArmor profile that allows userns creation"
		if probeErr != nil {
			message = fmt.Sprintf("unprivileged user namespaces are restricted by AppArmor (%v); set kernel.apparmor_restrict_unprivileged_userns=0 or grant the microagent binaries an AppArmor profile that allows userns creation", probeErr)
		}
		return false, message
	}
	if probeErr != nil {
		return false, fmt.Sprintf("unprivileged user namespace creation failed (%v); a kernel security policy may be blocking CLONE_NEWUSER", probeErr)
	}
	return true, ""
}

func dedupeIssues(issues []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		issue = strings.TrimSpace(issue)
		if issue == "" || seen[issue] {
			continue
		}
		seen[issue] = true
		out = append(out, issue)
	}
	return out
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
		if resp.Host.SupervisorPath == "" {
			resp.Host.SupervisorPath = workspace.FirecrackerSupervisorPath(workspace.Options{SupervisorPath: opts.SupervisorPath})
		}
		resp.Host.ConsoleAvailable = true
		resp.Host.ConsoleMode = "interactive"
		resp.Host.PauseResumeAvailable = true
		resp.Host.SnapshotAvailable = true
	case vmkit.BackendWindowsHyperV:
		if resp.Host.ConsoleMode == "" {
			resp.Host.ConsoleMode = "unsupported"
		}
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

func ResolveFirecrackerSupervisorPath(opts Options) (string, error) {
	name := workspace.FirecrackerSupervisorPath(workspace.Options{SupervisorPath: opts.SupervisorPath})
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			return name, nil
		}
		return "", fmt.Errorf("microagent Firecracker supervisor not found at %s; set MICROAGENT_FIRECRACKER_SUPERVISOR or install microagent-firecracker-supervisor on PATH", name)
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	if exe, err := os.Executable(); err == nil {
		path := DefaultFirecrackerSupervisorPathFromExecutable(exe)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("microagent Firecracker supervisor not found; set MICROAGENT_FIRECRACKER_SUPERVISOR or install microagent-firecracker-supervisor on PATH")
}

func DefaultFirecrackerSupervisorPathFromExecutable(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return filepath.Join(filepath.Dir(executable), "microagent-firecracker-supervisor")
}

func ResolveGuestInitPath(opts Options) (string, error) {
	path := workspace.GuestInitPath(opts.Arch)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, nil
	}
	name := "microagent-guestinit-" + opts.Arch
	return "", fmt.Errorf("microagent guest init not found at %s; set guest init explicitly or install %s under the microagent Homebrew libexec directory", path, name)
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
