package firecracker

import (
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// The supervisor's self-enforced run bound must come from RunBoundSeconds —
// set only for one-shot shapes — never be invented from TimeoutSeconds alone
// when the new field is present. TimeoutSeconds is the host dispatch timeout
// and rides on every request, persistent starts included; treating it as a
// run bound is what would kill persistent workspaces at the CLI default.
func TestNormalizedOptionsPrefersRunBound(t *testing.T) {
	req := vmkit.Request{
		Command: "run",
		Config: &vmkit.Config{
			StateDir:        t.TempDir(),
			RunBoundSeconds: 7,
			TimeoutSeconds:  120,
		},
	}
	opts := Supervisor{}.normalizedOptions(req)
	if opts.Timeout != 7*time.Second {
		t.Fatalf("Timeout = %v, want 7s from RunBoundSeconds", opts.Timeout)
	}

	// Legacy requests (no RunBoundSeconds) keep their historical bound.
	req.Config.RunBoundSeconds = 0
	opts = Supervisor{}.normalizedOptions(req)
	if opts.Timeout != 120*time.Second {
		t.Fatalf("legacy Timeout = %v, want 120s fallback from TimeoutSeconds", opts.Timeout)
	}
}
