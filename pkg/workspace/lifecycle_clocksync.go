package workspace

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// snapshotResumeExecReadyWait bounds how long a snapshot resume waits for the
// guest exec service before giving up on the clock sync, on backends that
// have no faster verified path (apple-vf; see below). On apple-vf, Start
// returns before the detached VMM has finished loading the snapshot, so the
// exec service may take several seconds to answer even though the workspace
// already reports running.
const snapshotResumeExecReadyWait = 30 * time.Second

// snapshotResumeVsockWait bounds the linux-kvm vsock probe. Matches
// pkg/supervisors/firecracker's restoreLivenessWait: vsock is realized
// synchronously by PUT /snapshot/load, so a real answer arrives in well
// under a second in practice: this is a safety margin, not an expectation.
const snapshotResumeVsockWait = 5 * time.Second

// snapshotResumeVsockPoll matches restoreLivenessPoll.
const snapshotResumeVsockPoll = 100 * time.Millisecond

// clockSyncExecTimeout bounds the in-guest settime command itself.
const clockSyncExecTimeout = 5 * time.Second

// clockSyncNow is the host time source, injectable for tests.
var clockSyncNow = time.Now

// syncGuestClockAfterResume pushes the host wall clock into a guest that just
// resumed from a snapshot. A restored guest wakes with the clock it was
// captured with — days stale for a workspace parked between tasks — because
// nothing in the VMM resume path corrects the guest's real-time clock, so
// everything the guest date-stamps is wrong until something sets it.
//
// The sync is best-effort by contract: the outcome lands in the workspace
// event history either way, and a guest that cannot be reached in time keeps
// its stale clock rather than failing the start. Whole-second precision is
// the goal; the days-scale drift is the bug, not sub-second skew. The guest
// exec service runs as root, which is the privilege settimeofday needs, and
// `date -u -s @<epoch>` is understood by both coreutils and busybox.
//
// On linux-kvm this dials the guest directly over the Firecracker vsock UDS
// instead of polling the host TCP port forward: the forwarder is a detached
// companion process started only after Start returns, so a probe routed
// through it cannot succeed for most of a 30s window — the same race
// pkg/supervisors/firecracker's waitForRestoreLiveness (mw#592) fixed for
// the liveness gate. The vsock socket path and dial protocol are duplicated
// here rather than imported: pkg/supervisors/firecracker already imports
// pkg/workspace, so the reverse import is a cycle. If the wire protocol
// (dialGuestVsock there, dialClockSyncVsock here) ever changes, both copies
// need it. If the vsock probe never succeeds within its (much smaller)
// window, this falls back to the pre-existing TCP-forward poll so linux-kvm
// never regresses below today's behavior, just usually skips it entirely.
// apple-vf has no equivalent local vsock convention and keeps the TCP path.
func syncGuestClockAfterResume(ctx context.Context, opts Options) {
	runtimeState, err := ReadRuntimeState(opts)
	if err != nil || runtimeState.Config.ExecPort == 0 {
		return
	}
	epoch := clockSyncNow().UTC().Unix()
	req := execprotocol.NewExecRequest([]string{"date", "-u", "-s", "@" + strconv.FormatInt(epoch, 10)})
	req.TimeoutMS = int64(clockSyncExecTimeout / time.Millisecond)

	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(runtimeState.Config.ExecPort)))
	client := execclient.New(target)
	if opts.Backend == vmkit.BackendLinuxKVM {
		if vsockClient, vsockTarget, ok := clockSyncVsockClient(ctx, opts, runtimeState); ok {
			client, target = vsockClient, vsockTarget
		} else {
			waitForExecReady(ctx, runtimeState, snapshotResumeExecReadyWait)
		}
	} else {
		waitForExecReady(ctx, runtimeState, snapshotResumeExecReadyWait)
	}

	execCtx, cancel := context.WithTimeout(ctx, clockSyncExecTimeout)
	result, err := client.Exec(execCtx, req)
	cancel()
	detail := fmt.Sprintf("guest clock synced to host time after snapshot resume (epoch %d)", epoch)
	switch {
	case err != nil:
		detail = fmt.Sprintf("guest clock sync skipped after snapshot resume: exec service unreachable at %s: %v", target, err)
	case result.Error != nil:
		detail = fmt.Sprintf("guest clock sync failed after snapshot resume: %s: %s", result.Error.Code, result.Error.Message)
	case result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0:
		detail = fmt.Sprintf("guest clock sync failed after snapshot resume: settime command status=%s", result.Status)
	}
	event := EventFile{
		Identity:   runtimeState.Event.Identity,
		State:      runtimeState.Event.State,
		Detail:     detail,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	_ = appendEvent(filepath.Join(opts.StateDir, opts.Name, "events.json"), event)
}

// clockSyncVsockClient polls the guest over vsock for up to
// snapshotResumeVsockWait and, if it answers, returns an execclient
// configured to reach it that way. ok is false if no guest exec port is
// configured, the socket file does not exist at all, or the guest never
// answered within the window — the caller falls back to the TCP-forward poll
// in that case.
//
// The socket's existence is checked once, up front, rather than folded into
// the retry loop: the vsock device is realized synchronously before
// syncGuestClockAfterResume ever runs (see the package doc above), so if the
// file is not there yet, it is not coming — retrying a stat that cannot
// change the outcome would just spend the whole window failing fast, one
// syscall at a time, and then fall back anyway. Reserve the retry budget for
// the case that IS transient: the socket exists but the guest's exec service
// has not answered yet.
func clockSyncVsockClient(ctx context.Context, opts Options, runtimeState RuntimeState) (client *execclient.Client, target string, ok bool) {
	guestPort := runtimeState.Config.GuestExecPort
	if guestPort == 0 {
		guestPort = runtimeState.Config.ExecPort
	}
	if guestPort == 0 {
		return nil, "", false
	}
	udsPath := filepath.Join(opts.StateDir, opts.Name, "vsock.sock")
	if _, err := os.Stat(udsPath); err != nil {
		return nil, "", false
	}
	target = fmt.Sprintf("vsock:%s:%d", udsPath, guestPort)
	client = execclient.New(target).WithDialer(clockSyncVsockDialer{udsPath: udsPath, guestPort: uint32(guestPort)})

	deadline := time.Now().Add(snapshotResumeVsockWait)
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		result, err := client.Exec(probeCtx, execprotocol.NewExecRequest([]string{"true"}))
		cancel()
		if err == nil && result.Error == nil && result.Status == execprotocol.ExecStatusExited &&
			result.ExitCode != nil && *result.ExitCode == 0 {
			return client, target, true
		}
		if !time.Now().Before(deadline) {
			return nil, "", false
		}
		select {
		case <-time.After(snapshotResumeVsockPoll):
		case <-ctx.Done():
			return nil, "", false
		}
	}
}

// clockSyncVsockDialer adapts dialClockSyncVsock to execclient.Dialer so
// Client.Exec's dial goes over the Firecracker vsock UDS instead of TCP.
type clockSyncVsockDialer struct {
	udsPath   string
	guestPort uint32
}

func (d clockSyncVsockDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	return dialClockSyncVsock(d.udsPath, d.guestPort)
}

// dialClockSyncVsock speaks the Firecracker vsock-multiplexer handshake
// ("CONNECT <port>\n" / "OK ...\n") directly. This duplicates
// pkg/supervisors/firecracker's dialGuestVsock rather than importing it: that
// package already imports pkg/workspace, so the reverse import is a cycle.
func dialClockSyncVsock(udsPath string, guestPort uint32) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", udsPath, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", guestPort); err != nil {
		_ = conn.Close()
		return nil, err
	}
	ack, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.HasPrefix(ack, "OK ") {
		_ = conn.Close()
		return nil, fmt.Errorf("firecracker vsock connect failed: %s", strings.TrimSpace(ack))
	}
	return conn, nil
}
