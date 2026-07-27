package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// treeSnapshot lists every path under root, relative and sorted, so a test can
// assert that a command left the state directory exactly as it found it.
func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel != "." {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)
	return paths
}

// helpFlags are the spellings every command must honour. Bare "help" is
// deliberately excluded: wantsHelp accepts it, but several commands take a
// workspace name as their first positional, where "help" is a legitimate
// (if unlikely) workspace name rather than a request for documentation.
var helpFlags = []string{"--help", "-h"}

// TestHelpNeverActs is the conformance test for the rule that a flag meaning
// "explain yourself" resolves before any side effect. Every registered command
// is invoked with each help flag against a HOME that starts empty, and must
// write nothing.
//
// Pinning this across the whole registry rather than per command is the point:
// the failure mode is a command that forgets to check for help before doing its
// work, which stays invisible until someone runs it in anger. A command added
// later inherits the test for free.
//
// StateDir() derives from HOME, so redirecting HOME isolates the assertion from
// the developer's real ~/.microagent.
func TestHelpNeverActs(t *testing.T) {
	for _, spec := range commandRegistry {
		for _, flag := range helpFlags {
			t.Run(spec.Name+"/"+flag, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)

				stdout, stderr, code := runMainCapture(t, spec.Name, flag)

				if after := treeSnapshot(t, home); len(after) != 0 {
					t.Errorf("%s %s wrote %v under an empty HOME; help must never act",
						spec.Name, flag, after)
				}
				if len(stdout) == 0 && len(stderr) == 0 {
					t.Errorf("%s %s produced no output; help must explain the command", spec.Name, flag)
				}

				// A hidden refusal stub (compose) exists to reject with
				// guidance, so a nonzero exit is its contract. Every command on
				// the visible surface should explain itself successfully.
				if !spec.Hidden && code != 0 {
					t.Errorf("%s %s exited %d, want 0; asking for help is not a failure\nstdout: %s\nstderr: %s",
						spec.Name, flag, code, stdout, stderr)
				}
			})
		}
	}
}

// TestBareCommandDoesNotActOnHelpFlag guards the narrower shape that has
// actually broken in this family: a command taking no other arguments, whose
// implementation therefore never looks at the ones it was given, and which runs
// its real work when handed --help.
func TestBareCommandDoesNotActOnHelpFlag(t *testing.T) {
	// version is intentionally absent: main handles it ahead of the registry
	// lookup, so it is not a registry command and takes no flags.
	for _, name := range []string{"gc", "profiles", "contract", "host", "doctor"} {
		spec, ok := lookupCommand(name)
		if !ok {
			t.Fatalf("command %q is not registered; update this list", name)
		}
		t.Run(spec.Name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			_, _, code := runMainCapture(t, spec.Name, "--help")
			if code != 0 {
				t.Errorf("%s --help exited %d, want 0", spec.Name, code)
			}
			if after := treeSnapshot(t, home); len(after) != 0 {
				t.Errorf("%s --help wrote %v under an empty HOME; it must only print help",
					spec.Name, after)
			}
		})
	}
}
