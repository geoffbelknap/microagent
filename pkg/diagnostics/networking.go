package diagnostics

import "github.com/geoffbelknap/microagent/pkg/vmkit"

// DeriveNetworkReadiness fills the per-mode readiness fields on host from the
// gathered facts. isolated always works; user needs passt plus working
// unprivileged user namespaces; nat/bridged/named need IPv4 forwarding and the
// supervisor holding CAP_NET_ADMIN.
func DeriveNetworkReadiness(host *vmkit.HostSupport) {
	if host == nil {
		return
	}
	host.IsolatedNetworkReady = true
	host.UserNetworkReady = host.UserNetworkingAvailable && host.UserNamespacesAvailable
	host.PrivilegedNetworkReady = host.IPForwardEnabled && host.SupervisorNetAdminCapable
}

// NetworkRemediation returns a one-line hint for enabling privileged networking,
// or "" when nat/bridged/named are already usable.
func NetworkRemediation(host *vmkit.HostSupport) string {
	if host == nil || host.PrivilegedNetworkReady {
		return ""
	}
	if host.IPForwardEnabled && !host.SupervisorNetAdminCapable {
		return "nat/bridged/named networking unavailable: the supervisor lacks CAP_NET_ADMIN (a 'brew upgrade' resets it). Re-run: sudo microagent host setup-networking"
	}
	return "nat/bridged/named networking unavailable. Run: sudo microagent host setup-networking"
}
