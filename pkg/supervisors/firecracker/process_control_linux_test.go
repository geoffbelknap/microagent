//go:build linux

package firecracker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestWriteConfigAddsVsockForMediation(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   opts.StateDir,
			MemoryMiB:  512,
			CPUCount:   2,
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     "127.0.0.1:9900",
				FailClosed: true,
			},
		},
	}
	if err := writeConfig(opts, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	var cfg config
	data, err := os.ReadFile(configPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Vsock == nil || cfg.Vsock.GuestCID != firecrackerGuestCID(opts) || cfg.Vsock.UDSPath == "" {
		t.Fatalf("vsock = %#v", cfg.Vsock)
	}
}

func TestHaltWaitsForGuestExitWithoutSignalingVMM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	marker := filepath.Join(dir, "received-term")
	vmProcess := exec.Command("sh", "-c", `trap 'printf term > "$MARKER"' TERM; while :; do :; done`)
	vmProcess.Env = append(os.Environ(), "MARKER="+marker)
	if err := vmProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = vmProcess.Process.Kill()
		_, _ = vmProcess.Process.Wait()
	})
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-run", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	// Simulate Firecracker exiting because guest PID 1 powered off. If halt
	// sends SIGTERM to the VMM first, the trap leaves evidence in marker.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = vmProcess.Process.Signal(syscall.SIGKILL)
	}()
	haltReq := vmkit.Request{
		Command: "halt",
		Identity: &vmkit.Identity{
			RequestID: "req-halt", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	resp, err := Supervisor{}.Do(context.Background(), haltReq)
	if err != nil {
		t.Fatalf("halt: resp=%#v err=%v", resp, err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateHalted {
		t.Fatalf("response = %#v, want halted", resp)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("halt signaled VMM directly; marker stat = %v", err)
	}
}

// TestQuarantineStopsRuntimeAndSeversHostSideEffects: containment stops the
// runtime AND severs every host-side path, recording StateQuarantined so the
// action is distinguishable from an operational halt (only resume lifts it).
//
// This deliberately replaces the previous "quarantine preserves the VM pid"
// expectation. Preserving it was never real: with user-mode networking the VM
// died anyway as collateral of tearing down pasta, so the behavior differed by
// network mode and the contract's RuntimeMayContinue was false in practice.
// Stopping explicitly makes containment identical across modes. Volatile state
// is secured by capturing BEFORE quarantining, not by surviving it.
func TestQuarantineStopsRuntimeAndSeversHostSideEffects(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     "127.0.0.1:9900",
				FailClosed: true,
			},
		},
	}
	vmProcess := exec.Command("sleep", "30")
	if err := vmProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = vmProcess.Process.Kill()
		_, _ = vmProcess.Process.Wait()
	})
	forwarder := exec.Command("sleep", "30")
	if err := forwarder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = forwarder.Process.Kill()
		_, _ = forwarder.Process.Wait()
	})
	vsockListener := exec.Command("sleep", "30")
	if err := vsockListener.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = vsockListener.Process.Kill()
		_, _ = vsockListener.Process.Wait()
	})
	if err := os.MkdirAll(filepath.Dir(vsockSocketPath(opts)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vsockSocketPath(opts), []byte("socket placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	quarantineReq := vmkit.Request{
		Command:  "quarantine",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	}
	resp, err := Supervisor{}.Do(context.Background(), quarantineReq)
	if err != nil {
		t.Fatalf("quarantine: resp=%+v err=%v", resp, err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateQuarantined {
		t.Fatalf("response = %+v", resp)
	}
	if err := waitForProcessExit(context.Background(), forwarder.Process.Pid, time.Second); err != nil {
		t.Fatalf("forwarder still active: %v", err)
	}
	if err := waitForProcessExit(context.Background(), vsockListener.Process.Pid, time.Second); err != nil {
		t.Fatalf("vsock listener still active: %v", err)
	}
	// The runtime is stopped, not left severed-but-alive.
	if err := waitForProcessExit(context.Background(), vmProcess.Process.Pid, 5*time.Second); err != nil {
		t.Fatalf("quarantine must stop the runtime: %v", err)
	}
	// Guest-facing endpoints are gone, so nothing stale survives to reconnect to.
	if _, err := os.Stat(vsockSocketPath(opts)); !os.IsNotExist(err) {
		t.Fatalf("vsock socket stat err = %v, want not exist", err)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Contained, with disk and event history intact and every aux path cleared.
	if state.Event.State != vmkit.StateQuarantined || state.PortForwardPID != 0 || state.VsockListenerPID != 0 || len(state.NetworkDevices) != 0 {
		t.Fatalf("state = %#v", state)
	}
}

func TestBackgroundTerminalWriteDoesNotDemoteQuarantine(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	startReq := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-run",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, startReq, vmkit.StateRunning, 1234, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	quarantineReq := vmkit.Request{
		Command: "quarantine",
		Identity: &vmkit.Identity{
			RequestID: "req-quarantine",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, quarantineReq, vmkit.StateQuarantined, 1234, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}

	if err := writeProcessStateWithProcessesAndNetwork(opts, startReq, vmkit.StateStopped, 0, 0, 0, 0, nil, nil, "late run exit"); err != nil {
		t.Fatal(err)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateQuarantined || state.Event.Identity.RequestID != "req-quarantine" {
		t.Fatalf("state = %#v, want quarantine preserved", state.Event)
	}

	stopReq := vmkit.Request{
		Command: "stop",
		Identity: &vmkit.Identity{
			RequestID: "req-stop",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, stopReq, vmkit.StateStopped, 0, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	state, err = readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateStopped || state.Event.Identity.RequestID != "req-stop" {
		t.Fatalf("state = %#v, want explicit stop to leave quarantine", state.Event)
	}
}

func TestEnsureCanDeleteRejectsRunningStateWithoutPID(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := ensureCanDelete(opts); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("ensureCanDelete error = %v, want running rejection", err)
	}
}

func TestEnsureCanDeleteRejectsActiveUserNetworkProcess(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command: "stop",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	networkProcess := exec.Command("sleep", "30")
	if err := networkProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = networkProcess.Process.Kill()
		_, _ = networkProcess.Process.Wait()
	})
	if err := os.WriteFile(userNetworkPIDPath(opts), []byte(strconv.Itoa(networkProcess.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCanDelete(opts); err == nil || !strings.Contains(err.Error(), "user network process is running") {
		t.Fatalf("ensureCanDelete error = %v, want user network rejection", err)
	}
}

func TestDetachedStartExitErrorDetectsImmediateExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 7")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	err := detachedStartExitError(cmd, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("detachedStartExitError = %v, want exit status 7", err)
	}
}

func TestDetachedStartExitErrorIgnoresRunningProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := detachedStartExitError(cmd, 10*time.Millisecond); err != nil {
		t.Fatalf("detachedStartExitError = %v, want nil", err)
	}
}

func TestWriteConfigOmitsNetworkInterfaceForIsolated(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   opts.StateDir,
			MemoryMiB:  512,
			CPUCount:   2,
			Network:    &vmkit.NetworkConfig{Mode: "isolated"},
		},
	}
	if err := writeConfig(opts, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	var cfg config
	data, err := os.ReadFile(configPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.NetworkInterfaces) != 0 {
		t.Fatalf("network interfaces = %#v", cfg.NetworkInterfaces)
	}
}

type unexpectedResponseError struct {
	response vmkit.Response
}

func (e unexpectedResponseError) Error() string {
	return "unexpected response"
}

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(port)
}

type fakeVMController struct {
	states      []string
	stateCtxErr []error // ctx.Err() observed at each patchVMState call, in order
	vmState     string  // returned by getVMState (e.g. "Running"/"Paused")
	vmStateErr  error   // returned by getVMState
	getCalls    int
	snapshots   [][2]string
	loads       [][2]string
	loadResume  bool
	err         error
	snapErr     error
	loadErr     error
	// onCreateSnapshot, if set, runs at the start of createSnapshot. Tests use it
	// to simulate the request being cancelled mid-capture (after the auto-pause).
	onCreateSnapshot func()
}

func (f *fakeVMController) patchVMState(ctx context.Context, state string) error {
	f.states = append(f.states, state)
	f.stateCtxErr = append(f.stateCtxErr, ctx.Err())
	return f.err
}

func (f *fakeVMController) getVMState(_ context.Context) (string, error) {
	f.getCalls++
	return f.vmState, f.vmStateErr
}

func (f *fakeVMController) createSnapshot(_ context.Context, snapshotPath, memFilePath string) error {
	if f.onCreateSnapshot != nil {
		f.onCreateSnapshot()
	}
	f.snapshots = append(f.snapshots, [2]string{snapshotPath, memFilePath})
	return f.snapErr
}

func (f *fakeVMController) loadSnapshot(_ context.Context, snapshotPath, memFilePath string, resume bool, _ []networkOverride) error {
	f.loads = append(f.loads, [2]string{snapshotPath, memFilePath})
	f.loadResume = resume
	return f.loadErr
}

func withFakeVMController(t *testing.T, fake *fakeVMController) {
	t.Helper()
	previous := newVMStateController
	newVMStateController = func(string) vmStateController { return fake }
	t.Cleanup(func() { newVMStateController = previous })
}

func withFakeFirecrackerProcessConfined(t *testing.T, confined bool) {
	t.Helper()
	previous := firecrackerProcessConfinedToWorkspace
	firecrackerProcessConfinedToWorkspace = func(int, Options) bool { return confined }
	t.Cleanup(func() { firecrackerProcessConfinedToWorkspace = previous })
}

func startSleepProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func pauseResumeRequest(dir string) vmkit.Request {
	return vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
}

func TestPausePatchesVMStateAndPreservesAuxProcesses(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	vmProcess := startSleepProcess(t)
	forwarder := startSleepProcess(t)
	vsockListener := startSleepProcess(t)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "pause",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err != nil {
		t.Fatalf("pause: resp=%+v err=%v", resp, err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StatePaused {
		t.Fatalf("response = %+v", resp)
	}
	if len(fake.states) != 1 || fake.states[0] != "Paused" {
		t.Fatalf("controller states = %#v, want [Paused]", fake.states)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StatePaused {
		t.Fatalf("persisted state = %s, want paused", state.Event.State)
	}
	if state.PID != vmProcess.Process.Pid || state.PortForwardPID != forwarder.Process.Pid || state.VsockListenerPID != vsockListener.Process.Pid {
		t.Fatalf("pause dropped process state: %#v", state)
	}
}

func TestResumePatchesVMStateBackToRunning(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	vmProcess := startSleepProcess(t)
	forwarder := startSleepProcess(t)
	vsockListener := startSleepProcess(t)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StatePaused, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "resume",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err != nil {
		t.Fatalf("resume: resp=%+v err=%v", resp, err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("response = %+v", resp)
	}
	if len(fake.states) != 1 || fake.states[0] != "Resumed" {
		t.Fatalf("controller states = %#v, want [Resumed]", fake.states)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateRunning {
		t.Fatalf("persisted state = %s, want running", state.Event.State)
	}
	if state.PortForwardPID != forwarder.Process.Pid || state.VsockListenerPID != vsockListener.Process.Pid {
		t.Fatalf("resume dropped process state: %#v", state)
	}
}

func TestContainmentFreezesThenSeversCompanionsWithoutStoppingVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	req.Config.Network = &vmkit.NetworkConfig{Mode: "isolated"}
	vmProcess := startSleepProcess(t)
	forwarder := startSleepProcess(t)
	vsockListener := startSleepProcess(t)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	for _, command := range []string{"contain-freeze", "contain-sever"} {
		resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
			Command: command, Identity: req.Identity, Config: &vmkit.Config{StateDir: dir},
		})
		if err != nil || !resp.OK {
			t.Fatalf("%s: resp=%+v err=%v", command, resp, err)
		}
	}
	if len(fake.states) != 1 || fake.states[0] != "Paused" {
		t.Fatalf("controller states = %#v, want [Paused]", fake.states)
	}
	if active, err := processActive(vmProcess.Process.Pid); err != nil || !active {
		t.Fatal("containment severance stopped the VM before forensic capture")
	}
	forwarderActive, _ := processActive(forwarder.Process.Pid)
	vsockActive, _ := processActive(vsockListener.Process.Pid)
	if forwarderActive || vsockActive {
		t.Fatal("containment severance left a published-port or vsock companion active")
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StatePaused || state.PortForwardPID != 0 || state.VsockListenerPID != 0 {
		t.Fatalf("severed state = %#v", state)
	}
}

func TestContainmentSeveranceFreezesUserNetworkDatapath(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	req.Config.Network = &vmkit.NetworkConfig{Mode: "user"}
	pasta := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StatePaused, pasta.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userNetworkPIDPath(opts), []byte(strconv.Itoa(pasta.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command: "contain-sever", Identity: req.Identity, Config: &vmkit.Config{StateDir: dir},
	})
	if err != nil || !resp.OK {
		t.Fatalf("contain-sever: resp=%#v err=%v", resp, err)
	}
	if err := waitForProcessStopped(pasta.Process.Pid, time.Second); err != nil {
		t.Fatalf("user-network datapath remained executable: %v", err)
	}
}

func TestContainmentMarkerBlocksBackendResume(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StatePaused, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(vmkit.ContainmentMarkerDir(dir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command: "resume", Identity: req.Identity, Config: &vmkit.Config{StateDir: dir},
	})
	if err == nil || !strings.Contains(err.Error(), "containment marker") {
		t.Fatalf("resume err = %v, want containment denial", err)
	}
	if len(fake.states) != 0 {
		t.Fatalf("resume reached VMM despite containment marker: %#v", fake.states)
	}
}

func TestContainmentMarkerBlocksBackendStart(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	if err := os.MkdirAll(vmkit.ContainmentMarkerDir(dir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	req := pauseResumeRequest(dir)
	req.Command = "run"
	resp, err := Supervisor{}.Do(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "containment marker") || resp.OK {
		t.Fatalf("run resp=%#v err=%v, want containment denial", resp, err)
	}
}

func TestPauseRejectsWorkspaceThatIsNotRunning(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	if err := writeProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "pause",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err == nil {
		t.Fatal("expected pause to reject a stopped workspace")
	}
	if len(fake.states) != 0 {
		t.Fatalf("controller should not be called, got %#v", fake.states)
	}
}

func TestResumeRejectsWorkspaceThatIsNotPaused(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "resume",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err == nil {
		t.Fatal("expected resume to reject a running workspace")
	}
	if len(fake.states) != 0 {
		t.Fatalf("controller should not be called, got %#v", fake.states)
	}
}
