package rootfs

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setuidFixtureLayer builds a layer with one representative of each mode
// class: a setuid binary, a setgid binary, a setgid directory, a sticky
// directory, and a plain file.
func setuidFixtureLayer(t *testing.T) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries := []struct {
		name     string
		typeflag byte
		mode     int64
	}{
		{"tmp", tar.TypeDir, 0o1777},
		{"var/mail", tar.TypeDir, 0o2775},
		{"usr/bin", tar.TypeDir, 0o755},
		{"usr/bin/su", tar.TypeReg, 0o4755},
		{"usr/bin/expiry", tar.TypeReg, 0o2755},
		{"usr/bin/env", tar.TypeReg, 0o755},
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: e.mode}
		if e.typeflag == tar.TypeReg {
			hdr.Size = 2
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("#!")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

// stageModes reads the sidecar the debugfs pass replays into the ext4 image —
// the modes the guest actually ends up with, which matters more than the
// staged host modes.
func stageModes(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, stageMetadataName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	modes := map[string]int64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec stageModeRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		modes[rec.Path] = rec.Mode // last record wins, as in the debugfs pass
	}
	return modes
}

// The default strips setuid and setgid from files and directories alike, on
// both routes into the guest: the staged host mode and the sidecar record.
// Sticky survives — it is a shared-tmp convention, not a privilege bit.
func TestExtractLayerStripsSetuidByDefault(t *testing.T) {
	dir := t.TempDir()
	stripper := &setuidStripper{}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", setuidFixtureLayer(t), stripper, testExtractionBudget()); err != nil {
		t.Fatal(err)
	}

	modes := stageModes(t, dir)
	want := map[string]int64{
		"tmp":            0o1777, // sticky kept
		"var/mail":       0o775,  // setgid dir stripped
		"usr/bin/su":     0o755,  // setuid stripped
		"usr/bin/expiry": 0o755,  // setgid stripped
		"usr/bin/env":    0o755,  // untouched
	}
	for path, wantMode := range want {
		if modes[path] != wantMode {
			t.Errorf("sidecar mode for %s = %o, want %o", path, modes[path], wantMode)
		}
	}
	for _, path := range []string{"usr/bin/su", "var/mail"} {
		info, err := os.Stat(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			t.Errorf("staged host mode for %s still carries setuid/setgid: %v", path, info.Mode())
		}
	}

	count, list := stripper.finalize()
	if count != 3 {
		t.Errorf("stripped count = %d, want 3 (su, expiry, var/mail): %v", count, list)
	}
	wantList := []string{"usr/bin/expiry", "usr/bin/su", "var/mail"}
	if len(list) != len(wantList) {
		t.Fatalf("stripped list = %v, want %v", list, wantList)
	}
	for i := range wantList {
		if list[i] != wantList[i] {
			t.Fatalf("stripped list = %v, want %v", list, wantList)
		}
	}
}

// AllowGuestSetuid preserves everything, byte for byte, in the sidecar — the
// devcontainer pattern (non-root user + working sudo) depends on it.
func TestExtractLayerPreservesSetuidOnRequest(t *testing.T) {
	dir := t.TempDir()
	stripper := &setuidStripper{allow: true}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", setuidFixtureLayer(t), stripper, testExtractionBudget()); err != nil {
		t.Fatal(err)
	}

	modes := stageModes(t, dir)
	want := map[string]int64{
		"tmp":            0o1777,
		"var/mail":       0o2775,
		"usr/bin/su":     0o4755,
		"usr/bin/expiry": 0o2755,
		"usr/bin/env":    0o755,
	}
	for path, wantMode := range want {
		if modes[path] != wantMode {
			t.Errorf("sidecar mode for %s = %o, want %o", path, modes[path], wantMode)
		}
	}
	if count, list := stripper.finalize(); count != 0 || list != nil {
		t.Errorf("preserving build recorded strips: count=%d list=%v", count, list)
	}
}

// The two policies must never share cache entries: same digest, different
// trees. An entry saved under one policy is a miss for the other, and the two
// entry paths differ so the variants can coexist.
func TestSetuidPolicyKeysTheBaseStageCache(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()
	if baseStageCacheEntryDir(cacheDir, probeDigest, platform, SetuidPolicyStripped) ==
		baseStageCacheEntryDir(cacheDir, probeDigest, platform, SetuidPolicyPreserved) {
		t.Fatal("stripped and preserved entries share a cache path")
	}

	stage := newStageTree(t)
	meta := baseStageCacheMetadata{Digest: probeDigest, Platform: platform, SetuidPolicy: SetuidPolicyPreserved}
	if err := saveBaseStageCache(cacheDir, meta, stage); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, SetuidPolicyStripped, t.TempDir()); err != nil || ok {
		t.Fatalf("a preserved entry answered a stripped lookup: ok=%v err=%v", ok, err)
	}
	if _, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, SetuidPolicyPreserved, t.TempDir()); err != nil || !ok {
		t.Fatalf("the preserved entry did not answer its own policy: ok=%v err=%v", ok, err)
	}
}

// A pre-policy cache entry (metadata without SetuidPolicy) must never be
// restored: its sidecar carries the image's setuid bits, and a stripped
// build restoring it would hand them to the guest anyway.
func TestPrePolicyCacheEntriesAreNeverRestored(t *testing.T) {
	cacheDir := t.TempDir()
	platform := probePlatform()
	// Seed an entry exactly where a stripped lookup will probe, but with
	// metadata that predates the policy field — the strongest variant of the
	// stale-entry hazard, since the path itself matches.
	entryDir := baseStageCacheEntryDir(cacheDir, probeDigest, platform, SetuidPolicyStripped)
	baseDir := filepath.Join(entryDir, "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, stageMetadataName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(baseStageCacheMetadata{Digest: probeDigest, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, "metadata.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := restoreBaseStageCache(cacheDir, probeDigest, platform, SetuidPolicyStripped, t.TempDir()); err != nil || ok {
		t.Fatalf("restored a pre-policy entry into a stripped build: ok=%v err=%v", ok, err)
	}
}
