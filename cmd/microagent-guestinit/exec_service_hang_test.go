//go:build linux

package main

import (
	"testing"
	"time"

	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// runExecWithin runs executeStructuredExec and fails if it does not return within
// limit — a regression of the WaitDelay / process-group teardown would block Wait
// forever and hang here instead of failing cleanly.
func runExecWithin(t *testing.T, req execprotocol.ExecRequest, service structuredExecService, limit time.Duration) execprotocol.ExecResult {
	t.Helper()
	if service.now == nil {
		service.now = time.Now
	}
	done := make(chan execprotocol.ExecResult, 1)
	go func() { done <- executeStructuredExec(req, service) }()
	select {
	case r := <-done:
		return r
	case <-time.After(limit):
		t.Fatalf("executeStructuredExec did not return within %s (Wait blocked on a lingering child?)", limit)
		return execprotocol.ExecResult{}
	}
}

// TestExecReturnsWhenCommandLeavesBackgroundChildHoldingStdout is the core B6
// guard: a command that exits cleanly but leaves a child holding stdout/stderr
// open must NOT hang Wait forever. WaitDelay bounds the post-exit drain, and the
// command's real exit code (0) is reported — not a wait failure, not a timeout.
func TestExecReturnsWhenCommandLeavesBackgroundChildHoldingStdout(t *testing.T) {
	service := structuredExecService{
		terminationGrace: 50 * time.Millisecond,
		waitDelay:        200 * time.Millisecond,
		now:              time.Now,
	}
	req := execprotocol.ExecRequest{
		// The backgrounded `sleep` inherits stdout/stderr and holds the pipe open
		// well past WaitDelay, while the shell itself exits 0 immediately.
		Argv:      []string{"/bin/sh", "-c", "sleep 2 & echo started; exit 0"},
		TimeoutMS: 10000, // generous — the command exits at once; only the pipe lingers
	}
	result := runExecWithin(t, req, service, 5*time.Second)
	if result.Status != execprotocol.ExecStatusExited {
		t.Fatalf("status = %v, want exited (a lingering child must not fail or time out the command)", result.Status)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", result.ExitCode)
	}
}

// TestExecTimeoutTearsDownProcessGroup: a command that hangs (and spawned a
// child) is torn down as a group on timeout, so the exec returns TimedOut
// promptly rather than blocking on Wait because a child still holds the pipe.
func TestExecTimeoutTearsDownProcessGroup(t *testing.T) {
	service := structuredExecService{
		terminationGrace: 50 * time.Millisecond,
		waitDelay:        5 * time.Second,
		now:              time.Now,
	}
	req := execprotocol.ExecRequest{
		Argv:      []string{"/bin/sh", "-c", "sleep 30 & wait"},
		TimeoutMS: 200,
	}
	result := runExecWithin(t, req, service, 5*time.Second)
	if result.Status != execprotocol.ExecStatusTimedOut {
		t.Fatalf("status = %v, want timed_out", result.Status)
	}
}
