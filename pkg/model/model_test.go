package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertListAndReadWriteIndex(t *testing.T) {
	dir := t.TempDir()
	rec := Record{
		ModelRef:   "hf.co/org/repo@main/model-Q4_K_M.gguf",
		Digest:     "sha256:abc",
		OutputPath: dir + "/models/blobs/x.gguf",
		SizeBytes:  123,
		LastUsedAt: "2026-06-02T00:00:00Z",
	}
	if err := Upsert(dir, rec); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	rec.SizeBytes = 456
	if err := Upsert(dir, rec); err != nil {
		t.Fatalf("Upsert 2: %v", err)
	}
	list, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].SizeBytes != 456 {
		t.Fatalf("expected 1 record size 456, got %+v", list)
	}
}

func TestModelPathStableAndFind(t *testing.T) {
	a := ModelPath("/tmp/state", "hf.co/org/repo@main/m.gguf")
	b := ModelPath("/tmp/state", "hf.co/org/repo@main/m.gguf")
	if a != b {
		t.Fatalf("ModelPath not stable: %q %q", a, b)
	}
	if !strings.HasSuffix(a, ".gguf") || !strings.Contains(a, "/models/blobs/") {
		t.Fatalf("unexpected ModelPath: %q", a)
	}
	dir := t.TempDir()
	rec := Record{ModelRef: "hf.co/org/repo@main/m.gguf", Digest: "sha256:zzz"}
	if err := Upsert(dir, rec); err != nil {
		t.Fatal(err)
	}
	got, err := Find(dir, "hf.co/org/repo@main/m.gguf")
	if err != nil || got.Digest != "sha256:zzz" {
		t.Fatalf("Find by ref: %+v err=%v", got, err)
	}
	if _, err := Find(dir, "sha256:zzz"); err != nil {
		t.Fatalf("Find by digest: %v", err)
	}
	if _, err := Find(dir, "missing"); err == nil {
		t.Fatal("expected error for missing ref")
	}
}

func TestResolveHFURL(t *testing.T) {
	cases := []struct{ in, canon, url string }{
		{"hf.co/org/repo/m.gguf", "hf.co/org/repo@main/m.gguf", "https://huggingface.co/org/repo/resolve/main/m.gguf"},
		{"org/repo/m.gguf", "hf.co/org/repo@main/m.gguf", "https://huggingface.co/org/repo/resolve/main/m.gguf"},
		{"org/repo@v2/sub/m.gguf", "hf.co/org/repo@v2/sub/m.gguf", "https://huggingface.co/org/repo/resolve/v2/sub/m.gguf"},
		{"https://huggingface.co/org/repo/resolve/main/m.gguf", "hf.co/org/repo@main/m.gguf", "https://huggingface.co/org/repo/resolve/main/m.gguf"},
	}
	for _, c := range cases {
		canon, url, err := resolveHFURL(c.in)
		if err != nil || canon != c.canon || url != c.url {
			t.Fatalf("resolveHFURL(%q) = (%q,%q,%v), want (%q,%q,nil)", c.in, canon, url, err, c.canon, c.url)
		}
	}
	if _, _, err := resolveHFURL("org/repo/notagguf.txt"); err == nil {
		t.Fatal("expected error for non-gguf ref")
	}
	if _, _, err := resolveHFURL("justone"); err == nil {
		t.Fatal("expected error for malformed ref")
	}
}

func TestResolveExported(t *testing.T) {
	canon, url, err := Resolve("org/repo/m.gguf")
	if err != nil || canon != "hf.co/org/repo@main/m.gguf" || url != "https://huggingface.co/org/repo/resolve/main/m.gguf" {
		t.Fatalf("Resolve: %q %q %v", canon, url, err)
	}
}

// stubHF stubs httpDo to serve a paths-info response advertising the given
// JSON for org/repo@main and a download body for m.gguf. It records which
// URLs were requested.
func stubHF(t *testing.T, pathsInfoJSON, downloadBody string) *[]string {
	t.Helper()
	prev := httpDo
	urls := &[]string{}
	httpDo = func(_ context.Context, method, url, _ string, body io.Reader, _ string) (io.ReadCloser, int64, error) {
		*urls = append(*urls, method+" "+url)
		switch {
		case method == http.MethodPost && url == "https://huggingface.co/api/models/org/repo/paths-info/main":
			data, err := io.ReadAll(body)
			if err != nil {
				t.Errorf("read paths-info request body: %v", err)
			}
			if !strings.Contains(string(data), `"m.gguf"`) {
				t.Errorf("paths-info request missing file path: %s", data)
			}
			return io.NopCloser(strings.NewReader(pathsInfoJSON)), int64(len(pathsInfoJSON)), nil
		case method == http.MethodGet && url == "https://huggingface.co/org/repo/resolve/main/m.gguf":
			return io.NopCloser(strings.NewReader(downloadBody)), int64(len(downloadBody)), nil
		}
		return nil, 0, fmt.Errorf("unexpected request %s %s", method, url)
	}
	t.Cleanup(func() { httpDo = prev })
	return urls
}

func lfsPathsInfo(file, digest string) string {
	return fmt.Sprintf(`[{"type":"file","path":%q,"size":8,"lfs":{"oid":%q,"size":8,"pointerSize":134}}]`, file, digest)
}

func TestPullDownloadsAndRecords(t *testing.T) {
	sum := sha256.Sum256([]byte("GGUFDATA"))
	digest := hex.EncodeToString(sum[:])
	stubHF(t, lfsPathsInfo("m.gguf", digest), "GGUFDATA")

	dir := t.TempDir()
	rec, err := Pull(context.Background(), PullOptions{StateDir: dir, ModelRef: "org/repo/m.gguf"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if rec.SizeBytes != 8 || rec.ModelRef != "hf.co/org/repo@main/m.gguf" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	data, err := os.ReadFile(rec.OutputPath)
	if err != nil || string(data) != "GGUFDATA" {
		t.Fatalf("blob not written: %q err=%v", data, err)
	}
	if rec.Digest != "sha256:"+digest {
		t.Fatalf("digest = %q, want sha256:%s", rec.Digest, digest)
	}
	found, err := Find(dir, rec.ModelRef)
	if err != nil || found.OutputPath != rec.OutputPath {
		t.Fatalf("record not indexed: %+v err=%v", found, err)
	}
}

func TestPullRejectsDigestMismatch(t *testing.T) {
	wrong := strings.Repeat("ab", 32)
	stubHF(t, lfsPathsInfo("m.gguf", wrong), "GGUFDATA")

	dir := t.TempDir()
	_, err := Pull(context.Background(), PullOptions{StateDir: dir, ModelRef: "org/repo/m.gguf"})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch error, got %v", err)
	}
	assertNoModelArtifacts(t, dir)
}

func TestPullRejectsTamperedUpstreamDigestPrefix(t *testing.T) {
	// An "sha256:"-prefixed oid must still verify against the same content.
	sum := sha256.Sum256([]byte("GGUFDATA"))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	stubHF(t, lfsPathsInfo("m.gguf", digest), "GGUFDATA")

	dir := t.TempDir()
	if _, err := Pull(context.Background(), PullOptions{StateDir: dir, ModelRef: "org/repo/m.gguf"}); err != nil {
		t.Fatalf("Pull with prefixed oid: %v", err)
	}
}

func TestPullFailsClosedWithoutLFSDigest(t *testing.T) {
	urls := stubHF(t, `[{"type":"file","path":"m.gguf","size":8}]`, "GGUFDATA")

	dir := t.TempDir()
	_, err := Pull(context.Background(), PullOptions{StateDir: dir, ModelRef: "org/repo/m.gguf"})
	if err == nil || !strings.Contains(err.Error(), "no LFS sha256") {
		t.Fatalf("expected missing-LFS error, got %v", err)
	}
	for _, u := range *urls {
		if strings.HasPrefix(u, http.MethodGet+" ") {
			t.Fatalf("download attempted despite missing upstream digest: %v", *urls)
		}
	}
	assertNoModelArtifacts(t, dir)
}

func TestPullFailsClosedWhenFileUnknownUpstream(t *testing.T) {
	stubHF(t, `[]`, "GGUFDATA")

	dir := t.TempDir()
	_, err := Pull(context.Background(), PullOptions{StateDir: dir, ModelRef: "org/repo/m.gguf"})
	if err == nil || !strings.Contains(err.Error(), "has no file") {
		t.Fatalf("expected unknown-file error, got %v", err)
	}
	assertNoModelArtifacts(t, dir)
}

func TestPullFailsClosedOnMalformedUpstreamDigest(t *testing.T) {
	stubHF(t, lfsPathsInfo("m.gguf", "not-a-digest"), "GGUFDATA")

	dir := t.TempDir()
	_, err := Pull(context.Background(), PullOptions{StateDir: dir, ModelRef: "org/repo/m.gguf"})
	if err == nil || !strings.Contains(err.Error(), "malformed LFS digest") {
		t.Fatalf("expected malformed-digest error, got %v", err)
	}
	assertNoModelArtifacts(t, dir)
}

// assertNoModelArtifacts checks that a failed pull left no blob, no partial
// download, and no index record behind.
func assertNoModelArtifacts(t *testing.T, dir string) {
	t.Helper()
	list, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("failed pull recorded models: %+v", list)
	}
	blobs, err := os.ReadDir(filepath.Join(dir, "models", "blobs"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("ReadDir blobs: %v", err)
	}
	for _, entry := range blobs {
		t.Fatalf("failed pull left blob %q behind", entry.Name())
	}
}

func TestPruneDeleteFilesRemovesPresentBlob(t *testing.T) {
	dir := t.TempDir()
	blob := ModelPath(dir, "hf.co/org/repo@main/present.gguf")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(dir, Record{ModelRef: "hf.co/org/repo@main/present.gguf", OutputPath: blob}); err != nil {
		t.Fatal(err)
	}
	res, err := Prune(dir, true)
	if err != nil {
		t.Fatalf("Prune(true): %v", err)
	}
	if len(res.Deleted) != 1 || len(res.Removed) != 1 {
		t.Fatalf("expected 1 deleted + 1 removed, got %+v", res)
	}
	if _, statErr := os.Stat(blob); !os.IsNotExist(statErr) {
		t.Fatal("blob should have been deleted")
	}
	list, _ := List(dir)
	if len(list) != 0 {
		t.Fatalf("expected empty index, got %+v", list)
	}
}

func TestRemovePropagatesDeleteError(t *testing.T) {
	dir := t.TempDir()
	// Point OutputPath at a NON-EMPTY directory so os.Remove fails with a
	// non-IsNotExist error (ENOTEMPTY) portably.
	blobDir := filepath.Join(dir, "models", "blobs", "stuck.gguf")
	if err := os.MkdirAll(filepath.Join(blobDir, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(dir, Record{ModelRef: "hf.co/org/repo@main/stuck.gguf", OutputPath: blobDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(dir, "hf.co/org/repo@main/stuck.gguf", true); err == nil {
		t.Fatal("expected Remove to propagate non-IsNotExist delete error")
	}
	// Prune with deleteFiles=true must likewise propagate.
	if _, err := Prune(dir, true); err == nil {
		t.Fatal("expected Prune to propagate non-IsNotExist delete error")
	}
}

func TestRemoveAndPrune(t *testing.T) {
	dir := t.TempDir()
	blob := ModelPath(dir, "hf.co/org/repo@main/m.gguf")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(dir, Record{ModelRef: "hf.co/org/repo@main/m.gguf", OutputPath: blob}); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(dir, "hf.co/org/repo@main/m.gguf", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Fatal("blob not deleted")
	}
	list, _ := List(dir)
	if len(list) != 0 {
		t.Fatalf("expected empty index, got %+v", list)
	}
	if err := Upsert(dir, Record{ModelRef: "hf.co/org/repo@main/gone.gguf", OutputPath: dir + "/models/blobs/none.gguf"}); err != nil {
		t.Fatal(err)
	}
	res, err := Prune(dir, false)
	if err != nil || len(res.Removed) != 1 {
		t.Fatalf("Prune: %+v err=%v", res, err)
	}
}
