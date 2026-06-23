package diagnostics

import (
	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// tproxyModuleProbe gathers the facts the TPROXY readiness decision needs,
// without mutating the host. The seams (readFile, statDir) make the
// loaded/built-in decision unit-testable without touching /proc or /sys.
type tproxyModuleProbe struct {
	// readFile reads a pseudo-file such as /proc/modules; a non-nil error means
	// the file is unavailable (treated as "no loadable modules observed").
	readFile func(string) ([]byte, error)
	// statDir reports whether a path (e.g. /sys/module/<name>) exists; it is how
	// a module compiled into the kernel (built-in, not loadable) is detected.
	statDir func(string) bool
}

// deriveTProxyModuleReadiness fills the EgressTProxy* fields on host from the
// probe. A module counts as present when it is loaded (listed in /proc/modules)
// or built-in (its /sys/module/<name> node exists). This is the shared core so
// the Linux probe and the unit tests agree on the decision.
func deriveTProxyModuleReadiness(host *vmkit.HostSupport, probe tproxyModuleProbe) {
	if host == nil {
		return
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
	host.EgressTProxyReady = len(missing) == 0
}

// EgressTProxyRemediation returns the one-line operator hint when the TPROXY
// kernel modules are not all loaded/built-in, or "" when they are. It is a WARN
// (not a hard failure): TCP egress mediation works without TPROXY; only UDP
// mediation needs it.
func EgressTProxyRemediation(host *vmkit.HostSupport) string {
	if host == nil || host.EgressTProxyReady {
		return ""
	}
	return "UDP egress mediation needs TPROXY kernel modules; load them (e.g. `modprobe nft_tproxy`) or build them into the kernel"
}
