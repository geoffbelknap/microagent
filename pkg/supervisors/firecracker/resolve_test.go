package firecracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stageBinary writes an executable stand-in and returns its path.
func stageBinary(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveBinaryFromLayouts pins the one shared resolution order:
// env override, PATH, then the anchor's ../libexec tree. Both the supervisor's
// boot path and doctor's probe call this function, so this table is the whole
// contract — there is no second copy left to drift.
func TestResolveBinaryFromLayouts(t *testing.T) {
	t.Run("env override wins and is validated", func(t *testing.T) {
		fc := stageBinary(t, t.TempDir(), "firecracker")
		t.Setenv("MICROAGENT_FIRECRACKER", fc)
		t.Setenv("PATH", t.TempDir())

		got, err := ResolveBinaryFrom("")
		if err != nil || got != fc {
			t.Errorf("got %q, %v; want the override", got, err)
		}
	})

	t.Run("missing env override fails with the fix attached", func(t *testing.T) {
		t.Setenv("MICROAGENT_FIRECRACKER", "/nonexistent/fc")

		_, err := ResolveBinaryFrom("")
		if err == nil || !strings.Contains(err.Error(), "point it at the firecracker binary") {
			t.Errorf("want the remediation, got: %v", err)
		}
	})

	t.Run("env override naming a directory fails", func(t *testing.T) {
		t.Setenv("MICROAGENT_FIRECRACKER", t.TempDir())

		_, err := ResolveBinaryFrom("")
		if err == nil || !strings.Contains(err.Error(), "is a directory") {
			t.Errorf("a directory satisfied the override check: %v", err)
		}
	})

	t.Run("PATH is consulted before the anchor tree", func(t *testing.T) {
		pathDir := t.TempDir()
		fromPath := stageBinary(t, pathDir, "firecracker")
		tree := t.TempDir()
		stageBinary(t, filepath.Join(tree, "libexec"), "firecracker")
		anchor := stageBinary(t, filepath.Join(tree, "bin"), "supervisor")
		t.Setenv("MICROAGENT_FIRECRACKER", "")
		t.Setenv("PATH", pathDir)

		got, err := ResolveBinaryFrom(anchor)
		if err != nil || got != fromPath {
			t.Errorf("got %q, %v; want the PATH hit", got, err)
		}
	})

	t.Run("anchor tree resolves, including through a symlinked anchor", func(t *testing.T) {
		tree := t.TempDir()
		packaged := stageBinary(t, filepath.Join(tree, "libexec"), "firecracker")
		real := stageBinary(t, filepath.Join(tree, "bin"), "supervisor")
		linkDir := t.TempDir()
		link := filepath.Join(linkDir, "supervisor")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		t.Setenv("MICROAGENT_FIRECRACKER", "")
		t.Setenv("PATH", t.TempDir())

		// The link's own dir has no ../libexec; only the real tree does. A
		// Homebrew bin/ entry is exactly this shape.
		got, err := ResolveBinaryFrom(link)
		if err != nil || got != packaged {
			t.Errorf("got %q, %v; want %q via the resolved anchor", got, err, packaged)
		}
	})

	t.Run("nothing anywhere is the install error", func(t *testing.T) {
		t.Setenv("MICROAGENT_FIRECRACKER", "")
		t.Setenv("PATH", t.TempDir())

		_, err := ResolveBinaryFrom(stageBinary(t, filepath.Join(t.TempDir(), "bin"), "supervisor"))
		if err == nil || !strings.Contains(err.Error(), "brew install") {
			t.Errorf("want BinaryNotFoundError with its install fix, got: %v", err)
		}
	})
}
