package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// ForensicCaptureTagPrefix names automatic quarantine captures so they are
// identifiable on sight and never collide with an operator's own tags.
const ForensicCaptureTagPrefix = "forensic-"

const snapshotTagPrefix = "snap-"

// CleanStopSyncTimeout bounds the guest filesystem flush attempted before a
// clean halt or stop. A wedged or compromised guest cannot delay containment
// indefinitely; timeout or failure is recorded and the stop still proceeds.
const CleanStopSyncTimeout = 2 * time.Second

const LifecycleInspectTimeout = time.Second

const lifecycleInspectOutputLimit = 64 * 1024

var executeCleanStopSync = Exec
var executeLifecycleInspect = Exec
var requestGracefulGuestShutdown = RequestShutdown

// DefaultSnapshotTag returns the stable timestamp-based tag used when an
// ordinary snapshot caller does not provide one.
func DefaultSnapshotTag(now time.Time) string {
	return snapshotTagPrefix + now.UTC().Format("20060102-150405")
}

// DefaultForensicSnapshotTag returns the visibly distinct timestamp-based tag
// used when a forensic snapshot caller does not provide one.
func DefaultForensicSnapshotTag(now time.Time) string {
	return ForensicCaptureTagPrefix + now.UTC().Format("20060102-150405")
}

// QuarantineOptions tunes the quarantine verb.
type QuarantineOptions struct {
	// SkipCapture freezes and severs authority but omits the forensic snapshot
	// before final custody. The runtime is still stopped, so skipping means
	// accepting the loss of volatile evidence.
	SkipCapture bool
	// CaptureTag overrides the generated capture tag.
	CaptureTag string
}

// QuarantineResult reports what containment did, including whether evidence was
// captured. CaptureError is set when a capture was attempted and failed; the
// typed phases show that the workspace remains frozen and severed with final
// custody pending.
type QuarantineResult struct {
	Response     vmkit.Response          `json:"response"`
	CaptureTag   string                  `json:"captureTag,omitempty"`
	CaptureError string                  `json:"captureError,omitempty"`
	Captured     bool                    `json:"captured"`
	Containment  vmkit.ContainmentResult `json:"containment"`
	Incident     IncidentReceipt         `json:"incident"`
}

// Quarantine atomically marks the workspace, freezes execution, severs every
// host capability, captures memory/disk/process evidence while frozen, and only
// then stops the VM into durable custody. A capture failure leaves the VM
// frozen and severed for a safe retry; it never restores execution or
// authority. The capture retains guest secrets, so it is secret-bearing and
// not restorable.
func Quarantine(ctx context.Context, opts Options, qopts QuarantineOptions) (QuarantineResult, error) {
	return containWorkspace(ctx, opts, qopts)
}

// Control dispatches a raw lifecycle command. Quarantine is routed through the
// same freeze/sever/capture/stop primitive as every other adapter. Its response
// carries the typed containment phases and capture tag; callers that need the
// incident receipt should call Quarantine directly.
func Control(ctx context.Context, opts Options, command string) (vmkit.Response, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.Response{}, err
	}
	if err := ValidateName(opts.Name); err != nil {
		return vmkit.Response{}, err
	}
	if command == "quarantine" {
		result, err := containWorkspace(ctx, opts, QuarantineOptions{})
		return result.Response, err
	}
	if command == "resume" {
		if err := containmentBlocked(opts.StateDir, opts.Name); err != nil {
			return vmkit.Response{OK: false, Backend: opts.Backend, Error: err.Error()}, err
		}
	}
	if command == "delete" && vmkit.ContainmentMarked(opts.StateDir, opts.Name) {
		err := operation.New(operation.ErrorConflict, "workspace %s is in durable containment custody; workspace delete is denied", opts.Name)
		return vmkit.Response{OK: false, Backend: opts.Backend, Error: err.Error()}, err
	}
	switch command {
	case "halt", "pause", "resume", "stop", "kill", "delete", "gc":
	default:
		return vmkit.Response{}, operation.New(operation.ErrorUnsupported, "unsupported workspace control command: %s", command)
	}
	if resp, err := unsupportedControlCapability(opts.Backend, command); err != nil {
		return resp, err
	}
	lifecycle := lifecycleAudit(ctx, opts, command)
	if command == "delete" {
		if resp, err := ensureDeletable(ctx, opts); err != nil {
			return resp, err
		}
	}
	if command == "halt" || command == "stop" {
		prepareCleanStop(ctx, opts)
		state, _, stateErr := LatestStartState(opts.StateDir, opts.Name)
		if stateErr == nil && state == vmkit.StateRunning {
			if err := requestGracefulGuestShutdown(ctx, opts); err != nil {
				wrapped := fmt.Errorf("request graceful shutdown from workspace %s: %w", opts.Name, err)
				return vmkit.Response{Backend: opts.Backend, Error: wrapped.Error()}, wrapped
			}
		}
	}
	req := vmkit.Request{
		Command:   command,
		Lifecycle: &lifecycle,
		Identity: &vmkit.Identity{
			RequestID:     NewRequestID(),
			RuntimeID:     opts.Name,
			Purpose:       opts.Purpose,
			CorrelationID: opts.CorrelationID,
			Role:          vmkit.RoleWorkload,
			Backend:       opts.Backend,
		},
		Config: &vmkit.Config{StateDir: opts.StateDir},
	}
	if state, stateErr := ReadRuntimeState(opts); stateErr == nil {
		req.Identity.SessionID = state.Event.Identity.SessionID
		if req.Identity.Purpose == "" {
			req.Identity.Purpose = state.Event.Identity.Purpose
		}
		if req.Identity.CorrelationID == "" {
			req.Identity.CorrelationID = state.Event.Identity.CorrelationID
		}
		if command == "resume" {
			req.Identity.SourceSessionID = state.Event.Identity.SessionID
			req.Identity.SessionID = NewSessionID()
		}
	}
	resp, err := Dispatch(ctx, opts, req)
	if command == "delete" && resp.OK {
		Cleanup(opts.StateDir, opts.Name)
	}
	return resp, err
}

func lifecycleAudit(ctx context.Context, opts Options, command string) vmkit.LifecycleAudit {
	caller := opts.Caller
	if caller.Channel == "" {
		caller = vmkit.CallerAttribution{Channel: "library", Assurance: "unavailable"}
	}
	audit := vmkit.LifecycleAudit{
		Initiator: caller,
		Reason:    opts.Purpose,
		Notification: vmkit.NotificationRecord{
			Status: "not_performed",
			Owner:  "caller",
			Reason: "microagent has no principal directory or notification channel",
		},
	}
	audit.WorkInFlight.Declared = declaredWork(opts.StateDir, opts.Name)
	audit.WorkInFlight.EvidenceRef = opts.LifecycleEvidenceRef

	state, err := ReadRuntimeState(opts)
	if err != nil || state.Event.State != vmkit.StateRunning {
		audit.WorkInFlight.CaptureStatus = "not_running"
		return audit
	}
	if command == "kill" {
		audit.WorkInFlight.CaptureStatus = "skipped_hard_stop"
		return audit
	}
	if command != "halt" && command != "stop" && command != "quarantine" && command != "delete" {
		audit.WorkInFlight.CaptureStatus = "not_applicable"
		return audit
	}

	inspectCtx, cancel := context.WithTimeout(ctx, LifecycleInspectTimeout)
	defer cancel()
	req := execprotocol.NewExecRequest([]string{"ps", "-o", "pid,ppid,comm"})
	req.TimeoutMS = LifecycleInspectTimeout.Milliseconds()
	req.OutputLimitBytesStdout = lifecycleInspectOutputLimit
	req.OutputLimitBytesStderr = lifecycleInspectOutputLimit
	result, inspectErr := executeLifecycleInspect(inspectCtx, opts, req)
	audit.WorkInFlight.CapturedAt = time.Now().UTC()
	if inspectErr != nil {
		audit.WorkInFlight.CaptureStatus = "unavailable"
		audit.WorkInFlight.CaptureError = truncateLifecycleText(inspectErr.Error())
		return audit
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		audit.WorkInFlight.CaptureStatus = "failed"
		audit.WorkInFlight.CaptureError = fmt.Sprintf("process snapshot status=%s", result.Status)
		return audit
	}
	audit.WorkInFlight.GuestReported = parseGuestProcesses(string(result.Stdout))
	audit.WorkInFlight.CaptureStatus = "captured"
	return audit
}

func declaredWork(stateDir, name string) []vmkit.DeclaredWork {
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return nil
	}
	var declared []vmkit.DeclaredWork
	add := func(kind, command string) {
		if len(declared) == vmkit.MaxLifecycleProcesses {
			return
		}
		if command = strings.TrimSpace(command); command != "" {
			declared = append(declared, vmkit.DeclaredWork{Kind: kind, Command: truncateLifecycleText(command)})
		}
	}
	add("entrypoint", manifest.Entrypoint)
	add("service", manifest.Service)
	if !manifest.SetupComplete {
		for _, command := range manifest.SetupCommands {
			add("setup", command)
		}
	}
	add("exec", manifest.ExecCommand)
	if manifest.UseImageCommand {
		add("image", strings.Join(append(append([]string{}, manifest.ImageEntrypoint...), manifest.ImageCmd...), " "))
	}
	if manifest.ModelRunner != nil {
		add("model_runner", strings.Join(manifest.ModelRunner.Command, " "))
	}
	return declared
}

func parseGuestProcesses(output string) []vmkit.GuestProcess {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	processes := make([]vmkit.GuestProcess, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}
		processes = append(processes, vmkit.GuestProcess{PID: pid, PPID: ppid, Command: truncateLifecycleText(strings.Join(fields[2:], " "))})
		if len(processes) == vmkit.MaxLifecycleProcesses {
			break
		}
	}
	return processes
}

func truncateLifecycleText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= vmkit.MaxLifecycleTextBytes {
		return value
	}
	for len(value) > vmkit.MaxLifecycleTextBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func prepareCleanStop(ctx context.Context, opts Options) {
	state, err := ReadRuntimeState(opts)
	if err != nil || state.Event.State != vmkit.StateRunning {
		return
	}
	syncCtx, cancel := context.WithTimeout(ctx, CleanStopSyncTimeout)
	defer cancel()
	req := execprotocol.NewExecRequest([]string{"sync"})
	req.TimeoutMS = CleanStopSyncTimeout.Milliseconds()
	result, syncErr := executeCleanStopSync(syncCtx, opts, req)
	detail := "clean stop preparation: guest filesystem sync completed"
	if syncErr != nil {
		detail = "clean stop preparation: guest filesystem sync failed: " + syncErr.Error()
	} else if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		detail = fmt.Sprintf("clean stop preparation: guest filesystem sync failed: status=%s", result.Status)
		if result.ExitCode != nil {
			detail += fmt.Sprintf(" exit_code=%d", *result.ExitCode)
		}
	}
	_ = appendEvent(EventsPath(opts.StateDir, opts.Name), EventFile{
		Identity:   state.Event.Identity,
		State:      state.Event.State,
		Detail:     detail,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// DeleteOptions controls how a live workspace is stopped before deletion.
type DeleteOptions struct {
	Force bool
}

// DeleteResult reports what delete actually did. Response keeps the shared
// lifecycle shape; Deleted distinguishes "removed" from "was already absent",
// because both are success under the idempotent contract and a caller given a
// name that never existed (a typo, an unexpanded glob) must be able to tell.
type DeleteResult struct {
	vmkit.Response
	Deleted bool `json:"deleted"`
}

// Absent reports whether nothing of the workspace exists: no runtime or event
// records and no root directory. A partially created workspace (a disk written
// but no event yet) is present, not absent — it still has state to remove.
func Absent(opts Options) bool {
	if _, err := Status(opts); err != nil {
		var notFound WorkspaceNotFoundError
		if errors.As(err, &notFound) {
			rootDir := filepath.Dir(WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend))
			if _, statErr := os.Stat(rootDir); os.IsNotExist(statErr) {
				return true
			}
		}
	}
	return false
}

// Delete removes a workspace through the shared lifecycle contract. A running
// workspace is stopped first, or killed when Force is set. Confirmation remains
// an adapter concern; callers invoke Delete only after their own interaction or
// authorization policy has approved the operation.
//
// Delete is idempotent cleanup: an absent workspace still deletes to success
// (exit-0 for retried teardown), but the result says so honestly — Deleted is
// false and no event is synthesized, because nothing was observed. Partial
// workspaces (a disk written but no event yet) are present and delete through
// the dispatch path.
func Delete(ctx context.Context, opts Options, deleteOpts DeleteOptions) (DeleteResult, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return DeleteResult{}, err
	}
	if err := ValidateName(opts.Name); err != nil {
		return DeleteResult{}, err
	}
	if Absent(opts) {
		// Best-effort, mirroring the removed path below: a stale volume holder
		// under this name is released even though the workspace itself is gone.
		_ = volume.DetachAll(opts.StateDir, opts.Name)
		return DeleteResult{Response: vmkit.Response{OK: true, Backend: opts.Backend}}, nil
	}
	state, _, err := LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return DeleteResult{}, err
	}
	if state == vmkit.StateRunning || state == vmkit.StateStarting {
		command := "stop"
		if deleteOpts.Force {
			command = "kill"
		}
		if resp, err := controlForDelete(ctx, opts, command); err != nil {
			return DeleteResult{Response: resp}, err
		}
	}
	resp, err := Control(ctx, opts, "delete")
	if err != nil && deleteNeedsStopped(err, resp) {
		command := "stop"
		if deleteOpts.Force {
			command = "kill"
		}
		if stopResp, stopErr := controlForDelete(ctx, opts, command); stopErr != nil {
			return DeleteResult{Response: stopResp}, stopErr
		}
		resp, err = Control(ctx, opts, "delete")
	}
	if err == nil && resp.OK {
		// Best-effort: a stale holder is reclaimed on the next attach even if
		// registry cleanup fails here.
		_ = volume.DetachAll(opts.StateDir, opts.Name)
	}
	return DeleteResult{Response: resp, Deleted: err == nil && resp.OK}, err
}

func controlForDelete(ctx context.Context, opts Options, command string) (vmkit.Response, error) {
	resp, err := Control(ctx, opts, command)
	if err != nil {
		return resp, err
	}
	if resp.OK {
		return resp, nil
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, fmt.Errorf("%s workspace %s failed", command, opts.Name)
}

func deleteNeedsStopped(err error, resp vmkit.Response) bool {
	text := ""
	if err != nil {
		text = err.Error()
	}
	if resp.Error != "" {
		if text != "" {
			text += " "
		}
		text += resp.Error
	}
	if !strings.Contains(text, "before delete") {
		return false
	}
	return strings.Contains(text, "is running") ||
		strings.Contains(text, "is starting") ||
		strings.Contains(text, "is paused")
}

// deleteBlockedByState reports whether a workspace in this reconciled state is
// still live enough that deleting it would destroy a running VM. delete tears
// the VM down and erases its runtime dir, so a running/starting/paused workspace
// must be stopped or killed first. Terminal and settling states
// (stopped/halted/failed/stopping/quarantined) are left to the backend
// supervisor's own delete handling, unchanged.
func deleteBlockedByState(state vmkit.VMState) bool {
	switch state {
	case vmkit.StateRunning, vmkit.StateStarting, vmkit.StatePaused:
		return true
	default:
		return false
	}
}

// ensureDeletable refuses to delete a workspace whose VM is still live, on every
// backend. Firecracker and Apple VF enforce this in their supervisors; hoisting
// the check into the shared control path keeps the behavior consistent.
// Inspect reconciles real liveness, so a crashed workspace recorded "running"
// reports a terminal state and still deletes cleanly; an Inspect error (no state
// on disk, or an unreachable supervisor) also lets delete proceed — the backend
// supervisor's own guard remains the last line, and delete stays the idempotent
// cleanup it is.
func ensureDeletable(ctx context.Context, opts Options) (vmkit.Response, error) {
	resp, err := Inspect(ctx, opts)
	if err != nil || resp.Event == nil {
		return vmkit.Response{}, nil
	}
	if deleteBlockedByState(resp.Event.State) {
		e := fmt.Errorf("workspace %s is %s; stop or kill it before delete", opts.Name, resp.Event.State)
		return vmkit.Response{OK: false, Backend: opts.Backend, Error: e.Error()}, e
	}
	return vmkit.Response{}, nil
}

func unsupportedControlCapability(backend, command string) (vmkit.Response, error) {
	if command == "pause" || command == "resume" {
		operationID := vmkit.OperationWorkspacePause
		if command == "resume" {
			operationID = vmkit.OperationWorkspaceResume
		}
		operation, _ := vmkit.OperationContractByID(operationID)
		if ready, _ := vmkit.BackendSupportsOperation(backend, operation); ready {
			return vmkit.Response{}, nil
		}
		err := vmkit.NewUnsupportedOperationError(backend, operation, command)
		return vmkit.Response{OK: false, Backend: backend, Error: err.Error()}, err
	}
	return vmkit.Response{}, nil
}

// Pause freezes a running workspace's vCPUs while preserving memory and disk
// state. The runtime process keeps running so the workspace can be resumed in
// place; structured exec, console, and stats are unavailable until Resume.
func Pause(ctx context.Context, opts Options) (vmkit.Response, error) {
	return Control(ctx, opts, "pause")
}

// Resume thaws a paused workspace's vCPUs, returning it to the running state.
func Resume(ctx context.Context, opts Options) (vmkit.Response, error) {
	return Control(ctx, opts, "resume")
}
