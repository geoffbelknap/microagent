package vmkit

type FeatureScope string

const (
	FeatureAllBackends     FeatureScope = "all-backends"
	FeatureCapabilityGated FeatureScope = "capability-gated"
	FeatureHostTooling     FeatureScope = "host-tooling"
	FeatureExperimental    FeatureScope = "experimental"
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
}

type FeatureBackend struct {
	Backend   string `json:"backend"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

func FeatureContracts() []FeatureContract {
	features := []FeatureContract{
		{
			ID:           "workspace.lifecycle",
			Description:  "create, start, inspect, stop, halt, kill, quarantine, delete, list, and clone workspaces with structured state transitions",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureAllBackends,
			CLICommands:  []string{"create", "start", "status", "stop", "halt", "kill", "quarantine", "delete", "list", "ps", "clone"},
			MCPTools:     []string{"workspace.create", "workspace.start", "workspace.inspect", "workspace.stop", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.delete", "workspace.list", "workspace.clone"},
		},
		{
			ID:           "workspace.dispatch",
			Description:  "run one isolated task with structured result and egress reporting",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureAllBackends,
			CLICommands:  []string{"dispatch", "run"},
			MCPTools:     []string{"workspace.dispatch"},
		},
		{
			ID:           "workspace.exec",
			Description:  "run structured exec requests against a running workspace",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureCapabilityGated,
			Capability:   FeatureCapabilityStructuredExec,
			CLICommands:  []string{"exec", "connect"},
			MCPTools:     []string{"workspace.exec"},
		},
		{
			ID:           "workspace.observability",
			Description:  "read workspace result, logs, events, stats, egress audit, and network metadata",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureAllBackends,
			CLICommands:  []string{"result", "logs", "events", "stats", "egress", "network status"},
			MCPTools:     []string{"workspace.result", "workspace.logs", "workspace.events", "workspace.stats", "workspace.egress", "network.inspect"},
		},
		{
			ID:           "workspace.apply",
			Description:  "apply supported workspace spec changes, including live host-bind reloads when the backend supports them",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureCapabilityGated,
			Capability:   FeatureCapabilityLiveNetworkApply,
			CLICommands:  []string{"apply"},
			MCPTools:     []string{"workspace.apply"},
		},
		{
			ID:           "workspace.files",
			Description:  "copy files, read declared artifacts, and commit stopped workspace rootfs state",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureAllBackends,
			CLICommands:  []string{"cp", "artifact", "commit"},
			MCPTools:     []string{"cp", "artifacts.list", "artifacts.get", "workspace.commit"},
		},
		{
			ID:           "workspace.snapshot",
			Description:  "pause/resume, create snapshots, restore from snapshots, and fork from snapshots",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureCapabilityGated,
			Capability:   FeatureCapabilitySnapshot,
			CLICommands:  []string{"pause", "resume", "snapshot", "start --from-snapshot", "create --from-snapshot"},
			MCPTools:     []string{"workspace.pause", "workspace.resume", "snapshot.create", "snapshot.list", "snapshot.delete"},
		},
		{
			ID:           "workspace.model",
			Description:  "pair workspaces with local model runners and model mediation policy",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureAllBackends,
			CLICommands:  []string{"model"},
			MCPTools:     []string{"models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners", "models.policy.validate", "models.policy.evaluate"},
		},
		{
			ID:           "workspace.cost",
			Description:  "estimate workspace resource cost from declared resources",
			OwnerPackage: "pkg/workspace",
			Scope:        FeatureAllBackends,
			MCPTools:     []string{"workspace.estimate_cost"},
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
		supported, reason := BackendSupportsFeature(backend, feature)
		out = append(out, FeatureBackend{Backend: backend, Supported: supported, Reason: reason})
	}
	return out
}

func BackendSupportsFeature(backend string, feature FeatureContract) (bool, string) {
	switch feature.Scope {
	case FeatureAllBackends, FeatureHostTooling:
		if !IsKnownBackend(backend) {
			return false, "unknown backend"
		}
		return true, ""
	case FeatureExperimental:
		return backend == BackendWindowsHyperV, "experimental feature"
	case FeatureCapabilityGated:
		return backendSupportsCapability(backend, feature.Capability)
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

func IsKnownBackend(backend string) bool {
	return backend == BackendAppleVF || backend == BackendLinuxKVM || backend == BackendWindowsHyperV
}
