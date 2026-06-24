package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DispatchResult is the outcome of a single dispatched task: the guest result
// plus a high-level, mediator-written summary of what the workspace reached on
// the network. That second half is the point — it lets the caller judge not
// just whether the task succeeded but whether it stayed on-intent (did it only
// talk to the hosts the work implied, or wander off?). The workspace is torn
// down before this returns.
type DispatchResult struct {
	Workspace  string             `json:"workspace"`
	FinalState string             `json:"final_state,omitempty"`
	Result     *GuestResult       `json:"result,omitempty"`
	Audit      EgressAuditSummary `json:"audit"`
}

// RunDispatch runs one command in a fresh, isolated, single-use workspace under
// the egress guardrails in opts, collects the guest result AND a summary of the
// mediator's egress decisions, then tears the workspace down. It is the one-call
// "delegate this to an isolated machine and tell me what it did" primitive that
// the `dispatch` CLI verb and MCP tool sit on.
//
// The egress audit is written by the mediator, outside the guest's control, so
// the returned summary is a trustworthy record of where the task actually
// connected — a prompt-injected or otherwise-rogue task can neither forge nor
// suppress it. Under the default guarded mode the mediator records allowed
// public destinations too (not just denials), so the summary reflects real
// behavior without needing strict mode.
//
// Cleanup is unconditional: dispatch is one-shot, so the workspace is removed on
// every exit path (success or failure) after the audit is read.
func RunDispatch(ctx context.Context, opts Options) (DispatchResult, error) {
	if strings.TrimSpace(opts.Name) == "" {
		opts.Name = fmt.Sprintf("dispatch-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(opts.StateDir) == "" {
		opts.StateDir = StateDir()
	}
	// Keep the workspace through Run so the egress audit is still on disk when we
	// read it; Run would otherwise discard a one-shot run on success. We own the
	// teardown below.
	opts.Keep = true
	result, runErr := Run(ctx, opts)

	events, auditErr := ReadEgressAudit(opts.StateDir, opts.Name)
	if auditErr != nil {
		events = nil
	}
	Cleanup(opts.StateDir, opts.Name)

	return DispatchResult{
		Workspace:  opts.Name,
		FinalState: result.FinalState,
		Result:     result.Result,
		Audit:      SummarizeEgressAudit(events),
	}, runErr
}
