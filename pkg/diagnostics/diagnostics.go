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
	firecracker "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

type Options struct {
	Backend        string
	Arch           string
	SupervisorPath string
	// StateDir is where workspace boots will write; the pasta start probe
	// runs against it because that is where a confined pasta actually fails
	// (the pid file under $HOME). Empty skips the start probe.
	StateDir string
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
	// ProbePastaStart runs pasta against the state dir the way a boot will
	// (pid file + trivial command); nil selects the live probe. See
	// defaultPastaStartProbe for why LookPath alone is not the capability.
	ProbePastaStart func(pastaPath, stateDir string) error
	// SELinuxConfinedPasta explains a failed pasta probe on hosts whose
	// policy confines pasta_t; consulted only after a real failure.
	SELinuxConfinedPasta func() (bool, string)
	// StatModule reports whether a /sys/module/<name> path exists, used to detect
	// a TPROXY module that is built into the kernel rather than loaded. Defaults
	// to an os.Stat-based check.
	StatModule func(path string) bool
	// ProbeTProxy is the attempt-based TPROXY check: install the real steering
	// rule in a scratch user+net namespace via the supervisor's
	// --tproxy-selfcheck. Defaults to the live probe; ran=false falls back to
	// the module heuristic.
	ProbeTProxy func(supervisorPath string) (ran bool, err error)
	// Geteuid reports the effective uid, one of the two inputs (with user
	// namespace availability) that resolve the confinement posture the host will
	// apply. Defaults to os.Geteuid.
	Geteuid func() int
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
			Stat:                   os.Stat,
			BinaryVersion:          FirecrackerVersion,
			ReadBinaryCapabilities: BinaryHasNetAdmin,
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
	if probe.ResolveSupervisor == nil {
		probe.ResolveSupervisor = ResolveFirecrackerSupervisorPath
	}
	if probe.ResolveBinary == nil {
		// Anchor the packaged VMM lookup on the supervisor that will launch it,
		// not on this process. See ResolveFirecrackerPathFor.
		probe.ResolveBinary = func() (string, error) {
			supervisorPath, err := probe.ResolveSupervisor(opts)
			if err != nil {
				supervisorPath = ""
			}
			return ResolveFirecrackerPathFor(supervisorPath)
		}
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
	if probe.ProbeTProxy == nil {
		probe.ProbeTProxy = defaultTProxyProbe
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
	}
	// VirtualizationSupported is an independent fact, not a /dev/kvm relabel: KVM
	// being available proves hardware virtualization works, and the CPU
	// advertising vmx/svm proves the capability even when /dev/kvm is absent. That
	// lets doctor tell "CPU can't virtualize" apart from "KVM isn't set up".
	host.VirtualizationSupported = host.KVMAvailable || cpuHasVirtualizationFlags(probe.ReadFile)
	if !host.KVMAvailable {
		if host.VirtualizationSupported {
			issues = append(issues, "/dev/kvm is not available (the CPU supports virtualization; load the kvm module or check permissions on /dev/kvm)")
		} else {
			issues = append(issues, "/dev/kvm is not available")
		}
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
		if probe.ProbePastaStart == nil {
			probe.ProbePastaStart = defaultPastaStartProbe
		}
		if probe.SELinuxConfinedPasta == nil {
			probe.SELinuxConfinedPasta = defaultSELinuxConfinedPasta
		}
		if opts.StateDir != "" {
			if perr := probe.ProbePastaStart(path, opts.StateDir); perr != nil {
				host.UserNetworkingAvailable = false
				issues = append(issues, pastaStartIssue(perr, probe.SELinuxConfinedPasta))
			}
		}
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
	deriveTProxyModuleReadiness(host, tproxyModuleProbe{probeSupport: probe.ProbeTProxy, readFile: probe.ReadFile, statDir: probe.StatModule})
	deriveConfinementReadiness(host, probe.Geteuid())
	deriveCapabilityDiagnostics(host)
	// Console availability derives from its L1 result (supervisor present) rather
	// than a hardcoded true, so doctor reports a verified prerequisite.
	host.ConsoleAvailable = capabilityReady(host, vmkit.FeatureCapabilityConsole)
	if host.ConsoleAvailable {
		host.ConsoleMode = "interactive"
	}
	// Snapshot availability is the conjunction of its operation-level L1
	// results. This preserves the aggregate compatibility field without hiding
	// a partial implementation.
	host.PauseResumeAvailable = capabilityReady(host, vmkit.FeatureCapabilityPauseResume)
	host.SnapshotCreateAvailable = capabilityReady(host, vmkit.FeatureCapabilitySnapshotCreate)
	host.SnapshotAvailable = host.PauseResumeAvailable &&
		host.SnapshotCreateAvailable &&
		capabilityReady(host, vmkit.FeatureCapabilitySnapshotRestore) &&
		capabilityReady(host, vmkit.FeatureCapabilitySnapshotFork)
	resp := vmkit.Response{
		OK:      len(issues) == 0,
		Backend: opts.Backend,
		Host:    host,
		Kernel:  kernel.Support(opts.Backend, opts.Arch),
	}
	if len(issues) > 0 {
		resp.Error = strings.Join(issues, "; ")
		resp.Verdict = DeriveVerdict(&resp)
		return resp, fmt.Errorf("%s", resp.Error)
	}
	resp.Verdict = DeriveVerdict(&resp)
	return resp, nil
}

// pastaStartIssue turns a failed pasta start probe into a diagnosis. A
// permission-denied failure on a host whose SELinux policy confines pasta_t
// names the policy and its fix; anything else reports the probe failure
// plainly so unrelated breakage is never misattributed to SELinux.
func pastaStartIssue(err error, confinedPasta func() (bool, string)) string {
	if strings.Contains(err.Error(), "ermission denied") && confinedPasta != nil {
		if confined, detail := confinedPasta(); confined {
			return fmt.Sprintf("pasta cannot start: this host's SELinux policy confines pasta (%s) and denies it access to the workspace state dir; user-networking boots will fail the same way. Fix: sudo semanage permissive -a pasta_t (reversible with -d; denials are still logged), or use --network isolated when the guest does not need network access (probe: %v)", detail, err)
		}
	}
	return fmt.Sprintf("pasta failed a start probe (%v); user-networking boots will fail the same way", err)
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
		// The Apple VF supervisor reports Virtualization.framework facts, but
		// guest init is a Go-side companion resolved by the workspace library.
		// Probe the exact path a boot will use instead of leaving the shared core
		// verdict to interpret the supervisor's absent field as unavailable.
		if path, err := ResolveGuestInitPath(opts); err == nil {
			resp.Host.GuestInitPath = path
			resp.Host.GuestInitAvailable = true
		} else {
			resp.Host.GuestInitPath = workspace.GuestInitPath(opts.Arch)
			resp.Host.GuestInitAvailable = false
			resp.OK = false
			if resp.Error == "" {
				resp.Error = err.Error()
			} else if !strings.Contains(resp.Error, err.Error()) {
				resp.Error += "; " + err.Error()
			}
		}
		// apple-vf capability diagnostics derive from the supervisor host
		// response facts; console availability follows its L1 result instead of a
		// hardcoded true.
		deriveCapabilityDiagnostics(resp.Host)
		resp.Host.ConsoleAvailable = capabilityReady(resp.Host, vmkit.FeatureCapabilityConsole)
		if resp.Host.ConsoleAvailable {
			resp.Host.ConsoleMode = "interactive"
		}
		// Re-derive the legacy availability booleans from the capability rows,
		// like the linux-kvm branch below: the supervisor's raw facts fed the L1
		// checks, and the payload must not contradict its own capability rows
		// (e.g. pauseResumeAvailable true while capabilities[pause-resume] is
		// not ready because the supervisor probe failed).
		resp.Host.PauseResumeAvailable = capabilityReady(resp.Host, vmkit.FeatureCapabilityPauseResume)
		resp.Host.SnapshotCreateAvailable = capabilityReady(resp.Host, vmkit.FeatureCapabilitySnapshotCreate)
		resp.Host.SnapshotAvailable = resp.Host.PauseResumeAvailable &&
			resp.Host.SnapshotCreateAvailable &&
			capabilityReady(resp.Host, vmkit.FeatureCapabilitySnapshotRestore) &&
			capabilityReady(resp.Host, vmkit.FeatureCapabilitySnapshotFork)
	case vmkit.BackendLinuxKVM:
		if resp.Host.SupervisorPath == "" {
			resp.Host.SupervisorPath = workspace.FirecrackerSupervisorPath(workspace.Options{SupervisorPath: opts.SupervisorPath})
		}
		// Console and snapshot availability derive from their operation-level L1 results
		// (CheckFirecracker populated Capabilities); nil/absent means not-ready,
		// which is honest on the error paths that reach the defaulting funnel
		// without a probe.
		resp.Host.ConsoleAvailable = capabilityReady(resp.Host, vmkit.FeatureCapabilityConsole)
		if resp.Host.ConsoleAvailable {
			resp.Host.ConsoleMode = "interactive"
		}
		resp.Host.PauseResumeAvailable = capabilityReady(resp.Host, vmkit.FeatureCapabilityPauseResume)
		resp.Host.SnapshotCreateAvailable = capabilityReady(resp.Host, vmkit.FeatureCapabilitySnapshotCreate)
		resp.Host.SnapshotAvailable = resp.Host.PauseResumeAvailable &&
			resp.Host.SnapshotCreateAvailable &&
			capabilityReady(resp.Host, vmkit.FeatureCapabilitySnapshotRestore) &&
			capabilityReady(resp.Host, vmkit.FeatureCapabilitySnapshotFork)
	}
	resp.Verdict = DeriveVerdict(resp)
}

// ResolveFirecrackerPath resolves the VMM without a supervisor to anchor the
// packaged lookup against. Prefer ResolveFirecrackerPathFor: the supervisor is
// the process that actually launches Firecracker, so it is the correct anchor.
func ResolveFirecrackerPath() (string, error) {
	return ResolveFirecrackerPathFor("")
}

// ResolveFirecrackerPathFor resolves the VMM the way the boot path will, given
// the supervisor that will launch it.
//
// The packaged layout puts the VMM at ../libexec/firecracker relative to the
// binary that runs it, and that binary is the supervisor, not this process. The
// probe used to anchor on os.Executable() — the CLI — so a CLI and supervisor
// installed in different trees disagreed: `run` booted fine through the
// supervisor's own tree while `doctor` reported the VMM missing and marked
// pause/resume and all three snapshot capabilities unavailable.
//
// This is the supervisor's own resolver, handed the supervisor's path as its
// anchor — not a reimplementation. The probe and the boot path drifting is
// how doctor lied in both directions: anchored on the CLI it reported split
// layouts broken (false red), and keeping the CLI tree as a last resort
// reported layouts the supervisor cannot see as healthy (false green). The
// only divergence left is deliberate: with no supervisor path at all, this
// executable's own tree anchors the lookup, because there is no boot path to
// mirror.
func ResolveFirecrackerPathFor(supervisorPath string) (string, error) {
	anchor := strings.TrimSpace(supervisorPath)
	if anchor == "" {
		if exe, err := os.Executable(); err == nil {
			anchor = exe
		}
	}
	return firecracker.ResolveBinaryFrom(anchor)
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
