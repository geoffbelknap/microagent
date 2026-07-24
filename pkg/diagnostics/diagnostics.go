package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/confine"
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
	// StatModule reports whether a /sys/module/<name> path exists, used to detect
	// a TPROXY module that is built into the kernel rather than loaded. Defaults
	// to an os.Stat-based check.
	StatModule func(path string) bool
	// Geteuid reports the effective uid, one of the two inputs (with user
	// namespace availability) that resolve the confinement posture the host will
	// apply. Defaults to os.Geteuid.
	Geteuid func() int
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
	case vmkit.BackendLinuxKVM:
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
	if probe.StatModule == nil {
		probe.StatModule = func(path string) bool {
			if probe.Stat == nil {
				return false
			}
			_, err := probe.Stat(path)
			return err == nil
		}
	}
	if probe.Geteuid == nil {
		probe.Geteuid = os.Geteuid
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
	deriveNetworkReadiness(host)
	deriveTProxyModuleReadiness(host, tproxyModuleProbe{readFile: probe.ReadFile, statDir: probe.StatModule})
	deriveConfinementReadiness(host, probe.Geteuid())
	deriveCapabilityDiagnostics(host)
	host.ConsoleAvailable = true
	host.ConsoleMode = "interactive"
	// Snapshot / pause-resume availability derives from the Snapshot L1 result
	// (supervisor + firecracker binary present) rather than a hardcoded true, so
	// doctor reports a verified prerequisite instead of an unconditional claim.
	snapshotReady := capabilityReady(host, vmkit.FeatureCapabilitySnapshot)
	host.PauseResumeAvailable = snapshotReady
	host.SnapshotAvailable = snapshotReady
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

// checkUserNamespaces reports whether the current user can use unprivileged
// user namespaces the way the supervisor jail and pasta do (create one AND
// self-write its uid map). The live probe is authoritative when available; the
// sysctl reads exist to turn a probe failure into the most specific
// remediation. Sysctls alone are not trusted for a positive verdict because
// policy layers such as AppArmor's kernel.apparmor_restrict_unprivileged_userns
// deny the confined child's uid_map self-write at runtime while the classic
// userns sysctls still look permissive.
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
		const remedy = "namespace creation succeeds but the uid_map self-write the supervisor jail and pasta perform is denied, so workspaces cannot boot; run sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 (persist it under /etc/sysctl.d) or install an AppArmor profile that grants userns to unshare and pasta"
		message := "unprivileged user namespaces are restricted by AppArmor (kernel.apparmor_restrict_unprivileged_userns=1): " + remedy
		if probeErr != nil {
			message = fmt.Sprintf("unprivileged user namespaces are restricted by AppArmor (kernel.apparmor_restrict_unprivileged_userns=1; probe: %v): %s", probeErr, remedy)
		}
		return false, message
	}
	if probeErr != nil {
		return false, fmt.Sprintf("unprivileged user namespace creation failed (%v); a kernel security policy may be blocking CLONE_NEWUSER", probeErr)
	}
	return true, ""
}

// deriveConfinementReadiness resolves the VMM-process confinement posture the
// Firecracker host will apply and records it on host, so `doctor` reports a
// value it actually verified instead of a hardcoded default. It reuses the same
// two inputs the supervisor's per-launch resolver uses — the effective uid and
// whether the rootless user-namespace jail is usable (already probed into
// UserNamespacesAvailable) — through the shared pkg/confine decision, so the
// reported mode cannot drift from the enforced one. A non-off result means the
// host supports the mode and will apply it; anything else leaves the fields
// unset for AugmentHostSupport to default to off/inactive.
func deriveConfinementReadiness(host *vmkit.HostSupport, euid int) {
	if host == nil {
		return
	}
	knob := confine.NormalizeKnob(os.Getenv(confine.EnvVar))
	if mode, err := confine.SelectMode(knob, euid, host.UserNamespacesAvailable); err == nil && mode != confine.ModeOff {
		host.ConfinementMode = mode.String()
		host.ConfinementActive = true
	}
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
	if resp.Host.ConfinementMode == "" {
		resp.Host.ConfinementMode = "off"
	}
	// ConfinementActive stays false until a backend enforces confinement.
	switch opts.Backend {
	case vmkit.BackendAppleVF:
		resp.Host.SupervisorPath = nonEmpty(opts.SupervisorPath, "microagent-applevf-supervisor")
		resp.Host.SupervisorAvailable = resp.Error == ""
		resp.Host.ConsoleAvailable = true
		resp.Host.ConsoleMode = "interactive"
	case vmkit.BackendLinuxKVM:
		if resp.Host.SupervisorPath == "" {
			resp.Host.SupervisorPath = workspace.FirecrackerSupervisorPath(workspace.Options{SupervisorPath: opts.SupervisorPath})
		}
		resp.Host.ConsoleAvailable = true
		resp.Host.ConsoleMode = "interactive"
		// Derive from the Snapshot L1 result (CheckFirecracker populated
		// Capabilities); nil/absent means not-ready, which is honest on the
		// error paths that reach the defaulting funnel without a probe.
		snapshotReady := capabilityReady(resp.Host, vmkit.FeatureCapabilitySnapshot)
		resp.Host.PauseResumeAvailable = snapshotReady
		resp.Host.SnapshotAvailable = snapshotReady
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
	return workspace.FirecrackerSupervisorPathFromExecutable(executable)
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
