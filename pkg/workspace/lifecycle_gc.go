package workspace

import (
	"context"
	"fmt"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// GCReap identifies one stale running record reconciled to stopped state.
type GCReap struct {
	Name string `json:"name"`
	Was  string `json:"was"`
}

// GCFailure records one target that could not be inspected or reconciled.
// Batch GC continues after a target failure and returns every failure here.
type GCFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// GCResult is the bounded batch result of a host stale-runtime sweep.
type GCResult struct {
	Checked int         `json:"checked"`
	Reaped  []GCReap    `json:"reaped"`
	Failed  []GCFailure `json:"failed,omitempty"`
}

var gcControl = Control

// GC reconciles every workspace recorded as running. Per-workspace failures
// are collected instead of aborting the scan; setup/list failures are returned.
func GC(ctx context.Context, opts Options) (GCResult, error) {
	report := operation.NewReporter(opts.Progress)
	report.Emit(operation.ProgressEvent{
		Operation: "workspace_gc", Phase: "gc_scan", Label: "Reconcile workspaces",
		Message: "scanning running workspace records", Indeterminate: true,
	})
	entries, err := List(opts.StateDir)
	if err != nil {
		return GCResult{}, err
	}
	total := int64(0)
	for _, entry := range entries {
		if vmkit.VMState(entry.State) == vmkit.StateRunning {
			total++
		}
	}
	result := GCResult{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if vmkit.VMState(entry.State) != vmkit.StateRunning {
			continue
		}
		result.Checked++
		report.Emit(operation.ProgressEvent{
			Operation: "workspace_gc", Phase: "gc_reconcile", Label: "Reconcile workspaces",
			Message: "checking running workspace", Current: int64(result.Checked), Total: total,
		})
		workspaceOpts := opts
		workspaceOpts.Name = entry.Name
		workspaceOpts.Progress = nil
		resp, controlErr := gcControl(ctx, workspaceOpts, "gc")
		if controlErr != nil {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			message := controlErr.Error()
			if resp.Error != "" {
				message = resp.Error
			}
			result.Failed = append(result.Failed, GCFailure{Name: entry.Name, Error: message})
			continue
		}
		if !resp.OK && resp.Error != "" {
			result.Failed = append(result.Failed, GCFailure{Name: entry.Name, Error: resp.Error})
			continue
		}
		if !resp.OK {
			result.Failed = append(result.Failed, GCFailure{Name: entry.Name, Error: "workspace reconciliation failed"})
			continue
		}
		if resp.Event != nil && resp.Event.State == vmkit.StateStopped {
			result.Reaped = append(result.Reaped, GCReap{Name: entry.Name, Was: entry.State})
		}
	}
	report.Emit(operation.ProgressEvent{
		Operation: "workspace_gc", Phase: "gc_complete", Label: "Reconcile workspaces",
		Message: fmt.Sprintf("checked %d; reaped %d; failed %d", result.Checked, len(result.Reaped), len(result.Failed)),
		Current: int64(result.Checked), Total: total,
	})
	return result, nil
}
