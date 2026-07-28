package main

import (
	"strings"
	"testing"
)

// TestGroupWithoutSubcommandIsAHelpRequest pins one convention for every
// command group: invoked bare, a group explains itself on stdout and exits 0
// (the git-remote convention six of the nine already followed). Before this,
// the same omission was a success in snapshot, a usage error in model, and in
// image it silently dispatched `list` — a bare `microagent image` emitted a
// JSON document indistinguishable from a deliberate listing.
func TestGroupWithoutSubcommandIsAHelpRequest(t *testing.T) {
	groups := []string{"snapshot", "volume", "registry", "kernel", "rootfs", "secret", "model", "image"}
	for _, group := range groups {
		t.Run(group, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stdout, stderr, code := runMainCapture(t, group)

			if code != 0 {
				t.Errorf("bare %q exit = %d, want 0: an omitted subcommand is a help request\nstderr:\n%s", group, code, stderr)
			}
			out := string(stdout)
			if !strings.Contains(strings.ToLower(out), "usage") {
				t.Errorf("bare %q did not explain the group:\nstdout:\n%s\nstderr:\n%s", group, out, stderr)
			}
			if strings.HasPrefix(strings.TrimSpace(out), "{") {
				t.Errorf("bare %q dispatched work (JSON output) instead of explaining the group:\n%s", group, out)
			}
		})
	}
}

// TestArgumentTakingCommandsKeepTheError is the control that untangles network
// from the group question: network takes <workspace> — bare invocation is a
// missing argument, not an omitted subcommand, and must keep the failure
// contract the lifecycle verbs use (status is the reference).
func TestArgumentTakingCommandsKeepTheError(t *testing.T) {
	for _, command := range []string{"network", "status"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stdout, stderr, code := runMainCapture(t, command)

			if code == 0 {
				t.Errorf("bare %q exited 0; a missing required argument is a failure", command)
			}
			combined := string(stdout) + string(stderr)
			if !strings.Contains(combined, "usage: microagent "+command) {
				t.Errorf("bare %q did not say what it needs:\n%s", command, combined)
			}
		})
	}
}

// TestImageListStillLists is the positive control for the image change: the
// listing did not vanish, it just requires being asked for.
func TestImageListStillLists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stdout, stderr, code := runMainCapture(t, "image", "list", "--state-dir", t.TempDir())

	if code != 0 {
		t.Fatalf("image list exit = %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(string(stdout), "images") {
		t.Errorf("image list no longer lists:\n%s", stdout)
	}
}

// TestUnknownModelCommandIsNamed pins the sharpened trailing error: with the
// omission now a help request, the usage-line error only fires for a real
// unknown subverb, and it names the input instead of restating the whole verb
// list.
func TestUnknownModelCommandIsNamed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stdout, stderr, code := runMainCapture(t, "model", "bogus")

	if code == 0 {
		t.Error("unknown model subcommand exited 0")
	}
	combined := string(stdout) + string(stderr)
	if !strings.Contains(combined, `"bogus"`) {
		t.Errorf("error does not name the input:\n%s", combined)
	}
}

// TestHelpAnywhereNeverActs extends the group convention to --help beside a
// mistyped subverb: `registry bogus --help` is a question, and four groups
// (registry, kernel, secret, rootfs) used to answer it "unknown command"
// exit 1 while their siblings explained themselves. One contract now: a help
// spelling anywhere in a group's arguments explains on stdout, exit 0.
func TestHelpAnywhereNeverActs(t *testing.T) {
	groups := []string{"snapshot", "volume", "registry", "kernel", "rootfs", "secret", "model", "image"}
	for _, group := range groups {
		t.Run(group, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stdout, stderr, code := runMainCapture(t, group, "bogus", "--help")

			if code != 0 {
				t.Errorf("%s bogus --help exit = %d, want 0\nstderr:\n%s", group, code, stderr)
			}
			if !strings.Contains(strings.ToLower(string(stdout)), "usage") {
				t.Errorf("%s bogus --help did not explain:\n%s%s", group, stdout, stderr)
			}
		})
	}

	// The control: a genuinely unknown subverb without a help spelling still
	// fails and names itself.
	t.Setenv("HOME", t.TempDir())
	stdout, stderr, code := runMainCapture(t, "registry", "bogus")
	if code == 0 || !strings.Contains(string(stdout)+string(stderr), "bogus") {
		t.Errorf("unknown subverb no longer fails by name (exit=%d):\n%s%s", code, stdout, stderr)
	}
}
