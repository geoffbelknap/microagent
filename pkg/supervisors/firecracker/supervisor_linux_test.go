//go:build linux

package firecracker

import (
	"context"
	"io"
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

func TestDialGuestVsockUsesFirecrackerConnectHandshake(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err.Error()
			return
		}
		defer conn.Close()
		buf := make([]byte, len("CONNECT 8080\n"))
		if _, err := io.ReadFull(conn, buf); err != nil {
			done <- err.Error()
			return
		}
		if _, err := conn.Write([]byte("OK 1234\npayload")); err != nil {
			done <- err.Error()
			return
		}
		done <- string(buf)
	}()
	conn, reader, err := dialGuestVsock(socketPath, 8080)
	if err != nil {
		t.Fatalf("dialGuestVsock: %v", err)
	}
	defer conn.Close()
	if got := <-done; got != "CONNECT 8080\n" {
		t.Fatalf("handshake = %q", got)
	}
	payload := make([]byte, len("payload"))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q", payload)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatal(err)
	}
}

func TestOpenSerialInputFIFOCreatesNamedPipe(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	file, err := openSerialInputFIFO(opts)
	if err != nil {
		t.Fatalf("openSerialInputFIFO: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(serialInputPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("serial input is not a fifo: %s", info.Mode())
	}
}

func TestOpenSerialInputFIFORejectsRegularFile(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Dir(serialInputPath(opts)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInputPath(opts), []byte("not a fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := openSerialInputFIFO(opts); err == nil {
		_ = file.Close()
		t.Fatal("openSerialInputFIFO accepted regular file")
	}
}

func TestRunConnectsSerialInputToFirecrackerStdin(t *testing.T) {
	dir := t.TempDir()
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf 'ready\n'
IFS= read -r line
printf 'got:%s\n' "$line"
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath:  filepath.Join(dir, "Image"),
			RootfsPath:  filepath.Join(dir, "rootfs.ext4"),
			StateDir:    dir,
			MemoryMiB:   128,
			CPUCount:    1,
			Network:     &vmkit.NetworkConfig{Mode: "isolated"},
			SerialInput: true,
		},
	}
	done := make(chan error, 1)
	go func() {
		resp, err := (Supervisor{Options: Options{
			Name:            "research",
			StateDir:        dir,
			FirecrackerPath: fakeFirecracker,
			Timeout:         2 * time.Second,
			// Exercises serial-input plumbing with a fake firecracker, not
			// confinement; pin it off so it doesn't take the (now default-on)
			// confined launch path, which can't unshare/pivot_root under plain
			// `go test`.
			Confinement: "off",
		}}).Do(context.Background(), req)
		if err != nil {
			done <- err
			return
		}
		if !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
			done <- unexpectedResponseError{response: resp}
			return
		}
		done <- nil
	}()
	inputPath := filepath.Join(dir, "research", "serial.in")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(inputPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", inputPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	input, err := os.OpenFile(inputPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString("hello\n"); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("firecracker supervisor did not exit")
	}
	serial, err := os.ReadFile(filepath.Join(dir, "research", "serial.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serial), "ready\n") || !strings.Contains(string(serial), "got:hello\n") {
		t.Fatalf("serial log = %q", serial)
	}
}

func TestServePortForwardUsesRequestedVsockPort(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err.Error()
			return
		}
		defer conn.Close()
		buf := make([]byte, len("CONNECT 8080\n"))
		if _, err := io.ReadFull(conn, buf); err != nil {
			done <- err.Error()
			return
		}
		done <- string(buf)
	}()
	hostListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostListener.Close()
	go servePortForward(hostListener, socketPath, 9090)
	conn, err := net.Dial("tcp", hostListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := <-done; got != "CONNECT 9090\n" {
		t.Fatalf("handshake = %q", got)
	}
}

func TestStartForegroundPortForwardsBindsAndReleases(t *testing.T) {
	// The -p publish path for a foreground `run`, which previously bound
	// nothing. Grab a free port, hand it to the helper, prove it is bound, then
	// prove the stop func releases it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	opts := Options{StateDir: t.TempDir(), Name: "fg"}
	config := &vmkit.Config{Network: &vmkit.NetworkConfig{
		PortForwards: []vmkit.PortForward{{
			Protocol:  "tcp",
			Host:      "127.0.0.1",
			HostPort:  hostPort,
			GuestPort: 8080,
		}},
	}}

	stop := startForegroundPortForwards(opts, config)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(hostPort)))
	if l, err := net.Listen("tcp", addr); err == nil {
		_ = l.Close()
		stop()
		t.Fatalf("port %d was not bound by startForegroundPortForwards", hostPort)
	}

	stop()

	// After stop the port is released; retry briefly to absorb close scheduling.
	var relisten net.Listener
	for i := 0; i < 50; i++ {
		if relisten, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("port %d not released after stop: %v", hostPort, err)
	}
	_ = relisten.Close()
}

func TestStartForegroundPortForwardsNoForwardsIsNoop(t *testing.T) {
	// No forwards declared: the stop func must be safe to call with no listeners.
	stop := startForegroundPortForwards(Options{StateDir: t.TempDir(), Name: "fg"}, &vmkit.Config{})
	stop()
}

func TestStartVsockListenersWritesGuestResult(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "demo"}
	resultPath := filepath.Join(dir, "demo", "result.json")
	set, err := startVsockListeners(opts, &vmkit.Config{
		VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: resultPath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	conn, err := net.Dial("unix", firecrackerGuestVsockPath(opts, 1024))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(resultPath)
		if err == nil {
			if string(data) != `{"ok":true}` {
				t.Fatalf("result = %s", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("result not written: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGuestHaltedStateWaitsForDelayedFailureResult(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "demo"}
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		result := `{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":42,"stdout":"failed\n"}`
		_ = os.WriteFile(filepath.Join(dir, "demo", "result.json"), []byte(result), 0o644)
	}()

	state, detail := guestHaltedState(opts, time.Second)

	if state != vmkit.StateFailed {
		t.Fatalf("state = %q, want %q", state, vmkit.StateFailed)
	}
	if detail != "guest exited with code 42" {
		t.Fatalf("detail = %q, want guest exit detail", detail)
	}
}

func TestGuestHaltedStateClassifiesPowerOffAsStopped(t *testing.T) {
	cases := []struct {
		name       string
		result     string
		wantState  vmkit.VMState
		wantDetail string
	}{
		{
			name:      "powered off with non-zero exit classifies as stopped",
			result:    `{"exit_code":143,"error":"signal: killed","powered_off":true}`,
			wantState: vmkit.StateStopped,
		},
		{
			name:       "non-zero exit without power-off marker stays failed",
			result:     `{"exit_code":143,"error":"signal: killed"}`,
			wantState:  vmkit.StateFailed,
			wantDetail: "signal: killed",
		},
		{
			name:      "clean zero exit classifies as stopped",
			result:    `{"exit_code":0}`,
			wantState: vmkit.StateStopped,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := Options{StateDir: dir, Name: "demo"}
			if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "demo", "result.json"), []byte(tc.result), 0o644); err != nil {
				t.Fatal(err)
			}

			state, detail := guestHaltedState(opts, 0)

			if state != tc.wantState {
				t.Fatalf("state = %q, want %q", state, tc.wantState)
			}
			if tc.wantDetail != "" && detail != tc.wantDetail {
				t.Fatalf("detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}

// TestInspectClassifiesStoppingWorkspaceAsStopped covers the host-`stop` race:
// when an intentional stop records Stopping before killing firecracker, the
// supervise loop's inspect re-classification of the now-dead firecracker must
// resolve to Stopped — not Failed from the killed command's non-zero
// result.json. A workspace dying the same way WITHOUT the Stopping intent (a
// genuine crash) must still classify as Failed.
func TestInspectClassifiesStoppingWorkspaceAsStopped(t *testing.T) {
	cases := []struct {
		name      string
		stopping  bool
		wantState vmkit.VMState
	}{
		{name: "stopping intent resolves dead firecracker to stopped", stopping: true, wantState: vmkit.StateStopped},
		{name: "no stopping intent classifies the killed command as failed", stopping: false, wantState: vmkit.StateFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
				Config: &vmkit.Config{StateDir: dir},
			}
			if err := os.MkdirAll(filepath.Join(dir, "agent-1"), 0o755); err != nil {
				t.Fatal(err)
			}
			// The killed workspace command left a non-zero result: guestHaltedState
			// would classify this Failed unless the stop intent overrides it.
			result := `{"exit_code":143,"error":"signal: killed"}`
			if err := os.WriteFile(filepath.Join(dir, "agent-1", "result.json"), []byte(result), 0o644); err != nil {
				t.Fatal(err)
			}
			// Record the workspace Running with a firecracker PID that is dead and
			// does not reference this workspace, so inspect's liveness check sees
			// firecracker as gone and enters the reclassification branch.
			deadPID := unusedPID(t)
			if err := writeProcessState(opts, req, vmkit.StateRunning, deadPID, ""); err != nil {
				t.Fatal(err)
			}
			if tc.stopping {
				state, err := readRuntimeState(opts)
				if err != nil {
					t.Fatal(err)
				}
				if err := persistStopIntent(opts, state); err != nil {
					t.Fatal(err)
				}
			}

			resp, err := inspectWorkspace(opts)
			if err != nil {
				t.Fatalf("inspectWorkspace: %v", err)
			}
			if resp.Event == nil || resp.Event.State != tc.wantState {
				t.Fatalf("inspect state = %#v, want %q", resp.Event, tc.wantState)
			}
			persisted, err := readRuntimeState(opts)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Event.State != tc.wantState {
				t.Fatalf("persisted state = %q, want %q", persisted.Event.State, tc.wantState)
			}
		})
	}
}

// unusedPID returns a PID that is not currently a live process, so liveness
// checks treat it as a dead firecracker.
func unusedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn throwaway process: %v", err)
	}
	pid := cmd.ProcessState.Pid()
	if pid <= 0 {
		t.Fatalf("throwaway pid = %d", pid)
	}
	return pid
}

func TestRuntimeHasResultListener(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "demo"}
	state := runtimeState{
		Event: eventFile{Identity: vmkit.Identity{RuntimeID: "demo"}},
		Config: vmkit.Config{
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(dir, "demo", "result.json")}},
		},
	}

	if !runtimeHasResultListener(opts, state) {
		t.Fatal("runtimeHasResultListener = false, want true")
	}
}

func TestInspectReturnsRuntimeMetadata(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	mediationListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mediationListener.Close()
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath:  "/tmp/kernel",
			RootfsPath:  "/tmp/rootfs.ext4",
			StateDir:    dir,
			MemoryMiB:   512,
			CPUCount:    2,
			SerialInput: true,
			Network: &vmkit.NetworkConfig{
				Mode:   "user",
				IP:     "10.43.1.2/29",
				Subnet: "10.43.1.0/29",
			},
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     mediationListener.Addr().String(),
				FailClosed: true,
			},
		},
	}
	if err := os.MkdirAll(filepath.Join(dir, "agent-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInputPath(opts), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := `{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":0,"stdout":"ok\n"}`
	if err := os.WriteFile(filepath.Join(dir, "agent-1", "result.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	// A genuinely-running workspace: a live process whose argv carries the
	// workspace state path, so the inspect liveness check sees firecracker as
	// alive and returns runtime metadata instead of reconciling it to stopped.
	vm := exec.Command("sleep", "3600")
	vm.Args = []string{filepath.Join(dir, opts.Name), "3600"}
	if err := vm.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vm.Process.Kill(); _, _ = vm.Process.Wait() })
	if err := writeProcessState(opts, req, vmkit.StateRunning, vm.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "inspect",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err != nil {
		t.Fatalf("inspect: resp=%+v err=%v", resp, err)
	}
	if resp.Readiness == nil || !resp.Readiness.GuestReady.Ready || !resp.Readiness.ShellReady.Ready || !resp.Readiness.ResultReady.Ready || !resp.Readiness.MediationReady.Ready {
		t.Fatalf("readiness = %#v", resp.Readiness)
	}
	if resp.Result == nil || resp.Result.ExitCode != 0 || resp.Result.CompletedAt != "2026-05-02T00:00:01Z" || resp.Result.Stdout != "ok\n" {
		t.Fatalf("result = %#v", resp.Result)
	}
	if resp.Network == nil || resp.Network.Mode != "user" || resp.Network.IP != "10.43.1.2/29" {
		t.Fatalf("network = %#v", resp.Network)
	}
	if resp.Mediation == nil || !resp.Mediation.Required || !resp.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", resp.Mediation)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Readiness.GuestReady.Ready || !state.Readiness.ShellReady.Ready || !state.Readiness.ResultReady.Ready || !state.Readiness.MediationReady.Ready {
		t.Fatalf("persisted readiness = %#v", state.Readiness)
	}
}

func TestSerialInputFIFOUsesFIFOType(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	file, err := openSerialInputFIFO(opts)
	if err != nil {
		t.Fatalf("openSerialInputFIFO: %v", err)
	}
	defer file.Close()
	var stat syscall.Stat_t
	if err := syscall.Stat(serialInputPath(opts), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		t.Fatalf("mode = %#o, want fifo", stat.Mode)
	}
}
