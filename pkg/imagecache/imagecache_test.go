package imagecache

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
)

func TestUpsertListTagRemoveAndPrune(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "images", "rootfs", "baseline.ext4")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	record := Record{
		ImageRef:   "local/busybox:baseline",
		Digest:     "sha256:abc",
		Platform:   rootfs.Platform{OS: "linux", Architecture: "amd64"},
		OutputPath: imagePath,
	}
	if err := Upsert(dir, record); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	images, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(images) != 1 || images[0].ImageRef != record.ImageRef {
		t.Fatalf("images = %#v", images)
	}
	tagged, err := Tag(dir, "sha256:abc", "local/busybox:tagged")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if tagged.ImageRef != "local/busybox:tagged" {
		t.Fatalf("tagged = %#v", tagged)
	}
	removed, err := Remove(dir, "local/busybox:tagged", false)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(removed.Removed) != 1 || len(removed.Kept) != 1 {
		t.Fatalf("removed = %#v", removed)
	}
	pruned, err := Prune(dir, true)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned.Deleted) != 1 || len(pruned.Kept) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
}

// TestPrunePurgeClearsBaseCache: image prune --purge reclaims the shared
// base-stage cache too, and the result accounts for what it cleared. The
// entry here is deliberately garbage (a 64-hex directory with no valid
// metadata) — exactly what an interrupted or legacy cache leaves behind and
// what a purge must not leave on disk.
func TestPrunePurgeClearsBaseCache(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "build", "base-cache")
	t.Setenv("MICROAGENT_ROOTFS_BASE_CACHE_DIR", cacheDir)
	entry := filepath.Join(cacheDir, strings.Repeat("ab", 32))
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "metadata.json"), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pruned, err := Prune(dir, true)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if pruned.CacheEntriesRemoved != 1 {
		t.Errorf("CacheEntriesRemoved = %d, want 1", pruned.CacheEntriesRemoved)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Error("base cache entry survived prune --purge")
	}

	// Without file deletion the cache is untouched.
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	pruned, err = Prune(dir, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if pruned.CacheEntriesRemoved != 0 {
		t.Errorf("record-only prune cleared %d cache entries, want 0", pruned.CacheEntriesRemoved)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Errorf("record-only prune touched the cache: %v", err)
	}
}

func TestRootfsPathIsStable(t *testing.T) {
	platform := rootfs.Platform{OS: "linux", Architecture: "amd64"}
	a := RootfsPath("/tmp/state", "docker.io/library/busybox:1.36", platform)
	b := RootfsPath("/tmp/state", "docker.io/library/busybox:1.36", platform)
	if a != b {
		t.Fatalf("paths differ: %q %q", a, b)
	}
}

func TestSaveBaselinePublishesImmutableMeasuredRootfs(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "workspace-rootfs.ext4")
	content := []byte("immutable baseline bytes")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	provenance := rootfs.Provenance{
		ImageRef:   "docker.io/library/busybox:1.36",
		Digest:     "sha256:abc",
		Platform:   rootfs.Platform{OS: "linux", Architecture: "amd64"},
		OutputPath: source,
		SizeBytes:  int64(len(content)),
	}
	if err := SaveBaseline(dir, source, provenance, "init-sha"); err != nil {
		t.Fatalf("SaveBaseline: %v", err)
	}

	record, err := Find(dir, provenance.ImageRef, provenance.Platform)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	wantSHA := fmt.Sprintf("%x", sha256.Sum256(content))
	if record.RootfsSHA256 != wantSHA || !record.RootfsImmutable {
		t.Fatalf("rootfs integrity = sha:%q immutable:%t, want sha:%q immutable:true", record.RootfsSHA256, record.RootfsImmutable, wantSHA)
	}
	if err := ValidateImmutableRootfs(record); err != nil {
		t.Fatalf("ValidateImmutableRootfs: %v", err)
	}
	info, err := os.Stat(record.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("baseline mode = %04o, want no host write bits", info.Mode().Perm())
	}
	derived := Provenance(record, filepath.Join(dir, "workspaces", "agent", "rootfs.ext4"))
	if derived.RootfsBase == nil || derived.RootfsBase.SHA256 != wantSHA || !derived.RootfsBase.Immutable {
		t.Fatalf("derived rootfs base = %#v, want immutable source %s", derived.RootfsBase, wantSHA)
	}
	if derived.OutputPath == record.OutputPath {
		t.Fatal("derived provenance points at shared baseline instead of private workspace path")
	}
}

func TestValidateImmutableRootfsRejectsMutableOrChangedBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.ext4")
	content := []byte("rootfs")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	record := Record{
		OutputPath:      path,
		SizeBytes:       int64(len(content)),
		RootfsSHA256:    fmt.Sprintf("%x", sha256.Sum256(content)),
		RootfsImmutable: true,
	}
	if err := ValidateImmutableRootfs(record); err == nil || !strings.Contains(err.Error(), "host-writable") {
		t.Fatalf("mutable baseline validation error = %v, want host-writable rejection", err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	record.SizeBytes++
	if err := ValidateImmutableRootfs(record); err == nil || !strings.Contains(err.Error(), "size changed") {
		t.Fatalf("changed baseline validation error = %v, want size rejection", err)
	}
}

// newLocalImageLayout writes a tiny single-layer OCI image directly into a
// committed-OCI layout at dir, tagged with ref -- the same on-disk shape
// `microagent commit` (pkg/commit) produces, built by hand here so the test
// does not need a real workspace to commit from.
func newLocalImageLayout(t *testing.T, dir, ref string) {
	t.Helper()
	var layerBuf bytes.Buffer
	tw := tar.NewWriter(&layerBuf)
	content := "local-image-ok\n"
	if err := tw.WriteHeader(&tar.Header{Name: "etc/microagent-local-image.txt", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	layerBytes := layerBuf.Bytes()
	layerDigest := digest.FromBytes(layerBytes)

	configBytes, err := json.Marshal(map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"rootfs": map[string]any{
			"type":     "layers",
			"diff_ids": []string{layerDigest.String()},
		},
		"config": map[string]any{
			"Entrypoint": []string{"/bin/sh"},
			"Cmd":        []string{"-c", "cat /etc/microagent-local-image.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configDigest := digest.FromBytes(configBytes)

	manifestBytes, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    configDigest,
			Size:      int64(len(configBytes)),
		},
		Layers: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    layerDigest,
			Size:      int64(len(layerBytes)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := digest.FromBytes(manifestBytes)

	store, err := oci.New(dir)
	if err != nil {
		t.Fatalf("oci.New: %v", err)
	}
	ctx := context.Background()
	push := func(data []byte, mediaType string, dgst digest.Digest) {
		t.Helper()
		desc := ocispec.Descriptor{MediaType: mediaType, Digest: dgst, Size: int64(len(data))}
		if err := store.Push(ctx, desc, bytes.NewReader(data)); err != nil {
			t.Fatalf("push %s: %v", mediaType, err)
		}
	}
	push(layerBytes, ocispec.MediaTypeImageLayer, layerDigest)
	push(configBytes, ocispec.MediaTypeImageConfig, configDigest)
	manifestDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: manifestDigest, Size: int64(len(manifestBytes))}
	push(manifestBytes, ocispec.MediaTypeImageManifest, manifestDesc.Digest)
	if err := store.Tag(ctx, manifestDesc, ref); err != nil {
		t.Fatalf("tag %s: %v", ref, err)
	}
}

// TestPullNeverConsultsLocalImageLayout is the regression proof that `image
// pull` always goes to the registry: a ref committed to the local
// committed-OCI layout (the same layout local-first rootfs builds consult)
// must not be silently served from there. If Pull ever threaded
// LocalImageLayout into its BuildRequest, this test would get back a
// successful record built from the local layout instead of the expected
// registry-fetch failure.
func TestPullNeverConsultsLocalImageLayout(t *testing.T) {
	dir := t.TempDir()
	const ref = "microagent-imagecache-test.invalid/demo:v1"
	newLocalImageLayout(t, commit.LayoutPath(dir), ref)
	var events []operation.ProgressEvent

	_, err := Pull(context.Background(), PullOptions{
		StateDir:     dir,
		ImageRef:     ref,
		Architecture: "amd64",
		SizeMiB:      64,
		Progress: func(event operation.ProgressEvent) {
			events = append(events, event)
		},
	})
	if err == nil {
		t.Fatal("Pull succeeded from a locally committed image, want a registry fetch failure -- pull must always hit the registry")
	}
	if !strings.Contains(err.Error(), "fetch OCI image") {
		t.Fatalf("Pull error = %v, want an OCI fetch failure (proof it went to the registry rather than the local layout)", err)
	}
	if len(events) == 0 {
		t.Fatal("Pull emitted no progress before the registry failure")
	}
	if got := events[0]; got.Operation != "image_pull" || got.Label != "Pull image" || got.Phase != "fetch-manifest" {
		t.Fatalf("first progress event = %#v, want image_pull/Pull image/fetch-manifest", got)
	}
}

func TestPathInRootfsStoreRejectsSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "images", "rootfs")
	outside := t.TempDir()
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store, "link")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	if PathInRootfsStore(dir, filepath.Join(store, "link", "victim.ext4")) {
		t.Fatal("symlinked image-store parent was accepted")
	}
}
