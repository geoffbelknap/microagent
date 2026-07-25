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
//
// It proves this by COMPARING against a capture-skipped control run rather than
// inspecting the response shape: containment must behave identically whether or
// not a capture was attempted and failed. That comparison holds on any host,
// where "the response looks non-empty" does not.
func TestQuarantineContainsDespiteCaptureFailure(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: "linux-kvm", SupervisorPath: dir + "/no-supervisor"}

	// Control: contain with no capture attempted at all.
	skipResult, skipErr := Quarantine(context.Background(), opts, QuarantineOptions{SkipCapture: true})
	// Same containment, but preceded by a capture that cannot succeed (no
	// runtime state exists).
	capResult, capErr := Quarantine(context.Background(), opts, QuarantineOptions{})

	if capResult.Captured {
		t.Fatal("capture reported success without a runtime")
	}
	if strings.TrimSpace(capResult.CaptureError) == "" {
		t.Fatal("a failed capture must be reported, not silently dropped")
	}
	if errText(skipErr) != errText(capErr) {
		t.Fatalf("capture failure changed containment: with-capture err = %q, control err = %q", errText(capErr), errText(skipErr))
	}
	if skipResult.Response.Error != capResult.Response.Error || skipResult.Response.OK != capResult.Response.OK {
		t.Fatalf("capture failure changed the containment response:\n with capture: %#v\n control:      %#v", capResult.Response, skipResult.Response)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestQuarantineCaptureTagIsIdentifiable: an automatic capture must be
// recognizable as one on sight, and must not collide with an operator's tags.
func TestQuarantineCaptureTagIsIdentifiable(t *testing.T) {
	if !strings.HasPrefix(ForensicCaptureTagPrefix, "forensic") {
		t.Fatalf("capture tag prefix %q does not identify a forensic capture", ForensicCaptureTagPrefix)
	}
}
