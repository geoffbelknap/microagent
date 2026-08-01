package workspace

import (
	"path/filepath"

	"github.com/geoffbelknap/microagent/internal/ext4fs"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// ResizeOptions configures a workspace rootfs resize.
type ResizeOptions struct {
	StateDir string
	Name     string
	Backend  string
	// SizeMiB is the target rootfs size. Must be greater than 0.
	SizeMiB int64
	// Resize2fsPath is the resize2fs binary to shell out to, resolved by the
	// caller (workspace.Resize2fsPath() on the CLI/MCP path) the same way
	// Options.Mke2fsPath is resolved for rootfs builds.
	Resize2fsPath string
}

// ResizeResult reports the outcome of a workspace rootfs resize.
type ResizeResult struct {
	Workspace   string           `json:"workspace"`
	FromSizeMiB int64            `json:"from_size_mib"`
	ToSizeMiB   int64            `json:"to_size_mib"`
	Usage       *vmkit.DiskUsage `json:"usage,omitempty"`
}

// Resize grows or shrinks a stopped workspace's rootfs disk. It is host-side
// and offline only: the workspace must not be running, starting, paused, or
// quarantined, and must have no snapshots — a snapshot's machine state
// captures device geometry, and a restore would replace the resized rootfs
// with the snapshot's own anyway. On success the manifest records the new
// size, clears SizeDerived (an explicit resize is no longer a derived size),
// and the recorded rootfs verification hash is refreshed so a later
// inspect/status does not compare the live disk against a stale hash.
func Resize(opts ResizeOptions) (ResizeResult, error) {
	if err := ValidateName(opts.Name); err != nil {
		return ResizeResult{}, err
	}
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = StateDir()
	}
	if opts.SizeMiB <= 0 {
		return ResizeResult{}, operation.New(operation.ErrorValidation, "resize target size must be greater than 0 MiB")
	}
	if err := ensureCanResize(stateDir, opts.Name); err != nil {
		return ResizeResult{}, err
	}
	snapshots, err := SnapshotList(Options{StateDir: stateDir, Name: opts.Name})
	if err != nil {
		return ResizeResult{}, err
	}
	if len(snapshots) > 0 {
		return ResizeResult{}, operation.New(operation.ErrorConflict, "workspace %s has %d snapshot(s); delete them before resizing the rootfs (a restore would replace the resized disk with the snapshot's own geometry anyway)", opts.Name, len(snapshots))
	}

	manifest, err := ReadManifest(stateDir, opts.Name)
	if err != nil {
		return ResizeResult{}, err
	}
	fromSizeMiB := manifest.Resources.SizeMiB

	rootfsPath := WorkspaceRootfsPath(stateDir, opts.Name, opts.Backend)
	targetBytes := opts.SizeMiB * 1024 * 1024
	if err := ext4fs.Resize(e2fsckPath, opts.Resize2fsPath, rootfsPath, targetBytes); err != nil {
		return ResizeResult{}, err
	}

	manifest.Resources.SizeMiB = opts.SizeMiB
	manifest.SizeDerived = false
	if manifest.Verification != nil {
		manifest.Verification.Rootfs = recordedArtifact(rootfsPath)
	}
	if err := writeJSONFile(filepath.Join(stateDir, "workspaces", opts.Name, "workspace.json"), manifest); err != nil {
		return ResizeResult{}, err
	}

	return ResizeResult{
		Workspace:   opts.Name,
		FromSizeMiB: fromSizeMiB,
		ToSizeMiB:   opts.SizeMiB,
		Usage:       rootfsUsage(Options{StateDir: stateDir, Name: opts.Name, Backend: opts.Backend}),
	}, nil
}

// ensureCanResize mirrors EnsureCanStart's state gate: resizing needs the
// rootfs closed, the same precondition as starting. Paused is refused
// explicitly (falls to default below) rather than inheriting EnsureCanStart's
// incidental treatment of it — a paused backend may still have the disk open.
func ensureCanResize(stateDir, name string) error {
	state, _, err := LatestStartState(stateDir, name)
	if err != nil {
		return err
	}
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed:
		return nil
	default:
		return operation.New(operation.ErrorConflict, "workspace %s cannot be resized from state %s; halt or stop it first", name, state)
	}
}
