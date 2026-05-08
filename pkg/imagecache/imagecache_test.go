package imagecache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent-kit/pkg/rootfs"
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

func TestPathInRootfsStoreRejectsSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "images", "rootfs")
	outside := t.TempDir()
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store, "link")); err != nil {
		t.Fatal(err)
	}
	if PathInRootfsStore(dir, filepath.Join(store, "link", "victim.ext4")) {
		t.Fatal("symlinked image-store parent was accepted")
	}
}
