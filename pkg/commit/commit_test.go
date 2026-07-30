package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	"oras.land/oras-go/v2/content/oci"
)

// stopWorkspaceFixture writes a stopped workspace with a placeholder rootfs and
// returns (stateDir, backend).
func stopWorkspaceFixture(t *testing.T, state vmkit.VMState) (string, string) {
	t.Helper()
	dir := t.TempDir()
	backend := workspace.HostBackend()
	opts := workspace.Options{Name: "demo", StateDir: dir, Backend: backend}
	rootfsPath := workspace.WorkspaceRootfsPath(dir, "demo", backend)
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("placeholder ext4"), 0o644); err != nil {
		t.Fatal(err)
	}
	req, err := workspace.Request(opts, "run", rootfsPath, "req-1")
	if err != nil {
		t.Fatalf("workspace.Request: %v", err)
	}
	if err := workspace.WriteProcessState(opts, req, state, 0, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	return dir, backend
}

func fakeExtractor(t *testing.T) {
	t.Helper()
	saved := extractRootfs
	t.Cleanup(func() { extractRootfs = saved })
	extractRootfs = func(_, _, destDir string) error {
		if err := os.MkdirAll(filepath.Join(destDir, "etc"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destDir, "etc", "hostname"), []byte("demo\n"), 0o644)
	}
}

func TestDebugfsExtractReconcilesBeforeDump(t *testing.T) {
	savedReconcile := reconcileRootfs
	savedDump := dumpRootfs
	t.Cleanup(func() {
		reconcileRootfs = savedReconcile
		dumpRootfs = savedDump
	})

	reconciled := false
	reconcileRootfs = func(rootfsPath string) error {
		if rootfsPath != "rootfs.ext4" {
			t.Fatalf("rootfs path = %q", rootfsPath)
		}
		reconciled = true
		return nil
	}
	dumpRootfs = func(_, _, destDir string) (string, error) {
		if !reconciled {
			t.Fatal("debugfs ran before ext4 reconciliation")
		}
		return "", os.WriteFile(filepath.Join(destDir, "payload"), []byte("ok"), 0o644)
	}

	if err := debugfsExtract("debugfs", "rootfs.ext4", t.TempDir()); err != nil {
		t.Fatalf("debugfsExtract: %v", err)
	}
}

func TestDebugfsExtractFailsClosedBeforeDump(t *testing.T) {
	savedReconcile := reconcileRootfs
	savedDump := dumpRootfs
	t.Cleanup(func() {
		reconcileRootfs = savedReconcile
		dumpRootfs = savedDump
	})

	wantErr := errors.New("filesystem check failed")
	reconcileRootfs = func(string) error { return wantErr }
	dumped := false
	dumpRootfs = func(_, _, _ string) (string, error) {
		dumped = true
		return "", nil
	}

	err := debugfsExtract("debugfs", "rootfs.ext4", t.TempDir())
	if !errors.Is(err, wantErr) {
		t.Fatalf("debugfsExtract error = %v, want %v", err, wantErr)
	}
	if dumped {
		t.Fatal("debugfs ran after ext4 reconciliation failed")
	}
}

func TestQuoteDebugFSArg(t *testing.T) {
	got, err := quoteDebugFSArg("/tmp/output with spaces")
	if err != nil {
		t.Fatalf("quoteDebugFSArg: %v", err)
	}
	if got != `"/tmp/output with spaces"` {
		t.Fatalf("quoted argument = %q", got)
	}
	for _, input := range []string{"", "-option", "/tmp/bad\npath", `/tmp/bad"path`} {
		if _, err := quoteDebugFSArg(input); err == nil {
			t.Errorf("quoteDebugFSArg(%q) unexpectedly succeeded", input)
		}
	}
}

func TestCommitWritesLayout(t *testing.T) {
	dir, backend := stopWorkspaceFixture(t, vmkit.StateStopped)
	fakeExtractor(t)

	res, err := Commit(context.Background(), Options{
		StateDir:     dir,
		Backend:      backend,
		Workspace:    "demo",
		Reference:    "localhost:5000/demo:v1",
		Architecture: "amd64",
		CreatedAt:    time.Unix(1000, 0),
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Reference != "localhost:5000/demo:v1" || res.Digest == "" || res.SizeBytes == 0 {
		t.Fatalf("result = %+v", res)
	}

	// The layout must resolve the tagged image.
	store, err := oci.New(LayoutPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	desc, err := store.Resolve(context.Background(), "localhost:5000/demo:v1")
	if err != nil {
		t.Fatalf("Resolve committed image: %v", err)
	}
	if desc.Digest.String() != res.Digest {
		t.Errorf("resolved digest %s != result digest %s", desc.Digest, res.Digest)
	}
	exists, err := store.Exists(context.Background(), desc)
	if err != nil || !exists {
		t.Errorf("committed manifest blob missing: exists=%v err=%v", exists, err)
	}
}

func TestCommitSameWorkspaceTwoTags(t *testing.T) {
	// Committing the same rootfs to a second tag must succeed: the OCI layout is
	// content-addressed, so the shared layer/config blobs already exist and are a
	// hit, not an error. This is the multi-tag publish path (e.g. :sha + :latest).
	dir, backend := stopWorkspaceFixture(t, vmkit.StateStopped)
	fakeExtractor(t)

	base := Options{StateDir: dir, Backend: backend, Workspace: "demo", Architecture: "amd64", CreatedAt: time.Unix(1000, 0)}
	refs := []string{"localhost:5000/demo:sha", "localhost:5000/demo:latest"}
	for _, ref := range refs {
		opts := base
		opts.Reference = ref
		if _, err := Commit(context.Background(), opts); err != nil {
			t.Fatalf("commit %s: %v", ref, err)
		}
	}

	store, err := oci.New(LayoutPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if _, err := store.Resolve(context.Background(), ref); err != nil {
			t.Errorf("resolve %s: %v", ref, err)
		}
	}
}

func TestCommitRefusesRunningWorkspace(t *testing.T) {
	dir, backend := stopWorkspaceFixture(t, vmkit.StateRunning)
	fakeExtractor(t)
	_, err := Commit(context.Background(), Options{StateDir: dir, Backend: backend, Workspace: "demo", Reference: "localhost:5000/demo:v1"})
	if err == nil {
		t.Fatal("expected commit of a running workspace to be refused")
	}
}

func TestCommitRejectsBadInput(t *testing.T) {
	if _, err := Commit(context.Background(), Options{Reference: "x"}); err == nil {
		t.Error("missing workspace should error")
	}
	dir, backend := stopWorkspaceFixture(t, vmkit.StateStopped)
	if _, err := Commit(context.Background(), Options{StateDir: dir, Backend: backend, Workspace: "demo"}); err == nil {
		t.Error("missing reference should error")
	}
}

func TestPushMissingImage(t *testing.T) {
	dir := t.TempDir()
	if err := Push(context.Background(), dir, "localhost:5000/none:v1"); err == nil {
		t.Fatal("Push should error when the image is not in the local layout")
	}
}
