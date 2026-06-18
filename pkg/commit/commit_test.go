package commit

import (
	"context"
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
