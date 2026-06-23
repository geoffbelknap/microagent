package diagnostics

import "github.com/geoffbelknap/microagent/pkg/vmkit"

// deriveNetworkReadiness fills the per-mode readiness fields on host from the
// gathered facts. isolated always works; user needs passt plus working
// unprivileged user namespaces.
func deriveNetworkReadiness(host *vmkit.HostSupport) {
	if host == nil {
		return
	}
	host.IsolatedNetworkReady = true
	host.UserNetworkReady = host.UserNetworkingAvailable && host.UserNamespacesAvailable
}
