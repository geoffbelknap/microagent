//go:build linux

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"golang.org/x/sys/unix"
)

func startPortForwarderProcess(opts Options) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := portForwarderLogPath(opts)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(executable, "--port-forwarder", "--state-dir", opts.StateDir, "--name", opts.Name)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return pid, nil
}

func portForwarderLogPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "port-forward.log")
}

// RunDeadman is the per-VM reaper spawned for every backgrounded (start) VM. It
// waits event-driven on firecracker's exit (via a pidfd) and reconciles the
// workspace through gcWorkspace the instant it dies — recording the terminal
// state and reaping companion processes + transient network without waiting for a
// status read or gc sweep. For a leased VM (--ttl) it also re-checks on an idle
// cadence so an idle-but-alive VM is reaped at its deadline. The deadman's own PID
// is never recorded in runtime state, so the reap's teardown cannot kill it; it
// observes the resulting Stopped/Failed state and exits. The gc sweep remains the
// on-demand backstop.
func RunDeadman(ctx context.Context, opts Options) error {
	for {
		state, err := readRuntimeState(opts)
		if err != nil {
			return nil // runtime state gone — nothing to watch
		}
		if state.Event.State != vmkit.StateRunning {
			return nil // stopped / halted / failed — done
		}
		if _, err := reconcileDeadmanWorkspace(opts); err != nil {
			fmt.Fprintf(os.Stderr, "deadman reconcile %s: %v\n", opts.Name, err)
		}
		// Block until firecracker exits — observed event-driven through a pidfd, so
		// a dead VM is reaped within milliseconds rather than on a poll tick — or,
		// for a leased VM, until the idle re-check cadence elapses, whichever comes
		// first. An unleased VM has no idle deadline, so this is purely the exit
		// wait; the next loop's gcWorkspace then records the terminal state.
		if !waitForProcessExitEvent(ctx, state.PID, deadmanBudget(state.Config.LeaseSeconds)) {
			return ctx.Err() // context canceled
		}
	}
}

// deadmanPollInterval polls at ~1/4 the lease so reap latency past the idle
// deadline stays small, clamped to a sane band.
func deadmanPollInterval(leaseSeconds int) time.Duration {
	d := time.Duration(leaseSeconds) * time.Second / 4
	if d < time.Second {
		d = time.Second
	}
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// deadmanBudget is how long the deadman blocks per cycle before re-reconciling.
// For a leased VM it tracks the idle re-check cadence; an unleased VM has no idle
// deadline, so it is just a coarse safety re-check — the pidfd wait detects a real
// exit instantly regardless of this bound.
func deadmanBudget(leaseSeconds int) time.Duration {
	if leaseSeconds <= 0 {
		return 30 * time.Second
	}
	return deadmanPollInterval(leaseSeconds)
}

// waitForProcessExitEvent blocks until the process exits — observed event-driven
// through a pidfd, no polling — or budget elapses, whichever comes first. It
// returns false only when ctx is canceled. A vanished pid (ESRCH) returns true
// immediately so the caller reconciles now; kernels without pidfd fall back to a
// plain ctx-aware sleep.
func waitForProcessExitEvent(ctx context.Context, pid int, budget time.Duration) bool {
	if pid <= 0 {
		return sleepCtx(ctx, budget)
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return true // already gone — reconcile now
		}
		return sleepCtx(ctx, budget) // no pidfd support — coarse fallback
	}
	defer func() { _ = unix.Close(fd) }()
	deadline := time.Now().Add(budget)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true
		}
		if remaining > time.Second {
			remaining = time.Second // chunk so ctx cancellation is observed promptly
		}
		n, perr := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, int(remaining.Milliseconds()))
		if ctx.Err() != nil {
			return false
		}
		if perr != nil {
			if errors.Is(perr, syscall.EINTR) {
				continue
			}
			return true // treat a poll error as "re-check now"
		}
		if n > 0 {
			return true // POLLIN: the process exited
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func deadmanLogPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "deadman.log")
}

func startDeadmanProcess(opts Options) (int, error) {
	return startDeadmanProcessWithLease(opts, nil)
}

// startDeadmanProcessWithLease transfers ownership of runtimeLease to the
// detached deadman through an inherited descriptor. The caller may close its
// copy after this returns; the deadman's copy keeps the flock held until the VM
// has exited and reconciliation is complete.
func startDeadmanProcessWithLease(opts Options, runtimeLease *os.File) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := deadmanLogPath(opts)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	args := []string{"--deadman", "--state-dir", opts.StateDir, "--name", opts.Name}
	if runtimeLease != nil {
		args = append(args, "--lease-fd", "3")
	}
	cmd := exec.Command(executable, args...)
	if runtimeLease != nil {
		cmd.ExtraFiles = []*os.File{runtimeLease}
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return pid, nil
}

// startEgressMediator allocates a free port on bindHost, spawns a detached
// `microagent-firecracker-supervisor --egress-mediator` in the CURRENT netns
// (host for nat, pasta for user mode — in user mode the spawning supervisor is
// the in-netns re-exec, so the child inherits the pasta netns), waits until it
// accepts, and returns (pid, port). Uses the same detached-spawn mechanism as
// the port-forwarder companion.
//
// bindHost must be the tap gateway IP (e.g. "10.43.29.1") because the nftables
// REDIRECT target rewrites the destination to the primary address of the
// incoming interface — i.e. the tap host-side IP — not 127.0.0.1.
//
// caCertPath and caKeyPath, when non-empty, enable TLS interception: the
// mediator loads the per-workspace CA and signs per-SNI leaf certs on the fly.
// passthrough lists hosts whose TLS is forwarded opaquely (not intercepted).
// egressMediatorArgs builds the argv for the detached
// `microagent-firecracker-supervisor --egress-mediator` child. Pure (no I/O) so
// it can be unit-tested. The mode ("guarded"/"strict") is threaded to the
// mediator via --mode; an empty mode is normalized to the secure default.
// egressCaps carries the bounded-operations caps (ASK tenet 8) from the workspace
// Config into egressMediatorArgs. All fields default to zero = unlimited (current
// behavior), so an unset config produces argv byte-identical to the pre-caps one.
