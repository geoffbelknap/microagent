package diagnostics

import (
	"strings"
	"testing"

	firecracker "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
)

// TestBinaryNotFoundCarriesRemediation pins the rule that a failure blocking
// every workflow tells the operator what to do about it. This one used to read
// "firecracker binary not found" and stop there, while the neighbouring
// guest-init and MICROAGENT_FIRECRACKER failures both already named a next step.
func TestBinaryNotFoundCarriesRemediation(t *testing.T) {
	msg := firecracker.BinaryNotFoundError

	// The prefix is load-bearing: downstream consumers surface this string and
	// tests match on it, so remediation is appended rather than replacing it.
	if !strings.HasPrefix(msg, "firecracker binary not found") {
		t.Errorf("message = %q, want it to keep the \"firecracker binary not found\" prefix", msg)
	}
	for _, want := range []string{"brew install", "make install", "MICROAGENT_FIRECRACKER"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message = %q, want it to mention %q", msg, want)
		}
	}
}

// TestResolveFirecrackerPathReportsRemediation checks the probe's real failure
// path, not just the constant: with no MICROAGENT_FIRECRACKER and nothing on
// PATH, the resolver must return the remediating message rather than a bare
// "not found".
//
// The executable-relative fallback cannot resolve from a test binary's
// directory, so this fails deterministically.
func TestResolveFirecrackerPathReportsRemediation(t *testing.T) {
	t.Setenv("MICROAGENT_FIRECRACKER", "")
	t.Setenv("PATH", t.TempDir())

	path, err := ResolveFirecrackerPath()
	if err == nil {
		t.Skipf("firecracker unexpectedly resolved to %q in a stripped environment", path)
	}
	if err.Error() != firecracker.BinaryNotFoundError {
		t.Errorf("error = %q, want the shared remediating message %q", err, firecracker.BinaryNotFoundError)
	}
}
