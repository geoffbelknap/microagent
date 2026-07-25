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

const (
	FeatureCapabilityStructuredExec   FeatureCapability = "StructuredExec"
	FeatureCapabilityLiveNetworkApply FeatureCapability = "LiveNetworkApply"
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
	OperationWorkspacePause  OperationID = "workspace.pause"
	OperationWorkspaceResume OperationID = "workspace.resume"
	OperationSnapshotCreate  OperationID = "snapshot.create"
	OperationSnapshotRestore OperationID = "snapshot.restore"
	OperationSnapshotFork    OperationID = "snapshot.fork"
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
	ID                   OperationID         `json:"id"`
	FeatureID            string              `json:"featureId"`
	RequiredCapabilities []FeatureCapability `json:"requiredCapabilities,omitempty"`
	CLICommands          []string            `json:"cliCommands,omitempty"`
	MCPTools             []string            `json:"mcpTools,omitempty"`
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
			ID:           "workspace.observability",
			Description:  "read workspace result, logs, events, stats, egress audit, and network metadata",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
		},
		{
			ID:           "workspace.apply",
			Description:  "apply supported workspace spec changes, including live host-bind reloads when the backend supports them",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityLiveNetworkApply,
		},
		{
			ID:           "workspace.files",
			Description:  "copy files, read declared artifacts, and commit stopped workspace rootfs state",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
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
	return []OperationContract{
		{ID: "workspace.lifecycle", FeatureID: "workspace.lifecycle", CLICommands: []string{"create", "start", "status", "wait", "stop", "halt", "kill", "quarantine", "delete", "list", "ls", "ps", "clone"}, MCPTools: []string{"workspace.create", "workspace.start", "workspace.inspect", "workspace.wait", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.delete", "workspace.list", "workspace.clone"}},
		{ID: "workspace.dispatch", FeatureID: "workspace.dispatch", CLICommands: []string{"dispatch", "run"}, MCPTools: []string{"workspace.dispatch"}},
		{ID: "workspace.exec", FeatureID: "workspace.exec", RequiredCapabilities: []FeatureCapability{FeatureCapabilityStructuredExec}, CLICommands: []string{"exec", "connect"}, MCPTools: []string{"workspace.exec"}},
		{ID: "workspace.observability", FeatureID: "workspace.observability", CLICommands: []string{"result", "logs", "events", "stats", "egress", "network", "network status"}, MCPTools: []string{"workspace.result", "workspace.logs", "workspace.events", "workspace.stats", "workspace.egress", "network.inspect"}},
		{ID: "workspace.apply", FeatureID: "workspace.apply", RequiredCapabilities: []FeatureCapability{FeatureCapabilityLiveNetworkApply}, CLICommands: []string{"apply"}, MCPTools: []string{"workspace.apply"}},
		{ID: "workspace.files", FeatureID: "workspace.files", CLICommands: []string{"cp", "artifact", "commit"}, MCPTools: []string{"cp", "artifacts.list", "artifacts.get", "workspace.commit"}},
		{ID: OperationWorkspacePause, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilityPauseResume}, CLICommands: []string{"pause"}, MCPTools: []string{"workspace.pause"}},
		{ID: OperationWorkspaceResume, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilityPauseResume}, CLICommands: []string{"resume"}, MCPTools: []string{"workspace.resume"}},
		{ID: OperationSnapshotCreate, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilitySnapshotCreate}, CLICommands: []string{"snapshot"}, MCPTools: []string{"snapshot.create"}},
		{ID: OperationSnapshotRestore, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilitySnapshotRestore}, CLICommands: []string{"start --from-snapshot"}},
		{ID: OperationSnapshotFork, FeatureID: "workspace.snapshot", RequiredCapabilities: []FeatureCapability{FeatureCapabilitySnapshotFork}, CLICommands: []string{"create --from-snapshot"}},
		{ID: "snapshot.catalog", FeatureID: "workspace.snapshot", MCPTools: []string{"snapshot.list", "snapshot.delete"}},
		{ID: "workspace.broker", FeatureID: "workspace.broker", RequiredCapabilities: []FeatureCapability{FeatureCapabilityBrokerEndpoints}, CLICommands: []string{"create --broker-upstream", "create --broker-endpoint", "run --broker-upstream", "dispatch --broker-upstream", "start --broker-upstream"}},
		{ID: "workspace.model", FeatureID: "workspace.model", CLICommands: []string{"model"}, MCPTools: []string{"models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners", "models.policy.validate", "models.policy.evaluate"}},
		{ID: "workspace.cost", FeatureID: "workspace.cost", MCPTools: []string{"workspace.estimate_cost"}},
		{ID: "workspace.supervision", FeatureID: "workspace.supervision", CLICommands: []string{"supervise"}},
		{ID: "volume.management", FeatureID: "volume.management", CLICommands: []string{"volume"}, MCPTools: []string{"volume.create", "volume.list", "volume.inspect", "volume.delete"}},
		{ID: "image.management", FeatureID: "image.management", CLICommands: []string{"image"}, MCPTools: []string{"images.pull", "images.list", "images.push", "images.tag", "images.delete", "images.prune"}},
		{ID: "kernel.management", FeatureID: "kernel.management", CLICommands: []string{"kernel"}, MCPTools: []string{"kernel.install", "kernel.verify"}},
		{ID: "rootfs.build", FeatureID: "rootfs.build", CLICommands: []string{"rootfs build"}, MCPTools: []string{"rootfs.build"}},
		{ID: "project.scaffold", FeatureID: "project.scaffold", CLICommands: []string{"init"}},
		{ID: "secret.management", FeatureID: "secret.management", CLICommands: []string{"secret", "secret check", "secret audit"}},
		{ID: "performance.measurement", FeatureID: "performance.measurement", CLICommands: []string{"perf"}},
		{ID: "host.diagnostics", FeatureID: "host.diagnostics", CLICommands: []string{"host", "doctor", "profiles", "contract"}, MCPTools: []string{"host.inspect", "doctor.check", "profiles.list", "contract.get", "microagent.describe"}},
	}
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
		FeatureCapabilityLiveNetworkApply,
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
	case FeatureCapabilityLiveNetworkApply:
		if caps.LiveNetworkApply {
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
