package vmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type Supervisor interface {
	Do(ctx context.Context, req Request) (Response, error)
}

type ExecutableSupervisor struct {
	Path string
}

// SupervisorClient is kept as a compatibility alias for the executable
// supervisor implementation.
type SupervisorClient = ExecutableSupervisor

func (c ExecutableSupervisor) Do(ctx context.Context, req Request) (Response, error) {
	NormalizeConfig(req.Config)
	if err := ValidateRequest(req); err != nil {
		return Response{}, err
	}
	path := c.Path
	if path == "" {
		path = "microagent-applevf-supervisor"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	cmd := exec.CommandContext(ctx, path)
	if req.Command == "snapshot" {
		// A snapshot pauses the VM mid-operation and must resume it if interrupted.
		// On ctx cancellation send SIGTERM (catchable) with a grace window instead
		// of the default immediate SIGKILL, so the supervisor can run its resume
		// cleanup before exiting rather than being killed with the VM left frozen.
		// Scoped to snapshot on purpose: other commands — notably start, which
		// daemonizes long-lived processes and relies on the process's default
		// signal disposition — must keep the default cancellation behavior.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 10 * time.Second
	}
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = executableSupervisorEnv(req)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	var resp Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		if runErr != nil {
			return Response{}, fmt.Errorf("%w: %s", runErr, bytes.TrimSpace(stderr.Bytes()))
		}
		return Response{}, err
	}
	if runErr != nil {
		if resp.Error != "" {
			return resp, fmt.Errorf("%w: %s", runErr, resp.Error)
		}
		return resp, runErr
	}
	return resp, nil
}

// EgressDatapathBinEnv names the environment variable that points the apple-vf
// supervisor at the microagent binary hosting the `--egress-datapath`
// subprocess for host-fd mediated egress.
const EgressDatapathBinEnv = "MICROAGENT_EGRESS_DATAPATH_BIN"

// ResolveEgressDatapathBin resolves the egress datapath binary the way the
// apple-vf boot paths do: a pre-set MICROAGENT_EGRESS_DATAPATH_BIN wins
// (embedders of this library — go test, custom hosts — are not the microagent
// CLI, so os.Executable would point the supervisor at a binary with no
// --egress-datapath mode); otherwise the current executable. Empty means
// nothing resolved and the supervisor would refuse mediated-egress boots.
func ResolveEgressDatapathBin() string {
	if p := strings.TrimSpace(os.Getenv(EgressDatapathBinEnv)); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		return exe
	}
	return ""
}

func executableSupervisorEnv(req Request) []string {
	env := os.Environ()
	if req.Identity == nil || req.Identity.Backend != BackendAppleVF {
		return env
	}
	// A pre-set MICROAGENT_EGRESS_DATAPATH_BIN wins and is already in the
	// inherited environment; only the os.Executable fallback needs appending.
	if strings.TrimSpace(os.Getenv(EgressDatapathBinEnv)) != "" {
		return env
	}
	bin := ResolveEgressDatapathBin()
	if bin == "" {
		return env
	}
	return append(env, EgressDatapathBinEnv+"="+bin)
}
