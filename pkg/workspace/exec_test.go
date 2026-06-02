package workspace

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestExecSuccessfulRunningWorkspace(t *testing.T) {
	addr, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		if err := execprotocol.DecodeMessage(conn, &req); err != nil {
			t.Errorf("DecodeMessage: %v", err)
			return
		}
		if strings.Join(req.Argv, " ") != "uname -a" && strings.Join(req.Argv, " ") != "true" {
			t.Errorf("argv = %#v, want uname -a", req.Argv)
		}
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stdout = []byte("Linux demo\n")
		if err := execprotocol.EncodeMessage(conn, result); err != nil {
			t.Errorf("EncodeMessage: %v", err)
		}
	})
	_ = addr
	defer stop()
	opts := writeExecRuntimeState(t, vmkit.BackendFirecracker, vmkit.StateRunning, port)

	result, err := Exec(context.Background(), opts, execprotocol.NewExecRequest([]string{"uname", "-a"}))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 || string(result.Stdout) != "Linux demo\n" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecWorkspaceNotFound(t *testing.T) {
	_, err := Exec(context.Background(), Options{StateDir: t.TempDir(), Name: "missing"}, execprotocol.NewExecRequest([]string{"true"}))
	var notFound WorkspaceNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %T %v, want WorkspaceNotFoundError", err, err)
	}
}

func TestExecWorkspaceNotRunning(t *testing.T) {
	opts := writeExecRuntimeState(t, vmkit.BackendFirecracker, vmkit.StateStopped, 45000)
	_, err := Exec(context.Background(), opts, execprotocol.NewExecRequest([]string{"true"}))
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v, want not running", err)
	}
}

func TestExecRejectsPausedWorkspace(t *testing.T) {
	opts := writeExecRuntimeState(t, vmkit.BackendFirecracker, vmkit.StatePaused, 45000)
	_, err := Exec(context.Background(), opts, execprotocol.NewExecRequest([]string{"true"}))
	if err == nil || !strings.Contains(err.Error(), "paused; resume it first") {
		t.Fatalf("err = %v, want paused; resume it first", err)
	}
}

func TestExecRejectsNonFirecrackerBackend(t *testing.T) {
	opts := writeExecRuntimeState(t, vmkit.BackendWindowsHyperV, vmkit.StateRunning, 45000)
	_, err := Exec(context.Background(), opts, execprotocol.NewExecRequest([]string{"true"}))
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err = %v, want unsupported", err)
	}
}

func TestExecReadySignalFalseBeforeReachable(t *testing.T) {
	state := execRuntimeState(vmkit.BackendFirecracker, vmkit.StateRunning, 45000)
	signal, ok := ExecReadinessSignal(context.Background(), state, 25*time.Millisecond)
	if !ok {
		t.Fatal("ExecReadinessSignal ok = false, want true")
	}
	if signal.Ready || !strings.Contains(signal.Detail, "unreachable") {
		t.Fatalf("signal = %#v, want unreachable", signal)
	}
}

func TestExecReadySignalTrueAfterNoopExec(t *testing.T) {
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		if err := execprotocol.DecodeMessage(conn, &req); err != nil {
			t.Errorf("DecodeMessage: %v", err)
			return
		}
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		if err := execprotocol.EncodeMessage(conn, result); err != nil {
			t.Errorf("EncodeMessage: %v", err)
		}
	})
	defer stop()
	state := execRuntimeState(vmkit.BackendFirecracker, vmkit.StateRunning, port)
	signal, ok := ExecReadinessSignal(context.Background(), state, time.Second)
	if !ok {
		t.Fatal("ExecReadinessSignal ok = false, want true")
	}
	if !signal.Ready {
		t.Fatalf("signal = %#v, want ready", signal)
	}
}

func TestExecReadySignalFalseOnProbeTimeout(t *testing.T) {
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		_ = execprotocol.DecodeMessage(conn, &req)
		time.Sleep(time.Second)
	})
	defer stop()
	state := execRuntimeState(vmkit.BackendFirecracker, vmkit.StateRunning, port)
	signal, ok := ExecReadinessSignal(context.Background(), state, 25*time.Millisecond)
	if !ok {
		t.Fatal("ExecReadinessSignal ok = false, want true")
	}
	if signal.Ready || !strings.Contains(signal.Detail, "unreachable") {
		t.Fatalf("signal = %#v, want timeout/unreachable", signal)
	}
}

func TestExecReadySignalFalseOnNonzeroProbe(t *testing.T) {
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		_ = execprotocol.DecodeMessage(conn, &req)
		code := 7
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stop()
	state := execRuntimeState(vmkit.BackendFirecracker, vmkit.StateRunning, port)
	signal, ok := ExecReadinessSignal(context.Background(), state, time.Second)
	if !ok {
		t.Fatal("ExecReadinessSignal ok = false, want true")
	}
	if signal.Ready || !strings.Contains(signal.Detail, "exit_code=7") {
		t.Fatalf("signal = %#v, want nonzero probe detail", signal)
	}
}

func TestExecReadySignalFalseOnServiceError(t *testing.T) {
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		_ = execprotocol.DecodeMessage(conn, &req)
		result := execprotocol.NewExecResult(execprotocol.ExecStatusFailedToStart)
		result.Error = &execprotocol.ExecError{Code: "invalid_request", Message: "bad request"}
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stop()
	state := execRuntimeState(vmkit.BackendFirecracker, vmkit.StateRunning, port)
	signal, ok := ExecReadinessSignal(context.Background(), state, time.Second)
	if !ok {
		t.Fatal("ExecReadinessSignal ok = false, want true")
	}
	if signal.Ready || !strings.Contains(signal.Detail, "invalid_request") {
		t.Fatalf("signal = %#v, want service error detail", signal)
	}
}

func TestExecReadySignalIndependentOfShellReady(t *testing.T) {
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		_ = execprotocol.DecodeMessage(conn, &req)
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stop()
	state := execRuntimeState(vmkit.BackendFirecracker, vmkit.StateRunning, port)
	state.SerialInputPath = ""
	readiness := readinessFromRuntime(state)
	if !readiness.ExecReady.Ready {
		t.Fatalf("execReady = %#v, want ready", readiness.ExecReady)
	}
	if readiness.ShellReady.Ready {
		t.Fatalf("shellReady = %#v, want not ready/empty", readiness.ShellReady)
	}
}

func startWorkspaceExecServer(t *testing.T, handle func(net.Conn)) (string, uint16, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconvParseUint16(portText)
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
					t.Errorf("Accept: %v", err)
				}
				return
			}
			go func() {
				defer conn.Close()
				handle(conn)
			}()
		}
	}()
	return listener.Addr().String(), port, func() {
		_ = listener.Close()
		<-done
	}
}

func writeExecRuntimeState(t *testing.T, backend string, state vmkit.VMState, execPort uint16) Options {
	t.Helper()
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir, Backend: backend, ExecPort: execPort}
	req := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err := WriteProcessState(opts, req, state, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	return opts
}

func execRuntimeState(backend string, state vmkit.VMState, execPort uint16) RuntimeState {
	return RuntimeState{
		Event: EventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent-1", Backend: backend},
			State:      state,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config:    vmkit.Config{StateDir: os.TempDir(), ExecPort: execPort},
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func strconvParseUint16(raw string) (uint16, error) {
	value, err := strconv.ParseUint(raw, 10, 16)
	return uint16(value), err
}

func TestWaitForExecReadyGate(t *testing.T) {
	saved := execReadinessProbe
	t.Cleanup(func() { execReadinessProbe = saved })

	t.Run("ready immediately probes once", func(t *testing.T) {
		calls := 0
		execReadinessProbe = func(context.Context, RuntimeState, time.Duration) (vmkit.ReadinessSignal, bool) {
			calls++
			return vmkit.ReadinessSignal{Ready: true}, true
		}
		start := time.Now()
		waitForExecReady(context.Background(), RuntimeState{}, ExecReadyWait)
		if calls != 1 {
			t.Fatalf("probe calls = %d, want 1", calls)
		}
		if time.Since(start) > time.Second {
			t.Fatal("blocked despite immediate readiness")
		}
	})

	t.Run("waits until ready", func(t *testing.T) {
		calls := 0
		execReadinessProbe = func(context.Context, RuntimeState, time.Duration) (vmkit.ReadinessSignal, bool) {
			calls++
			return vmkit.ReadinessSignal{Ready: calls >= 3}, true
		}
		waitForExecReady(context.Background(), RuntimeState{}, 2*time.Second)
		if calls < 3 {
			t.Fatalf("probe calls = %d, want >= 3", calls)
		}
	})

	t.Run("bounded by grace when never ready", func(t *testing.T) {
		calls := 0
		execReadinessProbe = func(context.Context, RuntimeState, time.Duration) (vmkit.ReadinessSignal, bool) {
			calls++
			return vmkit.ReadinessSignal{Ready: false}, true
		}
		start := time.Now()
		waitForExecReady(context.Background(), RuntimeState{}, 300*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("did not respect grace: %s", elapsed)
		}
		if calls == 0 {
			t.Fatal("expected at least one probe")
		}
	})

	t.Run("zero grace skips probing", func(t *testing.T) {
		calls := 0
		execReadinessProbe = func(context.Context, RuntimeState, time.Duration) (vmkit.ReadinessSignal, bool) {
			calls++
			return vmkit.ReadinessSignal{Ready: false}, true
		}
		waitForExecReady(context.Background(), RuntimeState{}, 0)
		if calls != 0 {
			t.Fatalf("probe calls = %d, want 0", calls)
		}
	})

	t.Run("cancelled context returns promptly", func(t *testing.T) {
		execReadinessProbe = func(context.Context, RuntimeState, time.Duration) (vmkit.ReadinessSignal, bool) {
			return vmkit.ReadinessSignal{Ready: false}, true
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		waitForExecReady(ctx, RuntimeState{}, 10*time.Second)
		if time.Since(start) > 2*time.Second {
			t.Fatal("cancelled context did not short-circuit")
		}
	})
}
