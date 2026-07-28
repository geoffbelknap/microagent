package main

import (
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// captureError runs the renderer with a controlled output format.
func captureError(t *testing.T, format string, err error) (string, int) {
	t.Helper()
	outputFormat = format
	t.Cleanup(func() { outputFormat = "" })
	t.Setenv("MICROAGENT_OUTPUT", "")
	var b strings.Builder
	code := renderCLIError(&b, err)
	return b.String(), code
}

// TestCLIErrorsCarryTheClassifiersRemediation is the one-library-path pin.
// mapStructuredError — seven kinds, remediation, retryability — was wired
// only to MCP, so an AI agent received {kind, remediation, retryable} while
// a human at a terminal received the raw text: the classifier was the
// library path, and the CLI was the adapter that dropped it.
func TestCLIErrorsCarryTheClassifiersRemediation(t *testing.T) {
	err := operation.New(operation.ErrorNotFound, "workspace nope not found")

	out, code := captureError(t, "", err)

	if !strings.Contains(out, "workspace nope not found") {
		t.Errorf("message lost:\n%s", out)
	}
	if !strings.Contains(out, "Check the workspace name") {
		t.Errorf("remediation the classifier knows was dropped:\n%s", out)
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1 for a permanent failure", code)
	}
}

// TestTransientFailuresExitTempfail gives scripts the branch MCP callers
// always had: retryable is a field there, an exit code here. 75 is
// sysexits EX_TEMPFAIL.
func TestTransientFailuresExitTempfail(t *testing.T) {
	err := operation.New(operation.ErrorTransient, "supervisor busy; try again")

	_, code := captureError(t, "", err)

	if code != exitTransient {
		t.Errorf("exit = %d, want %d for a retryable failure", code, exitTransient)
	}
}

// TestExplicitJSONGetsTheStructuredObject: one line on stderr, machine
// fields intact — and only when asked, because TTY inference reshaping every
// piped failure would change what existing scripts grep.
func TestExplicitJSONGetsTheStructuredObject(t *testing.T) {
	err := operation.New(operation.ErrorNotFound, "workspace nope not found")

	out, _ := captureError(t, "json", err)

	for _, want := range []string{`"kind":"not_found"`, `"retryable":false`, `"remediation"`} {
		if !strings.Contains(out, want) {
			t.Errorf("structured error missing %s:\n%s", want, out)
		}
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("structured error is not one line:\n%s", out)
	}

	// Default (no flag, no env) stays text even though nothing is a TTY here.
	out, _ = captureError(t, "", err)
	if strings.Contains(out, `"kind"`) {
		t.Errorf("JSON leaked without being requested:\n%s", out)
	}
}

// TestTypedWorkspaceErrorsClassify covers the typed path the tracker's
// execution plan once mislabeled as missing: WorkspaceNotFoundError has
// always classified; the CLI just never showed the result.
func TestTypedWorkspaceErrorsClassify(t *testing.T) {
	out, code := captureError(t, "json", workspace.WorkspaceNotFoundError{Name: "ghost"})

	if !strings.Contains(out, `"kind":"not_found"`) {
		t.Errorf("typed not-found did not classify:\n%s", out)
	}
	if code != 1 {
		t.Errorf("exit = %d", code)
	}
}

// TestOneShotsRejectEntrypoint pins the accepted-silently fix found while
// documenting the execution knobs: --entrypoint is what later `start`s of a
// CREATED workspace boot, run and dispatch have no later starts, and the flag
// was parsed and ignored — the user got the image default and no signal that
// their flag did nothing.
func TestOneShotsRejectEntrypoint(t *testing.T) {
	for _, command := range []string{"run", "dispatch"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stdout, stderr, code := runMainCapture(t, command,
				"--dry-run", "--image", "docker.io/library/alpine:3.20",
				"--entrypoint", "/x", "--exec", "echo hi")
			combined := string(stdout) + string(stderr)

			if code == 0 {
				t.Errorf("%s accepted --entrypoint silently", command)
			}
			if !strings.Contains(combined, "does not support --entrypoint") {
				t.Errorf("rejection does not name the flag:\n%s", combined)
			}
			if !strings.Contains(combined, "use --exec") {
				t.Errorf("rejection does not name the alternative:\n%s", combined)
			}
		})
	}

	// The control: create keeps it — entrypoint is create's whole point.
	t.Setenv("HOME", t.TempDir())
	stdout, stderr, code := runMainCapture(t, "create", "probe",
		"--dry-run", "--image", "docker.io/library/alpine:3.20", "--entrypoint", "/app/s.sh")
	if code != 0 || strings.Contains(string(stdout)+string(stderr), "does not support") {
		t.Errorf("create rejected --entrypoint (code=%d):\n%s%s", code, stdout, stderr)
	}
}

// TestUnclassifiedCLIErrorsSkipTheCorrelationRemediation pins the empty-ID
// fallback: the CLI never issues a correlation ID, and the old fallback told
// a user to "Inspect correlation_id  in surrounding logs" — a double space
// and an ID that does not exist. An unclassified CLI error now prints its
// message alone; callers that do pass an ID (MCP) keep the pointer.
func TestUnclassifiedCLIErrorsSkipTheCorrelationRemediation(t *testing.T) {
	err := run(t.Context(), []string{"show"}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("run show err = %v", err)
	}

	out, _ := captureError(t, "", err)

	if strings.Contains(out, "correlation_id") {
		t.Errorf("CLI error points at a correlation ID that was never issued:\n%s", out)
	}
	if lines := strings.Split(strings.TrimRight(out, "\n"), "\n"); len(lines) != 1 {
		t.Errorf("unclassified error grew extra lines:\n%s", out)
	}

	if mapped := mapStructuredError(err, "req-42"); !strings.Contains(mapped.Remediation, "correlation_id req-42") {
		t.Errorf("caller-supplied correlation ID lost its pointer: %q", mapped.Remediation)
	}
}

// TestUnknownCommandSuggestsTheNearMiss pins the did-you-mean over the same
// registry that rejected the input. "statu" was a one-edit miss on a
// 45-command surface, and the old message asked the user to scan the full
// list to find the character they dropped.
func TestUnknownCommandSuggestsTheNearMiss(t *testing.T) {
	for input, want := range map[string]string{
		"statu":  "status",
		"lisst":  "list",
		"delet":  "delete",
		"quaran": "quarantine", // prefix match beyond distance 2
		"sp":     "cp",         // an alias-adjacent short miss still resolves
	} {
		if got := nearestCommandName(input); got != want {
			t.Errorf("nearestCommandName(%q) = %q, want %q", input, got, want)
		}
	}
	if got := nearestCommandName("zzzqqq"); got != "" {
		t.Errorf("nonsense input got a confident suggestion: %q", got)
	}
}

// TestRemovedOutputFlagsGetTheMigrationNote pins the --text/--human tripwire
// beside the --json one that already existed for exactly this class.
func TestRemovedOutputFlagsGetTheMigrationNote(t *testing.T) {
	for _, flagName := range []string{"--text", "--human"} {
		t.Run(flagName, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stdout, stderr, code := runMainCapture(t, "list", flagName)
			combined := string(stdout) + string(stderr)

			if code == 0 {
				t.Errorf("%s accepted; it was removed", flagName)
			}
			if !strings.Contains(combined, "--output text") {
				t.Errorf("no migration note pointing at --output text:\n%s", combined)
			}
		})
	}
	// The suffix match must not fire for flags that merely contain the word.
	t.Setenv("HOME", t.TempDir())
	stdout, stderr, _ := runMainCapture(t, "list", "--textual")
	if strings.Contains(string(stdout)+string(stderr), "--output text") {
		t.Errorf("migration note fired for an unrelated flag:\n%s%s", stdout, stderr)
	}
}
