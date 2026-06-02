package secret

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvProviderReadsVariable(t *testing.T) {
	p := &EnvProvider{Getenv: func(k string) string {
		if k == "API_KEY" {
			return "sekret"
		}
		return ""
	}}
	got, err := p.Resolve(context.Background(), "API_KEY")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if string(got) != "sekret" {
		t.Fatalf("value = %q, want %q", got, "sekret")
	}
	if !p.Plaintext() {
		t.Fatal("env provider must report Plaintext() == true")
	}
}

func TestEnvProviderMissingNameErrors(t *testing.T) {
	p := &EnvProvider{Getenv: func(string) string { return "" }}
	if _, err := p.Resolve(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty variable name")
	}
}

func TestFileProviderReadsContentsVerbatim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("line1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &FileProvider{}
	got, err := p.Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if string(got) != "line1\n" {
		t.Fatalf("value = %q, want verbatim %q (no trimming)", got, "line1\n")
	}
	if !p.Plaintext() {
		t.Fatal("file provider must report Plaintext() == true")
	}
}

func TestFileProviderMissingFileErrors(t *testing.T) {
	p := &FileProvider{}
	if _, err := p.Resolve(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestDotenvProviderReadsKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.env")
	contents := "# comment\nFOO=bar\nexport API_KEY=\"sekret\"\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &DotenvProvider{}
	got, err := p.Resolve(context.Background(), path+"#API_KEY")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if string(got) != "sekret" {
		t.Fatalf("value = %q, want %q", got, "sekret")
	}
	if !p.Plaintext() {
		t.Fatal("dotenv provider must report Plaintext() == true")
	}
}

func TestDotenvProviderMissingKeyErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.env")
	if err := os.WriteFile(path, []byte("FOO=bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &DotenvProvider{}
	if _, err := p.Resolve(context.Background(), path+"#NOPE"); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestDotenvProviderMalformedReferenceErrors(t *testing.T) {
	p := &DotenvProvider{}
	if _, err := p.Resolve(context.Background(), "/path/without/hash"); err == nil {
		t.Fatal("expected error for reference without #KEY")
	}
}
