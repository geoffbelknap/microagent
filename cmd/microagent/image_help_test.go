package main

import (
	"strings"
	"testing"
)

// imageSubcommands are the verbs reachable under `microagent image`.
var imageSubcommands = []string{"pull", "list", "push", "tag", "delete", "prune"}

// TestImageSubcommandHelpDoesNotAct pins the fix for a group whose every
// subcommand took --help as an image reference.
//
//	$ microagent image delete --help
//	image "--help" not found                                     exit 1
//	$ microagent image pull --help
//	parse OCI image ref "docker.io/library/--help": invalid ...   exit 1
//
// delete and push ran a lookup with it and pull put it through the OCI ref
// parser, so a flag meaning "don't do it" reached the operation. Nothing was
// destroyed — no image is named --help — but the guard belongs ahead of the
// work, not downstream of it.
func TestImageSubcommandHelpDoesNotAct(t *testing.T) {
	for _, sub := range imageSubcommands {
		t.Run(sub, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			stdout, stderr, code := runMainCapture(t, "image", sub, "--help")
			combined := string(stdout) + string(stderr)

			// Asking for help is not a failure.
			if code != 0 {
				t.Errorf("exit = %d, want 0:\n%s", code, combined)
			}
			if !strings.Contains(combined, "Usage:") {
				t.Errorf("no usage block:\n%s", combined)
			}
			// The operation must not have been attempted with "--help" as its
			// argument. These are the messages it produced when it was.
			for _, leak := range []string{
				`image "--help" not found`,
				`library/--help`,
				"not found in local layout",
			} {
				if strings.Contains(combined, leak) {
					t.Errorf("image %s --help reached the operation (%q):\n%s", sub, leak, combined)
				}
			}
		})
	}
}

// TestImageSubcommandHelpIsScoped checks help answers what was asked. Dumping
// all six shapes at someone who asked about one is the behavior `model --help`
// had before it was fixed.
func TestImageSubcommandHelpIsScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	stdout, stderr, _ := runMainCapture(t, "image", "delete", "--help")
	combined := string(stdout) + string(stderr)

	if !strings.Contains(combined, "microagent image delete <image>") {
		t.Errorf("missing the delete shape:\n%s", combined)
	}
	if strings.Contains(combined, "microagent image pull") {
		t.Errorf("delete --help listed pull's shape too:\n%s", combined)
	}
}

// TestImageGroupHelpStillListsEverything is the control: scoping subcommand
// help must not narrow the group's own page.
func TestImageGroupHelpStillListsEverything(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	stdout, stderr, code := runMainCapture(t, "image", "--help")
	combined := string(stdout) + string(stderr)

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	for _, sub := range imageSubcommands {
		if !strings.Contains(combined, "microagent image "+sub) {
			t.Errorf("group help omits %s:\n%s", sub, combined)
		}
	}
	if !strings.Contains(combined, "Options:") {
		t.Errorf("group help lost its options block:\n%s", combined)
	}
}
