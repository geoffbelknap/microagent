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
	command := append([]string{}, req.Command...)
	if len(command) == 0 {
		command = append([]string{}, imageConfig.Config.Entrypoint...)
		command = append(command, imageConfig.Config.Cmd...)
	}
	if err := writeInit(stageDir, req.InitPath, command, req.Env); err != nil {
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
		linkTarget, err := safeSymlinkTarget(header.Linkname)
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

func safeSymlinkTarget(linkTarget string) (string, error) {
	if linkTarget == "" || strings.ContainsRune(linkTarget, 0) {
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

func writeInit(stageDir, initPath string, command []string, env map[string]string) error {
	target, err := safeStagePath(stageDir, initPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create init dir: %w", err)
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
	if err := os.WriteFile(target, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write init: %w", err)
	}
	return nil
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
