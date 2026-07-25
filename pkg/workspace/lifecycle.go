package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/internal/eventhistory"
	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"gopkg.in/yaml.v3"
)

type Result struct {
	Workspace    string                     `json:"workspace"`
	StateDir     string                     `json:"state_dir"`
	Profile      string                     `json:"profile,omitempty"`
	Restart      string                     `json:"restart"`
	Resources    Resources                  `json:"resources"`
	Network      NetworkSpec                `json:"network,omitempty"`
	Service      string                     `json:"service_command,omitempty"`
	ConsoleShell string                     `json:"shell,omitempty"`
	Hostname     string                     `json:"hostname,omitempty"`
	RootfsPath   string                     `json:"rootfs_path"`
	KernelPath   string                     `json:"kernel_path"`
	Disks        []Disk                     `json:"disks,omitempty"`
	Artifacts    Artifacts                  `json:"artifacts,omitempty"`
	SerialPath   string                     `json:"serial_path,omitempty"`
	SerialLog    string                     `json:"serial_log,omitempty"`
	FinalState   string                     `json:"final_state,omitempty"`
	Result       *GuestResult               `json:"result,omitempty"`
	Image        rootfs.Provenance          `json:"image"`
	Verification *vmkit.RuntimeVerification `json:"verification,omitempty"`
	Response     vmkit.Response             `json:"response"`
}

type GuestResult struct {
	StartedAt       string `json:"started_at"`
	ExitedAt        string `json:"exited_at"`
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}

type EventFile struct {
	Identity   vmkit.Identity `json:"identity"`
	State      vmkit.VMState  `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ObservedAt string         `json:"observedAt"`
}

type RuntimeState struct {
	Event                  EventFile              `json:"event"`
	Config                 vmkit.Config           `json:"config"`
	PID                    int                    `json:"pid,omitempty"`
	ComputeSystemRuntimeID string                 `json:"computeSystemRuntimeID,omitempty"`
	VsockListenerPID       int                    `json:"vsockListenerPid,omitempty"`
	SerialLogPath          string                 `json:"serialLogPath"`
	SerialInputPath        string                 `json:"serialInputPath,omitempty"`
	StartedAt              string                 `json:"startedAt,omitempty"`
	UpdatedAt              string                 `json:"updatedAt"`
	Readiness              vmkit.RuntimeReadiness `json:"readiness,omitempty"`
	Error                  string                 `json:"error,omitempty"`
}

type ListEntry struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Backend    string `json:"backend,omitempty"`
	Profile    string `json:"profile,omitempty"`
	Restart    string `json:"restart,omitempty"`
	Network    string `json:"network,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
	RootfsPath string `json:"rootfs_path,omitempty"`
	SerialPath string `json:"serial_path,omitempty"`
}

func Create(ctx context.Context, opts Options) (Result, error) {
	opts.PrepareForStart = true
	if opts.Name == "" {
		return Result{}, operation.New(operation.ErrorValidation, "create requires a name")
	}
	if err := ValidateName(opts.Name); err != nil {
		return Result{}, err
	}
	if opts.ImageRef == "" {
		opts.ImageRef = DefaultImage(opts.Architecture)
	}
	if opts.UseImageCommand && strings.TrimSpace(opts.ServiceCommand) != "" {
		return Result{}, operation.New(operation.ErrorConflict, "create cannot use both image command and service command")
	}
	if err := normalizeLifecycleOptions(&opts, true); err != nil {
		return Result{}, err
	}
	if err := EnsureKernel(ctx, &opts); err != nil {
		return Result{}, err
	}
	if err := materializeCredSwapConfig(&opts); err != nil {
		return Result{}, err
	}
	if err := EnsureCanCreate(opts); err != nil {
		return Result{}, err
	}
	disks, err := PrepareDisks(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	opts.Disks = disks
	result, err := BuildRootfs(ctx, opts)
	if err != nil {
		return result, err
	}
	// An auto-sized build may have grown the disk; record the size the
	// workspace actually has.
	if result.Resources.SizeMiB > opts.SizeMiB {
		opts.SizeMiB = result.Resources.SizeMiB
	}
	result.Disks = disks
	verification, err := BuildVerification(opts, result)
	if err != nil {
		return result, err
	}
	opts.Verification = &verification
	result.Verification = &verification
	if err := WriteManifest(opts); err != nil {
		return result, err
	}
	if HasGuestCommand(opts) && (strings.TrimSpace(opts.ServiceCommand) == "" || HasSetupCommand(opts) || strings.TrimSpace(opts.ExecCommand) != "") {
		runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
		stopProgress := startIndeterminateProgress(opts.Progress, "guest-setup", "running guest setup")
		runReq, reqErr := Request(opts, "run", result.RootfsPath, NewRequestID())
		if reqErr != nil {
			stopProgress("egress policy invalid")
			return result, reqErr
		}
		resp, runErr := runForeground(runCtx, opts, runReq)
		result.Response = resp
		if runErr != nil {
			stopProgress("guest setup failed")
		} else {
			stopProgress("guest setup complete")
		}
		result.SerialPath = SerialLogPath(opts.StateDir, opts.Name)
		if runErr != nil {
			return result, runErr
		}
		finalResp, waitErr := Inspect(ctx, opts)
		if finalResp.Event != nil {
			result.FinalState = string(finalResp.Event.State)
			result.Response = finalResp
		}
		fillRunResult(&result, opts)
		return result, waitErr
	}
	prepReq, err := Request(opts, "prepare", result.RootfsPath, NewRequestID())
	if err != nil {
		return result, err
	}
	resp, err := Dispatch(ctx, opts, prepReq)
	result.Response = resp
	return result, err
}

func EnsureCanCreate(opts Options) error {
	state, _, err := LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return err
	}
	switch state {
	case vmkit.StateStarting, vmkit.StateRunning:
		return operation.New(operation.ErrorConflict, "workspace %s is already %s; stop or delete it before create", opts.Name, state)
	}
	return ensureHostPortsAvailable(opts.Network.PortForwards)
}

func ensureHostPortsAvailable(forwards []vmkit.PortForward) error {
	for _, forward := range forwards {
		protocol := strings.TrimSpace(forward.Protocol)
		if protocol == "" {
			protocol = "tcp"
		}
		if protocol != "tcp" || forward.HostPort == 0 {
			continue
		}
		host := strings.TrimSpace(forward.Host)
		if host == "" || host == "localhost" {
			host = "127.0.0.1"
		}
		addr := net.JoinHostPort(host, strconv.Itoa(int(forward.HostPort)))
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("host port %s is unavailable; stop the process using it or choose another publish port: %w", addr, err)
		}
		_ = listener.Close()
	}
	return nil
}

func startIndeterminateProgress(progress rootfs.ProgressFunc, phase, message string) func(string) {
	if progress == nil {
		return func(string) {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	started := time.Now()
	progress(rootfs.ProgressEvent{
		Phase:         phase,
		Message:       message,
		Indeterminate: true,
	})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				progress(rootfs.ProgressEvent{
					Phase:         phase,
					Message:       message,
					Current:       int64(time.Since(started).Round(time.Second) / time.Second),
					Indeterminate: true,
				})
			}
		}
	}()
	return func(finalMessage string) {
		close(done)
		<-stopped
		progress(rootfs.ProgressEvent{
			Phase:   phase,
			Message: finalMessage,
			Current: int64(time.Since(started).Round(time.Second) / time.Second),
		})
	}
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if strings.TrimSpace(opts.ExecCommand) == "" && !opts.UseImageCommand {
		return Result{}, operation.New(operation.ErrorValidation, "run requires ExecCommand")
	}
	if opts.Name == "" {
		opts.Name = RandomName("run")
	}
	if err := ValidateName(opts.Name); err != nil {
		return Result{}, err
	}
	if opts.ImageRef == "" {
		opts.ImageRef = DefaultImage(opts.Architecture)
	}
	if err := normalizeLifecycleOptions(&opts, true); err != nil {
		return Result{}, err
	}
	if err := EnsureKernel(ctx, &opts); err != nil {
		return Result{}, err
	}
	if err := materializeCredSwapConfig(&opts); err != nil {
		return Result{}, err
	}
	disks, err := PrepareDisks(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	opts.Disks = disks
	result, err := BuildRootfs(ctx, opts)
	if err != nil {
		return result, err
	}
	// An auto-sized build may have grown the disk; record the size the
	// workspace actually has.
	if result.Resources.SizeMiB > opts.SizeMiB {
		opts.SizeMiB = result.Resources.SizeMiB
	}
	result.Disks = disks
	verification, err := BuildVerification(opts, result)
	if err != nil {
		return result, err
	}
	opts.Verification = &verification
	result.Verification = &verification
	if err := WriteManifest(opts); err != nil {
		return result, err
	}
	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	stopProgress := startIndeterminateProgress(opts.Progress, "guest-run", "booting and running command")
	runReq, err := Request(opts, "run", result.RootfsPath, NewRequestID())
	if err != nil {
		stopProgress("egress policy invalid")
		return result, err
	}
	resp, err := runForeground(runCtx, opts, runReq)
	if err != nil {
		stopProgress("run failed")
	} else {
		stopProgress("command complete")
	}
	result.Response = resp
	result.SerialPath = SerialLogPath(opts.StateDir, opts.Name)
	if err == nil && resp.OK {
		finalResp, waitErr := Inspect(ctx, opts)
		if finalResp.Event != nil {
			result.FinalState = string(finalResp.Event.State)
			result.Response = finalResp
		}
		fillRunResult(&result, opts)
		if waitErr != nil {
			return result, waitErr
		}
		// Persistence contract: `run` is one-shot and discards its disk by default
		// (--keep retains, --rm is the explicit discard). `create`+`start` are durable
		// and persist; `delete` is the explicit removal for durable workspaces.
		if !opts.Keep {
			Cleanup(opts.StateDir, opts.Name)
			result.SerialPath = ""
		}
	}
	return result, err
}

func Start(ctx context.Context, opts Options) (Result, error) {
	if opts.Name == "" {
		return Result{}, operation.New(operation.ErrorValidation, "start requires a name")
	}
	if err := ValidateName(opts.Name); err != nil {
		return Result{}, err
	}
	if tag := strings.TrimSpace(opts.FromSnapshot); tag != "" {
		backend := opts.Backend
		if backend == "" {
			backend = DefaultOptions().Backend
		}
		operation, _ := vmkit.OperationContractByID(vmkit.OperationSnapshotRestore)
		if ready, _ := vmkit.BackendSupportsOperation(backend, operation); !ready {
			return Result{}, vmkit.NewUnsupportedOperationError(backend, operation, "snapshot restore (--from-snapshot)")
		}
	}
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return Result{}, err
	}
	if err := EnsureKernel(ctx, &opts); err != nil {
		return Result{}, err
	}
	if opts.ResultPort == 0 && !opts.MaintenanceBoot {
		opts.ResultPort = DefaultResultPort
	}
	if err := EnsureCanStart(opts.StateDir, opts.Name); err != nil {
		return Result{}, err
	}
	requestedProfile := opts.Profile
	requestedMemoryMiB := opts.MemoryMiB
	requestedCPUCount := opts.CPUCount
	manifest, err := ReadManifest(opts.StateDir, opts.Name)
	if err != nil {
		return Result{}, err
	}
	if !opts.MaintenanceBoot {
		// A maintenance boot deliberately deviates from the manifest: no
		// secrets, no model pairing, no forwards, isolated networking. The
		// caller supplies the complete minimal options.
		applyManifest(&opts, manifest)
	}
	if tag := strings.TrimSpace(opts.FromSnapshot); tag != "" {
		// Resume-in-place of a workspace that was itself a fork: the loaded
		// VM keeps its baked identity (ancestor vsock path, guest service
		// ports), exactly as CreateFromSnapshot adopts it for a new fork.
		// Without this, stop + start --from-snapshot of a fork bridges to
		// guest ports nobody listens on and its shell/exec are dead.
		if snapManifest, err := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(opts.StateDir, opts.Name, tag)); err == nil {
			adoptSnapshotIdentity(&opts, snapManifest)
		}
	}
	if opts.ProfileExplicit {
		opts.Profile = requestedProfile
		if err := ApplyProfile(&opts, opts.SpecMemory, opts.SpecCPU, true); err != nil {
			return Result{}, err
		}
	}
	if opts.SpecMemory {
		opts.MemoryMiB = requestedMemoryMiB
	}
	if opts.SpecCPU {
		opts.CPUCount = requestedCPUCount
	}
	if err := ValidateResources(Resources{MemoryMiB: opts.MemoryMiB, CPUCount: opts.CPUCount}, false); err != nil {
		return Result{}, err
	}
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)
	if _, err := os.Stat(rootfsPath); err != nil {
		return Result{}, err
	}
	if err := os.Remove(ResultPath(opts.StateDir, opts.Name)); err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	startReq, err := Request(opts, "run", rootfsPath, NewRequestID())
	if err != nil {
		return Result{}, err
	}
	if tag := strings.TrimSpace(opts.FromSnapshot); tag != "" {
		startReq.Tag = tag
		if opts.Backend == vmkit.BackendAppleVF {
			if err := prepareAppleVFSnapshotRestore(opts, startReq); err != nil {
				return Result{}, err
			}
		}
	}
	resp, err := startDetached(opts, startReq)
	return Result{
		Workspace:    opts.Name,
		StateDir:     opts.StateDir,
		Profile:      opts.Profile,
		Restart:      opts.RestartPolicy,
		Resources:    ResourcesFromOptions(opts),
		Network:      NetworkSpecFromConfig(opts.Network),
		Service:      strings.TrimSpace(opts.ServiceCommand),
		ConsoleShell: strings.TrimSpace(opts.ConsoleShell),
		Hostname:     strings.TrimSpace(opts.Hostname),
		RootfsPath:   rootfsPath,
		KernelPath:   opts.KernelPath,
		Disks:        opts.Disks,
		Artifacts:    ArtifactsFromOptions(opts),
		SerialPath:   SerialLogPath(opts.StateDir, opts.Name),
		Response:     resp,
	}, err
}

func Inspect(ctx context.Context, opts Options) (vmkit.Response, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.Response{}, err
	}
	req, err := Request(opts, "inspect", "", NewRequestID())
	if err != nil {
		return vmkit.Response{}, err
	}
	resp, err := Dispatch(ctx, opts, req)
	if resp.EgressCapture == nil {
		networkMode := opts.Network.Mode
		if req.Config != nil && req.Config.Network != nil {
			networkMode = req.Config.Network.Mode
		}
		report := vmkit.NegotiateEgressCapture(opts.Backend, networkMode, opts.EgressMode)
		resp.EgressCapture = &report
	}
	return resp, err
}

func Status(opts Options) (vmkit.Response, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.Response{}, err
	}
	state, err := ReadRuntimeState(opts)
	if err == nil {
		return responseFromEvent(opts, state.Event, state.Error), nil
	}
	event, eventErr := ReadEvent(opts)
	if eventErr != nil {
		// No runtime state and no event file means the workspace does not
		// exist; report that instead of the raw file-open error. Corrupt
		// state (unreadable or malformed files) still surfaces as-is.
		if os.IsNotExist(err) && os.IsNotExist(eventErr) {
			return vmkit.Response{}, WorkspaceNotFoundError{Name: opts.Name}
		}
		return vmkit.Response{}, err
	}
	return responseFromEvent(opts, event, ""), nil
}

func ResultStatus(opts Options) (vmkit.Response, error) {
	resp, err := Status(opts)
	if err != nil {
		return resp, err
	}
	if resp.Event == nil {
		err := fmt.Errorf("workspace %s has no state event", opts.Name)
		resp.OK = false
		resp.Error = err.Error()
		return resp, err
	}
	result, resultErr := ReadRuntimeResult(opts, resp.Event.Identity)
	if resultErr != nil {
		err := fmt.Errorf("workspace %s result is not ready: %w", opts.Name, resultErr)
		resp.OK = false
		resp.Error = err.Error()
		return resp, err
	}
	resp.Result = &result
	return resp, nil
}

func ArtifactsFor(stateDir, name string) (vmkit.RuntimeArtifacts, error) {
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return vmkit.RuntimeArtifacts{}, err
	}
	return RuntimeArtifacts(manifest.Artifacts), nil
}

func List(stateDir string) ([]ListEntry, error) {
	names := map[string]bool{}
	workspaceRoot := filepath.Join(stateDir, "workspaces")
	if entries, err := os.ReadDir(workspaceRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				names[entry.Name()] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if entries, err := os.ReadDir(stateDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == "build" || entry.Name() == "workspaces" {
				continue
			}
			name := entry.Name()
			if _, err := os.Stat(filepath.Join(stateDir, name, "event.json")); err != nil {
				continue
			}
			if names[name] {
				continue
			}
			event, err := ReadEvent(Options{StateDir: stateDir, Name: name})
			if err != nil {
				continue
			}
			if isLiveState(event.State) {
				names[name] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	sortedNames := make([]string, 0, len(names))
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	out := make([]ListEntry, 0, len(sortedNames))
	for _, name := range sortedNames {
		entry := ListEntry{Name: name, State: string(vmkit.StateUnknown)}
		if manifest, err := ReadManifest(stateDir, name); err == nil {
			entry.Profile = manifest.Profile
			entry.Restart = manifest.Restart
			entry.Network = manifest.Network.Mode
		}
		if event, err := ReadEvent(Options{StateDir: stateDir, Name: name}); err == nil {
			entry.State = string(event.State)
			entry.Backend = event.Identity.Backend
			entry.ObservedAt = event.ObservedAt
		}
		for _, rootfsPath := range CandidateWorkspaceRootfsPaths(stateDir, name, entry.Backend) {
			if _, err := os.Stat(rootfsPath); err == nil {
				entry.RootfsPath = rootfsPath
				break
			}
		}
		serialPath := SerialLogPath(stateDir, name)
		if _, err := os.Stat(serialPath); err == nil {
			entry.SerialPath = serialPath
		}
		out = append(out, entry)
	}
	return out, nil
}

func isLiveState(state vmkit.VMState) bool {
	switch state {
	case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined, vmkit.StateStopping:
		return true
	default:
		return false
	}
}

// ForensicCaptureTagPrefix names automatic quarantine captures so they are
// identifiable on sight and never collide with an operator's own tags.
const ForensicCaptureTagPrefix = "forensic-"

// QuarantineOptions tunes the quarantine verb.
type QuarantineOptions struct {
	// SkipCapture contains WITHOUT first capturing evidence. Quarantine is
	// destructive to volatile state — memory, in-flight work, and any credential
	// the workload obtained at runtime are gone once the runtime stops — so
	// skipping means accepting that loss.
	SkipCapture bool
	// CaptureTag overrides the generated capture tag.
	CaptureTag string
}

// QuarantineResult reports what containment did, including whether evidence was
// captured. CaptureError is set when a capture was attempted and failed; the
// workspace is contained regardless.
type QuarantineResult struct {
	Response     vmkit.Response `json:"response"`
	CaptureTag   string         `json:"captureTag,omitempty"`
	CaptureError string         `json:"captureError,omitempty"`
	Captured     bool           `json:"captured"`
}

// Quarantine captures evidence and then contains the workspace. This is the
// verb-level entry point; Control(ctx, opts, "quarantine") is the raw
// containment primitive and does NOT capture — callers that keep custody of
// evidence themselves use that one.
//
// Capture comes FIRST because containment stops the runtime: memory, live
// processes, open connections, injected code, and runtime-obtained credentials
// exist only in volatile state and are gone once it is contained. There is no
// plausible reason to want the other order, which is why this is the default
// rather than a flag.
//
// The capture is deliberately BEST-EFFORT: containment must never be blocked by
// evidence collection, or making capture fail becomes a way to avoid being
// contained. A failure is reported loudly in the result instead — losing
// evidence silently is the thing to avoid, not the containment.
//
// The capture retains guest secrets (credential material is the evidence), so
// it is secret-bearing and not restorable. Route it to protected custody.
func Quarantine(ctx context.Context, opts Options, qopts QuarantineOptions) (QuarantineResult, error) {
	result := QuarantineResult{}
	if !qopts.SkipCapture {
		tag := strings.TrimSpace(qopts.CaptureTag)
		if tag == "" {
			tag = ForensicCaptureTagPrefix + time.Now().UTC().Format("20060102-150405")
		}
		if _, err := SnapshotForensic(ctx, opts, tag); err != nil {
			result.CaptureError = err.Error()
		} else {
			result.CaptureTag = tag
			result.Captured = true
		}
	}
	resp, err := Control(ctx, opts, "quarantine")
	result.Response = resp
	return result, err
}

// Control dispatches a raw lifecycle command. For "quarantine" this is the
// containment primitive ONLY: it does not capture evidence first. Use
// Quarantine for the verb-level behavior operators expect.
func Control(ctx context.Context, opts Options, command string) (vmkit.Response, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.Response{}, err
	}
	if err := ValidateName(opts.Name); err != nil {
		return vmkit.Response{}, err
	}
	switch command {
	case "halt", "quarantine", "pause", "resume", "stop", "kill", "delete", "gc":
	default:
		return vmkit.Response{}, operation.New(operation.ErrorUnsupported, "unsupported workspace control command: %s", command)
	}
	if resp, err := unsupportedControlCapability(opts.Backend, command); err != nil {
		return resp, err
	}
	if command == "delete" {
		if resp, err := ensureDeletable(ctx, opts); err != nil {
			return resp, err
		}
	}
	req := vmkit.Request{
		Command: command,
		Identity: &vmkit.Identity{
			RequestID: NewRequestID(),
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   opts.Backend,
		},
		Config: &vmkit.Config{StateDir: opts.StateDir},
	}
	resp, err := Dispatch(ctx, opts, req)
	if command == "delete" && resp.OK {
		Cleanup(opts.StateDir, opts.Name)
	}
	return resp, err
}

// DeleteOptions controls how a live workspace is stopped before deletion.
type DeleteOptions struct {
	Force bool
}

// Delete removes a workspace through the shared lifecycle contract. A running
// workspace is stopped first, or killed when Force is set. Confirmation remains
// an adapter concern; callers invoke Delete only after their own interaction or
// authorization policy has approved the operation.
func Delete(ctx context.Context, opts Options, deleteOpts DeleteOptions) (vmkit.Response, error) {
	state, _, err := LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return vmkit.Response{}, err
	}
	// Status reports not-found when both runtime state and event files are
	// missing. A workspace directory may still exist if creation stopped after
	// writing its disk but before the first event; keep that partial workspace
	// deletable.
	if _, statusErr := Status(opts); statusErr != nil {
		var notFound WorkspaceNotFoundError
		if errors.As(statusErr, &notFound) {
			rootDir := filepath.Dir(WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend))
			if _, statErr := os.Stat(rootDir); os.IsNotExist(statErr) {
				return vmkit.Response{}, statusErr
			}
		}
	}
	if state == vmkit.StateRunning || state == vmkit.StateStarting {
		command := "stop"
		if deleteOpts.Force {
			command = "kill"
		}
		if resp, err := controlForDelete(ctx, opts, command); err != nil {
			return resp, err
		}
	}
	resp, err := Control(ctx, opts, "delete")
	if err != nil && deleteNeedsStopped(err, resp) {
		command := "stop"
		if deleteOpts.Force {
			command = "kill"
		}
		if stopResp, stopErr := controlForDelete(ctx, opts, command); stopErr != nil {
			return stopResp, stopErr
		}
		resp, err = Control(ctx, opts, "delete")
	}
	if err == nil && resp.OK {
		// Best-effort: a stale holder is reclaimed on the next attach even if
		// registry cleanup fails here.
		_ = volume.DetachAll(opts.StateDir, opts.Name)
	}
	return resp, err
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

// Snapshot captures a tagged snapshot of a running or paused workspace via the
// backend supervisor and returns the resulting manifest, enriched with the
// workspace image reference. A running workspace is briefly paused and resumed
// around the capture; an already-paused workspace stays paused. Memory comes
// from a live VM, so quarantine (which stops the runtime) makes a workspace
// uncapturable — capture BEFORE containing when volatile state matters.
func Snapshot(ctx context.Context, opts Options, tag string) (vmkit.SnapshotManifest, error) {
	return snapshotWith(ctx, opts, tag, false)
}

// SnapshotForensic captures for INVESTIGATION rather than restore: the guest
// secret purge is skipped, because credential material is the evidence and
// exists only in volatile memory. The resulting manifest records secrets as
// materialized and NOT purged, which ValidateSnapshotSecretRestore refuses — so
// a forensic capture can never be rehydrated as a workspace, and its flags mark
// it as secret-bearing so callers route it to protected custody.
func SnapshotForensic(ctx context.Context, opts Options, tag string) (vmkit.SnapshotManifest, error) {
	return snapshotWith(ctx, opts, tag, true)
}

func snapshotWith(ctx context.Context, opts Options, tag string, retainSecrets bool) (vmkit.SnapshotManifest, error) {
	if err := ValidateName(opts.Name); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	if strings.TrimSpace(tag) == "" {
		return vmkit.SnapshotManifest{}, fmt.Errorf("snapshot tag is required")
	}
	backend := opts.Backend
	if backend == "" {
		backend = DefaultOptions().Backend
	}
	operation, _ := vmkit.OperationContractByID(vmkit.OperationSnapshotCreate)
	if ready, _ := vmkit.BackendSupportsOperation(backend, operation); !ready {
		return vmkit.SnapshotManifest{}, vmkit.NewUnsupportedOperationError(backend, operation, "snapshot create")
	}
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	if opts.Backend == vmkit.BackendAppleVF {
		return snapshotAppleVF(ctx, opts, tag, retainSecrets)
	}
	req := vmkit.Request{
		Command: "snapshot",
		Identity: &vmkit.Identity{
			RequestID: NewRequestID(),
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   opts.Backend,
		},
		Config:        &vmkit.Config{StateDir: opts.StateDir},
		Tag:           tag,
		RetainSecrets: retainSecrets,
	}
	if _, err := Dispatch(ctx, opts, req); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	dir := vmkit.SnapshotDir(opts.StateDir, opts.Name, tag)
	manifest, err := vmkit.ReadSnapshotManifest(dir)
	if err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	if manifest.ImageRef == "" {
		if workspaceManifest, err := ReadManifest(opts.StateDir, opts.Name); err == nil && workspaceManifest.Verification != nil {
			if ref := strings.TrimSpace(workspaceManifest.Verification.ImageRef); ref != "" {
				manifest.ImageRef = ref
				if err := vmkit.WriteSnapshotManifest(dir, manifest); err != nil {
					return vmkit.SnapshotManifest{}, err
				}
			}
		}
	}
	return manifest, nil
}

func snapshotAppleVF(ctx context.Context, opts Options, tag string, retainSecrets bool) (vmkit.SnapshotManifest, error) {
	state, err := ReadRuntimeState(opts)
	if err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	previousState := state.Event.State
	if previousState != vmkit.StateRunning && previousState != vmkit.StatePaused {
		return vmkit.SnapshotManifest{}, fmt.Errorf("apple-vf workspace %s is %s; snapshot requires a running or paused workspace", opts.Name, previousState)
	}
	// The secrets control port is a purge precondition; a forensic
	// (retainSecrets) capture never purges, so it has no use for the channel.
	if !retainSecrets && vmkit.MaterializedSecretsDeclared(&state.Config) && state.Config.SecretsControlPort == 0 {
		return vmkit.SnapshotManifest{}, fmt.Errorf("cannot purge secrets for snapshot: workspace %s has materialized secrets but no secrets control port", opts.Name)
	}
	// Capture into a staging dir outside the snapshots directory, then publish
	// atomically only on success, matching the Firecracker capture flow. A
	// failure at any step then leaves an existing snapshot at this tag
	// untouched — this is what makes re-snapshotting a tag safe.
	finalDir := vmkit.SnapshotDir(opts.StateDir, opts.Name, tag)
	stagingParent := vmkit.SnapshotStagingParent(opts.StateDir, opts.Name)
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	stagingDir, err := os.MkdirTemp(stagingParent, tag+"-*")
	if err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stagingDir)
		}
	}()
	req := vmkit.Request{
		Command: "snapshot",
		Identity: &vmkit.Identity{
			RequestID: NewRequestID(),
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   opts.Backend,
		},
		Config:             &vmkit.Config{StateDir: opts.StateDir},
		Tag:                tag,
		SnapshotStagingDir: stagingDir,
		RetainSecrets:      retainSecrets,
	}
	resp, err := Dispatch(ctx, opts, req)
	if err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	if err := writeAppleVFSnapshotArtifacts(stagingDir, tag, state, opts, resp.SecretsPurged, retainSecrets); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	if err := vmkit.PublishSnapshotDir(stagingDir, finalDir); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	published = true
	return vmkit.ReadSnapshotManifest(finalDir)
}

func writeAppleVFSnapshotArtifacts(dir, tag string, state RuntimeState, opts Options, purgeReport *bool, retainSecrets bool) error {
	for _, artifact := range []string{vmkit.SnapshotRootfsName, vmkit.SnapshotAppleVFMachineState} {
		if _, err := os.Stat(filepath.Join(dir, artifact)); err != nil {
			return fmt.Errorf("snapshot artifact %s: %w", artifact, err)
		}
	}
	if err := writeJSONFile(filepath.Join(dir, vmkit.SnapshotAppleVFConfig), state.Config); err != nil {
		return fmt.Errorf("write Apple VF snapshot restore config: %w", err)
	}
	manifest, err := appleVFSnapshotManifestFromState(tag, state, opts, purgeReport, retainSecrets)
	if err != nil {
		return err
	}
	return vmkit.WriteSnapshotManifest(dir, manifest)
}

func appleVFSnapshotManifestFromState(tag string, state RuntimeState, opts Options, purgeReport *bool, retainSecrets bool) (vmkit.SnapshotManifest, error) {
	// The manifest records the supervisor's own report of whether the purge ran,
	// not an assumption about its behavior. A supervisor that predates the
	// report always purges, so its silence is safe for an ordinary capture; for
	// a forensic capture silence would mean recording a purged image as
	// secret-bearing evidence, so fail instead.
	var purged bool
	switch {
	case purgeReport != nil:
		purged = *purgeReport
	case retainSecrets:
		return vmkit.SnapshotManifest{}, fmt.Errorf("forensic capture of workspace %s: supervisor did not report guest secret purge state; rebuild the apple-vf supervisor", opts.Name)
	default:
		purged = vmkit.MaterializedSecretsDeclared(&state.Config)
	}
	if err := vmkit.ValidateSnapshotSecretCapture(&state.Config, purged, retainSecrets); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	kernelSHA := ""
	if path := strings.TrimSpace(state.Config.KernelPath); path != "" {
		sha, err := fileSHA256(path)
		if err != nil {
			return vmkit.SnapshotManifest{}, fmt.Errorf("hash kernel for snapshot: %w", err)
		}
		kernelSHA = sha
	}
	mode, guestIP := "", ""
	netIP, netGateway, netSubnet := "", "", ""
	if state.Config.Network != nil {
		mode = strings.TrimSpace(state.Config.Network.Mode)
		guestIP = guestIPFromNetwork(*state.Config.Network)
		netIP = strings.TrimSpace(state.Config.Network.IP)
		netGateway = strings.TrimSpace(state.Config.Network.Gateway)
		netSubnet = strings.TrimSpace(state.Config.Network.Subnet)
	}
	// Only certificate-forging modes mint a per-workspace CA (broker splices
	// and delivers none), so only for those is the persisted CA required.
	caSHA := ""
	if vmkit.EgressModeForgesCerts(state.Config.EgressMode) && vmkit.NetworkModeMediates(mode) {
		sha, err := egressCACertSHA256(filepath.Join(opts.StateDir, opts.Name))
		if err != nil {
			return vmkit.SnapshotManifest{}, fmt.Errorf("snapshot of mediated workspace %s requires its persisted egress CA: %w", opts.Name, err)
		}
		caSHA = sha
	}
	// A fork's guest listens on the baked (ancestor-derived) service ports in
	// GuestShellPort/GuestExecPort; ShellPort/ExecPort are this workspace's
	// host-side bridge ports. The manifest records what the GUEST listens on.
	shellPort := state.Config.ShellPort
	if state.Config.GuestShellPort != 0 {
		shellPort = state.Config.GuestShellPort
	}
	execPort := state.Config.ExecPort
	if state.Config.GuestExecPort != 0 {
		execPort = state.Config.GuestExecPort
	}
	return vmkit.SnapshotManifest{
		Tag:                      tag,
		NetworkMode:              mode,
		GuestIP:                  guestIP,
		KernelSHA256:             kernelSHA,
		VCPUCount:                state.Config.CPUCount,
		MemoryMiB:                state.Config.MemoryMiB,
		CreatedAt:                time.Now().UTC().Format(time.RFC3339),
		ShellPort:                shellPort,
		ExecPort:                 execPort,
		NetworkIP:                netIP,
		NetworkGateway:           netGateway,
		NetworkSubnet:            netSubnet,
		RootfsArtifact:           vmkit.SnapshotRootfsName,
		MachineStateArtifacts:    vmkit.AppleVFSnapshotArtifacts(),
		SecretsMaterialized:      vmkit.MaterializedSecretsDeclared(&state.Config),
		SecretsPurged:            purged,
		EgressMode:               state.Config.EgressMode,
		EgressAllow:              state.Config.EgressAllow,
		EgressPassthrough:        state.Config.EgressPassthrough,
		EgressAllowlistLocked:    state.Config.EgressAllowlistLocked,
		EgressSwapConfigPath:     state.Config.EgressSwapConfigPath,
		EgressCASHA256:           caSHA,
		EgressMaxBytesPerSec:     state.Config.EgressMaxBytesPerSec,
		EgressMaxTotalBytes:      state.Config.EgressMaxTotalBytes,
		EgressMaxConcurrentConns: state.Config.EgressMaxConcurrentConns,
		EgressAuditMaxBytes:      state.Config.EgressAuditMaxBytes,
		EgressAuditMaxBackups:    state.Config.EgressAuditMaxBackups,
	}, nil
}

// SnapshotList returns the snapshots recorded for a workspace. It is a host-side
// read of the snapshot directory and needs no running VM.
func SnapshotList(opts Options) ([]vmkit.SnapshotInfo, error) {
	if err := ValidateName(opts.Name); err != nil {
		return nil, err
	}
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = StateDir()
	}
	return vmkit.ListSnapshots(stateDir, opts.Name)
}

// SnapshotRemove deletes a single snapshot tag. It is a host-side operation and
// needs no running VM.
func SnapshotRemove(opts Options, tag string) error {
	if err := ValidateName(opts.Name); err != nil {
		return err
	}
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = StateDir()
	}
	return vmkit.RemoveSnapshot(stateDir, opts.Name, tag)
}

// CreateFromSnapshot forks a new workspace from an existing workspace's
// snapshot. It provisions a fresh identity whose disk is a copy of the
// snapshot's rootfs, copies the snapshot into the new workspace so its restore
// path is self-contained, and resumes it from that snapshot.
//
// Networking scope (intentional, revisit if needed): every fork resumes a guest
// that keeps the snapshot's baked IP, so concurrent forks share one guest IP and
// each fork's host-side networking must be isolated. user-mode (pasta) gives
// each fork its own network namespace, so concurrent user-mode forks don't
// collide — this is the supported path for forking with networking, and it's
// what we validate. Firecracker "nat" mode runs tap+nftables in the shared host
// network namespace, so concurrent nat forks would collide on the duplicated
// guest IP/tap/rules; a nat fork is therefore single-instance and inherits nat's
// CAP_NET_ADMIN requirement. Per-fork network namespaces for nat are
// deliberately NOT built: it is a Linux/Firecracker-only edge case that user
// mode already covers (and on Apple VF "user" and "nat" are the same per-VM
// NAT), so it isn't worth the complexity now. It can be added if a concrete need
// for concurrent nat forks appears.
func CreateFromSnapshot(ctx context.Context, opts Options, sourceWorkspace, tag string) (Result, error) {
	if opts.Name == "" {
		return Result{}, fmt.Errorf("create requires a name")
	}
	if err := ValidateName(opts.Name); err != nil {
		return Result{}, err
	}
	if err := ValidateName(sourceWorkspace); err != nil {
		return Result{}, fmt.Errorf("invalid source workspace %q: %w", sourceWorkspace, err)
	}
	if strings.TrimSpace(tag) == "" {
		return Result{}, fmt.Errorf("snapshot tag is required")
	}
	if opts.StateDir == "" {
		opts.StateDir = StateDir()
	}
	forkBackend := opts.Backend
	if forkBackend == "" {
		forkBackend = HostBackend()
	}
	operation, _ := vmkit.OperationContractByID(vmkit.OperationSnapshotFork)
	if ready, _ := vmkit.BackendSupportsOperation(forkBackend, operation); !ready {
		return Result{}, vmkit.NewUnsupportedOperationError(forkBackend, operation, "snapshot fork (--from-snapshot)")
	}
	srcDir := vmkit.SnapshotDir(opts.StateDir, sourceWorkspace, tag)
	manifest, err := vmkit.ReadSnapshotManifest(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, fmt.Errorf("snapshot %q not found for workspace %s", tag, sourceWorkspace)
		}
		return Result{}, err
	}
	if manifest.SecretsMaterialized {
		sourceManifest, err := ReadManifest(opts.StateDir, sourceWorkspace)
		if err != nil {
			return Result{}, fmt.Errorf("read source workspace manifest for secret-bearing snapshot: %w", err)
		}
		if err := applyForkSecretManifest(&opts, sourceManifest, manifest); err != nil {
			return Result{}, err
		}
	}
	if manifest.MemoryMiB > 0 {
		opts.MemoryMiB = manifest.MemoryMiB
		opts.SpecMemory = true
	}
	if manifest.VCPUCount > 0 {
		opts.CPUCount = manifest.VCPUCount
		opts.SpecCPU = true
	}
	if strings.TrimSpace(manifest.NetworkMode) != "" {
		opts.Network = adoptSnapshotNetwork(opts.Network, manifest)
	}
	if opts.ImageRef == "" {
		opts.ImageRef = manifest.ImageRef
	}
	// The resumed guest listens on the source's baked vsock service ports. The
	// fork keeps its own unique host ports (name-derived) and bridges them to
	// the source's guest ports, so concurrent forks don't collide on the host.
	opts.GuestShellPort = manifest.ShellPort
	opts.GuestExecPort = manifest.ExecPort
	// Likewise the loaded VM state references the manifest's vsock path, not
	// the fork's own; carry it so a snapshot OF this fork records the truth.
	opts.BakedVsockUDSPath = manifest.VsockUDSPath
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return Result{}, err
	}
	if err := EnsureKernel(ctx, &opts); err != nil {
		return Result{}, err
	}
	if err := EnsureCanCreate(opts); err != nil {
		return Result{}, err
	}
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o700); err != nil {
		return Result{}, err
	}
	if err := CopyFile(filepath.Join(srcDir, vmkit.SnapshotRootfsArtifact(manifest)), rootfsPath, 0o600); err != nil {
		return Result{}, fmt.Errorf("copy snapshot rootfs into fork: %w", err)
	}
	if err := WriteManifest(opts); err != nil {
		return Result{}, err
	}
	if err := copySnapshotInto(srcDir, vmkit.SnapshotDir(opts.StateDir, opts.Name, tag), manifest); err != nil {
		return Result{}, err
	}
	// A mediated source baked its per-workspace egress CA into the guest's trust
	// store. The fork resumes that exact guest, so it must re-arm the mediator with
	// the SAME CA — the fork's restore path reuses the persisted CA from its own
	// workspace dir and fails closed if it is absent. The CA lives in the source
	// workspace dir (not the snapshot), so copy it into the fork's workspace dir.
	// Keyed on the snapshot's recorded egress posture so a non-mediated source
	// stays untouched.
	if manifest.EgressCASHA256 != "" {
		if err := copyForkEgressCA(opts.StateDir, sourceWorkspace, opts.Name); err != nil {
			return Result{}, err
		}
	}
	opts.FromSnapshot = tag
	return Start(ctx, opts)
}

// adoptSnapshotIdentity defaults the baked identity fields from a snapshot
// manifest onto opts: the guest service ports the resumed guest listens on
// and the vsock UDS path its VM state references. For an original (non-fork)
// workspace these equal the workspace's own values, so adoption is a no-op
// in behavior; for a fork they differ and are load-bearing. Explicit caller
// values win.
func adoptSnapshotIdentity(opts *Options, manifest vmkit.SnapshotManifest) {
	if opts.GuestShellPort == 0 {
		opts.GuestShellPort = manifest.ShellPort
	}
	if opts.GuestExecPort == 0 {
		opts.GuestExecPort = manifest.ExecPort
	}
	if strings.TrimSpace(opts.BakedVsockUDSPath) == "" {
		opts.BakedVsockUDSPath = manifest.VsockUDSPath
	}
}

// adoptSnapshotNetwork builds the fork's network config: addressing comes
// from the snapshot — the resumed guest keeps the source's baked IP, so the
// fork configures its own tap/pasta (in its own namespace) with the source's
// addressing rather than deriving a fresh subnet from the fork's name. The
// caller's port forwards are preserved: they are realized host-side by this
// fork's own pasta/forwarder and are invisible to the resumed guest, so
// adopting the source's addressing must not silently drop them.
func adoptSnapshotNetwork(requested vmkit.NetworkConfig, manifest vmkit.SnapshotManifest) vmkit.NetworkConfig {
	return vmkit.NetworkConfig{
		Mode:         manifest.NetworkMode,
		IP:           manifest.NetworkIP,
		Gateway:      manifest.NetworkGateway,
		Subnet:       manifest.NetworkSubnet,
		PortForwards: requested.PortForwards,
	}
}

func applyForkSecretManifest(opts *Options, source Manifest, snapshot vmkit.SnapshotManifest) error {
	if !snapshot.SecretsMaterialized {
		return nil
	}
	if len(source.Secrets) == 0 && len(source.SecretEnvFiles) == 0 {
		return fmt.Errorf("snapshot %q requires source materialized secret references for fork rehydrate", snapshot.Tag)
	}
	opts.Secrets = make(map[string]string, len(source.Secrets))
	for _, ref := range source.Secrets {
		opts.Secrets[ref.Name] = ref.Ref
	}
	opts.SecretEnvFiles = append([]string(nil), source.SecretEnvFiles...)
	if len(source.OnDemandSecrets) > 0 {
		opts.OnDemandSecrets = make(map[string]string, len(source.OnDemandSecrets))
		for _, ref := range source.OnDemandSecrets {
			opts.OnDemandSecrets[ref.Name] = ref.Ref
		}
	}
	opts.SecretsAudit = source.SecretsAudit
	return nil
}

// copyForkEgressCA copies the source workspace's persisted egress CA cert and key
// into the fork's workspace dir so the fork's restore path can reuse them (the
// guest's baked trust store anchors on this CA). It fails closed if the source CA
// is missing — a mediated fork must not boot with a re-minted or absent CA. The
// fingerprint match against the snapshot manifest is enforced later by the
// supervisor's acquireEgressCA on the restore path.
func copyForkEgressCA(stateDir, sourceWorkspace, forkName string) error {
	srcWsDir := filepath.Join(stateDir, sourceWorkspace)
	dstWsDir := filepath.Join(stateDir, forkName)
	if err := os.MkdirAll(dstWsDir, 0o700); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		mode os.FileMode
	}{
		{"egress-ca.pem", 0o644},
		{"egress-ca-key.pem", 0o600},
	} {
		if err := CopyFile(filepath.Join(srcWsDir, f.name), filepath.Join(dstWsDir, f.name), f.mode); err != nil {
			return fmt.Errorf("copy source egress CA %s into fork: %w", f.name, err)
		}
	}
	return nil
}

func prepareAppleVFSnapshotRestore(opts Options, req vmkit.Request) error {
	if req.Config == nil {
		return fmt.Errorf("apple-vf snapshot restore requires a VM config")
	}
	dir := vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag)
	manifest, err := vmkit.ReadSnapshotManifest(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot %q not found for workspace %s", req.Tag, opts.Name)
		}
		return err
	}
	if manifest.KernelSHA256 != "" {
		sha, err := fileSHA256(req.Config.KernelPath)
		if err != nil {
			return fmt.Errorf("hash kernel for snapshot restore: %w", err)
		}
		if sha != manifest.KernelSHA256 {
			return fmt.Errorf("snapshot %q was taken against kernel sha256 %s but the workspace kernel is %s; refusing to load", req.Tag, manifest.KernelSHA256, sha)
		}
	}
	if err := vmkit.ValidateSnapshotSecretRestore(manifest, req.Config); err != nil {
		return err
	}
	if err := verifySnapshotEgressCA(opts.StateDir, opts.Name, manifest); err != nil {
		return err
	}
	if err := applyAppleVFRestoreConfig(dir, req.Config); err != nil {
		return err
	}
	applySnapshotEgressCaps(req.Config, manifest)
	if err := copyFileReplace(filepath.Join(dir, vmkit.SnapshotRootfsArtifact(manifest)), req.Config.RootfsPath, 0o600); err != nil {
		return fmt.Errorf("restore snapshot rootfs: %w", err)
	}
	return nil
}

func applyAppleVFRestoreConfig(snapshotDir string, config *vmkit.Config) error {
	data, err := os.ReadFile(filepath.Join(snapshotDir, vmkit.SnapshotAppleVFConfig))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read Apple VF snapshot restore config: %w", err)
	}
	var saved vmkit.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("decode Apple VF snapshot restore config: %w", err)
	}
	kernelPath := config.KernelPath
	rootfsPath := config.RootfsPath
	stateDir := config.StateDir
	vsockListeners := config.VsockListeners
	identityShellPort := config.ShellPort
	identityExecPort := config.ExecPort
	guestShellPort := config.GuestShellPort
	guestExecPort := config.GuestExecPort
	saved.KernelPath = kernelPath
	saved.RootfsPath = rootfsPath
	saved.StateDir = stateDir
	saved.VsockListeners = vsockListeners
	if guestShellPort != 0 {
		saved.GuestShellPort = guestShellPort
		saved.ShellPort = identityShellPort
	}
	if guestExecPort != 0 {
		saved.GuestExecPort = guestExecPort
		saved.ExecPort = identityExecPort
	}
	*config = saved
	return nil
}

func applySnapshotEgressCaps(config *vmkit.Config, manifest vmkit.SnapshotManifest) {
	if config == nil {
		return
	}
	config.EgressMaxBytesPerSec = manifest.EgressMaxBytesPerSec
	config.EgressMaxTotalBytes = manifest.EgressMaxTotalBytes
	config.EgressMaxConcurrentConns = manifest.EgressMaxConcurrentConns
	config.EgressAuditMaxBytes = manifest.EgressAuditMaxBytes
	config.EgressAuditMaxBackups = manifest.EgressAuditMaxBackups
}

func verifySnapshotEgressCA(stateDir, workspace string, manifest vmkit.SnapshotManifest) error {
	if manifest.EgressCASHA256 == "" {
		return nil
	}
	got, err := egressCACertSHA256(filepath.Join(stateDir, workspace))
	if err != nil {
		return fmt.Errorf("snapshot restore of mediated workspace %s requires its persisted egress CA: %w", workspace, err)
	}
	if got != manifest.EgressCASHA256 {
		return fmt.Errorf("egress CA fingerprint %s does not match snapshot fingerprint %s; refusing restore", got, manifest.EgressCASHA256)
	}
	return nil
}

func copySnapshotInto(srcDir, dstDir string, manifest vmkit.SnapshotManifest) error {
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return err
	}
	names := []string{vmkit.SnapshotRootfsArtifact(manifest), vmkit.SnapshotManifestName}
	for _, artifact := range vmkit.SnapshotMachineStateArtifacts(manifest) {
		if artifact.Path != "" {
			names = append(names, artifact.Path)
		}
	}
	for _, name := range names {
		if err := CopyFile(filepath.Join(srcDir, name), filepath.Join(dstDir, name), 0o644); err != nil {
			return fmt.Errorf("copy snapshot %s into fork: %w", name, err)
		}
	}
	return nil
}

func BuildRootfs(ctx context.Context, opts Options) (Result, error) {
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)

	// Fast path: for a plain workspace, clone a previously pulled/tagged baseline
	// rootfs instead of pulling and rebuilding. The resolver is injected by the
	// CLI (which owns the image cache) so pkg/workspace does not depend on
	// pkg/imagecache; it returns ok=false when there is no reusable baseline.
	if opts.RootfsBaseline != nil && canReuseRootfsBaseline(opts) {
		if baseline, prov, ok := opts.RootfsBaseline(rootfsPath); ok {
			if err := CopyFile(baseline, rootfsPath, 0o644); err != nil {
				return Result{}, err
			}
			return buildRootfsResult(opts, rootfsPath, prov), nil
		}
	}

	req, err := rootfsRequest(opts, rootfsPath)
	if err != nil {
		return Result{}, err
	}
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	if builtMiB := provenance.SizeBytes / (1024 * 1024); builtMiB > opts.SizeMiB {
		opts.SizeMiB = builtMiB
	}
	return buildRootfsResult(opts, rootfsPath, provenance), err
}

func buildRootfsResult(opts Options, rootfsPath string, image rootfs.Provenance) Result {
	return Result{
		Workspace:    opts.Name,
		StateDir:     opts.StateDir,
		Profile:      opts.Profile,
		Restart:      opts.RestartPolicy,
		Resources:    ResourcesFromOptions(opts),
		Network:      NetworkSpecFromConfig(opts.Network),
		Service:      strings.TrimSpace(opts.ServiceCommand),
		ConsoleShell: strings.TrimSpace(opts.ConsoleShell),
		Hostname:     strings.TrimSpace(opts.Hostname),
		RootfsPath:   rootfsPath,
		KernelPath:   opts.KernelPath,
		Artifacts:    ArtifactsFromOptions(opts),
		Image:        image,
	}
}

// canReuseRootfsBaseline reports whether the workspace's rootfs would be identical
// to a plain pulled/tagged image baseline — i.e. nothing bakes workspace-specific
// content into it. Only then is cloning a baseline safe instead of building.
func canReuseRootfsBaseline(opts Options) bool {
	return opts.PrepareForStart &&
		!HasGuestCommand(opts) &&
		strings.TrimSpace(opts.ConsoleShell) == "" &&
		strings.TrimSpace(opts.Hostname) == "" &&
		len(opts.Files) == 0 &&
		len(opts.Disks) == 0 &&
		len(opts.Env) == 0 &&
		len(opts.Network.PortForwards) == 0
}

// rootfsRequest composes the rootfs build request, baking the broker guest
// env (vsock bridge, proxy, base URLs) into the image env when a broker is
// configured. Fail-closed: an invalid broker config fails the build rather
// than producing a workspace whose egress silently bypasses the broker.
func rootfsRequest(opts Options, rootfsPath string) (rootfs.BuildRequest, error) {
	req := buildRootfsRequest(opts, rootfsPath)
	brokers, err := normalizeEffectiveBrokers(opts)
	if err != nil {
		return rootfs.BuildRequest{}, err
	}
	for _, bc := range brokers {
		guest := broker.GuestConfig{
			GuestListen: bc.GuestListen,
			VsockPort:   bc.VsockPort,
			Proxy:       bc.Proxy,
			BaseURL:     bc.BaseURLEnv,
		}
		env, err := guest.MergeGuestEnvMap(req.Env)
		if err != nil {
			return rootfs.BuildRequest{}, fmt.Errorf("broker guest env: %w", err)
		}
		req.Env = env
	}
	return req, nil
}

// localImageLayoutPath returns the committed-OCI layout path for stateDir.
// This mirrors commit.LayoutPath without importing pkg/commit: pkg/commit
// already imports pkg/workspace, so importing it back here would create an
// import cycle.
func localImageLayoutPath(stateDir string) string {
	return filepath.Join(stateDir, "images", "oci")
}

func buildRootfsRequest(opts Options, rootfsPath string) rootfs.BuildRequest {
	command, resultPort := BuildCommandAndPort(opts)
	mode := ""
	if opts.PrepareForStart && opts.UseImageCommand {
		mode = "service"
	} else if opts.PrepareForStart && strings.TrimSpace(opts.ServiceCommand) != "" && !HasSetupCommand(opts) && strings.TrimSpace(opts.ExecCommand) == "" {
		mode = "managed-service"
	}
	finalCommand, finalMode, resetFinal := FinalCommandAndMode(opts)
	return rootfs.BuildRequest{
		ImageRef:         opts.ImageRef,
		Platform:         rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:       rootfsPath,
		Format:           WorkspaceRootfsFormat(opts.Backend),
		InitPath:         rootfs.DefaultInitPath,
		Command:          command,
		Mode:             mode,
		ConsoleShell:     opts.ConsoleShell,
		Hostname:         opts.Hostname,
		ShellPort:        ShellPort(opts),
		ExecPort:         ExecPort(opts),
		InitBinaryPath:   opts.GuestInitPath,
		ResultPort:       resultPort,
		NoImageCommand:   opts.PrepareForStart && !HasGuestCommand(opts) && !opts.UseImageCommand,
		StateDir:         filepath.Join(opts.StateDir, "build"),
		LocalImageLayout: localImageLayoutPath(opts.StateDir),
		Mke2fsPath:       opts.Mke2fsPath,
		SizeMiB:          opts.SizeMiB,
		AutoSize:         !opts.SizeExplicit && !opts.SpecSize,
		Env:              opts.Env,
		Files:            RootfsFiles(opts.Files),
		Mounts:           MountsForBackend(opts.Backend, opts.Disks),
		HostForwards:     RootfsPortForwards(opts.Network.PortForwards),
		AllowMutable:     true,
		Progress:         opts.Progress,
		ResetFinalConfig: resetFinal,
		FinalCommand:     finalCommand,
		FinalMode:        finalMode,
	}
}

func WorkspaceRootfsFormat(backend string) string {
	return rootfs.FormatExt4
}

func WorkspaceDiskFormat(backend string) string {
	return WorkspaceRootfsFormat(backend)
}

func WorkspaceRootfsFilename(backend string) string {
	return "rootfs.ext4"
}

func WorkspaceDiskFilename(backend, name string) string {
	return name + ".ext4"
}

func WorkspaceDiskPath(stateDir, workspaceName, backend, diskName string) string {
	return filepath.Join(stateDir, "workspaces", workspaceName, "disks", WorkspaceDiskFilename(backend, diskName))
}

func WorkspaceRootfsPath(stateDir, name, backend string) string {
	return filepath.Join(stateDir, "workspaces", name, WorkspaceRootfsFilename(backend))
}

func CandidateWorkspaceRootfsPaths(stateDir, name, backend string) []string {
	primary := WorkspaceRootfsPath(stateDir, name, backend)
	secondary := WorkspaceRootfsPath(stateDir, name, "")
	if primary == secondary {
		return []string{primary}
	}
	return []string{primary, secondary}
}

// volumeHolderActive reports whether a workspace still counts as holding a
// named volume — i.e. it is in a state where the VM could be using the disk.
// A stopped, halted, failed, or absent workspace is reclaimable.
func volumeHolderActive(stateDir string) func(string) bool {
	return func(name string) bool {
		event, err := ReadEvent(Options{StateDir: stateDir, Name: name})
		if err != nil {
			return false
		}
		switch event.State {
		case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined:
			return true
		default:
			return false
		}
	}
}

func PrepareDisks(ctx context.Context, opts Options) ([]Disk, error) {
	if len(opts.Disks) == 0 {
		return nil, nil
	}
	disks := make([]Disk, 0, len(opts.Disks))
	seenNames := map[string]bool{}
	seenMountpoints := map[string]bool{}
	for _, disk := range opts.Disks {
		if disk.ManagedVolume {
			path, err := volume.Path(opts.StateDir, opts.Backend, disk.Name)
			if err != nil {
				return nil, err
			}
			if _, err := volume.Attach(opts.StateDir, disk.Name, opts.Name, volumeHolderActive(opts.StateDir)); err != nil {
				return nil, err
			}
			disk.SourcePath = path
			disk.Path = path
			disk.Bundle = false
			disk.ManagedVolume = false
		}
		if err := ValidateDisk(disk); err != nil {
			return nil, err
		}
		if seenNames[disk.Name] {
			return nil, fmt.Errorf("duplicate disk name %q", disk.Name)
		}
		seenNames[disk.Name] = true
		if seenMountpoints[disk.Mountpoint] {
			return nil, fmt.Errorf("duplicate disk mountpoint %q", disk.Mountpoint)
		}
		seenMountpoints[disk.Mountpoint] = true
		if disk.Bundle {
			outputPath := WorkspaceDiskPath(opts.StateDir, opts.Name, opts.Backend, disk.Name)
			_, err := rootfs.NewBuilder().BuildBundle(ctx, rootfs.BundleRequest{
				SourcePath: disk.SourcePath,
				OutputPath: outputPath,
				Format:     WorkspaceDiskFormat(opts.Backend),
				StateDir:   filepath.Join(opts.StateDir, "build"),
				Mke2fsPath: opts.Mke2fsPath,
				SizeMiB:    64,
				AutoSize:   true,
			})
			if err != nil {
				return nil, err
			}
			disk.Path = outputPath
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

// materializeCredSwapConfig resolves opts.CredSwapProviders into a generated
// credential-swap config file and wires it into the egress fields. It is the
// library-side realization of the `--cred-swap PROVIDER` surface: for each
// provider it (1) unions the provider's egress host(s) into EgressAllow so the
// mediator permits the connection and (2) builds a static swap entry. The
// entries are merged with any operator-supplied EgressSwapConfigPath (union by
// name; a name collision is an error so nothing is silently overwritten),
// marshaled to YAML, and written to a durable per-workspace path
// (<StateDir>/workspaces/<name>/cred-swap.yaml) which becomes the new
// EgressSwapConfigPath. The path must be durable, not process-tied: it is
// persisted in the manifest and snapshot manifest and re-read by the mediator
// on restart/restore.
//
// Only references (env:/file:/vault:) are written — never the secret value. The
// mediator resolves the reference host-side at request time, so the guest never
// holds the key. This is a no-op when no providers are declared.
func materializeCredSwapConfig(opts *Options) error {
	if len(opts.CredSwapProviders) == 0 {
		return nil
	}
	// cred-swap is performed by the egress mediator (host-side MITM injection),
	// which only runs in mitm mode. With egress off there is no mediator to
	// inject the key, so the swap would silently do nothing — fail loud. This is
	// the library backstop for direct Go-API callers; the CLI catches it earlier.
	if vmkit.ResolveEgressModeDefault(opts.EgressMode) == vmkit.EgressModeOff {
		return fmt.Errorf("cred-swap: credential swap requires egress mitm, not off")
	}
	cfg := egress.SwapConfigFile{Swaps: map[string]egress.SwapEntry{}}
	// Merge an operator-supplied swap config first so generated provider entries
	// are added on top of it (collision below catches an overlapping name).
	if existing := strings.TrimSpace(opts.EgressSwapConfigPath); existing != "" {
		data, err := os.ReadFile(existing)
		if err != nil {
			return fmt.Errorf("cred-swap: read --egress-swap-config %q: %w", existing, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("cred-swap: parse --egress-swap-config %q: %w", existing, err)
		}
		if cfg.Swaps == nil {
			cfg.Swaps = map[string]egress.SwapEntry{}
		}
	}
	var hosts []string
	for _, p := range opts.CredSwapProviders {
		entry, entryHosts, err := egress.ProviderSwapEntry(p.Provider, p.Ref)
		if err != nil {
			return err
		}
		name := strings.ToLower(strings.TrimSpace(p.Provider))
		if _, exists := cfg.Swaps[name]; exists {
			return fmt.Errorf("cred-swap: swap entry %q already defined (collides with an --egress-swap-config entry or a repeated --cred-swap)", name)
		}
		cfg.Swaps[name] = entry
		hosts = append(hosts, entryHosts...)
	}
	// The guest must be allowed to reach the provider host for the injected
	// credential to matter; union into the allowlist (dedupe with what's already
	// there) before the egress policy is built.
	opts.EgressAllow = egress.DedupeHosts(append(append([]string(nil), opts.EgressAllow...), hosts...))

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("cred-swap: marshal config: %w", err)
	}
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return err
	}
	outPath := filepath.Join(workspaceDir, "cred-swap.yaml")
	if err := os.WriteFile(outPath, data, 0o600); err != nil {
		return fmt.Errorf("cred-swap: write %q: %w", outPath, err)
	}
	opts.EgressSwapConfigPath = outPath
	return nil
}

func WriteManifest(opts Options) error {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(workspaceDir, "workspace.json"), Manifest{
		Name:                  opts.Name,
		Profile:               opts.Profile,
		Restart:               NormalizeRestartPolicy(opts.RestartPolicy),
		Resources:             ResourcesFromOptions(opts),
		Network:               NetworkSpecFromConfig(opts.Network),
		Service:               strings.TrimSpace(opts.ServiceCommand),
		ConsoleShell:          strings.TrimSpace(opts.ConsoleShell),
		Hostname:              strings.TrimSpace(opts.Hostname),
		Model:                 strings.TrimSpace(opts.Model),
		ModelRunner:           modelRunnerManifest(opts.ModelRunner),
		ModelMediation:        modelMediationManifest(opts.ModelMediation),
		Mediation:             opts.Mediation,
		Health:                healthManifest(opts.Health),
		Disks:                 opts.Disks,
		Artifacts:             ArtifactsFromOptions(opts),
		Verification:          opts.Verification,
		Secrets:               secretRefsFromOptions(opts),
		SecretEnvFiles:        opts.SecretEnvFiles,
		OnDemandSecrets:       onDemandRefsFromOptions(opts),
		SecretsAudit:          opts.SecretsAudit,
		EgressMode:            opts.EgressMode,
		EgressAllow:           opts.EgressAllow,
		EgressPassthrough:     opts.EgressPassthrough,
		EgressAllowlistLocked: opts.EgressAllowlistLocked,
		EgressSwapConfigPath:  opts.EgressSwapConfigPath,
		Broker:                opts.Broker,
		Brokers:               opts.Brokers,
	})
}

func modelRunnerManifest(spec ModelRunnerSpec) *ModelRunnerSpec {
	if !modelRunnerSpecDeclared(spec) {
		return nil
	}
	spec.Env = nil
	return &spec
}

func modelMediationManifest(spec ModelMediationSpec) *ModelMediationSpec {
	if !modelMediationSpecDeclared(spec) {
		return nil
	}
	return &spec
}

func ReadManifest(stateDir, name string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "workspaces", name, "workspace.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ReadRuntimeState(opts Options) (RuntimeState, error) {
	var state RuntimeState
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "runtime.json"))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func ReadEvent(opts Options) (EventFile, error) {
	var event EventFile
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "event.json"))
	if err != nil {
		return event, err
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return event, err
	}
	return event, nil
}

func ReadGuestResult(opts Options) (GuestResult, error) {
	var result GuestResult
	data, err := os.ReadFile(ResultPath(opts.StateDir, opts.Name))
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}

func ReadRuntimeResult(opts Options, identity vmkit.Identity) (vmkit.RuntimeResult, error) {
	guest, err := ReadGuestResult(opts)
	if err != nil {
		return vmkit.RuntimeResult{}, err
	}
	backend := opts.Backend
	if backend == "" {
		backend = identity.Backend
	}
	return vmkit.RuntimeResult{
		Identity:    identity,
		Backend:     backend,
		ResultPath:  ResultPath(opts.StateDir, opts.Name),
		StartedAt:   guest.StartedAt,
		CompletedAt: guest.ExitedAt,
		ExitCode:    guest.ExitCode,
		Stdout:      guest.Stdout,
		Stderr:      guest.Stderr,
		Error:       guest.Error,
	}, nil
}

func BuildVerification(opts Options, result Result) (vmkit.RuntimeVerification, error) {
	verification := vmkit.RuntimeVerification{
		OK:          true,
		ImageRef:    result.Image.ImageRef,
		ResolvedRef: result.Image.ResolvedRef,
		ImageDigest: result.Image.Digest,
		Kernel:      recordedArtifact(opts.KernelPath),
		Rootfs:      recordedArtifact(result.RootfsPath),
	}
	if opts.GuestInitPath != "" {
		if info, err := os.Stat(opts.GuestInitPath); err == nil && !info.IsDir() {
			verification.Init = recordedArtifact(opts.GuestInitPath)
		}
	}
	for _, artifact := range []struct {
		name     string
		artifact *vmkit.VerifiedArtifact
	}{
		{name: "kernel", artifact: verification.Kernel},
		{name: "rootfs", artifact: verification.Rootfs},
		{name: "init", artifact: verification.Init},
	} {
		if artifact.artifact != nil && artifact.artifact.Error != "" {
			verification.OK = false
			verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{
				Artifact: artifact.name,
				Error:    artifact.artifact.Error,
			})
		}
	}
	if !verification.OK {
		return verification, fmt.Errorf("record workspace verification: %s", verification.Divergence[0].Error)
	}
	return verification, nil
}

func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return operation.New(operation.ErrorValidation, "workspace name is required")
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return operation.New(operation.ErrorValidation, "invalid workspace name: %s", name)
	}
	return nil
}

func DefaultHostname(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastHyphen := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	if out == "" {
		return "microagent"
	}
	return out
}

func ValidateHostname(hostname string) error {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	if len(hostname) > 63 {
		return fmt.Errorf("hostname must be 63 characters or fewer")
	}
	if hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return fmt.Errorf("hostname must not start or end with '-'")
	}
	for _, r := range hostname {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return fmt.Errorf("hostname must contain only letters, numbers, and '-'")
	}
	return nil
}

func ValidateDisk(disk Disk) error {
	if strings.TrimSpace(disk.Name) == "" {
		return fmt.Errorf("disk name is required")
	}
	if disk.Name == "rootfs" {
		return fmt.Errorf("disk name rootfs is reserved")
	}
	path := disk.Path
	if disk.Bundle {
		path = disk.SourcePath
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("disk %q path is required", disk.Name)
	}
	if strings.TrimSpace(disk.Mountpoint) == "" {
		return fmt.Errorf("disk %q mountpoint is required", disk.Name)
	}
	if !strings.HasPrefix(disk.Mountpoint, "/") {
		return fmt.Errorf("disk %q mountpoint must be absolute", disk.Name)
	}
	if disk.Mode != "ro" && disk.Mode != "rw" {
		return fmt.Errorf("disk %q mode must be ro or rw", disk.Name)
	}
	return nil
}

func ValidateOutput(output Output) error {
	if strings.TrimSpace(output.Name) == "" {
		return fmt.Errorf("output name is required")
	}
	if strings.TrimSpace(output.Path) == "" {
		return fmt.Errorf("output %q path is required", output.Name)
	}
	if !strings.HasPrefix(output.Path, "/") {
		return fmt.Errorf("output %q path must be absolute", output.Name)
	}
	return nil
}

func EnsureCanStart(stateDir, name string) error {
	state, pid, err := LatestStartState(stateDir, name)
	if err != nil {
		return err
	}
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed:
		return nil
	case vmkit.StateQuarantined:
		if pid > 0 {
			return operation.New(operation.ErrorConflict, "workspace %s is quarantined with preserved pid %d; halt, stop, or kill it before start", name, pid)
		}
		return operation.New(operation.ErrorConflict, "workspace %s is quarantined; halt, stop, or kill it before start", name)
	case vmkit.StateStarting, vmkit.StateRunning:
		return operation.New(operation.ErrorConflict, "workspace %s is already %s", name, state)
	default:
		return operation.New(operation.ErrorConflict, "workspace %s cannot start from state %s", name, state)
	}
}

func LatestStartState(stateDir, name string) (vmkit.VMState, int, error) {
	state, err := ReadRuntimeState(Options{StateDir: stateDir, Name: name})
	if err == nil {
		return state.Event.State, state.PID, nil
	}
	if !os.IsNotExist(err) {
		return "", 0, err
	}
	event, eventErr := ReadEvent(Options{StateDir: stateDir, Name: name})
	if eventErr == nil {
		return event.State, 0, nil
	}
	if os.IsNotExist(eventErr) {
		return "", 0, nil
	}
	return "", 0, eventErr
}

func SerialLogPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "serial.log")
}

func SerialInputPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "serial.in")
}

func Cleanup(stateDir, name string) {
	if ValidateName(name) != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(stateDir, "workspaces", name))
	_ = os.RemoveAll(filepath.Join(stateDir, name))
}

func NewRequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func normalizeLifecycleOptions(opts *Options, requireDisk bool) error {
	defaults := DefaultOptions()
	if opts.Backend == "" {
		opts.Backend = defaults.Backend
	}
	if err := ValidateHostBackend(opts.Backend); err != nil {
		return err
	}
	if opts.Architecture == "" {
		opts.Architecture = defaults.Architecture
	}
	opts.Architecture = NormalizeArch(opts.Architecture)
	if opts.Profile == "" {
		opts.Profile = defaults.Profile
	}
	if opts.RestartPolicy == "" {
		opts.RestartPolicy = defaults.RestartPolicy
	}
	if opts.Network.Mode == "" {
		opts.Network = defaults.Network
	}
	if opts.StateDir == "" {
		opts.StateDir = defaults.StateDir
	}
	if opts.KernelPath == "" {
		opts.KernelPath = KernelPath(opts.Backend, opts.Architecture)
	}
	if opts.GuestInitPath == "" {
		opts.GuestInitPath = GuestInitPath(opts.Architecture)
	}
	if opts.Mke2fsPath == "" {
		opts.Mke2fsPath = Mke2fsPath()
	}
	if opts.ResultPort == 0 && (opts.ExecCommand != "" || len(opts.SetupCommands) != 0 || opts.UseImageCommand) {
		opts.ResultPort = DefaultResultPort
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if err := ValidateRestartPolicy(opts.RestartPolicy); err != nil {
		return err
	}
	opts.RestartPolicy = NormalizeRestartPolicy(opts.RestartPolicy)
	opts.Network = NormalizeNetworkConfig(opts.Network)
	if err := vmkit.ValidateNetworkConfig(opts.Network); err != nil {
		return err
	}
	if strings.TrimSpace(opts.Hostname) == "" {
		opts.Hostname = DefaultHostname(opts.Name)
	}
	if err := ValidateHostname(opts.Hostname); err != nil {
		return err
	}
	opts.SerialInput = BackendSupportsConsoleInput(opts.Backend)
	if opts.MemoryMiB == 0 || opts.CPUCount == 0 || (requireDisk && opts.SizeMiB == 0) {
		if err := ApplyProfile(opts, opts.MemoryMiB != 0, opts.CPUCount != 0, opts.SizeMiB != 0); err != nil {
			return err
		}
	}
	return ValidateResources(ResourcesFromOptions(*opts), requireDisk)
}

func applyManifest(opts *Options, manifest Manifest) {
	if manifest.Profile != "" {
		opts.Profile = manifest.Profile
	}
	opts.RestartPolicy = NormalizeRestartPolicy(manifest.Restart)
	if manifest.Network.Mode != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" || manifest.Network.Subnet != "" || manifest.Network.Gateway != "" {
		opts.Network = NetworkConfigFromSpec(manifest.Network)
	}
	if strings.TrimSpace(manifest.ConsoleShell) != "" {
		opts.ConsoleShell = strings.TrimSpace(manifest.ConsoleShell)
	}
	if strings.TrimSpace(manifest.Service) != "" {
		opts.ServiceCommand = strings.TrimSpace(manifest.Service)
	}
	if strings.TrimSpace(manifest.Hostname) != "" {
		opts.Hostname = strings.TrimSpace(manifest.Hostname)
	}
	opts.Model = strings.TrimSpace(manifest.Model)
	if manifest.ModelRunner != nil {
		opts.ModelRunner = *manifest.ModelRunner
	} else {
		opts.ModelRunner = ModelRunnerSpec{}
	}
	if manifest.ModelMediation != nil {
		opts.ModelMediation = *manifest.ModelMediation
	} else {
		opts.ModelMediation = ModelMediationSpec{}
	}
	if manifest.Resources.MemoryMiB != 0 {
		opts.MemoryMiB = manifest.Resources.MemoryMiB
	}
	if manifest.Resources.CPUCount != 0 {
		opts.CPUCount = manifest.Resources.CPUCount
	}
	if manifest.Resources.SizeMiB != 0 {
		opts.SizeMiB = manifest.Resources.SizeMiB
	}
	opts.Disks = manifest.Disks
	opts.Mediation = manifest.Mediation
	if len(manifest.Secrets) > 0 {
		opts.Secrets = make(map[string]string, len(manifest.Secrets))
		for _, ref := range manifest.Secrets {
			opts.Secrets[ref.Name] = ref.Ref
		}
	} else {
		opts.Secrets = nil
	}
	opts.SecretEnvFiles = manifest.SecretEnvFiles
	if len(manifest.OnDemandSecrets) > 0 {
		opts.OnDemandSecrets = make(map[string]string, len(manifest.OnDemandSecrets))
		for _, ref := range manifest.OnDemandSecrets {
			opts.OnDemandSecrets[ref.Name] = ref.Ref
		}
	} else {
		opts.OnDemandSecrets = nil
	}
	opts.SecretsAudit = manifest.SecretsAudit
	// Resolve the manifest egress mode's default (empty -> broker) without
	// validating, so a workspace whose manifest carries an unspecified mode
	// starts under broker; a retired mode survives to be rejected at Request()'s
	// policy chokepoint. Request() then re-allocates the CA-cert vsock listener
	// (mitm only) on start, mirroring create.
	opts.EgressMode = vmkit.ResolveEgressModeDefault(manifest.EgressMode)
	opts.EgressAllow = manifest.EgressAllow
	opts.EgressPassthrough = manifest.EgressPassthrough
	opts.EgressAllowlistLocked = manifest.EgressAllowlistLocked
	opts.EgressSwapConfigPath = manifest.EgressSwapConfigPath
	opts.Broker = manifest.Broker
	opts.Brokers = manifest.Brokers
}

func runForeground(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	resp, err := Dispatch(ctx, opts, req)
	state := vmkit.StateStopped
	errorText := ""
	if backendOwnsRuntimeState(opts.Backend) {
		return resp, err
	}
	if err != nil || !resp.OK {
		state = vmkit.StateFailed
		errorText = resp.Error
		if errorText == "" && err != nil {
			errorText = err.Error()
		}
	}
	if stateErr := WriteProcessState(opts, req, state, 0, errorText); stateErr != nil && err == nil {
		return resp, stateErr
	}
	return resp, err
}

func backendOwnsRuntimeState(backend string) bool {
	return vmkit.BackendCapabilities(backend).OwnsRuntimeState
}

func startDetached(opts Options, req vmkit.Request) (vmkit.Response, error) {
	if command := detachedSupervisorCommand(opts.Backend); command != "run" {
		req.Command = command
		dispatchCtx := context.Background()
		var cancel context.CancelFunc
		if opts.Timeout > 0 {
			dispatchCtx, cancel = context.WithTimeout(dispatchCtx, opts.Timeout)
			defer cancel()
		}
		return Dispatch(dispatchCtx, opts, req)
	}
	if !vmkit.BackendCapabilities(opts.Backend).DetachedHostSupervisor {
		return Dispatch(context.Background(), opts, req)
	}
	if err := requireReadableFile(opts.KernelPath, "kernel"); err != nil {
		return vmkit.Response{}, err
	}
	path := opts.SupervisorPath
	if path == "" {
		path = "microagent-applevf-supervisor"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return vmkit.Response{}, err
	}
	if err := os.MkdirAll(filepath.Join(opts.StateDir, opts.Name), 0o700); err != nil {
		return vmkit.Response{}, err
	}
	supervisorLogPath := filepath.Join(opts.StateDir, opts.Name, "supervisor.log")
	supervisorLog, err := os.OpenFile(supervisorLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return vmkit.Response{}, err
	}
	defer func() { _ = supervisorLog.Close() }()
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Stdout = supervisorLog
	cmd.Stderr = supervisorLog
	cmd.Env = supervisorEnvironment(opts)
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return vmkit.Response{}, err
	}
	if err := WriteProcessState(opts, req, vmkit.StateRunning, cmd.Process.Pid, ""); err != nil {
		_ = cmd.Process.Kill()
		return vmkit.Response{}, err
	}
	_ = cmd.Process.Release()
	event := vmkit.Event{
		Identity:   *req.Identity,
		State:      vmkit.StateRunning,
		Detail:     "serial=" + SerialLogPath(opts.StateDir, opts.Name),
		ObservedAt: time.Now().UTC(),
	}
	return vmkit.Response{OK: true, Backend: opts.Backend, Event: &event}, nil
}

func supervisorEnvironment(opts Options) []string {
	env := os.Environ()
	if opts.Backend != vmkit.BackendAppleVF {
		return env
	}
	// A pre-set MICROAGENT_EGRESS_DATAPATH_BIN wins: embedders of this library
	// (go test, custom hosts) are not the microagent CLI, so os.Executable
	// would point the supervisor at a binary with no --egress-datapath mode.
	if strings.TrimSpace(os.Getenv("MICROAGENT_EGRESS_DATAPATH_BIN")) != "" {
		return env
	}
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return env
	}
	return append(env, "MICROAGENT_EGRESS_DATAPATH_BIN="+exe)
}

func requireReadableFile(path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s is not readable at %s: %w", name, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is not readable at %s: path is a directory", name, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s is not readable at %s: %w", name, path, err)
	}
	return file.Close()
}

func detachedSupervisorCommand(backend string) string {
	if command := vmkit.BackendCapabilities(backend).DetachedStartCommand; command != "" {
		return command
	}
	return "run"
}

func WriteProcessState(opts Options, req vmkit.Request, state vmkit.VMState, pid int, errorText string) error {
	if req.Identity == nil || req.Config == nil {
		return fmt.Errorf("workspace request is missing identity or config")
	}
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	event := vmkit.Event{
		Identity:   *req.Identity,
		State:      state,
		Detail:     "serial=" + SerialLogPath(opts.StateDir, opts.Name),
		ObservedAt: time.Now().UTC(),
	}
	fileEvent := EventFile{
		Identity:   *req.Identity,
		State:      state,
		Detail:     event.Detail,
		ObservedAt: event.ObservedAt.Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "event.json"), fileEvent); err != nil {
		return err
	}
	if err := appendEvent(filepath.Join(dir, "events.json"), fileEvent); err != nil {
		return err
	}
	updatedAt := time.Now().UTC()
	runtimeState := RuntimeState{
		Event:           fileEvent,
		Config:          *req.Config,
		PID:             pid,
		SerialLogPath:   SerialLogPath(opts.StateDir, opts.Name),
		SerialInputPath: SerialInputPath(opts.StateDir, opts.Name),
		UpdatedAt:       updatedAt.Format(time.RFC3339),
		Error:           errorText,
	}
	if state == vmkit.StateStarting || state == vmkit.StateRunning || state == vmkit.StateQuarantined {
		runtimeState.StartedAt = updatedAt.Format(time.RFC3339)
	}
	runtimeState.Readiness = readinessFromRuntime(runtimeState)
	return writeJSONFile(filepath.Join(dir, "runtime.json"), runtimeState)
}

func responseFromEvent(opts Options, eventFile EventFile, errorText string) vmkit.Response {
	event := vmkit.Event{
		Identity:   eventFile.Identity,
		State:      eventFile.State,
		Detail:     eventFile.Detail,
		ObservedAt: time.Now().UTC(),
	}
	if parsed, err := time.Parse(time.RFC3339, eventFile.ObservedAt); err == nil {
		event.ObservedAt = parsed
	}
	backend := opts.Backend
	if backend == "" {
		backend = eventFile.Identity.Backend
	}
	resp := vmkit.Response{OK: eventFile.State != vmkit.StateFailed, Backend: backend, Event: &event}
	if manifest, err := ReadManifest(opts.StateDir, eventFile.Identity.RuntimeID); err == nil {
		resp.RestartPolicy = firstNonEmpty(manifest.Restart, DefaultRestartPolicy)
		network := NetworkConfigFromSpec(manifest.Network)
		if state, err := ReadRuntimeState(Options{StateDir: opts.StateDir, Name: eventFile.Identity.RuntimeID}); err == nil && state.Config.Network != nil {
			runtimeNetwork := NormalizeNetworkConfig(*state.Config.Network)
			runtimeNetwork.Runtime = nil
			network.Runtime = &runtimeNetwork
		}
		resp.Network = &network
		resp.Mediation = manifest.Mediation
		report := vmkit.NegotiateEgressCapture(backend, network.Mode, manifest.EgressMode)
		resp.EgressCapture = &report
		artifacts := RuntimeArtifacts(manifest.Artifacts)
		resp.Artifacts = &artifacts
		resp.Verification = VerificationForStatus(opts, eventFile.Identity.RuntimeID, manifest, eventFile.State)
	}
	readiness := readinessForStatus(opts, eventFile)
	resp.Readiness = &readiness
	if result, err := ReadRuntimeResult(opts, eventFile.Identity); err == nil {
		resp.Result = &result
	}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func VerificationForStatus(opts Options, name string, manifest Manifest, state vmkit.VMState) *vmkit.RuntimeVerification {
	recorded := manifest.Verification
	if recorded == nil {
		if _, err := ReadRuntimeState(Options{StateDir: opts.StateDir, Name: name}); err != nil {
			return nil
		}
	}
	verification := vmkit.RuntimeVerification{OK: true}
	if recorded != nil {
		verification.ImageRef = recorded.ImageRef
		verification.ResolvedRef = recorded.ResolvedRef
		verification.ImageDigest = recorded.ImageDigest
	}
	kernelPath, rootfsPath := "", ""
	if state, err := ReadRuntimeState(Options{StateDir: opts.StateDir, Name: name}); err == nil {
		kernelPath = state.Config.KernelPath
		rootfsPath = state.Config.RootfsPath
	}
	if kernelPath == "" && recorded != nil && recorded.Kernel != nil {
		kernelPath = recorded.Kernel.Path
	}
	if rootfsPath == "" && recorded != nil && recorded.Rootfs != nil {
		rootfsPath = recorded.Rootfs.Path
	}
	if rootfsPath == "" {
		rootfsPath = WorkspaceRootfsPath(opts.StateDir, name, opts.Backend)
	}
	verification.Kernel = currentArtifact("kernel", kernelPath, recordedArtifactFor(recorded, "kernel"), &verification, true)
	verification.Rootfs = rootfsArtifactForStatus(rootfsPath, recordedArtifactFor(recorded, "rootfs"), &verification, state)
	if recorded != nil && recorded.Init != nil {
		verification.Init = currentArtifact("init", recorded.Init.Path, recorded.Init, &verification, true)
	}
	verification.OK = len(verification.Divergence) == 0
	return &verification
}

func readinessForStatus(opts Options, event EventFile) vmkit.RuntimeReadiness {
	state, err := ReadRuntimeState(Options{StateDir: opts.StateDir, Name: event.Identity.RuntimeID})
	if err == nil {
		return readinessFromRuntime(state)
	}
	readiness := vmkit.RuntimeReadiness{}
	if event.State == vmkit.StateRunning || event.State == vmkit.StateHalted || event.State == vmkit.StateStopped || event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: parseOptionalTime(event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(event.State),
		}
	}
	resultPath := ResultPath(opts.StateDir, event.Identity.RuntimeID)
	if _, statErr := os.Stat(resultPath); statErr == nil {
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: fileModTime(resultPath),
			Detail:     "guest result is available",
		}
	}
	return readiness
}

func readinessFromRuntime(state RuntimeState) vmkit.RuntimeReadiness {
	readiness := vmkit.RuntimeReadiness{}
	if state.StartedAt != "" || state.Event.State == vmkit.StateRunning || state.Event.State == vmkit.StateHalted || state.Event.State == vmkit.StateStopped || state.Event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: firstTime(state.StartedAt, state.Event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(state.Event.State),
		}
	}
	liveUnavailable := liveReadinessUnavailableSignal(state.Event.State, firstTime(state.StartedAt, state.Event.ObservedAt))
	if liveUnavailable != nil {
		readiness.ShellReady = *liveUnavailable
		readiness.ExecReady = *liveUnavailable
		readiness.ResultReady = *liveUnavailable
		if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
			readiness.MediationReady = *liveUnavailable
		}
	}
	if state.Event.State == vmkit.StateRunning && state.SerialInputPath != "" {
		if signal, ok := shellReadinessFromRuntime(state); ok {
			readiness.ShellReady = signal
		}
	}
	if signal, ok := ExecReadinessSignal(context.Background(), state, ExecReadyProbeTimeout); ok {
		readiness.ExecReady = signal
	}
	path := ResultPath(state.Config.StateDir, state.Event.Identity.RuntimeID)
	if _, err := os.Stat(path); err == nil {
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: fileModTime(path),
			Detail:     "guest result is available",
		}
	} else if !os.IsNotExist(err) {
		readiness.ResultReady = vmkit.ReadinessSignal{Error: err.Error()}
	}
	if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
		readiness.MediationReady = vmkit.MediationReadinessSignal(context.Background(), *state.Config.Mediation, state.Event.State, firstTime(state.StartedAt, state.Event.ObservedAt), 150*time.Millisecond)
	}
	return readiness
}

func shellReadinessFromRuntime(state RuntimeState) (vmkit.ReadinessSignal, bool) {
	return ShellReadinessSignalWithMode(context.Background(), state, 150*time.Millisecond, ShellReadinessProbeCommand)
}

type ShellReadinessProbeMode int

const (
	ShellReadinessProbeTCP ShellReadinessProbeMode = iota
	ShellReadinessProbeCommand
)

func ShellReadinessSignal(ctx context.Context, state RuntimeState, probeTimeout time.Duration) (vmkit.ReadinessSignal, bool) {
	return ShellReadinessSignalWithMode(ctx, state, probeTimeout, ShellReadinessProbeTCP)
}

func ShellReadinessSignalWithMode(ctx context.Context, state RuntimeState, probeTimeout time.Duration, mode ShellReadinessProbeMode) (vmkit.ReadinessSignal, bool) {
	if _, err := os.Stat(state.SerialInputPath); err != nil {
		if !os.IsNotExist(err) {
			return vmkit.ReadinessSignal{Error: err.Error()}, true
		}
		return vmkit.ReadinessSignal{}, false
	}
	if vmkit.BackendCapabilities(state.Event.Identity.Backend).ShellReadinessProbe && state.Config.ShellPort != 0 {
		target, err := ConsoleTarget(state.Event.Identity.RuntimeID, state)
		if err != nil {
			return vmkit.ReadinessSignal{Ready: false, Error: err.Error()}, true
		}
		observedAt := time.Now().UTC()
		var elapsed time.Duration
		var probeErr error
		if mode == ShellReadinessProbeCommand {
			elapsed, probeErr = ProbeShellCommand(ctx, ConsoleOptions{Name: state.Event.Identity.RuntimeID}, target, probeTimeout, nil)
		} else {
			elapsed, probeErr = ProbeShellTarget(ctx, target, probeTimeout, nil)
		}
		if probeErr != nil {
			kind := "unreachable"
			if mode == ShellReadinessProbeCommand {
				kind = "command probe failed"
			}
			return vmkit.ReadinessSignal{
				Ready:      false,
				ObservedAt: &observedAt,
				Detail:     fmt.Sprintf("shell target %s at %s after %s: %v", kind, ShellTargetDescription(target), elapsed.Round(time.Millisecond), probeErr),
			}, true
		}
		detail := fmt.Sprintf("shell target reachable at %s in %s", ShellTargetDescription(target), elapsed.Round(time.Millisecond))
		if mode == ShellReadinessProbeCommand {
			detail = fmt.Sprintf("shell command round-trip ready at %s in %s", ShellTargetDescription(target), elapsed.Round(time.Millisecond))
		}
		return vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: &observedAt,
			Detail:     detail,
		}, true
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: fileModTime(state.SerialInputPath),
		Detail:     "console input is available",
	}, true
}

func shellHelperListening(serialLogPath string, shellPort uint16) bool {
	if serialLogPath == "" || shellPort == 0 {
		return false
	}
	data, err := os.ReadFile(serialLogPath)
	if err != nil {
		return false
	}
	needle := []byte(fmt.Sprintf("microagent-init: shell helper listening on vsock port %d", shellPort))
	return bytes.Contains(data, needle)
}

func fillRunResult(result *Result, opts Options) {
	if serial, readErr := os.ReadFile(result.SerialPath); readErr == nil {
		result.SerialLog = string(serial)
	}
	if guest, readErr := ReadGuestResult(opts); readErr == nil {
		result.Result = &guest
	}
}

func recordedArtifact(path string) *vmkit.VerifiedArtifact {
	artifact := &vmkit.VerifiedArtifact{Path: path}
	if strings.TrimSpace(path) == "" {
		artifact.Error = "path is empty"
		return artifact
	}
	sum, err := FileSHA256(path)
	if err != nil {
		artifact.Error = err.Error()
		return artifact
	}
	artifact.SHA256 = sum
	return artifact
}

func recordedArtifactFor(recorded *vmkit.RuntimeVerification, name string) *vmkit.VerifiedArtifact {
	if recorded == nil {
		return nil
	}
	switch name {
	case "kernel":
		return recorded.Kernel
	case "rootfs":
		return recorded.Rootfs
	case "init":
		return recorded.Init
	default:
		return nil
	}
}

func shouldCompareRootfs(state vmkit.VMState) bool {
	return state == "" || state == vmkit.StateUnknown
}

func liveReadinessUnavailableSignal(state vmkit.VMState, observedAt *time.Time) *vmkit.ReadinessSignal {
	if !liveWorkspaceUnavailableState(state) {
		return nil
	}
	return &vmkit.ReadinessSignal{
		Ready:      false,
		ObservedAt: observedAt,
		Detail:     fmt.Sprintf("workspace is %s; live readiness unavailable", state),
	}
}

func liveWorkspaceUnavailableState(state vmkit.VMState) bool {
	return state == vmkit.StatePrepared || state == vmkit.StateHalted || state == vmkit.StateStopped || state == vmkit.StateQuarantined || state == vmkit.StateFailed
}

func rootfsArtifactForStatus(path string, recorded *vmkit.VerifiedArtifact, verification *vmkit.RuntimeVerification, state vmkit.VMState) *vmkit.VerifiedArtifact {
	if !liveWorkspaceUnavailableState(state) {
		return currentArtifact("rootfs", path, recorded, verification, shouldCompareRootfs(state))
	}
	artifact := &vmkit.VerifiedArtifact{Path: path}
	if recorded != nil {
		artifact.RecordedSHA256 = recorded.SHA256
		artifact.SHA256 = recorded.SHA256
		if artifact.Path == "" {
			artifact.Path = recorded.Path
		}
	}
	if strings.TrimSpace(artifact.Path) == "" {
		artifact.Error = "path is empty"
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{Artifact: "rootfs", Error: artifact.Error})
	}
	return artifact
}

func currentArtifact(name, path string, recorded *vmkit.VerifiedArtifact, verification *vmkit.RuntimeVerification, compare bool) *vmkit.VerifiedArtifact {
	artifact := &vmkit.VerifiedArtifact{Path: path}
	if recorded != nil {
		artifact.RecordedSHA256 = recorded.SHA256
		if artifact.Path == "" {
			artifact.Path = recorded.Path
		}
	}
	if strings.TrimSpace(artifact.Path) == "" {
		artifact.Error = "path is empty"
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{Artifact: name, Error: artifact.Error})
		return artifact
	}
	sum, err := FileSHA256(artifact.Path)
	if err != nil {
		artifact.Error = err.Error()
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{Artifact: name, Error: err.Error()})
		return artifact
	}
	artifact.SHA256 = sum
	if compare && artifact.RecordedSHA256 != "" && artifact.RecordedSHA256 != artifact.SHA256 {
		verification.Divergence = append(verification.Divergence, vmkit.VerificationDivergence{
			Artifact: name,
			Field:    "sha256",
			Expected: artifact.RecordedSHA256,
			Actual:   artifact.SHA256,
		})
	}
	return artifact
}

func appendEvent(path string, event EventFile) error {
	return eventhistory.Append(path, event, eventhistory.Options{})
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func CopyFile(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr != nil {
		_ = out.Close()
		return copyErr
	}
	if chmodErr := out.Chmod(mode); chmodErr != nil {
		_ = out.Close()
		return chmodErr
	}
	closeErr := out.Close()
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func copyFileReplace(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr != nil {
		_ = out.Close()
		return copyErr
	}
	if chmodErr := out.Chmod(mode); chmodErr != nil {
		_ = out.Close()
		return chmodErr
	}
	if closeErr := out.Close(); closeErr != nil {
		return closeErr
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func egressCACertSHA256(wsDir string) (string, error) {
	pemBytes, err := os.ReadFile(filepath.Join(wsDir, "egress-ca.pem"))
	if err != nil {
		return "", fmt.Errorf("read egress CA cert: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("egress CA cert at %s is not a valid CERTIFICATE PEM", wsDir)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

func guestIPFromNetwork(network vmkit.NetworkConfig) string {
	ip := strings.TrimSpace(network.IP)
	if ip == "" && network.Runtime != nil {
		ip = strings.TrimSpace(network.Runtime.IP)
	}
	if ip == "" {
		return ""
	}
	if host, _, err := net.ParseCIDR(ip); err == nil {
		return host.String()
	}
	if strings.Contains(ip, "/") {
		return strings.SplitN(ip, "/", 2)[0]
	}
	return ip
}

func parseOptionalTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func firstTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed := parseOptionalTime(value); parsed != nil {
			return parsed
		}
	}
	return nil
}

func fileModTime(path string) *time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mod := info.ModTime().UTC()
	return &mod
}
