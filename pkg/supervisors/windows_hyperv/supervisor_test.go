package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestHostResponseReportsBackend(t *testing.T) {
	resp := HostResponse()
	if resp.Backend != vmkit.BackendWindowsHyperV || resp.Host == nil || resp.Kernel == nil {
		t.Fatalf("HostResponse = %#v", resp)
	}
	if resp.Host.Backend != vmkit.BackendWindowsHyperV || resp.Kernel.Backend != vmkit.BackendWindowsHyperV {
		t.Fatalf("backend fields = %#v %#v", resp.Host, resp.Kernel)
	}
	if runtime.GOOS == "windows" && !resp.OK {
		t.Fatalf("windows host response not OK: %#v", resp)
	}
	if runtime.GOOS != "windows" && resp.OK {
		t.Fatalf("non-windows host response OK: %#v", resp)
	}
}

func TestCheckCommandFailsClosedWhenHostProbeFails(t *testing.T) {
	req := vmkit.Request{
		Command: "check",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/Image",
			RootfsPath: "/tmp/rootfs.vhd",
			StateDir:   t.TempDir(),
		},
	}
	resp, err := (Supervisor{}).Do(context.Background(), req)
	if runtime.GOOS == "windows" {
		if err == nil && resp.OK {
			return
		}
		if err == nil || resp.OK || !strings.Contains(resp.Error, "windows-hyperv HCS") {
			t.Fatalf("windows check resp=%#v err=%v", resp, err)
		}
		return
	}
	if err == nil || resp.OK || !strings.Contains(resp.Error, "only supported on windows") {
		t.Fatalf("non-windows check resp=%#v err=%v", resp, err)
	}
}

func TestLifecycleCommandsFailClosedWithoutRunnableHCSGuest(t *testing.T) {
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/Image",
			RootfsPath: "/tmp/rootfs.vhd",
			StateDir:   t.TempDir(),
		},
	}
	resp, err := (Supervisor{}).Do(context.Background(), req)
	if err == nil || resp.OK {
		t.Fatalf("run resp=%#v err=%v, want fail-closed error", resp, err)
	}
	if runtime.GOOS == "windows" && !strings.Contains(resp.Error, "windows-hyperv HCS") {
		t.Fatalf("windows error = %q", resp.Error)
	}
	if runtime.GOOS != "windows" && !strings.Contains(resp.Error, "only supported on windows") {
		t.Fatalf("non-windows error = %q", resp.Error)
	}
}

func TestRunCommandUsesAdapterAndWritesRuntimeState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv run path is windows-only")
	}
	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath:  "C:\\microagent\\Image",
			RootfsPath:  "C:\\microagent\\rootfs.vhd",
			StateDir:    stateDir,
			MemoryMiB:   512,
			CPUCount:    2,
			SerialInput: true,
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	if adapter.creates != 1 || adapter.starts != 1 || adapter.startedID != "fake" {
		t.Fatalf("adapter calls creates=%d starts=%d startedID=%q", adapter.creates, adapter.starts, adapter.startedID)
	}
	if adapter.spec.Name != "agent-1" || adapter.spec.Config.RootfsPath != req.Config.RootfsPath {
		t.Fatalf("adapter spec = %#v", adapter.spec)
	}

	var event struct {
		Identity vmkit.Identity `json:"identity"`
		State    vmkit.VMState  `json:"state"`
		Detail   string         `json:"detail"`
	}
	readJSON(t, filepath.Join(stateDir, "agent-1", "event.json"), &event)
	if event.Identity.RuntimeID != "agent-1" || event.State != vmkit.StateRunning || !strings.Contains(event.Detail, "serial=") {
		t.Fatalf("event.json = %#v", event)
	}

	var runtimeState struct {
		Event struct {
			State vmkit.VMState `json:"state"`
		} `json:"event"`
		Config                 vmkit.Config           `json:"config"`
		ComputeSystemID        string                 `json:"computeSystemID"`
		ComputeSystemRuntimeID string                 `json:"computeSystemRuntimeID"`
		SerialLogPath          string                 `json:"serialLogPath"`
		SerialInputPath        string                 `json:"serialInputPath"`
		Readiness              vmkit.RuntimeReadiness `json:"readiness"`
	}
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &runtimeState)
	if runtimeState.Event.State != vmkit.StateRunning || runtimeState.Config.RootfsPath != req.Config.RootfsPath {
		t.Fatalf("runtime.json = %#v", runtimeState)
	}
	if runtimeState.ComputeSystemID != "fake" || runtimeState.ComputeSystemRuntimeID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("runtime compute IDs = %q %q", runtimeState.ComputeSystemID, runtimeState.ComputeSystemRuntimeID)
	}
	if runtimeState.SerialLogPath != filepath.Join(stateDir, "agent-1", "serial.log") {
		t.Fatalf("serialLogPath = %q", runtimeState.SerialLogPath)
	}
	if runtimeState.SerialInputPath != filepath.Join(stateDir, "agent-1", "serial.in") {
		t.Fatalf("serialInputPath = %q", runtimeState.SerialInputPath)
	}
	if !runtimeState.Readiness.GuestReady.Ready || !strings.Contains(runtimeState.Readiness.GuestReady.Detail, "workspace reached runtime state running") {
		t.Fatalf("guest readiness = %#v, want state-signaled running", runtimeState.Readiness.GuestReady)
	}
	if !runtimeState.Readiness.ShellReady.Ready || !strings.Contains(runtimeState.Readiness.ShellReady.Detail, "console input is available") {
		t.Fatalf("shell readiness = %#v, want console input fallback without a shell port", runtimeState.Readiness.ShellReady)
	}
	if runtimeState.Readiness.ExecReady.Ready || !strings.Contains(runtimeState.Readiness.ExecReady.Detail, "structured exec port is not configured") {
		t.Fatalf("exec readiness = %#v, want not configured without an exec port", runtimeState.Readiness.ExecReady)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent-1", "serial.log")); err != nil {
		t.Fatalf("serial.log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent-1", "serial.in")); err != nil {
		t.Fatalf("serial.in: %v", err)
	}
	var history []eventFile
	readJSON(t, filepath.Join(stateDir, "agent-1", "events.json"), &history)
	if len(history) != 2 || history[0].State != vmkit.StateStarting || history[1].State != vmkit.StateRunning {
		t.Fatalf("events.json history = %#v, want starting and running events", history)
	}
}

func TestPrepareCommandWritesPreparedRuntimeStateWithoutCreatingComputeSystem(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv prepare path is windows-only")
	}
	stateDir := t.TempDir()
	adapter := &fakeAdapter{}
	req := vmkit.Request{
		Command: "prepare",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath:  "C:\\microagent\\Image",
			RootfsPath:  "C:\\microagent\\rootfs.vhd",
			StateDir:    stateDir,
			MemoryMiB:   512,
			CPUCount:    2,
			SerialInput: true,
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StatePrepared {
		t.Fatalf("prepare resp=%#v err=%v", resp, err)
	}
	if adapter.creates != 0 || adapter.starts != 0 || adapter.networks != 0 {
		t.Fatalf("prepare touched HCS adapter: creates=%d starts=%d networks=%d", adapter.creates, adapter.starts, adapter.networks)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.Event.State != vmkit.StatePrepared || state.Config.RootfsPath != req.Config.RootfsPath {
		t.Fatalf("runtime state = %#v", state)
	}
	if state.ComputeSystemID != "" || state.ComputeSystemRuntimeID != "" {
		t.Fatalf("prepared compute IDs = %q %q, want empty", state.ComputeSystemID, state.ComputeSystemRuntimeID)
	}
	if state.SerialLogPath != filepath.Join(stateDir, "agent-1", "serial.log") {
		t.Fatalf("serialLogPath = %q", state.SerialLogPath)
	}
	if state.SerialInputPath != filepath.Join(stateDir, "agent-1", "serial.in") {
		t.Fatalf("serialInputPath = %q", state.SerialInputPath)
	}
	if state.Readiness.GuestReady.Ready || state.Readiness.ShellReady.Ready {
		t.Fatalf("prepared readiness = %#v, want not ready", state.Readiness)
	}
}

func TestDeletePreparedWindowsHyperVStateDoesNotDeleteComputeSystem(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	stateDir := t.TempDir()
	prepareReq := vmkit.Request{
		Command: "prepare",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
		},
	}
	if _, err := writeRuntimeTransition(prepareReq, vmkit.StatePrepared, "prepared windows-hyperv workspace", ""); err != nil {
		t.Fatalf("write prepared state: %v", err)
	}
	adapter := &fakeAdapter{}
	deleteReq := prepareReq
	deleteReq.Command = "delete"
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), deleteReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("delete resp=%#v err=%v", resp, err)
	}
	if adapter.deletes != 0 || adapter.cleanups != 0 {
		t.Fatalf("prepared delete touched HCS adapter: deletes=%d cleanups=%d", adapter.deletes, adapter.cleanups)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent-1")); !os.IsNotExist(err) {
		t.Fatalf("runtime dir after prepared delete err=%v, want removed", err)
	}
}

func TestDeleteStoppedWindowsHyperVMissingComputeSystemSucceeds(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	stateDir := t.TempDir()
	startReq := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
		},
	}
	if _, err := writeRuntimeTransitionWithComputeIDs(startReq, vmkit.StateStopped, "windows-hyperv compute system killed", "", "fake", "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("write stopped state: %v", err)
	}
	adapter := &fakeAdapter{deleteErr: fmt.Errorf("windows-hyperv HCS delete open: A virtual machine or container with the specified identifier does not exist.")}
	deleteReq := startReq
	deleteReq.Command = "delete"
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), deleteReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("delete resp=%#v err=%v", resp, err)
	}
	if adapter.deletes != 1 || adapter.deleteID != "fake" {
		t.Fatalf("deletes=%d deleteID=%q, want one attempted delete", adapter.deletes, adapter.deleteID)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent-1")); !os.IsNotExist(err) {
		t.Fatalf("runtime dir after delete err=%v, want removed", err)
	}
}

func TestTerminalWindowsHyperVControlToleratesMissingComputeSystem(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	tests := []struct {
		name         string
		initialState vmkit.VMState
		command      string
		finalState   vmkit.VMState
	}{
		{name: "stop stopped", initialState: vmkit.StateStopped, command: "stop", finalState: vmkit.StateStopped},
		{name: "halt halted", initialState: vmkit.StateHalted, command: "halt", finalState: vmkit.StateHalted},
		{name: "kill stopped", initialState: vmkit.StateStopped, command: "kill", finalState: vmkit.StateStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := t.TempDir()
			req := vmkit.Request{
				Command: "start",
				Identity: &vmkit.Identity{
					RequestID: "req-1",
					RuntimeID: "agent-1",
					Role:      vmkit.RoleWorkload,
					Backend:   vmkit.BackendWindowsHyperV,
				},
				Config: &vmkit.Config{
					KernelPath: "C:\\microagent\\Image",
					RootfsPath: "C:\\microagent\\rootfs.vhd",
					StateDir:   stateDir,
				},
			}
			if _, err := writeRuntimeTransitionWithComputeIDs(req, tt.initialState, "terminal state", "", "fake", "11111111-1111-1111-1111-111111111111"); err != nil {
				t.Fatalf("write terminal state: %v", err)
			}
			adapter := &fakeAdapter{
				shutdownErr: fmt.Errorf("windows-hyperv HCS shutdown open: A virtual machine or container with the specified identifier does not exist."),
				killErr:     fmt.Errorf("windows-hyperv HCS kill open: A virtual machine or container with the specified identifier does not exist."),
			}
			controlReq := req
			controlReq.Command = tt.command
			resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), controlReq)
			if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != tt.finalState {
				t.Fatalf("%s resp=%#v err=%v", tt.command, resp, err)
			}
		})
	}
}

func TestPreparedWindowsHyperVControlCommandsDoNotTouchComputeSystem(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	tests := []struct {
		command string
		state   vmkit.VMState
	}{
		{command: "stop", state: vmkit.StateStopped},
		{command: "halt", state: vmkit.StateHalted},
		{command: "kill", state: vmkit.StateStopped},
		{command: "quarantine", state: vmkit.StateQuarantined},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			stateDir := t.TempDir()
			prepareReq := vmkit.Request{
				Command: "prepare",
				Identity: &vmkit.Identity{
					RequestID: "req-1",
					RuntimeID: "agent-1",
					Role:      vmkit.RoleWorkload,
					Backend:   vmkit.BackendWindowsHyperV,
				},
				Config: &vmkit.Config{
					KernelPath: "C:\\microagent\\Image",
					RootfsPath: "C:\\microagent\\rootfs.vhd",
					StateDir:   stateDir,
				},
			}
			if _, err := writeRuntimeTransition(prepareReq, vmkit.StatePrepared, "prepared windows-hyperv workspace", ""); err != nil {
				t.Fatalf("write prepared state: %v", err)
			}
			adapter := &fakeAdapter{}
			req := prepareReq
			req.Command = tt.command
			resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
			if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != tt.state {
				t.Fatalf("%s resp=%#v err=%v", tt.command, resp, err)
			}
			if adapter.shutdowns != 0 || adapter.kills != 0 || adapter.cleanups != 0 {
				t.Fatalf("%s touched HCS adapter: shutdowns=%d kills=%d cleanups=%d", tt.command, adapter.shutdowns, adapter.kills, adapter.cleanups)
			}
		})
	}
}

func TestStartCommandRecordsRuntimeNetworkState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv start path is windows-only")
	}
	stateDir := t.TempDir()
	adapter := &fakeAdapter{
		handle: computeSystemHandle{
			ID:        "fake",
			RuntimeID: "11111111-1111-1111-1111-111111111111",
		},
		network: networkAttachment{
			NetworkID:         "network-1",
			NetworkEndpointID: "endpoint-1",
			RuntimeNetwork: &vmkit.NetworkConfig{
				Mode:    "nat",
				IP:      "192.168.127.2",
				Subnet:  "192.168.127.0/24",
				Gateway: "192.168.127.1",
				DNS:     []string{"192.168.127.1"},
				Routes:  []string{"0.0.0.0/0"},
			},
		},
	}
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
			Network:    &vmkit.NetworkConfig{Mode: "nat"},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("start resp=%#v err=%v", resp, err)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.NetworkID != "network-1" || state.NetworkEndpointID != "endpoint-1" {
		t.Fatalf("runtime network IDs = %q %q", state.NetworkID, state.NetworkEndpointID)
	}
	if state.Config.Network == nil || state.Config.Network.Mode != "nat" || state.Config.Network.IP != "192.168.127.2" || state.Config.Network.Gateway != "192.168.127.1" {
		t.Fatalf("runtime network = %#v", state.Config.Network)
	}
}

func TestStartCommandRejectsWindowsHyperVBridgedWithoutInterface(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv start path is windows-only")
	}
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   t.TempDir(),
			Network:    &vmkit.NetworkConfig{Mode: "bridged"},
		},
	}
	resp, err := (Supervisor{adapter: &fakeAdapter{}}).Do(context.Background(), req)
	if err == nil || resp.OK || !strings.Contains(resp.Error, "bridged network requires network.interface") {
		t.Fatalf("start resp=%#v err=%v", resp, err)
	}
}

func TestRunCommandWaitsForResultListenerAndReturnsResult(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv run path is windows-only")
	}
	oldHook := startRuntimeListenersHook
	t.Cleanup(func() { startRuntimeListenersHook = oldHook })
	startRuntimeListenersHook = func(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
		return fakeListenerSet{wait: func() error {
			return os.WriteFile(filepath.Join(req.Config.StateDir, req.Identity.RuntimeID, "result.json"), []byte(`{"exitCode":0,"stdout":"ok\n"}`+"\n"), 0o644)
		}}, nil
	}

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
			VsockListeners: []vmkit.VsockListener{
				{Port: 1024, Target: filepath.Join(stateDir, "agent-1", "result.json")},
				{Port: 2048, Target: "127.0.0.1:9900"},
			},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	if resp.Result == nil || resp.Result.Stdout != "ok\n" || resp.Result.Backend != vmkit.BackendWindowsHyperV {
		t.Fatalf("result = %#v", resp.Result)
	}
	if adapter.waits != 1 || adapter.waitID != "fake" {
		t.Fatalf("waits=%d waitID=%q", adapter.waits, adapter.waitID)
	}
}

func TestRunCommandCleansUpNetworkAfterForegroundResult(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv run path is windows-only")
	}
	oldHook := startRuntimeListenersHook
	t.Cleanup(func() { startRuntimeListenersHook = oldHook })
	startRuntimeListenersHook = func(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
		return fakeListenerSet{wait: func() error {
			return os.WriteFile(filepath.Join(req.Config.StateDir, req.Identity.RuntimeID, "result.json"), []byte(`{"exitCode":0}`+"\n"), 0o644)
		}}, nil
	}

	stateDir := t.TempDir()
	adapter := &fakeAdapter{
		handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"},
		network: networkAttachment{
			NetworkID:         "network-1",
			NetworkEndpointID: "endpoint-1",
			RuntimeNetwork:    &vmkit.NetworkConfig{Mode: "nat"},
		},
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath:     "C:\\microagent\\Image",
			RootfsPath:     "C:\\microagent\\rootfs.vhd",
			StateDir:       stateDir,
			Network:        &vmkit.NetworkConfig{Mode: "nat"},
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(stateDir, "agent-1", "result.json")}},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	if adapter.cleanups != 1 {
		t.Fatalf("network cleanups = %d, want 1", adapter.cleanups)
	}
}

func TestStartCommandDoesNotWaitForResultListener(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv start path is windows-only")
	}
	oldHook := startRuntimeListenersHook
	oldProcessHook := startRuntimeListenerProcessHook
	t.Cleanup(func() {
		startRuntimeListenersHook = oldHook
		startRuntimeListenerProcessHook = oldProcessHook
	})
	waited := false
	startRuntimeListenersHook = func(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
		t.Fatal("detached start must use helper process instead of in-process listener")
		return nil, nil
	}
	startRuntimeListenerProcessHook = func(req vmkit.Request) (int, error) {
		return fakeListenerSet{wait: func() error {
			waited = true
			return fmt.Errorf("start must not wait for result")
		}}.pid(), nil
	}

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
			VsockListeners: []vmkit.VsockListener{
				{Port: 1024, Target: filepath.Join(stateDir, "agent-1", "result.json")},
				{Port: 2048, Target: "127.0.0.1:9900"},
			},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("start resp=%#v err=%v", resp, err)
	}
	if waited {
		t.Fatal("start waited for result listener")
	}
	if adapter.waits != 0 {
		t.Fatalf("adapter waits = %d, want 0", adapter.waits)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.Event.State != vmkit.StateRunning || state.ComputeSystemID != "fake" || state.ComputeSystemRuntimeID == "" || state.VsockListenerPID == 0 {
		t.Fatalf("runtime state = %#v", state)
	}
}

func TestStartCommandDoesNotLaunchListenerHelperForResultOnlyListener(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv start path is windows-only")
	}
	oldProcessHook := startRuntimeListenerProcessHook
	t.Cleanup(func() { startRuntimeListenerProcessHook = oldProcessHook })
	startRuntimeListenerProcessHook = func(req vmkit.Request) (int, error) {
		t.Fatal("result-only detached start should not launch listener helper")
		return 0, nil
	}

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath:     "C:\\microagent\\Image",
			RootfsPath:     "C:\\microagent\\rootfs.vhd",
			StateDir:       stateDir,
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(stateDir, "agent-1", "result.json")}},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("start resp=%#v err=%v", resp, err)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.VsockListenerPID != 0 {
		t.Fatalf("result-only VsockListenerPID = %d, want 0", state.VsockListenerPID)
	}
}

func TestStartCommandLaunchesListenerHelperForPublishedPortForwards(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv start path is windows-only")
	}
	oldProcessHook := startRuntimeListenerProcessHook
	t.Cleanup(func() { startRuntimeListenerProcessHook = oldProcessHook })
	helperStarted := false
	startRuntimeListenerProcessHook = func(req vmkit.Request) (int, error) {
		helperStarted = true
		return 4321, nil
	}

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
			Network: &vmkit.NetworkConfig{
				Mode:         "nat",
				PortForwards: []vmkit.PortForward{{Protocol: "tcp", Host: "127.0.0.1", HostPort: 18080, GuestPort: 8080}},
			},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("start resp=%#v err=%v", resp, err)
	}
	if !helperStarted {
		t.Fatal("listener helper was not started for published port forwards")
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.VsockListenerPID != 4321 {
		t.Fatalf("VsockListenerPID = %d, want helper pid 4321", state.VsockListenerPID)
	}
}

func TestStopTerminatesRuntimeListenerProcess(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	oldTerminateHook := terminateRuntimeListenerProcessHook
	t.Cleanup(func() { terminateRuntimeListenerProcessHook = oldTerminateHook })
	var terminated []int
	terminateRuntimeListenerProcessHook = func(pid int) {
		terminated = append(terminated, pid)
	}

	stateDir := t.TempDir()
	req := vmkit.Request{
		Command: "stop",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			StateDir: stateDir,
		},
	}
	startReq := req
	startReq.Command = "start"
	startReq.Config.KernelPath = "C:\\microagent\\Image"
	startReq.Config.RootfsPath = "C:\\microagent\\rootfs.vhd"
	startReq.Config.VsockListeners = []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(stateDir, "agent-1", "result.json")}}
	event, err := writeRuntimeTransitionWithComputeIDsAndListenerPID(startReq, vmkit.StateRunning, "serial="+serialLogPath(startReq), "", "fake", "11111111-1111-1111-1111-111111111111", 4321)
	if err != nil || event.State != vmkit.StateRunning {
		t.Fatalf("writeRuntimeTransitionWithComputeIDsAndListenerPID event=%#v err=%v", event, err)
	}
	adapter := &fakeAdapter{}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("stop resp=%#v err=%v", resp, err)
	}
	if len(terminated) != 1 || terminated[0] != 4321 {
		t.Fatalf("terminated listener pids = %#v, want [4321]", terminated)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.VsockListenerPID != 0 {
		t.Fatalf("VsockListenerPID after stop = %d, want 0", state.VsockListenerPID)
	}
}

func TestStopCleansUpRuntimeNetworkState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	stateDir := t.TempDir()
	req := vmkit.Request{
		Command: "stop",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{StateDir: stateDir},
	}
	startReq := req
	startReq.Command = "start"
	startReq.Config.KernelPath = "C:\\microagent\\Image"
	startReq.Config.RootfsPath = "C:\\microagent\\rootfs.vhd"
	startReq.Config.Network = &vmkit.NetworkConfig{Mode: "nat"}
	if _, err := writeRuntimeTransitionWithComputeIDsNetwork(startReq, vmkit.StateRunning, "serial="+serialLogPath(startReq), "", "fake", "11111111-1111-1111-1111-111111111111", "network-1", "endpoint-1"); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}
	adapter := &fakeAdapter{}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("stop resp=%#v err=%v", resp, err)
	}
	if adapter.shutdowns != 1 || adapter.cleanups != 1 {
		t.Fatalf("shutdowns=%d cleanups=%d, want 1 and 1", adapter.shutdowns, adapter.cleanups)
	}
}

func TestRunCommandFailsClosedForUnsupportedWindowsHyperVVsockTarget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv listener path is windows-only")
	}
	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath:     "C:\\microagent\\Image",
			RootfsPath:     "C:\\microagent\\rootfs.vhd",
			StateDir:       stateDir,
			VsockListeners: []vmkit.VsockListener{{Port: 2048, Target: filepath.Join(stateDir, "agent-1", "not-result.json")}},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err == nil || resp.OK || !strings.Contains(resp.Error, "target must be host:port or the workspace result path") {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	if adapter.starts != 0 {
		t.Fatalf("adapter starts = %d, want listener failure before start", adapter.starts)
	}
	if adapter.deletes != 1 || adapter.deleteID != "fake" {
		t.Fatalf("cleanup deletes=%d deleteID=%q, want created compute system deleted", adapter.deletes, adapter.deleteID)
	}
}

func TestRunCommandCleansUpComputeSystemWhenStartFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv run path is windows-only")
	}
	stateDir := t.TempDir()
	adapter := &fakeAdapter{startErr: fmt.Errorf("start failed")}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err == nil || resp.OK || !strings.Contains(resp.Error, "start failed") {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	if adapter.deletes != 1 || adapter.deleteID != "fake" {
		t.Fatalf("cleanup deletes=%d deleteID=%q, want created compute system deleted", adapter.deletes, adapter.deleteID)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.Event.State != vmkit.StateFailed || state.ComputeSystemID != "fake" {
		t.Fatalf("runtime state = %#v, want failed with compute system ID", state)
	}
}

func TestRunCommandCleansUpComputeSystemWhenResultWaitFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv run path is windows-only")
	}
	oldHook := startRuntimeListenersHook
	t.Cleanup(func() { startRuntimeListenersHook = oldHook })
	startRuntimeListenersHook = func(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
		return fakeListenerSet{wait: func() error {
			return fmt.Errorf("result wait failed")
		}}, nil
	}

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath:     "C:\\microagent\\Image",
			RootfsPath:     "C:\\microagent\\rootfs.vhd",
			StateDir:       stateDir,
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(stateDir, "agent-1", "result.json")}},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err == nil || resp.OK || !strings.Contains(resp.Error, "result wait failed") {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	if adapter.kills != 1 || adapter.killID != "fake" {
		t.Fatalf("cleanup kills=%d killID=%q, want running compute system killed", adapter.kills, adapter.killID)
	}
}

func TestInspectAndControlCommandsUseRuntimeState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	stateDir := t.TempDir()
	adapter := &fakeAdapter{}
	supervisor := Supervisor{adapter: adapter}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
		},
	}
	if resp, err := supervisor.Do(context.Background(), req); err != nil || !resp.OK {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	inspectReq := req
	inspectReq.Command = "inspect"
	resp, err := supervisor.Do(context.Background(), inspectReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("inspect resp=%#v err=%v", resp, err)
	}
	resultPath := filepath.Join(stateDir, "agent-1", "result.json")
	if err := os.WriteFile(resultPath, []byte(`{"exitCode":0,"stdout":"ok\n","stderr":"","error":""}`+"\n"), 0o644); err != nil {
		t.Fatalf("write result.json: %v", err)
	}
	resp, err = supervisor.Do(context.Background(), inspectReq)
	if err != nil || !resp.OK || resp.Result == nil || resp.Result.ResultPath != resultPath || resp.Result.Stdout != "ok\n" {
		t.Fatalf("inspect result resp=%#v err=%v", resp, err)
	}
	if resp.Readiness == nil || !resp.Readiness.ResultReady.Ready {
		t.Fatalf("inspect readiness = %#v", resp.Readiness)
	}

	stopReq := req
	stopReq.Command = "stop"
	resp, err = supervisor.Do(context.Background(), stopReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("stop resp=%#v err=%v", resp, err)
	}
	if adapter.shutdowns != 1 || adapter.shutdownID != "fake" {
		t.Fatalf("shutdowns=%d shutdownID=%q", adapter.shutdowns, adapter.shutdownID)
	}

	if _, err := supervisor.Do(context.Background(), req); err != nil {
		t.Fatalf("second run: %v", err)
	}
	killReq := req
	killReq.Command = "kill"
	resp, err = supervisor.Do(context.Background(), killReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("kill resp=%#v err=%v", resp, err)
	}
	if adapter.kills != 1 || adapter.killID != "fake" {
		t.Fatalf("kills=%d killID=%q", adapter.kills, adapter.killID)
	}

	if _, err := supervisor.Do(context.Background(), req); err != nil {
		t.Fatalf("third run: %v", err)
	}
	deleteReq := req
	deleteReq.Command = "delete"
	resp, err = supervisor.Do(context.Background(), deleteReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("delete resp=%#v err=%v", resp, err)
	}
	if adapter.deletes != 1 || adapter.deleteID != "fake" {
		t.Fatalf("deletes=%d deleteID=%q", adapter.deletes, adapter.deleteID)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent-1")); !os.IsNotExist(err) {
		t.Fatalf("runtime dir after delete err=%v, want removed", err)
	}
}

func TestHaltAndQuarantineUseRuntimeState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv lifecycle path is windows-only")
	}
	stateDir := t.TempDir()
	adapter := &fakeAdapter{}
	supervisor := Supervisor{adapter: adapter}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
		},
	}
	if resp, err := supervisor.Do(context.Background(), req); err != nil || !resp.OK {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	haltReq := req
	haltReq.Command = "halt"
	resp, err := supervisor.Do(context.Background(), haltReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateHalted {
		t.Fatalf("halt resp=%#v err=%v", resp, err)
	}
	if adapter.shutdowns != 1 || adapter.shutdownID != "fake" {
		t.Fatalf("shutdowns=%d shutdownID=%q", adapter.shutdowns, adapter.shutdownID)
	}

	if _, err := supervisor.Do(context.Background(), req); err != nil {
		t.Fatalf("second run: %v", err)
	}
	quarantineReq := req
	quarantineReq.Command = "quarantine"
	resp, err = supervisor.Do(context.Background(), quarantineReq)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateQuarantined {
		t.Fatalf("quarantine resp=%#v err=%v", resp, err)
	}
	if adapter.kills != 0 || adapter.deletes != 0 {
		t.Fatalf("quarantine terminated compute system: kills=%d deletes=%d", adapter.kills, adapter.deletes)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.Event.State != vmkit.StateQuarantined || state.ComputeSystemID != "fake" {
		t.Fatalf("runtime state = %#v, want quarantined with compute system ID", state)
	}
}

func TestSupervisorUsesInjectedAdapterForHostAndCheck(t *testing.T) {
	adapter := &fakeAdapter{
		host: vmkit.HostSupport{
			Backend:                 vmkit.BackendWindowsHyperV,
			Architecture:            "testarch",
			FrameworkAvailable:      true,
			VirtualizationSupported: true,
			ConsoleAvailable:        true,
			ConsoleMode:             "interactive",
		},
	}
	supervisor := Supervisor{adapter: adapter}
	resp, err := supervisor.Do(context.Background(), vmkit.Request{Command: "host"})
	if err != nil || !resp.OK || resp.Host == nil || resp.Host.Architecture != "testarch" {
		t.Fatalf("host resp=%#v err=%v", resp, err)
	}
	req := vmkit.Request{
		Command: "check",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/Image",
			RootfsPath: "/tmp/rootfs.vhd",
			StateDir:   t.TempDir(),
		},
	}
	resp, err = supervisor.Do(context.Background(), req)
	if err != nil || !resp.OK || adapter.checks != 1 {
		t.Fatalf("check resp=%#v err=%v checks=%d", resp, err, adapter.checks)
	}
}

type fakeAdapter struct {
	host        vmkit.HostSupport
	handle      computeSystemHandle
	network     networkAttachment
	checks      int
	networks    int
	cleanups    int
	creates     int
	starts      int
	startedID   string
	spec        computeSystemSpec
	shutdowns   int
	shutdownID  string
	shutdownErr error
	kills       int
	killID      string
	killErr     error
	deletes     int
	deleteID    string
	waits       int
	waitID      string
	startErr    error
	deleteErr   error
	gone        bool
	existsErr   error
}

func (f *fakeAdapter) Host(ctx context.Context) (vmkit.HostSupport, error) {
	return f.host, nil
}

func (f *fakeAdapter) Check(ctx context.Context) error {
	f.checks++
	return nil
}

func (f *fakeAdapter) PrepareNetwork(ctx context.Context, spec computeSystemSpec) (networkAttachment, error) {
	f.networks++
	if f.network.NetworkID != "" || f.network.NetworkEndpointID != "" || f.network.RuntimeNetwork != nil {
		return f.network, nil
	}
	return networkAttachment{}, nil
}

func (f *fakeAdapter) CleanupNetwork(ctx context.Context, state runtimeState) error {
	if state.NetworkEndpointID != "" {
		f.cleanups++
	}
	return nil
}

func (f *fakeAdapter) Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error) {
	f.creates++
	f.spec = spec
	if f.handle.ID != "" {
		return f.handle, nil
	}
	return computeSystemHandle{ID: "fake"}, nil
}

func (f *fakeAdapter) Start(ctx context.Context, id string) error {
	f.starts++
	f.startedID = id
	if f.startErr != nil {
		return f.startErr
	}
	return nil
}

func (f *fakeAdapter) Shutdown(ctx context.Context, id string) error {
	f.shutdowns++
	f.shutdownID = id
	if f.shutdownErr != nil {
		return f.shutdownErr
	}
	return nil
}

func (f *fakeAdapter) Kill(ctx context.Context, id string) error {
	f.kills++
	f.killID = id
	if f.killErr != nil {
		return f.killErr
	}
	return nil
}

func (f *fakeAdapter) Delete(ctx context.Context, id string) error {
	f.deletes++
	f.deleteID = id
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return nil
}

func (f *fakeAdapter) Exists(ctx context.Context, id string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return !f.gone, nil
}

func (f *fakeAdapter) Wait(ctx context.Context, id string) error {
	f.waits++
	f.waitID = id
	return nil
}

var _ runtimeAdapter = (*fakeAdapter)(nil)

type fakeListenerSet struct {
	wait func() error
}

func (f fakeListenerSet) pid() int {
	return 4321
}

func (f fakeListenerSet) Wait(ctx context.Context) error {
	if f.wait != nil {
		return f.wait()
	}
	return nil
}

func (f fakeListenerSet) Close() error {
	return nil
}

func TestInjectedAdapterHostErrorsBecomeStructuredResponses(t *testing.T) {
	supervisor := Supervisor{adapter: failingAdapter{}}
	resp, err := supervisor.Do(context.Background(), vmkit.Request{Command: "host"})
	if err == nil || resp.OK || !strings.Contains(resp.Error, "host unavailable") {
		t.Fatalf("host error resp=%#v err=%v", resp, err)
	}
}

type failingAdapter struct{}

func (failingAdapter) Host(ctx context.Context) (vmkit.HostSupport, error) {
	return vmkit.HostSupport{Backend: vmkit.BackendWindowsHyperV, Architecture: "testarch"}, fmt.Errorf("host unavailable")
}

func (failingAdapter) Check(ctx context.Context) error {
	return fmt.Errorf("check unavailable")
}

func (failingAdapter) PrepareNetwork(ctx context.Context, spec computeSystemSpec) (networkAttachment, error) {
	return networkAttachment{}, nil
}

func (failingAdapter) CleanupNetwork(ctx context.Context, state runtimeState) error {
	return nil
}

func (failingAdapter) Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error) {
	return computeSystemHandle{}, fmt.Errorf("create unavailable")
}

func (failingAdapter) Start(ctx context.Context, id string) error {
	return fmt.Errorf("start unavailable")
}

func (failingAdapter) Shutdown(ctx context.Context, id string) error {
	return fmt.Errorf("shutdown unavailable")
}

func (failingAdapter) Kill(ctx context.Context, id string) error {
	return fmt.Errorf("kill unavailable")
}

func (failingAdapter) Delete(ctx context.Context, id string) error {
	return fmt.Errorf("delete unavailable")
}

func (failingAdapter) Exists(ctx context.Context, id string) (bool, error) {
	return false, fmt.Errorf("exists unavailable")
}

func (failingAdapter) Wait(ctx context.Context, id string) error {
	return fmt.Errorf("wait unavailable")
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func writeReadinessSerialInput(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "serial.in")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write serial input marker: %v", err)
	}
	return path
}

func runningReadinessState(t *testing.T, config vmkit.Config) runtimeState {
	t.Helper()
	dir := t.TempDir()
	config.StateDir = dir
	if err := os.MkdirAll(filepath.Join(dir, "agent-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	return runtimeState{
		Event: eventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendWindowsHyperV},
			State:      vmkit.StateRunning,
			ObservedAt: "2026-06-12T00:00:00Z",
		},
		Config:                 config,
		ComputeSystemRuntimeID: "11111111-1111-1111-1111-111111111111",
		SerialInputPath:        writeReadinessSerialInput(t, filepath.Join(dir, "agent-1")),
		StartedAt:              "2026-06-12T00:00:00Z",
	}
}

func TestRuntimeReadinessShellProbeSignalsChannelTruth(t *testing.T) {
	oldProbe := shellHVSockProbeHook
	t.Cleanup(func() { shellHVSockProbeHook = oldProbe })

	state := runningReadinessState(t, vmkit.Config{ShellPort: 22001})

	shellHVSockProbeHook = func(ctx context.Context, probed runtimeState, timeout time.Duration) (time.Duration, error) {
		if got := guestShellPort(probed.Config); got != 22001 {
			t.Errorf("probed shell port = %d, want 22001", got)
		}
		return time.Millisecond, nil
	}
	readiness := runtimeReadinessForState(state)
	if !readiness.ShellReady.Ready || !strings.Contains(readiness.ShellReady.Detail, "shell target reachable") {
		t.Fatalf("shell readiness = %#v, want probe-signaled ready", readiness.ShellReady)
	}

	shellHVSockProbeHook = func(ctx context.Context, probed runtimeState, timeout time.Duration) (time.Duration, error) {
		return time.Millisecond, fmt.Errorf("connection refused")
	}
	readiness = runtimeReadinessForState(state)
	if readiness.ShellReady.Ready || !strings.Contains(readiness.ShellReady.Detail, "shell target unreachable") {
		t.Fatalf("shell readiness = %#v, want probe-signaled not ready", readiness.ShellReady)
	}
}

func TestRuntimeReadinessExecProbeRoundTrip(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake exec service: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				var req execprotocol.ExecRequest
				if err := execprotocol.DecodeMessage(conn, &req); err != nil {
					return
				}
				code := 0
				result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
				result.ExitCode = &code
				_ = execprotocol.EncodeMessage(conn, result)
			}(conn)
		}
	}()

	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	state := runningReadinessState(t, vmkit.Config{ExecPort: port})
	readiness := runtimeReadinessForState(state)
	if !readiness.ExecReady.Ready || !strings.Contains(readiness.ExecReady.Detail, "exec service round-trip ready") {
		t.Fatalf("exec readiness = %#v, want round-trip ready", readiness.ExecReady)
	}
}

func TestRuntimeReadinessExecUnreachableReportsDetail(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve unused port: %v", err)
	}
	port := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	state := runningReadinessState(t, vmkit.Config{ExecPort: port})
	readiness := runtimeReadinessForState(state)
	if readiness.ExecReady.Ready || !strings.Contains(readiness.ExecReady.Detail, "exec service unreachable") {
		t.Fatalf("exec readiness = %#v, want unreachable detail", readiness.ExecReady)
	}
}

func TestHasDetachedRuntimeServicesIncludesExecBridge(t *testing.T) {
	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-1"},
		Config:   &vmkit.Config{StateDir: t.TempDir(), ExecPort: 25279},
	}
	if !hasDetachedRuntimeServices(req) {
		t.Fatal("exec bridge should require the detached runtime listener helper")
	}
	req.Config.ExecPort = 0
	if hasDetachedRuntimeServices(req) {
		t.Fatal("no services configured should not require the detached runtime listener helper")
	}
}

func TestEnsureBindableManagementPortsMovesHeldExecPort(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold exec port: %v", err)
	}
	defer holder.Close()
	held := uint16(holder.Addr().(*net.TCPAddr).Port)

	config := &vmkit.Config{ExecPort: held}
	ensureBindableManagementPorts(config)
	if config.ExecPort == held || config.ExecPort == 0 {
		t.Fatalf("exec port = %d, want moved off held port %d", config.ExecPort, held)
	}
	if config.GuestExecPort != held {
		t.Fatalf("guest exec port = %d, want preserved original %d", config.GuestExecPort, held)
	}
}

func TestEnsureBindableManagementPortsKeepsFreeExecPort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	free := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	config := &vmkit.Config{ExecPort: free}
	ensureBindableManagementPorts(config)
	if config.ExecPort != free || config.GuestExecPort != 0 {
		t.Fatalf("config = %#v, want unchanged ports", config)
	}
}

func TestDetachedStartFailsClosedWhenExecBridgeNeverAccepts(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv start path is windows-only")
	}
	oldStart := startRuntimeListenerProcessHook
	oldTerminate := terminateRuntimeListenerProcessHook
	oldTimeout := execBridgeWaitTimeout
	t.Cleanup(func() {
		startRuntimeListenerProcessHook = oldStart
		terminateRuntimeListenerProcessHook = oldTerminate
		execBridgeWaitTimeout = oldTimeout
	})
	startRuntimeListenerProcessHook = func(req vmkit.Request) (int, error) { return 424242, nil }
	terminated := 0
	terminateRuntimeListenerProcessHook = func(pid int) { terminated++ }
	execBridgeWaitTimeout = 300 * time.Millisecond

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	execPort := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
			ExecPort:   execPort,
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err == nil || resp.OK {
		t.Fatalf("start resp=%#v err=%v, want fail-closed exec bridge error", resp, err)
	}
	if !strings.Contains(resp.Error, "structured exec bridge did not accept") {
		t.Fatalf("start error = %q, want exec bridge detail", resp.Error)
	}
	if terminated == 0 {
		t.Fatal("dead listener helper was not terminated")
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.Event.State != vmkit.StateFailed {
		t.Fatalf("runtime state = %s, want failed", state.Event.State)
	}
}

func TestDetachedStartWaitsForAcceptingExecBridge(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv start path is windows-only")
	}
	oldStart := startRuntimeListenerProcessHook
	t.Cleanup(func() { startRuntimeListenerProcessHook = oldStart })

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	execPort := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	var bridge net.Listener
	startRuntimeListenerProcessHook = func(req vmkit.Request) (int, error) {
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(execPort))))
		if err != nil {
			return 0, err
		}
		bridge = l
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		return 424242, nil
	}
	t.Cleanup(func() {
		if bridge != nil {
			_ = bridge.Close()
		}
	})

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
			ExecPort:   execPort,
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("start resp=%#v err=%v", resp, err)
	}
}

func TestRunMovesHeldExecPortAfterCreateAndRecordsIt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv run path is windows-only")
	}
	oldListeners := startRuntimeListenersHook
	t.Cleanup(func() { startRuntimeListenersHook = oldListeners })
	var listenerExecPort, listenerGuestExecPort uint16
	startRuntimeListenersHook = func(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
		listenerExecPort = req.Config.ExecPort
		listenerGuestExecPort = req.Config.GuestExecPort
		return nil, nil
	}

	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold exec port: %v", err)
	}
	defer holder.Close()
	held := uint16(holder.Addr().(*net.TCPAddr).Port)

	stateDir := t.TempDir()
	adapter := &fakeAdapter{handle: computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			StateDir:   stateDir,
			ExecPort:   held,
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK {
		t.Fatalf("run resp=%#v err=%v", resp, err)
	}
	if listenerExecPort == held || listenerExecPort == 0 {
		t.Fatalf("listener exec port = %d, want moved off held port %d", listenerExecPort, held)
	}
	if listenerGuestExecPort != held {
		t.Fatalf("listener guest exec port = %d, want preserved original %d", listenerGuestExecPort, held)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.Config.ExecPort != listenerExecPort || state.Config.GuestExecPort != held {
		t.Fatalf("runtime config ports = %d/%d, want %d/%d", state.Config.ExecPort, state.Config.GuestExecPort, listenerExecPort, held)
	}
}

func TestInspectMarksVanishedComputeSystemStopped(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv inspect path is windows-only")
	}
	oldTerminate := terminateRuntimeListenerProcessHook
	t.Cleanup(func() { terminateRuntimeListenerProcessHook = oldTerminate })
	terminated := 0
	terminateRuntimeListenerProcessHook = func(pid int) { terminated++ }

	stateDir := t.TempDir()
	req := vmkit.Request{
		Command: "inspect",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:/microagent/Image",
			RootfsPath: "C:/microagent/rootfs.vhd",
			StateDir:   stateDir,
		},
	}
	if _, err := writeRuntimeTransitionWithComputeIDs(req, vmkit.StateRunning, "serial=x", "", "fake", "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("write running state: %v", err)
	}
	adapter := &fakeAdapter{gone: true}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("inspect resp=%#v err=%v, want reconciled stopped", resp, err)
	}
	if !strings.Contains(resp.Event.Detail, "compute system exited") {
		t.Fatalf("detail = %q", resp.Event.Detail)
	}
	var state runtimeState
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &state)
	if state.Event.State != vmkit.StateStopped {
		t.Fatalf("persisted state = %s, want stopped", state.Event.State)
	}
}

func TestInspectKeepsRunningStateWhenComputeSystemAlive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-hyperv inspect path is windows-only")
	}
	stateDir := t.TempDir()
	req := vmkit.Request{
		Command: "inspect",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "C:/microagent/Image",
			RootfsPath: "C:/microagent/rootfs.vhd",
			StateDir:   stateDir,
		},
	}
	if _, err := writeRuntimeTransitionWithComputeIDs(req, vmkit.StateRunning, "serial=x", "", "fake", "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("write running state: %v", err)
	}
	resp, err := (Supervisor{adapter: &fakeAdapter{}}).Do(context.Background(), req)
	if err != nil || !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("inspect resp=%#v err=%v, want running preserved", resp, err)
	}
}
