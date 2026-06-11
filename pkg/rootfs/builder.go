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
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
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

	cacheDir := strings.TrimSpace(os.Getenv("MICROAGENT_ROOTFS_BASE_CACHE_DIR"))
	cacheRefresh := strings.TrimSpace(os.Getenv("MICROAGENT_ROOTFS_BASE_CACHE_REFRESH")) == "1"
	if cacheDir != "" && !cacheRefresh {
		metadata, ok, err := restoreBaseStageCache(cacheDir, req, stageDir)
		if err != nil {
			return provenance, err
		}
		if ok {
			provenance.ResolvedRef = metadata.ResolvedRef
			provenance.Digest = metadata.Digest
			provenance.LayerDigests = append([]string{}, metadata.LayerDigests...)
			provenance.BuilderPhase = "restore-base-cache"
			progress.emit("restore-base-cache", "restoring base rootfs cache", 1, 1, 0, 0)
			imageConfig = metadata.ImageConfig
		}
	}
	if provenance.BuilderPhase != "restore-base-cache" {
		repoRef, reference, err := splitRegistryReference(req.ImageRef)
		if err != nil {
			return provenance, err
		}
		repo, err := newRepository(repoRef)
		if err != nil {
			return provenance, err
		}
		provenance.BuilderPhase = "fetch-manifest"
		progress.emit("fetch-manifest", "fetching manifest", 0, 0, 0, 0)
		manifestDesc, manifestBytes, err := oras.FetchBytes(ctx, repo, reference, oras.FetchBytesOptions{
			FetchOptions: oras.FetchOptions{
				ResolveOptions: oras.ResolveOptions{TargetPlatform: &platform},
			},
		})
		if err != nil {
			return provenance, fmt.Errorf("fetch OCI image %s for %s/%s: %w", req.ImageRef, platform.OS, platform.Architecture, err)
		}
		provenance.Digest = manifestDesc.Digest.String()
		provenance.ResolvedRef = repoRef + "@" + manifestDesc.Digest.String()

		var manifest ocispec.Manifest
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			return provenance, fmt.Errorf("parse OCI image manifest: %w", err)
		}
		progress.emit("fetch-config", "fetching image config", 0, 0, 0, 0)
		configBytes, err := fetchBytes(ctx, repo, manifest.Config)
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
			rc, err := repo.Fetch(ctx, layer)
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
			if err := extractLayer(stageDir, layer.MediaType, reader); err != nil {
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
		if cacheDir != "" {
			if err := saveBaseStageCache(cacheDir, req, provenance, imageConfig, stageDir); err != nil {
				return provenance, err
			}
		}
	}

	provenance.BuilderPhase = "write-init"
	progress.emit("write-init", "writing guest init", 0, 0, 0, 0)
	command := buildCommand(req, imageConfig)
	if req.ResetFinalConfig {
		command, err = appendGuestConfigReset(command, req, imageConfig)
		if err != nil {
			return provenance, err
		}
	}
	if err := writeInit(stageDir, req.InitPath, command, req.Mode, buildGuestEnv(req.Env, imageConfig), req.InitBinaryPath, req.ResultPort, req.ShellPort, req.ExecPort, req.Mounts, req.HostForwards, req.ConsoleShell, req.Hostname); err != nil {
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

func buildCommand(req BuildRequest, imageConfig ocispec.Image) []string {
	command := append([]string{}, req.Command...)
	if len(command) == 0 && !req.NoImageCommand {
		command = append([]string{}, imageConfig.Config.Entrypoint...)
		command = append(command, imageConfig.Config.Cmd...)
	}
	return command
}

// appendGuestConfigReset appends a line to the setup script that rewrites
// /etc/microagent/run.json for later boots. The env written is the same
// image-config + request merge as the initial guest config, so a setup boot
// does not strip image env (PATH and friends) from the workspace.
func appendGuestConfigReset(command []string, req BuildRequest, imageConfig ocispec.Image) ([]string, error) {
	if len(command) != 3 || command[0] != "/bin/sh" || command[1] != "-lc" {
		return nil, fmt.Errorf("guest config reset requires a /bin/sh -lc script command, got %q", command)
	}
	final := req.FinalCommand
	if final == nil {
		final = []string{}
	}
	data, err := json.Marshal(guestRunConfig{
		Command:      final,
		Mode:         strings.TrimSpace(req.FinalMode),
		Env:          envList(buildGuestEnv(req.Env, imageConfig)),
		Port:         req.ResultPort,
		ShellPort:    req.ShellPort,
		ExecPort:     req.ExecPort,
		Mounts:       req.Mounts,
		HostForwards: req.HostForwards,
		ConsoleShell: strings.TrimSpace(req.ConsoleShell),
		Hostname:     strings.TrimSpace(req.Hostname),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal guest run config reset: %w", err)
	}
	out := append([]string{}, command...)
	out[2] += "\nprintf '%s\\n' " + shellQuote(string(data)) + " > /etc/microagent/run.json"
	return out, nil
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
	if err := extractLayer(stageDir, mediaType, source); err != nil {
		_ = source.Close()
		return provenance, fmt.Errorf("extract bundle: %w", err)
	}
	if err := source.Close(); err != nil {
		return provenance, fmt.Errorf("close bundle: %w", err)
	}
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

func restoreBaseStageCache(cacheDir string, req BuildRequest, stageDir string) (baseStageCacheMetadata, bool, error) {
	entryDir := baseStageCacheEntryDir(cacheDir, req.ImageRef, req.Platform)
	metadataPath := filepath.Join(entryDir, "metadata.json")
	baseDir := filepath.Join(entryDir, "base")
	metadataBytes, err := os.ReadFile(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		if metadata, ok := findBaseStageCacheMetadataForImage(cacheDir, req.ImageRef); ok {
			if err := validateImagePlatform(metadata.ImageConfig, req.Platform); err != nil {
				return baseStageCacheMetadata{}, false, err
			}
		}
		return baseStageCacheMetadata{}, false, nil
	}
	if err != nil {
		return baseStageCacheMetadata{}, false, fmt.Errorf("read rootfs base cache metadata: %w", err)
	}
	var metadata baseStageCacheMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return baseStageCacheMetadata{}, false, fmt.Errorf("parse rootfs base cache metadata: %w", err)
	}
	if metadata.ImageRef != req.ImageRef || metadata.Platform != req.Platform {
		return baseStageCacheMetadata{}, false, fmt.Errorf("rootfs base cache metadata does not match request")
	}
	if info, err := os.Stat(baseDir); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return baseStageCacheMetadata{}, false, fmt.Errorf("read rootfs base cache stage: %w", err)
	}
	if err := copyBaseStageCache(baseDir, stageDir); err != nil {
		return baseStageCacheMetadata{}, false, fmt.Errorf("restore rootfs base cache: %w", err)
	}
	return metadata, true, nil
}

func findBaseStageCacheMetadataForImage(cacheDir, imageRef string) (baseStageCacheMetadata, bool) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return baseStageCacheMetadata{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metadataBytes, err := os.ReadFile(filepath.Join(cacheDir, entry.Name(), "metadata.json"))
		if err != nil {
			continue
		}
		var metadata baseStageCacheMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue
		}
		if metadata.ImageRef == imageRef {
			return metadata, true
		}
	}
	return baseStageCacheMetadata{}, false
}

func saveBaseStageCache(cacheDir string, req BuildRequest, provenance Provenance, imageConfig ocispec.Image, stageDir string) error {
	entryDir := baseStageCacheEntryDir(cacheDir, req.ImageRef, req.Platform)
	baseDir := filepath.Join(entryDir, "base")
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		return fmt.Errorf("create rootfs base cache: %w", err)
	}
	if err := copyBaseStageCache(stageDir, baseDir); err != nil {
		return fmt.Errorf("save rootfs base cache stage: %w", err)
	}
	metadata := baseStageCacheMetadata{
		ImageRef:     req.ImageRef,
		ResolvedRef:  provenance.ResolvedRef,
		Digest:       provenance.Digest,
		Platform:     req.Platform,
		ImageConfig:  imageConfig,
		LayerDigests: append([]string{}, provenance.LayerDigests...),
	}
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rootfs base cache metadata: %w", err)
	}
	metadataBytes = append(metadataBytes, '\n')
	if err := os.WriteFile(filepath.Join(entryDir, "metadata.json"), metadataBytes, 0o644); err != nil {
		return fmt.Errorf("write rootfs base cache metadata: %w", err)
	}
	return nil
}

func baseStageCacheEntryDir(cacheDir, imageRef string, platform Platform) string {
	sum := sha256.Sum256([]byte(imageRef + "\x00" + platform.OS + "\x00" + platform.Architecture + "\x00" + platform.Variant))
	return filepath.Join(cacheDir, hex.EncodeToString(sum[:]))
}

func copyBaseStageCache(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("cp", "-a", src+string(os.PathSeparator)+".", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp -a: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func buildRootfsImage(ctx context.Context, req BuildRequest, stageDir, tmpDir string, progress *progressReporter, provenance *Provenance) error {
	switch req.Format {
	case FormatExt4:
		provenance.BuilderPhase = "build-ext4"
		progress.emit("build-ext4", "building ext4 image", 0, 0, 0, 0)
		return buildExt4Image(ctx, req.Mke2fsPath, stageDir, filepath.Join(tmpDir, "rootfs.ext4"), req.OutputPath, req.SizeMiB*1024*1024, "rootfs")
	case FormatVHD:
		provenance.BuilderPhase = "build-vhd"
		progress.emit("build-vhd", "building vhd image", 0, 0, 0, 0)
		return buildVHDImage(ctx, stageDir, filepath.Join(tmpDir, "rootfs.vhd"), req.OutputPath, req.SizeMiB*1024*1024)
	default:
		return fmt.Errorf("format must be %q or %q", FormatExt4, FormatVHD)
	}
}

func buildBundleImage(ctx context.Context, req BundleRequest, stageDir, tmpDir string, provenance *BundleProvenance) error {
	switch req.Format {
	case FormatExt4:
		provenance.BuilderPhase = "build-ext4"
		return buildExt4Image(ctx, req.Mke2fsPath, stageDir, filepath.Join(tmpDir, "bundle.ext4"), req.OutputPath, req.SizeMiB*1024*1024, "bundle")
	case FormatVHD:
		provenance.BuilderPhase = "build-vhd"
		return buildVHDImage(ctx, stageDir, filepath.Join(tmpDir, "bundle.vhd"), req.OutputPath, req.SizeMiB*1024*1024)
	default:
		return fmt.Errorf("format must be %q or %q", FormatExt4, FormatVHD)
	}
}

func buildExt4Image(ctx context.Context, mke2fsPath, stageDir, tmpImage, outputPath string, sizeBytes int64, label string) error {
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
		Credential: registryCredential(host),
	}
	return repo, nil
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

func registryCredential(host string) auth.CredentialFunc {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err == nil {
		return credentials.Credential(store)
	}
	return auth.StaticCredential(host, auth.Credential{})
}

func fetchBytes(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor) ([]byte, error) {
	rc, err := repo.Fetch(ctx, desc)
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

func extractLayer(stageDir, mediaType string, rc io.Reader) error {
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
		if err := applyTarEntry(root, header, tr); err != nil {
			return err
		}
	}
}

func applyTarEntry(root *os.Root, header *tar.Header, reader io.Reader) error {
	name, err := safeGuestRel(header.Name, false)
	if err != nil {
		if errors.Is(err, errRootPath) {
			return nil
		}
		return err
	}
	if name == "." {
		return nil
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
		return root.RemoveAll(target)
	}
	mode := os.FileMode(header.Mode).Perm()
	switch header.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(name, mode); err != nil {
			return err
		}
	// archive/tar normalizes legacy TypeRegA headers to TypeReg on read.
	case tar.TypeReg:
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		_ = root.RemoveAll(name)
		out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
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
			return recordStageMode(root, name, 0o777)
		}
		if err := recordStageMode(root, name, 0o777); err != nil {
			return err
		}
		return nil
	case tar.TypeLink:
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
	default:
		return nil
	}
	if err := root.Chmod(name, mode); err != nil {
		return err
	}
	return recordStageMode(root, name, mode)
}

var errRootPath = errors.New("OCI layer path is root")

func safeGuestRel(guestPath string, allowRoot bool) (string, error) {
	if strings.ContainsRune(guestPath, 0) {
		return "", fmt.Errorf("unsafe OCI layer path %q", guestPath)
	}
	// Layer paths are slash-separated; a backslash would be cleaned as a plain
	// name character here but acts as a separator once the path reaches
	// Windows filesystem APIs, so it could smuggle ".." components past this
	// validation. Reject it outright.
	if strings.ContainsRune(guestPath, '\\') {
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
	// Same reasoning as safeGuestRel: the traversal checks below treat the
	// target as slash-separated, so backslash separators would evade them on
	// Windows hosts.
	if strings.ContainsRune(linkTarget, '\\') {
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
		if err := root.RemoveAll(path.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func writeInit(stageDir, initPath string, command []string, mode string, env map[string]string, initBinaryPath string, resultPort uint32, shellPort, execPort uint16, mounts []Mount, forwards []PortForward, consoleShell, hostname string) error {
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
		return writeGuestRunConfig(stageDir, command, mode, env, resultPort, shellPort, execPort, mounts, forwards, consoleShell, hostname)
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
		hostnameLines(hostname) +
		commandLine +
		"if [ \"$#\" -gt 0 ]; then\n  set +e\n  \"$@\"\n  status=\"$?\"\n  set -e\n  poweroff -f || halt -f || reboot -f || true\n  exit \"$status\"\nfi\nexec /bin/sh\n"
	script = strings.Replace(script, "exec /bin/sh\n", "exec "+shellQuote(consoleShell)+"\n", 1)
	if err := writeBytesToRoot(root, target, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write init: %w", err)
	}
	return nil
}

type guestRunConfig struct {
	Command      []string      `json:"command"`
	Mode         string        `json:"mode,omitempty"`
	Env          []string      `json:"env,omitempty"`
	Port         uint32        `json:"port"`
	ShellPort    uint16        `json:"shellPort,omitempty"`
	ExecPort     uint16        `json:"execPort,omitempty"`
	Mounts       []Mount       `json:"mounts,omitempty"`
	HostForwards []PortForward `json:"hostForwards,omitempty"`
	ConsoleShell string        `json:"consoleShell,omitempty"`
	Hostname     string        `json:"hostname,omitempty"`
}

func writeGuestRunConfig(stageDir string, command []string, mode string, env map[string]string, resultPort uint32, shellPort, execPort uint16, mounts []Mount, forwards []PortForward, consoleShell, hostname string) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	target, err := safeStageRel("/etc/microagent/run.json")
	if err != nil {
		return err
	}
	if err := root.MkdirAll(path.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create guest config dir: %w", err)
	}
	data, err := json.Marshal(guestRunConfig{Command: command, Mode: strings.TrimSpace(mode), Env: envList(env), Port: resultPort, ShellPort: shellPort, ExecPort: execPort, Mounts: mounts, HostForwards: forwards, ConsoleShell: strings.TrimSpace(consoleShell), Hostname: strings.TrimSpace(hostname)})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeBytesToRoot(root, target, data, 0o644)
}

func envList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		if validShellEnvName(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

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
			mode, err = parseFileMode(file.Mode)
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
	return recordStageMode(root, dst, mode)
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
	return recordStageMode(root, dst, mode)
}

func recordStageMode(root *os.Root, name string, mode os.FileMode) error {
	name = path.Clean(strings.TrimPrefix(filepath.ToSlash(name), "/"))
	if name == "." || name == stageMetadataName {
		return nil
	}
	out, err := root.OpenFile(stageMetadataName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open stage metadata: %w", err)
	}
	record := stageModeRecord{Path: name, Mode: int64(mode.Perm())}
	if err := json.NewEncoder(out).Encode(record); err != nil {
		_ = out.Close()
		return fmt.Errorf("write stage metadata: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close stage metadata: %w", err)
	}
	return nil
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
		if err := recordStageMode(root, dir, 0o755); err != nil {
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

func hostnameLines(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return ""
	}
	quoted := shellQuote(hostname)
	return "hostname " + quoted + "\nprintf '%s\\n' " + quoted + " > /etc/hostname\n"
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
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}
