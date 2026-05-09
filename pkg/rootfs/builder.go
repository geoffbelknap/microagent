package rootfs

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

type Builder struct {
	Name string
}

func NewBuilder() Builder {
	return Builder{Name: "microagent-rootfs"}
}

func (b Builder) Build(ctx context.Context, req BuildRequest) (Provenance, error) {
	req = NormalizeRequest(req)
	if err := ValidateRequest(req); err != nil {
		return Provenance{}, err
	}
	name := b.Name
	if name == "" {
		name = "microagent-rootfs"
	}
	provenance := Provenance{
		ImageRef:   req.ImageRef,
		Platform:   req.Platform,
		OutputPath: req.OutputPath,
		InitPath:   req.InitPath,
		Builder:    name,
	}

	repoRef, reference, err := splitRegistryReference(req.ImageRef)
	if err != nil {
		return provenance, err
	}
	repo, err := newRepository(repoRef)
	if err != nil {
		return provenance, err
	}
	platform := ocispec.Platform{
		OS:           req.Platform.OS,
		Architecture: req.Platform.Architecture,
		Variant:      req.Platform.Variant,
	}
	provenance.BuilderPhase = "fetch-manifest"
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
	configBytes, err := fetchBytes(ctx, repo, manifest.Config)
	if err != nil {
		return provenance, fmt.Errorf("fetch OCI image config: %w", err)
	}
	var imageConfig ocispec.Image
	if err := json.Unmarshal(configBytes, &imageConfig); err != nil {
		return provenance, fmt.Errorf("parse OCI image config: %w", err)
	}
	if err := validateImagePlatform(imageConfig, req.Platform); err != nil {
		return provenance, err
	}

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

	provenance.BuilderPhase = "extract-layers"
	for _, layer := range manifest.Layers {
		rc, err := repo.Fetch(ctx, layer)
		if err != nil {
			return provenance, fmt.Errorf("fetch OCI layer %s: %w", layer.Digest, err)
		}
		if err := extractLayer(stageDir, layer.MediaType, rc); err != nil {
			_ = rc.Close()
			return provenance, fmt.Errorf("extract OCI layer %s: %w", layer.Digest, err)
		}
		if err := rc.Close(); err != nil {
			return provenance, fmt.Errorf("close OCI layer %s: %w", layer.Digest, err)
		}
		provenance.LayerDigests = append(provenance.LayerDigests, layer.Digest.String())
	}

	provenance.BuilderPhase = "write-init"
	command := buildCommand(req, imageConfig)
	if err := writeInit(stageDir, req.InitPath, command, req.Env, req.InitBinaryPath, req.ResultPort, req.Mounts, req.HostForwards); err != nil {
		return provenance, err
	}
	provenance.BuilderPhase = "write-files"
	if err := writeDeclaredFiles(stageDir, req.Files); err != nil {
		return provenance, err
	}
	if req.StageSnapshot != "" {
		provenance.BuilderPhase = "snapshot-stage"
		if err := copyStage(stageDir, req.StageSnapshot); err != nil {
			return provenance, err
		}
		provenance.StageSnapshot = req.StageSnapshot
	}

	provenance.BuilderPhase = "build-ext4"
	if err := os.MkdirAll(filepath.Dir(req.OutputPath), 0o755); err != nil {
		return provenance, fmt.Errorf("create output dir: %w", err)
	}
	tmpImage := filepath.Join(tmpDir, "rootfs.ext4")
	if err := allocateFile(tmpImage, req.SizeMiB*1024*1024); err != nil {
		return provenance, fmt.Errorf("allocate rootfs image: %w", err)
	}
	cmd := exec.CommandContext(ctx, req.Mke2fsPath, "-q", "-t", "ext4", "-d", stageDir, tmpImage)
	if out, err := cmd.CombinedOutput(); err != nil {
		return provenance, fmt.Errorf("build ext4 rootfs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmpImage, req.OutputPath); err != nil {
		return provenance, fmt.Errorf("commit rootfs image: %w", err)
	}
	info, err := os.Stat(req.OutputPath)
	if err != nil {
		return provenance, fmt.Errorf("stat output rootfs: %w", err)
	}
	provenance.SizeBytes = info.Size()
	provenance.BuilderPhase = "complete"
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

func (b Builder) BuildBundle(ctx context.Context, req BundleRequest) (BundleProvenance, error) {
	req = NormalizeBundleRequest(req)
	provenance := BundleProvenance{
		SourcePath:   req.SourcePath,
		OutputPath:   req.OutputPath,
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
	provenance.BuilderPhase = "build-ext4"
	if err := os.MkdirAll(filepath.Dir(req.OutputPath), 0o755); err != nil {
		return provenance, fmt.Errorf("create output dir: %w", err)
	}
	tmpImage := filepath.Join(tmpDir, "bundle.ext4")
	if err := allocateFile(tmpImage, req.SizeMiB*1024*1024); err != nil {
		return provenance, fmt.Errorf("allocate bundle image: %w", err)
	}
	cmd := exec.CommandContext(ctx, req.Mke2fsPath, "-q", "-t", "ext4", "-d", stageDir, tmpImage)
	if out, err := cmd.CombinedOutput(); err != nil {
		return provenance, fmt.Errorf("build ext4 bundle: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmpImage, req.OutputPath); err != nil {
		return provenance, fmt.Errorf("commit bundle image: %w", err)
	}
	info, err := os.Stat(req.OutputPath)
	if err != nil {
		return provenance, fmt.Errorf("stat output bundle: %w", err)
	}
	provenance.SizeBytes = info.Size()
	provenance.BuilderPhase = "complete"
	return provenance, nil
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

func splitRegistryReference(raw string) (repoRef, reference string, err error) {
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

func newRepository(repoRef string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, err
	}
	host := strings.SplitN(repoRef, "/", 2)[0]
	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.DefaultCache,
		Credential: auth.StaticCredential(host, auth.Credential{}),
	}
	return repo, nil
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
	defer root.Close()
	var reader io.Reader = rc
	if strings.Contains(mediaType, "gzip") || strings.HasSuffix(mediaType, ".gzip") || strings.HasSuffix(mediaType, "+gzip") {
		gz, err := gzip.NewReader(rc)
		if err != nil {
			return err
		}
		defer gz.Close()
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
	case tar.TypeReg, tar.TypeRegA:
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
		return root.Symlink(linkTarget, name)
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
	return root.Chmod(name, mode)
}

var errRootPath = errors.New("OCI layer path is root")

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
		if err := root.RemoveAll(path.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func writeInit(stageDir, initPath string, command []string, env map[string]string, initBinaryPath string, resultPort uint32, mounts []Mount, forwards []PortForward) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer root.Close()
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
		return writeGuestRunConfig(stageDir, command, env, resultPort, mounts, forwards)
	}
	var commandLine string
	if len(command) > 0 {
		quoted := make([]string, 0, len(command))
		for _, arg := range command {
			quoted = append(quoted, shellQuote(arg))
		}
		commandLine = "set -- " + strings.Join(quoted, " ") + "\n"
	}
	script := "#!/bin/sh\nset -eu\nmkdir -p /proc /sys /dev\nmount -t proc proc /proc || true\nmount -t sysfs sysfs /sys || true\n" +
		envLines(env) +
		commandLine +
		"if [ \"$#\" -gt 0 ]; then\n  set +e\n  \"$@\"\n  status=\"$?\"\n  set -e\n  poweroff -f || halt -f || reboot -f || true\n  exit \"$status\"\nfi\nexec /bin/sh\n"
	if err := writeBytesToRoot(root, target, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write init: %w", err)
	}
	return nil
}

type guestRunConfig struct {
	Command      []string      `json:"command"`
	Env          []string      `json:"env,omitempty"`
	Port         uint32        `json:"port"`
	Mounts       []Mount       `json:"mounts,omitempty"`
	HostForwards []PortForward `json:"hostForwards,omitempty"`
}

func writeGuestRunConfig(stageDir string, command []string, env map[string]string, resultPort uint32, mounts []Mount, forwards []PortForward) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer root.Close()
	target, err := safeStageRel("/etc/microagent/run.json")
	if err != nil {
		return err
	}
	if err := root.MkdirAll(path.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create guest config dir: %w", err)
	}
	data, err := json.Marshal(guestRunConfig{Command: command, Env: envList(env), Port: resultPort, Mounts: mounts, HostForwards: forwards})
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

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeDeclaredFiles(stageDir string, files []File) error {
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return err
	}
	defer root.Close()
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
	defer in.Close()
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
	return root.Chmod(dst, mode)
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
	return root.Chmod(dst, mode)
}

func copyFileOverwrite(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
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
	return os.Chmod(dst, mode)
}

func safeStagePath(stageDir, guestPath string) (string, error) {
	if strings.ContainsRune(guestPath, 0) {
		return "", fmt.Errorf("guest path contains NUL")
	}
	guestPath = filepath.Clean(guestPath)
	if !filepath.IsAbs(guestPath) || guestPath == string(os.PathSeparator) {
		return "", fmt.Errorf("guest path must be absolute and below root")
	}
	rel := strings.TrimPrefix(guestPath, string(os.PathSeparator))
	parts := strings.Split(rel, string(os.PathSeparator))
	return filepath.Join(append([]string{stageDir}, parts...)...), nil
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
		defer in.Close()
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
