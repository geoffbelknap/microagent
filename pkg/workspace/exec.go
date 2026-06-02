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

// ExecReadyWait bounds how long Exec waits for the guest structured-exec
// service to answer before running the caller's command. A command issued
// immediately after start can otherwise hit the brief window where the host
// exec forward is bound but the in-guest service is not yet listening.
const ExecReadyWait = 5 * time.Second

// execReadinessProbe is the readiness check used by the pre-exec gate. It is a
// package variable so tests can substitute a deterministic probe.
var execReadinessProbe = ExecReadinessSignal

func Exec(ctx context.Context, opts Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
	addr, err := execDialAddr(ctx, opts)
	if err != nil {
		return execprotocol.ExecResult{}, err
	}
	return execclient.New(addr).Exec(ctx, req)
}

// ExecStream runs req against the workspace's structured exec service in
// streaming mode. onChunk, when non-nil, is invoked for each stdout and stderr
// chunk as it arrives. It returns the final ExecResult; in stream mode the
// result's Stdout/Stderr are empty (delivered as chunks) but status, exit code,
// timing, and truncation flags are populated.
func ExecStream(ctx context.Context, opts Options, req execprotocol.ExecRequest, onChunk func(kind execprotocol.ExecStreamKind, data []byte)) (execprotocol.ExecResult, error) {
	addr, err := execDialAddr(ctx, opts)
	if err != nil {
		return execprotocol.ExecResult{}, err
	}
	return execclient.New(addr).ExecStream(ctx, req, onChunk)
}

// execDialAddr validates that the workspace is running with a reachable
// structured exec service, gates on readiness, and returns the dial address.
func execDialAddr(ctx context.Context, opts Options) (string, error) {
	if err := ValidateName(opts.Name); err != nil {
		return "", err
	}
	state, _, err := LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return "", err
	}
	if state == "" {
		return "", WorkspaceNotFoundError{Name: opts.Name}
	}
	if state == vmkit.StatePaused {
		return "", fmt.Errorf("workspace %s is paused; resume it first", opts.Name)
	}
	if state != vmkit.StateRunning {
		return "", fmt.Errorf("workspace %s is not running; structured exec is unavailable in state %s", opts.Name, state)
	}
	runtimeState, err := ReadRuntimeState(opts)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("workspace %s runtime state is missing; structured exec is unavailable", opts.Name)
		}
		return "", err
	}
	if runtimeState.Event.Identity.Backend != vmkit.BackendFirecracker {
		return "", fmt.Errorf("structured exec is unsupported for backend %s", runtimeState.Event.Identity.Backend)
	}
	if runtimeState.Config.ExecPort == 0 {
		return "", fmt.Errorf("workspace %s has no structured exec port in runtime state", opts.Name)
	}
	// Gate on readiness before issuing the command so the post-start window
	// where the guest exec service is not yet listening does not surface as a
	// transient failure. The readiness probe runs an idempotent `true`, so the
	// caller's command is still issued exactly once, after the service answers
	// (or the grace elapses, in which case the real attempt surfaces the error).
	waitForExecReady(ctx, runtimeState, ExecReadyWait)
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(runtimeState.Config.ExecPort))), nil
}

// waitForExecReady polls the structured-exec readiness probe until it reports
// ready, the context is cancelled, or the grace period elapses. It never runs
// the caller's command; it only delays issuing it.
func waitForExecReady(ctx context.Context, state RuntimeState, grace time.Duration) {
	if grace <= 0 {
		return
	}
	deadline := time.Now().Add(grace)
	for {
		if signal, ok := execReadinessProbe(ctx, state, ExecReadyProbeTimeout); ok && signal.Ready {
			return
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
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
