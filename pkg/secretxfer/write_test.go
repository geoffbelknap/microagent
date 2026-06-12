package secretxfer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidName(t *testing.T) {
	good := []string{"API_KEY", "_x", "Db2", "A1_B2"}
	bad := []string{"", "1abc", "a-b", "a.b", "../escape", "a/b", "a b", "Aé"}
	for _, n := range good {
		if !ValidName(n) {
			t.Errorf("expected %q valid", n)
		}
	}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("expected %q invalid", n)
		}
	}
}

func TestWriteSecretsCreatesFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	bundle := Bundle{Secrets: []Entry{
		{Name: "API_KEY", Value: []byte("sekret")},
		{Name: "DB_PASS", Value: []byte("p@ss\n")},
	}}
	if err := WriteSecrets(root, bundle); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sekret" {
		t.Fatalf("API_KEY = %q, want verbatim", data)
	}
	data2, _ := os.ReadFile(filepath.Join(root, "DB_PASS"))
	if string(data2) != "p@ss\n" {
		t.Fatalf("DB_PASS = %q, want verbatim", data2)
	}
	// POSIX permission bits do not survive on Windows hosts (os.Chmod only
	// honors the write bit there); WriteSecrets runs in the guest tmpfs in
	// production, so the mode assertions are POSIX-only.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "API_KEY"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o400 {
			t.Fatalf("mode = %v, want 0400", info.Mode().Perm())
		}
		dirInfo, _ := os.Stat(root)
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %v, want 0700", dirInfo.Mode().Perm())
		}
	}
}

func TestWriteSecretsRejectsBadName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	if err := WriteSecrets(root, Bundle{Secrets: []Entry{{Name: "../escape", Value: []byte("x")}}}); err == nil {
		t.Fatal("expected error for unsafe name")
	}
}

func TestWriteSecretsRejectsDuplicate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	b := Bundle{Secrets: []Entry{{Name: "A", Value: []byte("1")}, {Name: "A", Value: []byte("2")}}}
	if err := WriteSecrets(root, b); err == nil {
		t.Fatal("expected error for duplicate name")
	}
}
