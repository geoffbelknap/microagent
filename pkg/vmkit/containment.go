package vmkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ContainmentMarkerDirName is an atomic, durable deny marker. Its directory
	// is created before a containment phase begins; its presence alone is
	// authoritative even if a crash leaves the result document incomplete.
	ContainmentMarkerDirName = "containment"
	ContainmentResultName    = "result.json"
)

func ContainmentMarkerDir(stateDir, runtimeID string) string {
	return filepath.Join(stateDir, runtimeID, ContainmentMarkerDirName)
}

func ContainmentResultPath(stateDir, runtimeID string) string {
	return filepath.Join(ContainmentMarkerDir(stateDir, runtimeID), ContainmentResultName)
}

func ContainmentMarked(stateDir, runtimeID string) bool {
	_, err := os.Stat(ContainmentMarkerDir(stateDir, runtimeID))
	return err == nil || !os.IsNotExist(err)
}

func ValidateContainmentResult(result ContainmentResult) error {
	if result.Version != 1 {
		return fmt.Errorf("containment version %d is unsupported", result.Version)
	}
	if strings.TrimSpace(result.Backend) == "" {
		return fmt.Errorf("containment backend is required")
	}
	if result.AcceptedAt.IsZero() || result.UpdatedAt.IsZero() {
		return fmt.Errorf("containment acceptedAt and updatedAt are required")
	}
	if result.State != "in_progress" && result.State != "contained" {
		return fmt.Errorf("containment state %q is invalid", result.State)
	}
	for name, phase := range map[string]ContainmentPhaseResult{
		"freeze": result.Freeze, "severance": result.Severance,
		"capture": result.Capture, "stop": result.Stop, "custody": result.Custody,
	} {
		switch phase.Status {
		case ContainmentPhasePending:
			if phase.ObservedAt != nil {
				return fmt.Errorf("containment phase %s is pending with an observation time", name)
			}
		case ContainmentPhaseCompleted, ContainmentPhaseSkipped, ContainmentPhaseFailed:
			if phase.ObservedAt == nil {
				return fmt.Errorf("containment phase %s status %s requires observedAt", name, phase.Status)
			}
		default:
			return fmt.Errorf("containment phase %s status %q is invalid", name, phase.Status)
		}
		if phase.Status == ContainmentPhaseFailed && strings.TrimSpace(phase.Error) == "" {
			return fmt.Errorf("containment phase %s failed without an error", name)
		}
	}
	if result.Severance.Status == ContainmentPhaseCompleted && result.Freeze.Status != ContainmentPhaseCompleted {
		return fmt.Errorf("containment severance completed before freeze")
	}
	if result.Capture.Status == ContainmentPhaseCompleted {
		if result.Freeze.Status != ContainmentPhaseCompleted || result.Severance.Status != ContainmentPhaseCompleted {
			return fmt.Errorf("containment capture completed before freeze and severance")
		}
		if strings.TrimSpace(result.CaptureTag) == "" {
			return fmt.Errorf("completed containment capture requires captureTag")
		}
	}
	if result.Capture.Status == ContainmentPhaseSkipped && strings.TrimSpace(result.CaptureTag) != "" {
		return fmt.Errorf("skipped containment capture cannot retain captureTag")
	}
	if !result.CaptureRequired && result.Capture.Status != ContainmentPhaseSkipped {
		return fmt.Errorf("containment capture is not required but phase status is %s", result.Capture.Status)
	}
	if result.Capture.Status == ContainmentPhaseFailed && result.State == "in_progress" && result.Stop.Status != ContainmentPhasePending {
		return fmt.Errorf("failed containment capture must preserve pending stop for retry")
	}
	if result.Stop.Status == ContainmentPhaseCompleted && result.Custody.Status != ContainmentPhaseCompleted {
		return fmt.Errorf("completed containment stop requires completed custody")
	}
	if result.Custody.Status == ContainmentPhaseCompleted && result.Stop.Status != ContainmentPhaseCompleted {
		return fmt.Errorf("completed containment custody requires completed stop")
	}
	if result.State == "contained" && (result.Stop.Status != ContainmentPhaseCompleted || result.Custody.Status != ContainmentPhaseCompleted) {
		return fmt.Errorf("contained state requires completed stop and custody")
	}
	return nil
}
