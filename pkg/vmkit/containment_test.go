package vmkit

import (
	"strings"
	"testing"
	"time"
)

func TestValidateContainmentResultEnforcesPhaseOrdering(t *testing.T) {
	now := time.Now().UTC()
	completed := ContainmentPhaseResult{Status: ContainmentPhaseCompleted, ObservedAt: &now}
	pending := ContainmentPhaseResult{Status: ContainmentPhasePending}
	valid := ContainmentResult{
		Version: 1, Backend: BackendLinuxKVM, State: "in_progress", CaptureRequired: true,
		AcceptedAt: now, UpdatedAt: now, CaptureTag: "forensic-test",
		Freeze: completed, Severance: completed,
		Capture: ContainmentPhaseResult{Status: ContainmentPhaseFailed, ObservedAt: &now, Error: "device failed"},
		Stop:    pending, Custody: pending,
	}
	if err := ValidateContainmentResult(valid); err != nil {
		t.Fatalf("valid partial containment: %v", err)
	}

	invalid := valid
	invalid.Stop = completed
	if err := ValidateContainmentResult(invalid); err == nil || !strings.Contains(err.Error(), "preserve pending stop") {
		t.Fatalf("failed capture with completed stop err = %v", err)
	}

	containedAfterExternalStop := valid
	containedAfterExternalStop.State = "contained"
	containedAfterExternalStop.Stop = completed
	containedAfterExternalStop.Custody = completed
	if err := ValidateContainmentResult(containedAfterExternalStop); err != nil {
		t.Fatalf("failed capture reconciled after external stop: %v", err)
	}

	invalid = valid
	invalid.Capture = completed
	invalid.Freeze = pending
	if err := ValidateContainmentResult(invalid); err == nil || !strings.Contains(err.Error(), "before freeze") {
		t.Fatalf("out-of-order capture err = %v", err)
	}

	invalid = valid
	invalid.Capture = ContainmentPhaseResult{Status: ContainmentPhaseSkipped, ObservedAt: &now}
	if err := ValidateContainmentResult(invalid); err == nil || !strings.Contains(err.Error(), "cannot retain captureTag") {
		t.Fatalf("skipped capture with tag err = %v", err)
	}
}
