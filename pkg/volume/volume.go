// Package volume manages user-defined named volumes: VM-independent ext4 disks
// that workspaces attach by name so data persists across a workspace's
// lifecycle. This package owns the registry and the backing ext4 files only — a
// backend-neutral data model persisted under the state directory.
//
// A named volume is single-attach: at most one workspace holds it at a time.
// That keeps it the microVM analog of a disk rather than a daemon-managed,
// concurrently-shared container volume — two running VMs must never mount the
// same ext4 read-write. A holder that is no longer running is reclaimed, so a
// crashed workspace never wedges a volume permanently.
package volume

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const (
	// DefaultSizeMiB is the size of a named volume when none is requested.
	DefaultSizeMiB = 1024
	minSizeMiB     = 1
	// maxSizeMiB is a sanity ceiling (1 TiB), not a quota.
	maxSizeMiB = 1 << 20
)

// Record is one named volume.
type Record struct {
	Name       string `json:"name"`
	SizeMiB    int64  `json:"size_mib"`
	CreatedAt  string `json:"created_at,omitempty"`
	AttachedTo string `json:"attached_to,omitempty"` // workspace currently holding it
}

// Index is the persisted set of named volumes.
type Index struct {
	Volumes []Record `json:"volumes"`
}

// formatExt4 creates an empty ext4 filesystem of sizeMiB at path. It is a
// package var so tests can substitute a fixture that does not need mke2fs.
var formatExt4 = mke2fsFormat

// formatVHD creates an empty ext4 filesystem wrapped in a VHD footer of sizeMiB
// at path, for VHD-lane backends whose hosts lack mke2fs. It is a package var so
// tests can substitute a fixture that does not need the in-process VHD builder.
var formatVHD = func(ctx context.Context, path string, sizeMiB int64) error {
	return rootfs.BuildEmptyVolume(ctx, path, sizeMiB*1024*1024)
}

// IndexPath returns the registry file path for a state directory.
func IndexPath(stateDir string) string {
	return filepath.Join(stateDir, "volumes", "index.json")
}

// backendUsesVHD reports whether a backend's named volumes are VHD-wrapped ext4
// rather than bare ext4. The empty backend (callers with no backend context,
// e.g. the registry-only CLI paths) uses ext4, matching the historical layout.
func backendUsesVHD(backend string) bool {
	return vmkit.BackendCapabilities(backend).VHDRootfs
}

// diskExtension returns the backing-file extension for a backend's volumes.
func diskExtension(backend string) string {
	if backendUsesVHD(backend) {
		return ".vhd"
	}
	return ".ext4"
}

// DiskPath returns the backing-image path for a named volume on backend. On
// VHD-lane backends (windows-hyperv) the image is an ext4 filesystem wrapped in
// a VHD footer (<name>.vhd); every other backend uses bare ext4 (<name>.ext4).
// Names are constrained by ValidName, so the join never escapes the volumes
// directory.
func DiskPath(stateDir, backend, name string) string {
	return filepath.Join(stateDir, "volumes", name+diskExtension(backend))
}

// ReadIndex loads the registry, returning an empty Index when none exists.
func ReadIndex(stateDir string) (Index, error) {
	data, err := os.ReadFile(IndexPath(stateDir))
	if os.IsNotExist(err) {
		return Index{}, nil
	}
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}

// WriteIndex persists the registry.
func WriteIndex(stateDir string, idx Index) error {
	if err := os.MkdirAll(filepath.Dir(IndexPath(stateDir)), 0o700); err != nil {
		return err
	}
	sortRecords(idx.Volumes)
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(IndexPath(stateDir), data, 0o600)
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
}

// ValidName reports whether name is a usable volume name: a DNS-label-like
// token (lowercase letters, digits, hyphens; must start and end alphanumeric).
func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i != 0 && i != len(name)-1:
		default:
			return false
		}
	}
	return true
}

// Create registers and formats a new named volume for backend. It fails closed
// on a duplicate name or an invalid size. On VHD-lane backends the backing image
// is built in-process (no host mke2fs); every other backend formats bare ext4
// via mke2fsPath.
func Create(ctx context.Context, stateDir, backend, name string, sizeMiB int64, mke2fsPath string) (Record, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return Record{}, fmt.Errorf("invalid volume name %q: use lowercase letters, digits, and hyphens (1-63 chars, not starting or ending with a hyphen)", name)
	}
	if sizeMiB == 0 {
		sizeMiB = DefaultSizeMiB
	}
	if sizeMiB < minSizeMiB || sizeMiB > maxSizeMiB {
		return Record{}, fmt.Errorf("invalid volume size %d MiB: must be between %d and %d", sizeMiB, minSizeMiB, maxSizeMiB)
	}
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for _, r := range idx.Volumes {
		if r.Name == name {
			return Record{}, fmt.Errorf("volume %q already exists", name)
		}
	}

	disk := DiskPath(stateDir, backend, name)
	if err := os.MkdirAll(filepath.Dir(disk), 0o700); err != nil {
		return Record{}, err
	}
	if backendUsesVHD(backend) {
		if err := formatVHD(ctx, disk, sizeMiB); err != nil {
			return Record{}, err
		}
	} else if err := formatExt4(ctx, disk, sizeMiB, mke2fsPath); err != nil {
		return Record{}, err
	}

	record := Record{
		Name:      name,
		SizeMiB:   sizeMiB,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	idx.Volumes = append(idx.Volumes, record)
	if err := WriteIndex(stateDir, idx); err != nil {
		// Roll back the backing file so a failed registry write leaves no orphan.
		_ = os.Remove(disk)
		return Record{}, err
	}
	return record, nil
}

// List returns all volumes sorted by name.
func List(stateDir string) ([]Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return nil, err
	}
	sortRecords(idx.Volumes)
	return idx.Volumes, nil
}

// Get returns one volume by name.
func Get(stateDir, name string) (Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for _, r := range idx.Volumes {
		if r.Name == name {
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("volume %q not found", name)
}

// Path resolves a named volume to its backing image path for backend, erroring
// if unknown.
func Path(stateDir, backend, name string) (string, error) {
	if _, err := Get(stateDir, name); err != nil {
		return "", err
	}
	return DiskPath(stateDir, backend, name), nil
}

// Remove deletes a volume and its backing file. It fails closed while the
// volume is attached to a still-running workspace unless force is set.
func Remove(stateDir, name string, force bool, isRunning func(string) bool) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	for i, r := range idx.Volumes {
		if r.Name != name {
			continue
		}
		if r.AttachedTo != "" && !force && holderActive(r.AttachedTo, isRunning) {
			return fmt.Errorf("volume %q is attached to running workspace %q (use --force to remove anyway)", name, r.AttachedTo)
		}
		idx.Volumes = append(idx.Volumes[:i], idx.Volumes[i+1:]...)
		if err := WriteIndex(stateDir, idx); err != nil {
			return err
		}
		// The registry is backend-neutral and a state dir is bound to one host,
		// but remove either backing-file shape so a host that switched lanes
		// leaves no orphan.
		_ = os.Remove(filepath.Join(stateDir, "volumes", name+".ext4"))
		_ = os.Remove(filepath.Join(stateDir, "volumes", name+".vhd"))
		return nil
	}
	return fmt.Errorf("volume %q not found", name)
}

// Attach records that workspace holds the volume, enforcing single-attach. It
// fails closed when another, still-running workspace already holds it; a stale
// holder (one isRunning reports as not running) is reclaimed.
func Attach(stateDir, name, workspace string, isRunning func(string) bool) (Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for i := range idx.Volumes {
		r := &idx.Volumes[i]
		if r.Name != name {
			continue
		}
		if r.AttachedTo != "" && r.AttachedTo != workspace && holderActive(r.AttachedTo, isRunning) {
			return Record{}, fmt.Errorf("volume %q is already attached to running workspace %q", name, r.AttachedTo)
		}
		r.AttachedTo = workspace
		if err := WriteIndex(stateDir, idx); err != nil {
			return Record{}, err
		}
		return *r, nil
	}
	return Record{}, fmt.Errorf("volume %q not found", name)
}

// Detach releases a single volume held by workspace. It is idempotent and never
// errors on an unknown volume or a different holder.
func Detach(stateDir, name, workspace string) error {
	return mutateHolder(stateDir, func(r *Record) bool {
		return r.Name == name && r.AttachedTo == workspace
	})
}

// DetachAll releases every volume held by workspace. Use it when a workspace is
// deleted so the registry never shows a volume attached to a gone workspace.
func DetachAll(stateDir, workspace string) error {
	return mutateHolder(stateDir, func(r *Record) bool {
		return r.AttachedTo == workspace
	})
}

func mutateHolder(stateDir string, match func(*Record) bool) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	changed := false
	for i := range idx.Volumes {
		if match(&idx.Volumes[i]) {
			idx.Volumes[i].AttachedTo = ""
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return WriteIndex(stateDir, idx)
}

// holderActive reports whether a recorded holder still counts as holding the
// volume. With no predicate we assume it is active (fail closed).
func holderActive(holder string, isRunning func(string) bool) bool {
	if isRunning == nil {
		return true
	}
	return isRunning(holder)
}

func mke2fsFormat(ctx context.Context, path string, sizeMiB int64, mke2fsPath string) error {
	if strings.TrimSpace(mke2fsPath) == "" {
		mke2fsPath = "mke2fs"
	}
	stage, err := os.MkdirTemp("", "microagent-volume-stage-")
	if err != nil {
		return fmt.Errorf("stage volume: %w", err)
	}
	defer os.RemoveAll(stage)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("allocate volume image: %w", err)
	}
	if err := f.Truncate(sizeMiB * 1024 * 1024); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("allocate volume image: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("allocate volume image: %w", err)
	}

	cmd := exec.CommandContext(ctx, mke2fsPath, "-q", "-t", "ext4", "-d", stage, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("format ext4 volume: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
