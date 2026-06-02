package secretxfer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestZeroFileOverwritesBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(path, []byte("sekret-value"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := zeroFile(path); err != nil {
		t.Fatalf("zeroFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len("sekret-value") {
		t.Fatalf("length changed: %d", len(data))
	}
	if !bytes.Equal(data, make([]byte, len(data))) {
		t.Fatalf("bytes not zeroed: %q", data)
	}
}

func TestPurgeSecretsZeroesAndRemoves(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "API_KEY"), []byte("sekret"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "DB"), []byte("pw"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := PurgeSecrets(root); err != nil {
		t.Fatalf("PurgeSecrets: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dir, got %d entries", len(entries))
	}
}

func TestPurgeSecretsMissingRootIsNoOp(t *testing.T) {
	if err := PurgeSecrets(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("missing root should be a no-op, got %v", err)
	}
}
