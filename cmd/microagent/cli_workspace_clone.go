package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func cloneWorkspace(stateDir, source, target string) (workspaceResult, error) {
	sourceWorkspaceDir := filepath.Join(stateDir, "workspaces", source)
	targetWorkspaceDir := filepath.Join(stateDir, "workspaces", target)
	if _, err := os.Stat(sourceWorkspaceDir); err != nil {
		return workspaceResult{}, err
	}
	if _, err := os.Stat(targetWorkspaceDir); err == nil {
		return workspaceResult{}, fmt.Errorf("target workspace %q already exists", target)
	} else if !os.IsNotExist(err) {
		return workspaceResult{}, err
	}
	if _, err := os.Stat(filepath.Join(stateDir, target)); err == nil {
		return workspaceResult{}, fmt.Errorf("target workspace state %q already exists", target)
	} else if !os.IsNotExist(err) {
		return workspaceResult{}, err
	}
	if err := ensureWorkspaceCloneable(stateDir, source); err != nil {
		return workspaceResult{}, err
	}
	manifest, err := readWorkspaceManifest(stateDir, source)
	if err != nil {
		return workspaceResult{}, err
	}
	if err := copyDirectory(sourceWorkspaceDir, targetWorkspaceDir); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return workspaceResult{}, err
	}
	manifest.Name = target
	manifest.Disks = rewriteClonedDiskPaths(manifest.Disks, sourceWorkspaceDir, targetWorkspaceDir)
	if err := writeJSONFile(filepath.Join(targetWorkspaceDir, "workspace.json"), manifest); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return workspaceResult{}, err
	}
	event := workspaceEventFile{
		Identity: vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: target,
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		State:      vmkit.StatePrepared,
		Detail:     "cloned_from=" + source,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(filepath.Join(stateDir, target), 0o700); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return workspaceResult{}, err
	}
	if err := writeJSONFile(filepath.Join(stateDir, target, "event.json"), event); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		_ = os.RemoveAll(filepath.Join(stateDir, target))
		return workspaceResult{}, err
	}
	return workspaceResult{
		Workspace:  target,
		StateDir:   stateDir,
		Profile:    manifest.Profile,
		Restart:    nonEmpty(manifest.Restart, defaultRestartPolicy),
		Resources:  manifest.Resources,
		Network:    manifest.Network,
		RootfsPath: filepath.Join(targetWorkspaceDir, "rootfs.ext4"),
		Disks:      manifest.Disks,
		Response: vmkit.Response{
			OK:      true,
			Backend: event.Identity.Backend,
			Event: &vmkit.Event{
				Identity:   event.Identity,
				State:      event.State,
				Detail:     event.Detail,
				ObservedAt: time.Now().UTC(),
			},
		},
	}, nil
}

func ensureWorkspaceCloneable(stateDir, name string) error {
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: stateDir, Name: name})
	if os.IsNotExist(err) {
		event, eventErr := readWorkspaceEvent(workspaceOptions{StateDir: stateDir, Name: name})
		if os.IsNotExist(eventErr) {
			return nil
		}
		if eventErr != nil {
			return eventErr
		}
		return cloneableState(name, event.State)
	}
	if err != nil {
		event, eventErr := readWorkspaceEvent(workspaceOptions{StateDir: stateDir, Name: name})
		if os.IsNotExist(eventErr) {
			return err
		}
		if eventErr != nil {
			return err
		}
		return cloneableState(name, event.State)
	}
	return cloneableState(name, state.Event.State)
}

func cloneableState(name string, state vmkit.VMState) error {
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped:
		return nil
	default:
		return fmt.Errorf("workspace %s must be stopped before cloning; current state is %s", name, state)
	}
}

func rewriteClonedDiskPaths(disks []workspaceDisk, sourceWorkspaceDir, targetWorkspaceDir string) []workspaceDisk {
	if len(disks) == 0 {
		return nil
	}
	out := make([]workspaceDisk, 0, len(disks))
	sourceWorkspaceDir = filepath.Clean(sourceWorkspaceDir)
	for _, disk := range disks {
		if rel, err := filepath.Rel(sourceWorkspaceDir, disk.Path); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			disk.Path = filepath.Join(targetWorkspaceDir, rel)
		}
		out = append(out, disk)
	}
	return out
}

func copyDirectory(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		if info.Mode()&os.ModeType != 0 {
			return fmt.Errorf("cannot clone special file %s", path)
		}
		return copyFile(path, targetPath, info.Mode().Perm())
	})
}

func copyFile(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// validateWorkspaceName is the CLI adapter over the library's one name rule;
// keeping it a delegation means the CLI can never accept a name the library
// would refuse (or vice versa).
func validateWorkspaceName(name string) error {
	return workspace.ValidateName(name)
}

func validateSafeBasename(field, value string) error {
	if !vmkit.SafeIdentifier(value) {
		return fmt.Errorf("invalid %s: %s", field, value)
	}
	return nil
}
