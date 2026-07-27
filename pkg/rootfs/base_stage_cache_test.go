package rootfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// writeCacheEntry lays down a cache entry by hand: the metadata.json completion
// marker plus whatever base tree the test wants.
func writeCacheEntry(t *testing.T, cacheDir string, req BuildRequest, complete bool) string {
	t.Helper()
	entryDir := baseStageCacheEntryDir(cacheDir, req.ImageRef, req.Platform)
	baseDir := filepath.Join(entryDir, "base")
	if err := os.MkdirAll(filepath.Join(baseDir, "usr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if complete {
		if err := os.WriteFile(filepath.Join(baseDir, stageMetadataName), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	meta := baseStageCacheMetadata{ImageRef: req.ImageRef, Platform: req.Platform}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, "metadata.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return entryDir
}

func probeRequest() BuildRequest {
	return BuildRequest{
		ImageRef: "docker.io/library/python:3.13-slim",
		Platform: Platform{OS: "linux", Architecture: "amd64"},
	}
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
	req := probeRequest()
	writeCacheEntry(t, cacheDir, req, false)

	_, ok, err := restoreBaseStageCache(cacheDir, req, t.TempDir())
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
	req := probeRequest()
	writeCacheEntry(t, cacheDir, req, true)

	_, ok, err := restoreBaseStageCache(cacheDir, req, t.TempDir())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !ok {
		t.Error("a complete entry was not restored; the cache would never hit")
	}
}

// TestInterruptedSaveLeavesTheOldEntryIntact covers the swap. A save that dies
// before the rename must not damage what is already cached — the previous entry
// is still correct, and destroying it converts a crash into a poisoned cache.
func TestInterruptedSaveLeavesTheOldEntryIntact(t *testing.T) {
	cacheDir := t.TempDir()
	req := probeRequest()
	entryDir := writeCacheEntry(t, cacheDir, req, true)

	// A pending directory is exactly what an interrupted save leaves behind.
	pending, err := os.MkdirTemp(cacheDir, baseStageCachePendingPrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pending, "base", "usr"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, ok, err := restoreBaseStageCache(cacheDir, req, t.TempDir())
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
	req := probeRequest()

	for i := range 2 {
		stage := t.TempDir()
		if err := os.WriteFile(filepath.Join(stage, stageMetadataName), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := saveBaseStageCache(cacheDir, req, Provenance{}, ocispec.Image{}, stage); err != nil {
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

	_, ok, err := restoreBaseStageCache(cacheDir, req, t.TempDir())
	if err != nil || !ok {
		t.Errorf("the republished entry does not restore: ok=%v err=%v", ok, err)
	}
}
