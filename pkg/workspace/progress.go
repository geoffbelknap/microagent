package workspace

import (
	"strings"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

const (
	progressOperationStart      = "workspace_start"
	progressOperationWait       = "workspace_wait"
	progressOperationControl    = "workspace_control"
	progressOperationQuarantine = "workspace_quarantine"
	progressOperationDelete     = "workspace_delete"
	progressOperationSnapshot   = "workspace_snapshot"
	progressOperationFork       = "workspace_snapshot_fork"
)

func emitWorkspaceProgress(opts Options, operationID, label, phase, message string) {
	emitWorkspaceByteProgress(opts, operationID, label, phase, message, 0, 0)
}

func emitWorkspaceByteProgress(opts Options, operationID, label, phase, message string, bytes, totalBytes int64) {
	if opts.Progress == nil {
		return
	}
	opts.Progress(operation.ProgressEvent{
		Operation:     operationID,
		Phase:         phase,
		Label:         label,
		Message:       strings.TrimSpace(message),
		Bytes:         bytes,
		TotalBytes:    totalBytes,
		Indeterminate: totalBytes <= 0,
	})
}
