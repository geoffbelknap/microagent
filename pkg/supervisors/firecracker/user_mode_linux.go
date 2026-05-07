//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

const (
	userNetworkNamespaceEnv = "MICROAGENT_FIRECRACKER_USER_NETWORK"
	userNetworkPastaPIDEnv  = "MICROAGENT_FIRECRACKER_PASTA_PID_FILE"
)

func insideUserNetworkNamespace() bool {
	return os.Getenv(userNetworkNamespaceEnv) == "1"
}

func startUserNetworkProcess(ctx context.Context, opts Options, req vmkit.Request, detached bool) (vmkit.Response, error) {
	pasta, err := resolveUserNetworkBinary()
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	supervisor, err := os.Executable()
	if err != nil {
		err = fmt.Errorf("resolve firecracker supervisor executable for user networking: %w", err)
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	pidFile := filepath.Join(opts.StateDir, opts.Name, "pasta.pid")
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	args := []string{
		"--config-net",
		"--quiet",
		"--pid", pidFile,
		supervisor,
		"--request-json", string(requestJSON),
	}
	cmd := exec.CommandContext(ctx, pasta, args...)
	cmd.Env = append(os.Environ(),
		userNetworkNamespaceEnv+"=1",
		userNetworkPastaPIDEnv+"="+pidFile,
	)
	if opts.FirecrackerPath != "" {
		cmd.Env = append(cmd.Env, "MICROAGENT_FIRECRACKER="+opts.FirecrackerPath)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
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
