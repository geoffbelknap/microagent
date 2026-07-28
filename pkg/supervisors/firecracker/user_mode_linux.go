//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const (
	userNetworkNamespaceEnv = "MICROAGENT_FIRECRACKER_USER_NETWORK"
	userNetworkPastaPIDEnv  = "MICROAGENT_FIRECRACKER_PASTA_PID_FILE"
	userNetworkResidentEnv  = "MICROAGENT_FIRECRACKER_USER_NETWORK_RESIDENT"
)

func insideUserNetworkNamespace() bool {
	return os.Getenv(userNetworkNamespaceEnv) == "1"
}

func startUserNetworkProcess(ctx context.Context, opts Options, req vmkit.Request, detached bool) (vmkit.Response, error) {
	if detached {
		return startDetachedUserNetworkProcess(ctx, opts, req)
	}
	cmd, err := newUserNetworkCommand(ctx, opts, req, false)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	vsockListeners, err := startVsockListeners(opts, req.Config)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	defer vsockListeners.Close()
	// Publish requested host ports for the foreground VM. In detached mode a
	// resident forwarder process does this; a foreground run blocks here for the
	// VM's whole life, so run the forwarder in-process instead. It bridges host
	// TCP to the guest over the firecracker vsock UDS — reachable from the host
	// regardless of pasta's network namespace — and is torn down when the VM
	// exits. Without it, run's -p flag would bind nothing and silently no-op.
	stopForwards := startForegroundPortForwards(opts, req.Config)
	defer stopForwards()
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		wrapped := userNetworkStartErrorWithHint(message)
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
		return failedResponse(req, wrapped.Error()), wrapped
	}
	var resp vmkit.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		wrapped := fmt.Errorf("decode firecracker user networking supervisor response: %w: %s", err, strings.TrimSpace(stdout.String()))
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
		return failedResponse(req, wrapped.Error()), wrapped
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// startForegroundPortForwards binds each requested host port and proxies it to
// the guest over the firecracker vsock UDS, for a foreground (run) VM where no
// detached forwarder companion exists. It mirrors RunPortForwarder's TCP→vsock
// bridging but lives only for the duration of the foreground call. A host port
// that fails to bind is logged and skipped rather than aborting the run. The
// returned stop func closes every listener; the per-connection goroutines then
// unwind on their own. Shell/exec forwards are intentionally excluded — those
// are a persistent-workspace concern, not part of run's published -p ports.
func startForegroundPortForwards(opts Options, config *vmkit.Config) func() {
	if !hasPortForwards(config) {
		return func() {}
	}
	udsPath := vsockSocketPath(opts)
	listeners := make([]net.Listener, 0, len(config.Network.PortForwards))
	for _, forward := range config.Network.PortForwards {
		if forward.Protocol != "" && forward.Protocol != "tcp" {
			continue
		}
		host := strings.TrimSpace(forward.Host)
		if host == "" {
			host = "127.0.0.1"
		}
		addr := net.JoinHostPort(host, strconv.Itoa(int(forward.HostPort)))
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "publish tcp %s: %v\n", addr, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "forward tcp %s to guest vsock port %d\n", addr, forward.GuestPort)
		listeners = append(listeners, listener)
		go servePortForward(listener, udsPath, uint32(forward.GuestPort))
	}
	return func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}
}

func startDetachedUserNetworkProcess(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	innerReq := detachedUserNetworkRequest(req)
	cmd, err := newUserNetworkCommand(ctx, opts, innerReq, true)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	dir := userNetworkStateDir(opts)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdout, err := os.OpenFile(userNetworkStdoutLog(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile(userNetworkStderrLog(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	defer func() { _ = stderr.Close() }()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		wrapped := fmt.Errorf("start firecracker user networking with pasta: %w", err)
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
		return failedResponse(req, wrapped.Error()), wrapped
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				message := strings.TrimSpace(readTextFile(userNetworkStderrLog(opts)))
				if message == "" {
					message = err.Error()
				}
				_ = cmd.Process.Release()
				wrapped := userNetworkStartErrorWithHint(message)
				_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
				return failedResponse(req, wrapped.Error()), wrapped
			}
			state, err := readRuntimeState(opts)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					wrapped := fmt.Errorf("inspect firecracker user networking runtime state: %w", err)
					_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
					return failedResponse(req, wrapped.Error()), wrapped
				}
				continue
			}
			if state.Event.State == vmkit.StateFailed {
				err := fmt.Errorf("%s", state.Error)
				if state.Error == "" {
					err = fmt.Errorf("firecracker user networking failed before reaching running state")
				}
				return failedResponse(req, err.Error()), err
			}
			if state.Event.State == vmkit.StateRunning {
				runtimePID := userNetworkRuntimePID(opts, cmd)
				// Record the namespace init while pasta is still alive to
				// identify it by parentage. This is the only handle on the VM
				// that survives pasta dying — see userNetworkNSInitPIDPath.
				if readPIDFile(userNetworkNSInitPIDPath(opts)) == 0 {
					_ = recordUserNetworkNSInit(opts, runtimePID)
				}
				vsockListenerPID := state.VsockListenerPID
				if hasVsockListeners(req.Config) && vsockListenerPID == 0 {
					pid, err := startVsockListenerProcess(opts)
					if err != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Process.Release()
						cleanupUserNetworkProcess(opts)
						_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
						return failedResponse(req, err.Error()), err
					}
					// The listener process is detached and long-lived, so a
					// startup failure (bad broker secret, unreadable CA, unbound
					// socket) cannot surface as its exit code — wait for it to
					// signal ready, and fail the workspace loudly if it dies
					// first instead of leaving it "running" with dead egress.
					if err := waitForVsockListenersReady(opts, pid, vsockListenerReadyTimeout); err != nil {
						_ = signalProcessGroup(pid, syscall.SIGTERM)
						_ = cmd.Process.Kill()
						_ = cmd.Process.Release()
						cleanupUserNetworkProcess(opts)
						_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
						return failedResponse(req, err.Error()), err
					}
					vsockListenerPID = pid
					runtimeReq := runtimeStateRequest(req, state)
					if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, runtimePID, state.PortForwardPID, vsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
						_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
						_ = cmd.Process.Kill()
						_ = cmd.Process.Release()
						cleanupUserNetworkProcess(opts)
						return vmkit.Response{}, err
					}
					state.VsockListenerPID = vsockListenerPID
				}
				if needsPortForwarder(req.Config) && state.PortForwardPID == 0 {
					portForwardPID, err := startReadyPortForwarderProcessWithManagementPortRetry(ctx, opts, &state.Config, func() error {
						runtimeReq := runtimeStateRequest(req, state)
						return writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, runtimePID, 0, vsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, "")
					})
					if err != nil {
						if vsockListenerPID != 0 {
							_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
						}
						_ = cmd.Process.Kill()
						_ = cmd.Process.Release()
						cleanupUserNetworkProcess(opts)
						_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
						return failedResponse(req, err.Error()), err
					}
					runtimeReq := runtimeStateRequest(req, state)
					if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, runtimePID, portForwardPID, vsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
						_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
						if vsockListenerPID != 0 {
							_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
						}
						_ = cmd.Process.Kill()
						_ = cmd.Process.Release()
						cleanupUserNetworkProcess(opts)
						return vmkit.Response{}, err
					}
					state.PortForwardPID = portForwardPID
				}
				if state.PID != runtimePID {
					runtimeReq := runtimeStateRequest(req, state)
					if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, runtimePID, state.PortForwardPID, vsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Process.Release()
						cleanupUserNetworkProcess(opts)
						return vmkit.Response{}, err
					}
				}
				if err := cmd.Process.Release(); err != nil {
					wrapped := fmt.Errorf("release firecracker user networking process: %w", err)
					_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
					return failedResponse(req, wrapped.Error()), wrapped
				}
				// Leave an event-driven per-VM reaper (as startProcess does for the
				// isolated path): when firecracker exits it reconciles the workspace to
				// its terminal state and reaps companions/network without waiting for a
				// status read or gc sweep. Best-effort.
				if _, err := startDeadmanProcess(opts); err != nil {
					fmt.Fprintf(os.Stderr, "start workspace reaper %s: %v\n", opts.Name, err)
				}
				return eventResponse(req, vmkit.StateRunning, ""), nil
			}
		case <-timer.C:
			_ = cmd.Process.Kill()
			_ = cmd.Process.Release()
			wrapped := userNetworkStartError("firecracker did not reach running state before timeout")
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, wrapped.Error())
			return failedResponse(req, wrapped.Error()), wrapped
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Process.Release()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, ctx.Err().Error())
			return failedResponse(req, ctx.Err().Error()), ctx.Err()
		}
	}
}

func detachedUserNetworkRequest(req vmkit.Request) vmkit.Request {
	innerReq := req
	innerReq.Command = "run"
	return innerReq
}

func newUserNetworkCommand(ctx context.Context, opts Options, req vmkit.Request, resident bool) (*exec.Cmd, error) {
	pasta, err := resolveUserNetworkBinary()
	if err != nil {
		return nil, err
	}
	supervisor, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve firecracker supervisor executable for user networking: %w", err)
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	pidFile := userNetworkPIDPath(opts)
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, pasta, userNetworkArgs(supervisor, pidFile, string(requestJSON), !hostHasRoutableIPv6())...)
	cmd.Env = userNetworkEnv(opts, pidFile, resident)
	return cmd, nil
}

func userNetworkArgs(supervisor, pidFile, requestJSON string, ipv4Only bool) []string {
	args := []string{"--config-net", "--quiet", "--pid", pidFile}
	if ipv4Only {
		// The host has no routable IPv6 (IPv6 disabled, or an IPv4-only network
		// such as a CI runner). Without this, pasta's --config-net fails with
		// "No routable interface for IPv6: IPv6 is disabled".
		args = append(args, "-4")
	}
	args = append(args,
		// "--" stops pasta's option parsing: older passt releases (e.g. the
		// Ubuntu 24.04 package) permute getopt-style and otherwise try to
		// parse the supervisor's --request-json flag as their own.
		"--",
		supervisor,
		"--request-json", requestJSON,
	)
	return args
}

// hostHasRoutableIPv6 reports whether any up, non-loopback interface carries a
// global-unicast IPv6 address (a ULA such as a Tailscale address counts). When
// none does, pasta's --config-net must be restricted to IPv4.
func hostHasRoutableIPv6() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && isRoutableIPv6(ipnet.IP) {
				return true
			}
		}
	}
	return false
}

// isRoutableIPv6 reports whether ip is a global-unicast IPv6 address.
func isRoutableIPv6(ip net.IP) bool {
	return ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast()
}

func userNetworkEnv(opts Options, pidFile string, resident bool) []string {
	env := append(os.Environ(),
		userNetworkNamespaceEnv+"=1",
		userNetworkPastaPIDEnv+"="+pidFile,
	)
	if resident {
		env = append(env, userNetworkResidentEnv+"=1")
	}
	if opts.FirecrackerPath != "" {
		env = append(env, "MICROAGENT_FIRECRACKER="+opts.FirecrackerPath)
	}
	return env
}

func userNetworkStateDir(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name)
}

func userNetworkPIDPath(opts Options) string {
	return filepath.Join(userNetworkStateDir(opts), "pasta.pid")
}

func userNetworkStdoutLog(opts Options) string {
	return filepath.Join(userNetworkStateDir(opts), "user-network.stdout.log")
}

func userNetworkStderrLog(opts Options) string {
	return filepath.Join(userNetworkStateDir(opts), "user-network.stderr.log")
}

func userNetworkStartError(message string) error {
	return fmt.Errorf("start firecracker user networking with pasta: %s", strings.TrimSpace(message))
}

// userNetworkStartErrorWithHint wraps a pasta start failure. When the host's
// unprivileged-user-namespace gates are disabled (or pasta's stderr carries a
// namespace-setup failure signature), it returns a guiding error naming the
// tripped gate with its matching fix, plus the no-network fallback (--network
// isolated), while preserving the original pasta stderr. Otherwise it falls
// back to the plain wrap so unrelated failures are not misattributed to
// userns.
func userNetworkStartErrorWithHint(message string) error {
	trimmed := strings.TrimSpace(message)
	// A permission-denied failure from a pasta the host's SELinux policy
	// confines gets the policy named, not a bare EACCES: the denial is
	// invisible to the user (it lives in the audit log, not pasta's stderr),
	// and the failing path — usually the pid file under the workspace state
	// dir in $HOME — reads like a microagent bug rather than host policy.
	if strings.Contains(trimmed, "ermission denied") {
		if confined, detail := SELinuxConfinedPastaDetail(); confined {
			return fmt.Errorf("firecracker user (rootless) networking could not start: this host's SELinux policy confines pasta (%s) and denied it access — commonly writing its pid file under the workspace state dir in your home directory. Fix: sudo semanage permissive -a pasta_t (reversible with -d; denials are still logged), or use --network isolated when the guest does not need network access. Original error: %s", detail, trimmed)
		}
	}
	enabled, reason := unprivilegedUserNSEnabled()
	if enabled && !pastaStderrIndicatesUserNSFailure(trimmed) {
		return userNetworkStartError(trimmed)
	}
	cause := reason
	fix := "enable unprivileged user namespaces (check kernel.unprivileged_userns_clone, user.max_user_namespaces, and kernel.apparmor_restrict_unprivileged_userns)"
	switch reason {
	case userNSReasonCloneDisabled:
		fix = "enable them: sudo sysctl -w kernel.unprivileged_userns_clone=1"
	case userNSReasonMaxDisabled:
		fix = "raise the limit: sudo sysctl -w user.max_user_namespaces=32768"
	case userNSReasonAppArmor:
		fix = "allow the uid_map self-write: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0, or install an AppArmor profile that grants userns to pasta and unshare"
	default:
		cause = "a kernel security policy denied user namespace setup"
	}
	return fmt.Errorf("firecracker user (rootless) networking needs unprivileged user namespaces with a self-written uid map, which this host blocks (%s). Fix: %s. Or use --network isolated when the guest does not need network access. Original error: %s", cause, fix, trimmed)
}

// procRoot is concatenated in front of the absolute "/proc/sys/..." gate paths
// to locate the user-namespace sysctl files. It is "" in production (so the
// real /proc/sys is read) and is overridden in tests with a tempdir so the proc
// reads are table-testable against synthetic files.
var procRoot = ""

const (
	procUnprivilegedUserNSClone = "/proc/sys/kernel/unprivileged_userns_clone"
	procMaxUserNamespaces       = "/proc/sys/user/max_user_namespaces"
	procAppArmorRestrictUserNS  = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
)

// Reasons userNSDecision returns for a disabled gate, matched by callers that
// tailor remediation to the specific gate.
const (
	userNSReasonCloneDisabled = "kernel.unprivileged_userns_clone=0"
	userNSReasonMaxDisabled   = "user.max_user_namespaces=0"
	userNSReasonAppArmor      = "kernel.apparmor_restrict_unprivileged_userns=1"
)

// unprivilegedUserNSEnabled probes the known kernel gates that govern whether
// an unprivileged process can use a new user namespace the way microagent
// needs to (create it and self-write its uid map, as pasta and the rootless
// jail do). It returns whether the gates allow it and, when disabled, a
// human-readable reason naming the gate that was tripped. Hardened hosts
// disable these with kernel.unprivileged_userns_clone=0 or
// user.max_user_namespaces=0; stock Ubuntu 24.04 restricts them with
// kernel.apparmor_restrict_unprivileged_userns=1, which lets the namespace be
// created but denies the confined child's own uid_map write.
func unprivilegedUserNSEnabled() (bool, string) {
	clone, clonePresent := readSysctlGate(procRoot + procUnprivilegedUserNSClone)
	maxNS, maxNSPresent := readSysctlGate(procRoot + procMaxUserNamespaces)
	apparmor, apparmorPresent := readSysctlGate(procRoot + procAppArmorRestrictUserNS)
	return userNSDecision(clone, clonePresent, maxNS, maxNSPresent, apparmor, apparmorPresent)
}

// readSysctlGate reads a single-value sysctl-style proc file, returning its
// trimmed contents and whether the file was present and readable. A missing
// file (present=false) means the gate does not apply on this kernel.
func readSysctlGate(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// userNSDecision is the pure decision over the known gates, split out so it
// can be table-tested against synthetic proc contents. A gate that is present
// and reads its disabling value ("0" for the classic gates, "1" for the
// AppArmor restriction; empty, which the kernel never writes, signals a
// disabled/unreadable value) trips; an absent gate does not apply.
func userNSDecision(clone string, clonePresent bool, maxNS string, maxNSPresent bool, apparmor string, apparmorPresent bool) (bool, string) {
	if clonePresent && (clone == "0" || clone == "") {
		return false, userNSReasonCloneDisabled
	}
	if maxNSPresent && (maxNS == "0" || maxNS == "") {
		return false, userNSReasonMaxDisabled
	}
	if apparmorPresent && apparmor == "1" {
		return false, userNSReasonAppArmor
	}
	return true, ""
}

// pastaStderrIndicatesUserNSFailure recognizes the stderr signatures pasta (and
// the supervisor it spawns) emit when new-user-namespace creation is denied,
// independent of the sysctl probe, so the guiding error fires even when the
// gate read is inconclusive. Matching is case-insensitive.
func pastaStderrIndicatesUserNSFailure(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, sig := range []string{
		"clone_newuser",
		"unshare",
		"clone",
		"operation not permitted",
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func readTextFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func resolveUserNetworkBinary() (string, error) {
	if path, err := exec.LookPath("pasta"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("slirp4netns"); err == nil {
		return "", fmt.Errorf("firecracker user networking requires pasta; slirp4netns was found at %s but is not supported for Firecracker TAP mode yet; install passt (for example, apt install passt)", path)
	}
	return "", fmt.Errorf("firecracker user networking requires pasta on PATH; install passt (for example, apt install passt) or use --network isolated when the guest does not need network access")
}

func userNetworkPastaPID() int {
	path := strings.TrimSpace(os.Getenv(userNetworkPastaPIDEnv))
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func userNetworkRuntimePID(opts Options, cmd *exec.Cmd) int {
	if pid := readPIDFile(userNetworkPIDPath(opts)); pid > 0 {
		return pid
	}
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Pid
	}
	return 0
}

// userNetworkNSInitPIDPath records the host pid of the process pasta spawns
// inside the new namespaces — the supervisor child that goes on to launch
// firecracker, and PID 1 of the workspace's pid namespace.
//
// This is recorded because pasta's pid is NOT a handle on the VM. pasta runs on
// the host and only serves the network; its child anchors the pid namespace. If
// pasta dies alone (OOM kill, crash, an operator clearing what looks like a
// stray network helper) the kernel tears down the net namespace but the child
// and firecracker keep running, reparented to init. With only pasta's pid
// recorded, the workspace then looks dead while a guest is still executing:
// stop/halt/kill/quarantine signal a pid that no longer exists and report
// success, and gc reaps the record without killing anything. The VM survives
// `delete` and is findable only with ps.
//
// Killing the ns-init is the whole cascade: the kernel SIGKILLs every process
// in a pid namespace whose init exits, firecracker included.
func userNetworkNSInitPIDPath(opts Options) string {
	return filepath.Join(userNetworkStateDir(opts), "nsinit.pid")
}

// recordUserNetworkNSInit finds pasta's namespace-init child and records its
// host pid. It must run while pasta is still alive: once pasta exits the child
// reparents to init and its parentage no longer identifies it.
//
// Best-effort by design — a workspace that starts fine must not fail because
// this lookup lost a race. Without the record the behavior is only what it was
// before, so a miss degrades rather than breaks.
func recordUserNetworkNSInit(opts Options, pastaPID int) int {
	if pastaPID <= 0 {
		return 0
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if pid := findNamespaceInitChild(pastaPID); pid > 0 {
			if err := os.WriteFile(userNetworkNSInitPIDPath(opts), []byte(strconv.Itoa(pid)), 0o600); err != nil {
				return 0
			}
			return pid
		}
		if time.Now().After(deadline) {
			return 0
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// findNamespaceInitChild returns the child of parentPID that is PID 1 of a
// nested pid namespace. Requiring nested-init status (not merely "a child")
// keeps this from picking up any helper pasta might fork on the host.
func findNamespaceInitChild(parentPID int) int {
	children := linuxProcessChildrenByParent()
	for _, pid := range children[parentPID] {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		if processIsNestedNamespaceInit(status) {
			return pid
		}
	}
	return 0
}

// processIsNestedNamespaceInit reports whether /proc/<pid>/status describes a
// process that is PID 1 inside a pid namespace nested below ours. The NSpid
// line lists the pid in each namespace from ours inward, so more than one entry
// with a trailing 1 means exactly that.
func processIsNestedNamespaceInit(status []byte) bool {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "NSpid:"))
		return len(fields) > 1 && fields[len(fields)-1] == "1"
	}
	return false
}

// cleanupUserNetworkProcess tears down the user-mode network AND the VM behind
// it. The ns-init goes first: killing it makes the kernel SIGKILL everything in
// the workspace's pid namespace, firecracker included. Signaling pasta alone
// leaves the guest running (see userNetworkNSInitPIDPath).
func cleanupUserNetworkProcess(opts Options) {
	cleanupUserNetworkNSInit(opts)
	pid := readPIDFile(userNetworkPIDPath(opts))
	if pid == 0 {
		return
	}
	active, err := processActive(pid)
	if err == nil && active {
		_ = signalProcessGroup(pid, syscall.SIGTERM)
		_ = waitForProcessExit(context.Background(), pid, 2*time.Second)
	}
	_ = os.Remove(userNetworkPIDPath(opts))
}

// cleanupUserNetworkNSInit terminates the recorded namespace init, escalating to
// SIGKILL if it does not exit. The recorded pid is verified to still carry this
// workspace's identity first: pids are recycled, and this one may have been
// recorded long before an unrelated process inherited the number.
func cleanupUserNetworkNSInit(opts Options) {
	path := userNetworkNSInitPIDPath(opts)
	pid := readPIDFile(path)
	if pid == 0 {
		return
	}
	defer func() { _ = os.Remove(path) }()
	active, err := processActive(pid)
	if err != nil || !active {
		return
	}
	if !processReferencesWorkspace(pid, opts) {
		return
	}
	_ = signalProcessGroup(pid, syscall.SIGTERM)
	if err := waitForProcessExit(context.Background(), pid, 2*time.Second); err != nil {
		_ = signalProcessGroup(pid, syscall.SIGKILL)
		_ = waitForProcessExit(context.Background(), pid, 2*time.Second)
	}
}

func userNetworkProcessActive(opts Options) (bool, error) {
	pid := readPIDFile(userNetworkPIDPath(opts))
	if pid == 0 {
		return false, nil
	}
	return processActive(pid)
}

func readPIDFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func attachUserNetworkPID(devices []transientNetworkDevice) []transientNetworkDevice {
	pid := userNetworkPastaPID()
	if pid == 0 {
		return devices
	}
	for i := range devices {
		if devices[i].PID == 0 {
			devices[i].PID = pid
		}
	}
	return devices
}
