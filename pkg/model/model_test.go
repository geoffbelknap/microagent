package model

import (
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
