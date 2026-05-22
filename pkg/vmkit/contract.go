package vmkit

type RuntimeContract struct {
	Version          string            `json:"version"`
	Backends         []string          `json:"backends"`
	Commands         []ContractItem    `json:"commands"`
	States           []ContractState   `json:"states"`
	ReadinessSignals []ContractItem    `json:"readinessSignals"`
	ResultFields     []ContractItem    `json:"resultFields"`
	ArtifactChannels []ContractItem    `json:"artifactChannels"`
	Mediation        ContractMediation `json:"mediation"`
	Verification     ContractItem      `json:"verification"`
	Parity           ContractParity    `json:"parity"`
}

type ContractItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ContractState struct {
	Name               VMState `json:"name"`
	Description        string  `json:"description"`
	DiskPreserved      bool    `json:"diskPreserved"`
	EventHistoryKept   bool    `json:"eventHistoryKept"`
	RuntimeMayContinue bool    `json:"runtimeMayContinue,omitempty"`
}

type ContractMediation struct {
	Primitive    string   `json:"primitive"`
	Fields       []string `json:"fields"`
	RequiredMode string   `json:"requiredMode"`
	BreakMode    string   `json:"breakMode"`
}

type ContractParity struct {
	Scope string   `json:"scope"`
	Rules []string `json:"rules"`
}

func NewRuntimeContract() RuntimeContract {
	return RuntimeContract{
		Version:  "agent-runtime.v1",
		Backends: []string{BackendAppleVF, BackendFirecracker, BackendWindowsHyperV},
		Commands: []ContractItem{
			{Name: "prepare", Description: "write backend state/config without booting"},
			{Name: "start", Description: "start a prepared, halted, stopped, or failed workspace with preserved disk state; quarantined workspaces must be halted, stopped, or killed first"},
			{Name: "run", Description: "start in foreground and report structured lifecycle state"},
			{Name: "inspect", Description: "read latest structured state"},
			{Name: "halt", Description: "clean disk-preserving shutdown; memory state is not preserved"},
			{Name: "quarantine", Description: "sever host-side network, mediation, and side-effect paths while preserving disk and events"},
			{Name: "stop", Description: "graceful stop"},
			{Name: "kill", Description: "hard stop"},
			{Name: "delete", Description: "remove workspace runtime state and persisted disks"},
		},
		States: []ContractState{
			{Name: StatePrepared, Description: "workspace state/config exists but runtime is not started", DiskPreserved: true, EventHistoryKept: true},
			{Name: StateStarting, Description: "backend accepted start and is bringing up the runtime", DiskPreserved: true, EventHistoryKept: true},
			{Name: StateRunning, Description: "runtime is started", DiskPreserved: true, EventHistoryKept: true, RuntimeMayContinue: true},
			{Name: StateHalted, Description: "clean disk-preserving shutdown completed", DiskPreserved: true, EventHistoryKept: true},
			{Name: StateQuarantined, Description: "host-side network, mediation, and side-effect paths are severed", DiskPreserved: true, EventHistoryKept: true, RuntimeMayContinue: true},
			{Name: StateStopped, Description: "runtime process has stopped", DiskPreserved: true, EventHistoryKept: true},
			{Name: StateFailed, Description: "backend command failed with structured error detail", DiskPreserved: true, EventHistoryKept: true},
		},
		ReadinessSignals: []ContractItem{
			{Name: "guestReady", Description: "workspace reached a started terminal or runtime state"},
			{Name: "shellReady", Description: "interactive shell input path is available"},
			{Name: "execReady", Description: "structured exec service is reachable and a no-op exec succeeds end-to-end"},
			{Name: "resultReady", Description: "structured guest result is available"},
			{Name: "mediationReady", Description: "declared mediation channel is ready for a running workspace"},
		},
		ResultFields: []ContractItem{
			{Name: "identity", Description: "request/runtime identity copied into the result"},
			{Name: "backend", Description: "backend that produced the result"},
			{Name: "resultPath", Description: "host path of the result payload"},
			{Name: "startedAt", Description: "runtime start timestamp"},
			{Name: "completedAt", Description: "result completion timestamp"},
			{Name: "exitCode", Description: "machine-readable process exit code"},
			{Name: "stdout", Description: "guest-reported stdout, separate from serial logs"},
			{Name: "stderr", Description: "guest-reported stderr, separate from serial logs"},
			{Name: "error", Description: "guest-reported failure reason"},
		},
		ArtifactChannels: []ContractItem{
			{Name: "ingress", Description: "declared input bundles mounted into the workspace"},
			{Name: "egress", Description: "declared output paths retrievable by name without entering the workspace"},
		},
		Mediation: ContractMediation{
			Primitive:    "guest-to-host vsock contract for Body calls into the enforcer/orchestrator",
			Fields:       []string{"enabled", "required", "port", "target", "failClosed"},
			RequiredMode: "required mediation must set failClosed=true",
			BreakMode:    "when required mediation is unavailable or broken, callers must treat the channel as closed",
		},
		Verification: ContractItem{Name: "verification", Description: "image digest, kernel hash, rootfs hash, init hash, and divergence entries"},
		Parity: ContractParity{
			Scope: "Firecracker, Apple VF, and Windows Hyper-V expose the same backend-neutral states, response fields, readiness signals, mediation shape, result channel, and artifact declarations.",
			Rules: []string{
				"backend-specific mechanics stay behind the supervisor boundary",
				"public output remains structured and machine-readable",
				"state changes are API events and append to events.json",
				"halt preserves disk state but not memory state",
				"quarantine preserves disk state and event history while severing host-side effects",
			},
		},
	}
}
