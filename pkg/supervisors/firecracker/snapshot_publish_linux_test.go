package firecracker

import (
	"os"
	"path/filepath"
	"testing"
)

// stageWith creates a staging dir under parent containing a marker file so a
// test can tell which directory content ended up published.
func stageWith(t *testing.T, parent, marker string) string {
	t.Helper()
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(parent, "stage-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func markerAt(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "marker"))
	if err != nil {
		t.Fatalf("read marker in %s: %v", dir, err)
	}
	return string(b)
}

// TestPublishSnapshotCreatesTagWhenAbsent: publishing into a tag that does not
// exist yet installs the staged content.
func TestPublishSnapshotCreatesTagWhenAbsent(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, ".snapshot-staging")
	finalDir := filepath.Join(root, "snapshots", "nightly")

	stage := stageWith(t, staging, "fresh")
	if err := publishSnapshot(stage, finalDir); err != nil {
		t.Fatalf("publishSnapshot: %v", err)
	}
	if got := markerAt(t, finalDir); got != "fresh" {
		t.Fatalf("published marker = %q, want fresh", got)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging dir still present after publish: %v", err)
	}
}

// TestPublishSnapshotReplacesExistingOnSuccess: publishing over an existing tag
// replaces it and leaves no superseded backup behind.
func TestPublishSnapshotReplacesExistingOnSuccess(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, ".snapshot-staging")
	finalDir := filepath.Join(root, "snapshots", "nightly")

	// An existing good snapshot.
	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, "marker"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	stage := stageWith(t, staging, "new")
	if err := publishSnapshot(stage, finalDir); err != nil {
		t.Fatalf("publishSnapshot: %v", err)
	}
	if got := markerAt(t, finalDir); got != "new" {
		t.Fatalf("published marker = %q, want new (overwrite on success)", got)
	}
	// No superseded backup left behind in staging.
	if _, err := os.Stat(filepath.Join(staging, "nightly.superseded")); !os.IsNotExist(err) {
		t.Fatalf("superseded backup left behind: %v", err)
	}
}

// TestPublishSnapshotFailurePreservesExisting is the core data-loss guard: when
// the publish cannot complete (here the staging dir does not exist, standing in
// for any capture/rename failure), the prior good snapshot at the tag survives
// intact rather than being destroyed.
func TestPublishSnapshotFailurePreservesExisting(t *testing.T) {
	root := t.TempDir()
	finalDir := filepath.Join(root, "snapshots", "nightly")
	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, "marker"), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}

	missingStage := filepath.Join(root, ".snapshot-staging", "does-not-exist")
	if err := publishSnapshot(missingStage, finalDir); err == nil {
		t.Fatal("publishSnapshot succeeded with a missing staging dir; want error")
	}
	// The existing snapshot must be untouched (rolled back), not left empty.
	if got := markerAt(t, finalDir); got != "precious" {
		t.Fatalf("existing snapshot marker = %q, want precious (must survive a failed publish)", got)
	}
}
