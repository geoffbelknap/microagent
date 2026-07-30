//go:build linux

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/workspace"
	"golang.org/x/sys/unix"
)

type shellTestAddr string

func (a shellTestAddr) Network() string { return "socketpair" }
func (a shellTestAddr) String() string  { return string(a) }

type shellTestConn struct {
	*os.File
}

func (c shellTestConn) LocalAddr() net.Addr                { return shellTestAddr("local") }
func (c shellTestConn) RemoteAddr() net.Addr               { return shellTestAddr("remote") }
func (c shellTestConn) SetDeadline(t time.Time) error      { return c.File.SetDeadline(t) }
func (c shellTestConn) SetReadDeadline(t time.Time) error  { return c.File.SetReadDeadline(t) }
func (c shellTestConn) SetWriteDeadline(t time.Time) error { return c.File.SetWriteDeadline(t) }

func startSocketPairShellSession(t *testing.T) (*os.File, <-chan struct{}) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	client := os.NewFile(uintptr(fds[1]), "shell-session-test-client")
	if client == nil {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
		t.Fatal("wrap shell session test client")
	}
	done := make(chan struct{})
	go func() {
		runShellSession(fds[0], "/bin/sh")
		close(done)
	}()
	return client, done
}

func waitForShellSession(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shell session did not terminate")
	}
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid file %s was not written", path)
	return 0
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	path := filepath.Join("/proc", strconv.Itoa(pid))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d survived shell session teardown", pid)
}

func waitForProcessNotRunning(t *testing.T, pid int) {
	t.Helper()
	path := filepath.Join("/proc", strconv.Itoa(pid))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		state, _, ok := readProcStat(pid)
		if ok && state == 'Z' {
			// In the guest this orphan is reparented to guest-init (PID 1), whose
			// separately-tested child reaper collects it. The unit-test process is
			// not PID 1, so the host namespace's init owns final collection here.
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remained live after shell session teardown", pid)
}

func directChildPIDs() map[int]struct{} {
	children := map[int]struct{}{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return children
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		_, ppid, ok := readProcStat(pid)
		if ok && ppid == os.Getpid() {
			children[pid] = struct{}{}
		}
	}
	return children
}

func assertNoNewDirectChildren(t *testing.T, before map[int]struct{}) {
	t.Helper()
	for pid := range directChildPIDs() {
		if _, existed := before[pid]; !existed {
			t.Fatalf("shell session left child pid %d behind", pid)
		}
	}
}

func trackedChildCount() int {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	return len(reaper.tracked)
}

func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestShellSessionImmediateDisconnectLeavesNoChild(t *testing.T) {
	beforeChildren := directChildPIDs()
	beforeTracked := trackedChildCount()
	client, done := startSocketPairShellSession(t)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitForShellSession(t, done)
	assertNoNewDirectChildren(t, beforeChildren)
	if got := trackedChildCount(); got != beforeTracked {
		t.Fatalf("tracked children = %d, want baseline %d", got, beforeTracked)
	}
}

func TestRepeatedImmediateShellDisconnectsStayBounded(t *testing.T) {
	beforeChildren := directChildPIDs()
	beforeTracked := trackedChildCount()
	beforeFDs := openFDCount(t)
	beforeGoroutines := runtime.NumGoroutine()

	for range 50 {
		client, done := startSocketPairShellSession(t)
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		waitForShellSession(t, done)
	}
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	assertNoNewDirectChildren(t, beforeChildren)
	if got := trackedChildCount(); got != beforeTracked {
		t.Fatalf("tracked children grew from %d to %d", beforeTracked, got)
	}
	if got := openFDCount(t); got > beforeFDs+2 {
		t.Fatalf("open fds grew from %d to %d", beforeFDs, got)
	}
	if got := runtime.NumGoroutine(); got > beforeGoroutines+4 {
		t.Fatalf("goroutines grew from %d to %d", beforeGoroutines, got)
	}
}

func TestShellSessionDisconnectTerminatesProcessGroup(t *testing.T) {
	dir := t.TempDir()
	shellPIDPath := filepath.Join(dir, "shell.pid")
	childPIDPath := filepath.Join(dir, "child.pid")
	client, done := startSocketPairShellSession(t)
	command := fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait\r", shellPIDPath, childPIDPath)
	if _, err := client.Write([]byte(command)); err != nil {
		t.Fatal(err)
	}
	shellPID := readPIDFile(t, shellPIDPath)
	childPID := readPIDFile(t, childPIDPath)
	if shellPID == childPID {
		t.Fatalf("shell and child unexpectedly share pid %d", shellPID)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitForShellSession(t, done)
	waitForProcessGone(t, shellPID)
	waitForProcessNotRunning(t, childPID)
}

func TestShellSessionExplicitlyDetachedProcessSurvivesDisconnect(t *testing.T) {
	setsidPath, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is unavailable")
	}
	dir := t.TempDir()
	childPIDPath := filepath.Join(dir, "detached.pid")
	client, done := startSocketPairShellSession(t)
	command := fmt.Sprintf(
		"%s /bin/sh -c 'echo $$ > %s; exec sleep 300' </dev/null >/dev/null 2>&1 &\r",
		setsidPath,
		childPIDPath,
	)
	if _, err := client.Write([]byte(command)); err != nil {
		t.Fatal(err)
	}
	childPID := readPIDFile(t, childPIDPath)
	t.Cleanup(func() {
		_ = unix.Kill(-childPID, unix.SIGKILL)
		waitForProcessNotRunning(t, childPID)
	})
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitForShellSession(t, done)
	state, _, _, session, ok := readProcStatIdentity(childPID)
	if !ok || state == 'Z' {
		t.Fatalf("explicitly detached process %d did not survive disconnect", childPID)
	}
	if session != childPID {
		t.Fatalf("detached process session = %d, want its pid %d", session, childPID)
	}
}

func TestShellCommandReadinessRoundTripExitsAndIsReaped(t *testing.T) {
	beforeChildren := directChildPIDs()
	beforeTracked := trackedChildCount()
	clientFile, sessionDone := startSocketPairShellSession(t)
	conn := shellTestConn{File: clientFile}
	target := workspace.ShellTarget{Network: "test", Address: "socketpair"}
	elapsed, err := workspace.ProbeShellCommand(
		context.Background(),
		workspace.ConsoleOptions{Name: "readiness-test"},
		target,
		3*time.Second,
		func(context.Context, workspace.ShellTarget) (net.Conn, error) {
			return conn, nil
		},
	)
	if err != nil {
		t.Fatalf("ProbeShellCommand after %s: %v", elapsed, err)
	}
	waitForShellSession(t, sessionDone)
	assertNoNewDirectChildren(t, beforeChildren)
	if got := trackedChildCount(); got != beforeTracked {
		t.Fatalf("tracked children = %d, want baseline %d", got, beforeTracked)
	}
}
