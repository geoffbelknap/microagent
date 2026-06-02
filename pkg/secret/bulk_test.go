package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("A=1\nB=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile error: %v", err)
	}
	if got["A"] != "1" || got["B"] != "two" {
		t.Fatalf("got %v, want A=1 B=two", got)
	}
}

func TestLoadEnvFileMissingErrors(t *testing.T) {
	if _, err := LoadEnvFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing env file")
	}
}

func TestLoadJSONMap(t *testing.T) {
	got, err := LoadJSONMap(strings.NewReader(`{"A":"1","B":"two"}`))
	if err != nil {
		t.Fatalf("LoadJSONMap error: %v", err)
	}
	if got["A"] != "1" || got["B"] != "two" {
		t.Fatalf("got %v, want A=1 B=two", got)
	}
}

func TestLoadJSONMapRejectsNonStringValues(t *testing.T) {
	if _, err := LoadJSONMap(strings.NewReader(`{"A":1}`)); err == nil {
		t.Fatal("expected error for non-string JSON value")
	}
}

func TestLoadJSONMapRejectsNull(t *testing.T) {
	if _, err := LoadJSONMap(strings.NewReader(`null`)); err == nil {
		t.Fatal("expected error for JSON null")
	}
}
