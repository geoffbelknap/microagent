//go:build linux

package main

import (
	"errors"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// waitForZombie polls until pid is a zombie child of this process (exited but not
// yet reaped), failing if it does not become one in time.
func waitForZombie(t *testing.T, r *childReaper, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, ppid, ok := readProcStat(pid)
		if ok && state == 'Z' && ppid == r.selfPID {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d did not become a zombie child in time", pid)
}

// TestReaperReapsUntrackedZombie is the core B7 guard: a child that nothing is
// waiting on (a fire-and-forget helper, or an orphaned grandchild reparented
// here) is reaped rather than left as a zombie.
func TestReaperReapsUntrackedZombie(t *testing.T) {
	r := newChildReaper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil { // deliberately NOT tracked and never Wait()ed
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	waitForZombie(t, r, pid)

	r.reapOrphans()

	// If reapOrphans reaped it, a subsequent Wait4 finds no such child.
	wpid, _ := unix.Wait4(pid, nil, unix.WNOHANG, nil)
	if wpid == pid {
		t.Fatal("zombie was still reapable after reapOrphans; the reaper did not reap it")
	}
}

// TestReaperSkipsTrackedChild proves the reaper never steals a child that code
// intends to cmd.Wait(): a tracked child survives reapOrphans and its exit code
// is still delivered to cmd.Wait (the failure this design avoids — a global
// Wait4(-1) reaper would break every cmd.Wait in guest-init).
func TestReaperSkipsTrackedChild(t *testing.T) {
	r := newChildReaper()
	cmd := exec.Command("sh", "-c", "exit 7")
	if err := r.startTracked(cmd); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	waitForZombie(t, r, pid)

	r.reapOrphans() // must skip the tracked child

	err := cmd.Wait()
	r.untrack(pid)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("tracked child Wait = %v, want an ExitError with code 7 (reaper stole it?)", err)
	}
	if exitErr.ExitCode() != 7 {
		t.Fatalf("tracked child exit code = %d, want 7", exitErr.ExitCode())
	}
}

// TestReaperRunTrackedReturnsExitStatus checks the cmd.Run() replacement returns
// the real exit status while the child is protected from the reaper.
func TestReaperRunTrackedReturnsExitStatus(t *testing.T) {
	r := newChildReaper()
	// Reap concurrently to simulate the running reaper; runTracked must be immune.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				r.reapOrphans()
				time.Sleep(time.Millisecond)
			}
		}
	}()
	err := r.runTracked(exec.Command("sh", "-c", "exit 0"))
	close(done)
	if err != nil {
		t.Fatalf("runTracked = %v, want nil (exit 0)", err)
	}
}
