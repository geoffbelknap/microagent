//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	userNetworkNamespaceEnv         = "MICROAGENT_FIRECRACKER_USER_NETWORK"
	userNetworkPastaPIDEnv          = "MICROAGENT_FIRECRACKER_PASTA_PID_FILE"
	userNetworkDisableRunTimeoutEnv = "MICROAGENT_FIRECRACKER_DISABLE_RUN_TIMEOUT"
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

func newUserNetworkCommand(ctx context.Context, opts Options, req vmkit.Request, disableRunTimeout bool) (*exec.Cmd, error) {
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
	cmd := exec.CommandContext(ctx, pasta, userNetworkArgs(supervisor, pidFile, string(requestJSON))...)
	cmd.Env = userNetworkEnv(opts, pidFile, disableRunTimeout)
	return cmd, nil
}

func userNetworkArgs(supervisor, pidFile, requestJSON string) []string {
	return []string{
		"--config-net",
		"--quiet",
		"--pid", pidFile,
		// "--" stops pasta's option parsing: older passt releases (e.g. the
		// Ubuntu 24.04 package) permute getopt-style and otherwise try to
		// parse the supervisor's --request-json flag as their own.
		"--",
		supervisor,
		"--request-json", requestJSON,
	}
}

func userNetworkEnv(opts Options, pidFile string, disableRunTimeout bool) []string {
	env := append(os.Environ(),
		userNetworkNamespaceEnv+"=1",
		userNetworkPastaPIDEnv+"="+pidFile,
	)
	if disableRunTimeout {
		env = append(env, userNetworkDisableRunTimeoutEnv+"=1")
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
// namespace-creation failure signature), it returns a guiding error that points
// the operator at the privileged NAT alternative (--network nat) while
// preserving the original pasta stderr. Otherwise it falls back to the plain
// wrap so unrelated failures are not misattributed to userns.
func userNetworkStartErrorWithHint(message string) error {
	trimmed := strings.TrimSpace(message)
	enabled, _ := unprivilegedUserNSEnabled()
	if !enabled || pastaStderrIndicatesUserNSFailure(trimmed) {
		return fmt.Errorf("firecracker user (rootless) networking needs unprivileged user namespaces, which appear to be disabled on this host (kernel.unprivileged_userns_clone=0 or user.max_user_namespaces=0). Enable them (sudo sysctl -w kernel.unprivileged_userns_clone=1), or run privileged NAT networking instead: --network nat (requires CAP_NET_ADMIN; run as root). Original error: %s", trimmed)
	}
	return userNetworkStartError(trimmed)
}

// procRoot is concatenated in front of the absolute "/proc/sys/..." gate paths
// to locate the user-namespace sysctl files. It is "" in production (so the
// real /proc/sys is read) and is overridden in tests with a tempdir so the proc
// reads are table-testable against synthetic files.
var procRoot = ""

const (
	procUnprivilegedUserNSClone = "/proc/sys/kernel/unprivileged_userns_clone"
	procMaxUserNamespaces       = "/proc/sys/user/max_user_namespaces"
)

// unprivilegedUserNSEnabled probes the known kernel gates that govern whether
// an unprivileged process may create a new user namespace. It returns whether
// the gates allow it and, when disabled, a human-readable reason naming the
// gate that was tripped. Hardened hosts disable these with
// kernel.unprivileged_userns_clone=0 or user.max_user_namespaces=0.
func unprivilegedUserNSEnabled() (bool, string) {
	clone, clonePresent := readSysctlGate(procRoot + procUnprivilegedUserNSClone)
	maxNS, maxNSPresent := readSysctlGate(procRoot + procMaxUserNamespaces)
	return userNSDecision(clone, clonePresent, maxNS, maxNSPresent)
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

// userNSDecision is the pure decision over the two known gates, split out so it
// can be table-tested against synthetic proc contents. A gate that is present
// and reads "0" (or empty, which the kernel never writes but which signals a
// disabled/unreadable value) is treated as disabled; an absent gate does not
// apply.
func userNSDecision(clone string, clonePresent bool, maxNS string, maxNSPresent bool) (bool, string) {
	if clonePresent && (clone == "0" || clone == "") {
		return false, "kernel.unprivileged_userns_clone=0"
	}
	if maxNSPresent && (maxNS == "0" || maxNS == "") {
		return false, "user.max_user_namespaces=0"
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
	return "", fmt.Errorf("firecracker user networking requires pasta on PATH; install passt (for example, apt install passt) or use --network nat with supervisor capabilities")
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

func cleanupUserNetworkProcess(opts Options) {
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
