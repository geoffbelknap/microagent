package vmkit

// DurabilityTier names the lifetime boundary for a class of workspace state.
type DurabilityTier string

const (
	DurabilityRuntime     DurabilityTier = "runtime"
	DurabilityWorkspace   DurabilityTier = "workspace"
	DurabilitySnapshot    DurabilityTier = "snapshot"
	DurabilityIndependent DurabilityTier = "independent"
)

// DurabilityEffect is the guarantee an operation makes for one state class.
type DurabilityEffect string

const (
	DurabilityPreserved     DurabilityEffect = "preserved"
	DurabilityDiscarded     DurabilityEffect = "discarded"
	DurabilityCaptured      DurabilityEffect = "captured"
	DurabilityRestored      DurabilityEffect = "restored"
	DurabilityCopied        DurabilityEffect = "copied"
	DurabilityReset         DurabilityEffect = "reset"
	DurabilityRemoved       DurabilityEffect = "removed"
	DurabilityNotGuaranteed DurabilityEffect = "not_guaranteed"
)

type ContractDurability struct {
	Tiers       []ContractDurabilityTier       `json:"tiers"`
	Transitions []ContractDurabilityTransition `json:"transitions"`
}

type ContractDurabilityTier struct {
	Name        DurabilityTier `json:"name"`
	Description string         `json:"description"`
}

// ContractDurabilityTransition is deliberately field-based rather than an
// open map: adding a new state class requires every lifecycle guarantee and its
// tests to make an explicit choice.
type ContractDurabilityTransition struct {
	Operation            string           `json:"operation"`
	Memory               DurabilityEffect `json:"memory"`
	Processes            DurabilityEffect `json:"processes"`
	NetworkConnections   DurabilityEffect `json:"networkConnections"`
	Rootfs               DurabilityEffect `json:"rootfs"`
	Identity             DurabilityEffect `json:"identity"`
	EventHistory         DurabilityEffect `json:"eventHistory"`
	Results              DurabilityEffect `json:"results"`
	ArtifactDeclarations DurabilityEffect `json:"artifactDeclarations"`
	Snapshots            DurabilityEffect `json:"snapshots"`
	NamedVolumes         DurabilityEffect `json:"namedVolumes"`
	Notes                []string         `json:"notes,omitempty"`
}

func DurabilityContract() ContractDurability {
	preserveWorkspace := ContractDurabilityTransition{
		Rootfs:               DurabilityPreserved,
		Identity:             DurabilityPreserved,
		EventHistory:         DurabilityPreserved,
		Results:              DurabilityPreserved,
		ArtifactDeclarations: DurabilityPreserved,
		Snapshots:            DurabilityPreserved,
		NamedVolumes:         DurabilityPreserved,
	}
	halt := withRuntime(preserveWorkspace, "halt", DurabilityDiscarded, DurabilityDiscarded, DurabilityDiscarded)
	halt.Notes = []string{"a bounded guest filesystem sync is attempted before shutdown; if the guest is unavailable or uncooperative, halt proceeds and preserves only data already flushed"}
	stop := withRuntime(preserveWorkspace, "stop", DurabilityDiscarded, DurabilityDiscarded, DurabilityDiscarded)
	stop.Notes = append([]string(nil), halt.Notes...)
	transitions := []ContractDurabilityTransition{
		withRuntime(preserveWorkspace, "pause", DurabilityPreserved, DurabilityPreserved, DurabilityNotGuaranteed),
		withRuntime(preserveWorkspace, "resume", DurabilityPreserved, DurabilityPreserved, DurabilityNotGuaranteed),
		halt,
		stop,
		withRuntime(preserveWorkspace, "kill", DurabilityDiscarded, DurabilityDiscarded, DurabilityDiscarded),
		withRuntime(preserveWorkspace, "quarantine", DurabilityDiscarded, DurabilityDiscarded, DurabilityDiscarded),
		{
			Operation:            "snapshot.create",
			Memory:               DurabilityCaptured,
			Processes:            DurabilityCaptured,
			NetworkConnections:   DurabilityNotGuaranteed,
			Rootfs:               DurabilityCaptured,
			Identity:             DurabilityPreserved,
			EventHistory:         DurabilityPreserved,
			Results:              DurabilityPreserved,
			ArtifactDeclarations: DurabilityPreserved,
			Snapshots:            DurabilityCaptured,
			NamedVolumes:         DurabilityPreserved,
			Notes:                []string{"the source workspace resumes after capture; named volumes and live network connections are not part of the snapshot"},
		},
		{
			Operation:            "snapshot.restore",
			Memory:               DurabilityRestored,
			Processes:            DurabilityRestored,
			NetworkConnections:   DurabilityReset,
			Rootfs:               DurabilityRestored,
			Identity:             DurabilityPreserved,
			EventHistory:         DurabilityPreserved,
			Results:              DurabilityPreserved,
			ArtifactDeclarations: DurabilityPreserved,
			Snapshots:            DurabilityPreserved,
			NamedVolumes:         DurabilityPreserved,
			Notes:                []string{"restore is in place; host-side history is retained and live guest connections must reconnect"},
		},
		{
			Operation:            "snapshot.fork",
			Memory:               DurabilityCopied,
			Processes:            DurabilityCopied,
			NetworkConnections:   DurabilityReset,
			Rootfs:               DurabilityCopied,
			Identity:             DurabilityReset,
			EventHistory:         DurabilityReset,
			Results:              DurabilityReset,
			ArtifactDeclarations: DurabilityReset,
			Snapshots:            DurabilityCopied,
			NamedVolumes:         DurabilityNotGuaranteed,
			Notes:                []string{"the fork gets a fresh workspace identity and isolated host network path; named volumes are not snapshot contents"},
		},
		{
			Operation:            "delete",
			Memory:               DurabilityRemoved,
			Processes:            DurabilityRemoved,
			NetworkConnections:   DurabilityRemoved,
			Rootfs:               DurabilityRemoved,
			Identity:             DurabilityRemoved,
			EventHistory:         DurabilityRemoved,
			Results:              DurabilityRemoved,
			ArtifactDeclarations: DurabilityRemoved,
			Snapshots:            DurabilityRemoved,
			NamedVolumes:         DurabilityPreserved,
			Notes:                []string{"named volumes have an independent lifecycle and are detached, not deleted"},
		},
	}
	return ContractDurability{
		Tiers: []ContractDurabilityTier{
			{Name: DurabilityRuntime, Description: "memory, processes, and live connections exist only with the runtime"},
			{Name: DurabilityWorkspace, Description: "rootfs, identity, events, results, and artifact declarations persist until workspace deletion"},
			{Name: DurabilitySnapshot, Description: "captured memory, device state, and rootfs persist with the owning workspace until snapshot or workspace deletion"},
			{Name: DurabilityIndependent, Description: "named volumes have their own lifecycle and survive workspace deletion"},
		},
		Transitions: transitions,
	}
}

func withRuntime(base ContractDurabilityTransition, operation string, memory, processes, network DurabilityEffect) ContractDurabilityTransition {
	base.Operation = operation
	base.Memory = memory
	base.Processes = processes
	base.NetworkConnections = network
	return base
}
