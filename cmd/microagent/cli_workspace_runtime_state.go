package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/eventhistory"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func dispatchWorkspaceRequest(ctx context.Context, opts workspaceOptions, req vmkit.Request) (vmkit.Response, error) {
	return workspace.Dispatch(ctx, opts, req)
}

func readWorkspaceRuntimeState(opts workspaceOptions) (workspaceRuntimeState, error) {
	var state workspaceRuntimeState
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "runtime.json"))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func readWorkspaceEvent(opts workspaceOptions) (workspaceEventFile, error) {
	var event workspaceEventFile
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "event.json"))
	if err != nil {
		return event, err
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return event, err
	}
	return event, nil
}

func buildWorkspaceVerification(opts workspaceOptions, result workspaceResult) (vmkit.RuntimeVerification, error) {
	verification := vmkit.RuntimeVerification{
		OK:          true,
		ImageRef:    result.Image.ImageRef,
		ResolvedRef: result.Image.ResolvedRef,
		ImageDigest: result.Image.Digest,
		Kernel:      recordedArtifact(opts.KernelPath),
		Rootfs:      recordedArtifact(result.RootfsPath),
	}
	if opts.GuestInitPath != "" {
		if info, err := os.Stat(opts.GuestInitPath); err == nil && !info.IsDir() {
			verification.Init = recordedArtifact(opts.GuestInitPath)
		}
	}
	for _, artifact := range []struct {
		name     string
		artifact *vmkit.VerifiedArtifact
	}{
		{name: "kernel", artifact: verification.Kernel},
		{name: "rootfs", artifact: verification.Rootfs},
		{name: "init", artifact: verification.Init},
	} {
		if artifact.artifact != nil && artifact.artifact.Error != "" {
			verification.OK = false
			verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{
				Artifact: artifact.name,
				Error:    artifact.artifact.Error,
			})
		}
	}
	if !verification.OK {
		return verification, fmt.Errorf("record workspace verification: %s", verification.Divergence[0].Error)
	}
	return verification, nil
}

func recordedArtifact(path string) *vmkit.VerifiedArtifact {
	artifact := &vmkit.VerifiedArtifact{Path: path}
	if strings.TrimSpace(path) == "" {
		artifact.Error = "path is empty"
		return artifact
	}
	sum, err := workspace.FileSHA256(path)
	if err != nil {
		artifact.Error = err.Error()
		return artifact
	}
	artifact.SHA256 = sum
	return artifact
}

func liveReadinessUnavailableSignal(state vmkit.VMState, observedAt *time.Time) *vmkit.ReadinessSignal {
	if !liveWorkspaceUnavailableState(state) {
		return nil
	}
	return &vmkit.ReadinessSignal{
		Ready:      false,
		ObservedAt: observedAt,
		Detail:     fmt.Sprintf("workspace is %s; live readiness unavailable", state),
	}
}

func liveWorkspaceUnavailableState(state vmkit.VMState) bool {
	return state == vmkit.StatePrepared || state == vmkit.StateHalted || state == vmkit.StateStopped || state == vmkit.StateQuarantined || state == vmkit.StateFailed
}

func workspaceReadinessFromRuntime(state workspaceRuntimeState) vmkit.RuntimeReadiness {
	readiness := vmkit.RuntimeReadiness{}
	if state.StartedAt != "" || state.Event.State == vmkit.StateRunning || state.Event.State == vmkit.StateHalted || state.Event.State == vmkit.StateStopped || state.Event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: firstTime(state.StartedAt, state.Event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(state.Event.State),
		}
	}
	liveUnavailable := liveReadinessUnavailableSignal(state.Event.State, firstTime(state.StartedAt, state.Event.ObservedAt))
	if liveUnavailable != nil {
		readiness.ShellReady = *liveUnavailable
		readiness.ExecReady = *liveUnavailable
		readiness.ResultReady = *liveUnavailable
		if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
			readiness.MediationReady = *liveUnavailable
		}
	}
	if state.Event.State == vmkit.StateRunning && state.SerialInputPath != "" {
		if signal, ok := workspaceShellReadinessFromRuntime(state); ok {
			readiness.ShellReady = signal
		}
	}
	if signal, ok := workspace.ExecReadinessSignal(context.Background(), state, workspace.ExecReadyProbeTimeout); ok {
		readiness.ExecReady = signal
	}
	path := resultPath(workspaceOptions{StateDir: state.Config.StateDir, Name: state.Event.Identity.RuntimeID})
	if _, err := os.Stat(path); err == nil {
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: fileModTime(path),
			Detail:     "guest result is available",
		}
	} else if !os.IsNotExist(err) {
		readiness.ResultReady = vmkit.ReadinessSignal{Error: err.Error()}
	}
	if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
		readiness.MediationReady = vmkit.MediationReadinessSignal(context.Background(), *state.Config.Mediation, state.Event.State, firstTime(state.StartedAt, state.Event.ObservedAt), 150*time.Millisecond)
	}
	return readiness
}

func workspaceShellReadinessFromRuntime(state workspaceRuntimeState) (vmkit.ReadinessSignal, bool) {
	return workspace.ShellReadinessSignalWithMode(context.Background(), state, time.Second, workspace.ShellReadinessProbeCommand)
}

func firstTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed := parseOptionalTime(value); parsed != nil {
			return parsed
		}
	}
	return nil
}

func parseOptionalTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func fileModTime(path string) *time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mod := info.ModTime().UTC()
	return &mod
}

func writeWorkspaceProcessState(opts workspaceOptions, req vmkit.Request, state vmkit.VMState, pid int, errorText string) error {
	if req.Identity == nil || req.Config == nil {
		return fmt.Errorf("workspace request is missing identity or config")
	}
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	event := vmkit.Event{
		Identity:   *req.Identity,
		State:      state,
		Detail:     "serial=" + serialLogPath(opts),
		ObservedAt: time.Now().UTC(),
	}
	fileEvent := workspaceEventFile{
		Identity:   *req.Identity,
		State:      state,
		Detail:     event.Detail,
		ObservedAt: event.ObservedAt.Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "event.json"), fileEvent); err != nil {
		return err
	}
	if err := appendWorkspaceEvent(filepath.Join(dir, "events.json"), fileEvent); err != nil {
		return err
	}
	updatedAt := time.Now().UTC()
	runtimeState := workspaceRuntimeState{
		Event:           fileEvent,
		Config:          *req.Config,
		PID:             pid,
		SerialLogPath:   serialLogPath(opts),
		SerialInputPath: serialInputPath(opts.StateDir, opts.Name),
		UpdatedAt:       updatedAt.Format(time.RFC3339),
		Error:           errorText,
	}
	if state == vmkit.StateStarting || state == vmkit.StateRunning || state == vmkit.StateQuarantined {
		runtimeState.StartedAt = updatedAt.Format(time.RFC3339)
	}
	runtimeState.Readiness = workspaceReadinessFromRuntime(runtimeState)
	return writeJSONFile(filepath.Join(dir, "runtime.json"), runtimeState)
}

func appendWorkspaceEvent(path string, event workspaceEventFile) error {
	return eventhistory.Append(path, event, eventhistory.Options{})
}

type workspaceEventFile = workspace.EventFile
type workspaceRuntimeState = workspace.RuntimeState

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func serialLogPath(opts workspaceOptions) string {
	return filepath.Join(opts.StateDir, opts.Name, "serial.log")
}

func serialInputPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "serial.in")
}

func resultPath(opts workspaceOptions) string {
	return filepath.Join(opts.StateDir, opts.Name, "result.json")
}
