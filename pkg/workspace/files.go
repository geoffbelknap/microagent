package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

var e2fsckPath = "e2fsck"

type CopyResult struct {
	Artifact  string `json:"artifact,omitempty"`
	Workspace string `json:"workspace"`
	Disk      string `json:"disk"`
	Direction string `json:"direction"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	ImagePath string `json:"image_path"`
	Bytes     int64  `json:"bytes,omitempty"`
}

type NetworkStatus struct {
	Workspace string               `json:"workspace"`
	State     string               `json:"state,omitempty"`
	Backend   string               `json:"backend,omitempty"`
	Network   vmkit.NetworkConfig  `json:"network"`
	Runtime   *vmkit.NetworkConfig `json:"runtime,omitempty"`
}

type remoteCopyEndpoint struct {
	Workspace string
	Disk      string
	Path      string
	Raw       string
}

func ReadLogs(stateDir, name string) ([]byte, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	return os.ReadFile(SerialLogPath(stateDir, name))
}

// EventsPath is the per-workspace lifecycle event history file (a JSON array of
// EventFile, rewritten atomically on each state change).
func EventsPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "events.json")
}

// ReadEvents returns the recorded lifecycle events for a workspace, oldest
// first. A workspace with no event history yet (never started) returns an empty
// slice and no error.
func ReadEvents(stateDir, name string) ([]EventFile, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(EventsPath(stateDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var events []EventFile
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("workspace %s event history is malformed: %w", name, err)
	}
	return events, nil
}

func Network(stateDir, name string) (NetworkStatus, error) {
	if err := ValidateName(name); err != nil {
		return NetworkStatus{}, err
	}
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return NetworkStatus{}, err
	}
	result := NetworkStatus{
		Workspace: name,
		Network:   NetworkConfigFromSpec(manifest.Network),
	}
	if state, err := ReadRuntimeState(Options{StateDir: stateDir, Name: name}); err == nil {
		result.State = string(state.Event.State)
		result.Backend = state.Event.Identity.Backend
		if state.Config.Network != nil {
			runtimeNetwork := NormalizeNetworkConfig(*state.Config.Network)
			result.Runtime = &runtimeNetwork
		}
	} else if event, eventErr := ReadEvent(Options{StateDir: stateDir, Name: name}); eventErr == nil {
		result.State = string(event.State)
		result.Backend = event.Identity.Backend
	}
	return result, nil
}

func Copy(stateDir, debugfsPath, source, target string) (CopyResult, error) {
	sourceRemote, sourceIsRemote, err := parseRemoteCopyEndpoint(source)
	if err != nil {
		return CopyResult{}, err
	}
	targetRemote, targetIsRemote, err := parseRemoteCopyEndpoint(target)
	if err != nil {
		return CopyResult{}, err
	}
	if sourceIsRemote == targetIsRemote {
		return CopyResult{}, fmt.Errorf("exactly one cp endpoint must be workspace:path")
	}
	if sourceIsRemote {
		return copyFromWorkspace(stateDir, debugfsPath, sourceRemote, target)
	}
	return copyToWorkspace(stateDir, debugfsPath, source, targetRemote)
}

func GetArtifact(stateDir, debugfsPath, name, artifactName, target string) (CopyResult, error) {
	if err := ValidateName(name); err != nil {
		return CopyResult{}, err
	}
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return CopyResult{}, err
	}
	output, err := findOutput(manifest.Artifacts.Egress, artifactName)
	if err != nil {
		return CopyResult{}, err
	}
	remote := outputRemoteEndpoint(name, output, manifest.Disks)
	result, err := copyFromWorkspace(stateDir, debugfsPath, remote, target)
	if err != nil {
		return CopyResult{}, err
	}
	result.Artifact = output.Name
	return result, nil
}

func Clone(stateDir, source, target string) (Result, error) {
	if err := ValidateName(source); err != nil {
		return Result{}, err
	}
	if err := ValidateName(target); err != nil {
		return Result{}, err
	}
	sourceWorkspaceDir := filepath.Join(stateDir, "workspaces", source)
	targetWorkspaceDir := filepath.Join(stateDir, "workspaces", target)
	if _, err := os.Stat(sourceWorkspaceDir); err != nil {
		return Result{}, err
	}
	if _, err := os.Stat(targetWorkspaceDir); err == nil {
		return Result{}, fmt.Errorf("target workspace %q already exists", target)
	} else if !os.IsNotExist(err) {
		return Result{}, err
	}
	if _, err := os.Stat(filepath.Join(stateDir, target)); err == nil {
		return Result{}, fmt.Errorf("target workspace state %q already exists", target)
	} else if !os.IsNotExist(err) {
		return Result{}, err
	}
	if err := EnsureCloneable(stateDir, source); err != nil {
		return Result{}, err
	}
	manifest, err := ReadManifest(stateDir, source)
	if err != nil {
		return Result{}, err
	}
	if err := copyDirectory(sourceWorkspaceDir, targetWorkspaceDir); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return Result{}, err
	}
	manifest.Name = target
	manifest.Disks = rewriteClonedDiskPaths(manifest.Disks, sourceWorkspaceDir, targetWorkspaceDir)
	if err := writeJSONFile(filepath.Join(targetWorkspaceDir, "workspace.json"), manifest); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return Result{}, err
	}
	event := EventFile{
		Identity: vmkit.Identity{
			RequestID: NewRequestID(),
			RuntimeID: target,
			Role:      vmkit.RoleWorkload,
			Backend:   HostBackend(),
		},
		State:      vmkit.StatePrepared,
		Detail:     "cloned_from=" + source,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(filepath.Join(stateDir, target), 0o700); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		return Result{}, err
	}
	if err := writeJSONFile(filepath.Join(stateDir, target, "event.json"), event); err != nil {
		_ = os.RemoveAll(targetWorkspaceDir)
		_ = os.RemoveAll(filepath.Join(stateDir, target))
		return Result{}, err
	}
	return Result{
		Workspace:  target,
		StateDir:   stateDir,
		Profile:    manifest.Profile,
		Restart:    firstNonEmpty(manifest.Restart, DefaultRestartPolicy),
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

func EnsureCloneable(stateDir, name string) error {
	state, err := ReadRuntimeState(Options{StateDir: stateDir, Name: name})
	if os.IsNotExist(err) {
		event, eventErr := ReadEvent(Options{StateDir: stateDir, Name: name})
		if os.IsNotExist(eventErr) {
			return nil
		}
		if eventErr != nil {
			return eventErr
		}
		return cloneableState(name, event.State)
	}
	if err != nil {
		event, eventErr := ReadEvent(Options{StateDir: stateDir, Name: name})
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

func parseRemoteCopyEndpoint(raw string) (remoteCopyEndpoint, bool, error) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 {
		return remoteCopyEndpoint{}, false, nil
	}
	if len(parts) == 2 {
		workspace := strings.TrimSpace(parts[0])
		path := parts[1]
		if workspace == "" || !strings.HasPrefix(path, "/") {
			return remoteCopyEndpoint{}, false, nil
		}
		if err := ValidateName(workspace); err != nil {
			return remoteCopyEndpoint{}, true, err
		}
		if err := validateRemoteCopyPath(path); err != nil {
			return remoteCopyEndpoint{}, true, err
		}
		return remoteCopyEndpoint{Workspace: workspace, Disk: "rootfs", Path: path, Raw: raw}, true, nil
	}
	workspace := strings.TrimSpace(parts[0])
	disk := strings.TrimSpace(parts[1])
	path := parts[2]
	if workspace == "" || disk == "" || !strings.HasPrefix(path, "/") {
		return remoteCopyEndpoint{}, false, nil
	}
	if err := ValidateName(workspace); err != nil {
		return remoteCopyEndpoint{}, true, err
	}
	if err := validateDiskName(disk); err != nil {
		return remoteCopyEndpoint{}, true, err
	}
	if err := validateRemoteCopyPath(path); err != nil {
		return remoteCopyEndpoint{}, true, err
	}
	return remoteCopyEndpoint{Workspace: workspace, Disk: disk, Path: path, Raw: raw}, true, nil
}

func copyFromWorkspace(stateDir, debugfsPath string, remote remoteCopyEndpoint, localTarget string) (CopyResult, error) {
	if err := EnsureCloneable(stateDir, remote.Workspace); err != nil {
		return CopyResult{}, err
	}
	imagePath, err := workspaceImagePath(stateDir, remote)
	if err != nil {
		return CopyResult{}, err
	}
	if err := reconcileExt4Journal(imagePath); err != nil {
		return CopyResult{}, err
	}
	target, err := localCopyTarget(localTarget, path.Base(remote.Path))
	if err != nil {
		return CopyResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return CopyResult{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".microagent-cp-*")
	if err != nil {
		return CopyResult{}, err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return CopyResult{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := runDebugFS(debugfsPath, imagePath, false, "dump "+remote.Path+" "+tmpPath); err != nil {
		return CopyResult{}, err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return CopyResult{}, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return CopyResult{}, err
	}
	cleanup = false
	return CopyResult{
		Workspace: remote.Workspace,
		Disk:      remote.Disk,
		Direction: "from-workspace",
		Source:    remote.Raw,
		Target:    target,
		ImagePath: imagePath,
		Bytes:     info.Size(),
	}, nil
}

func copyToWorkspace(stateDir, debugfsPath, localSource string, remote remoteCopyEndpoint) (CopyResult, error) {
	if err := EnsureCloneable(stateDir, remote.Workspace); err != nil {
		return CopyResult{}, err
	}
	info, err := os.Stat(localSource)
	if err != nil {
		return CopyResult{}, err
	}
	if !info.Mode().IsRegular() {
		return CopyResult{}, fmt.Errorf("source must be a regular file: %s", localSource)
	}
	if strings.ContainsAny(localSource, "\x00\n\r\t ") {
		return CopyResult{}, fmt.Errorf("local source path must not contain whitespace")
	}
	if err := ensureRemoteWritable(stateDir, remote); err != nil {
		return CopyResult{}, err
	}
	imagePath, err := workspaceImagePath(stateDir, remote)
	if err != nil {
		return CopyResult{}, err
	}
	if err := reconcileExt4Journal(imagePath); err != nil {
		return CopyResult{}, err
	}
	if err := ensureDebugFSParentDir(debugfsPath, imagePath, remote.Path); err != nil {
		return CopyResult{}, err
	}
	if err := removeDebugFSFileIfExists(debugfsPath, imagePath, remote.Path); err != nil {
		return CopyResult{}, err
	}
	if err := runDebugFS(debugfsPath, imagePath, true, "write "+localSource+" "+remote.Path); err != nil {
		return CopyResult{}, err
	}
	return CopyResult{
		Workspace: remote.Workspace,
		Disk:      remote.Disk,
		Direction: "to-workspace",
		Source:    localSource,
		Target:    remote.Raw,
		ImagePath: imagePath,
		Bytes:     info.Size(),
	}, nil
}

func workspaceImagePath(stateDir string, remote remoteCopyEndpoint) (string, error) {
	if remote.Disk == "" || remote.Disk == "rootfs" {
		path := filepath.Join(stateDir, "workspaces", remote.Workspace, "rootfs.ext4")
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return path, nil
	}
	manifest, err := ReadManifest(stateDir, remote.Workspace)
	if err != nil {
		return "", err
	}
	for _, disk := range manifest.Disks {
		if disk.Name == remote.Disk {
			if _, err := os.Stat(disk.Path); err != nil {
				return "", err
			}
			return disk.Path, nil
		}
	}
	return "", fmt.Errorf("workspace %s has no disk %q", remote.Workspace, remote.Disk)
}

func ensureRemoteWritable(stateDir string, remote remoteCopyEndpoint) error {
	if remote.Disk == "" || remote.Disk == "rootfs" {
		return nil
	}
	manifest, err := ReadManifest(stateDir, remote.Workspace)
	if err != nil {
		return err
	}
	for _, disk := range manifest.Disks {
		if disk.Name == remote.Disk {
			if disk.Mode == "ro" {
				return fmt.Errorf("workspace %s disk %q is read-only", remote.Workspace, remote.Disk)
			}
			return nil
		}
	}
	return fmt.Errorf("workspace %s has no disk %q", remote.Workspace, remote.Disk)
}

func ensureDebugFSParentDir(debugfsPath, imagePath, target string) error {
	parent := path.Dir(target)
	if parent == "." || parent == "/" {
		return nil
	}
	output, err := runDebugFSOutput(debugfsPath, imagePath, false, "stat "+parent)
	if err != nil {
		return fmt.Errorf("workspace path parent %s does not exist in the image", parent)
	}
	if !strings.Contains(output, "Type: directory") {
		return fmt.Errorf("workspace path parent %s exists but is not a directory", parent)
	}
	return nil
}

func removeDebugFSFileIfExists(debugfsPath, imagePath, target string) error {
	output, err := runDebugFSOutput(debugfsPath, imagePath, true, "rm "+target)
	if err == nil {
		return nil
	}
	if strings.Contains(output, "File not found") {
		return nil
	}
	return err
}

func outputRemoteEndpoint(workspace string, output Output, disks []Disk) remoteCopyEndpoint {
	disk := "rootfs"
	path := output.Path
	longestMount := ""
	for _, candidate := range disks {
		mount := strings.TrimRight(candidate.Mountpoint, "/")
		if mount == "" {
			continue
		}
		if output.Path == mount || strings.HasPrefix(output.Path, mount+"/") {
			if len(mount) > len(longestMount) {
				longestMount = mount
				disk = candidate.Name
				path = strings.TrimPrefix(output.Path, mount)
				if path == "" {
					path = "/"
				}
			}
		}
	}
	if path == "" || !strings.HasPrefix(path, "/") {
		path = "/" + strings.TrimLeft(path, "/")
	}
	raw := workspace + ":" + path
	if disk != "rootfs" {
		raw = workspace + ":" + disk + ":" + path
	}
	return remoteCopyEndpoint{Workspace: workspace, Disk: disk, Path: path, Raw: raw}
}

func findOutput(outputs []Output, name string) (Output, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Output{}, fmt.Errorf("artifact name is required")
	}
	for _, output := range outputs {
		if output.Name == name {
			return output, nil
		}
	}
	return Output{}, fmt.Errorf("output artifact %q is not declared", name)
}

func validateRemoteCopyPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("workspace path must be absolute: %s", path)
	}
	if path == "/" || strings.HasSuffix(path, "/") {
		return fmt.Errorf("workspace path must name a file: %s", path)
	}
	if strings.ContainsAny(path, "\x00\n\r") {
		return fmt.Errorf("workspace path contains unsupported characters")
	}
	if strings.Contains(path, " ") || strings.Contains(path, "\t") {
		return fmt.Errorf("workspace path must not contain whitespace")
	}
	return nil
}

func validateDiskName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("disk name is required")
	}
	if strings.ContainsAny(name, `/\:`) || name == "." || name == ".." {
		return fmt.Errorf("invalid disk name: %s", name)
	}
	return nil
}

func localCopyTarget(target, fallbackName string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("local target is required")
	}
	if strings.ContainsAny(target, "\x00\n\r") {
		return "", fmt.Errorf("local target path contains unsupported characters")
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return filepath.Join(target, fallbackName), nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return target, nil
}

func runDebugFS(debugfsPath, imagePath string, write bool, command string) error {
	_, err := runDebugFSOutput(debugfsPath, imagePath, write, command)
	return err
}

func reconcileExt4Journal(imagePath string) error {
	ext, err := hasExtSuperblock(imagePath)
	if err != nil {
		return err
	}
	if !ext {
		return nil
	}
	cmd := exec.Command(e2fsckPath, "-fy", imagePath)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err == nil {
		return nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return fmt.Errorf("e2fsck %s: %w: %s", imagePath, err, text)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return fmt.Errorf("e2fsck %s: %w: %s", imagePath, err, text)
	}
	// e2fsck uses bit flags. Bits 1 and 2 mean the filesystem was corrected;
	// higher bits indicate uncorrected errors or operational failures.
	if status.ExitStatus()&^3 == 0 {
		return nil
	}
	return fmt.Errorf("e2fsck %s: %w: %s", imagePath, err, text)
}

func hasExtSuperblock(imagePath string) (bool, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	magic := []byte{0, 0}
	n, err := file.ReadAt(magic, 1080)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}
	if n != len(magic) {
		return false, nil
	}
	return magic[0] == 0x53 && magic[1] == 0xef, nil
}

func runDebugFSOutput(debugfsPath, imagePath string, write bool, command string) (string, error) {
	args := []string{}
	if write {
		args = append(args, "-w")
	}
	args = append(args, "-R", command, imagePath)
	cmd := exec.Command(debugfsPath, args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("debugfs %s: %w: %s", command, err, text)
	}
	if debugFSOutputFailed(text) {
		return text, fmt.Errorf("debugfs %s: %s", command, text)
	}
	return text, nil
}

func debugFSOutputFailed(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "debugfs ") || line == "" {
			continue
		}
		if strings.Contains(line, "File not found") ||
			strings.Contains(line, "Filesystem not open") ||
			strings.Contains(line, "Ext2 file already exists") ||
			strings.Contains(line, "while trying to open") ||
			strings.Contains(line, "Could not allocate block") ||
			strings.Contains(line, "Could not allocate inode") {
			return true
		}
	}
	return false
}

func cloneableState(name string, state vmkit.VMState) error {
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped:
		return nil
	default:
		return fmt.Errorf("workspace %s must be stopped before cloning; current state is %s", name, state)
	}
}

func rewriteClonedDiskPaths(disks []Disk, sourceWorkspaceDir, targetWorkspaceDir string) []Disk {
	if len(disks) == 0 {
		return nil
	}
	out := make([]Disk, 0, len(disks))
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
		return CopyFile(path, targetPath, info.Mode().Perm())
	})
}
