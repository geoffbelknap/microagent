package volume

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// fakeFormat replaces the mke2fs-backed formatter with one that just writes a
// sized placeholder file, so tests do not depend on mke2fs being installed.
func fakeFormat(t *testing.T) {
	t.Helper()
	prev := formatExt4
	formatExt4 = func(_ context.Context, path string, sizeMiB int64, _ string) error {
		return os.WriteFile(path, make([]byte, 0), 0o644)
	}
	t.Cleanup(func() { formatExt4 = prev })
}

// fakeVHDFormat replaces the in-process VHD builder with a stub that writes a
// placeholder file, so VHD-lane tests do not depend on tar2ext4/host platform.
// It returns a recorder reporting which formatter ran.
func fakeVHDFormat(t *testing.T) *struct{ vhdCalled, ext4Called bool } {
	t.Helper()
	rec := &struct{ vhdCalled, ext4Called bool }{}
	prevVHD := formatVHD
	prevExt4 := formatExt4
	formatVHD = func(_ context.Context, path string, _ int64) error {
		rec.vhdCalled = true
		return os.WriteFile(path, make([]byte, 0), 0o644)
	}
	formatExt4 = func(_ context.Context, path string, _ int64, _ string) error {
		rec.ext4Called = true
		return os.WriteFile(path, make([]byte, 0), 0o644)
	}
	t.Cleanup(func() {
		formatVHD = prevVHD
		formatExt4 = prevExt4
	})
	return rec
}

// TestCreateVHDBackendUsesVHDBuilder asserts that a VHD-lane backend
// (windows-hyperv) builds its backing image in-process with a .vhd extension
// and never invokes mke2fs.
func TestCreateVHDBackendUsesVHDBuilder(t *testing.T) {
	rec := fakeVHDFormat(t)
	dir := t.TempDir()
	backend := vmkit.BackendWindowsHyperV

	r, err := Create(context.Background(), dir, backend, "data", 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !rec.vhdCalled {
		t.Error("expected the VHD builder to run on a VHD-lane backend")
	}
	if rec.ext4Called {
		t.Error("mke2fs formatter must not run on a VHD-lane backend")
	}
	want := DiskPath(dir, backend, r.Name)
	if filepath.Ext(want) != ".vhd" {
		t.Errorf("DiskPath = %q, want a .vhd backing file", want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected backing disk at %q: %v", want, err)
	}
	// Path must resolve to the same VHD image for this backend.
	got, err := Path(dir, backend, "data")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// TestRemoveDropsBackingFileRegardlessOfExtension guards that Remove cleans up
// the VHD backing file (not just the historical .ext4 shape).
func TestRemoveDropsBackingFileRegardlessOfExtension(t *testing.T) {
	fakeVHDFormat(t)
	dir := t.TempDir()
	backend := vmkit.BackendWindowsHyperV
	if _, err := Create(context.Background(), dir, backend, "data", 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	disk := DiskPath(dir, backend, "data")
	if _, err := os.Stat(disk); err != nil {
		t.Fatalf("expected backing file: %v", err)
	}
	if err := Remove(dir, "data", false, nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Errorf("expected VHD backing file removed, stat err: %v", err)
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"data", "data-1", "a", "x9", "my-vol-2"}
	for _, n := range valid {
		if !ValidName(n) {
			t.Errorf("expected %q to be valid", n)
		}
	}
	invalid := []string{"", "-data", "data-", "Data", "da_ta", "da/ta", "..", "a/../b"}
	for _, n := range invalid {
		if ValidName(n) {
			t.Errorf("expected %q to be invalid", n)
		}
	}
}

func TestCreateListGet(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()

	rec, err := Create(context.Background(), dir, "", "data", 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.SizeMiB != DefaultSizeMiB {
		t.Errorf("expected default size %d, got %d", DefaultSizeMiB, rec.SizeMiB)
	}
	if rec.CreatedAt == "" {
		t.Error("expected CreatedAt to be set")
	}
	if _, err := os.Stat(DiskPath(dir, "", "data")); err != nil {
		t.Errorf("expected backing disk to exist: %v", err)
	}

	if _, err := Create(context.Background(), dir, "", "cache", 256, ""); err != nil {
		t.Fatalf("create cache: %v", err)
	}
	list, err := List(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Name != "cache" || list[1].Name != "data" {
		t.Errorf("expected sorted [cache data], got %+v", list)
	}

	got, err := Get(dir, "cache")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SizeMiB != 256 {
		t.Errorf("expected size 256, got %d", got.SizeMiB)
	}
}

func TestCreateRejectsDuplicateAndBadSize(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Create(context.Background(), dir, "", "data", 0, ""); err == nil {
		t.Error("expected duplicate name to fail")
	}
	if _, err := Create(context.Background(), dir, "", "toobig", maxSizeMiB+1, ""); err == nil {
		t.Error("expected oversize volume to fail")
	}
	if _, err := Create(context.Background(), dir, "", "BadName", 0, ""); err == nil {
		t.Error("expected invalid name to fail")
	}
}

func TestPathResolution(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	p, err := Path(dir, "", "data")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if p != DiskPath(dir, "", "data") {
		t.Errorf("expected %q, got %q", DiskPath(dir, "", "data"), p)
	}
	if _, err := Path(dir, "", "missing"); err == nil {
		t.Error("expected unknown volume to fail")
	}
}

func TestAttachEnforcesSingleAttach(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	running := map[string]bool{"ws1": true}
	isRunning := func(ws string) bool { return running[ws] }

	if _, err := Attach(dir, "data", "ws1", isRunning); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	// Re-attaching to the same workspace is idempotent.
	if _, err := Attach(dir, "data", "ws1", isRunning); err != nil {
		t.Fatalf("re-attach same workspace: %v", err)
	}
	// A second running workspace is refused.
	if _, err := Attach(dir, "data", "ws2", isRunning); err == nil {
		t.Error("expected attach to a running holder to fail")
	}

	// Once the holder stops, the volume is reclaimable.
	running["ws1"] = false
	rec, err := Attach(dir, "data", "ws2", isRunning)
	if err != nil {
		t.Fatalf("attach after holder stopped: %v", err)
	}
	if rec.AttachedTo != "ws2" {
		t.Errorf("expected holder ws2, got %q", rec.AttachedTo)
	}
}

func TestDetachAndDetachAll(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if _, err := Create(context.Background(), dir, "", n, 0, ""); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
		if _, err := Attach(dir, n, "ws1", nil); err != nil {
			t.Fatalf("attach %s: %v", n, err)
		}
	}

	if err := Detach(dir, "a", "ws1"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got, _ := Get(dir, "a"); got.AttachedTo != "" {
		t.Errorf("expected a detached, got holder %q", got.AttachedTo)
	}
	// Detach is idempotent and tolerant of mismatched holders.
	if err := Detach(dir, "a", "ws1"); err != nil {
		t.Errorf("idempotent detach: %v", err)
	}
	if err := Detach(dir, "b", "other"); err != nil {
		t.Errorf("mismatched-holder detach: %v", err)
	}
	if got, _ := Get(dir, "b"); got.AttachedTo != "ws1" {
		t.Errorf("expected b still held by ws1, got %q", got.AttachedTo)
	}

	if err := DetachAll(dir, "ws1"); err != nil {
		t.Fatalf("detach all: %v", err)
	}
	if got, _ := Get(dir, "b"); got.AttachedTo != "" {
		t.Errorf("expected b detached after DetachAll, got %q", got.AttachedTo)
	}
}

func TestRemove(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Attach(dir, "data", "ws1", nil); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Attached to a running workspace: refused without force.
	if err := Remove(dir, "data", false, func(string) bool { return true }); err == nil {
		t.Error("expected remove of in-use volume to fail")
	}
	// Holder no longer running: removable.
	if err := Remove(dir, "data", false, func(string) bool { return false }); err != nil {
		t.Fatalf("remove stale-held volume: %v", err)
	}
	if _, err := os.Stat(DiskPath(dir, "", "data")); !os.IsNotExist(err) {
		t.Errorf("expected backing disk removed, stat err: %v", err)
	}
	if _, err := Get(dir, "data"); err == nil {
		t.Error("expected volume gone from registry")
	}
	if err := Remove(dir, "missing", false, nil); err == nil {
		t.Error("expected remove of unknown volume to fail")
	}
}

func TestForceRemoveInUse(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Attach(dir, "data", "ws1", nil); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := Remove(dir, "data", true, func(string) bool { return true }); err != nil {
		t.Fatalf("force remove: %v", err)
	}
}

func TestIndexPathLayout(t *testing.T) {
	dir := t.TempDir()
	if got, want := IndexPath(dir), filepath.Join(dir, "volumes", "index.json"); got != want {
		t.Errorf("IndexPath = %q, want %q", got, want)
	}
	if got, want := DiskPath(dir, "", "data"), filepath.Join(dir, "volumes", "data.ext4"); got != want {
		t.Errorf("DiskPath = %q, want %q", got, want)
	}
}
