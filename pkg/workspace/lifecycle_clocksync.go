package workspace

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"time"

	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// snapshotResumeExecReadyWait bounds how long a snapshot resume waits for the
// guest exec service before giving up on the clock sync. Deliberately longer
// than ExecReadyWait: on apple-vf, Start returns before the detached VMM has
// finished loading the snapshot, so the exec service may take several seconds
// to answer even though the workspace already reports running.
const snapshotResumeExecReadyWait = 30 * time.Second

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
func syncGuestClockAfterResume(ctx context.Context, opts Options) {
	runtimeState, err := ReadRuntimeState(opts)
	if err != nil || runtimeState.Config.ExecPort == 0 {
		return
	}
	waitForExecReady(ctx, runtimeState, snapshotResumeExecReadyWait)
	epoch := clockSyncNow().UTC().Unix()
	req := execprotocol.NewExecRequest([]string{"date", "-u", "-s", "@" + strconv.FormatInt(epoch, 10)})
	req.TimeoutMS = int64(clockSyncExecTimeout / time.Millisecond)
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(runtimeState.Config.ExecPort)))
	execCtx, cancel := context.WithTimeout(ctx, clockSyncExecTimeout)
	result, err := execclient.New(target).Exec(execCtx, req)
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
