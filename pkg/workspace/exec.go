package workspace

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

const ExecReadyProbeTimeout = 2 * time.Second

func Exec(ctx context.Context, opts Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
	if err := ValidateName(opts.Name); err != nil {
		return execprotocol.ExecResult{}, err
	}
	state, _, err := LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return execprotocol.ExecResult{}, err
	}
	if state == "" {
		return execprotocol.ExecResult{}, WorkspaceNotFoundError{Name: opts.Name}
	}
	if state != vmkit.StateRunning {
		return execprotocol.ExecResult{}, fmt.Errorf("workspace %s is not running; structured exec is unavailable in state %s", opts.Name, state)
	}
	runtimeState, err := ReadRuntimeState(opts)
	if err != nil {
		if os.IsNotExist(err) {
			return execprotocol.ExecResult{}, fmt.Errorf("workspace %s runtime state is missing; structured exec is unavailable", opts.Name)
		}
		return execprotocol.ExecResult{}, err
	}
	if runtimeState.Event.Identity.Backend != vmkit.BackendFirecracker {
		return execprotocol.ExecResult{}, fmt.Errorf("structured exec is unsupported for backend %s", runtimeState.Event.Identity.Backend)
	}
	if runtimeState.Config.ExecPort == 0 {
		return execprotocol.ExecResult{}, fmt.Errorf("workspace %s has no structured exec port in runtime state", opts.Name)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(runtimeState.Config.ExecPort)))
	return execclient.New(addr).Exec(ctx, req)
}

func ExecReadinessSignal(ctx context.Context, state RuntimeState, probeTimeout time.Duration) (vmkit.ReadinessSignal, bool) {
	if state.Event.State != vmkit.StateRunning {
		return vmkit.ReadinessSignal{}, false
	}
	observedAt := time.Now().UTC()
	if state.Event.Identity.Backend != vmkit.BackendFirecracker {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("structured exec unsupported for backend %s", state.Event.Identity.Backend),
			Error:      "structured exec unsupported for backend",
		}, true
	}
	if state.Config.ExecPort == 0 {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     "structured exec port is not configured",
		}, true
	}
	if probeTimeout <= 0 {
		probeTimeout = ExecReadyProbeTimeout
	}
	req := execprotocol.NewExecRequest([]string{"true"})
	req.TimeoutMS = int64(probeTimeout / time.Millisecond)
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(state.Config.ExecPort)))
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	result, err := execclient.New(target).Exec(probeCtx, req)
	cancel()
	elapsed := time.Since(start)
	if err != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service unreachable at %s after %s: %v", target, elapsed.Round(time.Millisecond), err),
		}, true
	}
	if result.Error != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service returned %s: %s", result.Error.Code, result.Error.Message),
			Error:      result.Error.Error(),
		}, true
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		exit := "nil"
		if result.ExitCode != nil {
			exit = strconv.Itoa(*result.ExitCode)
		}
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec probe command failed unexpectedly: status=%s exit_code=%s", result.Status, exit),
		}, true
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: &observedAt,
		Detail:     fmt.Sprintf("exec service round-trip ready at %s in %s", target, elapsed.Round(time.Millisecond)),
	}, true
}
