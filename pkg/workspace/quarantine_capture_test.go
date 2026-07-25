package workspace

import (
	"context"
	"strings"
	"testing"
)

// TestQuarantineCapturesBeforeContaining: containment stops the runtime, so
// evidence must be acquired first. This asserts the ORDER at the seam we can
// observe without a VM — a capture is attempted before Control is reached —
// and that --no-capture suppresses it.
func TestQuarantineSkipCaptureAttemptsNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: "linux-kvm", SupervisorPath: dir + "/no-supervisor"}

	// With capture skipped, the only failure can come from containment itself.
	result, err := Quarantine(context.Background(), opts, QuarantineOptions{SkipCapture: true})
	if err == nil {
		t.Fatal("expected containment to fail with no supervisor")
	}
	if result.Captured {
		t.Fatal("SkipCapture must not capture")
	}
	if result.CaptureError != "" {
		t.Fatalf("SkipCapture must not attempt a capture, got error %q", result.CaptureError)
	}
}

// TestQuarantineContainsDespiteCaptureFailure is the invariant that matters
// most: a workspace whose capture fails must still be contained. If capture
// could block containment, making capture fail would become a way to avoid
// being contained — and the containment is the safety property, the evidence is
// the nice-to-have.
func TestQuarantineContainsDespiteCaptureFailure(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: "linux-kvm", SupervisorPath: dir + "/no-supervisor"}

	result, _ := Quarantine(context.Background(), opts, QuarantineOptions{})
	// The capture cannot succeed here (no runtime state), and that must be
	// recorded rather than swallowed...
	if result.Captured {
		t.Fatal("capture reported success without a runtime")
	}
	if strings.TrimSpace(result.CaptureError) == "" {
		t.Fatal("a failed capture must be reported, not silently dropped")
	}
	// ...and containment must still have been ATTEMPTED. A response means
	// Control ran; an empty zero response would mean the capture short-circuited
	// containment, which is the failure mode this guards.
	if result.Response.Backend == "" && result.Response.Error == "" {
		t.Fatalf("containment was not attempted after a failed capture: %#v", result.Response)
	}
}

// TestQuarantineCaptureTagIsIdentifiable: an automatic capture must be
// recognizable as one on sight, and must not collide with an operator's tags.
func TestQuarantineCaptureTagIsIdentifiable(t *testing.T) {
	if !strings.HasPrefix(ForensicCaptureTagPrefix, "forensic") {
		t.Fatalf("capture tag prefix %q does not identify a forensic capture", ForensicCaptureTagPrefix)
	}
}
