package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

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
		return ApplyResult{}, fmt.Errorf("apply spec requires name")
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
	state, _, err := LatestStartState(opts.StateDir, name)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(applied) == 0 {
		return ApplyResult{Workspace: name, State: string(state), Network: next.Network}, nil
	}
	if state == vmkit.StateRunning && containsString(applied, "network") {
		if !vmkit.BackendCapabilities(opts.Backend).LiveNetworkApply {
			return ApplyResult{}, fmt.Errorf("the %s backend does not support live network apply; stop and start %s to apply this change", opts.Backend, name)
		}
		oldNetwork := NetworkConfigFromSpec(manifest.Network)
		newNetwork := NetworkConfigFromSpec(next.Network)
		if !LivePortForwardHostOnlyChange(oldNetwork, newNetwork) {
			return ApplyResult{}, fmt.Errorf("live network apply only supports host bind changes for existing port forwards; stop and start %s to apply this change", name)
		}
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
			err = writeWorkspaceManifestRecord(opts.StateDir, name, next)
		}
		return result, err
	}
	if err := writeWorkspaceManifestRecord(opts.StateDir, name, next); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

func OptionsFromManifest(base Options, manifest Manifest) Options {
	opts := base
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
	// Normalize the egress mode loaded from the manifest so an unspecified mode
	// carries the explicit secure default ("mediated") into Request(), which
	// then provisions the mediator and the CA-cert vsock listener.
	opts.EgressMode = vmkit.NormalizeEgressMode(manifest.EgressMode)
	opts.EgressAllow = manifest.EgressAllow
	opts.EgressPassthrough = manifest.EgressPassthrough
	opts.EgressSwapConfigPath = manifest.EgressSwapConfigPath
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
		oldNetwork.Interface != newNetwork.Interface ||
		!reflect.DeepEqual(oldNetwork.DNS, newNetwork.DNS) ||
		!reflect.DeepEqual(oldNetwork.Routes, newNetwork.Routes) ||
		oldNetwork.IP != newNetwork.IP ||
		oldNetwork.Subnet != newNetwork.Subnet ||
		oldNetwork.Gateway != newNetwork.Gateway {
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

func writeWorkspaceManifestRecord(stateDir, name string, manifest Manifest) error {
	return writeJSONFile(filepath.Join(stateDir, "workspaces", name, "workspace.json"), manifest)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
