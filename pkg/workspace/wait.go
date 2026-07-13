package workspace

import (
	"context"
	"fmt"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// WaitOptions bounds a Wait call.
type WaitOptions struct {
	// Timeout ends the wait with a WaitTimeoutError when the workspace has
	// not reached a terminal state in time. Zero means no timeout.
	Timeout time.Duration
	// Interval is the delay between state checks. Zero means one second.
	Interval time.Duration
}

// WaitResult reports the terminal state a Wait observed.
type WaitResult struct {
	Workspace string `json:"workspace"`
	State     string `json:"state"`
	// OK is true when the terminal state is a clean finish (prepared,
	// stopped, or halted) and false for failed or quarantined, so adapters
	// share one success rule instead of each re-deriving it.
	OK bool `json:"ok"`
}

// WaitTimeoutError reports a Wait that gave up before the workspace reached a
// terminal state.
type WaitTimeoutError struct {
	Name      string
	Timeout   time.Duration
	LastState vmkit.VMState
}

func (e WaitTimeoutError) Error() string {
	return fmt.Sprintf("wait timeout: workspace %s is still %s after %s", e.Name, e.LastState, e.Timeout)
}

func (e WaitTimeoutError) Is(target error) bool {
	_, ok := target.(WaitTimeoutError)
	return ok
}

// waitInspect lets tests observe the poll loop without a live supervisor.
var waitInspect = Inspect

// IsWaitTerminalState reports whether a state ends a Wait: the supervised
// terminal states (stopped, halted, failed, quarantined) plus prepared, which
// cannot progress without another start.
func IsWaitTerminalState(state vmkit.VMState) bool {
	return state == vmkit.StatePrepared || isSupervisedTerminalState(state)
}

// WaitStateOK reports whether a terminal state counts as a clean finish.
func WaitStateOK(state vmkit.VMState) bool {
	switch state {
	case vmkit.StatePrepared, vmkit.StateStopped, vmkit.StateHalted:
		return true
	default:
		return false
	}
}

// Wait blocks until the workspace reaches a terminal state and reports it.
// A recorded terminal state returns immediately; a recorded live state is
// reconciled against the supervisor each tick, so a VM whose process died
// resolves instead of blocking forever. A missing workspace returns
// WorkspaceNotFoundError on the first check rather than polling for a
// workspace that can never appear.
func Wait(ctx context.Context, opts Options, waitOpts WaitOptions) (WaitResult, error) {
	interval := waitOpts.Interval
	if interval <= 0 {
		interval = time.Second
	}
	var timeoutC <-chan time.Time
	if waitOpts.Timeout > 0 {
		timer := time.NewTimer(waitOpts.Timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}
	last := vmkit.StateUnknown
	for {
		state, err := waitObserveState(ctx, opts)
		if state != vmkit.StateUnknown {
			last = state
		}
		if err != nil {
			return WaitResult{Workspace: opts.Name, State: string(last)}, err
		}
		if IsWaitTerminalState(state) {
			return WaitResult{Workspace: opts.Name, State: string(state), OK: WaitStateOK(state)}, nil
		}
		select {
		case <-ctx.Done():
			return WaitResult{Workspace: opts.Name, State: string(last)}, ctx.Err()
		case <-timeoutC:
			return WaitResult{Workspace: opts.Name, State: string(last)}, WaitTimeoutError{Name: opts.Name, Timeout: waitOpts.Timeout, LastState: last}
		case <-time.After(interval):
		}
	}
}

// waitObserveState reads the recorded state and, when it is not terminal,
// reconciles against the supervisor the same way the status command does:
// Inspect reaps a dead VM and reports the real state, so a stale "running"
// record resolves. Like WaitForSupervised, an Inspect error that still
// carries a terminal event counts as that terminal state.
func waitObserveState(ctx context.Context, opts Options) (vmkit.VMState, error) {
	resp, err := Status(opts)
	if err != nil {
		return vmkit.StateUnknown, err
	}
	state := vmkit.StateUnknown
	if resp.Event != nil {
		state = resp.Event.State
	}
	if IsWaitTerminalState(state) {
		return state, nil
	}
	inspected, err := waitInspect(ctx, opts)
	if inspected.Event != nil {
		state = inspected.Event.State
	}
	if err != nil && !IsWaitTerminalState(state) {
		return state, err
	}
	return state, nil
}
