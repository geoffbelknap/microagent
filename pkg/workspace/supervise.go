package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
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
			if !ShouldRestart(policy, vmkit.StateFailed) {
				result.Stopped = true
				return result, err
			}
			result.Restarts++
			if opts.MaxRestarts > 0 && result.Restarts >= opts.MaxRestarts {
				result.Stopped = true
				return result, nil
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

func WaitForSupervised(ctx context.Context, opts Options, interval time.Duration) (vmkit.VMState, error) {
	for {
		resp, err := Inspect(ctx, opts)
		if err != nil {
			if resp.Event != nil {
				return resp.Event.State, err
			}
			return vmkit.StateUnknown, err
		}
		if resp.Event != nil {
			switch resp.Event.State {
			case vmkit.StateHalted, vmkit.StateQuarantined, vmkit.StateStopped, vmkit.StateFailed:
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
	if manifest.Network.Mode != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" {
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
	if err := ValidateRestartPolicy(workspaceOpts.RestartPolicy); err != nil {
		return Options{}, err
	}
	rootfsPath := filepath.Join(opts.StateDir, "workspaces", opts.Name, "rootfs.ext4")
	if _, err := os.Stat(rootfsPath); err != nil {
		return Options{}, err
	}
	return workspaceOpts, nil
}
