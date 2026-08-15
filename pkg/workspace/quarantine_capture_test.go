package workspace

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestQuarantineSkipCaptureAttemptsNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend(), SupervisorPath: dir + "/no-supervisor"}

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
	if result.Containment.Capture.Status != vmkit.ContainmentPhaseSkipped {
		t.Fatalf("capture phase = %#v, want skipped", result.Containment.Capture)
	}
	if !containmentMarkerExists(dir, "agent-1") {
		t.Fatal("containment failure removed the durable deny marker")
	}
}

func TestQuarantineCaptureFailureStaysFrozenAndSeveredUntilRetry(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	var order []string
	var progressPhases []string
	opts.Progress = func(event operation.ProgressEvent) {
		progressPhases = append(progressPhases, event.Phase)
	}
	oldDispatch := dispatchContainmentCommand
	oldCapture := captureForContainment
	t.Cleanup(func() {
		dispatchContainmentCommand = oldDispatch
		captureForContainment = oldCapture
	})
	dispatchContainmentCommand = func(_ context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		order = append(order, req.Command)
		state := vmkit.StatePaused
		if req.Command == "contain-stop" {
			state = vmkit.StateQuarantined
		}
		return vmkit.Response{OK: true, Backend: opts.Backend, Event: &vmkit.Event{State: state}}, nil
	}
	captureForContainment = func(_ context.Context, _ Options, tag string) (vmkit.SnapshotManifest, error) {
		if !containmentMarkerExists(dir, "agent-1") {
			t.Fatal("capture ran before the durable marker")
		}
		order = append(order, "capture:"+tag)
		return vmkit.SnapshotManifest{}, errors.New("capture device failed")
	}

	result, err := Quarantine(context.Background(), opts, QuarantineOptions{CaptureTag: "forensic-test"})
	if err == nil || !strings.Contains(err.Error(), "capture frozen evidence") {
		t.Fatalf("capture failure err = %v, want structured partial failure", err)
	}
	if result.Captured {
		t.Fatal("capture reported success without a runtime")
	}
	if strings.TrimSpace(result.CaptureError) == "" {
		t.Fatal("a failed capture must be reported, not silently dropped")
	}
	wantOrder := []string{"contain-freeze", "contain-sever", "capture:forensic-test"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("containment order = %#v, want %#v", order, wantOrder)
	}
	wantProgress := []string{"quarantine_validate", "quarantine_mark", "quarantine_freeze", "quarantine_sever", "quarantine_capture"}
	if !reflect.DeepEqual(progressPhases, wantProgress) {
		t.Fatalf("progress phases = %#v, want %#v", progressPhases, wantProgress)
	}
	if result.Containment.Freeze.Status != vmkit.ContainmentPhaseCompleted ||
		result.Containment.Severance.Status != vmkit.ContainmentPhaseCompleted ||
		result.Containment.Capture.Status != vmkit.ContainmentPhaseFailed ||
		result.Containment.Stop.Status != vmkit.ContainmentPhasePending ||
		result.Containment.Custody.Status != vmkit.ContainmentPhasePending ||
		result.Containment.State != "in_progress" {
		t.Fatalf("containment result = %#v", result.Containment)
	}
	if err := EnsureCanStart(dir, "agent-1"); err == nil || !strings.Contains(err.Error(), "containment marker") {
		t.Fatalf("EnsureCanStart after containment = %v, want durable denial", err)
	}

	order = nil
	captureForContainment = func(_ context.Context, _ Options, tag string) (vmkit.SnapshotManifest, error) {
		order = append(order, "capture:"+tag)
		return vmkit.SnapshotManifest{}, nil
	}
	retried, err := Quarantine(context.Background(), opts, QuarantineOptions{})
	if err != nil {
		t.Fatalf("retry containment: %v", err)
	}
	if want := []string{"capture:forensic-test", "contain-stop"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("retry order = %#v, want %#v", order, want)
	}
	if !retried.Captured || retried.Containment.State != "contained" || retried.Containment.Custody.Status != vmkit.ContainmentPhaseCompleted {
		t.Fatalf("retried containment = %#v", retried)
	}
}

func TestQuarantineCaptureFailureCanBeStoppedOnlyByExplicitSkip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	var order []string
	oldDispatch := dispatchContainmentCommand
	oldCapture := captureForContainment
	dispatchContainmentCommand = func(_ context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		order = append(order, req.Command)
		state := vmkit.StatePaused
		if req.Command == "contain-stop" {
			state = vmkit.StateQuarantined
		}
		return vmkit.Response{OK: true, Backend: opts.Backend, Event: &vmkit.Event{State: state}}, nil
	}
	captureForContainment = func(_ context.Context, _ Options, _ string) (vmkit.SnapshotManifest, error) {
		order = append(order, "capture")
		return vmkit.SnapshotManifest{}, errors.New("capture device failed")
	}
	t.Cleanup(func() {
		dispatchContainmentCommand = oldDispatch
		captureForContainment = oldCapture
	})

	if _, err := Quarantine(context.Background(), opts, QuarantineOptions{CaptureTag: "forensic-test"}); err == nil {
		t.Fatal("expected the first capture to fail")
	}
	order = nil
	result, err := Quarantine(context.Background(), opts, QuarantineOptions{SkipCapture: true})
	if err != nil {
		t.Fatalf("explicit evidence-loss retry: %v", err)
	}
	if want := []string{"contain-stop"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("explicit skip retry order = %#v, want %#v", order, want)
	}
	if result.Containment.Capture.Status != vmkit.ContainmentPhaseSkipped || result.Containment.CaptureTag != "" || result.Containment.State != "contained" {
		t.Fatalf("explicit skip result = %#v", result.Containment)
	}
}

func TestQuarantineFreezeFailureUsesFreshFailSafeSeverAndStop(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	var order []string
	var freezeBudget, stopBudget time.Duration
	oldDispatch := dispatchContainmentCommand
	oldCapture := captureForContainment
	dispatchContainmentCommand = func(ctx context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		order = append(order, req.Command)
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("%s containment phase has no deadline", req.Command)
		}
		switch req.Command {
		case "contain-freeze":
			freezeBudget = time.Until(deadline)
			return vmkit.Response{Backend: opts.Backend}, errors.New("VMM pause unavailable")
		case "quarantine":
			stopBudget = time.Until(deadline)
			return vmkit.Response{OK: true, Backend: opts.Backend, Event: &vmkit.Event{State: vmkit.StateQuarantined}}, nil
		default:
			t.Fatalf("unexpected phase after freeze failure: %s", req.Command)
			return vmkit.Response{}, nil
		}
	}
	captureForContainment = func(context.Context, Options, string) (vmkit.SnapshotManifest, error) {
		t.Fatal("capture ran without a confirmed freeze")
		return vmkit.SnapshotManifest{}, nil
	}
	t.Cleanup(func() {
		dispatchContainmentCommand = oldDispatch
		captureForContainment = oldCapture
	})

	result, err := Quarantine(context.Background(), opts, QuarantineOptions{})
	if err == nil || !strings.Contains(err.Error(), "freeze") {
		t.Fatalf("freeze failure err = %v", err)
	}
	if want := []string{"contain-freeze", "quarantine"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("freeze failure order = %#v, want %#v", order, want)
	}
	if freezeBudget <= 0 || freezeBudget > containmentControlTimeout || stopBudget < 45*time.Second {
		t.Fatalf("phase budgets freeze=%s stop=%s", freezeBudget, stopBudget)
	}
	if result.Containment.Freeze.Status != vmkit.ContainmentPhaseFailed ||
		result.Containment.Severance.Status != vmkit.ContainmentPhaseSkipped ||
		result.Containment.Stop.Status != vmkit.ContainmentPhaseCompleted ||
		result.Containment.Custody.Status != vmkit.ContainmentPhaseCompleted ||
		result.Containment.State != "contained" {
		t.Fatalf("fail-safe containment result = %#v", result.Containment)
	}
}

func TestQuarantineRetryRearmsCaptureAfterFailedEmergencyStop(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	oldDispatch := dispatchContainmentCommand
	oldCapture := captureForContainment
	first := true
	var order []string
	dispatchContainmentCommand = func(_ context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		order = append(order, req.Command)
		if first {
			return vmkit.Response{Backend: opts.Backend}, errors.New("host control unavailable")
		}
		state := vmkit.StatePaused
		if req.Command == "contain-stop" {
			state = vmkit.StateQuarantined
		}
		return vmkit.Response{OK: true, Backend: opts.Backend, Event: &vmkit.Event{State: state}}, nil
	}
	captureForContainment = func(_ context.Context, _ Options, tag string) (vmkit.SnapshotManifest, error) {
		order = append(order, "capture:"+tag)
		return vmkit.SnapshotManifest{Tag: tag}, nil
	}
	t.Cleanup(func() {
		dispatchContainmentCommand = oldDispatch
		captureForContainment = oldCapture
	})

	failed, err := Quarantine(context.Background(), opts, QuarantineOptions{CaptureTag: "forensic-test"})
	if err == nil {
		t.Fatal("first containment unexpectedly succeeded")
	}
	if want := []string{"contain-freeze", "quarantine"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("failed attempt order = %#v, want %#v", order, want)
	}
	if !failed.Containment.CaptureRequired || failed.Containment.Capture.Status != vmkit.ContainmentPhaseSkipped || failed.Containment.Stop.Status != vmkit.ContainmentPhaseFailed {
		t.Fatalf("failed attempt = %#v", failed.Containment)
	}

	first = false
	order = nil
	retried, err := Quarantine(context.Background(), opts, QuarantineOptions{})
	if err != nil {
		t.Fatalf("retry containment: %v", err)
	}
	if len(order) != 4 || order[0] != "contain-freeze" || order[1] != "contain-sever" || !strings.HasPrefix(order[2], "capture:forensic-") || order[3] != "contain-stop" {
		t.Fatalf("retry order = %#v, want freeze/sever/new forensic capture/stop", order)
	}
	if !retried.Captured || retried.Containment.Capture.Status != vmkit.ContainmentPhaseCompleted || retried.Containment.State != "contained" {
		t.Fatalf("retried containment = %#v", retried)
	}
}

func TestContainmentMarkerBlocksResumeDeleteAndAppearsInStatus(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	state := RuntimeState{Event: EventFile{Identity: vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend}, State: vmkit.StateQuarantined}}
	if err := writeJSONFile(dir+"/agent-1/runtime.json", state); err != nil {
		t.Fatal(err)
	}
	containment, err := beginContainment(opts, "forensic-test", false)
	if err != nil {
		t.Fatal(err)
	}
	containment.State = "contained"
	containment.Freeze = completedContainmentPhase()
	containment.Severance = completedContainmentPhase()
	containment.Capture = completedContainmentPhase()
	containment.Stop = completedContainmentPhase()
	containment.Custody = completedContainmentPhase()
	if err := writeContainment(opts, &containment); err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"resume", "pause", "delete"} {
		resp, err := Control(context.Background(), opts, command)
		if err == nil || resp.OK || !strings.Contains(err.Error(), "containment") {
			t.Fatalf("Control(%s) resp=%#v err=%v, want containment denial", command, resp, err)
		}
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Containment == nil || resp.Containment.State != "contained" || resp.Containment.CaptureTag != "forensic-test" {
		t.Fatalf("status containment = %#v", resp.Containment)
	}
	for _, tag := range []string{"forensic-test", "ordinary"} {
		if err := os.MkdirAll(vmkit.SnapshotDir(dir, opts.Name, tag), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := SnapshotRemove(opts, "forensic-test"); err == nil || !strings.Contains(err.Error(), "custody") {
		t.Fatalf("remove custody snapshot = %v, want denial", err)
	}
	if err := SnapshotRemove(opts, "ordinary"); err != nil {
		t.Fatalf("remove unrelated ordinary snapshot: %v", err)
	}
}

func TestQuarantineReconcilesCrashAfterRuntimeReachedCustody(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	state := RuntimeState{Event: EventFile{
		Identity: vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend},
		State:    vmkit.StateQuarantined,
	}}
	if err := writeJSONFile(dir+"/agent-1/runtime.json", state); err != nil {
		t.Fatal(err)
	}
	containment, err := beginContainment(opts, "forensic-test", false)
	if err != nil {
		t.Fatal(err)
	}
	containment.Freeze = completedContainmentPhase()
	containment.Severance = completedContainmentPhase()
	containment.Capture = completedContainmentPhase()
	if err := writeContainment(opts, &containment); err != nil {
		t.Fatal(err)
	}

	oldDispatch := dispatchContainmentCommand
	dispatchContainmentCommand = func(_ context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		t.Fatalf("runtime already in custody, but dispatched %q", req.Command)
		return vmkit.Response{}, nil
	}
	t.Cleanup(func() { dispatchContainmentCommand = oldDispatch })

	result, err := Quarantine(context.Background(), opts, QuarantineOptions{})
	if err != nil {
		t.Fatalf("reconcile containment: %v", err)
	}
	if result.Containment.State != "contained" ||
		result.Containment.Stop.Status != vmkit.ContainmentPhaseCompleted ||
		result.Containment.Custody.Status != vmkit.ContainmentPhaseCompleted {
		t.Fatalf("reconciled containment = %#v", result.Containment)
	}
	persisted, err := ReadContainment(dir, opts.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != "contained" || persisted.Custody.Status != vmkit.ContainmentPhaseCompleted {
		t.Fatalf("persisted containment = %#v", persisted)
	}
}

func TestQuarantineRecoversPublishedCaptureAfterPhaseWriteCrash(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	state := RuntimeState{Event: EventFile{
		Identity: vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend},
		State:    vmkit.StateQuarantined,
	}}
	if err := writeJSONFile(dir+"/agent-1/runtime.json", state); err != nil {
		t.Fatal(err)
	}
	containment, err := beginContainment(opts, "forensic-test", false)
	if err != nil {
		t.Fatal(err)
	}
	containment.Freeze = completedContainmentPhase()
	containment.Severance = completedContainmentPhase()
	if err := writeContainment(opts, &containment); err != nil {
		t.Fatal(err)
	}
	snapshotDir := vmkit.SnapshotDir(dir, opts.Name, "forensic-test")
	if err := vmkit.WriteSnapshotManifest(snapshotDir, vmkit.SnapshotManifest{
		Tag: "forensic-test", Forensic: true, FrozenProcessState: true,
	}); err != nil {
		t.Fatal(err)
	}

	oldDispatch := dispatchContainmentCommand
	dispatchContainmentCommand = func(_ context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		t.Fatalf("published capture recovery dispatched %q", req.Command)
		return vmkit.Response{}, nil
	}
	t.Cleanup(func() { dispatchContainmentCommand = oldDispatch })

	result, err := Quarantine(context.Background(), opts, QuarantineOptions{})
	if err != nil {
		t.Fatalf("recover published capture: %v", err)
	}
	if !result.Captured || result.CaptureTag != "forensic-test" || result.Containment.Capture.Status != vmkit.ContainmentPhaseCompleted || result.Containment.State != "contained" {
		t.Fatalf("recovered containment = %#v", result)
	}
}

func TestQuarantineBackendMismatchFailsClosedWithoutDispatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	markerBackend := vmkit.BackendAppleVF
	if markerBackend == HostBackend() {
		markerBackend = vmkit.BackendLinuxKVM
	}
	original := Options{StateDir: dir, Name: "agent-1", Backend: markerBackend}
	if _, err := beginContainment(original, "forensic-test", false); err != nil {
		t.Fatal(err)
	}

	oldDispatch := dispatchContainmentCommand
	dispatchContainmentCommand = func(_ context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		t.Fatalf("backend mismatch dispatched %q", req.Command)
		return vmkit.Response{}, nil
	}
	t.Cleanup(func() { dispatchContainmentCommand = oldDispatch })

	requested := original
	requested.Backend = HostBackend()
	result, err := Quarantine(context.Background(), requested, QuarantineOptions{})
	if err == nil || !errors.Is(err, errContainmentBackendMismatch) {
		t.Fatalf("backend mismatch err = %v, want typed fail-closed error", err)
	}
	if !containmentMarkerExists(dir, requested.Name) {
		t.Fatal("backend mismatch removed the durable containment marker")
	}
	if result.Containment.Backend != requested.Backend || result.Containment.State != "in_progress" {
		t.Fatalf("structured mismatch result = %#v", result.Containment)
	}
}

func TestStatusWithDamagedContainmentRecordIsTypedAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	if err := os.MkdirAll(vmkit.ContainmentMarkerDir(dir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	state := RuntimeState{Event: EventFile{
		Identity: vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend},
		State:    vmkit.StatePaused,
	}}
	if err := writeJSONFile(dir+"/agent-1/runtime.json", state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vmkit.ContainmentResultPath(dir, opts.Name), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := Status(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Containment == nil || resp.Containment.Error == "" || resp.Containment.State != "in_progress" {
		t.Fatalf("damaged containment status = %#v", resp.Containment)
	}
	if validateErr := vmkit.ValidateContainmentResult(*resp.Containment); validateErr != nil {
		t.Fatalf("synthetic containment status is not typed/valid: %v", validateErr)
	}
	if err := EnsureCanStart(dir, opts.Name); err == nil {
		t.Fatal("damaged containment record bypassed the marker")
	}
}

func TestControlQuarantineUsesTheCompleteContainmentPrimitive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/agent-1", 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	var order []string
	oldDispatch := dispatchContainmentCommand
	oldCapture := captureForContainment
	dispatchContainmentCommand = func(_ context.Context, _ Options, req vmkit.Request) (vmkit.Response, error) {
		order = append(order, req.Command)
		state := vmkit.StatePaused
		if req.Command == "contain-stop" {
			state = vmkit.StateQuarantined
		}
		return vmkit.Response{OK: true, Backend: opts.Backend, Event: &vmkit.Event{State: state}}, nil
	}
	captureForContainment = func(_ context.Context, _ Options, tag string) (vmkit.SnapshotManifest, error) {
		order = append(order, "capture")
		return vmkit.SnapshotManifest{Tag: tag}, nil
	}
	t.Cleanup(func() {
		dispatchContainmentCommand = oldDispatch
		captureForContainment = oldCapture
	})

	resp, err := Control(context.Background(), opts, "quarantine")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"contain-freeze", "contain-sever", "capture", "contain-stop"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("Control quarantine order = %#v, want %#v", order, want)
	}
	if resp.Containment == nil || resp.Containment.State != "contained" || resp.Containment.Capture.Status != vmkit.ContainmentPhaseCompleted {
		t.Fatalf("Control quarantine response = %#v", resp)
	}
}

func TestDispatchCannotBypassLibraryContainment(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent-1", Backend: HostBackend()}
	identity := &vmkit.Identity{RequestID: "req-1", RuntimeID: opts.Name, Role: vmkit.RoleWorkload, Backend: opts.Backend}
	for _, command := range []string{"quarantine", "contain-freeze", "contain-sever", "contain-stop"} {
		resp, err := Dispatch(context.Background(), opts, vmkit.Request{Command: command, Identity: identity, Config: &vmkit.Config{StateDir: dir}})
		if err == nil || resp.OK {
			t.Fatalf("Dispatch(%s) resp=%#v err=%v, want library-owned denial", command, resp, err)
		}
	}
	if err := os.MkdirAll(vmkit.ContainmentMarkerDir(dir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	resp, err := Dispatch(context.Background(), opts, vmkit.Request{Command: "exec", Identity: identity, Config: &vmkit.Config{StateDir: dir}})
	if err == nil || resp.OK || !strings.Contains(err.Error(), "containment marker") {
		t.Fatalf("Dispatch(exec) resp=%#v err=%v, want marker denial", resp, err)
	}
}

// TestQuarantineCaptureTagIsIdentifiable: an automatic capture must be
// recognizable as one on sight, and must not collide with an operator's tags.
func TestQuarantineCaptureTagIsIdentifiable(t *testing.T) {
	if !strings.HasPrefix(ForensicCaptureTagPrefix, "forensic") {
		t.Fatalf("capture tag prefix %q does not identify a forensic capture", ForensicCaptureTagPrefix)
	}
}
