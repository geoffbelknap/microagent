package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type eventFile struct {
	Identity   vmkit.Identity `json:"identity"`
	State      vmkit.VMState  `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ObservedAt string         `json:"observedAt"`
}

type runtimeState struct {
	Event           eventFile              `json:"event"`
	Config          vmkit.Config           `json:"config"`
	ComputeSystemID string                 `json:"computeSystemID,omitempty"`
	SerialLogPath   string                 `json:"serialLogPath"`
	StartedAt       string                 `json:"startedAt,omitempty"`
	UpdatedAt       string                 `json:"updatedAt"`
	Readiness       vmkit.RuntimeReadiness `json:"readiness,omitempty"`
	Error           string                 `json:"error,omitempty"`
}

func (s Supervisor) run(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	adapter := s.runtimeAdapter()
	spec := computeSystemSpec{
		Name:     req.Identity.RuntimeID,
		StateDir: req.Config.StateDir,
		Identity: *req.Identity,
		Config:   *req.Config,
	}
	if _, err := writeRuntimeTransition(req, vmkit.StateStarting, "creating windows-hyperv compute system", ""); err != nil {
		return vmkit.Response{}, err
	}
	handle, err := adapter.Create(ctx, spec)
	if err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("create failed: %s", err), err)
	}
	if err := adapter.Start(ctx, handle.ID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("start failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeID(req, vmkit.StateRunning, "serial="+serialLogPath(req), "", handle.ID)
	if err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

func inspect(req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	event, err := eventFromFile(state.Event)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	resp := vmkit.Response{OK: state.Event.State != vmkit.StateFailed, Backend: vmkit.BackendWindowsHyperV, Event: &event, Readiness: &state.Readiness}
	if result, resultErr := readRuntimeResult(req, event.Identity); resultErr == nil {
		resp.Result = &result
		now := time.Now().UTC()
		if resp.Readiness == nil {
			resp.Readiness = &vmkit.RuntimeReadiness{}
		}
		resp.Readiness.ResultReady = vmkit.ReadinessSignal{Ready: true, ObservedAt: &now, Detail: "result.json present"}
	} else if !os.IsNotExist(resultErr) {
		if resp.Readiness == nil {
			resp.Readiness = &vmkit.RuntimeReadiness{}
		}
		resp.Readiness.ResultReady = vmkit.ReadinessSignal{Error: resultErr.Error()}
	}
	if state.Error != "" {
		resp.Error = state.Error
	}
	return resp, nil
}

func (s Supervisor) stop(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if err := s.runtimeAdapter().Shutdown(ctx, state.ComputeSystemID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("stop failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeID(req, vmkit.StateStopped, "windows-hyperv compute system stopped", "", state.ComputeSystemID)
	if err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

func (s Supervisor) kill(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if err := s.runtimeAdapter().Kill(ctx, state.ComputeSystemID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("kill failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeID(req, vmkit.StateStopped, "windows-hyperv compute system killed", "", state.ComputeSystemID)
	if err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

func (s Supervisor) delete(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if err := s.runtimeAdapter().Delete(ctx, state.ComputeSystemID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("delete failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeID(req, vmkit.StateStopped, "windows-hyperv compute system deleted", "", state.ComputeSystemID)
	if err != nil {
		return vmkit.Response{}, err
	}
	if err := os.RemoveAll(runtimeDir(req)); err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

func failRun(req vmkit.Request, state vmkit.VMState, detail string, cause error) (vmkit.Response, error) {
	event, writeErr := writeRuntimeTransition(req, state, detail, cause.Error())
	resp := vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: cause.Error()}
	if writeErr == nil {
		resp.Event = &event
	}
	return resp, cause
}

func writeRuntimeTransition(req vmkit.Request, state vmkit.VMState, detail, errorText string) (vmkit.Event, error) {
	return writeRuntimeTransitionWithComputeID(req, state, detail, errorText, "")
}

func writeRuntimeTransitionWithComputeID(req vmkit.Request, state vmkit.VMState, detail, errorText, computeSystemID string) (vmkit.Event, error) {
	if req.Identity == nil || req.Config == nil {
		return vmkit.Event{}, fmt.Errorf("windows-hyperv request is missing identity or config")
	}
	dir := runtimeDir(req)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return vmkit.Event{}, err
	}
	serialPath := serialLogPath(req)
	serialFile, err := os.OpenFile(serialPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return vmkit.Event{}, fmt.Errorf("create serial log: %w", err)
	}
	if err := serialFile.Close(); err != nil {
		return vmkit.Event{}, fmt.Errorf("close serial log: %w", err)
	}
	now := time.Now().UTC()
	event := vmkit.Event{
		Identity:   *req.Identity,
		State:      state,
		Detail:     detail,
		ObservedAt: now,
	}
	fileEvent := eventFile{
		Identity:   *req.Identity,
		State:      state,
		Detail:     detail,
		ObservedAt: now.Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "event.json"), fileEvent); err != nil {
		return vmkit.Event{}, err
	}
	if err := appendJSONLine(filepath.Join(dir, "events.json"), fileEvent); err != nil {
		return vmkit.Event{}, err
	}
	startedAt := ""
	if state == vmkit.StateStarting || state == vmkit.StateRunning {
		startedAt = now.Format(time.RFC3339)
	}
	readiness := vmkit.RuntimeReadiness{}
	if state == vmkit.StateRunning {
		readiness.GuestReady = vmkit.ReadinessSignal{Ready: true, ObservedAt: &now, Detail: "windows-hyperv compute system started"}
	}
	if computeSystemID == "" {
		if previous, err := readRuntimeState(req); err == nil {
			computeSystemID = previous.ComputeSystemID
		}
	}
	runtime := runtimeState{
		Event:           fileEvent,
		Config:          *req.Config,
		ComputeSystemID: computeSystemID,
		SerialLogPath:   serialPath,
		StartedAt:       startedAt,
		UpdatedAt:       now.Format(time.RFC3339),
		Readiness:       readiness,
		Error:           errorText,
	}
	if err := writeJSONFile(filepath.Join(dir, "runtime.json"), runtime); err != nil {
		return vmkit.Event{}, err
	}
	return event, nil
}

func readRuntimeState(req vmkit.Request) (runtimeState, error) {
	var state runtimeState
	data, err := os.ReadFile(filepath.Join(runtimeDir(req), "runtime.json"))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func readRuntimeResult(req vmkit.Request, identity vmkit.Identity) (vmkit.RuntimeResult, error) {
	path := resultPath(req)
	data, err := os.ReadFile(path)
	if err != nil {
		return vmkit.RuntimeResult{}, err
	}
	var result vmkit.RuntimeResult
	if err := json.Unmarshal(data, &result); err != nil {
		return vmkit.RuntimeResult{}, err
	}
	result.Identity = identity
	result.Backend = vmkit.BackendWindowsHyperV
	result.ResultPath = path
	return result, nil
}

func eventFromFile(file eventFile) (vmkit.Event, error) {
	observedAt := time.Now().UTC()
	if file.ObservedAt != "" {
		parsed, err := time.Parse(time.RFC3339, file.ObservedAt)
		if err != nil {
			return vmkit.Event{}, err
		}
		observedAt = parsed
	}
	return vmkit.Event{Identity: file.Identity, State: file.State, Detail: file.Detail, ObservedAt: observedAt}, nil
}

func runtimeDir(req vmkit.Request) string {
	return filepath.Join(req.Config.StateDir, req.Identity.RuntimeID)
}

func serialLogPath(req vmkit.Request) string {
	return filepath.Join(runtimeDir(req), "serial.log")
}

func resultPath(req vmkit.Request) string {
	return filepath.Join(runtimeDir(req), "result.json")
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendJSONLine(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
