package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

const clearVsockListenerPID = -1

// HCS shutdown/terminate calls return once the operation is initiated, not
// when the compute system is unregistered. Terminal controls wait for the
// system to actually disappear so an immediate restart cannot collide with
// the old registration ("identifier already exists"). Variables so tests
// can shorten the bounds. The post-terminate bound is sized for loaded
// hosted CI runners, where HCS has been observed to take well over a minute
// to unregister a NAT-attached compute system after Terminate during a
// degraded-pool window (the endpoint release that normally unblocks it is
// itself async and slow under that load).
var (
	computeSystemShutdownGrace        = 15 * time.Second
	computeSystemTeardownTimeout      = 120 * time.Second
	computeSystemTeardownPollInterval = 200 * time.Millisecond
)

// listenerExitTimeout bounds how long apply waits for a terminated runtime
// listener helper to release its host binds before the replacement rebinds.
var listenerExitTimeout = 10 * time.Second

// waitForProcessExitHook lets tests substitute a deterministic exit wait.
var waitForProcessExitHook = waitForProcessExit

// computeSystemDescriber is the optional adapter diagnostic used when a
// compute system survives teardown: the raw HCS State distinguishes a
// terminate that never landed from a registration pinned by open handles
// or attachments.
type computeSystemDescriber interface {
	DescribeComputeSystem(ctx context.Context, id string) (string, error)
}

// waitForComputeSystemGone polls the HCS Exists probe until the compute
// system is unregistered or the timeout elapses.
func (s Supervisor) waitForComputeSystemGone(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		exists, err := s.runtimeAdapter().Exists(ctx, id)
		if err == nil && !exists {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("compute system %s teardown probe: %w", id, err)
			}
			// The system is still registered. Surface whatever HCS will say
			// about it — its State, or, when even Describe fails, that error —
			// so a teardown timeout is never a bare "still registered" with no
			// evidence of why (a terminate that never landed reads very
			// differently from a registration pinned by an open handle or a
			// slow async deregistration on a loaded host).
			if describer, ok := s.runtimeAdapter().(computeSystemDescriber); ok {
				props, describeErr := describer.DescribeComputeSystem(ctx, id)
				if describeErr != nil {
					return fmt.Errorf("compute system %s is still registered after teardown (HCS describe failed: %v)", id, describeErr)
				}
				if detail := computeSystemStateDetail(props); detail != "" {
					return fmt.Errorf("compute system %s is still registered after teardown (HCS state: %s)", id, detail)
				}
			}
			return fmt.Errorf("compute system %s is still registered after teardown", id)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(computeSystemTeardownPollInterval):
		}
	}
}

// computeSystemStateDetail extracts the State field from an HCS properties
// document; a document without one is reported raw (bounded) so the
// teardown diagnostic never hides what HCS said.
func computeSystemStateDetail(properties string) string {
	var doc struct {
		State string `json:"State"`
	}
	if err := json.Unmarshal([]byte(properties), &doc); err == nil && doc.State != "" {
		return doc.State
	}
	const maxRawDetail = 512
	trimmed := strings.TrimSpace(properties)
	if len(trimmed) > maxRawDetail {
		trimmed = trimmed[:maxRawDetail] + "..."
	}
	return trimmed
}

// teardownComputeSystem brings a compute system fully out of HCS
// registration. graceful first asks the guest to power off and gives it a
// bounded grace window — guests that ignore the request (no shutdown
// integration, init without power-signal handling) are then terminated,
// like every container runtime's stop semantics. A system that survives
// terminate fails the control instead of recording a terminal state the
// host does not match.
//
// releaseAttachments runs once terminate has been issued, before the
// unregistration wait: HCS has been observed (hosted CI runners) to keep a
// terminated compute system registered while its HNS endpoint is still
// attached, so terminal controls release network attachments first rather
// than waiting out a registration that cannot clear.
func (s Supervisor) teardownComputeSystem(ctx context.Context, id string, graceful bool, releaseAttachments func()) error {
	if graceful {
		err := s.runtimeAdapter().Shutdown(ctx, id)
		if err != nil && isMissingComputeSystem(err) {
			return nil
		}
		if err == nil {
			if waitErr := s.waitForComputeSystemGone(ctx, id, computeSystemShutdownGrace); waitErr == nil {
				return nil
			}
		} else if !isHCSShutdownUnsupported(err) {
			return err
		}
	}
	if err := s.runtimeAdapter().Kill(ctx, id); err != nil {
		if isMissingComputeSystem(err) {
			return nil
		}
		return err
	}
	if releaseAttachments != nil {
		releaseAttachments()
	}
	return s.waitForComputeSystemGone(ctx, id, computeSystemTeardownTimeout)
}

// isHCSShutdownUnsupported reports HCS errors that mean the compute system
// cannot take a guest shutdown request (no integration components running);
// the caller escalates to terminate instead of failing.
func isHCSShutdownUnsupported(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not supported") || strings.Contains(text, "0x80070032") ||
		strings.Contains(text, "integration services") || strings.Contains(text, "0xc0370110")
}

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
var waitForExecBridgeHook = waitForExecBridge

// execBridgeWaitTimeout bounds how long a detached start waits for the
// listener helper's exec bridge to accept before failing closed.
var execBridgeWaitTimeout = 3 * time.Second

func (s Supervisor) run(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	return s.startComputeSystem(ctx, req, true)
}

func (s Supervisor) prepare(req vmkit.Request) (vmkit.Response, error) {
	event, err := writeRuntimeTransition(req, vmkit.StatePrepared, "prepared windows-hyperv workspace", "")
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
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
	// Snapshot restore/fork: a request carrying a snapshot tag rolls the rootfs
	// back to the snapshot's coherent VHD copy and boots the compute system from
	// the saved memory/device state (VirtualMachine.RestoreState) rather than
	// cold-booting the kernel. Kernel/network compatibility is validated here,
	// once, before any compute system is created. Mirrors the firecracker
	// loadMode branch keyed off req.Tag.
	if strings.TrimSpace(req.Tag) != "" {
		vmStatePath, err := prepareSnapshotRestore(req)
		if err != nil {
			return failRun(req, vmkit.StateFailed, fmt.Sprintf("snapshot restore failed: %s", err), err)
		}
		spec.RestoreStateFilePath = vmStatePath
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
	// Hyper-V reserves dynamic TCP port ranges while the compute system and
	// its network are created, so the host exec bind is only chosen here,
	// after Create. The guest's hv_sock service keeps the original port (the
	// HCS service table and the guest run config were already built from it),
	// so a moved host bind stays coherent with the guest's own listener.
	ensureBindableManagementPorts(req.Config)
	spec.Config.ExecPort = req.Config.ExecPort
	spec.Config.GuestExecPort = req.Config.GuestExecPort
	listenerPID := 0
	var listeners runtimeListenerSet
	if foreground {
		listeners, err = startRuntimeListenersHook(ctx, handle, runtimeReq)
		if err != nil {
			return failRunWithCleanup(ctx, runtimeReq, adapter, handle, false, fmt.Sprintf("listener setup failed: %s", err), err)
		}
		if listeners != nil {
			defer func() { _ = listeners.Close() }()
		}
	} else if hasDetachedRuntimeServices(req) {
		if _, err := writeRuntimeTransitionWithComputeIDs(req, vmkit.StateStarting, "starting windows-hyperv runtime listener helper", "", handle.ID, handle.RuntimeID); err != nil {
			return vmkit.Response{}, err
		}
		listenerPID, err = startRuntimeListenerProcessHook(req)
		if err != nil {
			return failRunWithCleanup(ctx, req, adapter, handle, false, fmt.Sprintf("listener helper failed: %s", err), err)
		}
		if err := waitForExecBridgeHook(ctx, req); err != nil {
			terminateRuntimeListenerProcessHook(listenerPID)
			return failRunWithCleanup(ctx, req, adapter, handle, false, fmt.Sprintf("exec bridge failed: %s", err), err)
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

func (s Supervisor) inspect(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	// A guest that exits on its own takes its compute system with it, but
	// nothing rewrites runtime.json at that moment. Reconcile here so inspect
	// (and the supervise restart loop polling it) reports the truth instead
	// of a stale running state. A probe error leaves the recorded state
	// untouched: only a definite "gone" transitions the workspace, and the
	// final state follows the delivered guest result (failed on a non-zero
	// exit) so on-failure supervision restarts it, mirroring the firecracker
	// inspect reconcile.
	if (state.Event.State == vmkit.StateRunning || state.Event.State == vmkit.StateStarting) && state.ComputeSystemID != "" {
		if exists, existsErr := s.runtimeAdapter().Exists(ctx, state.ComputeSystemID); existsErr == nil && !exists {
			terminateRuntimeListenerProcessHook(state.VsockListenerPID)
			if cleanupErr := s.runtimeAdapter().CleanupNetwork(ctx, state); cleanupErr != nil {
				return failRun(req, vmkit.StateFailed, fmt.Sprintf("network cleanup failed: %s", cleanupErr), cleanupErr)
			}
			finalState, errorText := guestExitState(req, guestExitResultWait)
			if _, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(req, finalState, "windows-hyperv compute system exited", errorText, state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID); err != nil {
				return vmkit.Response{}, err
			}
			state, err = readRuntimeState(req)
			if err != nil {
				return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
			}
		}
	}
	event, err := eventFromFile(state.Event)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	readiness := runtimeReadinessForState(state)
	resp := vmkit.Response{OK: state.Event.State != vmkit.StateFailed, Backend: vmkit.BackendWindowsHyperV, Event: &event, Readiness: &readiness}
	if result, resultErr := readRuntimeResult(req, event.Identity); resultErr == nil {
		resp.Result = &result
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
	stopRuntimeListenerForTeardown(state.VsockListenerPID)
	if state.ComputeSystemID == "" {
		return s.transitionWithoutComputeSystem(ctx, req, state, vmkit.StateStopped, "windows-hyperv compute system stopped")
	}
	if err := s.teardownComputeSystem(ctx, state.ComputeSystemID, true, s.releaseNetworkAttachments(ctx, state)); err != nil {
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
	stopRuntimeListenerForTeardown(state.VsockListenerPID)
	if state.ComputeSystemID == "" {
		return s.transitionWithoutComputeSystem(ctx, req, state, vmkit.StateHalted, "windows-hyperv compute system halted")
	}
	if err := s.teardownComputeSystem(ctx, state.ComputeSystemID, true, s.releaseNetworkAttachments(ctx, state)); err != nil {
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

// pause freezes a running compute system's vCPUs in place. Unlike the
// teardown controls (halt/stop/kill), pause MUST NOT touch the runtime
// listener helper: the helper holds the compute system open via its exit Wait
// and brokers the exec/shell bridges, so it has to survive a pause for resume
// to thaw an intact workspace. Memory, disk, the compute system registration,
// the network endpoint, and the listener PID are all preserved.
func (s Supervisor) pause(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	return s.transitionComputeSystemState(ctx, req, vmkit.StateRunning, vmkit.StatePaused, "windows-hyperv compute system paused",
		func(id string) error { return s.runtimeAdapter().Pause(ctx, id) })
}

// resume thaws a paused compute system's vCPUs back to running, preserving the
// same compute IDs, network endpoint, and runtime listener PID that pause left
// intact.
func (s Supervisor) resume(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	return s.transitionComputeSystemState(ctx, req, vmkit.StatePaused, vmkit.StateRunning, "windows-hyperv compute system resumed",
		func(id string) error { return s.runtimeAdapter().Resume(ctx, id) })
}

// transitionComputeSystemState pauses or resumes the workspace in place. It
// requires the recorded state to be fromState and the compute system to be
// known, issues the HCS transition, and persists toState while PRESERVING the
// compute IDs and the runtime listener PID (never clearVsockListenerPID). On a
// transition error the persisted state is left untouched — the HCS pause/resume
// is atomic, so a failure means the system is still in fromState.
func (s Supervisor) transitionComputeSystemState(ctx context.Context, req vmkit.Request, fromState, toState vmkit.VMState, detail string, transition func(id string) error) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if state.Event.State != fromState {
		// Fail closed WITHOUT rewriting runtime.json: a pause/resume request
		// carries only a sparse config, so persisting here would clobber the
		// stored exec/shell ports and network. The workspace stays exactly as it
		// was; only the response reports the rejection.
		err := fmt.Errorf("windows-hyperv workspace %s is %s; %s requires state %s", req.Identity.RuntimeID, state.Event.State, req.Command, fromState)
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if state.ComputeSystemID == "" {
		err := fmt.Errorf("windows-hyperv workspace %s has no compute system to %s", req.Identity.RuntimeID, req.Command)
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if err := transition(state.ComputeSystemID); err != nil {
		// The HCS transition is atomic; on failure the compute system stays in
		// fromState, so leave the persisted state untouched rather than recording
		// a spurious failed state.
		resp := vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}
		return resp, err
	}
	// A pause/resume request carries only a sparse config (StateDir); persist the
	// stored runtime config so the resumed workspace keeps its exec/shell ports,
	// network, and readiness shape — only the StateDir comes from the request.
	runtimeReq := req
	storedConfig := state.Config
	storedConfig.StateDir = req.Config.StateDir
	runtimeReq.Config = &storedConfig
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(runtimeReq, toState, detail, "", state.ComputeSystemID, state.ComputeSystemRuntimeID, state.VsockListenerPID)
	if err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

// apply live-reloads host bind changes for existing port forwards on a
// running workspace. The forwards (and the exec bridge) live in the runtime
// listener helper, so the helper restarts with the updated network config —
// in-flight forwarded connections and exec sessions drop, exactly like the
// Firecracker port-forwarder restart, and the exec bridge is confirmed back
// before the apply reports success.
func (s Supervisor) apply(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if state.Event.State != vmkit.StateRunning {
		err := fmt.Errorf("windows-hyperv apply only live-reloads running workspaces")
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	if !sameGuestPortForwardShape(networkPortForwards(state.Config.Network), networkPortForwards(req.Config.Network)) {
		err := fmt.Errorf("windows-hyperv apply can only live-reload host bind changes for existing port forwards; stop and start the workspace for port, guest port, protocol, or network mode changes")
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	// The runtime ports (exec/shell binds chosen at start) stay; only the
	// network block changes. The new helper re-reads this state file.
	runtimeReq := req
	config := state.Config
	config.Network = req.Config.Network
	runtimeReq.Config = &config
	if hasDetachedRuntimeServices(runtimeReq) && state.VsockListenerPID == 0 {
		err := fmt.Errorf("windows-hyperv apply requires a detached workspace with a runtime listener helper; foreground runs hold their listeners in-process")
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	terminateRuntimeListenerProcessHook(state.VsockListenerPID)
	// The new helper rebinds the same exec port; wait for the old process to
	// release its sockets so the rebind cannot race into address-in-use.
	if state.VsockListenerPID > 0 {
		if err := waitForProcessExitHook(state.VsockListenerPID, listenerExitTimeout); err != nil {
			err = fmt.Errorf("windows-hyperv apply: old runtime listener helper did not exit: %w", err)
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
		}
	}
	listenerPID := clearVsockListenerPID
	if hasDetachedRuntimeServices(runtimeReq) {
		pid, err := startRuntimeListenerProcessHook(runtimeReq)
		if err != nil {
			detail := fmt.Sprintf("windows-hyperv apply: listener helper failed: %s", err)
			_, _ = writeRuntimeTransitionWithComputeIDsAndListenerPID(runtimeReq, vmkit.StateRunning, detail, err.Error(), state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID)
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: detail}, fmt.Errorf("%s", detail)
		}
		// The helper reads runtime.json, so the updated config must be on
		// disk before the exec bridge is probed.
		if _, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(runtimeReq, vmkit.StateRunning, "windows-hyperv network applying", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, pid); err != nil {
			terminateRuntimeListenerProcessHook(pid)
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
		}
		if err := waitForExecBridgeHook(ctx, runtimeReq); err != nil {
			terminateRuntimeListenerProcessHook(pid)
			detail := fmt.Sprintf("windows-hyperv apply: exec bridge failed: %s", err)
			_, _ = writeRuntimeTransitionWithComputeIDsAndListenerPID(runtimeReq, vmkit.StateRunning, detail, err.Error(), state.ComputeSystemID, state.ComputeSystemRuntimeID, clearVsockListenerPID)
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: detail}, fmt.Errorf("%s", detail)
		}
		listenerPID = pid
	}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(runtimeReq, vmkit.StateRunning, "windows-hyperv network applied", "", state.ComputeSystemID, state.ComputeSystemRuntimeID, listenerPID)
	if err != nil {
		return vmkit.Response{}, err
	}
	return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV, Event: &event}, nil
}

func networkPortForwards(network *vmkit.NetworkConfig) []vmkit.PortForward {
	if network == nil {
		return nil
	}
	return network.PortForwards
}

// sameGuestPortForwardShape reports whether two forward lists differ only in
// host bind addresses — the one change apply can deliver without a restart.
func sameGuestPortForwardShape(oldForwards, newForwards []vmkit.PortForward) bool {
	if len(oldForwards) != len(newForwards) {
		return false
	}
	for i := range oldForwards {
		oldForward := oldForwards[i]
		newForward := newForwards[i]
		oldProtocol := strings.TrimSpace(oldForward.Protocol)
		if oldProtocol == "" {
			oldProtocol = "tcp"
		}
		newProtocol := strings.TrimSpace(newForward.Protocol)
		if newProtocol == "" {
			newProtocol = "tcp"
		}
		if oldProtocol != newProtocol || oldForward.HostPort != newForward.HostPort || oldForward.GuestPort != newForward.GuestPort {
			return false
		}
	}
	return true
}

func (s Supervisor) quarantine(req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(req)
	if err != nil {
		return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
	}
	terminateRuntimeListenerProcessHook(state.VsockListenerPID)
	if state.ComputeSystemID == "" {
		return s.transitionWithoutComputeSystem(context.Background(), req, state, vmkit.StateQuarantined, "windows-hyperv compute system quarantined")
	}
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
	stopRuntimeListenerForTeardown(state.VsockListenerPID)
	if state.ComputeSystemID == "" {
		return s.transitionWithoutComputeSystem(ctx, req, state, vmkit.StateStopped, "windows-hyperv compute system killed")
	}
	if err := s.teardownComputeSystem(ctx, state.ComputeSystemID, false, s.releaseNetworkAttachments(ctx, state)); err != nil {
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
	stopRuntimeListenerForTeardown(state.VsockListenerPID)
	if state.ComputeSystemID != "" {
		deleted := true
		if err := s.runtimeAdapter().Delete(ctx, state.ComputeSystemID); err != nil {
			if !isMissingComputeSystem(err) {
				return failRun(req, vmkit.StateFailed, fmt.Sprintf("delete failed: %s", err), err)
			}
			deleted = false
		}
		// Release the HNS endpoint before waiting out unregistration: an
		// attached endpoint can pin a deleted compute system in the registry.
		if err := s.runtimeAdapter().CleanupNetwork(ctx, state); err != nil {
			return failRun(req, vmkit.StateFailed, fmt.Sprintf("network cleanup failed: %s", err), err)
		}
		if deleted {
			if err := s.waitForComputeSystemGone(ctx, state.ComputeSystemID, computeSystemTeardownTimeout); err != nil {
				return failRun(req, vmkit.StateFailed, fmt.Sprintf("delete failed: %s", err), err)
			}
		}
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

// stopRuntimeListenerForTeardown terminates the runtime listener helper and
// gives it a bounded window to actually exit. The helper holds an open HCS
// handle on the compute system (its exit Wait), and an open handle keeps a
// terminated system registered — tearing down before the helper is gone
// turns the unregistration wait into a race against process cleanup. The
// wait is best-effort: a helper that cannot be confirmed gone must not veto
// the teardown, which fails closed on unregistration itself.
func stopRuntimeListenerForTeardown(pid int) {
	terminateRuntimeListenerProcessHook(pid)
	if pid > 0 {
		_ = waitForProcessExitHook(pid, listenerExitTimeout)
	}
}

// releaseNetworkAttachments builds the teardown callback that deletes the
// workspace's HNS endpoint once terminate has been issued. Best-effort by
// design: the terminal controls re-run CleanupNetwork (idempotent) after
// teardown and fail closed there.
func (s Supervisor) releaseNetworkAttachments(ctx context.Context, state runtimeState) func() {
	if strings.TrimSpace(state.NetworkEndpointID) == "" {
		return nil
	}
	return func() {
		_ = s.runtimeAdapter().CleanupNetwork(ctx, state)
	}
}

func isMissingComputeSystem(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "compute system") && strings.Contains(text, "does not exist") ||
		strings.Contains(text, "virtual machine or container") && strings.Contains(text, "does not exist")
}

func isTerminalState(state vmkit.VMState) bool {
	switch state {
	case vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed, vmkit.StateQuarantined:
		return true
	default:
		return false
	}
}

func (s Supervisor) transitionWithoutComputeSystem(ctx context.Context, req vmkit.Request, state runtimeState, finalState vmkit.VMState, detail string) (vmkit.Response, error) {
	if state.NetworkEndpointID != "" {
		if err := s.runtimeAdapter().CleanupNetwork(ctx, state); err != nil {
			return failRun(req, vmkit.StateFailed, fmt.Sprintf("network cleanup failed: %s", err), err)
		}
	}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(req, finalState, detail, "", "", "", clearVsockListenerPID)
	if err != nil {
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
	if network.Mode == "named" && strings.TrimSpace(network.Name) == "" {
		return fmt.Errorf("named network requires network.name")
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return vmkit.Event{}, err
	}
	serialPath := serialLogPath(req)
	serialFile, err := os.OpenFile(serialPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return vmkit.Event{}, fmt.Errorf("create serial log: %w", err)
	}
	if err := serialFile.Close(); err != nil {
		return vmkit.Event{}, fmt.Errorf("close serial log: %w", err)
	}
	serialInput := ""
	if req.Config.SerialInput {
		serialInput = serialInputPath(req)
		inputFile, err := os.OpenFile(serialInput, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
	if err := appendEvent(filepath.Join(dir, "events.json"), fileEvent); err != nil {
		return vmkit.Event{}, err
	}
	startedAt := ""
	if state == vmkit.StateStarting || state == vmkit.StateRunning {
		startedAt = now.Format(time.RFC3339)
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
		Error:                  errorText,
	}
	runtime.Readiness = runtimeReadinessForState(runtime)
	if err := writeJSONFile(filepath.Join(dir, "runtime.json"), runtime); err != nil {
		return vmkit.Event{}, err
	}
	return event, nil
}

// runtimeReadinessForState reports readiness from the channels themselves:
// guest readiness from the recorded runtime state, shell readiness from an
// hv_sock dial of the guest shell service, exec readiness from a structured
// exec round-trip, and result readiness from result.json. It mirrors the
// Firecracker supervisor's readiness semantics so status output is
// backend-neutral.
func runtimeReadinessForState(state runtimeState) vmkit.RuntimeReadiness {
	readiness := vmkit.RuntimeReadiness{}
	if state.StartedAt != "" || state.Event.State == vmkit.StateRunning || state.Event.State == vmkit.StateHalted || state.Event.State == vmkit.StateStopped || state.Event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: firstEventTime(state.StartedAt, state.Event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(state.Event.State),
		}
	}
	if state.Event.State == vmkit.StateRunning && state.SerialInputPath != "" {
		if signal, ok := shellReadinessFromRuntimeState(state); ok {
			readiness.ShellReady = signal
		}
	}
	if signal, ok := execReadinessFromRuntimeState(state); ok {
		readiness.ExecReady = signal
	}
	resultFile := filepath.Join(state.Config.StateDir, state.Event.Identity.RuntimeID, "result.json")
	if info, err := os.Stat(resultFile); err == nil {
		modTime := info.ModTime().UTC()
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: &modTime,
			Detail:     "guest result is available",
		}
	} else if !os.IsNotExist(err) {
		readiness.ResultReady = vmkit.ReadinessSignal{Error: err.Error()}
	}
	return readiness
}

// shellReadinessFromRuntimeState probes the guest shell service over hv_sock.
// With no shell port configured, the serial input marker is the only console
// channel and its presence is the readiness signal.
func shellReadinessFromRuntimeState(state runtimeState) (vmkit.ReadinessSignal, bool) {
	if _, err := os.Stat(state.SerialInputPath); err != nil {
		if !os.IsNotExist(err) {
			return vmkit.ReadinessSignal{Error: err.Error()}, true
		}
		return vmkit.ReadinessSignal{}, false
	}
	if port := guestShellPort(state.Config); port != 0 {
		observedAt := time.Now().UTC()
		elapsed, err := shellHVSockProbeHook(context.Background(), state, 150*time.Millisecond)
		if err != nil {
			return vmkit.ReadinessSignal{
				Ready:      false,
				ObservedAt: &observedAt,
				Detail:     fmt.Sprintf("shell target unreachable at hvsock:%s:%d after %s: %v", state.ComputeSystemRuntimeID, port, elapsed.Round(time.Millisecond), err),
			}, true
		}
		return vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("shell target reachable at hvsock:%s:%d in %s", state.ComputeSystemRuntimeID, port, elapsed.Round(time.Millisecond)),
		}, true
	}
	observedAt := time.Now().UTC()
	if info, err := os.Stat(state.SerialInputPath); err == nil {
		modTime := info.ModTime().UTC()
		observedAt = modTime
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: &observedAt,
		Detail:     "console input is available",
	}, true
}

// execReadinessFromRuntimeState round-trips a probe command through the host
// exec bridge so readiness reports the structured exec channel actually
// answering, not just the compute system starting.
func execReadinessFromRuntimeState(state runtimeState) (vmkit.ReadinessSignal, bool) {
	if state.Event.State != vmkit.StateRunning {
		return vmkit.ReadinessSignal{}, false
	}
	observedAt := time.Now().UTC()
	if state.Config.ExecPort == 0 {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     "structured exec port is not configured",
		}, true
	}
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(state.Config.ExecPort)))
	req := execprotocol.NewExecRequest([]string{"true"})
	req.TimeoutMS = 2000
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	start := time.Now()
	result, err := execclient.New(target).Exec(ctx, req)
	cancel()
	elapsed := time.Since(start)
	if err != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service unreachable at %s after %s: %v", target, elapsed.Round(time.Millisecond), err),
		}, true
	}
	if result.Error != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service returned %s: %s", result.Error.Code, result.Error.Message),
			Error:      result.Error.Error(),
		}, true
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		exit := "nil"
		if result.ExitCode != nil {
			exit = strconv.Itoa(*result.ExitCode)
		}
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec probe command failed unexpectedly: status=%s exit_code=%s", result.Status, exit),
		}, true
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: &observedAt,
		Detail:     fmt.Sprintf("exec service round-trip ready at %s in %s", target, elapsed.Round(time.Millisecond)),
	}, true
}

// guestShellPort and guestExecPort return the in-guest vsock service port for
// the shell and exec services. They differ from the host bind ports only when
// the request records explicit guest ports.
func guestShellPort(config vmkit.Config) uint16 {
	if config.GuestShellPort != 0 {
		return config.GuestShellPort
	}
	return config.ShellPort
}

func guestExecPort(config vmkit.Config) uint16 {
	if config.GuestExecPort != 0 {
		return config.GuestExecPort
	}
	return config.ExecPort
}

func firstEventTime(values ...string) *time.Time {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return &parsed
		}
	}
	return nil
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

func hasDetachedRuntimeServices(req vmkit.Request) bool {
	return hasDetachedRuntimeListeners(req) || hasPortForwards(req.Config) || hasExecBridge(req.Config)
}

// ensureBindableManagementPorts moves the host bind for the structured exec
// bridge off any unbindable port — most notably one transiently held by an
// ephemeral outbound connection, since the default exec port range overlaps
// the Windows dynamic TCP range — onto a free port, preserving the original
// as the guest vsock port so the bridge and the guest's own listener still
// agree. User port-forwards are intentionally left untouched: those ports are
// operator intent and a conflict there should surface, not be silently
// reassigned.
func ensureBindableManagementPorts(config *vmkit.Config) {
	if config == nil || config.ExecPort == 0 {
		return
	}
	if port, changed := bindableHostPort(config.ExecPort); changed {
		if config.GuestExecPort == 0 {
			config.GuestExecPort = config.ExecPort
		}
		config.ExecPort = port
	}
}

// bindableHostPort returns a host port that can actually be bound on
// 127.0.0.1. If the preferred port binds it is returned unchanged; otherwise
// an OS-assigned free port is returned. The bool reports whether the port
// changed.
func bindableHostPort(preferred uint16) (uint16, bool) {
	if preferred == 0 {
		return 0, false
	}
	if l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(preferred)))); err == nil {
		_ = l.Close()
		return preferred, false
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// Could not secure any port; leave the preferred port in place and let
		// the listener surface the original bind error.
		return preferred, false
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return port, true
}

// waitForExecBridge confirms the detached listener helper's exec bridge is
// accepting on the host before the workspace is reported running, so a helper
// that died on startup (for example, a port bind failure) fails the start
// instead of leaving structured exec silently dead.
func waitForExecBridge(ctx context.Context, req vmkit.Request) error {
	if !hasExecBridge(req.Config) {
		return nil
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(req.Config.ExecPort)))
	deadline := time.Now().Add(execBridgeWaitTimeout)
	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	detail := fmt.Sprintf("structured exec bridge did not accept on %s within %s: %v", addr, execBridgeWaitTimeout, lastErr)
	if executable, err := os.Executable(); err == nil {
		detail = fmt.Sprintf("%s (the runtime listener helper runs %q --windows-hyperv-listener; detached starts must run from a microagent CLI binary)", detail, executable)
	}
	if log, err := os.ReadFile(filepath.Join(runtimeDir(req), "hvsock-listener.log")); err == nil {
		if trimmed := strings.TrimSpace(string(log)); trimmed != "" {
			lines := strings.Split(trimmed, "\n")
			detail = fmt.Sprintf("%s; helper log: %s", detail, strings.TrimSpace(lines[len(lines)-1]))
		}
	}
	return fmt.Errorf("%s", detail)
}

func hasExecBridge(config *vmkit.Config) bool {
	return config != nil && config.ExecPort != 0
}

func hasPortForwards(config *vmkit.Config) bool {
	return config != nil && config.Network != nil && len(config.Network.PortForwards) != 0
}

func startRuntimeListenerProcess(req vmkit.Request) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := filepath.Join(runtimeDir(req), "hvsock-listener.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
		defer func() { _ = listeners.Close() }()
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

// runtimeStateReadRetries bounds the transient-read retry on runtime.json.
// The supervisor rewrites runtime.json atomically (temp file + rename); on
// Windows a concurrent reader — most often the freshly started runtime
// listener helper reading its config while apply rewrites the file — can hit
// "the process cannot access the file because it is being used by another
// process" (a sharing violation) during the rename window. Variables so tests
// can shorten the bound.
var (
	runtimeStateReadRetries     = 25
	runtimeStateReadRetryBackup = 20 * time.Millisecond
)

// readRuntimeStateFile reads runtime.json, retrying briefly on a transient
// read error so a concurrent atomic rewrite does not surface as a hard
// failure. A genuinely missing file returns immediately so the many
// not-exist callers stay fast.
func readRuntimeStateFile(path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < runtimeStateReadRetries; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
		if os.IsNotExist(err) {
			return nil, err
		}
		lastErr = err
		time.Sleep(runtimeStateReadRetryBackup)
	}
	return nil, lastErr
}

func readRuntimeState(req vmkit.Request) (runtimeState, error) {
	var state runtimeState
	data, err := readRuntimeStateFile(filepath.Join(runtimeDir(req), "runtime.json"))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

// guestExitResultWait bounds how long the inspect reconcile waits for the
// result file after the compute system is gone: the listener helper may still
// be flushing the guest's final result. A variable so tests can shorten it.
var guestExitResultWait = 2 * time.Second

// guestExitState classifies a guest self-exit by its delivered result,
// mirroring the firecracker guestHaltedState contract: a missing result or a
// zero exit code is a clean stop; a non-zero exit is a failure, so on-failure
// supervision restarts the workspace. The result file is the guest schema
// (snake_case), written by the runtime listener helper.
func guestExitState(req vmkit.Request, waitForResult time.Duration) (vmkit.VMState, string) {
	path := resultPath(req)
	deadline := time.Now().Add(waitForResult)
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			if waitForResult <= 0 || !os.IsNotExist(err) || time.Now().After(deadline) {
				return vmkit.StateStopped, ""
			}
		} else {
			var result struct {
				ExitCode int    `json:"exit_code"`
				Error    string `json:"error"`
			}
			if jsonErr := json.Unmarshal(data, &result); jsonErr == nil {
				if result.ExitCode == 0 {
					return vmkit.StateStopped, ""
				}
				if result.Error != "" {
					return vmkit.StateFailed, result.Error
				}
				return vmkit.StateFailed, fmt.Sprintf("guest exited with code %d", result.ExitCode)
			} else if waitForResult <= 0 || time.Now().After(deadline) {
				return vmkit.StateFailed, jsonErr.Error()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readRuntimeResult(req vmkit.Request, identity vmkit.Identity) (vmkit.RuntimeResult, error) {
	path := resultPath(req)
	data, err := os.ReadFile(path)
	if err != nil {
		return vmkit.RuntimeResult{}, err
	}
	// The result file holds the raw guest result (snake_case schema written
	// by the runtime listener helper), not the vmkit wire form; parse the
	// guest schema and map it so exit codes and output survive intact.
	var guest struct {
		StartedAt string `json:"started_at"`
		ExitedAt  string `json:"exited_at"`
		ExitCode  int    `json:"exit_code"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(data, &guest); err != nil {
		return vmkit.RuntimeResult{}, err
	}
	return vmkit.RuntimeResult{
		Identity:    identity,
		Backend:     vmkit.BackendWindowsHyperV,
		ResultPath:  path,
		StartedAt:   guest.StartedAt,
		CompletedAt: guest.ExitedAt,
		ExitCode:    guest.ExitCode,
		Stdout:      guest.Stdout,
		Stderr:      guest.Stderr,
		Error:       guest.Error,
	}, nil
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
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// appendEvent maintains events.json as a JSON array of EventFile, capped and
// atomically rewritten on each transition — the backend-neutral history shape
// the events CLI reads (same semantics as the Firecracker supervisor).
func appendEvent(path string, event eventFile) error {
	const maxEvents = 1024
	var events []eventFile
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && len(strings.TrimSpace(string(data))) != 0 {
		if unmarshalErr := json.Unmarshal(data, &events); unmarshalErr != nil {
			// Workspaces created before the JSON-array history migrate from
			// the legacy JSON-lines format on first touch; without this,
			// every lifecycle control on an old workspace fails at the
			// event append and the workspace becomes uncontrollable.
			migrated, migrateErr := eventsFromJSONLines(data)
			if migrateErr != nil {
				return fmt.Errorf("event history is malformed (not a JSON array or legacy JSON lines): %w", unmarshalErr)
			}
			events = migrated
		}
	}
	events = append(events, event)
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	return writeJSONFile(path, events)
}

// eventsFromJSONLines parses the pre-array event history: one JSON event
// object per line.
func eventsFromJSONLines(data []byte) ([]eventFile, error) {
	var events []eventFile
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event eventFile
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
