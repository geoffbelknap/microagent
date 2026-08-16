package workspace

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
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
	emitWorkspaceProgress(opts, progressOperationExec, "Execute command", "exec_connect", "connecting to structured exec service")
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
		emitWorkspaceProgress(opts, progressOperationExec, "Execute command", "exec_retry", fmt.Sprintf("retrying transient connection failure (%d/%d)", meta.Count, ExecMaxTransientRetries))
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
	emitWorkspaceProgress(opts, progressOperationExec, "Execute command", "exec_execute", "command is running")
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
	emitWorkspaceProgress(opts, progressOperationExec, "Execute command", "exec_connect", "connecting to structured exec service")
	addr, err := execDialAddr(ctx, opts)
	if err != nil {
		return execprotocol.ExecResult{}, err
	}
	emitWorkspaceProgress(opts, progressOperationExec, "Execute command", "exec_execute", "command is running")
	firstOutput := true
	return execclient.New(addr).ExecStream(ctx, req, func(kind execprotocol.ExecStreamKind, data []byte) {
		if firstOutput && len(data) > 0 {
			firstOutput = false
			emitWorkspaceProgress(opts, progressOperationExec, "Execute command", "exec_output", "command output started")
		}
		if onChunk != nil {
			onChunk(kind, data)
		}
	})
}

// RequestShutdown asks guest PID 1 to begin the graceful power-off path. The
// guest, not the host, selects the workload signal so an OCI StopSignal can be
// honored. This control request deliberately does not renew workspace activity.
func RequestShutdown(ctx context.Context, opts Options) error {
	addr, err := execDialAddrWithActivity(ctx, opts, false)
	if err != nil {
		return err
	}
	result, err := execclient.New(addr).Exec(ctx, execprotocol.NewShutdownRequest())
	if err != nil {
		return err
	}
	if result.Error != nil {
		if result.Error.Code == "invalid_request" || result.Error.Code == "unsupported_protocol_version" {
			return fmt.Errorf("workspace guest init may not support graceful shutdown control; recreate the workspace with the current guest init, or use microagent kill for this running instance: %w", result.Error)
		}
		return fmt.Errorf("guest rejected graceful shutdown: %w", result.Error)
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		exitCode := "nil"
		if result.ExitCode != nil {
			exitCode = strconv.Itoa(*result.ExitCode)
		}
		return fmt.Errorf("guest did not accept graceful shutdown: status=%s exit_code=%s", result.Status, exitCode)
	}
	return nil
}

// MarkActivity records that the workspace was just genuinely used (an exec or
// connect) by bumping its activity marker file's mtime. The deadman watcher and
// gc sweep read this to measure idleness, so each real use renews a declared
// --ttl lease. Only real user use calls this — internal readiness probes (the
// forwarder's liveness ticker) deliberately do not, so they cannot keep an
// abandoned VM alive. Keep the "activity" filename in sync with the firecracker
// supervisor's activity reader (workspaceActivityPath).
func MarkActivity(opts Options) {
	if strings.TrimSpace(opts.Name) == "" || strings.TrimSpace(opts.StateDir) == "" {
		return
	}
	path := filepath.Join(opts.StateDir, opts.Name, "activity")
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// execDialAddr validates that the workspace is running with a reachable
// structured exec service, gates on readiness, and returns the dial address.
func execDialAddr(ctx context.Context, opts Options) (string, error) {
	return execDialAddrWithActivity(ctx, opts, true)
}

func execDialAddrWithActivity(ctx context.Context, opts Options, markActivity bool) (string, error) {
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
		return "", operation.New(operation.ErrorConflict, "workspace %s is paused; resume it first", opts.Name)
	}
	if state != vmkit.StateRunning {
		message := fmt.Sprintf("workspace %s is not running; structured exec is unavailable in state %s", opts.Name, state)
		// The terminal runtime record carries the supervisor's actual failure
		// cause. Preserve it at this boundary instead of reducing every failed
		// start or restore to the same generic state error. The stable prefix
		// remains unchanged for callers that classify this conflict.
		if runtimeState, runtimeErr := ReadRuntimeState(opts); runtimeErr == nil {
			if cause := strings.TrimSpace(runtimeState.Error); cause != "" {
				message += ": " + cause
			}
		}
		return "", operation.New(operation.ErrorConflict, "%s", message)
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
	if markActivity {
		MarkActivity(opts)
	}
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
