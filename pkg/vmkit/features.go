package vmkit

import "fmt"

type FeatureScope string

const (
	FeatureBackendNeutral FeatureScope = "backend-neutral"
	FeatureHostTooling    FeatureScope = "host-tooling"
	FeatureExperimental   FeatureScope = "experimental"
)

type FeatureCapability string

const (
	FeatureCapabilityStructuredExec   FeatureCapability = "StructuredExec"
	FeatureCapabilityLiveNetworkApply FeatureCapability = "LiveNetworkApply"
	FeatureCapabilitySnapshot         FeatureCapability = "Snapshot"
)

type FeatureContract struct {
	ID           string            `json:"id"`
	Description  string            `json:"description"`
	OwnerPackage string            `json:"ownerPackage"`
	Scope        FeatureScope      `json:"scope"`
	Capability   FeatureCapability `json:"capability,omitempty"`
	CLICommands  []string          `json:"cliCommands,omitempty"`
	MCPTools     []string          `json:"mcpTools,omitempty"`
	Backends     []FeatureBackend  `json:"backends"`
	Gaps         []FeatureGap      `json:"gaps,omitempty"`
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
			CLICommands:  []string{"create", "start", "status", "wait", "stop", "halt", "kill", "quarantine", "delete", "list", "ls", "ps", "clone"},
			MCPTools:     []string{"workspace.create", "workspace.start", "workspace.inspect", "workspace.wait", "workspace.stop", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.delete", "workspace.list", "workspace.clone"},
		},
		{
			ID:           "workspace.dispatch",
			Description:  "run one isolated task with structured result and egress reporting",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			CLICommands:  []string{"dispatch", "run"},
			MCPTools:     []string{"workspace.dispatch"},
		},
		{
			ID:           "workspace.exec",
			Description:  "run structured exec requests against a running workspace",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityStructuredExec,
			CLICommands:  []string{"exec", "connect"},
			MCPTools:     []string{"workspace.exec"},
		},
		{
			ID:           "workspace.observability",
			Description:  "read workspace result, logs, events, stats, egress audit, and network metadata",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			CLICommands:  []string{"result", "logs", "events", "stats", "egress", "network", "network status"},
			MCPTools:     []string{"workspace.result", "workspace.logs", "workspace.events", "workspace.stats", "workspace.egress", "network.inspect"},
		},
		{
			ID:           "workspace.apply",
			Description:  "apply supported workspace spec changes, including live host-bind reloads when the backend supports them",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilityLiveNetworkApply,
			CLICommands:  []string{"apply"},
			MCPTools:     []string{"workspace.apply"},
		},
		{
			ID:           "workspace.files",
			Description:  "copy files, read declared artifacts, and commit stopped workspace rootfs state",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			CLICommands:  []string{"cp", "artifact", "commit"},
			MCPTools:     []string{"cp", "artifacts.list", "artifacts.get", "workspace.commit"},
		},
		{
			ID:           "workspace.snapshot",
			Description:  "pause/resume, create snapshots, restore from snapshots, and fork from snapshots",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			Capability:   FeatureCapabilitySnapshot,
			CLICommands:  []string{"pause", "resume", "snapshot", "start --from-snapshot", "create --from-snapshot"},
			MCPTools:     []string{"workspace.pause", "workspace.resume", "snapshot.create", "snapshot.list", "snapshot.delete"},
		},
		{
			ID:           "workspace.model",
			Description:  "pair workspaces with local model runners and model mediation policy",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			CLICommands:  []string{"model"},
			MCPTools:     []string{"models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners", "models.policy.validate", "models.policy.evaluate"},
		},
		{
			ID:           "workspace.cost",
			Description:  "estimate workspace resource cost from declared resources",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureBackendNeutral,
			MCPTools:     []string{"workspace.estimate_cost"},
		},
		{
			ID:           "workspace.supervision",
			Description:  "install, remove, and run host restart supervision for persistent workspaces",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"supervise"},
		},
		{
			ID:           "volume.management",
			Description:  "manage named microVM volumes",
			OwnerPackage: "pkg/volume",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"volume"},
			MCPTools:     []string{"volume.create", "volume.list", "volume.inspect", "volume.delete"},
		},
		{
			ID:           "image.management",
			Description:  "manage reusable local rootfs image records",
			OwnerPackage: "pkg/imagecache",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"image"},
			MCPTools:     []string{"images.pull", "images.list", "images.push", "images.tag", "images.delete", "images.prune"},
		},
		{
			ID:           "kernel.management",
			Description:  "install, verify, list, and check backend kernel artifacts",
			OwnerPackage: "pkg/kernel",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"kernel"},
			MCPTools:     []string{"kernel.install", "kernel.verify"},
		},
		{
			ID:           "rootfs.build",
			Description:  "build rootfs images from OCI images",
			OwnerPackage: "pkg/rootfs",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"rootfs build"},
			MCPTools:     []string{"rootfs.build"},
		},
		{
			ID:           "project.scaffold",
			Description:  "scaffold starter agent projects that can run in a workspace",
			OwnerPackage: "pkg/scaffold",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"init"},
		},
		{
			ID:           "secret.management",
			Description:  "validate secret references and read workspace secret-access audit records",
			OwnerPackage: "pkg/secret",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"secret", "secret check", "secret audit"},
		},
		{
			ID:           "performance.measurement",
			Description:  "measure boot latency, workspace footprint, and steady-state resource usage",
			OwnerPackage: "pkg/perf",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"perf"},
		},
		{
			ID:           "host.diagnostics",
			Description:  "inspect host support, run diagnostics, list profiles, and expose the runtime contract",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureHostTooling,
			CLICommands:  []string{"host", "doctor", "profiles", "contract"},
			MCPTools:     []string{"host.inspect", "doctor.check", "profiles.list", "contract.get", "microagent.describe"},
		},
	}
	for i := range features {
		features[i].Backends = FeatureBackendSupport(features[i])
	}
	return features
}

func FeatureBackendSupport(feature FeatureContract) []FeatureBackend {
	backends := []string{BackendAppleVF, BackendLinuxKVM, BackendWindowsHyperV}
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
		if feature.Capability != "" {
			return backendSupportsCapability(backend, feature.Capability)
		}
		return true, ""
	case FeatureExperimental:
		return backend == BackendWindowsHyperV, "experimental feature"
	default:
		return false, "unknown feature scope"
	}
}

func FeatureForMCPTool(tool string) (FeatureContract, bool) {
	for _, feature := range FeatureContracts() {
		for _, candidate := range feature.MCPTools {
			if candidate == tool {
				return feature, true
			}
		}
	}
	return FeatureContract{}, false
}

func FeatureForCLICommand(command string) (FeatureContract, bool) {
	for _, feature := range FeatureContracts() {
		for _, candidate := range feature.CLICommands {
			if candidate == command {
				return feature, true
			}
		}
	}
	return FeatureContract{}, false
}

// NewUnsupportedFeatureError returns the backend gap for a feature as an error
// that can be shared by library, CLI, and MCP adapters.
func NewUnsupportedFeatureError(backend string, feature FeatureContract, operation string) UnsupportedFeatureError {
	reason := ""
	gapID := ""
	if gap, ok := featureGapForBackend(feature, backend); ok {
		reason = gap.Reason
		gapID = gap.ID
	} else if _, unsupportedReason := BackendSupportsFeature(backend, feature); unsupportedReason != "" {
		reason = unsupportedReason
	}
	return UnsupportedFeatureError{
		Backend:    backend,
		FeatureID:  feature.ID,
		Operation:  operation,
		Capability: feature.Capability,
		GapID:      gapID,
		Reason:     reason,
	}
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
	default:
		return false, "unknown capability"
	}
	return false, string(capability) + " capability is not supported"
}

func backendRequiredForFeature(backend string, feature FeatureContract) bool {
	if feature.Scope == FeatureExperimental {
		return backend == BackendWindowsHyperV
	}
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
	return backend == BackendAppleVF || backend == BackendLinuxKVM || backend == BackendWindowsHyperV
}
