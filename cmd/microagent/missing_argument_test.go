package main

import (
	"strings"
	"testing"
)

// lifecycleVerbs are the commands that take a workspace name and reach the
// low-level supervisor request path when they do not get one.
var lifecycleVerbs = []string{
	"status", "inspect", "halt", "stop", "kill",
	"pause", "resume", "quarantine", "delete", "rm", "start",
}

// TestMissingWorkspaceNameIsAUsageError pins the fix for a usage error that
// presented as a failed VM operation.
//
// With no argument these verbs used to build a request with no identity and
// report the supervisor's contract violation verbatim:
//
//	Status: failed
//	Error: halt workspace "unknown" failed (backend=linux-kvm
//	  supervisor=/opt/.../microagent-firecracker-supervisor): identity.runtimeID is required
//
// Three separate leaks in one message — a workspace called "unknown" the user
// never typed, the absolute supervisor path, and an internal contract field —
// framed as a runtime failure rather than a missing argument.
func TestMissingWorkspaceNameIsAUsageError(t *testing.T) {
	for _, verb := range lifecycleVerbs {
		t.Run(verb, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			stdout, stderr, code := runMainCapture(t, verb)
			combined := string(stdout) + string(stderr)

			if code == 0 {
				t.Errorf("%s with no argument exited 0; a missing required argument is a failure", verb)
			}
			if !strings.Contains(combined, "usage: microagent") {
				t.Errorf("%s with no argument did not state its usage:\n%s", verb, combined)
			}
			for _, leak := range []string{
				"identity.runtimeID", // internal contract field
				`"unknown"`,          // a workspace name the user never typed
				"supervisor=",        // the absolute supervisor path
				"Status: failed",     // framed as a failed operation
			} {
				if strings.Contains(combined, leak) {
					t.Errorf("%s leaked %q into a usage error:\n%s", verb, leak, combined)
				}
			}
		})
	}
}

// TestRequestJSONStillReachesTheRequestPath guards the form that legitimately
// names no workspace: the request file supplies the identity, which is why
// hasWorkspaceStateTarget returns false for it. The usage check must not
// swallow it.
func TestRequestJSONStillReachesTheRequestPath(t *testing.T) {
	for _, verb := range []string{"halt", "status", "delete", "start"} {
		t.Run(verb, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			missing := t.TempDir() + "/absent-request.json"
			stdout, stderr, _ := runMainCapture(t, verb, "--request-json", missing)
			combined := string(stdout) + string(stderr)

			if strings.Contains(combined, "usage: microagent") {
				t.Errorf("%s --request-json hit the missing-name usage error; the request file supplies the identity:\n%s",
					verb, combined)
			}
			if !strings.Contains(combined, "absent-request.json") {
				t.Errorf("%s --request-json did not report the unreadable request file:\n%s", verb, combined)
			}
		})
	}
}

// TestExecWithNoArgumentsFailsButHelpDoesNot separates a help request from a
// forgotten argument. exec printed its help and exited 0 for both, so a script
// could not tell a missing workspace name from a successful run.
func TestExecWithNoArgumentsFailsButHelpDoesNot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, _, code := runMainCapture(t, "exec"); code == 0 {
		t.Error("exec with no arguments exited 0; a missing workspace name is a failure")
	}
	if _, _, code := runMainCapture(t, "exec", "--help"); code != 0 {
		t.Errorf("exec --help exited %d; asking for help is not a failure", code)
	}
}
