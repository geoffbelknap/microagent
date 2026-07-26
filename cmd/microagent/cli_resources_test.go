package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestPerfBootRejectsInvalidIterations(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "perf.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"boot", "--iterations", "0"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "iterations must be positive") {
		t.Fatalf("runPerf err = %v", err)
	}
}

func TestSummarizePerfIterations(t *testing.T) {
	summary := summarizePerfIterations([]perfIteration{
		{Name: "one", OK: true, DurationMs: 30},
		{Name: "two", OK: true, DurationMs: 10},
		{Name: "three", OK: true, DurationMs: 20},
	})
	if summary.Count != 3 || summary.MinMs != 10 || summary.AvgMs != 20 || summary.MaxMs != 30 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestParseRSSKiB(t *testing.T) {
	rss, err := parseRSSKiB([]byte("  12345\n"))
	if err != nil {
		t.Fatalf("parseRSSKiB: %v", err)
	}
	if rss != 12345 {
		t.Fatalf("rss = %d", rss)
	}
	if _, err := parseRSSKiB([]byte("")); err == nil {
		t.Fatal("parseRSSKiB accepted empty list output")
	}
}

func TestRunPerfFootprintRequiresRunningPID(t *testing.T) {
	dir := t.TempDir()
	testFirecrackerRuntimeState(t, dir, "research", vmkit.StateStopped, 0)
	stdoutPath := filepath.Join(dir, "footprint.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerfFootprint([]string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "does not have a running process pid") {
		t.Fatalf("runPerfFootprint err = %v", err)
	}
}

func TestSummarizeRSSSamples(t *testing.T) {
	summary := summarizeRSSSamples([]perfRSSSample{
		{RSSKiB: 40},
		{RSSKiB: 20},
		{RSSKiB: 30},
	})
	if summary.Count != 3 || summary.MinKiB != 20 || summary.AvgKiB != 30 || summary.MaxKiB != 40 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRunPerfSteadyRejectsInvalidSampling(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "steady.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"steady", "research", "--duration", "1", "--interval", "2", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "interval must be less than or equal to duration") {
		t.Fatalf("runPerf err = %v", err)
	}
}

func TestImagesListAndPruneUseLocalIndex(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.RecordProvenance(dir, rootfs.Provenance{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "images.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"list", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runImage list: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"digest": "sha256:abc"`) {
		t.Fatalf("images output = %s", data)
	}
	if err := os.Remove(rootfsPath); err != nil {
		t.Fatal(err)
	}
	pruned, err := imagecache.Prune(dir, false)
	if err != nil {
		t.Fatalf("imagecache.Prune: %v", err)
	}
	if len(pruned.Removed) != 1 || len(pruned.Kept) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
}

func TestImagesPruneDeleteRemovesReusableBaselines(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := imagecache.Upsert(dir, imagecache.Record{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := imagecache.Prune(dir, true)
	if err != nil {
		t.Fatalf("imagecache.Prune: %v", err)
	}
	if len(pruned.Deleted) != 2 || len(pruned.Kept) != 0 || len(pruned.Removed) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("rootfs still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRunImagesPruneDeleteRequiresConfirmationWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	oldTerminal := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = oldTerminal })
	stdinIsTerminal = func() bool { return false }
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"prune", "--purge", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("err = %v, want --yes confirmation error", err)
	}
}

func TestRunImagePruneDeletesReusableBaselinesWithYes(t *testing.T) {
	oldOutput := outputFormat
	t.Cleanup(func() { outputFormat = oldOutput })
	outputFormat = "text"
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, imagecache.Record{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "prune.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"prune", "--purge", "--yes", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runImage: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Deleted: 1") {
		t.Fatalf("prune output = %s", data)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("rootfs still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRunImageDeleteFlagRejected(t *testing.T) {
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"delete", "test", "--delete", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// The stray --delete token is bucketed as a positional by the reorder
	// machinery, so the rejection surfaces as image delete's usage error
	// (which names the current --purge flag), not a flag-package error.
	if err == nil || !strings.Contains(err.Error(), "usage: microagent image delete") || !strings.Contains(err.Error(), "--purge") {
		t.Fatalf("err = %v, want image delete usage error naming --purge", err)
	}
}

func TestImagesPruneDeleteKeepsWorkspaceRootfs(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, imagecache.Record{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	pruned, err := imagecache.Prune(dir, true)
	if err != nil {
		t.Fatalf("imagecache.Prune: %v", err)
	}
	if len(pruned.Kept) != 1 || len(pruned.Deleted) != 0 || len(pruned.Removed) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
	if _, err := os.Stat(rootfsPath); err != nil {
		t.Fatalf("workspace rootfs was removed: %v", err)
	}
}

func TestImagesTagCreatesAlias(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, imagecache.Record{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  "2026-05-06T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	tagged, err := imagecache.Tag(dir, "sha256:abc", "local/busybox:baseline")
	if err != nil {
		t.Fatalf("imagecache.Tag: %v", err)
	}
	if tagged.ImageRef != "local/busybox:baseline" || tagged.OutputPath != rootfsPath {
		t.Fatalf("tagged = %#v", tagged)
	}
	images, err := imagecache.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 {
		t.Fatalf("images len = %d, want 2: %#v", len(images), images)
	}
}

func TestImagesRemoveAliasKeepsSharedBaseline(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := imagecache.Upsert(dir, imagecache.Record{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := imagecache.Remove(dir, "local/busybox:baseline", true)
	if err != nil {
		t.Fatalf("imagecache.Remove: %v", err)
	}
	if len(removed.Removed) != 1 || len(removed.Deleted) != 0 || len(removed.Kept) != 1 {
		t.Fatalf("removed = %#v", removed)
	}
	if _, err := os.Stat(rootfsPath); err != nil {
		t.Fatalf("baseline was removed: %v", err)
	}
}

func TestImagesRemoveDigestDeletesUnsharedBaseline(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := imagecache.Upsert(dir, imagecache.Record{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := imagecache.Remove(dir, "sha256:abc", true)
	if err != nil {
		t.Fatalf("imagecache.Remove: %v", err)
	}
	if len(removed.Deleted) != 2 || len(removed.Removed) != 0 || len(removed.Kept) != 0 {
		t.Fatalf("removed = %#v", removed)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("baseline still exists or stat failed unexpectedly: %v", err)
	}
}
