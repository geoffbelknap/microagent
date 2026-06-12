package workspace

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
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

const (
	ExecMaxTransientRetries = 3
	ExecRetryBackoff        = 750 * time.Millisecond
	ExecRetryJitterWindow   = 100 * time.Millisecond
	ExecRetryBudget         = 3 * time.Second
)

// execReadinessProbe is the readiness check used by the pre-exec gate. It is a
// package variable so tests can substitute a deterministic probe.
var (
	execReadinessProbe = ExecReadinessSignal
	execRetryJitter    = randomExecRetryJitter
	execRetrySleep     = sleepExecRetry
	execRetryNow       = time.Now
	execRetrySince     = time.Since
)

type ExecRetryMetadata struct {
	Count     int           `json:"retry_count"`
	WallClock time.Duration `json:"-"`
	Exhausted bool          `json:"retry_exhausted,omitempty"`
}

func (meta ExecRetryMetadata) WallClockMilliseconds() int64 {
	return meta.WallClock.Milliseconds()
}

type ExecRetryExhaustedError struct {
	Retries   int
	WallClock time.Duration
	LastErr   error
}

func (err ExecRetryExhaustedError) Error() string {
	return fmt.Sprintf("structured exec transient connection failure persisted after %d retries over %s retry window: %v", err.Retries, err.WallClock.Round(time.Millisecond), err.LastErr)
}

func (err ExecRetryExhaustedError) Unwrap() error {
	return err.LastErr
}

func Exec(ctx context.Context, opts Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
	result, _, err := ExecWithMetadata(ctx, opts, req)
	return result, err
}

// ExecWithMetadata runs a structured exec like Exec and also reports retry
// metadata describing any transient-failure retries that occurred.
func ExecWithMetadata(ctx context.Context, opts Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, ExecRetryMetadata, error) {
	var meta ExecRetryMetadata
	var retryStart time.Time
	for {
		result, err := execOnce(ctx, opts, req)
		if err == nil || !IsRetryableExecTransient(err) {
			if meta.Count > 0 {
				meta.WallClock = execRetrySince(retryStart)
			}
			return result, meta, err
		}
		if retryStart.IsZero() {
			retryStart = execRetryNow()
		}
		meta.WallClock = execRetrySince(retryStart)
		if meta.Count >= ExecMaxTransientRetries {
			meta.Exhausted = true
			return result, meta, ExecRetryExhaustedError{Retries: meta.Count, WallClock: meta.WallClock, LastErr: err}
		}
		backoff := ExecRetryBackoff + execRetryJitter()
		if backoff < 0 {
			backoff = 0
		}
		if meta.WallClock+backoff > ExecRetryBudget {
			meta.Exhausted = true
			return result, meta, ExecRetryExhaustedError{Retries: meta.Count, WallClock: meta.WallClock, LastErr: err}
		}
		meta.Count++
		if err := execRetrySleep(ctx, backoff); err != nil {
			if meta.Count > 0 {
				meta.WallClock = execRetrySince(retryStart)
			}
			return result, meta, err
		}
	}
}

func execOnce(ctx context.Context, opts Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
	addr, err := execDialAddr(ctx, opts)
	if err != nil {
		return execprotocol.ExecResult{}, err
	}
	return execclient.New(addr).Exec(ctx, req)
}

func IsRetryableExecTransient(err error) bool {
	var unreachable execclient.UnreachableError
	if errors.As(err, &unreachable) {
		return isExecConnectionRefused(unreachable.Err) || isExecConnectionTimeout(unreachable.Err) || isExecConnectionReset(unreachable.Err)
	}
	var protocolErr execclient.ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Op == "decode response" && isExecConnectionReset(protocolErr.Err)
	}
	return false
}

func isExecConnectionRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	text := strings.ToLower(err.Error())
	// Windows reports WSAECONNREFUSED as "actively refused it".
	return strings.Contains(text, "connection refused") || strings.Contains(text, "actively refused")
}

func isExecConnectionReset(err error) bool {
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	text := strings.ToLower(err.Error())
	// Windows reports WSAECONNRESET as "forcibly closed by the remote host".
	return strings.Contains(text, "connection reset by peer") || strings.Contains(text, "forcibly closed by the remote host")
}

func isExecConnectionTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func randomExecRetryJitter() time.Duration {
	windowMS := int64(ExecRetryJitterWindow / time.Millisecond)
	if windowMS <= 0 {
		return 0
	}
	var b [1]byte
	if _, err := crand.Read(b[:]); err != nil {
		return 0
	}
	offset := int64(b[0]) % (2*windowMS + 1)
	return time.Duration(offset-windowMS) * time.Millisecond
}

func sleepExecRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
