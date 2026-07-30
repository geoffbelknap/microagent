package rootfs

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	probeDigest   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	foreignDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func probePlatform() Platform {
	return Platform{OS: "linux", Architecture: "amd64"}
}

// writeCacheEntry lays down a cache entry by hand: the metadata.json completion
// marker plus whatever base tree the test wants.
func writeCacheEntry(t *testing.T, cacheDir, digest string, platform Platform, complete bool) string {
	t.Helper()
	entryDir := baseStageCacheEntryDir(cacheDir, digest, platform)
	baseDir := filepath.Join(entryDir, "base")
	if err := os.MkdirAll(filepath.Join(baseDir, "usr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if complete {
		if err := os.WriteFile(filepath.Join(baseDir, stageMetadataName), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	meta := baseStageCacheMetadata{Digest: digest, Platform: platform}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, "metadata.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return entryDir
}

// TestPartialCacheEntryIsNotRestored is the fix for a silent, permanent
// failure.
//
// metadata.json is the completion marker, but the entry was built in place:
// base/ was emptied and refilled by `cp -a` while metadata.json survived from
// the previous run. A process killed in that window — OOM, Ctrl-C, a full disk
// — left a valid marker over a fraction of a rootfs.
//
// Nothing detected it afterwards. The next build restored the partial tree,
// produced a rootfs with no /bin, and the guest exited 1 with no output on any
// stream, which reads as the user's own command failing. It never recovered,
// because the marker stayed valid forever.
//
// Rebuilding costs one pull. Trusting a partial tree costs every build after
// it, so a miss is the only safe answer.
func TestPartialCacheEntryIsNotRestored(t *testing.T) {
	cacheDir := t.TempDir()
	writeCacheEntry(t, cacheDir, probeDigest, probePlatform(), false)

	_, ok, err := restoreBaseStageCache(cacheDir, probeDigest, probePlatform(), t.TempDir())
	if err != nil {
		t.Fatalf("a partial entry should be a miss, not an error: %v", err)
	}
	if ok {
		t.Error("restored a cache entry whose base tree is incomplete")
	}
}

// TestCompleteCacheEntryIsRestored is the control. Rejecting partial entries
// must not reject sound ones, or the cache never hits and every build pulls.
func TestCompleteCacheEntryIsRestored(t *testing.T) {
	cacheDir := t.TempDir()
	writeCacheEntry(t, cacheDir, probeDigest, probePlatform(), true)

	_, ok, err := restoreBaseStageCache(cacheDir, probeDigest, probePlatform(), t.TempDir())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !ok {
		t.Error("a complete entry was not restored; the cache would never hit")
	}
}

func TestBaseStageCachePreservesHardLinks(t *testing.T) {
	cacheDir := t.TempDir()
	stage := t.TempDir()
	busybox := filepath.Join(stage, "bin", "busybox")
	if err := os.MkdirAll(filepath.Dir(busybox), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(busybox, []byte("busybox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(busybox, filepath.Join(stage, "bin", "sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, stageMetadataName), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	platform := probePlatform()
	if err := saveBaseStageCache(cacheDir, baseStageCacheMetadata{Digest: probeDigest, Platform: platform}, stage); err != nil {
		t.Fatalf("save: %v", err)
	}
	restored := t.TempDir()
	if _, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, restored); err != nil || !ok {
		t.Fatalf("restore: ok=%v err=%v", ok, err)
	}

	for _, root := range []string{
		filepath.Join(baseStageCacheEntryDir(cacheDir, probeDigest, platform), "base"),
		restored,
	} {
		busyboxInfo, err := os.Stat(filepath.Join(root, "bin", "busybox"))
		if err != nil {
			t.Fatal(err)
		}
		shellInfo, err := os.Stat(filepath.Join(root, "bin", "sh"))
		if err != nil {
			t.Fatal(err)
		}
		busyboxID, busyboxLinked := stageHardLinkID("", busyboxInfo)
		shellID, shellLinked := stageHardLinkID("", shellInfo)
		if !busyboxLinked || !shellLinked || busyboxID != shellID {
			t.Errorf("%s did not preserve the busybox/sh hard link", root)
		}
	}
}

// TestCorruptMetadataIsAMissThatSelfHeals: with the cache on by default, a
// corrupt entry must never wedge builds. Restore treats it as a miss, and the
// save that follows the re-fetch overwrites the bad entry in place.
func TestCorruptMetadataIsAMissThatSelfHeals(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()
	entryDir := writeCacheEntry(t, cacheDir, probeDigest, platform, true)
	if err := os.WriteFile(filepath.Join(entryDir, "metadata.json"), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, t.TempDir())
	if err != nil {
		t.Fatalf("a corrupt entry should be a miss, not an error: %v", err)
	}
	if ok {
		t.Fatal("restored an entry with corrupt metadata")
	}

	stage := newStageTree(t)
	if err := saveBaseStageCache(cacheDir, baseStageCacheMetadata{Digest: probeDigest, Platform: platform}, stage); err != nil {
		t.Fatalf("save over the corrupt entry: %v", err)
	}
	_, ok, err = restoreBaseStageCache(cacheDir, probeDigest, platform, t.TempDir())
	if err != nil || !ok {
		t.Errorf("the cache did not heal after a save over the corrupt entry: ok=%v err=%v", ok, err)
	}
}

// TestMismatchedMetadataIsAMiss: an entry whose recorded digest disagrees
// with the digest that keyed the lookup is corrupt or foreign, never a hit.
func TestMismatchedMetadataIsAMiss(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()
	entryDir := writeCacheEntry(t, cacheDir, probeDigest, platform, true)
	meta, err := json.Marshal(baseStageCacheMetadata{Digest: foreignDigest, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, "metadata.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, t.TempDir())
	if err != nil {
		t.Fatalf("a mismatched entry should be a miss, not an error: %v", err)
	}
	if ok {
		t.Error("restored an entry recorded for a different digest")
	}
}

// TestInterruptedSaveLeavesTheOldEntryIntact covers the swap. A save that dies
// before the rename must not damage what is already cached — the previous entry
// is still correct, and destroying it converts a crash into a poisoned cache.
func TestInterruptedSaveLeavesTheOldEntryIntact(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()
	entryDir := writeCacheEntry(t, cacheDir, probeDigest, platform, true)

	// A pending directory is exactly what an interrupted save leaves behind.
	pending, err := os.MkdirTemp(cacheDir, baseStageCachePendingPrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pending, "base", "usr"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, t.TempDir())
	if err != nil || !ok {
		t.Errorf("the surviving entry stopped restoring after an interrupted save: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(entryDir, "metadata.json")); err != nil {
		t.Errorf("the previous entry was damaged: %v", err)
	}
}

// TestTransientDirectoriesAreNeverTreatedAsEntries keeps the swap invisible to
// lookup. A half-written pending directory that counted as an entry would be
// the same bug in a new place.
func TestTransientDirectoriesAreNeverTreatedAsEntries(t *testing.T) {
	for _, name := range []string{
		baseStageCachePendingPrefix + "123456",
		"abc123" + baseStageCacheSupersededSuffix,
	} {
		if !isBaseStageCacheTransient(name) {
			t.Errorf("%q is a swap directory but would be read as a cache entry", name)
		}
	}
	if isBaseStageCacheTransient("4e3841399f740b394732b88f43ed4aa70b964d9723563abbd41a29a37437019d") {
		t.Error("a real entry name was classified as transient; the cache would never hit")
	}
}

// TestSaveIsAtomicAcrossRepeatedPublishes exercises the swap end to end: a
// second save over an existing entry must leave exactly one entry, complete,
// with no leftovers.
func TestSaveIsAtomicAcrossRepeatedPublishes(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()

	for i := range 2 {
		stage := newStageTree(t)
		if err := saveBaseStageCache(cacheDir, baseStageCacheMetadata{Digest: probeDigest, Platform: platform}, stage); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if isBaseStageCacheTransient(e.Name()) {
			t.Errorf("swap left %q behind", e.Name())
		}
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("got %d entries (%s), want exactly 1", len(entries), strings.Join(names, ", "))
	}

	_, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, t.TempDir())
	if err != nil || !ok {
		t.Errorf("the republished entry does not restore: ok=%v err=%v", ok, err)
	}
}

// TestReapRemovesForeignEntriesAndOldLitter: the reaper clears entries whose
// directory name does not reproduce from their own metadata (earlier cache
// layouts keyed by image ref land here), removes swap litter old enough to be
// crash debris, keeps young litter that may belong to a live publish in
// another process, and never touches names that are not shaped like cache
// entries — a misdirected cache dir override must not lose unrelated files.
func TestReapRemovesForeignEntriesAndOldLitter(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()

	valid := writeCacheEntry(t, cacheDir, probeDigest, platform, true)
	// An old-layout entry: a well-formed 64-hex directory whose name was
	// derived from something other than its metadata digest.
	refKeyed := filepath.Join(cacheDir, "4e3841399f740b394732b88f43ed4aa70b964d9723563abbd41a29a37437019d")
	if err := os.MkdirAll(filepath.Join(refKeyed, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(baseStageCacheMetadata{ImageRef: "docker.io/library/ubuntu:24.04", Digest: foreignDigest, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refKeyed, "metadata.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	oldLitter := filepath.Join(cacheDir, baseStageCachePendingPrefix+"crashed")
	if err := os.MkdirAll(oldLitter, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * baseStageCacheLitterMaxAge)
	if err := os.Chtimes(oldLitter, stale, stale); err != nil {
		t.Fatal(err)
	}
	youngLitter := filepath.Join(cacheDir, baseStageCachePendingPrefix+"live")
	if err := os.MkdirAll(youngLitter, 0o755); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(cacheDir, "README")
	if err := os.WriteFile(unrelated, []byte("not a cache entry\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reapBaseStageCache(cacheDir)

	for path, wantGone := range map[string]bool{
		valid:       false,
		refKeyed:    true,
		oldLitter:   true,
		youngLitter: false,
		unrelated:   false,
	} {
		_, err := os.Stat(path)
		gone := os.IsNotExist(err)
		if gone != wantGone {
			t.Errorf("%s: gone=%v, want gone=%v", filepath.Base(path), gone, wantGone)
		}
	}
}

// TestReapEvictsOldestEntriesBeyondCap bounds cache growth: tag updates
// strand their previous digests' entries, and beyond the cap the
// least-recently-used ones go.
func TestReapEvictsOldestEntriesBeyondCap(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()

	total := baseStageCacheMaxEntries + 2
	dirs := make([]string, 0, total)
	base := time.Now().Add(-time.Duration(total) * time.Minute)
	for i := range total {
		digest := "sha256:" + strings.Repeat("0", 60) + string(rune('a'+i%16)) + string(rune('a'+i/16)) + "00"
		entryDir := writeCacheEntry(t, cacheDir, digest, platform, true)
		mtime := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(entryDir, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, entryDir)
	}

	reapBaseStageCache(cacheDir)

	for i, dir := range dirs {
		_, err := os.Stat(dir)
		gone := os.IsNotExist(err)
		wantGone := i < total-baseStageCacheMaxEntries
		if gone != wantGone {
			t.Errorf("entry %d: gone=%v, want gone=%v", i, gone, wantGone)
		}
	}
}

// TestClearBaseCacheSelectsByDigest: image delete --purge clears exactly the
// purged digests' entries, always sweeps litter, and leaves other entries in
// place.
func TestClearBaseCacheSelectsByDigest(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()
	target := writeCacheEntry(t, cacheDir, probeDigest, platform, true)
	kept := writeCacheEntry(t, cacheDir, foreignDigest, platform, true)
	litter := filepath.Join(cacheDir, baseStageCachePendingPrefix+"crashed")
	if err := os.MkdirAll(litter, 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := ClearBaseCache(cacheDir, func(entry BaseCacheEntry) bool {
		return entry.Digest == probeDigest
	})
	if err != nil {
		t.Fatalf("ClearBaseCache: %v", err)
	}
	if len(removed) != 1 || removed[0].Digest != probeDigest {
		t.Fatalf("removed = %+v, want exactly the selected digest", removed)
	}
	if removed[0].SizeBytes <= 0 {
		t.Errorf("removed entry reports %d bytes, want > 0", removed[0].SizeBytes)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("selected entry still present")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("unselected entry was removed: %v", err)
	}
	if _, err := os.Stat(litter); !os.IsNotExist(err) {
		t.Error("swap litter survived the clear")
	}
}

func TestBaseCacheDirForEnvOverride(t *testing.T) {
	stateDir := filepath.Join(string(os.PathSeparator), "state")

	t.Setenv("MICROAGENT_ROOTFS_BASE_CACHE_DIR", "placeholder")
	os.Unsetenv("MICROAGENT_ROOTFS_BASE_CACHE_DIR")
	if got, want := BaseCacheDirFor(stateDir), filepath.Join(stateDir, "build", "base-cache"); got != want {
		t.Errorf("unset env: got %q, want %q", got, want)
	}
	if got := BaseCacheDirFor(""); got != "" {
		t.Errorf("no state dir: got %q, want disabled", got)
	}

	t.Setenv("MICROAGENT_ROOTFS_BASE_CACHE_DIR", "")
	if got := BaseCacheDirFor(stateDir); got != "" {
		t.Errorf("set-but-empty env must disable the cache, got %q", got)
	}

	t.Setenv("MICROAGENT_ROOTFS_BASE_CACHE_DIR", "/elsewhere/cache")
	if got := BaseCacheDirFor(stateDir); got != "/elsewhere/cache" {
		t.Errorf("env override ignored, got %q", got)
	}
}

// TestBuildBaseCacheRoundTrip is the end-to-end proof, hermetic via a local
// committed-OCI layout (no registry): the first build resolves and publishes
// a digest-keyed entry, the second build resolves again and restores from
// it, and both report their base source truthfully. It also pins the
// security-relevant save point: the cache entry is captured before any
// per-workspace configuration is written, so the entry can never leak a
// request's env or guest config into later builds.
func TestBuildBaseCacheRoundTrip(t *testing.T) {
	format, output, mke2fsPath := rootfsHostFormat(t)

	dir := t.TempDir()
	layoutDir := filepath.Join(dir, "images", "oci")
	const ref = "microagent-cache-test.invalid/demo:v1"
	manifestDigest := newLocalImageLayout(t, layoutDir, ref)
	cacheDir := filepath.Join(dir, "base-cache")

	const secret = "cache-round-trip-do-not-persist"
	req := BuildRequest{
		ImageRef:         ref,
		Platform:         Platform{OS: "linux", Architecture: "amd64"},
		OutputPath:       filepath.Join(dir, "first-"+output),
		Format:           format,
		StateDir:         filepath.Join(dir, "state"),
		Mke2fsPath:       mke2fsPath,
		SizeMiB:          64,
		AllowMutable:     true,
		LocalImageLayout: layoutDir,
		BaseCacheDir:     cacheDir,
		Env:              map[string]string{"CACHE_TEST_SECRET": secret},
	}
	first, err := NewBuilder().Build(context.Background(), req)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if first.BaseSource != BaseSourceLocalLayout {
		t.Errorf("first build base_source = %q, want %q", first.BaseSource, BaseSourceLocalLayout)
	}
	if first.Digest != manifestDigest.String() {
		t.Errorf("first build digest = %q, want %q", first.Digest, manifestDigest)
	}

	entryDir := baseStageCacheEntryDir(cacheDir, first.Digest, req.Platform)
	if _, err := os.Stat(filepath.Join(entryDir, "metadata.json")); err != nil {
		t.Fatalf("no cache entry published under the resolved digest: %v", err)
	}
	// The entry must hold only image content: no guest config, and no trace
	// of the request env anywhere in the tree.
	if _, err := os.Stat(filepath.Join(entryDir, "base", "etc", "microagent")); !os.IsNotExist(err) {
		t.Errorf("cache entry contains guest config: stat etc/microagent err=%v", err)
	}
	walkErr := filepath.WalkDir(filepath.Join(entryDir, "base"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("cache entry file %s contains the request env value", path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk cache entry: %v", walkErr)
	}

	second := req
	second.OutputPath = filepath.Join(dir, "second-"+output)
	restored, err := NewBuilder().Build(context.Background(), second)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if restored.BaseSource != BaseSourceCache {
		t.Errorf("second build base_source = %q, want %q", restored.BaseSource, BaseSourceCache)
	}
	if restored.Digest != first.Digest {
		t.Errorf("second build digest = %q, want %q", restored.Digest, first.Digest)
	}
	if strings.Join(restored.LayerDigests, ",") != strings.Join(first.LayerDigests, ",") {
		t.Errorf("layer digests diverged across the cache: %v vs %v", restored.LayerDigests, first.LayerDigests)
	}
	if _, err := os.Stat(second.OutputPath); err != nil {
		t.Errorf("second rootfs output: %v", err)
	}
}

func newStageTree(t *testing.T) string {
	t.Helper()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, stageMetadataName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return stage
}
