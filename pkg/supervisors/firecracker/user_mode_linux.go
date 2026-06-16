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
		wrapped := fmt.Errorf("start firecracker user networking with pasta: %s", message)
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
				wrapped := userNetworkStartError(message)
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
