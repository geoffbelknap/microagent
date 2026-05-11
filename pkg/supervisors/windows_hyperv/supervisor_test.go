package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
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
	adapter := &fakeAdapter{}
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
			MemoryMiB:  512,
			CPUCount:   2,
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
		Config        vmkit.Config `json:"config"`
		SerialLogPath string       `json:"serialLogPath"`
	}
	readJSON(t, filepath.Join(stateDir, "agent-1", "runtime.json"), &runtimeState)
	if runtimeState.Event.State != vmkit.StateRunning || runtimeState.Config.RootfsPath != req.Config.RootfsPath {
		t.Fatalf("runtime.json = %#v", runtimeState)
	}
	if runtimeState.SerialLogPath != filepath.Join(stateDir, "agent-1", "serial.log") {
		t.Fatalf("serialLogPath = %q", runtimeState.SerialLogPath)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent-1", "serial.log")); err != nil {
		t.Fatalf("serial.log: %v", err)
	}
	eventsData, err := os.ReadFile(filepath.Join(stateDir, "agent-1", "events.json"))
	if err != nil {
		t.Fatalf("events.json: %v", err)
	}
	if got := strings.Count(string(eventsData), "\n"); got != 2 {
		t.Fatalf("events.json lines = %d, want starting and running events: %q", got, eventsData)
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
			KernelPath:     "C:\\microagent\\Image",
			RootfsPath:     "C:\\microagent\\rootfs.vhd",
			StateDir:       stateDir,
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(stateDir, "agent-1", "result.json")}},
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
			VsockListeners: []vmkit.VsockListener{{Port: 2048, Target: "127.0.0.1:9900"}},
		},
	}
	resp, err := (Supervisor{adapter: adapter}).Do(context.Background(), req)
	if err == nil || resp.OK || !strings.Contains(resp.Error, "target must be the workspace result path") {
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
	host       vmkit.HostSupport
	handle     computeSystemHandle
	checks     int
	creates    int
	starts     int
	startedID  string
	spec       computeSystemSpec
	shutdowns  int
	shutdownID string
	kills      int
	killID     string
	deletes    int
	deleteID   string
	waits      int
	waitID     string
	startErr   error
}

func (f *fakeAdapter) Host(ctx context.Context) (vmkit.HostSupport, error) {
	return f.host, nil
}

func (f *fakeAdapter) Check(ctx context.Context) error {
	f.checks++
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
	return nil
}

func (f *fakeAdapter) Kill(ctx context.Context, id string) error {
	f.kills++
	f.killID = id
	return nil
}

func (f *fakeAdapter) Delete(ctx context.Context, id string) error {
	f.deletes++
	f.deleteID = id
	return nil
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
