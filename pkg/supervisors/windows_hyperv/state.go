package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const clearVsockListenerPID = -1

type eventFile struct {
	Identity   vmkit.Identity `json:"identity"`
	State      vmkit.VMState  `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ObservedAt string         `json:"observedAt"`
}

type runtimeState struct {
	Event                  eventFile              `json:"event"`
	Config                 vmkit.Config           `json:"config"`
	ComputeSystemID        string                 `json:"computeSystemID,omitempty"`
	ComputeSystemRuntimeID string                 `json:"computeSystemRuntimeID,omitempty"`
	NetworkID              string                 `json:"networkID,omitempty"`
	NetworkEndpointID      string                 `json:"networkEndpointID,omitempty"`
	VsockListenerPID       int                    `json:"vsockListenerPid,omitempty"`
	SerialLogPath          string                 `json:"serialLogPath"`
	SerialInputPath        string                 `json:"serialInputPath,omitempty"`
	StartedAt              string                 `json:"startedAt,omitempty"`
	UpdatedAt              string                 `json:"updatedAt"`
	Readiness              vmkit.RuntimeReadiness `json:"readiness,omitempty"`
	Error                  string                 `json:"error,omitempty"`
}

var startRuntimeListenersHook = startRuntimeListeners
var startRuntimeListenerProcessHook = startRuntimeListenerProcess
var terminateRuntimeListenerProcessHook = terminateRuntimeListenerProcess

func (s Supervisor) run(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	return s.startComputeSystem(ctx, req, true)
}

func (s Supervisor) start(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	return s.startComputeSystem(ctx, req, false)
}

func (s Supervisor) startComputeSystem(ctx context.Context, req vmkit.Request, foreground bool) (vmkit.Response, error) {
	if err := validateNetwork(req); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("network validation failed: %s", err), err)
	}
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
	network, err := adapter.PrepareNetwork(ctx, spec)
	if err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("network setup failed: %s", err), err)
	}
	if network.RuntimeNetwork != nil {
		spec.Config.Network = network.RuntimeNetwork
	}
	runtimeReq := req
	runtimeReq.Config = &spec.Config
	spec.NetworkID = network.NetworkID
	spec.NetworkEndpointID = network.NetworkEndpointID
	handle, err := adapter.Create(ctx, spec)
	if err != nil {
		_ = adapter.CleanupNetwork(ctx, runtimeState{NetworkID: network.NetworkID, NetworkEndpointID: network.NetworkEndpointID})
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("create failed: %s", err), err)
	}
	handle.NetworkID = network.NetworkID
	handle.NetworkEndpointID = network.NetworkEndpointID
	handle.RuntimeNetwork = network.RuntimeNetwork
	listenerPID := 0
	var listeners runtimeListenerSet
	if foreground {
		listeners, err = startRuntimeListenersHook(ctx, handle, runtimeReq)
		if err != nil {
			return failRunWithCleanup(ctx, runtimeReq, adapter, handle, false, fmt.Sprintf("listener setup failed: %s", err), err)
		}
		if listeners != nil {
			defer listeners.Close()
		}
	} else if hasDetachedRuntimeListeners(req) {
		if _, err := writeRuntimeTransitionWithComputeIDs(req, vmkit.StateStarting, "starting windows-hyperv runtime listener helper", "", handle.ID, handle.RuntimeID); err != nil {
			return vmkit.Response{}, err
		}
		listenerPID, err = startRuntimeListenerProcessHook(req)
		if err != nil {
			return failRunWithCleanup(ctx, req, adapter, handle, false, fmt.Sprintf("listener helper failed: %s", err), err)
		}
	}
	if err := adapter.Start(ctx, handle.ID); err != nil {
		if listeners != nil {
			_ = listeners.Close()
		}
		if listenerPID != 0 {
			terminateRuntimeListenerProcessHook(listenerPID)
		}
		return failRunWithCleanup(ctx, req, adapter, handle, false, fmt.Sprintf("start failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeIDsNetworkAndListenerPID(runtimeReq, vmkit.StateRunning, "serial="+serialLogPath(runtimeReq), "", handle.ID, handle.RuntimeID, handle.NetworkID, handle.NetworkEndpointID, listenerPID)
	if err != nil {
		if listeners != nil && foreground {
			_ = listeners.Close()
		}
		return vmkit.Response{}, err
	}
	if foreground && listeners != nil {
		if err := listeners.Wait(ctx); err != nil {
			return failRunWithCleanup(ctx, runtimeReq, adapter, handle, true, fmt.Sprintf("result listener failed: %s", err), err)
		}
		if err := adapter.Wait(ctx, handle.ID); err != nil {
			return failRunWithCleanup(ctx, runtimeReq, adapter, handle, true, fmt.Sprintf("wait failed: %s", err), err)
		}
		if err := adapter.CleanupNetwork(ctx, runtimeState{NetworkID: handle.NetworkID, NetworkEndpointID: handle.NetworkEndpointID}); err != nil {
			return failRunWithCleanup(ctx, runtimeReq, adapter, handle, true, fmt.Sprintf("network cleanup failed: %s", err), err)
		}
		event, err = writeRuntimeTransitionWithComputeIDsNetwork(runtimeReq, vmkit.StateStopped, "windows-hyperv result received", "", handle.ID, handle.RuntimeID, handle.NetworkID, handle.NetworkEndpointID)
		if err != nil {
			return vmkit.Response{}, err
		}
		resp := vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}
		if result, resultErr := readRuntimeResult(req, event.Identity); resultErr == nil {
			resp.Result = &result
		}
		return resp, nil
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
	terminateRuntimeListenerProcessHook(state.VsockListenerPID)
	if err := s.runtimeAdapter().Shutdown(ctx, state.ComputeSystemID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("stop failed: %s", err), err)
	}
	if err := s.runtimeAdapter().CleanupNetwork(ctx, state); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("network cleanup failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(req, vmkit.StateStopped, "windows-hyperv compute system stopped", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID)
	if err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

func (s Supervisor) halt(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	terminateRuntimeListenerProcessHook(state.VsockListenerPID)
	if err := s.runtimeAdapter().Shutdown(ctx, state.ComputeSystemID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("halt failed: %s", err), err)
	}
	if err := s.runtimeAdapter().CleanupNetwork(ctx, state); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("network cleanup failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(req, vmkit.StateHalted, "windows-hyperv compute system halted", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID)
	if err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

func (s Supervisor) quarantine(req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	terminateRuntimeListenerProcessHook(state.VsockListenerPID)
	if err := s.runtimeAdapter().CleanupNetwork(context.Background(), state); err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(req, vmkit.StateQuarantined, "windows-hyperv compute system quarantined", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID)
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
	terminateRuntimeListenerProcessHook(state.VsockListenerPID)
	if err := s.runtimeAdapter().Kill(ctx, state.ComputeSystemID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("kill failed: %s", err), err)
	}
	if err := s.runtimeAdapter().CleanupNetwork(ctx, state); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("network cleanup failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(req, vmkit.StateStopped, "windows-hyperv compute system killed", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID)
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
	terminateRuntimeListenerProcessHook(state.VsockListenerPID)
	if err := s.runtimeAdapter().Delete(ctx, state.ComputeSystemID); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("delete failed: %s", err), err)
	}
	if err := s.runtimeAdapter().CleanupNetwork(ctx, state); err != nil {
		return failRun(req, vmkit.StateFailed, fmt.Sprintf("network cleanup failed: %s", err), err)
	}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(req, vmkit.StateStopped, "windows-hyperv compute system deleted", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID)
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

func failRunWithCleanup(ctx context.Context, req vmkit.Request, adapter runtimeAdapter, handle computeSystemHandle, started bool, detail string, cause error) (vmkit.Response, error) {
	cleanupDetail := detail
	if cleanupErr := cleanupComputeSystem(ctx, adapter, handle, started); cleanupErr != nil {
		cleanupDetail = fmt.Sprintf("%s; cleanup failed: %s", detail, cleanupErr)
	}
	event, writeErr := writeRuntimeTransitionWithComputeIDsNetwork(req, vmkit.StateFailed, cleanupDetail, cause.Error(), handle.ID, handle.RuntimeID, handle.NetworkID, handle.NetworkEndpointID)
	resp := vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: cause.Error()}
	if writeErr == nil {
		resp.Event = &event
	}
	return resp, cause
}

func cleanupComputeSystem(ctx context.Context, adapter runtimeAdapter, handle computeSystemHandle, started bool) error {
	if handle.ID == "" {
		return adapter.CleanupNetwork(ctx, runtimeState{NetworkID: handle.NetworkID, NetworkEndpointID: handle.NetworkEndpointID})
	}
	if started {
		if err := adapter.Kill(ctx, handle.ID); err != nil {
			return err
		}
	} else {
		if err := adapter.Delete(ctx, handle.ID); err != nil {
			return err
		}
	}
	return adapter.CleanupNetwork(ctx, runtimeState{NetworkID: handle.NetworkID, NetworkEndpointID: handle.NetworkEndpointID})
}

func validateNetwork(req vmkit.Request) error {
	if req.Config == nil || req.Config.Network == nil {
		return nil
	}
	network := *req.Config.Network
	if network.Mode == "bridged" && network.Interface == "" {
		return fmt.Errorf("bridged network requires network.interface")
	}
	return nil
}

func writeRuntimeTransition(req vmkit.Request, state vmkit.VMState, detail, errorText string) (vmkit.Event, error) {
	return writeRuntimeTransitionWithComputeID(req, state, detail, errorText, "")
}

func writeRuntimeTransitionWithComputeID(req vmkit.Request, state vmkit.VMState, detail, errorText, computeSystemID string) (vmkit.Event, error) {
	return writeRuntimeTransitionWithComputeIDs(req, state, detail, errorText, computeSystemID, "")
}

func writeRuntimeTransitionWithComputeIDs(req vmkit.Request, state vmkit.VMState, detail, errorText, computeSystemID, computeSystemRuntimeID string) (vmkit.Event, error) {
	return writeRuntimeTransitionWithComputeIDsNetworkAndListenerPID(req, state, detail, errorText, computeSystemID, computeSystemRuntimeID, "", "", 0)
}

func writeRuntimeTransitionWithComputeIDsAndListenerPID(req vmkit.Request, state vmkit.VMState, detail, errorText, computeSystemID, computeSystemRuntimeID string, vsockListenerPID int) (vmkit.Event, error) {
	return writeRuntimeTransitionWithComputeIDsNetworkAndListenerPID(req, state, detail, errorText, computeSystemID, computeSystemRuntimeID, "", "", vsockListenerPID)
}

func writeRuntimeTransitionWithComputeIDsNetwork(req vmkit.Request, state vmkit.VMState, detail, errorText, computeSystemID, computeSystemRuntimeID, networkID, networkEndpointID string) (vmkit.Event, error) {
	return writeRuntimeTransitionWithComputeIDsNetworkAndListenerPID(req, state, detail, errorText, computeSystemID, computeSystemRuntimeID, networkID, networkEndpointID, 0)
}

func writeRuntimeTransitionWithComputeIDsNetworkAndListenerPID(req vmkit.Request, state vmkit.VMState, detail, errorText, computeSystemID, computeSystemRuntimeID, networkID, networkEndpointID string, vsockListenerPID int) (vmkit.Event, error) {
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
	serialInput := ""
	if req.Config.SerialInput {
		serialInput = serialInputPath(req)
		inputFile, err := os.OpenFile(serialInput, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return vmkit.Event{}, fmt.Errorf("create serial input marker: %w", err)
		}
		if err := inputFile.Close(); err != nil {
			return vmkit.Event{}, fmt.Errorf("close serial input marker: %w", err)
		}
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
		if req.Config.SerialInput {
			readiness.ShellReady = vmkit.ReadinessSignal{Ready: true, ObservedAt: &now, Detail: "windows-hyperv shell socket is available"}
		}
	}
	if computeSystemID == "" {
		if previous, err := readRuntimeState(req); err == nil {
			computeSystemID = previous.ComputeSystemID
		}
	}
	if computeSystemRuntimeID == "" {
		if previous, err := readRuntimeState(req); err == nil {
			computeSystemRuntimeID = previous.ComputeSystemRuntimeID
		}
	}
	if networkID == "" {
		if previous, err := readRuntimeState(req); err == nil {
			networkID = previous.NetworkID
		}
	}
	if networkEndpointID == "" {
		if previous, err := readRuntimeState(req); err == nil {
			networkEndpointID = previous.NetworkEndpointID
		}
	}
	if vsockListenerPID == clearVsockListenerPID {
		vsockListenerPID = 0
	} else if vsockListenerPID == 0 {
		if previous, err := readRuntimeState(req); err == nil {
			vsockListenerPID = previous.VsockListenerPID
		}
	}
	runtime := runtimeState{
		Event:                  fileEvent,
		Config:                 *req.Config,
		ComputeSystemID:        computeSystemID,
		ComputeSystemRuntimeID: computeSystemRuntimeID,
		NetworkID:              networkID,
		NetworkEndpointID:      networkEndpointID,
		VsockListenerPID:       vsockListenerPID,
		SerialLogPath:          serialPath,
		SerialInputPath:        serialInput,
		StartedAt:              startedAt,
		UpdatedAt:              now.Format(time.RFC3339),
		Readiness:              readiness,
		Error:                  errorText,
	}
	if err := writeJSONFile(filepath.Join(dir, "runtime.json"), runtime); err != nil {
		return vmkit.Event{}, err
	}
	return event, nil
}

func hasDetachedRuntimeListeners(req vmkit.Request) bool {
	if req.Config == nil {
		return false
	}
	for _, listener := range req.Config.VsockListeners {
		if listener.Target != resultPath(req) {
			return true
		}
	}
	return false
}

func startRuntimeListenerProcess(req vmkit.Request) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := filepath.Join(runtimeDir(req), "hvsock-listener.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(executable, "--windows-hyperv-listener", "--state-dir", req.Config.StateDir, "--name", req.Identity.RuntimeID)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedListenerSysProcAttr()
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return pid, nil
}

func terminateRuntimeListenerProcess(pid int) {
	if pid <= 0 {
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = process.Kill()
	_ = process.Release()
}

func RunRuntimeListeners(ctx context.Context, opts Options) error {
	req, state, err := runtimeListenerRequest(opts)
	if err != nil {
		return err
	}
	handle := computeSystemHandle{ID: state.ComputeSystemID, RuntimeID: state.ComputeSystemRuntimeID}
	listeners, err := startRuntimeListenersHook(ctx, handle, req)
	if err != nil {
		return err
	}
	if listeners != nil {
		defer listeners.Close()
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- (Supervisor{}).runtimeAdapter().Wait(ctx, state.ComputeSystemID)
	}()
	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runtimeListenerRequest(opts Options) (vmkit.Request, runtimeState, error) {
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RuntimeID: opts.Name,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			StateDir: opts.StateDir,
		},
	}
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Request{}, runtimeState{}, err
	}
	req.Identity = &state.Event.Identity
	req.Config = &state.Config
	if state.ComputeSystemID == "" || state.ComputeSystemRuntimeID == "" {
		return vmkit.Request{}, runtimeState{}, fmt.Errorf("windows-hyperv runtime listener state is missing compute system IDs")
	}
	return req, state, nil
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

func serialInputPath(req vmkit.Request) string {
	return filepath.Join(runtimeDir(req), "serial.in")
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
