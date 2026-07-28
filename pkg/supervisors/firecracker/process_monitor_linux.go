//go:build linux

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func waitForeground(ctx context.Context, cmd *exec.Cmd, serialPath string, timeout time.Duration) error {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-waitCh:
			return err
		case <-ticker.C:
			if GuestHalted(serialPath) {
				if err := terminateProcess(cmd.Process, waitCh, 5*time.Second); err != nil {
					return err
				}
				return nil
			}
		case <-timer:
			_ = cmd.Process.Kill()
			<-waitCh
			return fmt.Errorf("firecracker process did not exit before timeout")
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-waitCh
			return ctx.Err()
		}
	}
}

func GuestHalted(serialPath string) bool {
	data, err := os.ReadFile(serialPath)
	if err != nil {
		return false
	}
	log := string(data)
	return strings.Contains(log, "reboot: System halted") ||
		strings.Contains(log, "reboot: Power down")
}

func terminateProcess(process *os.Process, waitCh <-chan error, timeout time.Duration) error {
	if process == nil {
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waitCh:
		return nil
	case <-timer.C:
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-waitCh
		return nil
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, signal); err == nil || err != syscall.ESRCH {
		return err
	}
	return syscall.Kill(pid, signal)
}

func detachedStartExitError(cmd *exec.Cmd, delay time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	var status syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
	if err != nil {
		if err == syscall.ECHILD {
			return nil
		}
		return fmt.Errorf("check detached firecracker process %d: %w", cmd.Process.Pid, err)
	}
	if pid == 0 {
		return nil
	}
	if status.Exited() {
		return fmt.Errorf("firecracker exited during detached startup: exit status %d", status.ExitStatus())
	}
	if status.Signaled() {
		return fmt.Errorf("firecracker exited during detached startup: signal %s", status.Signal())
	}
	return fmt.Errorf("firecracker exited during detached startup: wait status %d", status)
}

func processActive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			return false, nil
		}
		return false, err
	}
	if state, err := linuxProcessState(pid); err == nil && state == "Z" {
		return false, nil
	}
	return true, nil
}

// processReferencesWorkspace reports whether the live process pid is actually
// this workspace's own (firecracker/pasta/companion). This guards the gc
// liveness check against PID reuse: a recycled pid is alive but won't reference
// this workspace.
//
// An unconfined process carries the workspace state path in its argv. A confined
// (VMM-process confinement) firecracker is pivot_root'd into the jail and exec'd
// with jail-relative argv, so the workspace path is absent from its cmdline —
// but its mount namespace binds the per-workspace jail root (<workspace>/jail),
// which appears in its mountinfo. Either match is reuse-safe: a recycled
// unrelated pid carries neither.
func processReferencesWorkspace(pid int, opts Options) bool {
	if pid <= 0 {
		return false
	}
	wsPath := filepath.Join(opts.StateDir, opts.Name)
	cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	mountinfo, _ := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", pid))
	if processIdentityReferencesWorkspace(cmdline, mountinfo, wsPath) {
		return true
	}
	return processRootMatchesJail(pid, wsPath)
}

// processRootMatchesJail reports whether pid's root directory is this
// workspace's jail — the confined firecracker after pivot_root. The mountinfo
// match above fails when the jail lives on tmpfs or a btrfs subvolume: the
// kernel records a bind source relative to that filesystem's own root, so the
// host-absolute jail path never appears in mountinfo and a live confined VM
// would be mistaken for a reused PID and reaped. Comparing /proc/<pid>/root
// against the jail directory by device and inode is filesystem-agnostic. A
// var for test injection.
var processRootMatchesJail = func(pid int, wsPath string) bool {
	if pid <= 0 || wsPath == "" {
		return false
	}
	procRoot, err := os.Stat(fmt.Sprintf("/proc/%d/root", pid))
	if err != nil {
		return false
	}
	jail, err := os.Stat(filepath.Join(wsPath, "jail"))
	if err != nil {
		return false
	}
	return os.SameFile(procRoot, jail)
}

var firecrackerProcessConfinedToWorkspace = func(pid int, opts Options) bool {
	if pid <= 0 {
		return false
	}
	wsPath := filepath.Join(opts.StateDir, opts.Name)
	children := linuxProcessChildrenByParent()
	return processTreeReferencesWorkspaceJail(pid, wsPath, children, func(pid int) []byte {
		mountinfo, _ := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", pid))
		return mountinfo
	}, func(pid int) bool {
		return processRootMatchesJail(pid, wsPath)
	})
}

// processIdentityReferencesWorkspace is the pure matcher behind
// processReferencesWorkspace: the workspace state path appears in an unconfined
// process's argv (NUL-separated cmdline), or the per-workspace jail root
// (<workspace>/jail) appears in a confined firecracker's mountinfo (its
// pivot_root'd mount namespace binds that jail). Either is reuse-safe — a
// recycled unrelated pid carries neither.
func processIdentityReferencesWorkspace(cmdline, mountinfo []byte, wsPath string) bool {
	if wsPath == "" {
		return false
	}
	if strings.Contains(strings.ReplaceAll(string(cmdline), "\x00", " "), wsPath) {
		return true
	}
	return processMountinfoReferencesWorkspaceJail(mountinfo, wsPath)
}

func processMountinfoReferencesWorkspaceJail(mountinfo []byte, wsPath string) bool {
	if wsPath == "" {
		return false
	}
	return strings.Contains(string(mountinfo), filepath.Join(wsPath, "jail"))
}

// processTreeReferencesWorkspaceJail checks the recorded runtime PID and its
// descendants for the workspace jail. In user-network mode the recorded
// runtime PID is pasta, while the confined Firecracker process is a
// descendant with the jail bind in its own mount namespace. A process matches
// through its mountinfo (jail path visible when the state dir's filesystem
// records bind sources host-absolute) or through rootIs (device+inode
// identity of its pivot_root'd root; see processRootMatchesJail). Either
// injected check may be nil.
func processTreeReferencesWorkspaceJail(rootPID int, wsPath string, childrenByParent map[int][]int, mountinfo func(int) []byte, rootIs func(int) bool) bool {
	if rootPID <= 0 || wsPath == "" {
		return false
	}
	queue := []int{rootPID}
	seen := map[int]bool{}
	for len(queue) != 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if mountinfo != nil && processMountinfoReferencesWorkspaceJail(mountinfo(pid), wsPath) {
			return true
		}
		if rootIs != nil && rootIs(pid) {
			return true
		}
		for _, child := range childrenByParent[pid] {
			if !seen[child] {
				queue = append(queue, child)
			}
		}
	}
	return false
}

func linuxProcessChildrenByParent() map[int][]int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid, err := linuxProcessStatParentPID(data)
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	return children
}

func linuxProcessStatParentPID(data []byte) (int, error) {
	stat := string(data)
	commEnd := strings.LastIndex(stat, ")")
	if commEnd == -1 {
		return 0, fmt.Errorf("invalid proc stat: missing command terminator")
	}
	fields := strings.Fields(stat[commEnd+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("invalid proc stat: missing parent pid")
	}
	return strconv.Atoi(fields[1])
}

// leaseExpired reports whether a workspace declared a lifetime lease
// (Config.LeaseSeconds > 0) and has now been idle past it. "Idle" is measured
// from the last recorded activity (a connection on the exec/shell port) or, before
// any activity, from StartedAt. A zero lease means permanent — never expired.
// Activity renews the deadline, so an actively-used VM is never reaped; only an
// idle one is.
func leaseExpired(state runtimeState, opts Options) bool {
	if state.Config.LeaseSeconds <= 0 {
		return false
	}
	base := time.Time{}
	if state.StartedAt != "" {
		if t, err := time.Parse(time.RFC3339, state.StartedAt); err == nil {
			base = t
		}
	}
	if at, ok := workspaceActivityTime(opts); ok && at.After(base) {
		base = at
	}
	if base.IsZero() {
		return false
	}
	return time.Now().After(base.Add(time.Duration(state.Config.LeaseSeconds) * time.Second))
}

// workspaceActivityPath is the marker file whose mtime records the last time the
// VM was genuinely used (an exec or connect), written by workspace.MarkActivity.
// A single-purpose file avoids a read-modify-write race on runtime.json across
// processes; last-writer-wins is exactly the semantics we want. Keep the filename
// in sync with workspace.MarkActivity.
func workspaceActivityPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "activity")
}

// workspaceActivityTime returns the last-activity time, or ok=false before any
// activity has been recorded.
func workspaceActivityTime(opts Options) (time.Time, bool) {
	fi, err := os.Stat(workspaceActivityPath(opts))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

func waitForProcessExit(ctx context.Context, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		active, err := processActive(pid)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process %d did not exit before timeout", pid)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// terminateRecordedCompanions kills any companion processes recorded in the
// workspace runtime state. Only the recorded companion PIDs are touched; the
// VM process entry is left alone.
func terminateRecordedCompanions(opts Options) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return
	}
	if state.PortForwardPID != 0 {
		terminateAuxProcess(state.PortForwardPID)
	}
	if state.VsockListenerPID != 0 {
		terminateAuxProcess(state.VsockListenerPID)
	}
	if state.EgressMediatorPID != 0 {
		terminateAuxProcess(state.EgressMediatorPID)
	}
}

func terminateAuxProcess(pid int) {
	if pid <= 0 {
		return
	}
	_ = signalProcessGroup(pid, syscall.SIGTERM)
	if err := waitForProcessExit(context.Background(), pid, 2*time.Second); err == nil {
		return
	}
	_ = signalProcessGroup(pid, syscall.SIGKILL)
	_ = waitForProcessExit(context.Background(), pid, time.Second)
}

func linuxProcessState(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return "", fmt.Errorf("invalid proc stat for pid %d", pid)
	}
	return fields[2], nil
}
