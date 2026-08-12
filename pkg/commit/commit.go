// Package commit snapshots a stopped workspace's rootfs back into an OCI image
// and pushes it to a registry — the reverse of the OCI->rootfs realize path.
// commit extracts the ext4 rootfs (via debugfs, unprivileged), assembles a
// single-layer OCI image (pkg/ociimage), and writes it to a local OCI layout;
// Push copies that image to a registry with ORAS using the same standard pull
// credentials as the rootfs builder.
//
// Limitation: unprivileged debugfs extraction does not preserve original file
// ownership, so committed layers record the current user's uid/gid. Content,
// modes, and symlinks are preserved.
package commit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/ext4fs"
	"github.com/geoffbelknap/microagent/pkg/ociimage"
	"github.com/geoffbelknap/microagent/pkg/registryauth"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// Options configures a commit.
type Options struct {
	StateDir            string
	DebugFSPath         string
	Workspace           string
	Backend             string
	Reference           string // target image reference; local/... or loopback by default
	AllowRegistryShadow bool   // allow globally meaningful registry identity
	Architecture        string // OCI architecture; defaults to the guest arch
	CreatedAt           time.Time
}

// Result reports a commit.
type Result struct {
	Reference  string `json:"reference"`
	Digest     string `json:"digest"`
	SizeBytes  int64  `json:"size_bytes"`
	LayoutPath string `json:"layout_path"`
}

// extractRootfs dumps an ext4 image's filesystem tree into destDir. It is a
// package variable so tests can substitute a fixture extractor.
var extractRootfs = debugfsExtract

var e2fsckPath = defaultE2fsckPath()

func defaultE2fsckPath() string {
	path, _ := workspace.LookupE2fsprogsTool("e2fsck")
	return path
}

var reconcileRootfs = func(rootfsPath string) error {
	return ext4fs.ReconcileJournal(e2fsckPath, rootfsPath)
}

// dumpRootfs is a package variable so tests can verify that filesystem
// reconciliation completes before debugfs is allowed to extract anything.
var dumpRootfs = runDebugFSDump

// LayoutPath is where committed images are stored as an OCI image layout.
func LayoutPath(stateDir string) string {
	return filepath.Join(stateDir, "images", "oci")
}

// Commit snapshots the workspace rootfs into a local OCI image tagged with the
// target reference.
func Commit(ctx context.Context, opts Options) (Result, error) {
	if strings.TrimSpace(opts.Workspace) == "" {
		return Result{}, fmt.Errorf("workspace name is required")
	}
	ref := strings.TrimSpace(opts.Reference)
	if ref == "" {
		return Result{}, fmt.Errorf("target image reference is required")
	}
	if err := validateCommitReference(ref, opts.AllowRegistryShadow); err != nil {
		return Result{}, err
	}
	if opts.StateDir == "" {
		opts.StateDir = workspace.StateDir()
	}
	if opts.Backend == "" {
		opts.Backend = workspace.HostBackend()
	}
	if opts.Architecture == "" {
		opts.Architecture = workspace.GuestArch()
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}

	// Refuse to read a live disk: commit a stopped workspace.
	state, _, err := workspace.LatestStartState(opts.StateDir, opts.Workspace)
	if err != nil {
		return Result{}, err
	}
	if state == "" {
		return Result{}, fmt.Errorf("workspace %q not found", opts.Workspace)
	}
	if state == "running" || state == "paused" {
		return Result{}, fmt.Errorf("workspace %q is %s; halt or stop it before committing", opts.Workspace, state)
	}

	rootfsPath := workspace.WorkspaceRootfsPath(opts.StateDir, opts.Workspace, opts.Backend)
	if _, err := os.Stat(rootfsPath); err != nil {
		return Result{}, fmt.Errorf("workspace rootfs not found: %w", err)
	}

	assemble := ociimage.Options{Architecture: opts.Architecture, CreatedAt: opts.CreatedAt}
	if manifest, err := workspace.ReadManifest(opts.StateDir, opts.Workspace); err == nil {
		assemble.Config = imageConfigFromManifest(manifest)
	}
	staging, err := os.MkdirTemp("", "microagent-commit-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staging)
	if err := extractRootfs(opts.DebugFSPath, rootfsPath, staging); err != nil {
		return Result{}, fmt.Errorf("extract rootfs: %w", err)
	}
	assemble.Dir = staging

	img, err := ociimage.Assemble(assemble)
	if err != nil {
		return Result{}, err
	}

	store, err := oci.New(LayoutPath(opts.StateDir))
	if err != nil {
		return Result{}, fmt.Errorf("open OCI layout: %w", err)
	}
	for _, blob := range []ociimage.Blob{img.Layer, img.Config, img.Manifest} {
		// The OCI layout is content-addressed: a blob already present (e.g. when
		// committing the same rootfs to a second tag) is a hit, not an error.
		if err := store.Push(ctx, blob.Descriptor, strings.NewReader(string(blob.Data))); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
			return Result{}, fmt.Errorf("write %s blob: %w", blob.Descriptor.MediaType, err)
		}
	}
	if err := store.Tag(ctx, img.Manifest.Descriptor, ref); err != nil {
		return Result{}, fmt.Errorf("tag %s: %w", ref, err)
	}

	return Result{
		Reference:  ref,
		Digest:     img.Manifest.Descriptor.Digest.String(),
		SizeBytes:  img.Layer.Descriptor.Size,
		LayoutPath: LayoutPath(opts.StateDir),
	}, nil
}

func imageConfigFromManifest(manifest workspace.Manifest) ocispec.ImageConfig {
	defaults := manifest.ImageDefaults
	if defaults.IsZero() {
		defaults.Env = append([]string{}, manifest.ImageEnv...)
		defaults.Entrypoint = append([]string{}, manifest.ImageEntrypoint...)
		defaults.Cmd = append([]string{}, manifest.ImageCmd...)
	}
	config := ocispec.ImageConfig{
		User:       defaults.User,
		Env:        append([]string{}, defaults.Env...),
		Entrypoint: append([]string{}, defaults.Entrypoint...),
		Cmd:        append([]string{}, defaults.Cmd...),
		WorkingDir: defaults.WorkingDir,
		StopSignal: defaults.StopSignal,
	}
	if len(defaults.ExposedPorts) != 0 {
		config.ExposedPorts = make(map[string]struct{}, len(defaults.ExposedPorts))
		for _, port := range defaults.ExposedPorts {
			config.ExposedPorts[port] = struct{}{}
		}
	}
	if len(defaults.Volumes) != 0 {
		config.Volumes = make(map[string]struct{}, len(defaults.Volumes))
		for _, volume := range defaults.Volumes {
			config.Volumes[volume] = struct{}{}
		}
	}
	if len(defaults.Labels) != 0 {
		config.Labels = make(map[string]string, len(defaults.Labels))
		for key, value := range defaults.Labels {
			config.Labels[key] = value
		}
	}
	return config
}

// Push copies a previously committed image from the local OCI layout to its
// registry.
func Push(ctx context.Context, stateDir, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("image reference is required")
	}
	store, err := oci.New(LayoutPath(stateDir))
	if err != nil {
		return fmt.Errorf("open OCI layout: %w", err)
	}
	if _, err := store.Resolve(ctx, ref); err != nil {
		return fmt.Errorf("image %q not found in local layout; commit it first: %w", ref, err)
	}
	repoRef, tag, err := splitRegistryReference(ref)
	if err != nil {
		return err
	}
	repo, err := newRepository(repoRef)
	if err != nil {
		return err
	}
	if _, err := oras.Copy(ctx, store, ref, repo, tag, oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("push %s: %w", ref, err)
	}
	return nil
}

// debugfsExtract dumps the whole filesystem of an ext4 image into destDir using
// debugfs rdump (unprivileged).
func debugfsExtract(debugfsPath, rootfsPath, destDir string) error {
	if strings.TrimSpace(debugfsPath) == "" {
		debugfsPath = "debugfs"
	}
	if err := reconcileRootfs(rootfsPath); err != nil {
		return fmt.Errorf("reconcile ext4 filesystem: %w", err)
	}
	out, err := dumpRootfs(debugfsPath, rootfsPath, destDir)
	if err != nil {
		return fmt.Errorf("debugfs rdump failed: %w: %s", err, strings.TrimSpace(out))
	}
	entries, readErr := os.ReadDir(destDir)
	if readErr != nil {
		return readErr
	}
	if len(entries) == 0 {
		return fmt.Errorf("debugfs rdump produced no files (output: %s)", strings.TrimSpace(out))
	}
	return nil
}

func runDebugFSDump(debugfsPath, rootfsPath, destDir string) (string, error) {
	quotedDest, err := quoteDebugFSArg(destDir)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(debugfsPath, "-R", `rdump "/" `+quotedDest, rootfsPath)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func quoteDebugFSArg(arg string) (string, error) {
	if arg == "" || strings.HasPrefix(arg, "-") {
		return "", fmt.Errorf("invalid debugfs argument %q", arg)
	}
	for _, r := range arg {
		if r == '"' || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("invalid debugfs argument %q", arg)
		}
	}
	return `"` + arg + `"`, nil
}

// --- registry reference + repository helpers (mirrors pkg/rootfs) ---

func validateCommitReference(raw string, allowRegistryShadow bool) error {
	normalized := normalizeRegistryReference(raw)
	ref, err := registry.ParseReference(normalized)
	if err != nil {
		return fmt.Errorf("parse OCI image ref %q: %w", normalized, err)
	}
	first, _, hasSlash := strings.Cut(strings.TrimSpace(raw), "/")
	if allowRegistryShadow || (hasSlash && first == "local") || isLoopbackRegistry(ref.Registry) {
		return nil
	}
	return fmt.Errorf(
		"commit target %q resolves to registry namespace %q; use a local/... reference or explicitly allow registry shadowing",
		raw, ref.Registry,
	)
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
