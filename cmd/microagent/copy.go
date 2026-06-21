package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runCP(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	debugfsPath := defaultDebugFSPath()
	fs := flag.NewFlagSet("cp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: microagent cp <source> <target> [--state-dir <dir>]")
	}
	result, err := workspace.Copy(ctx, opts.StateDir, debugfsPath, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
}

func runArtifact(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) > 0 && args[0] == "get" {
		return runArtifactGet(ctx, args[1:], stdout)
	}
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("artifact", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent artifact <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	artifacts, err := workspace.ArtifactsFor(opts.StateDir, name)
	if err != nil {
		return err
	}
	result := artifactsResult{Workspace: name, Artifacts: artifacts}
	return writeArtifactsResult(stdout, result)
}

func runArtifactGet(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	debugfsPath := defaultDebugFSPath()
	fs := flag.NewFlagSet("artifact get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: microagent artifact get <name> <artifact> <target> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	result, err := workspace.GetArtifact(ctx, opts.StateDir, debugfsPath, name, fs.Arg(1), fs.Arg(2))
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
}

func copyWorkspaceFile(stateDir, debugfsPath, source, target string) (copyResult, error) {
	sourceRemote, sourceIsRemote, err := parseRemoteCopyEndpoint(source)
	if err != nil {
		return copyResult{}, err
	}
	targetRemote, targetIsRemote, err := parseRemoteCopyEndpoint(target)
	if err != nil {
		return copyResult{}, err
	}
	if sourceIsRemote == targetIsRemote {
		return copyResult{}, fmt.Errorf("exactly one cp endpoint must be workspace:path")
	}
	if sourceIsRemote {
		return copyFromWorkspace(stateDir, debugfsPath, sourceRemote, target)
	}
	return copyToWorkspace(stateDir, debugfsPath, source, targetRemote)
}

func getWorkspaceArtifact(stateDir, debugfsPath, name, artifactName, target string) (copyResult, error) {
	manifest, err := readWorkspaceManifest(stateDir, name)
	if err != nil {
		return copyResult{}, err
	}
	output, err := findWorkspaceOutput(manifest.Artifacts.Egress, artifactName)
	if err != nil {
		return copyResult{}, err
	}
	remote := outputRemoteEndpoint(name, output, manifest.Disks)
	if err := validateRemoteCopyPath(remote.Path); err != nil {
		return copyResult{}, err
	}
	result, err := copyFromWorkspace(stateDir, debugfsPath, remote, target)
	if err != nil {
		return copyResult{}, err
	}
	result.Artifact = output.Name
	return result, nil
}

func findWorkspaceOutput(outputs []workspaceOutput, name string) (workspaceOutput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return workspaceOutput{}, fmt.Errorf("artifact name is required")
	}
	for _, output := range outputs {
		if output.Name == name {
			return output, nil
		}
	}
	return workspaceOutput{}, fmt.Errorf("output artifact %q is not declared", name)
}

func outputRemoteEndpoint(workspace string, output workspaceOutput, disks []workspaceDisk) remoteCopyEndpoint {
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

type remoteCopyEndpoint struct {
	Workspace string
	Disk      string
	Path      string
	Raw       string
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
		if err := validateWorkspaceName(workspace); err != nil {
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
	if err := validateWorkspaceName(workspace); err != nil {
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

func copyFromWorkspace(stateDir, debugfsPath string, remote remoteCopyEndpoint, localTarget string) (copyResult, error) {
	if err := ensureWorkspaceCloneable(stateDir, remote.Workspace); err != nil {
		return copyResult{}, err
	}
	imagePath, err := workspaceImagePath(stateDir, remote)
	if err != nil {
		return copyResult{}, err
	}
	target, err := localCopyTarget(localTarget, filepath.Base(remote.Path))
	if err != nil {
		return copyResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return copyResult{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".microagent-cp-*")
	if err != nil {
		return copyResult{}, err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return copyResult{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := runDebugFS(debugfsPath, imagePath, false, "dump "+remote.Path+" "+tmpPath); err != nil {
		return copyResult{}, err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return copyResult{}, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return copyResult{}, err
	}
	cleanup = false
	return copyResult{
		Workspace: remote.Workspace,
		Disk:      remote.Disk,
		Direction: "from-workspace",
		Source:    remote.Raw,
		Target:    target,
		ImagePath: imagePath,
		Bytes:     info.Size(),
	}, nil
}

func copyToWorkspace(stateDir, debugfsPath, localSource string, remote remoteCopyEndpoint) (copyResult, error) {
	if err := ensureWorkspaceCloneable(stateDir, remote.Workspace); err != nil {
		return copyResult{}, err
	}
	info, err := os.Stat(localSource)
	if err != nil {
		return copyResult{}, err
	}
	if !info.Mode().IsRegular() {
		return copyResult{}, fmt.Errorf("source must be a regular file: %s", localSource)
	}
	if strings.ContainsAny(localSource, "\x00\n\r\t ") {
		return copyResult{}, fmt.Errorf("local source path must not contain whitespace")
	}
	imagePath, err := workspaceImagePath(stateDir, remote)
	if err != nil {
		return copyResult{}, err
	}
	if err := runDebugFS(debugfsPath, imagePath, true, "write "+localSource+" "+remote.Path); err != nil {
		return copyResult{}, err
	}
	return copyResult{
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
	manifest, err := readWorkspaceManifest(stateDir, remote.Workspace)
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
	args := []string{}
	if write {
		args = append(args, "-w")
	}
	args = append(args, "-R", command, imagePath)
	cmd := exec.Command(debugfsPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs %s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	if debugFSOutputFailed(output) {
		return fmt.Errorf("debugfs %s failed: %s", command, strings.TrimSpace(string(output)))
	}
	return nil
}

func debugFSOutputFailed(output []byte) bool {
	text := strings.ToLower(string(output))
	for _, marker := range []string{
		"file not found",
		"not found by ext2_lookup",
		"ext2fs_open2",
		"permission denied",
		"no such file",
		"usage:",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
