//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type activeWorkloadState struct {
	mu      sync.Mutex
	process *os.Process
	done    chan struct{}
}

var activeWorkload activeWorkloadState

func trackActiveWorkload(process *os.Process) func() {
	done := make(chan struct{})
	activeWorkload.mu.Lock()
	activeWorkload.process = process
	activeWorkload.done = done
	activeWorkload.mu.Unlock()
	return func() {
		activeWorkload.mu.Lock()
		if activeWorkload.done == done {
			activeWorkload.process = nil
			activeWorkload.done = nil
			close(done)
		}
		activeWorkload.mu.Unlock()
	}
}

func signalActiveWorkload(signal syscall.Signal, timeout time.Duration) {
	activeWorkload.mu.Lock()
	process := activeWorkload.process
	done := activeWorkload.done
	activeWorkload.mu.Unlock()
	if process == nil {
		return
	}
	// Workloads are process-group leaders. Signal the group so shell wrappers
	// and their children receive the OCI stop signal together.
	if err := unix.Kill(-process.Pid, signal); err != nil && err != unix.ESRCH {
		_ = process.Signal(signal)
	}
	if timeout <= 0 || done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
		_ = unix.Kill(-process.Pid, unix.SIGKILL)
	}
}

func parseOCIStopSignal(value string) (syscall.Signal, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return syscall.SIGTERM, nil
	}
	if number, err := strconv.Atoi(value); err == nil {
		if number <= 0 || number >= 65 {
			return 0, fmt.Errorf("invalid OCI stop signal %q", value)
		}
		return syscall.Signal(number), nil
	}
	value = strings.TrimPrefix(value, "SIG")
	if strings.HasPrefix(value, "RTMIN+") {
		offset, err := strconv.Atoi(strings.TrimPrefix(value, "RTMIN+"))
		if err == nil && offset >= 0 && 34+offset <= 64 {
			return syscall.Signal(34 + offset), nil
		}
		return 0, fmt.Errorf("invalid OCI stop signal %q", value)
	}
	if strings.HasPrefix(value, "RTMAX-") {
		offset, err := strconv.Atoi(strings.TrimPrefix(value, "RTMAX-"))
		if err == nil && offset >= 0 && 64-offset >= 34 {
			return syscall.Signal(64 - offset), nil
		}
		return 0, fmt.Errorf("invalid OCI stop signal %q", value)
	}
	signals := map[string]syscall.Signal{
		"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
		"ILL": syscall.SIGILL, "ABRT": syscall.SIGABRT, "FPE": syscall.SIGFPE,
		"KILL": syscall.SIGKILL, "SEGV": syscall.SIGSEGV, "PIPE": syscall.SIGPIPE,
		"ALRM": syscall.SIGALRM, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1,
		"USR2": syscall.SIGUSR2, "CHLD": syscall.SIGCHLD, "CONT": syscall.SIGCONT,
		"STOP": syscall.SIGSTOP, "TSTP": syscall.SIGTSTP, "TTIN": syscall.SIGTTIN,
		"TTOU": syscall.SIGTTOU, "BUS": syscall.SIGBUS, "TRAP": syscall.SIGTRAP,
		"URG": syscall.SIGURG, "XCPU": syscall.SIGXCPU, "XFSZ": syscall.SIGXFSZ,
		"VTALRM": syscall.SIGVTALRM, "PROF": syscall.SIGPROF, "WINCH": syscall.SIGWINCH,
		"IO": syscall.SIGIO, "POLL": syscall.SIGIO, "PWR": syscall.SIGPWR,
		"SYS": syscall.SIGSYS,
	}
	if signal, ok := signals[value]; ok {
		return signal, nil
	}
	return 0, fmt.Errorf("invalid OCI stop signal %q", value)
}
