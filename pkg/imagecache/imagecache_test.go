package imagecache

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/commit"
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

func TestRootfsPathIsStable(t *testing.T) {
	platform := rootfs.Platform{OS: "linux", Architecture: "amd64"}
	a := RootfsPath("/tmp/state", "docker.io/library/busybox:1.36", platform)
	b := RootfsPath("/tmp/state", "docker.io/library/busybox:1.36", platform)
	if a != b {
		t.Fatalf("paths differ: %q %q", a, b)
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

	_, err := Pull(context.Background(), PullOptions{
		StateDir:     dir,
		ImageRef:     ref,
		Architecture: "amd64",
		SizeMiB:      64,
	})
	if err == nil {
		t.Fatal("Pull succeeded from a locally committed image, want a registry fetch failure -- pull must always hit the registry")
	}
	if !strings.Contains(err.Error(), "fetch OCI image") {
		t.Fatalf("Pull error = %v, want an OCI fetch failure (proof it went to the registry rather than the local layout)", err)
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
