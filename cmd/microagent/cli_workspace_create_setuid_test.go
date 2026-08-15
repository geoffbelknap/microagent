package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// A baseline must carry both the setuid policy and the immutable-rootfs
// contract before reuse. Legacy cache entries rebuild once instead of quietly
// becoming trusted shared OS state after an upgrade.
func TestBaselineReuseRequiresStrippedPolicyAndImmutableRootfs(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "baseline.ext4")
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	initPath := filepath.Join(dir, "guest-init")
	if err := os.WriteFile(initPath, []byte("init"), 0o755); err != nil {
		t.Fatal(err)
	}
	record := imagecache.Record{
		ImageRef:   "docker.io/library/busybox:1.36",
		Digest:     "sha256:abc",
		Platform:   rootfs.Platform{OS: "linux", Architecture: "amd64"},
		OutputPath: rootfsPath,
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
		InitSHA256: workspace.GuestInitSHA256(initPath),
	}
	if err := imagecache.Upsert(dir, record); err != nil {
		t.Fatal(err)
	}

	reuse, _ := rootfsBaselineHooks(dir, record.ImageRef, "amd64", initPath)
	if _, _, ok := reuse(filepath.Join(dir, "ws.ext4")); ok {
		t.Fatal("reused a baseline with no recorded setuid policy")
	}

	record.SetuidPolicy = rootfs.SetuidPolicyStripped
	if err := imagecache.Upsert(dir, record); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reuse(filepath.Join(dir, "ws.ext4")); ok {
		t.Fatal("reused a stripped but unsealed baseline")
	}
	record.RootfsSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("rootfs")))
	record.RootfsImmutable = true
	if err := os.Chmod(rootfsPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, record); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reuse(filepath.Join(dir, "ws.ext4")); !ok {
		t.Fatal("a sealed baseline with a matching init and stripped policy must be reusable")
	}
}
