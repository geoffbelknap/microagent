package rootfs

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/registryauth"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

type Builder struct {
	Name string
}

type baseStageCacheMetadata struct {
	ImageRef     string        `json:"image_ref"`
	ResolvedRef  string        `json:"resolved_ref"`
	Digest       string        `json:"digest"`
	Platform     Platform      `json:"platform"`
	ImageConfig  ocispec.Image `json:"image_config"`
	LayerDigests []string      `json:"layer_digests"`
	// SetuidPolicy keys the entry alongside digest+platform: a stripped tree
	// and a preserved tree for the same digest are different artifacts (the
	// sidecar modes differ), so they must never answer for each other. The
	// stripped list travels with the entry because a cache hit skips the
	// extraction that would otherwise record it.
	SetuidPolicy        string   `json:"setuid_policy,omitempty"`
	SetuidStrippedCount int      `json:"setuid_stripped_count,omitempty"`
	SetuidStripped      []string `json:"setuid_stripped,omitempty"`
}

func NewBuilder() Builder {
	return Builder{Name: "microagent-rootfs"}
}

func (b Builder) Build(ctx context.Context, req BuildRequest) (Provenance, error) {
	req = NormalizeRequest(req)
	if err := ValidateRequest(req); err != nil {
		return Provenance{}, err
	}
	progress := newProgressReporter(req.Progress)
	name := b.Name
	if name == "" {
		name = "microagent-rootfs"
	}
	provenance := Provenance{
		ImageRef:   req.ImageRef,
		Platform:   req.Platform,
		OutputPath: req.OutputPath,
		Format:     req.Format,
		InitPath:   req.InitPath,
		Builder:    name,
	}

	platform := ocispec.Platform{
		OS:           req.Platform.OS,
		Architecture: req.Platform.Architecture,
		Variant:      req.Platform.Variant,
	}
	var imageConfig ocispec.Image

	tmpBase := req.StateDir
	if tmpBase == "" {
		tmpBase = filepath.Join(os.TempDir(), "microagent-rootfs")
	}
	tmpBase = filepath.Join(tmpBase, "tmp")
	if err := os.MkdirAll(tmpBase, 0o755); err != nil {
		return provenance, fmt.Errorf("create temp dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "build-*")
	if err != nil {
		return provenance, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := !req.KeepStage
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	stageDir := filepath.Join(tmpDir, "stage")
	if req.KeepStage {
		provenance.StageDir = stageDir
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return provenance, fmt.Errorf("create stage dir: %w", err)
	}
	if err := ensureStageMetadata(stageDir); err != nil {
		return provenance, err
	}

	// Resolve the image ref from the local committed-OCI layout before
	// falling back to a remote registry (standard local-first image
	// resolution — see BuildRequest.LocalImageLayout). Any local miss or
	// error (no layout, ref not committed there, corrupt layout, ...) is
	// not fatal: it just means this ref falls back to the remote path,
	// so a legitimately remote-only ref still works. The
	// localImageLayoutExists check below guards oci.New, which
	// unconditionally creates the OCI layout scaffold (blobs/,
	// index.json, oci-layout) for a path that doesn't have one yet --
	// without it, every remote-only build would create that scaffold
	// under LocalImageLayout even though no image was ever committed
	// there.
	var src oras.ReadOnlyTarget
	var localResolvedRef string
	if req.LocalImageLayout != "" && localImageLayoutExists(req.LocalImageLayout) {
		if localStore, err := oci.New(req.LocalImageLayout); err == nil {
			if desc, err := localStore.Resolve(ctx, req.ImageRef); err == nil {
				src = localStore
				localResolvedRef = req.ImageRef + "@" + desc.Digest.String()
			}
		}
	}
	var repoRef, reference string
	if src == nil {
		var err error
		repoRef, reference, err = splitRegistryReference(req.ImageRef)
		if err != nil {
			return provenance, err
		}
		repo, err := newRepository(repoRef)
		if err != nil {
			return provenance, err
		}
		src = repo
	} else {
		reference = req.ImageRef
	}
	// The manifest is resolved from the source on every build, cached or
	// not: the source stays the authority on what the ref means, and the
	// base-stage cache below can only substitute bytes for the digest
	// resolved here. A cache hit therefore never pins a tag to a stale or
	// withdrawn image, and content cached from one source cannot answer
	// for a same-ref image from another — the digests differ.
	provenance.BuilderPhase = "fetch-manifest"
	progress.emit("fetch-manifest", "fetching manifest", 0, 0, 0, 0)
	manifestDesc, manifestBytes, err := oras.FetchBytes(ctx, src, reference, oras.FetchBytesOptions{
		FetchOptions: oras.FetchOptions{
			ResolveOptions: oras.ResolveOptions{TargetPlatform: &platform},
		},
	})
	if err != nil {
		return provenance, fmt.Errorf("fetch OCI image %s for %s/%s: %w", req.ImageRef, platform.OS, platform.Architecture, err)
	}
	provenance.Digest = manifestDesc.Digest.String()
	if localResolvedRef != "" {
		provenance.ResolvedRef = localResolvedRef
	} else {
		provenance.ResolvedRef = repoRef + "@" + manifestDesc.Digest.String()
	}

	setuidPolicy := setuidPolicyFor(req.AllowGuestSetuid)
	provenance.SetuidPolicy = setuidPolicy
	stripper := &setuidStripper{allow: req.AllowGuestSetuid}
	cacheDir := strings.TrimSpace(req.BaseCacheDir)
	restored := false
	if cacheDir != "" {
		metadata, ok, err := restoreBaseStageCache(cacheDir, provenance.Digest, req.Platform, setuidPolicy, stageDir)
		if err != nil {
			return provenance, err
		}
		if ok {
			provenance.LayerDigests = append([]string{}, metadata.LayerDigests...)
			provenance.BuilderPhase = "restore-base-cache"
			provenance.BaseSource = BaseSourceCache
			// A cache hit skips extraction, so the strip record travels with
			// the entry: the restored tree was stripped when it was first
			// extracted, and the provenance must say so rather than read as
			// "nothing was stripped".
			provenance.SetuidStrippedCount = metadata.SetuidStrippedCount
			provenance.SetuidStripped = append([]string(nil), metadata.SetuidStripped...)
			progress.emit("restore-base-cache", "restoring cached base rootfs", 1, 1, 0, 0)
			imageConfig = metadata.ImageConfig
			if err := validateImagePlatform(imageConfig, req.Platform); err != nil {
				return provenance, err
			}
			restored = true
		}
	}
	if !restored {
		if localResolvedRef != "" {
			provenance.BaseSource = BaseSourceLocalLayout
		} else {
			provenance.BaseSource = BaseSourceRegistry
		}
		var manifest ocispec.Manifest
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			return provenance, fmt.Errorf("parse OCI image manifest: %w", err)
		}
		progress.emit("fetch-config", "fetching image config", 0, 0, 0, 0)
		configBytes, err := fetchBytes(ctx, src, manifest.Config)
		if err != nil {
			return provenance, fmt.Errorf("fetch OCI image config: %w", err)
		}
		if err := json.Unmarshal(configBytes, &imageConfig); err != nil {
			return provenance, fmt.Errorf("parse OCI image config: %w", err)
		}
		if err := validateImagePlatform(imageConfig, req.Platform); err != nil {
			return provenance, err
		}

		provenance.BuilderPhase = "extract-layers"
		totalLayerBytes := descriptorSize(manifest.Layers...)
		progress.emit("extract-layers", "extracting layers", 0, int64(len(manifest.Layers)), 0, totalLayerBytes)
		var fetchedLayerBytes int64
		for i, layer := range manifest.Layers {
			rc, err := src.Fetch(ctx, layer)
			if err != nil {
				return provenance, fmt.Errorf("fetch OCI layer %s: %w", layer.Digest, err)
			}
			var layerBytes int64
			reader := &progressReadCloser{
				ReadCloser: rc,
				OnRead: func(n int64) {
					layerBytes += n
					progress.emitThrottled("extract-layers", "extracting layers", int64(i+1), int64(len(manifest.Layers)), fetchedLayerBytes+layerBytes, totalLayerBytes)
				},
			}
			if err := extractLayer(stageDir, layer.MediaType, reader, stripper); err != nil {
				_ = rc.Close()
				return provenance, fmt.Errorf("extract OCI layer %s: %w", layer.Digest, err)
			}
			if err := rc.Close(); err != nil {
				return provenance, fmt.Errorf("close OCI layer %s: %w", layer.Digest, err)
			}
			provenance.LayerDigests = append(provenance.LayerDigests, layer.Digest.String())
			fetchedLayerBytes += layerBytes
			progress.emit("extract-layers", "extracting layers", int64(i+1), int64(len(manifest.Layers)), fetchedLayerBytes, totalLayerBytes)
		}
		baseCount, baseList := stripper.finalize()
		provenance.SetuidStrippedCount = baseCount
		provenance.SetuidStripped = append([]string(nil), baseList...)
		if cacheDir != "" {
			metadata := baseStageCacheMetadata{
				ImageRef:            req.ImageRef,
				ResolvedRef:         provenance.ResolvedRef,
				Digest:              provenance.Digest,
				Platform:            req.Platform,
				ImageConfig:         imageConfig,
				LayerDigests:        append([]string{}, provenance.LayerDigests...),
				SetuidPolicy:        setuidPolicy,
				SetuidStrippedCount: baseCount,
				SetuidStripped:      append([]string(nil), baseList...),
			}
			if err := saveBaseStageCache(cacheDir, metadata, stageDir); err != nil {
				// The build result is unaffected by a failed cache publish
				// (read-only cache dir, full disk); report it on the
				// progress stream rather than failing a completed fetch.
				progress.emit("save-base-cache", fmt.Sprintf("base cache not saved: %v", err), 0, 0, 0, 0)
			}
		}
	}

	provenance.ImageEnv = append([]string{}, imageConfig.Config.Env...)
	provenance.ImageEntrypoint = append([]string{}, imageConfig.Config.Entrypoint...)
	provenance.ImageCmd = append([]string{}, imageConfig.Config.Cmd...)
	provenance.ImageDefaults = imageDefaultsFromOCI(imageConfig.Config)

	provenance.BuilderPhase = "write-init"
	progress.emit("write-init", "writing guest init", 0, 0, 0, 0)
	if err := writeInit(stageDir, req.InitPath, buildCommand(req, imageConfig), buildGuestEnv(req.Env, imageConfig), req.InitBinaryPath, req.ConsoleShell); err != nil {
		return provenance, err
	}
	if err := ensureGuestRuntimeDirs(stageDir); err != nil {
		return provenance, err
	}
	provenance.BuilderPhase = "write-files"
	progress.emit("write-files", "writing declared files", 0, 0, 0, 0)
	if err := writeDeclaredFiles(stageDir, req.Files); err != nil {
		return provenance, err
	}
	if req.StageSnapshot != "" {
		provenance.BuilderPhase = "snapshot-stage"
		progress.emit("snapshot-stage", "snapshotting unpacked stage", 0, 0, 0, 0)
		if err := copyStage(stageDir, req.StageSnapshot); err != nil {
			return provenance, err
		}
		provenance.StageSnapshot = req.StageSnapshot
	}

	if err := buildRootfsImage(ctx, req, stageDir, tmpDir, progress, &provenance); err != nil {
		return provenance, err
	}
	info, err := os.Stat(req.OutputPath)
	if err != nil {
		return provenance, fmt.Errorf("stat output rootfs: %w", err)
	}
	provenance.SizeBytes = info.Size()
	provenance.BuilderPhase = "complete"
	progress.emit("complete", "rootfs complete", 1, 1, provenance.SizeBytes, provenance.SizeBytes)
	return provenance, nil
}

func imageDefaultsFromOCI(config ocispec.ImageConfig) ImageDefaults {
	defaults := ImageDefaults{
		User:         config.User,
		Env:          append([]string{}, config.Env...),
		Entrypoint:   append([]string{}, config.Entrypoint...),
		Cmd:          append([]string{}, config.Cmd...),
		WorkingDir:   config.WorkingDir,
		StopSignal:   config.StopSignal,
		Labels:       make(map[string]string, len(config.Labels)),
		ExposedPorts: make([]string, 0, len(config.ExposedPorts)),
		Volumes:      make([]string, 0, len(config.Volumes)),
	}
	for port := range config.ExposedPorts {
		defaults.ExposedPorts = append(defaults.ExposedPorts, port)
	}
	for volume := range config.Volumes {
		defaults.Volumes = append(defaults.Volumes, volume)
	}
	for key, value := range config.Labels {
		defaults.Labels[key] = value
	}
	sort.Strings(defaults.ExposedPorts)
	sort.Strings(defaults.Volumes)
	if len(defaults.Labels) == 0 {
		defaults.Labels = nil
	}
	return defaults
}

func buildCommand(req BuildRequest, imageConfig ocispec.Image) []string {
	command := append([]string{}, req.Command...)
	if len(command) == 0 && !req.NoImageCommand {
		command = append([]string{}, imageConfig.Config.Entrypoint...)
		command = append(command, imageConfig.Config.Cmd...)
	}
	return command
}

func buildGuestEnv(reqEnv map[string]string, imageConfig ocispec.Image) map[string]string {
	out := map[string]string{}
	for _, entry := range imageConfig.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && validShellEnvName(key) {
			out[key] = value
		}
	}
	for key, value := range reqEnv {
		if validShellEnvName(key) {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (b Builder) BuildBundle(ctx context.Context, req BundleRequest) (BundleProvenance, error) {
	req = NormalizeBundleRequest(req)
	provenance := BundleProvenance{
		SourcePath:   req.SourcePath,
		OutputPath:   req.OutputPath,
		Format:       req.Format,
		Builder:      b.Name,
		BuilderPhase: "validate",
	}
	if provenance.Builder == "" {
		provenance.Builder = "microagent-rootfs"
	}
	if err := ValidateBundleRequest(req); err != nil {
		return provenance, err
	}
	tmpBase := req.StateDir
	if tmpBase == "" {
		tmpBase = filepath.Join(os.TempDir(), "microagent-rootfs")
	}
	if err := os.MkdirAll(tmpBase, 0o755); err != nil {
		return provenance, fmt.Errorf("create temp dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "bundle-*")
	if err != nil {
		return provenance, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	stageDir := filepath.Join(tmpDir, "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return provenance, fmt.Errorf("create stage dir: %w", err)
	}
	provenance.BuilderPhase = "extract-bundle"
	source, err := os.Open(req.SourcePath)
	if err != nil {
		return provenance, fmt.Errorf("open bundle: %w", err)
	}
	mediaType := ""
	if strings.HasSuffix(req.SourcePath, ".tgz") || strings.HasSuffix(req.SourcePath, ".tar.gz") {
		mediaType = "application/gzip"
	}
	stripper := &setuidStripper{allow: req.AllowGuestSetuid}
	provenance.SetuidPolicy = setuidPolicyFor(req.AllowGuestSetuid)
	if err := extractLayer(stageDir, mediaType, source, stripper); err != nil {
		_ = source.Close()
		return provenance, fmt.Errorf("extract bundle: %w", err)
	}
	if err := source.Close(); err != nil {
		return provenance, fmt.Errorf("close bundle: %w", err)
	}
	provenance.SetuidStrippedCount, provenance.SetuidStripped = stripper.finalize()
	if err := buildBundleImage(ctx, req, stageDir, tmpDir, &provenance); err != nil {
		return provenance, err
	}
	info, err := os.Stat(req.OutputPath)
	if err != nil {
		return provenance, fmt.Errorf("stat output bundle: %w", err)
	}
	provenance.SizeBytes = info.Size()
	provenance.BuilderPhase = "complete"
	return provenance, nil
}

// restoreBaseStageCache copies the cached base stage for digest+platform
// into stageDir. Every unusable entry — missing, unreadable, mismatched,
// torn — is a miss, never an error: the caller re-fetches from the source
// and the next save overwrites the bad entry, so the cache self-heals
// instead of wedging builds. The only error case is a stage dir this
// function dirtied and could not clean, which the caller must not build on.
func restoreBaseStageCache(cacheDir, digest string, platform Platform, setuidPolicy, stageDir string) (baseStageCacheMetadata, bool, error) {
	entryDir := baseStageCacheEntryDir(cacheDir, digest, platform, setuidPolicy)
	metadataBytes, err := os.ReadFile(filepath.Join(entryDir, "metadata.json"))
	if err != nil {
		return baseStageCacheMetadata{}, false, nil
	}
	var metadata baseStageCacheMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return baseStageCacheMetadata{}, false, nil
	}
	if metadata.Digest != digest || metadata.Platform != platform || metadata.SetuidPolicy != setuidPolicy {
		return baseStageCacheMetadata{}, false, nil
	}
	baseDir := filepath.Join(entryDir, "base")
	// Entries written before the save became atomic can be a valid metadata.json
	// over a partial tree. Treat those as a miss and rebuild: a stale cache that
	// rebuilds costs one pull, where a stale cache that is trusted produces a
	// rootfs with no /bin and a guest that exits 1 with nothing on any stream.
	if _, err := os.Stat(filepath.Join(baseDir, stageMetadataName)); err != nil {
		return baseStageCacheMetadata{}, false, nil
	}
	if _, err := readStageMetadata(baseDir); err != nil {
		// Metadata-free cache entries were produced by the old, lossy
		// extractor. Rebuild them from the source instead of preserving the
		// flattened ownership and inode attributes.
		return baseStageCacheMetadata{}, false, nil
	}
	if err := copyBaseStageCache(baseDir, stageDir); err != nil {
		// The copy may have half-populated the stage; reset it so the fetch
		// path starts from an empty tree, then treat the entry as a miss.
		if resetErr := resetBaseStageDir(stageDir); resetErr != nil {
			return baseStageCacheMetadata{}, false, fmt.Errorf("restore rootfs base cache: %w", errors.Join(err, resetErr))
		}
		return baseStageCacheMetadata{}, false, nil
	}
	// Hits refresh the entry's mtime so the save-side reaper evicts by
	// least-recent use, not by original publish time.
	now := time.Now()
	_ = os.Chtimes(entryDir, now, now)
	return metadata, true, nil
}

func resetBaseStageDir(stageDir string) error {
	if err := os.RemoveAll(stageDir); err != nil {
		return err
	}
	return os.MkdirAll(stageDir, 0o755)
}

// saveBaseStageCache publishes a cache entry atomically.
//
// It used to build the entry in place: RemoveAll(base/), repopulate it with a
// `cp -a` of the whole rootfs, then write metadata.json. metadata.json is the
// completion marker, and it survived from the previous run — so for the entire
// duration of the copy the entry was a valid-looking marker over a tree that
// was being emptied and refilled. A process killed in that window (OOM, Ctrl-C,
// a full disk) left the marker pointing at a fraction of a rootfs.
//
// Nothing detected that afterwards. The next build restored the partial tree,
// produced a rootfs with no /bin, and the guest exited 1 with no output on any
// stream — indistinguishable from the user's own command failing. It never
// recovered on its own, because the marker stayed valid forever.
//
// Now the entry is staged beside its final location and renamed into place.
// Rename is atomic, so an interrupted save leaves the old entry intact or no
// entry at all; either is safe, because a miss just rebuilds.
func saveBaseStageCache(cacheDir string, metadata baseStageCacheMetadata, stageDir string) error {
	entryDir := baseStageCacheEntryDir(cacheDir, metadata.Digest, metadata.Platform, metadata.SetuidPolicy)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create rootfs base cache: %w", err)
	}
	pendingDir, err := os.MkdirTemp(cacheDir, baseStageCachePendingPrefix+"*")
	if err != nil {
		return fmt.Errorf("create rootfs base cache: %w", err)
	}
	defer os.RemoveAll(pendingDir)
	if err := copyBaseStageCache(stageDir, filepath.Join(pendingDir, "base")); err != nil {
		return fmt.Errorf("save rootfs base cache stage: %w", err)
	}
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rootfs base cache metadata: %w", err)
	}
	metadataBytes = append(metadataBytes, '\n')
	if err := os.WriteFile(filepath.Join(pendingDir, "metadata.json"), metadataBytes, 0o644); err != nil {
		return fmt.Errorf("write rootfs base cache metadata: %w", err)
	}
	if err := os.Chmod(pendingDir, 0o755); err != nil {
		return fmt.Errorf("write rootfs base cache metadata: %w", err)
	}
	// Move any existing entry aside first, so the window in which the entry is
	// absent is a cache miss rather than a torn entry.
	supersededDir := ""
	if _, err := os.Stat(entryDir); err == nil {
		supersededDir = entryDir + baseStageCacheSupersededSuffix
		_ = os.RemoveAll(supersededDir)
		if err := os.Rename(entryDir, supersededDir); err != nil {
			return fmt.Errorf("replace rootfs base cache entry: %w", err)
		}
	}
	if err := os.Rename(pendingDir, entryDir); err != nil {
		if supersededDir != "" {
			_ = os.Rename(supersededDir, entryDir)
		}
		return fmt.Errorf("publish rootfs base cache entry: %w", err)
	}
	if supersededDir != "" {
		_ = os.RemoveAll(supersededDir)
	}
	reapBaseStageCache(cacheDir)
	return nil
}

// baseStageCacheMaxEntries bounds the cache: every tag update strands its
// previous digest's entry, so without a bound the cache grows by one
// extracted rootfs per image update forever. Eviction is by
// least-recently-used entry (hits refresh mtime), which cannot thrash
// between distinct images the way per-repo replacement would.
const baseStageCacheMaxEntries = 16

// baseStageCacheLitterMaxAge protects live publishes in other processes: a
// pending or superseded directory younger than this may belong to a save
// that is still running and is left alone. Real publishes finish in
// seconds; anything this old is crash debris.
const baseStageCacheLitterMaxAge = time.Hour

// reapBaseStageCache is the save-side janitor. It removes swap litter old
// enough to be crash debris, entries whose directory name does not
// reproduce from their own metadata (entries from earlier cache layouts,
// or corrupted ones), and — beyond baseStageCacheMaxEntries — the oldest
// entries by mtime. It only ever touches names shaped like cache entries
// (64 hex chars) or swap litter, so a cache dir override pointed at a
// directory with unrelated content cannot lose that content. Everything
// here is best-effort: the cache must never fail a build.
func reapBaseStageCache(cacheDir string) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	type liveEntry struct {
		path    string
		modTime time.Time
	}
	var live []liveEntry
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(cacheDir, name)
		if isBaseStageCacheTransient(name) {
			if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > baseStageCacheLitterMaxAge {
				_ = os.RemoveAll(path)
			}
			continue
		}
		if !isBaseStageCacheEntryName(name) {
			continue
		}
		if !entry.IsDir() {
			_ = os.Remove(path)
			continue
		}
		metadataBytes, err := os.ReadFile(filepath.Join(path, "metadata.json"))
		if err != nil {
			_ = os.RemoveAll(path)
			continue
		}
		var metadata baseStageCacheMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			_ = os.RemoveAll(path)
			continue
		}
		if baseStageCacheEntryDir(cacheDir, metadata.Digest, metadata.Platform, metadata.SetuidPolicy) != path {
			_ = os.RemoveAll(path)
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		live = append(live, liveEntry{path: path, modTime: info.ModTime()})
	}
	if len(live) <= baseStageCacheMaxEntries {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].modTime.After(live[j].modTime) })
	for _, entry := range live[baseStageCacheMaxEntries:] {
		_ = os.RemoveAll(entry.path)
	}
}

// BaseCacheEntry identifies one digest-keyed base-stage cache entry.
type BaseCacheEntry struct {
	Digest    string   `json:"digest"`
	Platform  Platform `json:"platform"`
	SizeBytes int64    `json:"size_bytes"`
}

// ClearBaseCache removes the base-stage cache entries selected by remove
// (nil selects every entry) plus any swap litter, and reports what was
// removed. Entries that are unreadable or from an earlier cache layout are
// always removed regardless of the selector: they can never hit and only
// hold space. Like the reaper, it only touches names shaped like cache
// entries or swap litter, so unrelated content in a misdirected cache dir
// survives. A missing cache dir is an empty cache, not an error.
func ClearBaseCache(cacheDir string, remove func(BaseCacheEntry) bool) ([]BaseCacheEntry, error) {
	entries, err := os.ReadDir(cacheDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []BaseCacheEntry
	var firstErr error
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(cacheDir, name)
		if isBaseStageCacheTransient(name) {
			if err := os.RemoveAll(path); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !isBaseStageCacheEntryName(name) {
			continue
		}
		record := BaseCacheEntry{}
		valid := false
		if metadataBytes, err := os.ReadFile(filepath.Join(path, "metadata.json")); err == nil {
			var metadata baseStageCacheMetadata
			if json.Unmarshal(metadataBytes, &metadata) == nil &&
				baseStageCacheEntryDir(cacheDir, metadata.Digest, metadata.Platform, metadata.SetuidPolicy) == path {
				record = BaseCacheEntry{Digest: metadata.Digest, Platform: metadata.Platform}
				valid = true
			}
		}
		if valid && remove != nil && !remove(record) {
			continue
		}
		record.SizeBytes = dirSizeBytes(path)
		if err := os.RemoveAll(path); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed = append(removed, record)
	}
	return removed, firstErr
}

func dirSizeBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func isBaseStageCacheEntryName(name string) bool {
	if len(name) != sha256.Size*2 {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// isBaseStageCacheTransient reports whether a directory is one of the two that
// exist only mid-swap. A crash during a swap should leave litter, never a hit.
func isBaseStageCacheTransient(name string) bool {
	return strings.HasPrefix(name, baseStageCachePendingPrefix) ||
		strings.HasSuffix(name, baseStageCacheSupersededSuffix)
}

// baseStageCachePendingPrefix and baseStageCacheSupersededSuffix name the
// transient directories that exist only during a swap. Entry lookup skips
// them, so a crash mid-swap leaves litter rather than a cache hit.
const (
	baseStageCachePendingPrefix    = ".pending-"
	baseStageCacheSupersededSuffix = ".superseded"
)

// baseStageCacheEntryDir keys entries by the resolved manifest digest, not
// the requested ref. Content-addressing is what makes the cache safe to
// share across sources: two same-named images from different sources (a
// registry tag and a locally committed image, say) have different digests
// and therefore different entries, and a tag that moves upstream resolves
// to a digest the old entry cannot answer for.
func baseStageCacheEntryDir(cacheDir, digest string, platform Platform, setuidPolicy string) string {
	// setuidPolicy is part of the key: the two policies produce different
	// trees for the same digest. Folding it into the hash also orphans every
	// entry written before the policy existed — those trees carry setuid bits
	// in their sidecars and must never be restored into a stripped build; the
	// LRU reaper ages them out.
	sum := sha256.Sum256([]byte(digest + "\x00" + platform.OS + "\x00" + platform.Architecture + "\x00" + platform.Variant + "\x00" + setuidPolicy))
	return filepath.Join(cacheDir, hex.EncodeToString(sum[:]))
}

// BaseCacheDirFor returns the base-stage cache directory for a state
// directory: <stateDir>/build/base-cache. The
// MICROAGENT_ROOTFS_BASE_CACHE_DIR environment variable overrides it — set
// to a path to relocate the cache, set to an empty value to disable
// caching entirely.
func BaseCacheDirFor(stateDir string) string {
	if value, ok := os.LookupEnv("MICROAGENT_ROOTFS_BASE_CACHE_DIR"); ok {
		return strings.TrimSpace(value)
	}
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "build", "base-cache")
}

func copyBaseStageCache(src, dst string) error {
	return copyStage(src, dst)
}

func buildRootfsImage(ctx context.Context, req BuildRequest, stageDir, tmpDir string, progress *progressReporter, provenance *Provenance) error {
	sizeBytes := req.SizeMiB * 1024 * 1024
	headroom := req.HeadroomMiB * 1024 * 1024
	if headroom <= 0 {
		headroom = defaultHeadroomBytes
	}
	if req.DeriveSize {
		// Nothing pinned a size: the content decides, in either direction.
		derived, err := autoSizeBytes(stageDir, 0, headroom)
		if err == nil && derived > 0 && derived != sizeBytes {
			sizeBytes = derived
			progress.emit("size", fmt.Sprintf("disk sized to %d MiB from image content", derived/(1024*1024)), 0, 0, 0, 0)
		}
	} else if req.AutoSize {
		grown, err := autoSizeBytes(stageDir, sizeBytes, headroom)
		if err == nil && grown > sizeBytes {
			sizeBytes = grown
			progress.emit("size", fmt.Sprintf("disk grown to %d MiB to fit the image", grown/(1024*1024)), 0, 0, 0, 0)
		}
	}
	switch req.Format {
	case FormatExt4:
		provenance.BuilderPhase = "build-ext4"
		progress.emit("build-ext4", "building ext4 image", 0, 0, 0, 0)
		return buildExt4Image(ctx, req.Mke2fsPath, req.DebugfsPath, stageDir, filepath.Join(tmpDir, "rootfs.ext4"), req.OutputPath, sizeBytes, "rootfs")
	default:
		return fmt.Errorf("format must be %q", FormatExt4)
	}
}

// defaultHeadroomBytes is the writable space a derived or auto-grown disk
// guarantees the guest beyond the image content when the caller sets no
// headroom of its own.
const defaultHeadroomBytes = int64(512 * 1024 * 1024)

// autoSizeBytes returns the disk size to use when the caller did not pin one.
// A stage that fits keeps the requested size. One that doesn't gets the
// smallest GiB multiple holding the data, filesystem overhead, and the
// headroom of writable space for the guest. A zero requestedBytes means
// "derive purely from content": no floor, the computed size wins.
func autoSizeBytes(stageDir string, requestedBytes, headroomBytes int64) (int64, error) {
	const gib = int64(1024 * 1024 * 1024)
	if headroomBytes <= 0 {
		headroomBytes = defaultHeadroomBytes
	}
	dataBytes, err := stageDataBytes(stageDir)
	if err != nil {
		return requestedBytes, err
	}
	if requestedBytes > 0 && dataBytes+ext4MinOverheadBytes <= requestedBytes {
		return requestedBytes, nil
	}
	overhead := dataBytes / 20
	if overhead < 2*ext4MinOverheadBytes {
		overhead = 2 * ext4MinOverheadBytes
	}
	needed := dataBytes + overhead + headroomBytes
	grown := (needed + gib - 1) / gib * gib
	if grown < requestedBytes {
		return requestedBytes, nil
	}
	return grown, nil
}

func buildBundleImage(ctx context.Context, req BundleRequest, stageDir, tmpDir string, provenance *BundleProvenance) error {
	sizeBytes := req.SizeMiB * 1024 * 1024
	if req.AutoSize {
		if grown, err := autoSizeBytes(stageDir, sizeBytes, 0); err == nil && grown > sizeBytes {
			sizeBytes = grown
		}
	}
	switch req.Format {
	case FormatExt4:
		provenance.BuilderPhase = "build-ext4"
		return buildExt4Image(ctx, req.Mke2fsPath, req.DebugfsPath, stageDir, filepath.Join(tmpDir, "bundle.ext4"), req.OutputPath, sizeBytes, "bundle")
	default:
		return fmt.Errorf("format must be %q", FormatExt4)
	}
}

// ext4MinOverheadBytes is deliberately below the real metadata overhead
// (inode tables, journal, group descriptors), so the preflight check only
// rejects builds mke2fs could never complete.
const ext4MinOverheadBytes = 32 * 1024 * 1024

// stageDataBytes estimates the ext4 data footprint of a staged tree: regular
// file contents rounded up to 4 KiB blocks with hard links counted once, plus
// one block per directory and symlink.
func stageDataBytes(stageDir string) (int64, error) {
	const blockSize = 4096
	seen := map[string]struct{}{}
	var total int64
	err := filepath.WalkDir(stageDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == stageDir {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			total += blockSize
			return nil
		}
		if id, ok := stageHardLinkID(path, info); ok {
			if _, dup := seen[id]; dup {
				return nil
			}
			seen[id] = struct{}{}
		}
		total += (info.Size() + blockSize - 1) / blockSize * blockSize
		return nil
	})
	return total, err
}

func checkStageFits(stageDir string, sizeBytes int64, label string) error {
	dataBytes, err := stageDataBytes(stageDir)
	if err != nil || dataBytes+ext4MinOverheadBytes <= sizeBytes {
		return nil
	}
	needMiB := (dataBytes + ext4MinOverheadBytes) / (1024 * 1024)
	suggestMiB := (needMiB/1024 + 1) * 1024
	hint := fmt.Sprintf("give it a larger size (at least %d MiB)", suggestMiB)
	if label == "rootfs" {
		hint = fmt.Sprintf("give the workspace a larger disk, for example --size-mib %d, or drop the pinned size to let the disk grow to fit", suggestMiB)
	}
	return fmt.Errorf("%s contents need about %d MiB but the %s disk size is %d MiB; %s", label, needMiB, label, sizeBytes/(1024*1024), hint)
}

func buildExt4Image(ctx context.Context, mke2fsPath, debugfsPath, stageDir, tmpImage, outputPath string, sizeBytes int64, label string) error {
	if err := checkStageFits(stageDir, sizeBytes, label); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := allocateFile(tmpImage, sizeBytes); err != nil {
		return fmt.Errorf("allocate %s image: %w", label, err)
	}
	cmd := exec.CommandContext(ctx, mke2fsPath, "-q", "-t", "ext4", "-d", stageDir, tmpImage)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build ext4 %s: %w: %s", label, err, strings.TrimSpace(string(out)))
	}
	if debugfsPath != "" {
		if err := applyExt4Metadata(ctx, debugfsPath, stageDir, tmpImage); err != nil {
			return fmt.Errorf("apply %s filesystem metadata: %w", label, err)
		}
	}
	if err := os.Rename(tmpImage, outputPath); err != nil {
		return fmt.Errorf("commit %s image: %w", label, err)
	}
	return nil
}

func allocateFile(path string, sizeBytes int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := f.Truncate(sizeBytes); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

type progressReporter struct {
	fn       ProgressFunc
	lastEmit time.Time
}

func newProgressReporter(fn ProgressFunc) *progressReporter {
	return &progressReporter{fn: fn}
}

func (p *progressReporter) emit(phase, message string, current, total, bytesDone, totalBytes int64) {
	if p == nil || p.fn == nil {
		return
	}
	p.lastEmit = time.Now()
	p.fn(ProgressEvent{
		Phase:      phase,
		Message:    message,
		Current:    current,
		Total:      total,
		Bytes:      bytesDone,
		TotalBytes: totalBytes,
	})
}

func (p *progressReporter) emitThrottled(phase, message string, current, total, bytesDone, totalBytes int64) {
	if p == nil || p.fn == nil {
		return
	}
	if time.Since(p.lastEmit) < 500*time.Millisecond {
		return
	}
	p.emit(phase, message, current, total, bytesDone, totalBytes)
}

type progressReadCloser struct {
	io.ReadCloser
	OnRead func(int64)
}

func (r *progressReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.OnRead != nil {
		r.OnRead(int64(n))
	}
	return n, err
}

func descriptorSize(descriptors ...ocispec.Descriptor) int64 {
	var total int64
	for _, descriptor := range descriptors {
		if descriptor.Size > 0 {
			total += descriptor.Size
		}
	}
	return total
}

// ValidateImageRef reports whether raw parses as an OCI image reference,
// using the same normalization and parser the builder applies when it pulls.
// It touches neither the network nor the filesystem, so callers can reject a
// doomed configuration before spending anything on it — a dry run that
// accepts a ref the real build's first parse would refuse is not validating.
func ValidateImageRef(raw string) error {
	_, _, err := splitRegistryReference(raw)
	return err
}

func splitRegistryReference(raw string) (repoRef, reference string, err error) {
	raw = normalizeRegistryReference(raw)
	ref, err := registry.ParseReference(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse OCI image ref %q: %w", raw, err)
	}
	reference = ref.Reference
	if reference == "" {
		reference = "latest"
	}
	return ref.Registry + "/" + ref.Repository, reference, nil
}

func normalizeRegistryReference(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	first, rest, hasSlash := strings.Cut(raw, "/")
	if !hasSlash {
		return "docker.io/library/" + raw
	}
	if isExplicitRegistry(first) {
		if first == "docker.io" && !strings.Contains(rest, "/") {
			return "docker.io/library/" + rest
		}
		return raw
	}
	return "docker.io/" + raw
}

func isExplicitRegistry(component string) bool {
	return component == "localhost" || strings.Contains(component, ".") || strings.Contains(component, ":")
}

func newRepository(repoRef string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, err
	}
	host := strings.SplitN(repoRef, "/", 2)[0]
	repo.PlainHTTP = isLoopbackRegistry(host)
	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.DefaultCache,
		Credential: registryauth.Credential(host),
	}
	return repo, nil
}

// localImageLayoutExists reports whether an OCI image layout has already
// been initialized at path, by checking for its oci-layout marker file.
// oci.New unconditionally creates the {blobs,index.json,oci-layout} scaffold
// for any path that doesn't already have one, so callers must confirm the
// layout already exists before calling it -- otherwise a build that never
// consults a local image (a remote-only create or pull) would still leave
// that scaffold behind under LocalImageLayout as a side effect.
func localImageLayoutExists(path string) bool {
	_, err := os.Stat(filepath.Join(path, ocispec.ImageLayoutFile))
	return err == nil
}

func isLoopbackRegistry(host string) bool {
	host = strings.TrimSpace(host)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func fetchBytes(ctx context.Context, src oras.ReadOnlyTarget, desc ocispec.Descriptor) ([]byte, error) {
	rc, err := src.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func validateImagePlatform(config ocispec.Image, platform Platform) error {
	if config.OS != "" && config.OS != platform.OS {
		return fmt.Errorf("OCI image OS = %s, want %s", config.OS, platform.OS)
	}
	if config.Architecture != "" && config.Architecture != platform.Architecture {
		return fmt.Errorf("OCI image architecture = %s, want %s", config.Architecture, platform.Architecture)
	}
	if platform.Variant != "" && config.Variant != "" && config.Variant != platform.Variant {
		return fmt.Errorf("OCI image variant = %s, want %s", config.Variant, platform.Variant)
	}
	return nil
}

func extractLayer(stageDir, mediaType string, rc io.Reader, stripper *setuidStripper) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	reader := rc
	if strings.Contains(mediaType, "gzip") || strings.HasSuffix(mediaType, ".gzip") || strings.HasSuffix(mediaType, "+gzip") {
		gz, err := gzip.NewReader(rc)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	}
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := applyTarEntry(root, header, tr, stripper); err != nil {
			return err
		}
	}
}

func applyTarEntry(root *os.Root, header *tar.Header, reader io.Reader, stripper *setuidStripper) error {
	name, err := safeGuestRel(header.Name, false)
	if err != nil {
		if errors.Is(err, errRootPath) {
			if header.Typeflag != tar.TypeDir {
				return nil
			}
			if header.Uid < 0 || header.Gid < 0 {
				return fmt.Errorf("OCI layer root has negative uid or gid")
			}
			rootRecord := stageMetadataRecord{
				Version: stageMetadataVersion,
				Path:    ".",
				Type:    "directory",
				Mode:    header.Mode & 0o7777,
				UID:     header.Uid,
				GID:     header.Gid,
				Xattrs:  tarHeaderXattrs(header),
			}
			if !header.ModTime.IsZero() {
				mtime := header.ModTime.Unix()
				rootRecord.Mtime = &mtime
			}
			return recordStageMetadata(root, rootRecord)
		}
		return err
	}
	if name == "." {
		return nil
	}
	if name == stageMetadataName {
		return fmt.Errorf("OCI layer path %q is reserved for extraction metadata", header.Name)
	}
	base := path.Base(name)
	dir := path.Dir(name)
	if base == ".wh..wh..opq" {
		targetDir, err := safeGuestRel(dir, true)
		if err != nil {
			return err
		}
		return removeDirectoryChildren(root, targetDir)
	}
	if strings.HasPrefix(base, ".wh.") {
		target, err := safeGuestRel(path.Join(dir, strings.TrimPrefix(base, ".wh.")), false)
		if err != nil {
			return err
		}
		if target == stageMetadataName {
			return fmt.Errorf("OCI whiteout target %q is reserved for extraction metadata", target)
		}
		return root.RemoveAll(target)
	}
	mode := stripper.apply(name, modeFromTarHeader(header.Mode))
	// os.Root.MkdirAll and os.Root.OpenFile reject any FileMode bits outside
	// the low 9 permission bits, so setuid/setgid/sticky (encoded by Go far
	// outside that range) must not reach them here; root.Chmod below
	// translates those bits to the raw syscall values and applies them once
	// the entry exists.
	if header.Uid < 0 || header.Gid < 0 {
		return fmt.Errorf("OCI layer path %q has negative uid or gid", header.Name)
	}
	record := stageMetadataRecord{
		Version: stageMetadataVersion,
		Path:    name,
		Mode:    posixModeBits(mode),
		UID:     header.Uid,
		GID:     header.Gid,
		Xattrs:  tarHeaderXattrs(header),
	}
	if !header.ModTime.IsZero() {
		mtime := header.ModTime.Unix()
		record.Mtime = &mtime
	}
	switch header.Typeflag {
	case tar.TypeDir:
		record.Type = "directory"
		if err := root.MkdirAll(name, mode.Perm()); err != nil {
			return err
		}
	// archive/tar normalizes legacy TypeRegA headers to TypeReg on read.
	case tar.TypeReg, tar.TypeGNUSparse:
		record.Type = "regular"
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		_ = root.RemoveAll(name)
		out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, reader); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	case tar.TypeSymlink:
		record.Type = "symlink"
		linkTarget, err := safeSymlinkTarget(name, header.Linkname)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		_ = root.RemoveAll(name)
		if err := root.Symlink(linkTarget, name); err != nil {
			if !canFallbackToSymlinkMarker(err) {
				return err
			}
			if err := writeSymlinkMarkerInRoot(root, name, linkTarget); err != nil {
				return err
			}
			return recordStageMetadata(root, record)
		}
		if err := recordStageMetadata(root, record); err != nil {
			return err
		}
		return nil
	case tar.TypeLink:
		record.Type = "hardlink"
		linkTarget, err := safeGuestRel(header.Linkname, false)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		_ = root.RemoveAll(name)
		if err := root.Link(linkTarget, name); err != nil {
			return err
		}
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		if header.Devmajor < 0 || header.Devminor < 0 {
			return fmt.Errorf("OCI layer path %q has a negative device number", header.Name)
		}
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		_ = root.RemoveAll(name)
		placeholder, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := placeholder.Close(); err != nil {
			return err
		}
		record.DevMajor = header.Devmajor
		record.DevMinor = header.Devminor
		switch header.Typeflag {
		case tar.TypeChar:
			record.Type = "character"
		case tar.TypeBlock:
			record.Type = "block"
		default:
			record.Type = "fifo"
		}
	case byte('s'):
		return fmt.Errorf("OCI layer path %q is a socket, which cannot be materialized in a rootfs", header.Name)
	default:
		return nil
	}
	if record.Type != "hardlink" {
		if err := root.Chmod(name, mode); err != nil {
			return err
		}
	}
	return recordStageMetadata(root, record)
}

func tarHeaderXattrs(header *tar.Header) map[string][]byte {
	result := map[string][]byte{}
	for key, value := range header.PAXRecords {
		const prefix = "SCHILY.xattr."
		if strings.HasPrefix(key, prefix) && len(key) > len(prefix) {
			result[strings.TrimPrefix(key, prefix)] = []byte(value)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func recordStageMetadata(root *os.Root, record stageMetadataRecord) error {
	record.Path = path.Clean(strings.TrimPrefix(filepath.ToSlash(record.Path), "/"))
	if record.Path == stageMetadataName {
		return nil
	}
	record.Version = stageMetadataVersion
	if err := ensureStageMetadataFile(root); err != nil {
		return err
	}
	out, err := root.OpenFile(stageMetadataName, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open stage metadata: %w", err)
	}
	if err := json.NewEncoder(out).Encode(record); err != nil {
		_ = out.Close()
		return fmt.Errorf("write stage metadata: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close stage metadata: %w", err)
	}
	return nil
}

// setuidPolicyFor names the policy a request's AllowGuestSetuid selected.
func setuidPolicyFor(allow bool) string {
	if allow {
		return SetuidPolicyPreserved
	}
	return SetuidPolicyStripped
}

// setuidStripper applies the setuid policy at the one point where source
// modes become staged modes, and remembers what it stripped. The staged mode
// feeds both the host-side chmod and the stage-metadata sidecar the debugfs
// pass replays into the ext4 image, so stripping here covers both routes a
// mode bit has into the guest. Sticky is untouched: it is a shared-tmp-dir
// convention, not a privilege bit.
type setuidStripper struct {
	allow    bool
	stripped []string
}

func (s *setuidStripper) apply(name string, mode os.FileMode) os.FileMode {
	if s.allow || mode&(os.ModeSetuid|os.ModeSetgid) == 0 {
		return mode
	}
	s.stripped = append(s.stripped, name)
	return mode &^ (os.ModeSetuid | os.ModeSetgid)
}

// finalize dedupes (a path can recur across layers) and returns the uncapped
// total plus the recorded list, capped at setuidStrippedListCap.
func (s *setuidStripper) finalize() (int, []string) {
	if len(s.stripped) == 0 {
		return 0, nil
	}
	sort.Strings(s.stripped)
	unique := slices.Compact(s.stripped)
	capped := unique
	if len(capped) > setuidStrippedListCap {
		capped = capped[:setuidStrippedListCap]
	}
	return len(unique), append([]string(nil), capped...)
}

// modeFromTarHeader maps a tar header's raw POSIX mode onto an os.FileMode,
// preserving the setuid/setgid/sticky bits that FileMode(header.Mode).Perm()
// would silently drop. Whether setuid/setgid then survive into the stage is
// the setuidStripper's decision (strip by default, preserve on request);
// sticky always survives so the shared-sticky-dir convention (e.g. /tmp)
// works as the source image declared it.
func modeFromTarHeader(raw int64) os.FileMode {
	mode := os.FileMode(raw) & os.ModePerm
	if raw&0o4000 != 0 {
		mode |= os.ModeSetuid
	}
	if raw&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	if raw&0o1000 != 0 {
		mode |= os.ModeSticky
	}
	return mode
}

func ensureStageMetadataFile(root *os.Root) error {
	f, err := root.OpenFile(stageMetadataName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create stage metadata: %w", err)
	}
	if err := initializeStageMetadata(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("initialize stage metadata: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close stage metadata: %w", err)
	}
	return nil
}

var errRootPath = errors.New("OCI layer path is root")

// Layer paths are slash-separated and this package is POSIX-only (see the
// !windows build tags), so a backslash is an ordinary name character, not a
// separator. Real images depend on that: systemd escapes "-" as `\x2d` in
// unit file names, so any image with systemd installed ships
// backslash-bearing paths that must extract intact.
func safeGuestRel(guestPath string, allowRoot bool) (string, error) {
	if strings.ContainsRune(guestPath, 0) {
		return "", fmt.Errorf("unsafe OCI layer path %q", guestPath)
	}
	if path.IsAbs(guestPath) {
		return "", fmt.Errorf("unsafe OCI layer path %q", guestPath)
	}
	rel := path.Clean(guestPath)
	if rel == "." || rel == "" {
		if allowRoot {
			return ".", nil
		}
		return "", errRootPath
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("unsafe OCI layer path %q", guestPath)
	}
	return rel, nil
}

func safeSymlinkTarget(linkName, linkTarget string) (string, error) {
	if linkTarget == "" || strings.ContainsRune(linkTarget, 0) {
		return "", fmt.Errorf("unsafe OCI symlink target %q", linkTarget)
	}
	if path.IsAbs(linkTarget) {
		guestRel := strings.TrimPrefix(linkTarget, "/")
		if guestRel == "" {
			return linkTarget, nil
		}
		if _, err := safeGuestRel(guestRel, false); err != nil {
			return "", fmt.Errorf("unsafe OCI symlink target %q", linkTarget)
		}
		return linkTarget, nil
	}
	resolved := path.Clean(path.Join(path.Dir(linkName), linkTarget))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", fmt.Errorf("unsafe OCI symlink target %q", linkTarget)
	}
	return linkTarget, nil
}

func removeDirectoryChildren(root *os.Root, dir string) error {
	f, err := root.Open(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if dir == "." && entry.Name() == stageMetadataName {
			continue
		}
		if err := root.RemoveAll(path.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// writeInit installs the guest init. The binary branch only copies the
// microagent-guestinit binary: every per-workspace fact — command, env,
// ports, mounts, forwards, console shell — reaches the guest on the
// per-boot config disk, never inside the image. The script fallback (no
// binary; `rootfs build`'s low-level images) still inlines command and env
// because those images boot with no config channel at all.
func writeInit(stageDir, initPath string, command []string, env map[string]string, initBinaryPath, consoleShell string) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	target, err := safeStageRel(initPath)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(path.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create init dir: %w", err)
	}
	if initBinaryPath != "" {
		if err := copyFileToRoot(root, initBinaryPath, target, 0o755); err != nil {
			return fmt.Errorf("copy init binary: %w", err)
		}
		return nil
	}
	var commandLine string
	if len(command) > 0 {
		quoted := make([]string, 0, len(command))
		for _, arg := range command {
			quoted = append(quoted, shellQuote(arg))
		}
		commandLine = "set -- " + strings.Join(quoted, " ") + "\n"
	}
	consoleShell = strings.TrimSpace(consoleShell)
	if consoleShell == "" {
		consoleShell = "/bin/sh"
	}
	script := "#!/bin/sh\nset -eu\nmkdir -p /proc /sys /dev\nmount -t proc proc /proc || true\nmount -t sysfs sysfs /sys || true\n" +
		envLines(env) +
		commandLine +
		"if [ \"$#\" -gt 0 ]; then\n  set +e\n  \"$@\"\n  status=\"$?\"\n  set -e\n  poweroff -f || halt -f || reboot -f || true\n  exit \"$status\"\nfi\nexec /bin/sh\n"
	script = strings.Replace(script, "exec /bin/sh\n", "exec "+shellQuote(consoleShell)+"\n", 1)
	if err := writeBytesToRoot(root, target, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write init: %w", err)
	}
	return nil
}

// writeDeclaredFiles needs no setuid policy: ParseFileMode confines declared
// modes to the low permission bits and the no-mode default masks to 0o644,
// so no setuid/setgid bit can enter the stage through a declared File.
func writeDeclaredFiles(stageDir string, files []File) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, file := range files {
		target, err := safeStageRel(file.Path)
		if err != nil {
			return fmt.Errorf("file %s: %w", file.Path, err)
		}
		info, err := os.Stat(file.SourcePath)
		if err != nil {
			return fmt.Errorf("file src %q: %w", file.SourcePath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file src must be a regular file: %s", file.SourcePath)
		}
		mode := info.Mode().Perm() & 0o644
		if strings.TrimSpace(file.Mode) != "" {
			mode, err = ParseFileMode(file.Mode)
			if err != nil {
				return fmt.Errorf("file %s mode: %w", file.Path, err)
			}
		}
		if err := copyFileToRoot(root, file.SourcePath, target, mode); err != nil {
			return fmt.Errorf("copy file %s to %s: %w", file.SourcePath, file.Path, err)
		}
	}
	return nil
}

func copyFileToRoot(root *os.Root, src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := root.MkdirAll(path.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := root.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := root.Chmod(dst, mode); err != nil {
		return err
	}
	// Host-authored files (init binary, declared Files) are not attributed to
	// the OCI image, so they are guest-root-owned rather than inheriting
	// whichever host user ran the build.
	return recordStageMode(root, dst, 0, 0, mode)
}

func writeBytesToRoot(root *os.Root, dst string, data []byte, mode os.FileMode) error {
	out, err := root.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := root.Chmod(dst, mode); err != nil {
		return err
	}
	return recordStageMode(root, dst, 0, 0, mode)
}

func recordStageMode(root *os.Root, name string, uid, gid int, mode os.FileMode) error {
	return recordStageMetadata(root, stageMetadataRecord{
		Path: name,
		Mode: posixModeBits(mode),
		UID:  uid,
		GID:  gid,
	})
}

// posixModeBits converts an os.FileMode back to a raw POSIX permission value
// (0-07777), the inverse of modeFromTarHeader, so the stage metadata sidecar
// and the debugfs correction pass both work in the same numeric space.
func posixModeBits(mode os.FileMode) int64 {
	bits := int64(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}

func ensureGuestRuntimeDirs(stageDir string) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, dir := range []string{"proc", "sys", "dev", "dev/pts"} {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create guest runtime dir %s: %w", dir, err)
		}
		if err := recordStageMode(root, dir, 0, 0, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func safeStageRel(guestPath string) (string, error) {
	if strings.ContainsRune(guestPath, 0) {
		return "", fmt.Errorf("guest path contains NUL")
	}
	clean := path.Clean(strings.ReplaceAll(guestPath, "\\", "/"))
	if !path.IsAbs(clean) || clean == "/" {
		return "", fmt.Errorf("guest path must be absolute and below root")
	}
	rel := strings.TrimPrefix(clean, "/")
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("guest path must stay below root")
	}
	return rel, nil
}

func envLines(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		if validShellEnvName(key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString("export ")
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(shellQuote(env[key]))
		b.WriteString("\n")
	}
	return b.String()
}

func validShellEnvName(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z' && i > 0:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func copyStage(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("remove stage snapshot: %w", err)
	}
	hardLinks := map[string]string{}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if id, linked := stageHardLinkID(path, info); linked {
			if first, seen := hardLinks[id]; seen {
				return os.Link(first, target)
			}
			hardLinks[id] = target
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = in.Close()
			_ = out.Close()
			return err
		}
		if err := in.Close(); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}
