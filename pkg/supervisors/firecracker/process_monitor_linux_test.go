//go:build linux

package firecracker

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func deadProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	return pid
}

func TestWaitForProcessExitEvent(t *testing.T) {
	// Returns true promptly (event-driven) when the watched process exits.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan bool, 1)
	go func() { done <- waitForProcessExitEvent(context.Background(), pid, 10*time.Second) }()
	time.Sleep(150 * time.Millisecond)
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	select {
	case got := <-done:
		if !got {
			t.Fatal("waitForProcessExitEvent = false on process exit, want true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForProcessExitEvent did not return within 3s of the process exiting")
	}

	// An already-dead pid returns true immediately (ESRCH).
	if !waitForProcessExitEvent(context.Background(), pid, time.Second) {
		t.Fatal("waitForProcessExitEvent = false for a dead pid, want true")
	}

	// A canceled context returns false rather than blocking out the budget.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live2 := exec.Command("sleep", "30")
	if err := live2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live2.Process.Kill(); _, _ = live2.Process.Wait() }()
	if waitForProcessExitEvent(ctx, live2.Process.Pid, 5*time.Second) {
		t.Fatal("waitForProcessExitEvent = true on canceled context, want false")
	}
}

func TestCompanionShouldExitConditions(t *testing.T) {
	live := startSleepProcess(t)
	dead := deadProcessPID(t)
	cases := []struct {
		name  string
		state vmkit.VMState
		pid   int
		want  bool
	}{
		{"running with live vm", vmkit.StateRunning, live.Process.Pid, false},
		{"paused with live vm", vmkit.StatePaused, live.Process.Pid, false},
		{"starting without pid", vmkit.StateStarting, 0, false},
		{"running with dead vm", vmkit.StateRunning, dead, true},
		{"stopped", vmkit.StateStopped, 0, true},
		{"halted", vmkit.StateHalted, 0, true},
		{"failed", vmkit.StateFailed, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := Options{Name: "agent-1", StateDir: dir}
			req := vmkit.Request{
				Command:  "run",
				Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
				Config:   &vmkit.Config{StateDir: dir},
			}
			if err := writeProcessState(opts, req, tc.state, tc.pid, ""); err != nil {
				t.Fatal(err)
			}
			if got := companionShouldExit(opts); got != tc.want {
				t.Fatalf("companionShouldExit(%s, pid=%d) = %v, want %v", tc.state, tc.pid, got, tc.want)
			}
		})
	}
	t.Run("missing runtime state", func(t *testing.T) {
		opts := Options{Name: "agent-1", StateDir: t.TempDir()}
		if !companionShouldExit(opts) {
			t.Fatal("companionShouldExit = false for missing runtime state, want true")
		}
	})
}

// A workspace recorded as running whose firecracker process is gone must be
// reconciled by inspect, not reported as still running. The guest here exited
// non-zero via a reset-path shutdown (no "System halted"/"Power down" serial
// marker), so reconciliation must trigger on PID liveness, not GuestHalted —
// otherwise supervise's WaitForSupervised polls inspect forever and hangs.
func TestInspectReconcilesDeadVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, deadProcessPID(t), ""); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "agent-1", "result.json")
	if err := os.WriteFile(resultPath, []byte(`{"exit_code":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateFailed {
		t.Fatalf("inspectWorkspace = %+v, want state failed (dead VM must be reconciled, not reported running)", resp.Event)
	}
}

// A workspace recorded running whose pid has been recycled by an unrelated live
// process (firecrackerAlive=false: the pid no longer carries this workspace's
// argv) must reconcile to a terminal state WITHOUT signaling that pid -- doing so
// SIGTERMs an innocent process group. This previously killed the e2e harness,
// which seeds the workspace pid to its own shell ($$).
func TestInspectDoesNotSignalReusedPID(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() }()
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, victim.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Event == nil || resp.Event.State == vmkit.StateRunning {
		t.Fatalf("inspectWorkspace did not reconcile a stale workspace: %+v", resp.Event)
	}
	if active, _ := processActive(victim.Process.Pid); !active {
		t.Fatal("reconcile signaled an unrelated (pid-reused) process; it must only signal this workspace's own firecracker")
	}
}

func TestRunPortForwarderExitsWhenVMProcessDies(t *testing.T) {
	dir := t.TempDir()
	port := freeTCPPort(t)
	opts := Options{Name: "agent-1", StateDir: dir}
	vm := startSleepProcess(t)
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir, ExecPort: port},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, vm.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RunPortForwarder(context.Background(), opts)
	}()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exec port forwarder did not listen on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := vm.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = vm.Process.Wait()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("RunPortForwarder did not exit after the VM process died")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("exec port still in use after forwarder exit: %v", err)
	}
	_ = listener.Close()
}

func TestRunVsockListenerExitsWhenWorkspaceStateRemoved(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	vm := startSleepProcess(t)
	resultPath := filepath.Join(dir, "agent-1", "result.json")
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config: &vmkit.Config{
			StateDir:       dir,
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: resultPath}},
		},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, vm.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RunVsockListener(context.Background(), opts)
	}()
	socketPath := firecrackerGuestVsockPath(opts, 1024)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("vsock listener socket %s did not appear", socketPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Remove(filepath.Join(dir, "agent-1", "runtime.json")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("RunVsockListener did not exit after runtime state removal")
	}
}

func TestForegroundExitTerminatesRecordedCompanions(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	forwarder := startSleepProcess(t)
	vsockListener := startSleepProcess(t)
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, 0, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	terminateRecordedCompanions(opts)
	if err := waitForProcessExit(context.Background(), forwarder.Process.Pid, 3*time.Second); err != nil {
		t.Fatalf("port forwarder still active: %v", err)
	}
	if err := waitForProcessExit(context.Background(), vsockListener.Process.Pid, 3*time.Second); err != nil {
		t.Fatalf("vsock listener still active: %v", err)
	}
}

func TestEnsureCanDeleteRejectsDeadVMWithLiveCompanions(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	forwarder := startSleepProcess(t)
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, deadProcessPID(t), forwarder.Process.Pid, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	err := ensureCanDelete(opts)
	if err == nil || !strings.Contains(err.Error(), "port forwarder") {
		t.Fatalf("ensureCanDelete error = %v, want port forwarder running error", err)
	}
}

func TestProcessTreeMountinfoReferencesWorkspaceJailFindsConfinedDescendant(t *testing.T) {
	const ws = "/state/feature-matrix"
	children := map[int][]int{
		100: {101, 102},
		101: {103},
		103: {101}, // cycle guard: a malformed/reparented view must not loop forever.
	}
	mountinfo := map[int][]byte{
		100: []byte("100 1 8:1 / / rw - ext4 /dev/sda1 rw\n"),
		103: []byte("277 268 253:1 /state/feature-matrix/jail / rw - ext4 /dev/vda1 rw\n"),
	}
	readMountinfo := func(pid int) []byte { return mountinfo[pid] }

	if !processTreeReferencesWorkspaceJail(100, ws, children, readMountinfo, nil) {
		t.Fatal("recorded parent PID should detect confined descendant jail mount")
	}
	if processTreeReferencesWorkspaceJail(102, ws, children, readMountinfo, nil) {
		t.Fatal("sibling subtree without a jail mount should not be confined")
	}
}

// TestProcessTreeReferencesWorkspaceJailByRootIdentity covers the state dir on
// tmpfs or a btrfs subvolume: mountinfo records the jail bind relative to that
// filesystem's own root, so the host-absolute path match fails and only the
// root device+inode identity finds the confined descendant.
func TestProcessTreeReferencesWorkspaceJailByRootIdentity(t *testing.T) {
	const ws = "/tmp/state/feature-matrix"
	children := map[int][]int{100: {103}}
	// tmpfs superblock-relative source: no "/tmp" prefix, so no substring match.
	mountinfo := map[int][]byte{
		103: []byte("277 268 0:41 /state/feature-matrix/jail / rw - tmpfs tmpfs rw\n"),
	}
	readMountinfo := func(pid int) []byte { return mountinfo[pid] }
	rootIs := func(pid int) bool { return pid == 103 }

	if processTreeReferencesWorkspaceJail(100, ws, children, readMountinfo, nil) {
		t.Fatal("superblock-relative jail source must not match the host-absolute path")
	}
	if !processTreeReferencesWorkspaceJail(100, ws, children, readMountinfo, rootIs) {
		t.Fatal("root identity should find the confined descendant when mountinfo cannot")
	}
}

// TestProcessRootMatchesJail exercises the device+inode identity check with the
// current process: /proc/self/root is "/", so a jail symlink to "/" matches and
// a plain directory does not.
func TestProcessRootMatchesJail(t *testing.T) {
	ws := t.TempDir()
	if processRootMatchesJail(os.Getpid(), ws) {
		t.Fatal("missing jail dir must not match")
	}
	if err := os.Symlink("/", filepath.Join(ws, "jail")); err != nil {
		t.Fatal(err)
	}
	if !processRootMatchesJail(os.Getpid(), ws) {
		t.Fatal("jail resolving to this process's root must match by device+inode")
	}
	if processRootMatchesJail(0, ws) {
		t.Fatal("pid 0 must not match")
	}
	if processRootMatchesJail(os.Getpid(), "") {
		t.Fatal("empty workspace path must not match")
	}
}

func TestLinuxProcessStatParentPIDParsesCommandWithSpaces(t *testing.T) {
	got, err := linuxProcessStatParentPID([]byte("123 (cmd with spaces) S 42 1 2 3 4\n"))
	if err != nil {
		t.Fatalf("linuxProcessStatParentPID: %v", err)
	}
	if got != 42 {
		t.Fatalf("parent pid = %d, want 42", got)
	}
}

func TestProcessIdentityReferencesWorkspace(t *testing.T) {
	const ws = "/state/feature-matrix"
	cases := []struct {
		name      string
		cmdline   []byte
		mountinfo []byte
		wsPath    string
		want      bool
	}{
		{"unconfined argv carries ws path",
			[]byte("/usr/bin/firecracker\x00--api-sock\x00/state/feature-matrix/run/api.sock\x00"), nil, ws, true},
		{"confined jail bind in mountinfo (jail-relative argv)",
			[]byte("/firecracker\x00--config-file\x00/run/firecracker.json\x00"),
			[]byte("277 268 253:1 /state/feature-matrix/jail / rw - ext4 /dev/vda1 rw\n"), ws, true},
		{"neither references the workspace (reused pid)",
			[]byte("/firecracker\x00--config-file\x00/run/firecracker.json\x00"),
			[]byte("100 1 8:1 / / rw - ext4 /dev/sda1 rw\n"), ws, false},
		{"another workspace's jail does not match (reuse-safe)", nil,
			[]byte("277 268 253:1 /state/other-ws/jail / rw - ext4 /dev/vda1 rw\n"), ws, false},
		{"prefix-name workspace jail does not match", nil,
			[]byte("277 268 253:1 /state/feature-matrix-2/jail / rw - ext4 /dev/vda1 rw\n"), ws, false},
		{"empty workspace path", []byte("/firecracker\x00"), nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processIdentityReferencesWorkspace(tc.cmdline, tc.mountinfo, tc.wsPath); got != tc.want {
				t.Errorf("processIdentityReferencesWorkspace = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInspectReconcilesDeadPausedVM is the B12 guard: a workspace left Paused
// (e.g. by an interrupted snapshot) whose firecracker has since died must be
// reconciled to a terminal state, not left stuck "Paused" forever with its aux
// resources leaked. inspect/gc previously only reconciled Running/Stopping.
func TestInspectReconcilesDeadPausedVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessState(opts, req, vmkit.StatePaused, deadProcessPID(t), ""); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "agent-1", "result.json")
	if err := os.WriteFile(resultPath, []byte(`{"exit_code":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Event == nil || (resp.Event.State != vmkit.StateFailed && resp.Event.State != vmkit.StateStopped) {
		t.Fatalf("inspectWorkspace = %+v, want a terminal state (a dead paused VM must be reconciled, not left Paused)", resp.Event)
	}
}

// TestGcReconcilesDeadPausedVM is the B12 gc counterpart to
// TestInspectReconcilesDeadPausedVM: gc must also reap a Paused workspace whose
// firecracker has died rather than short-circuiting on the non-Running guard and
// leaving it stuck Paused with leaked aux resources.
// startFrozenCandidateProcess starts a live process whose argv carries the
// workspace state path, so firecrackerAlive sees this workspace's VMM as alive.
func startFrozenCandidateProcess(t *testing.T, opts Options) int {
	t.Helper()
	cmd := exec.Command("sleep", "3600")
	cmd.Args = []string{filepath.Join(opts.StateDir, opts.Name), "3600"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd.Process.Pid
}

func frozenTestRequest(dir string) vmkit.Request {
	return vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
}

// TestGcHealsFrozenVM: an alive VM recorded Running whose firecracker reports
// Paused (frozen) is resumed by gc, and its recorded state stays Running.
func TestGcHealsFrozenVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := frozenTestRequest(dir)
	pid := startFrozenCandidateProcess(t, opts)
	if err := writeProcessState(opts, req, vmkit.StateRunning, pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{vmState: "Paused"}
	withFakeVMController(t, fake)

	resp, err := gcWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.states) != 1 || fake.states[0] != "Resumed" {
		t.Fatalf("controller states = %#v, want [Resumed] (gc must unfreeze the VM)", fake.states)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("post-gc state = %+v, want still Running", resp.Event)
	}
}

// TestGcLeavesHealthyRunningVM: a live VM that firecracker reports Running is not
// touched.
func TestGcLeavesHealthyRunningVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	pid := startFrozenCandidateProcess(t, opts)
	if err := writeProcessState(opts, frozenTestRequest(dir), vmkit.StateRunning, pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{vmState: "Running"}
	withFakeVMController(t, fake)

	if _, err := gcWorkspace(opts); err != nil {
		t.Fatal(err)
	}
	if len(fake.states) != 0 {
		t.Fatalf("controller states = %#v, want none (a healthy Running VM must not be resumed)", fake.states)
	}
}

// TestGcFrozenHealFailsSafeOnGetError: a GET-state error must not resume or
// otherwise disturb the VM.
func TestGcFrozenHealFailsSafeOnGetError(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	pid := startFrozenCandidateProcess(t, opts)
	if err := writeProcessState(opts, frozenTestRequest(dir), vmkit.StateRunning, pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{vmStateErr: errors.New("api unreachable")}
	withFakeVMController(t, fake)

	resp, err := gcWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.states) != 0 {
		t.Fatalf("controller states = %#v, want none (a GET error must fail safe)", fake.states)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("post-gc state = %+v, want still Running", resp.Event)
	}
}

// TestGcLeavesIntentionalPause: a workspace recorded Paused with a live
// firecracker is an intentional pause and must never be auto-resumed.
func TestGcLeavesIntentionalPause(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	pid := startFrozenCandidateProcess(t, opts)
	if err := writeProcessState(opts, frozenTestRequest(dir), vmkit.StatePaused, pid, ""); err != nil {
		t.Fatal(err)
	}
	// Even if the API would report Paused, gc must not resume a recorded-Paused VM.
	fake := &fakeVMController{vmState: "Paused"}
	withFakeVMController(t, fake)

	resp, err := gcWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.states) != 0 {
		t.Fatalf("controller states = %#v, want none (an intentional pause must be left alone)", fake.states)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StatePaused {
		t.Fatalf("post-gc state = %+v, want still Paused", resp.Event)
	}
}

// TestInspectHealsFrozenVM: a workspace recorded Running whose vCPUs are
// actually paused is repaired on inspect, not merely reported. Reporting alone
// meant nothing ever fixed it — the only healer was gc, which runs when an
// operator types `microagent gc` and never otherwise — so a guest killed
// mid-snapshot stayed frozen forever while every state file said Running.
func TestInspectHealsFrozenVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	pid := startFrozenCandidateProcess(t, opts)
	if err := writeProcessState(opts, frozenTestRequest(dir), vmkit.StateRunning, pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{vmState: "Paused"}
	withFakeVMController(t, fake)

	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.states) != 1 || fake.states[0] != "Resumed" {
		t.Fatalf("controller states = %#v, want exactly [Resumed]", fake.states)
	}
	// Healed, so the guest is no longer reported not-ready for being frozen.
	if resp.Readiness != nil && strings.Contains(resp.Readiness.GuestReady.Detail, "resume failed") {
		t.Fatalf("guest readiness = %#v, want no failure detail after a successful heal", resp.Readiness)
	}
	// The recorded state was already Running and stays Running: healing restores
	// what the record claims rather than imposing a new state.
	if state, err := readRuntimeState(opts); err != nil || state.Event.State != vmkit.StateRunning {
		t.Fatalf("recorded state = %v err=%v, want still Running", state.Event.State, err)
	}
}

// TestInspectReportsFrozenVMWhenResumeFails: healing is best-effort, so a VM
// that will not resume must still be surfaced as not-ready rather than silently
// reported healthy — the anomaly outlives a failed repair.
func TestInspectReportsFrozenVMWhenResumeFails(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	pid := startFrozenCandidateProcess(t, opts)
	if err := writeProcessState(opts, frozenTestRequest(dir), vmkit.StateRunning, pid, ""); err != nil {
		t.Fatal(err)
	}
	// Stays Paused no matter how many times it is told to resume.
	fake := &fakeVMController{vmState: "Paused", err: errors.New("resume boom")}
	withFakeVMController(t, fake)

	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Readiness == nil || resp.Readiness.GuestReady.Ready {
		t.Fatalf("guest readiness = %#v, want not-ready for a still-frozen VM", resp.Readiness)
	}
	if !strings.Contains(resp.Readiness.GuestReady.Detail, "paused") {
		t.Fatalf("guest readiness detail = %q, want a frozen explanation", resp.Readiness.GuestReady.Detail)
	}
}

// TestInspectHealthyRunningNoFrozenReport: a Running VM firecracker also reports
// Running gets the normal ready guest signal.
func TestInspectHealthyRunningNoFrozenReport(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	pid := startFrozenCandidateProcess(t, opts)
	if err := writeProcessState(opts, frozenTestRequest(dir), vmkit.StateRunning, pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{vmState: "Running"}
	withFakeVMController(t, fake)

	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Readiness == nil || !resp.Readiness.GuestReady.Ready {
		t.Fatalf("guest readiness = %#v, want ready for a healthy Running VM", resp.Readiness)
	}
}

func TestGcReconcilesDeadPausedVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessState(opts, req, vmkit.StatePaused, deadProcessPID(t), ""); err != nil {
		t.Fatal(err)
	}
	resp, err := gcWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Event == nil || resp.Event.State == vmkit.StatePaused || resp.Event.State == vmkit.StateRunning {
		t.Fatalf("gcWorkspace = %+v, want a terminal state (a dead paused VM must be reaped, not left Paused)", resp.Event)
	}
}
