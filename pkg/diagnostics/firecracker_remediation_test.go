package diagnostics

import (
	"strings"
	"testing"
)

// TestEveryFirecrackerResolutionFailureSaysWhatToDo closes the last gap in
// doctor's help gradient.
//
// doctor's non-blocking warnings tell you what to do — the TPROXY warning names
// `modprobe nft_tproxy`. Its blocking error did not, which is backwards: the
// one that stops you is the one that most needs a next step.
//
// BinaryNotFoundError was given remediation, but the override path kept
// reporting a bare stat error, so setting MICROAGENT_FIRECRACKER to a typo'd
// path produced the least actionable message of the three.
func TestEveryFirecrackerResolutionFailureSaysWhatToDo(t *testing.T) {
	t.Run("override does not resolve", func(t *testing.T) {
		t.Setenv("MICROAGENT_FIRECRACKER", "/nonexistent/firecracker")

		_, err := ResolveFirecrackerPathFor("")
		if err == nil {
			t.Fatal("resolved a path that does not exist")
		}
		if !strings.Contains(err.Error(), "MICROAGENT_FIRECRACKER") {
			t.Errorf("error does not name the variable at fault: %v", err)
		}
		if !remediates(err.Error()) {
			t.Errorf("error states the problem but not the fix: %v", err)
		}
	})

	t.Run("not found anywhere", func(t *testing.T) {
		t.Setenv("MICROAGENT_FIRECRACKER", "")
		t.Setenv("PATH", t.TempDir())

		_, err := ResolveFirecrackerPathFor(t.TempDir() + "/supervisor")
		if err == nil {
			t.Skip("firecracker resolved from this executable's own tree")
		}
		if !remediates(err.Error()) {
			t.Errorf("error states the problem but not the fix: %v", err)
		}
	})
}

// remediates reports whether a message tells the reader what to change, rather
// than only what went wrong.
func remediates(msg string) bool {
	for _, verb := range []string{"point it at", "install it", "set MICROAGENT_FIRECRACKER", "unset it"} {
		if strings.Contains(msg, verb) {
			return true
		}
	}
	return false
}
