package workspace

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type ApplyResult struct {
	Workspace string          `json:"workspace"`
	State     string          `json:"state,omitempty"`
	Applied   []string        `json:"applied,omitempty"`
	Reloaded  bool            `json:"reloaded,omitempty"`
	Network   NetworkSpec     `json:"network,omitempty"`
	Response  *vmkit.Response `json:"response,omitempty"`
}

func Apply(ctx context.Context, opts Options, spec Spec) (ApplyResult, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return ApplyResult{}, operation.New(operation.ErrorValidation, "apply spec requires name")
	}
	if err := ValidateName(name); err != nil {
		return ApplyResult{}, err
	}
	opts.Name = name
	manifest, err := ReadManifest(opts.StateDir, name)
	if err != nil {
		return ApplyResult{}, err
	}
	next := manifest
	var applied []string
	if spec.Restart != "" {
		restart := NormalizeRestartPolicy(spec.Restart)
		if err := ValidateRestartPolicy(restart); err != nil {
			return ApplyResult{}, err
		}
		if restart != NormalizeRestartPolicy(manifest.Restart) {
			next.Restart = restart
			applied = append(applied, "restart")
		}
	}
	if specHasNetwork(spec.Network) {
		network := NormalizeNetworkConfig(NetworkConfigFromSpec(spec.Network))
		if err := vmkit.ValidateNetworkConfig(network); err != nil {
			return ApplyResult{}, err
		}
		networkSpec := NetworkSpecFromConfig(network)
		if !reflect.DeepEqual(networkSpec, manifest.Network) {
			next.Network = networkSpec
			applied = append(applied, "network")
		}
	}
	if specAppliesEgress(spec.Agent) {
		mode := next.EgressMode
		if strings.TrimSpace(spec.Agent.Egress) != "" {
			mode, err = vmkit.ValidateEgressMode(spec.Agent.Egress)
			if err != nil {
				return ApplyResult{}, fmt.Errorf("agent egress: %w", err)
			}
		}
		allow := next.EgressAllow
		if len(spec.Agent.Allow) != 0 {
			// apply is declarative: replace the prior set rather than unioning it.
			allow = egress.DedupeHosts(spec.Agent.Allow)
		}
		locked := next.EgressAllowlistLocked || spec.Agent.LockAllowlist
		passthrough := next.EgressPassthrough
		if spec.Agent.LockAllowlist {
			// A locked apply means exactly the declared allowlist. Keeping an old
			// passthrough grant would make the apparent tightening fail open.
			passthrough = nil
		}
		policy := vmkit.NormalizeEgressPolicy(vmkit.EgressPolicy{
			Mode: mode, Allow: allow, Passthrough: passthrough,
			AllowlistLocked: locked,
			Caps: vmkit.EgressCaps{
				MaxBytesPerSec: next.EgressMaxBytesPerSec, MaxTotalBytes: next.EgressMaxTotalBytes,
				MaxConcurrentConns: next.EgressMaxConcurrentConns,
			},
		})
		if err := policy.Validate(); err != nil {
			return ApplyResult{}, err
		}
		if err := policy.ValidateForCaptureProvider(opts.Backend, NetworkConfigFromSpec(next.Network).Mode); err != nil {
			return ApplyResult{}, err
		}
		if next.EgressMode != policy.Mode || !reflect.DeepEqual(next.EgressAllow, policy.Allow) ||
			!reflect.DeepEqual(next.EgressPassthrough, policy.Passthrough) || next.EgressAllowlistLocked != policy.AllowlistLocked {
			next.EgressMode = policy.Mode
			next.EgressAllow = policy.Allow
			next.EgressPassthrough = policy.Passthrough
			next.EgressAllowlistLocked = policy.AllowlistLocked
			applied = append(applied, "egress")
		}
	}
	state, _, err := LatestStartState(opts.StateDir, name)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(applied) == 0 {
		return ApplyResult{Workspace: name, State: string(state), Network: next.Network}, nil
	}
	if containsString(applied, "network") {
		operationID := vmkit.OperationWorkspaceApply
		description := "persist network configuration"
		if state == vmkit.StateRunning {
			operationID = vmkit.OperationNetworkApplyLive
			description = "live network apply"
		}
		operation, ok := vmkit.OperationContractByID(operationID)
		if !ok {
			return ApplyResult{}, fmt.Errorf("operation contract %s is not registered", operationID)
		}
		if ready, _ := vmkit.BackendSupportsOperation(opts.Backend, operation); !ready {
			return ApplyResult{}, vmkit.NewUnsupportedOperationError(opts.Backend, operation, description)
		}
	}
	if state == vmkit.StateRunning && containsString(applied, "network") {
		oldNetwork := NetworkConfigFromSpec(manifest.Network)
		newNetwork := NetworkConfigFromSpec(next.Network)
		if !LivePortForwardHostOnlyChange(oldNetwork, newNetwork) {
			return ApplyResult{}, fmt.Errorf("live network apply only supports host bind changes for existing port forwards; stop and start %s to apply this change", name)
		}
	}
	if state == vmkit.StateRunning && containsString(applied, "egress") {
		return ApplyResult{}, fmt.Errorf("live egress apply is not supported; halt and start %s so the host mediator starts with the new policy", name)
	}
	result := ApplyResult{Workspace: name, State: string(state), Applied: applied, Network: next.Network}
	if state == vmkit.StateRunning && containsString(applied, "network") {
		applyOpts := OptionsFromManifest(opts, next)
		rootfsPath := WorkspaceRootfsPath(opts.StateDir, name, opts.Backend)
		applyReq, err := Request(applyOpts, "apply", rootfsPath, NewRequestID())
		if err != nil {
			return result, err
		}
		resp, err := Dispatch(ctx, applyOpts, applyReq)
		result.Reloaded = resp.OK
		result.Response = &resp
		if err == nil {
			err = writeWorkspaceManifestRecord(opts, next, "apply")
		}
		return result, err
	}
	if err := writeWorkspaceManifestRecord(opts, next, "apply"); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

func specAppliesEgress(agent AgentSpec) bool {
	return strings.TrimSpace(agent.Egress) != "" || len(agent.Allow) != 0 || agent.LockAllowlist
}

func OptionsFromManifest(base Options, manifest Manifest) Options {
	opts := base
	if opts.Purpose == "" {
		opts.Purpose = manifest.Purpose
	}
	if opts.CorrelationID == "" {
		opts.CorrelationID = manifest.CorrelationID
	}
	opts.Profile = firstNonEmpty(manifest.Profile, DefaultWorkspaceProfile)
	opts.RestartPolicy = NormalizeRestartPolicy(manifest.Restart)
	opts.Network = NormalizeNetworkConfig(NetworkConfigFromSpec(manifest.Network))
	if manifest.Resources.MemoryMiB != 0 {
		opts.MemoryMiB = manifest.Resources.MemoryMiB
	}
	if manifest.Resources.CPUCount != 0 {
		opts.CPUCount = manifest.Resources.CPUCount
	}
	if manifest.Resources.SizeMiB != 0 {
		opts.SizeMiB = manifest.Resources.SizeMiB
	}
	opts.ServiceCommand = manifest.Service
	opts.ConsoleShell = manifest.ConsoleShell
	opts.Hostname = manifest.Hostname
	opts.Model = strings.TrimSpace(manifest.Model)
	if manifest.ModelRunner != nil {
		opts.ModelRunner = *manifest.ModelRunner
	} else {
		opts.ModelRunner = ModelRunnerSpec{}
	}
	if manifest.ModelMediation != nil {
		opts.ModelMediation = *manifest.ModelMediation
	} else {
		opts.ModelMediation = ModelMediationSpec{}
	}
	opts.Mediation = manifest.Mediation
	opts.Disks = manifest.Disks
	if len(manifest.Secrets) > 0 {
		opts.Secrets = make(map[string]string, len(manifest.Secrets))
		for _, ref := range manifest.Secrets {
			opts.Secrets[ref.Name] = ref.Ref
		}
	}
	opts.SecretEnvFiles = manifest.SecretEnvFiles
	if len(manifest.OnDemandSecrets) > 0 {
		opts.OnDemandSecrets = make(map[string]string, len(manifest.OnDemandSecrets))
		for _, ref := range manifest.OnDemandSecrets {
			opts.OnDemandSecrets[ref.Name] = ref.Ref
		}
	}
	opts.SecretsAudit = manifest.SecretsAudit
	opts.CapabilityRiskAcknowledgement = manifest.CapabilityRiskAcknowledgement
	// Resolve the egress mode's default (empty -> broker) but do NOT validate
	// here: a retired mode from an old manifest must survive to be rejected at
	// Request()'s policy chokepoint on start/restore, not silently remapped.
	opts.EgressMode = vmkit.ResolveEgressModeDefault(manifest.EgressMode)
	opts.EgressAllow = manifest.EgressAllow
	opts.EgressPassthrough = manifest.EgressPassthrough
	opts.EgressAllowlistLocked = manifest.EgressAllowlistLocked
	opts.EgressSwapConfigPath = manifest.EgressSwapConfigPath
	// Egress caps were resolved once at create time; restore them as-is and
	// mark them Explicit so EgressPolicyFromOptions never re-derives a fresh
	// bounded-operations default on top of an already-decided value. See the
	// identical handling in applyManifest.
	opts.EgressMaxBytesPerSec = manifest.EgressMaxBytesPerSec
	opts.EgressMaxTotalBytes = manifest.EgressMaxTotalBytes
	opts.EgressMaxConcurrentConns = manifest.EgressMaxConcurrentConns
	opts.EgressMaxTotalBytesExplicit = true
	opts.EgressMaxConcurrentConnsExplicit = true
	opts.Outputs = manifest.Artifacts.Egress
	if opts.KernelPath == "" {
		opts.KernelPath = KernelPath(opts.Backend, opts.Architecture)
	}
	opts.ResultPort = DefaultResultPort
	opts.Timeout = DefaultTimeout
	opts.SerialInput = BackendSupportsConsoleInput(opts.Backend)
	return opts
}

func LivePortForwardHostOnlyChange(oldNetwork, newNetwork vmkit.NetworkConfig) bool {
	oldNetwork = NormalizeNetworkConfig(oldNetwork)
	newNetwork = NormalizeNetworkConfig(newNetwork)
	if oldNetwork.Mode != newNetwork.Mode ||
		!reflect.DeepEqual(oldNetwork.DNS, newNetwork.DNS) ||
		!reflect.DeepEqual(oldNetwork.Routes, newNetwork.Routes) ||
		oldNetwork.IP != newNetwork.IP ||
		oldNetwork.Subnet != newNetwork.Subnet ||
		oldNetwork.Gateway != newNetwork.Gateway ||
		oldNetwork.IPv6 != newNetwork.IPv6 ||
		oldNetwork.IPv6Subnet != newNetwork.IPv6Subnet ||
		oldNetwork.IPv6Gateway != newNetwork.IPv6Gateway {
		return false
	}
	if len(oldNetwork.PortForwards) != len(newNetwork.PortForwards) {
		return false
	}
	for i := range oldNetwork.PortForwards {
		oldForward := oldNetwork.PortForwards[i]
		newForward := newNetwork.PortForwards[i]
		if oldForward.Protocol != newForward.Protocol ||
			oldForward.HostPort != newForward.HostPort ||
			oldForward.GuestPort != newForward.GuestPort {
			return false
		}
	}
	return true
}

func writeWorkspaceManifestRecord(opts Options, manifest Manifest, trigger string) error {
	return writeManifestRecord(Options{
		StateDir: opts.StateDir, Name: manifest.Name, Purpose: firstNonEmpty(opts.Purpose, manifest.Purpose),
		CorrelationID: firstNonEmpty(opts.CorrelationID, manifest.CorrelationID),
	}, manifest, trigger)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
