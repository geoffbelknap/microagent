package diagnostics

import "github.com/geoffbelknap/microagent/pkg/vmkit"

// capabilityL1Check computes L1 (prerequisites-present) readiness for one
// declared capability from the host facts doctor already gathered. It never
// boots a VM: L1 verifies host-side preconditions, not that the capability
// actually operates (that is L2, behind an explicit smoke test).
type capabilityL1Check func(*vmkit.HostSupport) (ready bool, missing []string)

// linuxKVMCapabilityChecks maps each FeatureCapability the linux-kvm backend
// declares to its L1 diagnostic. TestCapabilityDiagnosticCoverage asserts every
// declared capability has an entry here, so a newly-declared capability cannot
// ship without an instance-level prerequisite check.
var linuxKVMCapabilityChecks = map[vmkit.FeatureCapability]capabilityL1Check{
	vmkit.FeatureCapabilityStructuredExec: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(
			l1Req("supervisor", h.SupervisorAvailable),
			l1Req("guest init", h.GuestInitAvailable),
			l1Req("vsock", h.VsockAvailable),
		)
	},
	vmkit.FeatureCapabilityLiveNetworkApply: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(
			l1Req("user networking (pasta)", h.UserNetworkingAvailable),
			l1Req("user namespaces", h.UserNamespacesAvailable),
		)
	},
	vmkit.FeatureCapabilitySnapshot: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(
			l1Req("supervisor", h.SupervisorAvailable),
			l1Req("firecracker binary", h.FrameworkAvailable),
		)
	},
	vmkit.FeatureCapabilityBrokerEndpoints: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(
			l1Req("supervisor", h.SupervisorAvailable),
			l1Req("vsock", h.VsockAvailable),
		)
	},
	vmkit.FeatureCapabilityConsole: func(h *vmkit.HostSupport) (bool, []string) {
		// The interactive console rides the supervisor, which wires the
		// serial/shell channel.
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
}

// appleVFCapabilityChecks maps each FeatureCapability the apple-vf backend
// declares to its L1 diagnostic.
//
// The apple-vf host facts come from the Virtualization.framework supervisor's
// `host` response (hostSupport() in the supervisor): the supervisor carries the
// exec vsock forward, live apply, and console channels itself, so those key on
// SupervisorAvailable like their linux-kvm counterparts key on the pieces that
// carry them. Snapshot additionally keys on the supervisor's precise
// snapshotAvailable fact (VZVirtualMachine save/restore, macOS 14+), not on
// FrameworkAvailable — the framework being present does not imply save/restore
// support on macOS 13.
var appleVFCapabilityChecks = map[vmkit.FeatureCapability]capabilityL1Check{
	vmkit.FeatureCapabilityStructuredExec: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
	vmkit.FeatureCapabilityLiveNetworkApply: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
	vmkit.FeatureCapabilitySnapshot: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(
			l1Req("supervisor", h.SupervisorAvailable),
			l1Req("save/restore support (macOS 14+)", h.SnapshotAvailable),
		)
	},
	vmkit.FeatureCapabilityConsole: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
}

// capabilityChecksForBackend returns the L1 registry for a backend, or nil when
// no per-instance checks are wired for it (e.g. experimental windows-hyperv,
// which reports no capability rows rather than fabricated ones).
func capabilityChecksForBackend(backend string) map[vmkit.FeatureCapability]capabilityL1Check {
	switch backend {
	case vmkit.BackendLinuxKVM:
		return linuxKVMCapabilityChecks
	case vmkit.BackendAppleVF:
		return appleVFCapabilityChecks
	default:
		return nil
	}
}

// deriveCapabilityDiagnostics fills host.Capabilities with the L1 status of
// every capability the backend declares. A declared capability with no
// registered L1 check is still reported (Ready=false, Missing names the gap) so
// the omission is visible rather than silent; the coverage test keeps that from
// shipping.
func deriveCapabilityDiagnostics(host *vmkit.HostSupport) {
	if host == nil {
		return
	}
	checks := capabilityChecksForBackend(host.Backend)
	if checks == nil {
		// No L1 registry wired for this backend (e.g. apple-vf, whose capability
		// diagnostics come from the supervisor host response). Populate nothing
		// rather than fabricate not-ready rows.
		return
	}
	declared := vmkit.DeclaredCapabilities(host.Backend)
	if len(declared) == 0 {
		return
	}
	out := make([]vmkit.CapabilityDiagnostic, 0, len(declared))
	for _, capability := range declared {
		d := vmkit.CapabilityDiagnostic{Capability: capability, Declared: true}
		if check, ok := checks[capability]; ok {
			d.Ready, d.Missing = check(host)
		} else {
			d.Missing = []string{"no L1 diagnostic registered"}
		}
		out = append(out, d)
	}
	host.Capabilities = out
}

// capabilityReady reports whether a capability's L1 diagnostic is present and
// ready on host. Legacy per-feature availability booleans derive from this so
// they report a verified prerequisite result instead of a hardcoded claim.
func capabilityReady(host *vmkit.HostSupport, capability vmkit.FeatureCapability) bool {
	if host == nil {
		return false
	}
	for _, c := range host.Capabilities {
		if c.Capability == capability {
			return c.Ready
		}
	}
	return false
}

type l1Item struct {
	name string
	ok   bool
}

func l1Req(name string, ok bool) l1Item { return l1Item{name: name, ok: ok} }

func l1All(items ...l1Item) (bool, []string) {
	var missing []string
	for _, it := range items {
		if !it.ok {
			missing = append(missing, it.name)
		}
	}
	return len(missing) == 0, missing
}
