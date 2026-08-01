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

	"github.com/geoffbelknap/microagent/internal/ext4fs"
	"github.com/geoffbelknap/microagent/pkg/fsutil"
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

// IndexPath returns the registry file path for a state directory.
func IndexPath(stateDir string) string {
	return filepath.Join(stateDir, "volumes", "index.json")
}

// DiskPath returns the ext4 backing-image path for a named volume.
// Names are constrained by ValidName, so the join never escapes the volumes
// directory. The backend argument is retained for API compatibility.
func DiskPath(stateDir, backend, name string) string {
	return filepath.Join(stateDir, "volumes", name+".ext4")
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

// WriteIndex persists the registry, writing atomically (temp + rename) so a
// concurrent reader or a crash never observes a truncated index.
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
	return fsutil.WriteFileAtomic(IndexPath(stateDir), data, 0o600)
}

// withIndexLock serializes an index read-modify-write against concurrent volume
// operations (single-attach enforcement, create/remove) so two callers cannot
// both read the index, mutate, and write back a last-writer-wins result — which
// could let two workspaces attach the same single-attach volume, or corrupt the
// index. The lock file lives beside the index and is advisory (unix flock; a
// no-op on platforms without it).
func withIndexLock(stateDir string, fn func() error) error {
	lockPath := IndexPath(stateDir) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	release, err := fsutil.Lock(lockPath)
	if err != nil {
		return fmt.Errorf("lock volume index: %w", err)
	}
	defer func() { _ = release() }()
	return fn()
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

// Create registers and formats a new named ext4 volume for backend. It fails
// closed on a duplicate name or an invalid size.
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
	var record Record
	err := withIndexLock(stateDir, func() error {
		idx, err := ReadIndex(stateDir)
		if err != nil {
			return err
		}
		for _, r := range idx.Volumes {
			if r.Name == name {
				return fmt.Errorf("volume %q already exists", name)
			}
		}

		disk := DiskPath(stateDir, backend, name)
		if err := os.MkdirAll(filepath.Dir(disk), 0o700); err != nil {
			return err
		}
		if err := formatExt4(ctx, disk, sizeMiB, mke2fsPath); err != nil {
			return err
		}

		record = Record{
			Name:      name,
			SizeMiB:   sizeMiB,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		idx.Volumes = append(idx.Volumes, record)
		if err := WriteIndex(stateDir, idx); err != nil {
			// Roll back the backing file so a failed registry write leaves no orphan.
			_ = os.Remove(disk)
			return err
		}
		return nil
	})
	if err != nil {
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
	return withIndexLock(stateDir, func() error {
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
			_ = os.Remove(filepath.Join(stateDir, "volumes", name+".ext4"))
			return nil
		}
		return fmt.Errorf("volume %q not found", name)
	})
}

// Resize grows or shrinks a named volume's ext4 backing image. Host-side and
// offline only: it fails closed, with no force override, while the volume is
// attached to a still-running workspace, the same way Remove's default (no
// --force) does — a disk a live workspace might have open is not safe to
// resize out from under it.
func Resize(stateDir, name string, sizeMiB int64, e2fsckPath, resize2fsPath string, isRunning func(string) bool) (Record, error) {
	name = strings.TrimSpace(name)
	if sizeMiB < minSizeMiB || sizeMiB > maxSizeMiB {
		return Record{}, fmt.Errorf("invalid volume size %d MiB: must be between %d and %d", sizeMiB, minSizeMiB, maxSizeMiB)
	}
	var record Record
	err := withIndexLock(stateDir, func() error {
		idx, err := ReadIndex(stateDir)
		if err != nil {
			return err
		}
		for i := range idx.Volumes {
			r := &idx.Volumes[i]
			if r.Name != name {
				continue
			}
			if r.AttachedTo != "" && holderActive(r.AttachedTo, isRunning) {
				return fmt.Errorf("volume %q is attached to running workspace %q; detach it before resizing", name, r.AttachedTo)
			}
			if sizeMiB == r.SizeMiB {
				record = *r
				return nil
			}
			path := DiskPath(stateDir, "", name)
			if err := ext4fs.Resize(e2fsckPath, resize2fsPath, path, sizeMiB*1024*1024); err != nil {
				return err
			}
			r.SizeMiB = sizeMiB
			if err := WriteIndex(stateDir, idx); err != nil {
				return err
			}
			record = *r
			return nil
		}
		return fmt.Errorf("volume %q not found", name)
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

// Attach records that workspace holds the volume, enforcing single-attach. It
// fails closed when another, still-running workspace already holds it; a stale
// holder (one isRunning reports as not running) is reclaimed.
func Attach(stateDir, name, workspace string, isRunning func(string) bool) (Record, error) {
	var rec Record
	err := withIndexLock(stateDir, func() error {
		idx, err := ReadIndex(stateDir)
		if err != nil {
			return err
		}
		for i := range idx.Volumes {
			r := &idx.Volumes[i]
			if r.Name != name {
				continue
			}
			if r.AttachedTo != "" && r.AttachedTo != workspace && holderActive(r.AttachedTo, isRunning) {
				return fmt.Errorf("volume %q is already attached to running workspace %q", name, r.AttachedTo)
			}
			r.AttachedTo = workspace
			if err := WriteIndex(stateDir, idx); err != nil {
				return err
			}
			rec = *r
			return nil
		}
		return fmt.Errorf("volume %q not found", name)
	})
	return rec, err
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
	return withIndexLock(stateDir, func() error {
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
	})
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
