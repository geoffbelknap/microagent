package diagnostics

import (
	"errors"

	firecracker "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
)

// defaultTProxyProbe is the live attempt-based probe CheckFirecracker uses
// when the caller does not inject one: the supervisor's --tproxy-selfcheck
// under `unshare --map-root-user --net`, installing the real steering rule in
// a scratch namespace. ran=false means the probe could not run (missing
// unshare or supervisor binary) and the module heuristic decides instead;
// ran=true carries the kernel's own verdict.
var defaultTProxyProbe = func(supervisorPath string) (ran bool, err error) {
	err = firecracker.ProbeEgressTProxySupport(supervisorPath)
	if errors.Is(err, firecracker.ErrTProxyProbeUnavailable) {
		return false, err
	}
	return true, err
}
