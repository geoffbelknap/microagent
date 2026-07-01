package vmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

func executableSupervisorEnv(req Request) []string {
	env := os.Environ()
	if req.Identity == nil || req.Identity.Backend != BackendAppleVF {
		return env
	}
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return env
	}
	return append(env, "MICROAGENT_EGRESS_DATAPATH_BIN="+exe)
}
