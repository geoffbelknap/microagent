package workspace

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type SuperviseOptions struct {
	StateDir       string
	SupervisorPath string
	Backend        string
	Architecture   string
	KernelPath     string
	KernelExplicit bool
	Name           string
	Interval       time.Duration
	MaxRestarts    int
}

type SuperviseResult struct {
	Workspace  string `json:"workspace"`
	Policy     string `json:"policy"`
	Restarts   int    `json:"restarts"`
	FinalState string `json:"final_state,omitempty"`
	Stopped    bool   `json:"stopped"`
}

func Supervise(ctx context.Context, opts SuperviseOptions) (SuperviseResult, error) {
	workspaceOpts, err := supervisedOptions(opts)
	if err != nil {
		return SuperviseResult{}, err
	}
	policy := NormalizeRestartPolicy(workspaceOpts.RestartPolicy)
	if policy == "never" {
		return SuperviseResult{Workspace: opts.Name, Policy: policy, Stopped: true}, nil
	}
	result := SuperviseResult{Workspace: opts.Name, Policy: policy}
	for {
		startResult, err := Start(ctx, workspaceOpts)
		if err != nil {
			result.FinalState = string(vmkit.StateFailed)
			writeSuperviseStartFailure(workspaceOpts, err)
			if !ShouldRestart(policy, vmkit.StateFailed) {
				result.Stopped = true
				return result, err
			}
			result.Restarts++
			if opts.MaxRestarts > 0 && result.Restarts >= opts.MaxRestarts {
				result.Stopped = true
				return result, nil
			}
			select {
			case <-ctx.Done():
				result.Stopped = true
				return result, ctx.Err()
			case <-time.After(opts.Interval):
			}
			continue
		} else if startResult.Response.Event != nil {
			result.FinalState = string(startResult.Response.Event.State)
		}
		state, waitErr := WaitForSupervised(ctx, workspaceOpts, opts.Interval)
		result.FinalState = string(state)
		if waitErr != nil {
			result.Stopped = true
			return result, waitErr
		}
		if !ShouldRestart(policy, state) {
			result.Stopped = true
			return result, nil
		}
		result.Restarts++
		if opts.MaxRestarts > 0 && result.Restarts >= opts.MaxRestarts {
			result.Stopped = true
			return result, nil
		}
	}
}

func writeSuperviseStartFailure(opts Options, startErr error) {
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)
	req := Request(opts, "run", rootfsPath, NewRequestID())
	_ = WriteProcessState(opts, req, vmkit.StateFailed, 0, startErr.Error())
}

func WaitForSupervised(ctx context.Context, opts Options, interval time.Duration) (vmkit.VMState, error) {
	for {
		resp, err := Inspect(ctx, opts)
		if err != nil {
			if resp.Event != nil {
				if isSupervisedTerminalState(resp.Event.State) {
					return resp.Event.State, nil
				}
				return resp.Event.State, err
			}
			return vmkit.StateUnknown, err
		}
		if resp.Event != nil {
			if isSupervisedTerminalState(resp.Event.State) {
				return resp.Event.State, nil
			}
		}
		select {
		case <-ctx.Done():
			return vmkit.StateUnknown, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func isSupervisedTerminalState(state vmkit.VMState) bool {
	switch state {
	case vmkit.StateHalted, vmkit.StateQuarantined, vmkit.StateStopped, vmkit.StateFailed:
		return true
	default:
		return false
	}
}

func ShouldRestart(policy string, state vmkit.VMState) bool {
	switch NormalizeRestartPolicy(policy) {
	case "always":
		return state == vmkit.StateHalted || state == vmkit.StateStopped || state == vmkit.StateFailed
	case "on-failure":
		return state == vmkit.StateFailed
	default:
		return false
	}
}

func supervisedOptions(opts SuperviseOptions) (Options, error) {
	if opts.Name == "" {
		return Options{}, fmt.Errorf("supervise requires a name")
	}
	if opts.StateDir == "" {
		opts.StateDir = StateDir()
	}
	if opts.Backend == "" {
		opts.Backend = HostBackend()
	}
	if err := ValidateHostBackend(opts.Backend); err != nil {
		return Options{}, err
	}
	if opts.Architecture == "" {
		opts.Architecture = GuestArch()
	}
	if opts.KernelPath == "" {
		opts.KernelPath = KernelPath(opts.Backend, opts.Architecture)
	}
	if opts.Interval == 0 {
		opts.Interval = time.Second
	}
	workspaceOpts := Options{
		Name:           opts.Name,
		Backend:        opts.Backend,
		Architecture:   opts.Architecture,
		KernelPath:     opts.KernelPath,
		KernelExplicit: opts.KernelExplicit,
		StateDir:       opts.StateDir,
		SupervisorPath: opts.SupervisorPath,
		Profile:        DefaultWorkspaceProfile,
		RestartPolicy:  DefaultRestartPolicy,
		Network:        vmkit.NetworkConfig{Mode: DefaultNetworkMode},
		MemoryMiB:      DefaultWorkspaceMemoryMiB,
		CPUCount:       DefaultWorkspaceCPUCount,
		SerialInput:    BackendSupportsConsoleInput(opts.Backend),
	}
	manifest, err := ReadManifest(opts.StateDir, opts.Name)
	if err != nil {
		return Options{}, err
	}
	if manifest.Profile != "" {
		workspaceOpts.Profile = manifest.Profile
	}
	workspaceOpts.RestartPolicy = NormalizeRestartPolicy(manifest.Restart)
	if manifest.Network.Mode != "" || manifest.Network.Interface != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" || manifest.Network.Subnet != "" || manifest.Network.Gateway != "" {
		workspaceOpts.Network = NetworkConfigFromSpec(manifest.Network)
	}
	if manifest.Resources.MemoryMiB != 0 {
		workspaceOpts.MemoryMiB = manifest.Resources.MemoryMiB
	}
	if manifest.Resources.CPUCount != 0 {
		workspaceOpts.CPUCount = manifest.Resources.CPUCount
	}
	if manifest.Resources.SizeMiB != 0 {
		workspaceOpts.SizeMiB = manifest.Resources.SizeMiB
	}
	workspaceOpts.Disks = manifest.Disks
	workspaceOpts.Mediation = manifest.Mediation
	if err := ValidateRestartPolicy(workspaceOpts.RestartPolicy); err != nil {
		return Options{}, err
	}
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)
	if _, err := os.Stat(rootfsPath); err != nil {
		return Options{}, err
	}
	return workspaceOpts, nil
}
