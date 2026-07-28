package diagnostics

import "github.com/geoffbelknap/microagent/pkg/vmkit"

// DeriveVerdict resolves the diagnostic rollup for a host-check response.
// The verdict speaks for the full advertised contract, which is a stronger
// claim than resp.OK makes:
//
//	ok       — everything the backend declares works on this host
//	degraded — workspaces boot and run, but a probe reported an issue or a
//	           declared capability is not ready; whatever needs the missing
//	           piece fails closed rather than running without it
//	failed   — the core boot path is broken, or a core-tier capability is
//	           unavailable; no run can work
//
// A missing safety-tier capability yields degraded even though enforcement
// fails closed: "no workspace can run unsafely" and "every advertised
// workspace can run" are different claims, and ok only makes the second.
func DeriveVerdict(resp *vmkit.Response) string {
	h := resp.Host
	if h == nil {
		return vmkit.VerdictFailed
	}
	core := h.SupervisorAvailable && h.VirtualizationSupported && h.GuestInitAvailable
	if h.Backend == vmkit.BackendLinuxKVM {
		core = core && h.KVMAvailable && h.BinaryPath != ""
	}
	if resp.Kernel != nil && resp.Kernel.Status != "present" {
		core = false
	}
	degraded := resp.Error != "" || !resp.OK
	for _, c := range h.Capabilities {
		if c.Ready {
			continue
		}
		if vmkit.CapabilityTierOf(c.Capability) == vmkit.CapabilityTierCore {
			core = false
		}
		degraded = true
	}
	if !core {
		return vmkit.VerdictFailed
	}
	if degraded {
		return vmkit.VerdictDegraded
	}
	return vmkit.VerdictOK
}
