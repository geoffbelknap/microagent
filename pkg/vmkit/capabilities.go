package vmkit

// Capabilities describes the backend-specific behavior the workspace layer
// needs to know about, so backend differences live in one declarative table
// instead of scattered conditionals. The zero value grants nothing: an
// unknown backend fails closed.
type Capabilities struct {
	// StructuredExec reports whether the guest structured exec service is
	// reachable from the host, enabling exec health probes.
	StructuredExec bool
	// LiveNetworkApply reports whether host-bind port-forward changes can
	// be applied to a running workspace without a restart.
	LiveNetworkApply bool
	// OwnsRuntimeState reports whether the backend supervisor maintains
	// runtime state itself, so the workspace layer must not overwrite it
	// around detached starts.
	OwnsRuntimeState bool
	// DetachedStartCommand is the supervisor command used for detached
	// starts: "start" when the supervisor backgrounds itself, "run" when
	// the host keeps a foreground supervisor process.
	DetachedStartCommand string
	// DetachedHostSupervisor reports whether a detached start spawns a
	// long-lived supervisor process on the host (Apple VF).
	DetachedHostSupervisor bool
	// ShellNetwork is the transport for the guest shell console.
	ShellNetwork string
	// ShellReadinessProbe reports whether shell readiness can be probed
	// from the host over ShellNetwork.
	ShellReadinessProbe bool
	// Snapshot reports whether the backend can checkpoint a workspace's guest
	// memory + device state to disk (snapshot create/restore/fork).
	Snapshot bool
	// PauseResume reports whether the backend can freeze and thaw a running
	// workspace in place.
	PauseResume bool
	// SnapshotCreate reports whether the backend can create a snapshot artifact.
	SnapshotCreate bool
	// SnapshotRestore reports whether the backend can resume a workspace from
	// a snapshot artifact.
	SnapshotRestore bool
	// SnapshotFork reports whether the backend can create a distinct workspace
	// from a snapshot artifact.
	SnapshotFork bool
	// BrokerEndpoints reports whether the backend supervisor serves the
	// broker://serve vsock listener target that credential-injecting broker
	// endpoints ride on. Only the Firecracker supervisor implements it; the
	// Apple VF rejects the target, so broker
	// workspaces must fail closed with the declared gap before they start.
	BrokerEndpoints bool
}

// CapabilityDiagnostic is the L1 (prerequisites-verified) status of one declared
// backend capability on the current host: whether the host-side preconditions
// the capability needs are present. It is not operational proof — L1 does not
// exercise the capability (booting a VM, taking a real snapshot); that is L2,
// which belongs behind an explicit smoke test. `doctor` reports these so every
// declared capability is paired with an instance-level check instead of an
// unverified static claim.
type CapabilityDiagnostic struct {
	Capability FeatureCapability `json:"capability"`
	// Declared is true when the backend's capability table advertises it.
	Declared bool `json:"declared"`
	// Ready is true when every L1 prerequisite is present on this host.
	Ready bool `json:"ready"`
	// Missing lists the absent prerequisites when Ready is false.
	Missing []string `json:"missing,omitempty"`
}

// BackendCapabilities returns the capability table entry for a backend.
// Unknown backends return the zero value, which grants nothing.
func BackendCapabilities(backend string) Capabilities {
	switch backend {
	case BackendLinuxKVM:
		return Capabilities{
			StructuredExec:       true,
			LiveNetworkApply:     true,
			OwnsRuntimeState:     true,
			DetachedStartCommand: "start",
			ShellNetwork:         "tcp",
			ShellReadinessProbe:  true,
			Snapshot:             true,
			PauseResume:          true,
			SnapshotCreate:       true,
			SnapshotRestore:      true,
			SnapshotFork:         true,
			BrokerEndpoints:      true,
		}
	case BackendAppleVF:
		return Capabilities{
			StructuredExec:         true,
			LiveNetworkApply:       true,
			DetachedStartCommand:   "run",
			DetachedHostSupervisor: true,
			ShellNetwork:           "tcp",
			ShellReadinessProbe:    true,
			Snapshot:               true,
			PauseResume:            true,
			SnapshotCreate:         true,
			SnapshotRestore:        true,
			SnapshotFork:           true,
		}
	default:
		return Capabilities{}
	}
}
