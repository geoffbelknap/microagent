//go:build linux

package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func observedEgressMediatorLive(opts Options, state RuntimeState) (bool, bool, string) {
	if state.EgressMediatorPID <= 0 {
		return false, false, "egress mediator liveness not observed: runtime has no recorded mediator process"
	}
	pid := state.EgressMediatorPID
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, true, fmt.Sprintf("egress mediator process %d is not running", pid)
		}
		return false, false, fmt.Sprintf("egress mediator process %d liveness unavailable: %v", pid, err)
	}
	workspacePath := filepath.Join(opts.StateDir, opts.Name)
	if !bytes.Contains(cmdline, []byte(workspacePath)) {
		return false, true, fmt.Sprintf("egress mediator process %d no longer belongs to workspace %s", pid, opts.Name)
	}
	return true, true, fmt.Sprintf("egress mediator process %d is running", pid)
}
