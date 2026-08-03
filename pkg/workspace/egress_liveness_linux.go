//go:build linux

package workspace

import (
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
)

// observedEgressMediatorLive reports whether this workspace's egress mediator is
// still running, and whether that could be observed at all.
//
// The verdict comes from the mediator's lease (EgressMediatorLeasePath), never
// from resolving its recorded PID against /proc. That PID is namespace local to
// the supervisor that spawned the mediator, so a PID lookup here answers about
// whichever process happens to hold that number in this namespace: an absent one
// reads as a dead mediator, and an unrelated live one reads as an ownership
// violation — or, if its command line happens to name the workspace, as proof
// that enforcement is healthy. The lease has no such ambiguity. It is per
// workspace by path and held by the mediator itself, so held means this
// workspace's mediator is alive and unheld means it is gone; no other process
// can lend it either answer.
//
// A runtime that recorded no lease reports unobserved rather than dead: a
// missing token is missing evidence, not evidence of missing enforcement. The
// recorded PID stays in the detail as the operator's handle on which mediator
// this was, which is all it was ever good for from here.
func observedEgressMediatorLive(opts Options, state RuntimeState) (bool, bool, string) {
	if state.EgressMediatorPID <= 0 {
		return false, false, "egress mediator liveness not observed: runtime has no recorded mediator process"
	}
	pid := state.EgressMediatorPID
	path := EgressMediatorLeasePath(opts.StateDir, opts.Name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, false, fmt.Sprintf("egress mediator liveness not observed: runtime recorded no mediation lease for workspace process %d", pid)
		}
		return false, false, fmt.Sprintf("egress mediator liveness unavailable: %v", err)
	}
	release, acquired, err := fsutil.TryLock(path)
	if err != nil {
		return false, false, fmt.Sprintf("egress mediator liveness unavailable: %v", err)
	}
	if !acquired {
		return true, true, fmt.Sprintf("egress mediator is running: workspace process %d holds the egress mediation lease", pid)
	}
	if err := release(); err != nil {
		return false, false, fmt.Sprintf("egress mediator liveness unavailable: %v", err)
	}
	return false, true, fmt.Sprintf("egress mediator is not running: the egress mediation lease recorded for workspace process %d is unheld", pid)
}
