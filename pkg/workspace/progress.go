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
	progressOperationFork       = "workspace_snapshot_fork"
)

func emitWorkspaceProgress(opts Options, operationID, label, phase, message string) {
	if opts.Progress == nil {
		return
	}
	opts.Progress(operation.ProgressEvent{
		Operation:     operationID,
		Phase:         phase,
		Label:         label,
		Message:       strings.TrimSpace(message),
		Indeterminate: true,
	})
}
