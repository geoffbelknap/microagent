package workspace

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestCopyFileReplaceOverwritesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	writeTestFile(t, source, "new content")
	writeTestFile(t, target, "old content that is longer than the new content")

	if err := CopyFileReplace(source, target, 0o600); err != nil {
		t.Fatalf("CopyFileReplace: %v", err)
	}
	if got := readTestFile(t, target); got != "new content" {
		t.Fatalf("target content = %q, want %q", got, "new content")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCopyFileReplaceCreatesMissingTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "nested", "target")
	writeTestFile(t, source, "content")

	if err := CopyFileReplace(source, target, 0o600); err != nil {
		t.Fatalf("CopyFileReplace: %v", err)
	}
	if got := readTestFile(t, target); got != "content" {
		t.Fatalf("target content = %q, want %q", got, "content")
	}
}

func TestCopyFileReplaceLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	writeTestFile(t, source, "content")
	writeTestFile(t, target, "old")

	if err := CopyFileReplace(source, target, 0o600); err != nil {
		t.Fatalf("CopyFileReplace: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "source" && entry.Name() != "target" {
			t.Fatalf("unexpected leftover file %q", entry.Name())
		}
	}
}

// TestCopyFileReplaceClonesWhenSupported pins the mechanism, not just the
// result: on a clone-capable filesystem the replacement must be a fresh
// inode from a rename, not an in-place rewrite of the old one. The
// in-place rewrite is what made restoring a multi-GiB snapshot rootfs a
// full byte copy.
func TestCopyFileReplaceClonesWhenSupported(t *testing.T) {
	dir := t.TempDir()
	probeSource := filepath.Join(dir, "probe-source")
	probeTarget := filepath.Join(dir, "probe-target")
	writeTestFile(t, probeSource, "probe")
	if !cloneFile(probeSource, probeTarget, 0o600) {
		t.Skipf("filesystem at %s does not support file cloning", dir)
	}

	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	writeTestFile(t, source, "new content")
	writeTestFile(t, target, "old content")
	before, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}

	if err := CopyFileReplace(source, target, 0o600); err != nil {
		t.Fatalf("CopyFileReplace: %v", err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	beforeSys, beforeOK := before.Sys().(*syscall.Stat_t)
	afterSys, afterOK := after.Sys().(*syscall.Stat_t)
	if !beforeOK || !afterOK {
		t.Skip("platform does not expose inode numbers")
	}
	if beforeSys.Ino == afterSys.Ino {
		t.Fatalf("target inode unchanged (%d): replacement was an in-place rewrite, not a clone", afterSys.Ino)
	}
	if got := readTestFile(t, target); got != "new content" {
		t.Fatalf("target content = %q, want %q", got, "new content")
	}
}
