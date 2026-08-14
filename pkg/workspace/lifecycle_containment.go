package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const (
	containmentControlTimeout = 15 * time.Second
	containmentCaptureTimeout = 2 * time.Minute
	containmentStopTimeout    = time.Minute
)

var dispatchContainmentCommand = Dispatch
var captureForContainment = SnapshotForensic

var errContainmentBackendMismatch = errors.New("containment backend mismatch")

// ReadContainment reads the durable containment result. The marker directory
// remains authoritative when this document is missing or malformed; callers
// must never use a read failure as permission to execute.
func ReadContainment(stateDir, name string) (vmkit.ContainmentResult, error) {
	var result vmkit.ContainmentResult
	data, err := os.ReadFile(vmkit.ContainmentResultPath(stateDir, name))
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode containment result: %w", err)
	}
	if err := vmkit.ValidateContainmentResult(result); err != nil {
		return result, err
	}
	return result, nil
}

func beginContainment(opts Options, captureTag string, skipCapture bool) (vmkit.ContainmentResult, error) {
	now := time.Now().UTC()
	result := vmkit.ContainmentResult{
		Version:         1,
		Backend:         opts.Backend,
		State:           "in_progress",
		AcceptedAt:      now,
		UpdatedAt:       now,
		CaptureRequired: !skipCapture,
		CaptureTag:      captureTag,
		Freeze:          vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhasePending},
		Severance:       vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhasePending},
		Capture:         vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhasePending},
		Stop:            vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhasePending},
		Custody:         vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhasePending},
	}
	if skipCapture {
		result.Capture = vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhaseSkipped, ObservedAt: &now}
	}
	markerDir := vmkit.ContainmentMarkerDir(opts.StateDir, opts.Name)
	if err := os.Mkdir(markerDir, 0o700); err != nil {
		if os.IsExist(err) {
			existing, readErr := ReadContainment(opts.StateDir, opts.Name)
			if readErr == nil {
				if existing.Backend != opts.Backend {
					return result, fmt.Errorf("%w: marker records %s but workspace requested %s", errContainmentBackendMismatch, existing.Backend, opts.Backend)
				}
				return existing, nil
			}
			// The marker alone is authoritative. Reconstruct a conservative
			// in-progress result and continue containment after a crash that
			// interrupted the first structured write.
			return result, fmt.Errorf("read existing containment result: %w", readErr)
		}
		return result, fmt.Errorf("mark workspace %s containment in progress: %w", opts.Name, err)
	}
	if err := syncContainmentPath(workspaceDirForContainment(opts)); err != nil {
		// Marker presence is already authoritative. Return the durability failure
		// without removing it so the caller continues fail-closed.
		return result, fmt.Errorf("sync initial containment marker: %w", err)
	}
	if err := writeContainment(opts, &result); err != nil {
		// The directory itself is the deny marker. Never remove it when the
		// structured write fails: a crash must fail closed.
		return result, fmt.Errorf("write initial containment result: %w", err)
	}
	return result, nil
}

func writeContainment(opts Options, result *vmkit.ContainmentResult) error {
	result.UpdatedAt = time.Now().UTC()
	if err := vmkit.ValidateContainmentResult(*result); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	path := vmkit.ContainmentResultPath(opts.StateDir, opts.Name)
	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := syncContainmentPath(path); err != nil {
		return err
	}
	return syncContainmentPath(filepath.Dir(path))
}

func workspaceDirForContainment(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name)
}

func syncContainmentPath(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}

func completedContainmentPhase() vmkit.ContainmentPhaseResult {
	now := time.Now().UTC()
	return vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhaseCompleted, ObservedAt: &now}
}

func failedContainmentPhase(err error) vmkit.ContainmentPhaseResult {
	now := time.Now().UTC()
	return vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhaseFailed, ObservedAt: &now, Error: err.Error()}
}

func skippedContainmentPhase(reason string) vmkit.ContainmentPhaseResult {
	now := time.Now().UTC()
	return vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhaseSkipped, ObservedAt: &now, Error: reason}
}

func containWorkspace(ctx context.Context, opts Options, qopts QuarantineOptions) (QuarantineResult, error) {
	result := QuarantineResult{}
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return result, err
	}
	if err := ValidateName(opts.Name); err != nil {
		return result, err
	}
	workspaceDir := filepath.Join(opts.StateDir, opts.Name)
	if info, err := os.Stat(workspaceDir); err != nil {
		return result, err
	} else if !info.IsDir() {
		return result, fmt.Errorf("workspace path %s is not a directory", workspaceDir)
	}
	release, acquired, err := fsutil.TryLock(filepath.Join(workspaceDir, ".containment.lock"))
	if err != nil {
		return result, fmt.Errorf("lock containment for workspace %s: %w", opts.Name, err)
	}
	if !acquired {
		return result, operation.New(operation.ErrorConflict, "workspace %s containment is already in progress", opts.Name)
	}
	defer func() { _ = release() }()

	sessionID, purpose, correlationID, observedFrom := containmentIdentity(opts)
	tag := ""
	if !qopts.SkipCapture {
		tag = strings.TrimSpace(qopts.CaptureTag)
		if tag == "" {
			tag = DefaultForensicSnapshotTag(time.Now())
		}
		if err := validateTag(tag); err != nil {
			return result, err
		}
	}
	containment, beginErr := beginContainment(opts, tag, qopts.SkipCapture)
	result.Containment = containment
	if beginErr != nil && !containmentMarkerExists(opts.StateDir, opts.Name) {
		return result, beginErr
	}
	if errors.Is(beginErr, errContainmentBackendMismatch) {
		// A marker from another backend is still authoritative, but selecting a
		// supervisor from the new request could act on the wrong host runtime.
		// Keep the marker and return without any guest or supervisor interaction.
		return result, beginErr
	}
	if qopts.SkipCapture && containment.Capture.Status != vmkit.ContainmentPhaseCompleted && (containment.CaptureRequired || containment.Capture.Status != vmkit.ContainmentPhaseSkipped) {
		// An operator may explicitly accept evidence loss after an earlier capture
		// failure. This is the only path from failed capture to final custody; the
		// VM stays frozen and severed throughout the retry.
		containment.CaptureRequired = false
		containment.Capture = skippedContainmentPhase("operator explicitly accepted volatile evidence loss")
		containment.CaptureTag = ""
		if writeErr := writeContainment(opts, &containment); writeErr != nil {
			beginErr = errors.Join(beginErr, fmt.Errorf("persist capture skip: %w", writeErr))
		}
	}
	if containment.CaptureRequired && containment.Capture.Status == vmkit.ContainmentPhaseSkipped && containment.State == "in_progress" {
		// A freeze or severance failure may skip capture without waiving it. Re-arm
		// the phase on retry so a later successful freeze/sever still captures.
		containment.Capture = vmkit.ContainmentPhaseResult{Status: vmkit.ContainmentPhasePending}
		if containment.CaptureTag == "" {
			containment.CaptureTag = tag
		}
		if writeErr := writeContainment(opts, &containment); writeErr != nil {
			beginErr = errors.Join(beginErr, fmt.Errorf("persist capture retry: %w", writeErr))
		}
	}
	if !containment.CaptureRequired {
		qopts.SkipCapture = true
		tag = ""
	} else if containment.CaptureTag != "" {
		tag = containment.CaptureTag
	}
	// A completed marker makes retries idempotent without ever reopening the
	// workspace. The response remains structured even when the original caller
	// disappeared after custody completed.
	if containment.State == "contained" {
		resp, inspectErr := Status(opts)
		resp.Containment = &containment
		result.Response = resp
		result.Captured = containment.Capture.Status == vmkit.ContainmentPhaseCompleted
		result.CaptureTag = containment.CaptureTag
		result.CaptureError = containment.Capture.Error
		result.Incident = buildIncidentReceipt(opts.StateDir, opts.Name, sessionID, purpose, correlationID, observedFrom, time.Now())
		return result, inspectErr
	}
	// A crash can land after the backend stopped and wrote quarantined state but
	// before the library persisted stop/custody. Reconcile that one-way boundary
	// without trying to freeze a VM that no longer exists or clearing the marker.
	if state, stateErr := ReadRuntimeState(opts); stateErr == nil && state.Event.State == vmkit.StateQuarantined {
		var recoveryErr error
		if containment.Freeze.Status == vmkit.ContainmentPhasePending {
			recoveryErr = fmt.Errorf("runtime reached custody before freeze was durably recorded")
			containment.Freeze = failedContainmentPhase(recoveryErr)
		}
		if containment.Severance.Status == vmkit.ContainmentPhasePending {
			containment.Severance = skippedContainmentPhase("runtime reached custody before severance was durably recorded")
		}
		if containment.Capture.Status == vmkit.ContainmentPhasePending {
			if forensicCapturePresent(opts, containment.CaptureTag) {
				containment.Capture = completedContainmentPhase()
			} else {
				containment.Capture = skippedContainmentPhase("runtime reached custody before forensic capture was durably recorded")
				containment.CaptureTag = ""
				result.CaptureError = containment.Capture.Error
				if recoveryErr == nil {
					recoveryErr = errors.New(containment.Capture.Error)
				}
			}
		}
		containment.Stop = completedContainmentPhase()
		containment.Custody = completedContainmentPhase()
		containment.State = "contained"
		if err := writeContainment(opts, &containment); err != nil {
			recoveryErr = errors.Join(recoveryErr, err)
		}
		result.Containment = containment
		resp, inspectErr := Status(opts)
		resp.Containment = &result.Containment
		result.Response = resp
		result.Captured = containment.Capture.Status == vmkit.ContainmentPhaseCompleted
		result.CaptureTag = containment.CaptureTag
		result.Incident = buildIncidentReceipt(opts.StateDir, opts.Name, sessionID, purpose, correlationID, observedFrom, time.Now())
		return result, errors.Join(recoveryErr, inspectErr)
	}

	// Once the marker exists, caller cancellation must not restore execution or
	// strand a live authority path. Each phase gets a fresh bound so a timed-out
	// freeze or severance cannot consume the emergency stop's deadline.
	containBase := context.WithoutCancel(ctx)
	audit := containmentLifecycleAudit(opts)
	var operationErrors []error
	if beginErr != nil {
		operationErrors = append(operationErrors, beginErr)
	}
	var lastResponse vmkit.Response

	var freezeErr error
	if containment.Freeze.Status != vmkit.ContainmentPhaseCompleted {
		var freezeResp vmkit.Response
		freezeCtx, freezeCancel := context.WithTimeout(containBase, containmentControlTimeout)
		freezeResp, freezeErr = dispatchContainmentPhase(freezeCtx, opts, "contain-freeze", &audit)
		freezeCancel()
		lastResponse = freezeResp
		if freezeErr != nil {
			containment.Freeze = failedContainmentPhase(freezeErr)
			containment.Severance = skippedContainmentPhase("freeze was not confirmed; emergency stop provides final revocation")
			containment.Capture = skippedContainmentPhase("freeze was not confirmed")
			containment.CaptureTag = ""
			if !qopts.SkipCapture {
				result.CaptureError = "freeze was not confirmed; forensic capture was not attempted"
			}
			operationErrors = append(operationErrors, fmt.Errorf("freeze: %w", freezeErr))
		} else {
			containment.Freeze = completedContainmentPhase()
		}
		if updateErr := writeContainment(opts, &containment); updateErr != nil {
			operationErrors = append(operationErrors, fmt.Errorf("persist freeze phase: %w", updateErr))
		}
	}

	var severErr error
	if freezeErr == nil && containment.Severance.Status != vmkit.ContainmentPhaseCompleted {
		var severResp vmkit.Response
		severCtx, severCancel := context.WithTimeout(containBase, containmentControlTimeout)
		severResp, severErr = dispatchContainmentPhase(severCtx, opts, "contain-sever", &audit)
		severCancel()
		lastResponse = severResp
		if severErr != nil {
			containment.Severance = failedContainmentPhase(severErr)
			operationErrors = append(operationErrors, fmt.Errorf("sever authority: %w", severErr))
		} else {
			containment.Severance = completedContainmentPhase()
		}
		if updateErr := writeContainment(opts, &containment); updateErr != nil {
			operationErrors = append(operationErrors, fmt.Errorf("persist severance phase: %w", updateErr))
		}
	}

	if containment.Capture.Status == vmkit.ContainmentPhaseCompleted {
		result.Captured = true
		result.CaptureTag = containment.CaptureTag
		opts.LifecycleEvidenceRef = "snapshot:" + containment.CaptureTag
		audit.WorkInFlight.EvidenceRef = opts.LifecycleEvidenceRef
	} else if severErr != nil && !qopts.SkipCapture {
		containment.Capture = skippedContainmentPhase("authority severance was not confirmed")
		containment.CaptureTag = ""
		result.CaptureError = "authority severance was not confirmed; forensic capture was not attempted"
	} else if freezeErr == nil && !qopts.SkipCapture {
		captureOpts := opts
		captureOpts.containmentOperation = true
		captureCtx, captureCancel := context.WithTimeout(containBase, containmentCaptureTimeout)
		_, captureErr := captureForContainment(captureCtx, captureOpts, tag)
		captureCancel()
		if captureErr != nil {
			containment.Capture = failedContainmentPhase(captureErr)
			result.CaptureError = captureErr.Error()
			operationErrors = append(operationErrors, fmt.Errorf("capture frozen evidence: %w", captureErr))
		} else {
			containment.Capture = completedContainmentPhase()
			result.Captured = true
			result.CaptureTag = tag
			opts.LifecycleEvidenceRef = "snapshot:" + tag
			audit.WorkInFlight.EvidenceRef = opts.LifecycleEvidenceRef
		}
		if updateErr := writeContainment(opts, &containment); updateErr != nil {
			operationErrors = append(operationErrors, fmt.Errorf("persist capture phase: %w", updateErr))
		}
	}
	if containment.Capture.Status == vmkit.ContainmentPhaseFailed && freezeErr == nil && severErr == nil {
		// Preserve the only remaining copy of volatile evidence. The marker and
		// dispatch fence keep this paused VM severed across caller/process restart;
		// a retry resumes at capture and reaches final custody only after success.
		result.Containment = containment
		if lastResponse.Backend == "" {
			lastResponse, _ = Status(opts)
		}
		lastResponse.Containment = &result.Containment
		result.Response = lastResponse
		result.Incident = buildIncidentReceipt(opts.StateDir, opts.Name, sessionID, purpose, correlationID, observedFrom, time.Now())
		return result, errors.Join(operationErrors...)
	}

	var stopErr error
	if containment.Stop.Status != vmkit.ContainmentPhaseCompleted {
		var stopResp vmkit.Response
		stopCommand := "contain-stop"
		if freezeErr != nil {
			// Without a confirmed freeze, use the legacy fail-safe supervisor path:
			// it severs live authority before stopping. The library still records
			// freeze/severance as failed or skipped rather than inventing success.
			stopCommand = "quarantine"
		}
		stopCtx, stopCancel := context.WithTimeout(containBase, containmentStopTimeout)
		stopResp, stopErr = dispatchContainmentPhase(stopCtx, opts, stopCommand, &audit)
		stopCancel()
		lastResponse = stopResp
		if stopErr != nil {
			containment.Stop = failedContainmentPhase(stopErr)
			operationErrors = append(operationErrors, fmt.Errorf("stop into custody: %w", stopErr))
		} else {
			containment.Stop = completedContainmentPhase()
		}
	}
	if stopErr == nil && containment.Stop.Status == vmkit.ContainmentPhaseCompleted {
		containment.Custody = completedContainmentPhase()
		containment.State = "contained"
	}
	if updateErr := writeContainment(opts, &containment); updateErr != nil {
		operationErrors = append(operationErrors, fmt.Errorf("persist custody phase: %w", updateErr))
	}

	result.Containment = containment
	if lastResponse.Backend == "" {
		lastResponse, _ = Status(opts)
	}
	lastResponse.Containment = &result.Containment
	result.Response = lastResponse
	result.Incident = buildIncidentReceipt(opts.StateDir, opts.Name, sessionID, purpose, correlationID, observedFrom, time.Now())
	return result, errors.Join(operationErrors...)
}

func forensicCapturePresent(opts Options, tag string) bool {
	if strings.TrimSpace(tag) == "" {
		return false
	}
	manifest, err := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(opts.StateDir, opts.Name, tag))
	return err == nil && manifest.Forensic && manifest.FrozenProcessState
}

func containmentIdentity(opts Options) (sessionID, purpose, correlationID, observedFrom string) {
	purpose = opts.Purpose
	correlationID = opts.CorrelationID
	if state, err := ReadRuntimeState(opts); err == nil {
		sessionID = state.Event.Identity.SessionID
		if purpose == "" {
			purpose = state.Event.Identity.Purpose
		}
		if correlationID == "" {
			correlationID = state.Event.Identity.CorrelationID
		}
		observedFrom = state.StartedAt
		if observedFrom == "" {
			observedFrom = state.Event.ObservedAt
		}
	}
	return
}

func containmentLifecycleAudit(opts Options) vmkit.LifecycleAudit {
	caller := opts.Caller
	if caller.Channel == "" {
		caller = vmkit.CallerAttribution{Channel: "library", Assurance: "unavailable"}
	}
	return vmkit.LifecycleAudit{
		Initiator: caller,
		Reason:    opts.Purpose,
		Notification: vmkit.NotificationRecord{
			Status: "not_performed",
			Owner:  "caller",
			Reason: "microagent has no principal directory or notification channel",
		},
		WorkInFlight: vmkit.WorkInFlight{
			Declared:      declaredWork(opts.StateDir, opts.Name),
			CaptureStatus: "frozen_forensic_capture",
		},
	}
}

func dispatchContainmentPhase(ctx context.Context, opts Options, command string, audit *vmkit.LifecycleAudit) (vmkit.Response, error) {
	opts.containmentOperation = true
	identity := vmkit.Identity{
		RequestID:     NewRequestID(),
		RuntimeID:     opts.Name,
		Purpose:       opts.Purpose,
		CorrelationID: opts.CorrelationID,
		Role:          vmkit.RoleWorkload,
		Backend:       opts.Backend,
	}
	if state, err := ReadRuntimeState(opts); err == nil {
		identity.SessionID = state.Event.Identity.SessionID
		if identity.Purpose == "" {
			identity.Purpose = state.Event.Identity.Purpose
		}
		if identity.CorrelationID == "" {
			identity.CorrelationID = state.Event.Identity.CorrelationID
		}
	}
	return dispatchContainmentCommand(ctx, opts, vmkit.Request{
		Command:   command,
		Identity:  &identity,
		Lifecycle: audit,
		Config:    &vmkit.Config{StateDir: opts.StateDir},
	})
}

func containmentBlocked(stateDir, name string) error {
	if !vmkit.ContainmentMarked(stateDir, name) {
		return nil
	}
	return operation.New(operation.ErrorConflict, "workspace %s has a durable containment marker; execution and resume are denied", name)
}

// containmentMarkerExists is kept local to the workspace package for tests
// that need to assert fail-closed behavior without depending on marker layout.
func containmentMarkerExists(stateDir, name string) bool {
	return vmkit.ContainmentMarked(filepath.Clean(stateDir), name)
}
