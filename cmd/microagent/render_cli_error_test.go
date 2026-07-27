package main

import (
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
