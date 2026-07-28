package diagnostics

import (
	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// tproxyModuleProbe gathers the facts the TPROXY readiness decision needs,
// without mutating the host. The seams (probeSupport, readFile, statDir) make
// the decision unit-testable without touching namespaces, /proc, or /sys.
type tproxyModuleProbe struct {
	// probeSupport is the attempt-based check: install the real steering rule
	// in a scratch user+net namespace via the supervisor's --tproxy-selfcheck.
	// ran=false means the probe could not run and the module heuristic decides
	// instead; ran=true carries the kernel's own verdict. nil skips straight
	// to the heuristic.
	probeSupport func(supervisorPath string) (ran bool, err error)
	// readFile reads a pseudo-file such as /proc/modules; a non-nil error means
	// the file is unavailable (treated as "no loadable modules observed").
	readFile func(string) ([]byte, error)
	// statDir reports whether a path (e.g. /sys/module/<name>) exists; it is how
	// a module compiled into the kernel (built-in, not loadable) is detected.
	statDir func(string) bool
}

// deriveTProxyModuleReadiness fills the EgressTProxy* fields on host.
//
// The attempt-based probe decides when it can run: installing the actual
// steering rule is the operation a mediated boot performs, so its verdict
// covers module autoload and built-ins that the presence heuristic misreads
// in both directions. The module heuristic remains for two jobs — deciding
// when the probe cannot run, and naming the missing modules as remediation
// detail when the kernel refuses.
func deriveTProxyModuleReadiness(host *vmkit.HostSupport, probe tproxyModuleProbe) {
	if host == nil {
		return
	}
	probed := false
	if probe.probeSupport != nil && host.SupervisorAvailable {
		if ran, err := probe.probeSupport(host.SupervisorPath); ran {
			if err == nil {
				host.EgressTProxyReady = true
				return
			}
			probed = true
			host.EgressTProxyProbeError = err.Error()
		}
	}
	var loaded map[string]bool
	if probe.readFile != nil {
		if data, err := probe.readFile("/proc/modules"); err == nil {
			loaded = egressprereq.ParseLoadedModules(data)
		}
	}
	isBuiltin := func(name string) bool {
		if probe.statDir == nil {
			return false
		}
		return probe.statDir("/sys/module/" + name)
	}
	missing := egressprereq.MissingModules(loaded, isBuiltin)
	host.EgressTProxyMissingModules = missing
	// A kernel that refused the probe rule is not ready no matter what the
	// module list says; without a probe verdict the heuristic decides.
	host.EgressTProxyReady = !probed && len(missing) == 0
}

// EgressTProxyRemediation returns the one-line operator hint when TPROXY
// steering is not ready, or "" when it is. It is a WARN (not a hard failure):
// TCP egress mediation works without TPROXY; only UDP mediation needs it, and
// a mediated boot fails closed rather than running without it.
func EgressTProxyRemediation(host *vmkit.HostSupport) string {
	if host == nil || host.EgressTProxyReady {
		return ""
	}
	if len(host.EgressTProxyMissingModules) > 0 {
		return "UDP egress mediation needs TPROXY kernel modules; load them (e.g. `modprobe nft_tproxy`) or build them into the kernel"
	}
	if host.EgressTProxyProbeError != "" {
		return "the kernel refused a live TPROXY probe rule; mediated boots will fail closed the same way (probe: " + host.EgressTProxyProbeError + ")"
	}
	return "UDP egress mediation needs kernel TPROXY support; mediated boots fail closed without it"
}
