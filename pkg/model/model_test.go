package model

import (
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
