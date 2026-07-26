package vmkit

import "fmt"

type FeatureScope string

const (
	FeatureBackendNeutral FeatureScope = "backend-neutral"
	FeatureHostTooling    FeatureScope = "host-tooling"
	FeatureExperimental   FeatureScope = "experimental"
)

type FeatureCapability string
type OperationID string
type OperationEffect string
type OperationIdempotency string
type OperationConfirmation string
type OperationSideEffect string
type OperationTypeID string

const (
	OperationEffectRead        OperationEffect = "read"
	OperationEffectMutation    OperationEffect = "mutation"
	OperationEffectDestructive OperationEffect = "destructive"

	OperationIdempotencyReadOnly      OperationIdempotency = "read_only"
	OperationIdempotencyReplayable    OperationIdempotency = "replayable"
	OperationIdempotencyKeyedReplay   OperationIdempotency = "keyed_replay"
	OperationIdempotencyNotIdempotent OperationIdempotency = "not_idempotent"

	OperationConfirmationPreview OperationConfirmation = "preview"

	OperationSideEffectHostState      OperationSideEffect = "host_state"
	OperationSideEffectWorkspaceState OperationSideEffect = "workspace_state"
)

const (
	FeatureCapabilityStructuredExec   FeatureCapability = "StructuredExec"
	FeatureCapabilityNetworkPublish   FeatureCapability = "NetworkPublish"
	FeatureCapabilityLiveNetworkApply FeatureCapability = "LiveNetworkApply"
	FeatureCapabilityOfflineFileCopy  FeatureCapability = "OfflineFileCopy"
	FeatureCapabilityLiveFileCopy     FeatureCapability = "LiveFileCopy"
	// FeatureCapabilitySnapshot is the legacy aggregate snapshot capability.
	// Operation gates use the four snapshot facets below.
	FeatureCapabilitySnapshot        FeatureCapability = "Snapshot"
	FeatureCapabilityPauseResume     FeatureCapability = "PauseResume"
	FeatureCapabilitySnapshotCreate  FeatureCapability = "SnapshotCreate"
	FeatureCapabilitySnapshotRestore FeatureCapability = "SnapshotRestore"
	FeatureCapabilitySnapshotFork    FeatureCapability = "SnapshotFork"
	FeatureCapabilityBrokerEndpoints FeatureCapability = "BrokerEndpoints"
	FeatureCapabilityConsole         FeatureCapability = "Console"
)

const (
	OperationWorkspaceDispatch   OperationID = "workspace.dispatch"
	OperationWorkspaceExec       OperationID = "workspace.exec"
	OperationWorkspaceConsole    OperationID = "workspace.console"
	OperationWorkspaceCreate     OperationID = "workspace.create"
	OperationWorkspaceStart      OperationID = "workspace.start"
	OperationWorkspaceInspect    OperationID = "workspace.inspect"
	OperationWorkspaceWait       OperationID = "workspace.wait"
	OperationWorkspaceStop       OperationID = "workspace.stop"
	OperationWorkspaceHalt       OperationID = "workspace.halt"
	OperationWorkspaceKill       OperationID = "workspace.kill"
	OperationWorkspaceQuarantine OperationID = "workspace.quarantine"
	OperationWorkspaceDelete     OperationID = "workspace.delete"
	OperationWorkspaceList       OperationID = "workspace.list"
	OperationWorkspaceClone      OperationID = "workspace.clone"
	OperationFileCopyOffline     OperationID = "workspace.file.copy.offline"
	OperationFileCopyLive        OperationID = "workspace.file.copy.live"
	OperationArtifactList        OperationID = "workspace.artifact.list"
	OperationArtifactGet         OperationID = "workspace.artifact.get"
	// OperationArtifactRead is the legacy aggregate artifact identity.
	// New adapter mappings use the independent list and get identities.
	OperationArtifactRead     OperationID = "workspace.artifact.read"
	OperationWorkspaceCommit  OperationID = "workspace.commit"
	OperationWorkspaceApply   OperationID = "workspace.apply"
	OperationWorkspaceResult  OperationID = "workspace.result"
	OperationWorkspaceLogs    OperationID = "workspace.logs"
	OperationWorkspaceEvents  OperationID = "workspace.events"
	OperationWorkspaceStats   OperationID = "workspace.stats"
	OperationWorkspaceEgress  OperationID = "workspace.egress"
	OperationWorkspaceCost    OperationID = "workspace.estimate_cost"
	OperationWorkspaceObserve OperationID = "workspace.observability"
	OperationNetworkPublish   OperationID = "workspace.network.publish"
	OperationNetworkApplyLive OperationID = "workspace.network.apply.live"
	OperationNetworkInspect   OperationID = "workspace.network.inspect"
	OperationWorkspacePause   OperationID = "workspace.pause"
	OperationWorkspaceResume  OperationID = "workspace.resume"
	OperationSnapshotCreate   OperationID = "snapshot.create"
	OperationSnapshotRestore  OperationID = "snapshot.restore"
	OperationSnapshotFork     OperationID = "snapshot.fork"
	OperationSnapshotList     OperationID = "snapshot.list"
	OperationSnapshotDelete   OperationID = "snapshot.delete"
	// OperationSnapshotCatalog is the legacy aggregate catalog identity.
	// New adapter mappings use the independent list and delete identities.
	OperationSnapshotCatalog  OperationID = "snapshot.catalog"
	OperationVolumeCreate     OperationID = "volume.create"
	OperationVolumeList       OperationID = "volume.list"
	OperationVolumeInspect    OperationID = "volume.inspect"
	OperationVolumeDelete     OperationID = "volume.delete"
	OperationImagePull        OperationID = "images.pull"
	OperationImageList        OperationID = "images.list"
	OperationImagePush        OperationID = "images.push"
	OperationImageTag         OperationID = "images.tag"
	OperationImageDelete      OperationID = "images.delete"
	OperationImagePrune       OperationID = "images.prune"
	OperationModelPull        OperationID = "models.pull"
	OperationModelList        OperationID = "models.list"
	OperationModelRemove      OperationID = "models.remove"
	OperationModelPrune       OperationID = "models.prune"
	OperationModelServe       OperationID = "models.serve"
	OperationModelStop        OperationID = "models.stop"
	OperationModelRunners     OperationID = "models.runners"
	OperationModelPolicyCheck OperationID = "models.policy.validate"
	OperationModelPolicyEval  OperationID = "models.policy.evaluate"
	OperationKernelInstall    OperationID = "kernel.install"
	OperationKernelVerify     OperationID = "kernel.verify"
	OperationKernelList       OperationID = "kernel.list"
	OperationKernelCheck      OperationID = "kernel.check"
	OperationRootfsBuild      OperationID = "rootfs.build"
	OperationHostInspect      OperationID = "host.inspect"
	OperationDoctorCheck      OperationID = "doctor.check"
	OperationProfilesList     OperationID = "profiles.list"
	OperationContractGet      OperationID = "contract.get"
	OperationDescribe         OperationID = "microagent.describe"
	OperationProjectInit      OperationID = "project.init"
	OperationSecretCheck      OperationID = "secret.check"
	OperationSecretAudit      OperationID = "secret.audit"
	OperationPerfBoot         OperationID = "performance.boot"
	OperationPerfFootprint    OperationID = "performance.footprint"
	OperationPerfSteady       OperationID = "performance.steady"
	OperationSupervise        OperationID = "workspace.supervise"
	OperationBrokerConfigure  OperationID = "workspace.broker.configure"
	OperationRegistryLogin    OperationID = "registry.login"
	OperationRegistryLogout   OperationID = "registry.logout"
	OperationRegistryList     OperationID = "registry.list"
	OperationServeMCP         OperationID = "serve.mcp"
	OperationHostGC           OperationID = "host.gc"
	OperationPing             OperationID = "microagent.ping"
)

type FeatureContract struct {
	ID           string            `json:"id"`
	Description  string            `json:"description"`
	OwnerPackage string            `json:"ownerPackage"`
	Scope        FeatureScope      `json:"scope"`
	Capability   FeatureCapability `json:"capability,omitempty"`
	// RequiredCapabilities are the operation-level capabilities whose
	// conjunction determines readiness for a high-level feature. Capability
	// remains the compatibility-level aggregate identifier.
	RequiredCapabilities []FeatureCapability `json:"requiredCapabilities,omitempty"`
	CLICommands          []string            `json:"cliCommands,omitempty"`
	MCPTools             []string            `json:"mcpTools,omitempty"`
	Backends             []FeatureBackend    `json:"backends"`
	Gaps                 []FeatureGap        `json:"gaps,omitempty"`
}

// OperationContract is the library-owned identity shared by public adapters.
// Adapter names are aliases, not separate implementations of product behavior.
type OperationContract struct {
	ID                   OperationID           `json:"id"`
	FeatureID            string                `json:"featureId"`
	RequiredCapabilities []FeatureCapability   `json:"requiredCapabilities,omitempty"`
	CLICommands          []string              `json:"cliCommands,omitempty"`
	MCPTools             []string              `json:"mcpTools,omitempty"`
	Effect               OperationEffect       `json:"effect,omitempty"`
	Idempotency          OperationIdempotency  `json:"idempotency,omitempty"`
	Confirmation         OperationConfirmation `json:"confirmation,omitempty"`
	SideEffects          []OperationSideEffect `json:"sideEffects,omitempty"`
	RequestType          OperationTypeID       `json:"requestType,omitempty"`
	ResultType           OperationTypeID       `json:"resultType,omitempty"`
}

type FeatureBackend struct {
	Backend  string `json:"backend"`
	Required bool   `json:"required"`
	Ready    bool   `json:"ready"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	GapID    string `json:"gapId,omitempty"`
}

type FeatureGap struct {
	ID         string            `json:"id"`
	Backend    string            `json:"backend"`
	Status     string            `json:"status"`
	Capability FeatureCapability `json:"capability,omitempty"`
	Reason     string            `json:"reason"`
}

// UnsupportedFeatureError is the shared structured reason a backend-neutral
// feature returns when a backend has an explicit capability gap.
type UnsupportedFeatureError struct {
	Backend    string            `json:"backend"`
	FeatureID  string            `json:"feature_id"`
	Operation  string            `json:"operation,omitempty"`
	Capability FeatureCapability `json:"capability,omitempty"`
	GapID      string            `json:"gap_id,omitempty"`
	Reason     string            `json:"reason"`
}

func (e UnsupportedFeatureError) Error() string {
	op := e.Operation
	if op == "" {
		op = e.FeatureID
	}
	if e.Reason == "" {
		return fmt.Sprintf("%s is not supported on the %s backend", op, e.Backend)
	}
	return fmt.Sprintf("%s is not supported on the %s backend: %s", op, e.Backend, e.Reason)
}

func FeatureContracts() []FeatureContract {
	features := []FeatureContract{
		{
			ID:           "workspace.lifecycle",
			Description:  "create, start, inspect, wait for, stop, halt, kill, quarantine, delete, list, and clone workspaces with structured state transitions",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
		},
		{
			ID:           "workspace.dispatch",
			Description:  "run one isolated task with structured result and egress reporting",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
		},
		{
			ID:           "workspace.exec",
			Description:  "run structured exec requests against a running workspace",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityStructuredExec,
		},
		{
			ID:           "workspace.console",
			Description:  "connect to the interactive console of a running workspace",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityConsole,
		},
		{
			ID:           "workspace.observability",
			Description:  "read workspace result, logs, events, stats, egress audit, and network metadata",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
		},
		{
			ID:           "workspace.apply",
			Description:  "persist workspace network changes, publish them at start, and apply supported host-bind changes live",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityNetworkPublish,
			RequiredCapabilities: []FeatureCapability{
				FeatureCapabilityNetworkPublish,
				FeatureCapabilityLiveNetworkApply,
			},
		},
		{
			ID:           "workspace.files",
			Description:  "copy files, read declared artifacts, and commit stopped workspace rootfs state",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityOfflineFileCopy,
			Gaps: []FeatureGap{
				{
					ID:         "gap.file-copy-live.linux-kvm",
					Backend:    BackendLinuxKVM,
					Status:     "unsupported",
					Capability: FeatureCapabilityLiveFileCopy,
					Reason:     "live file copy is not implemented; stop the workspace and use offline copy",
				},
				{
					ID:         "gap.file-copy-live.apple-vf",
					Backend:    BackendAppleVF,
					Status:     "unsupported",
					Capability: FeatureCapabilityLiveFileCopy,
					Reason:     "live file copy is not implemented; stop the workspace and use offline copy",
				},
			},
		},
		{
			ID:           "workspace.snapshot",
			Description:  "pause/resume, create snapshots, restore from snapshots, and fork from snapshots",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilitySnapshot,
			RequiredCapabilities: []FeatureCapability{
				FeatureCapabilityPauseResume,
				FeatureCapabilitySnapshotCreate,
				FeatureCapabilitySnapshotRestore,
				FeatureCapabilitySnapshotFork,
			},
			// The full capability, including forensic capture (guest secrets
			// RETAINED for investigation), is supported on linux-kvm and
		},
		{
			ID:           "workspace.broker",
			Description:  "serve credential-injecting egress broker endpoints on a workspace vsock listener; the credential is held host-side and never enters the guest",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityBrokerEndpoints,
			Gaps: []FeatureGap{
				{
					ID:         "gap.broker.apple-vf",
					Backend:    BackendAppleVF,
					Status:     "unsupported",
					Capability: FeatureCapabilityBrokerEndpoints,
					Reason:     "the Apple VF supervisor does not serve the broker vsock listener target; broker endpoints require the linux-kvm backend",
				},
			},
		},
		{
			ID:           "workspace.model",
			Description:  "pair workspaces with local model runners and model mediation policy",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
		},
		{
			ID:           "workspace.cost",
			Description:  "estimate workspace resource cost from declared resources",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
		},
		{
			ID:           "workspace.supervision",
			Description:  "install, remove, and run host restart supervision for persistent workspaces",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "volume.management",
			Description:  "manage named microVM volumes",
			OwnerPackage: "pkg/volume",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "image.management",
			Description:  "manage reusable local rootfs image records",
			OwnerPackage: "pkg/imagecache",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "kernel.management",
			Description:  "install, verify, list, and check backend kernel artifacts",
			OwnerPackage: "pkg/kernel",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "rootfs.build",
			Description:  "build rootfs images from OCI images",
			OwnerPackage: "pkg/rootfs",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "project.scaffold",
			Description:  "scaffold starter agent projects that can run in a workspace",
			OwnerPackage: "pkg/scaffold",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "secret.management",
			Description:  "validate secret references and read workspace secret-access audit records",
			OwnerPackage: "pkg/secret",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "performance.measurement",
			Description:  "measure boot latency, workspace footprint, and steady-state resource usage",
			OwnerPackage: "pkg/perf",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "host.diagnostics",
			Description:  "inspect host support, run diagnostics, list profiles, and expose the runtime contract",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "registry.credentials",
			Description:  "store, list, and remove private OCI registry credentials",
			OwnerPackage: "pkg/registryauth",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "agent.integration",
			Description:  "serve the MCP stdio adapter and expose transport diagnostics",
			OwnerPackage: "cmd/microagent",
			Scope:        FeatureHostTooling,
		},
		{
			ID:           "host.maintenance",
			Description:  "reap stale workspace runtime state and dead VM processes",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureHostTooling,
		},
	}
	for _, operation := range OperationContracts() {
		for i := range features {
			if features[i].ID != operation.FeatureID {
				continue
			}
			features[i].CLICommands = append(features[i].CLICommands, operation.CLICommands...)
			features[i].MCPTools = append(features[i].MCPTools, operation.MCPTools...)
			break
		}
	}
	for i := range features {
		features[i].Backends = FeatureBackendSupport(features[i])
	}
	return features
}

// OperationContracts returns the stable library operation registry. Feature
// records remain the high-level product presentation; this registry owns the
// exact CLI and MCP aliases and any narrower capability requirement.
func OperationContracts() []OperationContract {
	operations := []OperationContract{
		{ID: OperationWorkspaceCreate, FeatureID: "workspace.lifecycle", CLICommands: []string{"create"}, MCPTools: []string{"workspace.create"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceStart, FeatureID: "workspace.lifecycle", CLICommands: []string{"start"}, MCPTools: []string{"workspace.start"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceInspect, FeatureID: "workspace.lifecycle", CLICommands: []string{"status", "inspect"}, MCPTools: []string{"workspace.inspect"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceWait, FeatureID: "workspace.lifecycle", CLICommands: []string{"wait"}, MCPTools: []string{"workspace.wait"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceStop, FeatureID: "workspace.lifecycle", Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceHalt, FeatureID: "workspace.lifecycle", CLICommands: []string{"halt", "stop"}, MCPTools: []string{"workspace.halt"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceKill, FeatureID: "workspace.lifecycle", CLICommands: []string{"kill"}, MCPTools: []string{"workspace.kill"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceQuarantine, FeatureID: "workspace.lifecycle", CLICommands: []string{"quarantine"}, MCPTools: []string{"workspace.quarantine"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceDelete, FeatureID: "workspace.lifecycle", CLICommands: []string{"delete", "rm"}, MCPTools: []string{"workspace.delete"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceList, FeatureID: "workspace.lifecycle", CLICommands: []string{"list", "ls", "ps"}, MCPTools: []string{"workspace.list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceClone, FeatureID: "workspace.lifecycle", CLICommands: []string{"clone"}, MCPTools: []string{"workspace.clone"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceDispatch, FeatureID: "workspace.dispatch", CLICommands: []string{"dispatch", "run"}, MCPTools: []string{"workspace.dispatch"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceExec, FeatureID: "workspace.exec", RequiredCapabilities: []FeatureCapability{FeatureCapabilityStructuredExec}, CLICommands: []string{"exec"}, MCPTools: []string{"workspace.exec"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceConsole, FeatureID: "workspace.console", RequiredCapabilities: []FeatureCapability{FeatureCapabilityConsole}, CLICommands: []string{"connect"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: []OperationSideEffect{OperationSideEffectWorkspaceState}},
		{ID: OperationWorkspaceObserve, FeatureID: "workspace.observability"},
		{ID: OperationWorkspaceResult, FeatureID: "workspace.observability", CLICommands: []string{"result"}, MCPTools: []string{"workspace.result"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceLogs, FeatureID: "workspace.observability", CLICommands: []string{"logs", "log"}, MCPTools: []string{"workspace.logs"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceEvents, FeatureID: "workspace.observability", CLICommands: []string{"events"}, MCPTools: []string{"workspace.events"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceStats, FeatureID: "workspace.observability", CLICommands: []string{"stats"}, MCPTools: []string{"workspace.stats"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceEgress, FeatureID: "workspace.observability", CLICommands: []string{"egress"}, MCPTools: []string{"workspace.egress"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationNetworkInspect, FeatureID: "workspace.observability", CLICommands: []string{"network", "network status"}, MCPTools: []string{"network.inspect"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceApply, FeatureID: "workspace.apply", RequiredCapabilities: []FeatureCapability{FeatureCapabilityNetworkPublish}, CLICommands: []string{"apply"}, MCPTools: []string{"workspace.apply"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationNetworkPublish, FeatureID: "workspace.apply", RequiredCapabilities: []FeatureCapability{FeatureCapabilityNetworkPublish}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationNetworkApplyLive, FeatureID: "workspace.apply", RequiredCapabilities: []FeatureCapability{FeatureCapabilityLiveNetworkApply}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationFileCopyOffline, FeatureID: "workspace.files", RequiredCapabilities: []FeatureCapability{FeatureCapabilityOfflineFileCopy}, CLICommands: []string{"cp"}, MCPTools: []string{"cp"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationFileCopyLive, FeatureID: "workspace.files", RequiredCapabilities: []FeatureCapability{FeatureCapabilityLiveFileCopy}},
		{ID: OperationArtifactRead, FeatureID: "workspace.files", RequiredCapabilities: []FeatureCapability{FeatureCapabilityOfflineFileCopy}},
		{ID: OperationArtifactList, FeatureID: "workspace.files", RequiredCapabilities: []FeatureCapability{FeatureCapabilityOfflineFileCopy}, CLICommands: []string{"artifact"}, MCPTools: []string{"artifacts.list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationArtifactGet, FeatureID: "workspace.files", RequiredCapabilities: []FeatureCapability{FeatureCapabilityOfflineFileCopy}, MCPTools: []string{"artifacts.get"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceCommit, FeatureID: "workspace.files", RequiredCapabilities: []FeatureCapability{FeatureCapabilityOfflineFileCopy}, CLICommands: []string{"commit"}, MCPTools: []string{"workspace.commit"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspacePause, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilityPauseResume}, CLICommands: []string{"pause"}, MCPTools: []string{"workspace.pause"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationWorkspaceResume, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilityPauseResume}, CLICommands: []string{"resume"}, MCPTools: []string{"workspace.resume"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationSnapshotCreate, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilitySnapshotCreate}, CLICommands: []string{"snapshot"}, MCPTools: []string{"snapshot.create"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationSnapshotRestore, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilitySnapshotRestore}, CLICommands: []string{"start --from-snapshot"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationSnapshotFork, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilitySnapshotFork}, CLICommands: []string{"create --from-snapshot"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationSnapshotCatalog, FeatureID: "workspace.snapshot"},
		{ID: OperationSnapshotList, FeatureID: "workspace.snapshot", MCPTools: []string{"snapshot.list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationSnapshotDelete, FeatureID: "workspace.snapshot", MCPTools: []string{"snapshot.delete"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationBrokerConfigure, FeatureID: "workspace.broker", RequiredCapabilities: []FeatureCapability{FeatureCapabilityBrokerEndpoints}, CLICommands: []string{"create --broker-upstream", "create --broker-endpoint", "run --broker-upstream", "dispatch --broker-upstream", "start --broker-upstream"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: workspaceMutationSideEffects()},
		{ID: "workspace.model", FeatureID: "workspace.model", CLICommands: []string{"model"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationModelPull, FeatureID: "workspace.model", CLICommands: []string{"model pull"}, MCPTools: []string{"models.pull"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: hostMutationSideEffects()},
		{ID: OperationModelList, FeatureID: "workspace.model", CLICommands: []string{"model list"}, MCPTools: []string{"models.list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationModelRemove, FeatureID: "workspace.model", CLICommands: []string{"model delete"}, MCPTools: []string{"models.remove"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: hostMutationSideEffects()},
		{ID: OperationModelPrune, FeatureID: "workspace.model", CLICommands: []string{"model prune"}, MCPTools: []string{"models.prune"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: hostMutationSideEffects()},
		{ID: OperationModelServe, FeatureID: "workspace.model", CLICommands: []string{"model serve"}, MCPTools: []string{"models.serve"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: hostMutationSideEffects()},
		{ID: OperationModelStop, FeatureID: "workspace.model", CLICommands: []string{"model stop"}, MCPTools: []string{"models.stop"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, SideEffects: hostMutationSideEffects()},
		{ID: OperationModelRunners, FeatureID: "workspace.model", CLICommands: []string{"model runners"}, MCPTools: []string{"models.runners"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationModelPolicyCheck, FeatureID: "workspace.model", CLICommands: []string{"model policy validate"}, MCPTools: []string{"models.policy.validate"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationModelPolicyEval, FeatureID: "workspace.model", CLICommands: []string{"model policy evaluate", "model policy eval"}, MCPTools: []string{"models.policy.evaluate"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationWorkspaceCost, FeatureID: "workspace.cost", MCPTools: []string{"workspace.estimate_cost"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationSupervise, FeatureID: "workspace.supervision", CLICommands: []string{"supervise", "supervise --install", "supervise --uninstall"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
		{ID: "volume.management", FeatureID: "volume.management", CLICommands: []string{"volume"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationVolumeCreate, FeatureID: "volume.management", CLICommands: []string{"volume create"}, MCPTools: []string{"volume.create"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationVolumeList, FeatureID: "volume.management", CLICommands: []string{"volume list"}, MCPTools: []string{"volume.list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationVolumeInspect, FeatureID: "volume.management", CLICommands: []string{"volume status"}, MCPTools: []string{"volume.inspect"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationVolumeDelete, FeatureID: "volume.management", CLICommands: []string{"volume delete"}, MCPTools: []string{"volume.delete"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: "image.management", FeatureID: "image.management", CLICommands: []string{"image"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationImagePull, FeatureID: "image.management", CLICommands: []string{"image pull"}, MCPTools: []string{"images.pull"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationImageList, FeatureID: "image.management", CLICommands: []string{"image list"}, MCPTools: []string{"images.list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationImagePush, FeatureID: "image.management", CLICommands: []string{"image push"}, MCPTools: []string{"images.push"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationImageTag, FeatureID: "image.management", CLICommands: []string{"image tag"}, MCPTools: []string{"images.tag"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationImageDelete, FeatureID: "image.management", CLICommands: []string{"image delete"}, MCPTools: []string{"images.delete"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationImagePrune, FeatureID: "image.management", CLICommands: []string{"image prune"}, MCPTools: []string{"images.prune"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: workspaceMutationSideEffects()},
		{ID: "kernel.management", FeatureID: "kernel.management", CLICommands: []string{"kernel"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationKernelInstall, FeatureID: "kernel.management", CLICommands: []string{"kernel install"}, MCPTools: []string{"kernel.install"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, Confirmation: OperationConfirmationPreview, SideEffects: hostMutationSideEffects()},
		{ID: OperationKernelVerify, FeatureID: "kernel.management", CLICommands: []string{"kernel verify"}, MCPTools: []string{"kernel.verify"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationKernelList, FeatureID: "kernel.management", CLICommands: []string{"kernel list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationKernelCheck, FeatureID: "kernel.management", CLICommands: []string{"kernel check"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationRootfsBuild, FeatureID: "rootfs.build", CLICommands: []string{"rootfs", "rootfs build"}, MCPTools: []string{"rootfs.build"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyKeyedReplay, Confirmation: OperationConfirmationPreview, SideEffects: hostMutationSideEffects()},
		{ID: OperationProjectInit, FeatureID: "project.scaffold", CLICommands: []string{"init"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: hostMutationSideEffects()},
		{ID: "secret.management", FeatureID: "secret.management", CLICommands: []string{"secret"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationSecretCheck, FeatureID: "secret.management", CLICommands: []string{"secret check"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationSecretAudit, FeatureID: "secret.management", CLICommands: []string{"secret audit"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: "performance.measurement", FeatureID: "performance.measurement", CLICommands: []string{"perf"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationPerfBoot, FeatureID: "performance.measurement", CLICommands: []string{"perf boot"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
		{ID: OperationPerfFootprint, FeatureID: "performance.measurement", CLICommands: []string{"perf footprint"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationPerfSteady, FeatureID: "performance.measurement", CLICommands: []string{"perf steady"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
		{ID: "host.diagnostics", FeatureID: "host.diagnostics"},
		{ID: OperationHostInspect, FeatureID: "host.diagnostics", CLICommands: []string{"host"}, MCPTools: []string{"host.inspect"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationDoctorCheck, FeatureID: "host.diagnostics", CLICommands: []string{"doctor"}, MCPTools: []string{"doctor.check"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationProfilesList, FeatureID: "host.diagnostics", CLICommands: []string{"profiles"}, MCPTools: []string{"profiles.list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationContractGet, FeatureID: "host.diagnostics", CLICommands: []string{"contract"}, MCPTools: []string{"contract.get"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationDescribe, FeatureID: "host.diagnostics", MCPTools: []string{"microagent.describe"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationRegistryLogin, FeatureID: "registry.credentials", CLICommands: []string{"registry", "registry login"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: hostMutationSideEffects()},
		{ID: OperationRegistryLogout, FeatureID: "registry.credentials", CLICommands: []string{"registry logout"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyReplayable, SideEffects: hostMutationSideEffects()},
		{ID: OperationRegistryList, FeatureID: "registry.credentials", CLICommands: []string{"registry list"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationServeMCP, FeatureID: "agent.integration", CLICommands: []string{"serve", "serve mcp"}, Effect: OperationEffectMutation, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: hostMutationSideEffects()},
		{ID: OperationPing, FeatureID: "agent.integration", MCPTools: []string{"microagent.ping"}, Effect: OperationEffectRead, Idempotency: OperationIdempotencyReadOnly},
		{ID: OperationHostGC, FeatureID: "host.maintenance", CLICommands: []string{"gc"}, Effect: OperationEffectDestructive, Idempotency: OperationIdempotencyNotIdempotent, SideEffects: workspaceMutationSideEffects()},
	}
	for i := range operations {
		if len(operations[i].CLICommands) == 0 && len(operations[i].MCPTools) == 0 {
			continue
		}
		operations[i].RequestType = OperationTypeID(operations[i].ID + ".request")
		operations[i].ResultType = OperationTypeID(operations[i].ID + ".result")
	}
	return operations
}

func workspaceMutationSideEffects() []OperationSideEffect {
	return []OperationSideEffect{OperationSideEffectHostState, OperationSideEffectWorkspaceState}
}

func hostMutationSideEffects() []OperationSideEffect {
	return []OperationSideEffect{OperationSideEffectHostState}
}

func FeatureBackendSupport(feature FeatureContract) []FeatureBackend {
	backends := []string{BackendAppleVF, BackendLinuxKVM}
	out := make([]FeatureBackend, 0, len(backends))
	for _, backend := range backends {
		ready, reason := BackendSupportsFeature(backend, feature)
		status := "ready"
		gapID := ""
		if !ready {
			status = "unsupported"
			if gap, ok := featureGapForBackend(feature, backend); ok {
				status = gap.Status
				reason = gap.Reason
				gapID = gap.ID
			}
		}
		out = append(out, FeatureBackend{Backend: backend, Required: backendRequiredForFeature(backend, feature), Ready: ready, Status: status, Reason: reason, GapID: gapID})
	}
	return out
}

func BackendSupportsFeature(backend string, feature FeatureContract) (bool, string) {
	switch feature.Scope {
	case FeatureBackendNeutral, FeatureHostTooling:
		if !IsKnownBackend(backend) {
			return false, "unknown backend"
		}
		if len(feature.RequiredCapabilities) > 0 {
			for _, capability := range feature.RequiredCapabilities {
				if ready, reason := backendSupportsCapability(backend, capability); !ready {
					return false, reason
				}
			}
			return true, ""
		}
		if feature.Capability != "" {
			return backendSupportsCapability(backend, feature.Capability)
		}
		return true, ""
	case FeatureExperimental:
		return false, "experimental feature"
	default:
		return false, "unknown feature scope"
	}
}

// BackendSupportsOperation reports readiness for the operation's narrow
// capability requirements. Operations without an override inherit their
// owning feature's requirements.
func BackendSupportsOperation(backend string, operation OperationContract) (bool, string) {
	if len(operation.RequiredCapabilities) > 0 {
		if !IsKnownBackend(backend) {
			return false, "unknown backend"
		}
		for _, capability := range operation.RequiredCapabilities {
			if ready, reason := backendSupportsCapability(backend, capability); !ready {
				return false, reason
			}
		}
		return true, ""
	}
	feature, ok := featureByID(operation.FeatureID)
	if !ok {
		return false, "unknown feature"
	}
	return BackendSupportsFeature(backend, feature)
}

func OperationForMCPTool(tool string) (OperationContract, bool) {
	for _, operation := range OperationContracts() {
		for _, candidate := range operation.MCPTools {
			if candidate == tool {
				return operation, true
			}
		}
	}
	return OperationContract{}, false
}

func OperationContractByID(id OperationID) (OperationContract, bool) {
	for _, operation := range OperationContracts() {
		if operation.ID == id {
			return operation, true
		}
	}
	return OperationContract{}, false
}

func OperationForCLICommand(command string) (OperationContract, bool) {
	for _, operation := range OperationContracts() {
		for _, candidate := range operation.CLICommands {
			if candidate == command {
				return operation, true
			}
		}
	}
	return OperationContract{}, false
}

func FeatureForMCPTool(tool string) (FeatureContract, bool) {
	operation, ok := OperationForMCPTool(tool)
	if !ok {
		return FeatureContract{}, false
	}
	return featureByID(operation.FeatureID)
}

func FeatureForCLICommand(command string) (FeatureContract, bool) {
	operation, ok := OperationForCLICommand(command)
	if !ok {
		return FeatureContract{}, false
	}
	return featureByID(operation.FeatureID)
}

func featureByID(id string) (FeatureContract, bool) {
	for _, feature := range FeatureContracts() {
		if feature.ID == id {
			return feature, true
		}
	}
	return FeatureContract{}, false
}

// NewUnsupportedFeatureError returns the backend gap for a feature as an error
// that can be shared by library, CLI, and MCP adapters.
func NewUnsupportedFeatureError(backend string, feature FeatureContract, operation string) UnsupportedFeatureError {
	capability := feature.Capability
	for _, required := range feature.RequiredCapabilities {
		if ready, _ := backendSupportsCapability(backend, required); !ready {
			capability = required
			break
		}
	}
	return newUnsupportedFeatureError(backend, feature, operation, capability)
}

// NewUnsupportedFeatureCapabilityError returns a structured feature error for
// one operation-level capability. Callers use it when a high-level feature has
// several independently gated operations.
func NewUnsupportedFeatureCapabilityError(backend string, feature FeatureContract, operation string, capability FeatureCapability) UnsupportedFeatureError {
	return newUnsupportedFeatureError(backend, feature, operation, capability)
}

// NewUnsupportedOperationError builds the shared error directly from the
// operation registry, avoiding capability knowledge in CLI and MCP adapters.
func NewUnsupportedOperationError(backend string, operation OperationContract, description string) UnsupportedFeatureError {
	feature, _ := featureByID(operation.FeatureID)
	capability := feature.Capability
	for _, required := range operation.RequiredCapabilities {
		if ready, _ := backendSupportsCapability(backend, required); !ready {
			capability = required
			break
		}
	}
	return newUnsupportedFeatureError(backend, feature, description, capability)
}

func newUnsupportedFeatureError(backend string, feature FeatureContract, operation string, capability FeatureCapability) UnsupportedFeatureError {
	reason := ""
	gapID := ""
	if gap, ok := featureGapForBackend(feature, backend); ok {
		reason = gap.Reason
		gapID = gap.ID
	} else if _, unsupportedReason := backendSupportsCapability(backend, capability); unsupportedReason != "" {
		reason = unsupportedReason
	}
	return UnsupportedFeatureError{
		Backend:    backend,
		FeatureID:  feature.ID,
		Operation:  operation,
		Capability: capability,
		GapID:      gapID,
		Reason:     reason,
	}
}

// allFeatureCapabilities is the full set of declared capability identifiers, in
// a stable order. Adding a FeatureCapability constant without adding it here is
// caught by the capability-coverage test.
func allFeatureCapabilities() []FeatureCapability {
	return []FeatureCapability{
		FeatureCapabilityStructuredExec,
		FeatureCapabilityNetworkPublish,
		FeatureCapabilityLiveNetworkApply,
		FeatureCapabilityOfflineFileCopy,
		FeatureCapabilityLiveFileCopy,
		FeatureCapabilityPauseResume,
		FeatureCapabilitySnapshotCreate,
		FeatureCapabilitySnapshotRestore,
		FeatureCapabilitySnapshotFork,
		FeatureCapabilityBrokerEndpoints,
		FeatureCapabilityConsole,
	}
}

// DeclaredCapabilities returns the capabilities a backend advertises, in a
// stable order, derived from its BackendCapabilities table.
func DeclaredCapabilities(backend string) []FeatureCapability {
	var out []FeatureCapability
	for _, capability := range allFeatureCapabilities() {
		if ok, _ := backendSupportsCapability(backend, capability); ok {
			out = append(out, capability)
		}
	}
	return out
}

func backendSupportsCapability(backend string, capability FeatureCapability) (bool, string) {
	caps := BackendCapabilities(backend)
	switch capability {
	case FeatureCapabilityStructuredExec:
		if caps.StructuredExec {
			return true, ""
		}
	case FeatureCapabilityNetworkPublish:
		if caps.NetworkPublish {
			return true, ""
		}
	case FeatureCapabilityLiveNetworkApply:
		if caps.LiveNetworkApply {
			return true, ""
		}
	case FeatureCapabilityOfflineFileCopy:
		if caps.OfflineFileCopy {
			return true, ""
		}
	case FeatureCapabilityLiveFileCopy:
		if caps.LiveFileCopy {
			return true, ""
		}
	case FeatureCapabilitySnapshot:
		if caps.Snapshot {
			return true, ""
		}
	case FeatureCapabilityPauseResume:
		if caps.PauseResume {
			return true, ""
		}
	case FeatureCapabilitySnapshotCreate:
		if caps.SnapshotCreate {
			return true, ""
		}
	case FeatureCapabilitySnapshotRestore:
		if caps.SnapshotRestore {
			return true, ""
		}
	case FeatureCapabilitySnapshotFork:
		if caps.SnapshotFork {
			return true, ""
		}
	case FeatureCapabilityBrokerEndpoints:
		if caps.BrokerEndpoints {
			return true, ""
		}
	case FeatureCapabilityConsole:
		// A backend declares an interactive console when it defines a shell
		// transport (ShellNetwork). Present on every current backend.
		if caps.ShellNetwork != "" {
			return true, ""
		}
	default:
		return false, "unknown capability"
	}
	return false, string(capability) + " capability is not supported"
}

func backendRequiredForFeature(backend string, feature FeatureContract) bool {
	return backend == BackendAppleVF || backend == BackendLinuxKVM
}

func featureGapForBackend(feature FeatureContract, backend string) (FeatureGap, bool) {
	for _, gap := range feature.Gaps {
		if gap.Backend == backend {
			return gap, true
		}
	}
	return FeatureGap{}, false
}

func IsKnownBackend(backend string) bool {
	return backend == BackendAppleVF || backend == BackendLinuxKVM
}
