package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	firecrackersupervisor "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestHighLevelCreateDetection(t *testing.T) {
	if !hasFlagValue([]string{"--image", "ubuntu:24.04"}, "image") {
		t.Fatal("expected --image to be detected")
	}
	if !hasFlagValue([]string{"--image=ubuntu:24.04"}, "image") {
		t.Fatal("expected --image=value to be detected")
	}
	if hasFlagValue([]string{"--kernel", "/tmp/kernel"}, "image") {
		t.Fatal("did not expect image flag")
	}
	if !shouldUseHighLevelCreate([]string{"test"}) {
		t.Fatal("expected positional create name to use high-level create")
	}
	if shouldUseHighLevelCreate([]string{"--rootfs", "/tmp/rootfs.ext4", "--id", "agent-1"}) {
		t.Fatal("legacy rootfs create should not use high-level create")
	}
}

func TestParseWorkspaceOptionsAcceptsPositionalName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{"test", "--image", "docker.io/library/ubuntu:24.04"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "test" {
		t.Fatalf("Name = %q, want test", opts.Name)
	}
}

func TestFirecrackerStopTerminatesRecordedPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	_, execPort, stopExec := startCommandExecServer(t, gracefulShutdownProcessHandler(t, cmd.Process))
	defer stopExec()
	req.Config.ExecPort = execPort
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	// stop is an alias of halt: it requests guest shutdown, waits for the VMM
	// process to exit, and records halted (not stopped).
	err = run(t.Context(), []string{"stop", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run stop: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateHalted || state.PID != 0 {
		t.Fatalf("state = %#v, want halted with no pid", state)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("process %d still active", cmd.Process.Pid)
	}
}

func TestFirecrackerHaltRecordsHaltedState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	_, execPort, stopExec := startCommandExecServer(t, gracefulShutdownProcessHandler(t, cmd.Process))
	defer stopExec()
	req.Config.ExecPort = execPort
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"halt", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run halt: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateHalted || state.PID != 0 {
		t.Fatalf("state = %#v, want halted with no pid", state)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("process %d still active", cmd.Process.Pid)
	}
}

// TestFirecrackerQuarantineStopsRecordedPID: containment stops the runtime and
// records StateQuarantined. Replaces the earlier "preserves the pid"
// expectation — preserving it was never real (with user-mode networking the VM
// died anyway when pasta was torn down), so behavior differed by network mode.
// Volatile state is secured by capturing BEFORE quarantining.
func TestFirecrackerQuarantineStopsRecordedPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"quarantine", "agent-1", "--reason", "unexpected network activity", "--yes", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run quarantine: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateQuarantined {
		t.Fatalf("state = %#v, want quarantined", state)
	}
	if state.Event.Identity.Purpose != "unexpected network activity" {
		t.Fatalf("quarantine purpose = %q, want lifecycle reason", state.Event.Identity.Purpose)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("process %d still active; quarantine must stop the runtime", cmd.Process.Pid)
	}
}

func TestFirecrackerKillTerminatesRecordedPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"kill", "agent-1", "--reason", "guest did not halt", "--yes", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run kill: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateStopped || state.PID != 0 {
		t.Fatalf("state = %#v, want stopped with no pid", state)
	}
}

func TestFirecrackerDeleteStopsRunningPIDWithYes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	_, execPort, stopExec := startCommandExecServer(t, gracefulShutdownProcessHandler(t, cmd.Process))
	defer stopExec()
	req.Config.ExecPort = execPort
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"delete", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t), "--yes"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run delete: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "agent-1", "runtime.json")); !os.IsNotExist(statErr) {
		t.Fatalf("runtime state still exists after delete: %v", statErr)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("running process still exists after delete")
	}
}

func TestFirecrackerLegacyCreatePreparesStateLocally(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	kernel := filepath.Join(dir, "Image")
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(kernel, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfs, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"create",
		"--rootfs", rootfs,
		"--kernel", kernel,
		"--id", "agent-1",
		"--state-dir", dir,
		"--vsock", "1024=127.0.0.1:8200",
		"--supervisor", firecrackerSupervisorHelper(t),
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "agent-1", "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) || !strings.Contains(string(data), `"backend": "linux-kvm"`) {
		t.Fatalf("runtime state = %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-1", "firecracker.json")); err != nil {
		t.Fatalf("firecracker config missing: %v", err)
	}
	configData, err := os.ReadFile(filepath.Join(dir, "agent-1", "firecracker.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Vsock *struct {
			VsockID  string `json:"vsock_id"`
			GuestCID uint32 `json:"guest_cid"`
			UDSPath  string `json:"uds_path"`
		} `json:"vsock"`
	}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if config.Vsock == nil || config.Vsock.VsockID != "vsock0" || config.Vsock.GuestCID < 3 || config.Vsock.UDSPath == "" {
		t.Fatalf("firecracker config missing vsock: %s", configData)
	}
}

func TestFirecrackerStatusReadsPreparedState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := vmkit.Request{
		Command: "prepare",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if _, err := (firecrackersupervisor.Supervisor{Options: firecrackersupervisor.Options{Name: "research", StateDir: dir}}).Do(t.Context(), req); err != nil {
		t.Fatalf("firecracker prepare: %v", err)
	}
	stdout, err := os.Create(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) || !strings.Contains(string(data), `"backend": "linux-kvm"`) {
		t.Fatalf("status output = %s", data)
	}
}

func testFirecrackerRuntimeState(t *testing.T, dir, name string, state vmkit.VMState, pid int) vmkit.Request {
	t.Helper()
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: name}, req, state, pid, ""); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestKernelInstallFromLocalAndVerify(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Image")
	if err := os.WriteFile(src, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("kernel")))
	out := filepath.Join(dir, "kernels", "Image")
	stdoutPath := filepath.Join(dir, "install.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runKernelInstall(t.Context(), []string{"--from", src, "--sha256", sum, "--out", out}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runKernelInstall: %v", err)
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "kernel" {
		t.Fatalf("installed kernel = %q, %v", data, err)
	}
	verifyOut, err := os.Create(filepath.Join(dir, "verify.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = runKernelVerify([]string{"--path", out, "--sha256", sum}, verifyOut)
	if closeErr := verifyOut.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runKernelVerify: %v", err)
	}
}

// Kernel ensure semantics (installed kernel used as-is, explicit path
// skipped, missing default installed) live in pkg/workspace; see
// pkg/workspace/kernel_test.go.

func TestFirecrackerGuestHaltedDetectsKernelShutdown(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor is linux-only")
	}
	dir := t.TempDir()
	serialPath := filepath.Join(dir, "serial.log")
	if firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("missing serial log reported halted")
	}
	if err := os.WriteFile(serialPath, []byte("[ 1.0 ] reboot: System halted\n"), 0o644); err != nil {
		t.Fatalf("write serial log: %v", err)
	}
	if !firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("system halt was not detected")
	}
	if err := os.WriteFile(serialPath, []byte("[ 1.0 ] reboot: Power down\n"), 0o644); err != nil {
		t.Fatalf("write serial log: %v", err)
	}
	if !firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("power down was not detected")
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func contractItemSliceContains(items []vmkit.ContractItem, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func startCommandExecServer(t *testing.T, handle func(execprotocol.ExecRequest) execprotocol.ExecResult) (string, uint16, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					t.Errorf("exec server accept: %v", err)
				}
				return
			}
			go func() {
				defer conn.Close()
				var req execprotocol.ExecRequest
				if err := execprotocol.DecodeMessage(conn, &req); err != nil {
					t.Errorf("decode exec request: %v", err)
					return
				}
				if strings.Join(req.Argv, " ") == "true" {
					code := 0
					result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
					result.ExitCode = &code
					_ = execprotocol.EncodeMessage(conn, result)
					return
				}
				if err := execprotocol.EncodeMessage(conn, handle(req)); err != nil {
					t.Errorf("encode exec result: %v", err)
				}
			}()
		}
	}()
	return listener.Addr().String(), uint16(portValue), func() {
		_ = listener.Close()
		<-done
	}
}

func gracefulShutdownProcessHandler(t *testing.T, process *os.Process) func(execprotocol.ExecRequest) execprotocol.ExecResult {
	t.Helper()
	return func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		if !req.IsShutdown() {
			t.Errorf("exec request = %#v, want shutdown", req)
		}
		// Return the acknowledgement before simulating the guest-triggered VMM
		// exit, matching the real exec-service ordering.
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = process.Signal(os.Interrupt)
		}()
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		return result
	}
}

func writeCommandExecRuntimeState(t *testing.T, name string, state vmkit.VMState, execPort uint16) string {
	t.Helper()
	dir := t.TempDir()
	opts := workspace.Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM, ExecPort: execPort}
	req, err := workspace.Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatalf("workspace.Request: %v", err)
	}
	if err := workspace.WriteProcessState(opts, req, state, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	return dir
}

func unusedTCPPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(portValue)
}
