package modelservice

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/eventhistory"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func appendModelWorkerAttachedEvent(opts workspace.Options, runner modelrunner.Record, modelURL string, mediator *Attachment) error {
	fields := []string{
		"model_ref=" + runner.ModelRef,
		"engine=" + runner.Engine,
		fmt.Sprintf("pid=%d", runner.PID),
		"runner_config_digest=" + runner.RunnerConfigDigest,
		"holder=" + opts.Name,
		"model_url=" + modelURL,
	}
	if mediator == nil {
		fields = append(fields, "mediation=direct")
	} else {
		fields = append(fields,
			"mediation=host-worker",
			"mediation_mode="+mediator.Mode,
			fmt.Sprintf("mediator_pid=%d", mediator.PID),
			fmt.Sprintf("mediator_port=%d", mediator.Port),
			"mediator_audit_log="+mediator.AuditLogPath,
		)
	}
	detail := modelWorkerEventDetail("attached", fields)
	return appendModelWorkerEventIfWorkspaceExists(opts.StateDir, opts.Name, opts.Backend, vmkit.StateStarting, detail)
}

func appendModelWorkerReleasedEvent(stateDir, name, backend, modelRef string) error {
	state := latestWorkspaceEventState(stateDir, name)
	if state == vmkit.StateUnknown {
		state = vmkit.StateHalted
	}
	detail := modelWorkerEventDetail("released", []string{
		"model_ref=" + modelRef,
		"holder=" + name,
	})
	return appendModelWorkerEventIfWorkspaceExists(stateDir, name, backend, state, detail)
}

func modelWorkerEventDetail(action string, fields []string) string {
	parts := []string{"model_worker=" + action}
	for _, field := range fields {
		if strings.HasSuffix(field, "=") {
			continue
		}
		parts = append(parts, field)
	}
	return strings.Join(parts, " ")
}

func appendModelWorkerEventIfWorkspaceExists(stateDir, name, backend string, state vmkit.VMState, detail string) error {
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(name) == "" {
		return nil
	}
	workspaceDir := filepath.Join(stateDir, name)
	if _, err := os.Stat(workspaceDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(backend) == "" {
		backend = workspace.HostBackend()
	}
	event := workspace.EventFile{
		Identity: vmkit.Identity{
			RequestID: workspace.NewRequestID(),
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   backend,
		},
		State:      state,
		Detail:     detail,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return eventhistory.Append(filepath.Join(workspaceDir, "events.json"), event, eventhistory.Options{})
}

func latestWorkspaceEventState(stateDir, name string) vmkit.VMState {
	events, err := workspace.ReadEvents(stateDir, name)
	if err != nil || len(events) == 0 {
		return vmkit.StateUnknown
	}
	return events[len(events)-1].State
}
