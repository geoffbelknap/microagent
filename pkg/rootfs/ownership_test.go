package rootfs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestBuildExt4ImagePreservesOwnershipAndSpecialBits builds a real ext4 image
// from a stage tree using the actual mke2fs+debugfs pipeline, then inspects
// the built image with debugfs to confirm every entry carries the uid/gid
// and mode bits (including setuid/setgid/sticky) recorded during staging,
// independent of the host user that owns the stage directory's files.
func TestBuildExt4ImagePreservesOwnershipAndSpecialBits(t *testing.T) {
	mke2fsPath := lookupE2fsprogsToolForTest(t, "mke2fs")
	debugfsPath := lookupE2fsprogsToolForTest(t, "debugfs")

	dir := t.TempDir()
	stageDir := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// A setuid root binary owned by root, e.g. /usr/bin/sudo-like tooling.
	if err := os.MkdirAll(filepath.Join(stageDir, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "usr", "bin", "setuid-tool"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordStageMode(root, "usr/bin/setuid-tool", 0, 0, os.ModeSetuid|0o755); err != nil {
		t.Fatalf("recordStageMode setuid: %v", err)
	}

	// A file owned by a non-root, non-host-build uid/gid, e.g. the guest's
	// unprivileged apt sandbox user.
	if err := os.MkdirAll(filepath.Join(stageDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "etc", "passwd"), []byte("root:x:0:0:root:/root:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordStageMode(root, "etc/passwd", 0, 0, 0o644); err != nil {
		t.Fatalf("recordStageMode etc/passwd: %v", err)
	}

	// A world-writable sticky directory owned by a non-root uid, e.g. /tmp.
	if err := os.MkdirAll(filepath.Join(stageDir, "tmp"), 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := recordStageMode(root, "tmp", 0, 0, os.ModeSticky|0o1777); err != nil {
		t.Fatalf("recordStageMode tmp: %v", err)
	}

	// A file owned by a non-root, non-zero uid/gid pair distinct from the
	// host user running the build (this sandbox builds as uid/gid 1000).
	if err := os.WriteFile(filepath.Join(stageDir, "etc", "owned-by-guest-user"), []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := recordStageMode(root, "etc/owned-by-guest-user", 42, 43, 0o640); err != nil {
		t.Fatalf("recordStageMode owned-by-guest-user: %v", err)
	}

	image := filepath.Join(dir, "rootfs.ext4")
	tmpImage := filepath.Join(dir, "rootfs.ext4.tmp")
	ctx := context.Background()
	if err := buildExt4Image(ctx, mke2fsPath, debugfsPath, stageDir, tmpImage, image, 64*1024*1024, "rootfs"); err != nil {
		t.Fatalf("buildExt4Image: %v", err)
	}

	// debugfs's printed "Mode" field is permission + special bits only; it
	// does not include the file-type bits (those are reported separately via
	// "Type: ..."), even though "sif ... mode" requires the type bits in the
	// value written, on pain of corrupting the inode's type.
	cases := []struct {
		path string
		mode string
		uid  int
		gid  int
	}{
		{"/", "0755", 0, 0},
		{"/usr/bin/setuid-tool", "04755", 0, 0},
		{"/etc/passwd", "0644", 0, 0},
		{"/tmp", "01777", 0, 0},
		{"/etc/owned-by-guest-user", "0640", 42, 43},
	}
	for _, tc := range cases {
		mode, uid, gid := debugfsStat(t, debugfsPath, image, tc.path)
		wantMode, err := strconv.ParseInt(tc.mode, 8, 32)
		if err != nil {
			t.Fatalf("parse want mode %s: %v", tc.mode, err)
		}
		if mode != int(wantMode) {
			t.Errorf("%s mode = 0%o, want 0%o", tc.path, mode, wantMode)
		}
		if uid != tc.uid {
			t.Errorf("%s uid = %d, want %d", tc.path, uid, tc.uid)
		}
		if gid != tc.gid {
			t.Errorf("%s gid = %d, want %d", tc.path, gid, tc.gid)
		}
	}
}

var (
	debugfsModeRE  = regexp.MustCompile(`Mode:\s*(\d+)`)
	debugfsUserRE  = regexp.MustCompile(`User:\s*(\d+)`)
	debugfsGroupRE = regexp.MustCompile(`Group:\s*(\d+)`)
)

// debugfsStat reads back an inode's mode/uid/gid from a built ext4 image via
// `debugfs -R stat`, mirroring how a human would independently verify the
// fix rather than re-using any of the production code under test.
func debugfsStat(t *testing.T, debugfsPath, image, guestPath string) (mode, uid, gid int) {
	t.Helper()
	cmd := exec.Command(debugfsPath, "-R", "stat "+guestPath, image)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs stat %s: %v: %s", guestPath, err, strings.TrimSpace(string(out)))
	}
	text := string(out)
	mode = parseOctalGroup(t, debugfsModeRE, text, guestPath, "mode")
	uid = parseDecimalGroup(t, debugfsUserRE, text, guestPath, "uid")
	gid = parseDecimalGroup(t, debugfsGroupRE, text, guestPath, "gid")
	return mode, uid, gid
}

func parseOctalGroup(t *testing.T, re *regexp.Regexp, text, guestPath, field string) int {
	t.Helper()
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no %s field in debugfs stat output for %s:\n%s", field, guestPath, text)
	}
	v, err := strconv.ParseInt(m[1], 8, 32)
	if err != nil {
		t.Fatalf("parse %s %q for %s: %v", field, m[1], guestPath, err)
	}
	return int(v)
}

func parseDecimalGroup(t *testing.T, re *regexp.Regexp, text, guestPath, field string) int {
	t.Helper()
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no %s field in debugfs stat output for %s:\n%s", field, guestPath, text)
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse %s %q for %s: %v", field, m[1], guestPath, err)
	}
	return v
}
