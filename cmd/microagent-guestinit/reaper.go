//go:build linux

package main

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// childReaper reaps zombie children that no code is actively waiting on —
// orphaned grandchildren reparented to PID 1 and fire-and-forget helpers — so
// they do not accumulate for the life of the VM. It deliberately does NOT use
// Wait4(-1): that would race and steal the children that other code reaps with
// cmd.Wait() (the managed-service loop, the run/interactive workload, connect
// shells), breaking their exit-status handling. Instead it scans /proc for this
// process's zombie children and reaps only the ones not marked as tracked, by
// specific pid. Callers that will cmd.Wait() a child mark it via startTracked /
// runTracked so the reaper leaves it alone.
type childReaper struct {
	mu      sync.Mutex
	tracked map[int]struct{}
	selfPID int
}

func newChildReaper() *childReaper {
	return &childReaper{tracked: map[int]struct{}{}, selfPID: os.Getpid()}
}

// reaper is the process-wide child reaper. run() starts it early (as PID 1); the
// workload, managed-service, and shell wait sites mark their children with it so
// only true orphans and fire-and-forget helpers are reaped here.
var reaper = newChildReaper()

// startTracked starts cmd and records its pid so reapOrphans will not reap it out
// from under a later cmd.Wait. mu is held across Start+record so the reaper can
// never observe the pid as an untracked zombie: reapOrphans takes the same lock
// to test trackedness, so if the child is created here it is recorded before the
// reaper can decide to reap it.
func (r *childReaper) startTracked(cmd *exec.Cmd) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	r.tracked[cmd.Process.Pid] = struct{}{}
	return nil
}

func (r *childReaper) untrack(pid int) {
	r.mu.Lock()
	delete(r.tracked, pid)
	r.mu.Unlock()
}

func (r *childReaper) isTracked(pid int) bool {
	r.mu.Lock()
	_, ok := r.tracked[pid]
	r.mu.Unlock()
	return ok
}

// runTracked runs cmd to completion like cmd.Run(), but tracked so the orphan
// reaper leaves the child for cmd.Wait.
func (r *childReaper) runTracked(cmd *exec.Cmd) error {
	if err := r.startTracked(cmd); err != nil {
		return err
	}
	defer r.untrack(cmd.Process.Pid)
	return cmd.Wait()
}

// reapOrphans reaps every zombie child of this process that is not tracked by an
// active cmd.Wait.
func (r *childReaper) reapOrphans() {
	for _, pid := range r.zombieChildren() {
		if r.isTracked(pid) {
			continue
		}
		var status unix.WaitStatus
		if reaped, err := unix.Wait4(pid, &status, unix.WNOHANG, nil); err == nil && reaped == pid {
			log.Printf("microagent-init: reaped orphan pid %d", pid)
		}
	}
}

// run reaps once up front (clearing any pre-existing zombies), then reaps on every
// SIGCHLD for the life of the process. It never returns.
func (r *childReaper) run() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, unix.SIGCHLD)
	r.reapOrphans()
	for range sig {
		r.reapOrphans()
	}
}

// zombieChildren returns the pids of this process's children currently in the
// zombie (Z) state.
func (r *childReaper) zombieChildren() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		state, ppid, ok := readProcStat(pid)
		if !ok || state != 'Z' || ppid != r.selfPID {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// readProcStat parses the state and parent pid from /proc/<pid>/stat. The comm
// field (field 2) is parenthesized and may itself contain spaces or parentheses,
// so the remaining fields are taken after the LAST ')': state is the first, ppid
// the second.
func readProcStat(pid int) (state byte, ppid int, ok bool) {
	state, ppid, _, _, ok = readProcStatIdentity(pid)
	return state, ppid, ok
}

// readProcStatIdentity also returns process-group and session identity. An
// interactive shell uses job control, so commands can move into process groups
// other than the shell leader's while remaining in its session.
func readProcStatIdentity(pid int) (state byte, ppid, processGroup, session int, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, 0, 0, false
	}
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 4 || fields[0] == "" {
		return 0, 0, 0, 0, false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, 0, 0, false
	}
	processGroup, err = strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, 0, 0, false
	}
	session, err = strconv.Atoi(fields[3])
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return fields[0][0], ppid, processGroup, session, true
}
