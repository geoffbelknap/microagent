package imagecache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

type Record struct {
	ImageRef    string          `json:"image_ref"`
	ResolvedRef string          `json:"resolved_ref,omitempty"`
	Digest      string          `json:"digest,omitempty"`
	Platform    rootfs.Platform `json:"platform"`
	OutputPath  string          `json:"output_path,omitempty"`
	SizeBytes   int64           `json:"size_bytes,omitempty"`
	LastUsedAt  string          `json:"last_used_at"`
	// RootfsSHA256 is measured once when the reusable baseline is published.
	// RootfsImmutable means microagent sealed the image-store file read-only
	// and will only hand workspaces private writable copies of it.
	RootfsSHA256    string `json:"rootfs_sha256,omitempty"`
	RootfsImmutable bool   `json:"rootfs_immutable,omitempty"`
	// InitSHA256 is the hash of the guest init binary built into this
	// baseline. Reuse requires it to match the init the workspace would
	// inject, or an upgraded microagent would keep cloning stale inits.
	InitSHA256 string `json:"init_sha256,omitempty"`
	// SetuidPolicy records the setuid handling this baseline was built
	// under (rootfs.SetuidPolicy*). Reuse requires "stripped": a baseline
	// from before the policy existed (empty) carries the image's setuid
	// bits and must rebuild rather than be cloned into workspaces that
	// asked for the stripped default.
	SetuidPolicy string `json:"setuid_policy,omitempty"`
	// ImageEnv/ImageEntrypoint/ImageCmd carry the OCI image config so a
	// baseline clone can assemble the guest config disk without a build.
	ImageEnv        []string             `json:"image_env,omitempty"`
	ImageEntrypoint []string             `json:"image_entrypoint,omitempty"`
	ImageCmd        []string             `json:"image_cmd,omitempty"`
	ImageDefaults   rootfs.ImageDefaults `json:"image_defaults,omitempty"`
}

type Index struct {
	Images []Record `json:"images"`
}

type PullOptions struct {
	StateDir      string
	ImageRef      string
	Architecture  string
	SizeMiB       int64
	Mke2fsPath    string
	DebugfsPath   string
	GuestInitPath string
}

type PruneResult struct {
	Removed []Record `json:"removed"`
	Deleted []Record `json:"deleted,omitempty"`
	Kept    []Record `json:"kept"`
	// CacheEntriesRemoved / CacheBytesFreed report the digest-keyed
	// base-stage cache entries cleared alongside the records when files are
	// deleted, so a purge accounts for all the disk it reclaimed.
	CacheEntriesRemoved int   `json:"cache_entries_removed,omitempty"`
	CacheBytesFreed     int64 `json:"cache_bytes_freed,omitempty"`
}

func Pull(ctx context.Context, opts PullOptions) (Record, error) {
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		return Record{}, fmt.Errorf("image reference is required")
	}
	if opts.StateDir == "" {
		opts.StateDir = workspace.StateDir()
	}
	if opts.Architecture == "" {
		opts.Architecture = workspace.GuestArch()
	}
	opts.Architecture = workspace.NormalizeArch(opts.Architecture)
	autoSize := opts.SizeMiB == 0
	if autoSize {
		opts.SizeMiB = rootfs.DefaultSizeMiB
	}
	if opts.SizeMiB < 0 {
		return Record{}, fmt.Errorf("size-mib must not be negative")
	}
	if opts.Mke2fsPath == "" {
		opts.Mke2fsPath = workspace.Mke2fsPath()
	}
	if opts.DebugfsPath == "" {
		opts.DebugfsPath = workspace.DebugfsPath()
	}
	if opts.GuestInitPath == "" {
		opts.GuestInitPath = workspace.GuestInitPath(opts.Architecture)
	}
	outputPath := RootfsPath(opts.StateDir, opts.ImageRef, rootfs.Platform{OS: "linux", Architecture: opts.Architecture})
	// `image pull` always fetches from the registry: it must never resolve
	// ImageRef from the local committed-OCI layout (LocalImageLayout left
	// unset), or a locally committed image could silently shadow an explicit
	// registry pull.
	provenance, err := rootfs.NewBuilder().Build(ctx, rootfs.BuildRequest{
		ImageRef:       opts.ImageRef,
		Platform:       rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:     outputPath,
		InitPath:       rootfs.DefaultInitPath,
		InitBinaryPath: opts.GuestInitPath,
		NoImageCommand: true,
		StateDir:       filepath.Join(opts.StateDir, "images", "build"),
		BaseCacheDir:   rootfs.BaseCacheDirFor(opts.StateDir),
		Mke2fsPath:     opts.Mke2fsPath,
		DebugfsPath:    opts.DebugfsPath,
		SizeMiB:        opts.SizeMiB,
		AutoSize:       autoSize,
		AllowMutable:   true,
	})
	if err != nil {
		return Record{}, err
	}
	record := FromProvenance(provenance)
	record.InitSHA256 = workspace.GuestInitSHA256(opts.GuestInitPath)
	if err := sealRootfsBaseline(&record); err != nil {
		return Record{}, err
	}
	if err := Upsert(opts.StateDir, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// SaveBaseline copies a freshly built plain rootfs into the image store and
// records it, so the first build of an image seeds the baseline every later
// create/run of that image clones. Best suited as the RootfsBaselineSave
// callback: the caller guarantees the rootfs carries nothing
// per-workspace (which, with the per-boot config disk, is every workspace
// rootfs the lifecycle builds).
func SaveBaseline(stateDir, rootfsPath string, provenance rootfs.Provenance, initSHA256 string) error {
	if provenance.ImageRef == "" || provenance.Digest == "" {
		return nil
	}
	storePath := RootfsPath(stateDir, provenance.ImageRef, provenance.Platform)
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		return err
	}
	if err := workspace.CopyFileReplace(rootfsPath, storePath, 0o600); err != nil {
		return err
	}
	record := FromProvenance(provenance)
	record.OutputPath = storePath
	record.InitSHA256 = initSHA256
	if err := sealRootfsBaseline(&record); err != nil {
		return err
	}
	return Upsert(stateDir, record)
}

// ValidateImmutableRootfs reports whether record names a reusable rootfs
// baseline carrying both a content identity and the read-only host posture
// microagent promises for immutable bases. It deliberately does not rehash the
// multi-gigabyte artifact: the baseline was measured before publication and is
// never attached to a guest; only its private copies are writable.
func ValidateImmutableRootfs(record Record) error {
	if !record.RootfsImmutable {
		return fmt.Errorf("rootfs baseline is not recorded as immutable")
	}
	if len(record.RootfsSHA256) != sha256.Size*2 {
		return fmt.Errorf("rootfs baseline has no valid SHA-256 content identity")
	}
	if _, err := hex.DecodeString(record.RootfsSHA256); err != nil {
		return fmt.Errorf("rootfs baseline has invalid SHA-256 content identity: %w", err)
	}
	info, err := os.Stat(record.OutputPath)
	if err != nil {
		return fmt.Errorf("stat immutable rootfs baseline: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("immutable rootfs baseline is not a regular file")
	}
	if info.Mode().Perm()&0o222 != 0 {
		return fmt.Errorf("immutable rootfs baseline is host-writable (mode %04o)", info.Mode().Perm())
	}
	if record.SizeBytes > 0 && info.Size() != record.SizeBytes {
		return fmt.Errorf("immutable rootfs baseline size changed: recorded %d bytes, current %d", record.SizeBytes, info.Size())
	}
	return nil
}

func sealRootfsBaseline(record *Record) error {
	if record == nil || strings.TrimSpace(record.OutputPath) == "" {
		return fmt.Errorf("seal rootfs baseline: output path is required")
	}
	info, err := os.Stat(record.OutputPath)
	if err != nil {
		return fmt.Errorf("seal rootfs baseline: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("seal rootfs baseline: %s is not a regular file", record.OutputPath)
	}
	sum, err := workspace.FileSHA256(record.OutputPath)
	if err != nil {
		return fmt.Errorf("measure rootfs baseline: %w", err)
	}
	if err := os.Chmod(record.OutputPath, 0o444); err != nil {
		return fmt.Errorf("seal rootfs baseline read-only: %w", err)
	}
	record.SizeBytes = info.Size()
	record.RootfsSHA256 = sum
	record.RootfsImmutable = true
	return ValidateImmutableRootfs(*record)
}

func List(stateDir string) ([]Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return nil, err
	}
	images := append([]Record{}, idx.Images...)
	Sort(images)
	return images, nil
}

func Tag(stateDir, source, target string) (Record, error) {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if source == "" {
		return Record{}, fmt.Errorf("source image is required")
	}
	if target == "" {
		return Record{}, fmt.Errorf("target image is required")
	}
	if strings.ContainsAny(target, "\x00\n\r") {
		return Record{}, fmt.Errorf("target image contains unsupported characters")
	}
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for _, image := range idx.Images {
		if MatchesRef(image, source) {
			tagged := image
			tagged.ImageRef = target
			tagged.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
			if err := Upsert(stateDir, tagged); err != nil {
				return Record{}, err
			}
			return tagged, nil
		}
	}
	return Record{}, fmt.Errorf("image %q not found", source)
}

func Remove(stateDir, ref string, deleteFiles bool) (result PruneResult, err error) {
	lockErr := withIndexLock(stateDir, func() error {
		result, err = removeLocked(stateDir, ref, deleteFiles)
		return err
	})
	if lockErr != nil && err == nil {
		return PruneResult{}, lockErr
	}
	return result, err
}

func removeLocked(stateDir, ref string, deleteFiles bool) (PruneResult, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return PruneResult{}, fmt.Errorf("image reference is required")
	}
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return PruneResult{}, err
	}
	result := PruneResult{}
	var matched []Record
	keptPaths := map[string]bool{}
	for _, image := range idx.Images {
		if MatchesRef(image, ref) {
			matched = append(matched, image)
			continue
		}
		result.Kept = append(result.Kept, image)
		if image.OutputPath != "" {
			if canonical, ok := CanonicalRootfsStorePath(stateDir, image.OutputPath); ok {
				keptPaths[canonical] = true
			}
		}
	}
	if len(matched) == 0 {
		return PruneResult{}, fmt.Errorf("image %q not found", ref)
	}
	deletedPaths := map[string]bool{}
	for _, image := range matched {
		cleanPath, ok := CanonicalRootfsStorePath(stateDir, image.OutputPath)
		if deleteFiles && image.OutputPath != "" && ok && !keptPaths[cleanPath] {
			if deletedPaths[cleanPath] {
				result.Deleted = append(result.Deleted, image)
				continue
			}
			if err := os.Remove(cleanPath); err == nil {
				deletedPaths[cleanPath] = true
				result.Deleted = append(result.Deleted, image)
				continue
			} else if !os.IsNotExist(err) {
				return PruneResult{}, err
			}
		}
		result.Removed = append(result.Removed, image)
	}
	if deleteFiles {
		// A purged image's base-stage cache entry goes with it, unless a
		// kept record still names the same digest (a tag or another ref
		// pointing at identical content keeps the entry useful).
		keptDigests := map[string]bool{}
		for _, image := range result.Kept {
			keptDigests[image.Digest] = true
		}
		removedDigests := map[string]bool{}
		for _, image := range append(append([]Record{}, result.Removed...), result.Deleted...) {
			if image.Digest != "" && !keptDigests[image.Digest] {
				removedDigests[image.Digest] = true
			}
		}
		if err := clearBaseCacheEntries(stateDir, &result, func(entry rootfs.BaseCacheEntry) bool {
			return removedDigests[entry.Digest]
		}); err != nil {
			return PruneResult{}, err
		}
	}
	Sort(result.Kept)
	Sort(result.Removed)
	Sort(result.Deleted)
	if err := WriteIndex(stateDir, Index{Images: result.Kept}); err != nil {
		return PruneResult{}, err
	}
	return result, nil
}

// clearBaseCacheEntries clears selected entries from the base-stage cache
// the builders share (rootfs.BaseCacheDirFor) and folds the removals into
// the result. When the cache is disabled by the environment override there
// is nothing this invocation can see to clear.
func clearBaseCacheEntries(stateDir string, result *PruneResult, remove func(rootfs.BaseCacheEntry) bool) error {
	cacheDir := rootfs.BaseCacheDirFor(stateDir)
	if cacheDir == "" {
		return nil
	}
	removed, err := rootfs.ClearBaseCache(cacheDir, remove)
	if err != nil {
		return err
	}
	for _, entry := range removed {
		result.CacheEntriesRemoved++
		result.CacheBytesFreed += entry.SizeBytes
	}
	return nil
}

func Prune(stateDir string, deleteFiles bool) (result PruneResult, err error) {
	lockErr := withIndexLock(stateDir, func() error {
		result, err = pruneLocked(stateDir, deleteFiles)
		return err
	})
	if lockErr != nil && err == nil {
		return PruneResult{}, lockErr
	}
	return result, err
}

func pruneLocked(stateDir string, deleteFiles bool) (PruneResult, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return PruneResult{}, err
	}
	result := PruneResult{}
	deletedPaths := map[string]bool{}
	for _, image := range idx.Images {
		if image.OutputPath == "" {
			result.Kept = append(result.Kept, image)
			continue
		}
		cleanPath, ok := CanonicalRootfsStorePath(stateDir, image.OutputPath)
		if deleteFiles && ok {
			if deletedPaths[cleanPath] {
				result.Deleted = append(result.Deleted, image)
				continue
			}
			if err := os.Remove(cleanPath); err == nil {
				deletedPaths[cleanPath] = true
				result.Deleted = append(result.Deleted, image)
				continue
			} else if os.IsNotExist(err) {
				result.Removed = append(result.Removed, image)
				continue
			} else {
				return PruneResult{}, err
			}
		}
		if _, err := os.Stat(image.OutputPath); err == nil {
			result.Kept = append(result.Kept, image)
		} else if os.IsNotExist(err) {
			result.Removed = append(result.Removed, image)
		} else {
			return PruneResult{}, err
		}
	}
	if deleteFiles {
		if err := clearBaseCacheEntries(stateDir, &result, nil); err != nil {
			return PruneResult{}, err
		}
	}
	Sort(result.Kept)
	Sort(result.Removed)
	Sort(result.Deleted)
	if err := WriteIndex(stateDir, Index{Images: result.Kept}); err != nil {
		return PruneResult{}, err
	}
	return result, nil
}

func Find(stateDir, ref string, platform rootfs.Platform) (Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for _, image := range idx.Images {
		if !MatchesRef(image, ref) {
			continue
		}
		if platform.OS != "" && image.Platform.OS != "" && image.Platform.OS != platform.OS {
			continue
		}
		if platform.Architecture != "" && image.Platform.Architecture != "" && image.Platform.Architecture != platform.Architecture {
			continue
		}
		if image.OutputPath == "" {
			continue
		}
		if _, err := os.Stat(image.OutputPath); err != nil {
			continue
		}
		return image, nil
	}
	return Record{}, fmt.Errorf("image %q not found", ref)
}

func Upsert(stateDir string, record Record) error {
	return withIndexLock(stateDir, func() error {
		return upsertLocked(stateDir, record)
	})
}

func upsertLocked(stateDir string, record Record) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range idx.Images {
		if existing.ImageRef == record.ImageRef && existing.Platform == record.Platform {
			idx.Images[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		idx.Images = append(idx.Images, record)
	}
	Sort(idx.Images)
	return WriteIndex(stateDir, idx)
}

func RecordProvenance(stateDir string, provenance rootfs.Provenance) error {
	if provenance.ImageRef == "" || provenance.Digest == "" {
		return nil
	}
	return Upsert(stateDir, FromProvenance(provenance))
}

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

func WriteIndex(stateDir string, idx Index) error {
	if err := os.MkdirAll(filepath.Dir(IndexPath(stateDir)), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(IndexPath(stateDir), data, 0o600)
}

func IndexPath(stateDir string) string {
	return filepath.Join(stateDir, "images", "index.json")
}

// withIndexLock serializes an index read-modify-write against concurrent cache
// mutations (upsert/remove/prune) so two callers cannot both read, mutate, and
// write back a last-writer-wins result — which could resurrect a pruned baseline
// or corrupt the index. The lock file lives beside the index and is advisory
// (unix flock; a no-op on platforms without it).
func withIndexLock(stateDir string, fn func() error) error {
	lockPath := IndexPath(stateDir) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	release, err := fsutil.Lock(lockPath)
	if err != nil {
		return fmt.Errorf("lock image index: %w", err)
	}
	defer func() { _ = release() }()
	return fn()
}

func RootfsPath(stateDir, imageRef string, platform rootfs.Platform) string {
	sum := sha256.Sum256([]byte(imageRef + "\x00" + platform.OS + "\x00" + platform.Architecture + "\x00" + platform.Variant))
	name := hex.EncodeToString(sum[:])[:24] + ".ext4"
	return filepath.Join(stateDir, "images", "rootfs", name)
}

func FromProvenance(provenance rootfs.Provenance) Record {
	return Record{
		ImageRef:    provenance.ImageRef,
		ResolvedRef: provenance.ResolvedRef,
		Digest:      provenance.Digest,
		Platform:    provenance.Platform,
		OutputPath:  provenance.OutputPath,
		SizeBytes:   provenance.SizeBytes,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),

		ImageEnv:        provenance.ImageEnv,
		ImageEntrypoint: provenance.ImageEntrypoint,
		ImageCmd:        provenance.ImageCmd,
		SetuidPolicy:    provenance.SetuidPolicy,
		ImageDefaults:   provenance.ImageDefaults,
	}
}

func Provenance(record Record, outputPath string) rootfs.Provenance {
	return rootfs.Provenance{
		ImageRef:     record.ImageRef,
		ResolvedRef:  record.ResolvedRef,
		Digest:       record.Digest,
		Platform:     record.Platform,
		OutputPath:   outputPath,
		SizeBytes:    record.SizeBytes,
		Builder:      "microagent-image-store",
		BuilderPhase: "copy-baseline",
		RootfsBase: &vmkit.RootfsBase{
			SHA256:    record.RootfsSHA256,
			Immutable: record.RootfsImmutable,
		},

		ImageEnv:        record.ImageEnv,
		ImageEntrypoint: record.ImageEntrypoint,
		ImageCmd:        record.ImageCmd,
		SetuidPolicy:    record.SetuidPolicy,
		ImageDefaults:   effectiveImageDefaults(record.ImageDefaults, record.ImageEnv, record.ImageEntrypoint, record.ImageCmd),
	}
}

func effectiveImageDefaults(defaults rootfs.ImageDefaults, env, entrypoint, cmd []string) rootfs.ImageDefaults {
	if defaults.IsZero() {
		defaults.Env = append([]string{}, env...)
		defaults.Entrypoint = append([]string{}, entrypoint...)
		defaults.Cmd = append([]string{}, cmd...)
	}
	return defaults
}

func MatchesRef(image Record, ref string) bool {
	return image.ImageRef == ref || image.ResolvedRef == ref || image.Digest == ref
}

func PathInRootfsStore(stateDir, path string) bool {
	_, ok := CanonicalRootfsStorePath(stateDir, path)
	return ok
}

func CanonicalRootfsStorePath(stateDir, path string) (string, bool) {
	storeDir, err := filepath.Abs(filepath.Join(stateDir, "images", "rootfs"))
	if err != nil {
		return "", false
	}
	storeDir, err = filepath.EvalSymlinks(storeDir)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		return "", false
	}
	absPath = filepath.Join(parent, filepath.Base(absPath))
	rel, err := filepath.Rel(storeDir, absPath)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return absPath, true
}

func Sort(images []Record) {
	sort.Slice(images, func(i, j int) bool {
		if images[i].ImageRef != images[j].ImageRef {
			return images[i].ImageRef < images[j].ImageRef
		}
		if images[i].Platform.Architecture != images[j].Platform.Architecture {
			return images[i].Platform.Architecture < images[j].Platform.Architecture
		}
		return images[i].Digest < images[j].Digest
	})
}
