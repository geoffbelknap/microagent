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

	"github.com/geoffbelknap/microagent/pkg/rootfs"
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
	GuestInitPath string
}

type PruneResult struct {
	Removed []Record `json:"removed"`
	Deleted []Record `json:"deleted,omitempty"`
	Kept    []Record `json:"kept"`
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
	if opts.SizeMiB == 0 {
		opts.SizeMiB = rootfs.DefaultSizeMiB
	}
	if opts.SizeMiB < 0 {
		return Record{}, fmt.Errorf("size-mib must not be negative")
	}
	if opts.Mke2fsPath == "" {
		opts.Mke2fsPath = workspace.Mke2fsPath()
	}
	if opts.GuestInitPath == "" {
		opts.GuestInitPath = workspace.GuestInitPath(opts.Architecture)
	}
	outputPath := RootfsPath(opts.StateDir, opts.ImageRef, rootfs.Platform{OS: "linux", Architecture: opts.Architecture})
	provenance, err := rootfs.NewBuilder().Build(ctx, rootfs.BuildRequest{
		ImageRef:       opts.ImageRef,
		Platform:       rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:     outputPath,
		InitPath:       rootfs.DefaultInitPath,
		InitBinaryPath: opts.GuestInitPath,
		NoImageCommand: true,
		StateDir:       filepath.Join(opts.StateDir, "images", "build"),
		Mke2fsPath:     opts.Mke2fsPath,
		SizeMiB:        opts.SizeMiB,
		AllowMutable:   true,
	})
	if err != nil {
		return Record{}, err
	}
	record := FromProvenance(provenance)
	if err := Upsert(opts.StateDir, record); err != nil {
		return Record{}, err
	}
	return record, nil
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

func Remove(stateDir, ref string, deleteFiles bool) (PruneResult, error) {
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
	Sort(result.Kept)
	Sort(result.Removed)
	Sort(result.Deleted)
	if err := WriteIndex(stateDir, Index{Images: result.Kept}); err != nil {
		return PruneResult{}, err
	}
	return result, nil
}

func Prune(stateDir string, deleteFiles bool) (PruneResult, error) {
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
	if err := os.MkdirAll(filepath.Dir(IndexPath(stateDir)), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(IndexPath(stateDir), data, 0o644)
}

func IndexPath(stateDir string) string {
	return filepath.Join(stateDir, "images", "index.json")
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
	}
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
