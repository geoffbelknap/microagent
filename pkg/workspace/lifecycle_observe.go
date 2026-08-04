package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func Inspect(ctx context.Context, opts Options) (vmkit.Response, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.Response{}, err
	}
	req, err := Request(opts, "inspect", "", NewRequestID())
	if err != nil {
		return vmkit.Response{}, err
	}
	resp, err := Dispatch(ctx, opts, req)
	if resp.EgressCapture == nil {
		networkMode := opts.Network.Mode
		if req.Config != nil && req.Config.Network != nil {
			networkMode = req.Config.Network.Mode
		}
		report := vmkit.NegotiateEgressCapture(opts.Backend, networkMode, opts.EgressMode)
		resp.EgressCapture = &report
	}
	if resp.RootfsUsage == nil {
		resp.RootfsUsage = rootfsUsage(opts)
	}
	if resp.BoundedOperations == nil {
		resp.BoundedOperations = boundedOperationsStatus(opts)
	}
	if history, historyErr := constraintHistoryStatus(opts.StateDir, opts.Name); historyErr == nil {
		resp.ConstraintHistory = history
	} else if err == nil {
		resp.OK = false
		resp.Error = historyErr.Error()
		err = historyErr
	}
	return resp, err
}

func Status(opts Options) (vmkit.Response, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.Response{}, err
	}
	state, err := ReadRuntimeState(opts)
	if err == nil {
		return responseFromEvent(opts, state.Event, state.Error), nil
	}
	event, eventErr := ReadEvent(opts)
	if eventErr != nil {
		// No runtime state and no event file means the workspace does not
		// exist; report that instead of the raw file-open error. Corrupt
		// state (unreadable or malformed files) still surfaces as-is.
		if os.IsNotExist(err) && os.IsNotExist(eventErr) {
			return vmkit.Response{}, WorkspaceNotFoundError{Name: opts.Name}
		}
		return vmkit.Response{}, err
	}
	return responseFromEvent(opts, event, ""), nil
}

func ResultStatus(opts Options) (vmkit.Response, error) {
	resp, err := Status(opts)
	if err != nil {
		return resp, err
	}
	if resp.Event == nil {
		err := fmt.Errorf("workspace %s has no state event", opts.Name)
		resp.OK = false
		resp.Error = err.Error()
		return resp, err
	}
	result, resultErr := ReadRuntimeResult(opts, resp.Event.Identity)
	if resultErr != nil {
		err := fmt.Errorf("workspace %s result is not ready: %w", opts.Name, resultErr)
		resp.OK = false
		resp.Error = err.Error()
		return resp, err
	}
	resp.Result = &result
	return resp, nil
}

func ArtifactsFor(stateDir, name string) (vmkit.RuntimeArtifacts, error) {
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return vmkit.RuntimeArtifacts{}, err
	}
	return RuntimeArtifacts(manifest.Artifacts), nil
}

func List(stateDir string) ([]ListEntry, error) {
	names := map[string]bool{}
	workspaceRoot := filepath.Join(stateDir, "workspaces")
	if entries, err := os.ReadDir(workspaceRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				names[entry.Name()] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if entries, err := os.ReadDir(stateDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == "build" || entry.Name() == "workspaces" {
				continue
			}
			name := entry.Name()
			if _, err := os.Stat(filepath.Join(stateDir, name, "event.json")); err != nil {
				continue
			}
			if names[name] {
				continue
			}
			event, err := ReadEvent(Options{StateDir: stateDir, Name: name})
			if err != nil {
				continue
			}
			if isLiveState(event.State) {
				names[name] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	sortedNames := make([]string, 0, len(names))
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	out := make([]ListEntry, 0, len(sortedNames))
	for _, name := range sortedNames {
		entry := ListEntry{Name: name, State: string(vmkit.StateUnknown)}
		if manifest, err := ReadManifest(stateDir, name); err == nil {
			entry.Profile = manifest.Profile
			entry.Restart = manifest.Restart
			entry.Network = manifest.Network.Mode
		}
		if event, err := ReadEvent(Options{StateDir: stateDir, Name: name}); err == nil {
			entry.State = string(event.State)
			entry.Backend = event.Identity.Backend
			entry.ObservedAt = event.ObservedAt
		}
		for _, rootfsPath := range CandidateWorkspaceRootfsPaths(stateDir, name, entry.Backend) {
			if _, err := os.Stat(rootfsPath); err == nil {
				entry.RootfsPath = rootfsPath
				break
			}
		}
		serialPath := SerialLogPath(stateDir, name)
		if _, err := os.Stat(serialPath); err == nil {
			entry.SerialPath = serialPath
		}
		out = append(out, entry)
	}
	return out, nil
}

func isLiveState(state vmkit.VMState) bool {
	switch state {
	case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined, vmkit.StateStopping:
		return true
	default:
		return false
	}
}
