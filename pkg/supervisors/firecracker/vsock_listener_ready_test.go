//go:build linux

package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readyTestOpts(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "ws"}
	if err := os.MkdirAll(filepath.Join(dir, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	return opts
}

// deadPID returns the pid of a process that has already exited and been reaped,
// so processActive reports it inactive.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

func TestWaitForVsockListenersReadyMarkerPresent(t *testing.T) {
	opts := readyTestOpts(t)
	if err := os.WriteFile(vsockListenerReadyPath(opts), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The marker is checked first, so a dead pid must not matter once it exists.
	if err := waitForVsockListenersReady(opts, deadPID(t), time.Second); err != nil {
		t.Fatalf("marker present should be ready: %v", err)
	}
}

func TestWaitForVsockListenersReadyProcessDiedSurfacesLog(t *testing.T) {
	opts := readyTestOpts(t)
	const logLine = `egress broker: resolve secret "anthropic": reference must be projects/<p>/secrets/<s>`
	if err := os.WriteFile(vsockListenerLogPath(opts), []byte(logLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := waitForVsockListenersReady(opts, deadPID(t), time.Second)
	if err == nil {
		t.Fatal("a died listener process must fail readiness, not pass")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("error must carry the listener log so the operator sees the cause; got: %v", err)
	}
}

func TestWaitForVsockListenersReadyProcessDiedNoLog(t *testing.T) {
	opts := readyTestOpts(t)
	err := waitForVsockListenersReady(opts, deadPID(t), time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited before its listeners were ready") {
		t.Fatalf("a died process with no log must still fail loudly; got: %v", err)
	}
}

func TestWaitForVsockListenersReadyTimeout(t *testing.T) {
	opts := readyTestOpts(t)
	// A live process that never signals ready must be bounded by the timeout.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	err := waitForVsockListenersReady(opts, cmd.Process.Pid, 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not ready after") {
		t.Fatalf("a hung startup must time out; got: %v", err)
	}
}

// startVsockListenerProcess must clear a prior run's readiness marker so a
// relaunch cannot read a stale marker as this launch having come up. We can't
// spawn the real detached process in a unit test, but we can assert the
// pre-spawn cleanup contract directly.
func TestStartVsockListenerProcessClearsStaleReadyMarker(t *testing.T) {
	opts := readyTestOpts(t)
	if err := os.WriteFile(vsockListenerReadyPath(opts), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// startVsockListenerProcess spawns a real subprocess; we only care that the
	// stale marker is gone by the time it returns (the subprocess will exit
	// fast with a usage/parse error on the temp state, which is fine here).
	if _, err := startVsockListenerProcess(opts); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if _, err := os.Stat(vsockListenerReadyPath(opts)); !os.IsNotExist(err) {
		t.Fatalf("stale readiness marker must be cleared before launch; stat err=%v", err)
	}
}
