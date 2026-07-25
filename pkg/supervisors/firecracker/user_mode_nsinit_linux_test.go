//go:build linux

package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProcessIsNestedNamespaceInit uses the exact /proc/<pid>/status shapes this
// kernel produces: a host process has a single NSpid entry, while a process
// pasta spawned is PID 1 of a nested namespace and carries a trailing 1.
func TestProcessIsNestedNamespaceInit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   bool
	}{
		{"host process", "Name:\tbash\nNSpid:\t524358\nNSpgid:\t524358\n", false},
		{"nested namespace init", "Name:\tsleep\nNSpid:\t524364\t1\nNSpgid:\t524364\t1\n", true},
		{"nested but not init", "Name:\tsh\nNSpid:\t524370\t7\n", false},
		{"deeply nested init", "NSpid:\t900\t44\t1\n", true},
		{"no NSpid line", "Name:\tsh\nPid:\t5\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := processIsNestedNamespaceInit([]byte(tc.status)); got != tc.want {
				t.Fatalf("processIsNestedNamespaceInit = %v, want %v", got, tc.want)
			}
		})
	}
}

// startWorkspaceMarkedProcess starts a live process whose argv carries the
// workspace path, so processReferencesWorkspace recognizes it the way it
// recognizes a real supervisor child. The returned channel fires when the
// process is REAPED — signal 0 is useless here, because a killed-but-unreaped
// child lingers as a zombie and still answers it.
func startWorkspaceMarkedProcess(t *testing.T, marker string) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	// The trailing ":" defeats sh's exec optimization — with a single command sh
	// execs it and the marker disappears from argv, which is exactly what
	// processReferencesWorkspace reads.
	cmd := exec.Command("/bin/sh", "-c", "sleep 60; :", marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(reaped)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// Wait for the exec to land: a process caught mid-exec has an empty
	// /proc/<pid>/cmdline, and the identity check would then correctly decline
	// to kill it. Real captures record the ns-init long after it has exec'd.
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/cmdline")
		if err == nil && strings.Contains(strings.ReplaceAll(string(raw), "\x00", " "), marker) {
			return cmd, reaped
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker %q never appeared in the test process argv", marker)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertReaped(t *testing.T, reaped <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-reaped:
	case <-time.After(8 * time.Second):
		t.Fatal(msg)
	}
}

// TestCleanupUserNetworkNSInitKillsTheVM is the containment fix: pasta's death
// must not leave a live VM behind. Killing the recorded namespace init is what
// makes the kernel tear the pid namespace down, firecracker included, so this
// asserts the recorded process is actually signaled.
func TestCleanupUserNetworkNSInitKillsTheVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	if err := os.MkdirAll(userNetworkStateDir(opts), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd, reaped := startWorkspaceMarkedProcess(t, filepath.Join(dir, "agent-1"))
	if err := os.WriteFile(userNetworkNSInitPIDPath(opts), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupUserNetworkNSInit(opts)

	assertReaped(t, reaped, "the recorded namespace init is still alive; a dead pasta would leave a running VM")
	if _, err := os.Stat(userNetworkNSInitPIDPath(opts)); !os.IsNotExist(err) {
		t.Fatalf("nsinit pid file survived cleanup: %v", err)
	}
}

// TestCleanupUserNetworkNSInitSpareUnrelatedPID: pids are recycled, and this one
// may have been recorded long before an unrelated process inherited the number.
// Killing it would be worse than the leak, so a recorded pid that no longer
// carries this workspace's identity must be left strictly alone.
func TestCleanupUserNetworkNSInitSparesUnrelatedPID(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	if err := os.MkdirAll(userNetworkStateDir(opts), 0o700); err != nil {
		t.Fatal(err)
	}
	// Alive, but belongs to a DIFFERENT workspace — the recycled-pid case.
	cmd, _ := startWorkspaceMarkedProcess(t, filepath.Join(dir, "some-other-workspace"))
	if err := os.WriteFile(userNetworkNSInitPIDPath(opts), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupUserNetworkNSInit(opts)

	time.Sleep(50 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("cleanup killed a process that does not belong to this workspace")
	}
}

// TestCleanupUserNetworkProcessTearsDownNSInitFirst: every containment and reap
// path (stop, halt, kill, quarantine, gc) funnels through
// cleanupUserNetworkProcess. If it only signaled pasta, all of them would report
// success while the guest kept running — so it must take the VM down too.
func TestCleanupUserNetworkProcessTearsDownNSInit(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	if err := os.MkdirAll(userNetworkStateDir(opts), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd, reaped := startWorkspaceMarkedProcess(t, filepath.Join(dir, "agent-1"))
	if err := os.WriteFile(userNetworkNSInitPIDPath(opts), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	// pasta itself is already gone — exactly the leak's starting condition.
	if err := os.WriteFile(userNetworkPIDPath(opts), []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupUserNetworkProcess(opts)

	assertReaped(t, reaped, "containment left the VM running when pasta was already dead")
}
