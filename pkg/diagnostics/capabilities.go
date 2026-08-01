package diagnostics

import (
	"os"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

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
	vmkit.FeatureCapabilityNetworkPublish: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(
			l1Req("user networking (pasta)", h.UserNetworkingAvailable),
			l1Req("user namespaces", h.UserNamespacesAvailable),
		)
	},
	vmkit.FeatureCapabilityOfflineFileCopy: noBackendRuntimePrerequisites,
	vmkit.FeatureCapabilityPauseResume:     snapshotLinuxKVMCheck,
	vmkit.FeatureCapabilitySnapshotCreate:  snapshotLinuxKVMCheck,
	vmkit.FeatureCapabilitySnapshotRestore: snapshotLinuxKVMCheck,
	vmkit.FeatureCapabilitySnapshotFork:    snapshotLinuxKVMCheck,
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
	vmkit.FeatureCapabilityEgressMediation: egressMediationLinuxKVMCheck,
	vmkit.FeatureCapabilityResize:          noBackendRuntimePrerequisites,
}

// egressMediationLinuxKVMCheck verifies the host side of mediated egress:
// the user-mode netns pieces the mediator rides in, plus kernel TPROXY
// support for UDP steering. deriveTProxyModuleReadiness has already resolved
// the latter into EgressTProxyReady by the time capability diagnostics run —
// attempt-based when the probe can run (a real steering rule installed in a
// scratch namespace), module-presence heuristic otherwise. Missing names the
// exact modules when that is the diagnosis, so the remediation is one
// modprobe away; a kernel that refused the probe rule is named as such.
func egressMediationLinuxKVMCheck(h *vmkit.HostSupport) (bool, []string) {
	ready, missing := l1All(
		l1Req("supervisor", h.SupervisorAvailable),
		l1Req("user networking (pasta)", h.UserNetworkingAvailable),
		l1Req("user namespaces", h.UserNamespacesAvailable),
	)
	if !h.EgressTProxyReady {
		ready = false
		switch {
		case len(h.EgressTProxyMissingModules) > 0:
			for _, mod := range h.EgressTProxyMissingModules {
				missing = append(missing, "kernel module "+mod)
			}
		case h.EgressTProxyProbeError != "":
			missing = append(missing, "TPROXY rule installation ("+h.EgressTProxyProbeError+")")
		default:
			missing = append(missing, "TPROXY kernel support")
		}
	}
	return ready, missing
}

func snapshotLinuxKVMCheck(h *vmkit.HostSupport) (bool, []string) {
	return l1All(
		l1Req("supervisor", h.SupervisorAvailable),
		l1Req("firecracker binary", h.FrameworkAvailable),
	)
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
// support on macOS 13. Pause/resume keys on the separate pauseResumeAvailable
// fact (VZVirtualMachine pause, macOS 13+): a host that cannot save/restore
// can still pause.
var appleVFCapabilityChecks = map[vmkit.FeatureCapability]capabilityL1Check{
	vmkit.FeatureCapabilityStructuredExec: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
	vmkit.FeatureCapabilityLiveNetworkApply: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
	vmkit.FeatureCapabilityNetworkPublish: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
	vmkit.FeatureCapabilityOfflineFileCopy: offlineFileCopyAppleVFCheck,
	vmkit.FeatureCapabilityPauseResume:     pauseResumeAppleVFCheck,
	vmkit.FeatureCapabilitySnapshotCreate:  snapshotAppleVFCheck,
	vmkit.FeatureCapabilitySnapshotRestore: snapshotAppleVFCheck,
	vmkit.FeatureCapabilitySnapshotFork:    snapshotAppleVFCheck,
	vmkit.FeatureCapabilityConsole: func(h *vmkit.HostSupport) (bool, []string) {
		return l1All(l1Req("supervisor", h.SupervisorAvailable))
	},
	vmkit.FeatureCapabilityEgressMediation: egressMediationAppleVFCheck,
	// Broker endpoint companions exec the same microagent binary as the
	// egress datapath (`--broker-serve`), so the L1 prerequisites are
	// identical: supervisor present and the datapath binary resolvable.
	vmkit.FeatureCapabilityBrokerEndpoints: egressMediationAppleVFCheck,
	vmkit.FeatureCapabilityResize:          resizeAppleVFCheck,
}

func noBackendRuntimePrerequisites(*vmkit.HostSupport) (bool, []string) {
	return true, nil
}

// e2fsprogsTools are the host binaries the offline copy/commit/artifact paths
// shell out to: e2fsck before mounting-free edits, debugfs for reads/writes,
// mke2fs for image builds. Homebrew installs them keg-only on macOS, so a
// missing tool is a real and common host gap.
var e2fsprogsTools = []string{"e2fsck", "debugfs", "mke2fs"}

// lookupE2fsprogsTool is the runtime resolver (PATH, then the keg-only
// Homebrew locations); a seam so tests can simulate hosts with and without
// e2fsprogs installed.
var lookupE2fsprogsTool = workspace.LookupE2fsprogsTool

// offlineFileCopyAppleVFCheck verifies the host tools offline copy actually
// execs, resolved the same way the copy/commit/build paths resolve them. The
// capability needs no running supervisor — it operates on disk images — but it
// fails on the first missing e2fsprogs binary, so name each one with the brew
// remediation.
func offlineFileCopyAppleVFCheck(*vmkit.HostSupport) (bool, []string) {
	return checkE2fsprogsTools(e2fsprogsTools)
}

// resizeTools are the host binaries workspace and volume resize shell out
// to: e2fsck (resize2fs's own precondition for a shrink) and resize2fs
// itself.
var resizeTools = []string{"e2fsck", "resize2fs"}

// resizeAppleVFCheck mirrors offlineFileCopyAppleVFCheck for the resize
// capability's own, narrower tool set — resize needs no running supervisor,
// same as offline copy, but does not need debugfs or mke2fs.
func resizeAppleVFCheck(*vmkit.HostSupport) (bool, []string) {
	return checkE2fsprogsTools(resizeTools)
}

func checkE2fsprogsTools(tools []string) (bool, []string) {
	var missing []string
	for _, tool := range tools {
		if _, found := lookupE2fsprogsTool(tool); !found {
			missing = append(missing, tool+" (brew install e2fsprogs)")
		}
	}
	return len(missing) == 0, missing
}

// pauseResumeAppleVFCheck gates on the supervisor's precise pause fact
// (VZVirtualMachine pause support, macOS 13+), not on save/restore: pausing a
// running VM does not serialize it to disk, so macOS 13 hosts that cannot
// snapshot can still pause and resume.
func pauseResumeAppleVFCheck(h *vmkit.HostSupport) (bool, []string) {
	return l1All(
		l1Req("supervisor", h.SupervisorAvailable),
		l1Req("pause/resume support (macOS 13+)", h.PauseResumeAvailable),
	)
}

func snapshotAppleVFCheck(h *vmkit.HostSupport) (bool, []string) {
	return l1All(
		l1Req("supervisor", h.SupervisorAvailable),
		l1Req("save/restore support (macOS 14+)", h.SnapshotAvailable),
	)
}

// egressMediationAppleVFCheck verifies the host side of mediated egress on
// apple-vf: the supervisor carries the host-fd path (no kernel-module
// prerequisite), but it execs a microagent binary in --egress-datapath mode,
// resolved the way the boot path resolves it (MICROAGENT_EGRESS_DATAPATH_BIN,
// else this executable). A datapath binary that does not resolve to an
// executable file means every mediated-egress boot fails in the supervisor, so
// name the env var and what it must point at.
func egressMediationAppleVFCheck(h *vmkit.HostSupport) (bool, []string) {
	ready, missing := l1All(l1Req("supervisor", h.SupervisorAvailable))
	if problem := egressDatapathBinProblem(); problem != "" {
		ready = false
		missing = append(missing, problem)
	}
	return ready, missing
}

// egressDatapathBinProblem reports why the egress datapath binary would fail
// to launch, or "" when it resolves to an executable file. The remediation
// names MICROAGENT_EGRESS_DATAPATH_BIN because that is the knob: it must point
// at a microagent binary supporting --egress-datapath.
func egressDatapathBinProblem() string {
	const remedy = "; set " + vmkit.EgressDatapathBinEnv + " to a microagent binary supporting --egress-datapath"
	bin := vmkit.ResolveEgressDatapathBin()
	if bin == "" {
		return "egress datapath binary (unresolvable" + remedy + ")"
	}
	info, err := os.Stat(bin)
	switch {
	case err != nil:
		return "egress datapath binary (" + bin + " does not exist" + remedy + ")"
	case info.IsDir() || info.Mode().Perm()&0o111 == 0:
		return "egress datapath binary (" + bin + " is not executable" + remedy + ")"
	}
	return ""
}

// capabilityChecksForBackend returns the L1 registry for a backend, or nil when
// no per-instance checks are wired for it.
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
		// No L1 registry is wired for this backend. Populate nothing rather than
		// fabricate not-ready rows.
		return
	}
	declared := vmkit.DeclaredCapabilities(host.Backend)
	if len(declared) == 0 {
		return
	}
	out := make([]vmkit.CapabilityDiagnostic, 0, len(declared))
	for _, capability := range declared {
		d := vmkit.CapabilityDiagnostic{
			Capability: capability,
			Tier:       vmkit.CapabilityTierOf(capability),
			Declared:   true,
		}
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
