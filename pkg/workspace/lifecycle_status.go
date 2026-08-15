package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func responseFromEvent(opts Options, eventFile EventFile, errorText string) vmkit.Response {
	event := vmkit.Event{
		Identity:   eventFile.Identity,
		State:      eventFile.State,
		Detail:     eventFile.Detail,
		ObservedAt: time.Now().UTC(),
		Lifecycle:  eventFile.Lifecycle,
	}
	if parsed, err := time.Parse(time.RFC3339, eventFile.ObservedAt); err == nil {
		event.ObservedAt = parsed
	}
	backend := opts.Backend
	if backend == "" {
		backend = eventFile.Identity.Backend
	}
	resp := vmkit.Response{OK: eventFile.State != vmkit.StateFailed, Backend: backend, Event: &event}
	resp.RootfsUsage = rootfsUsage(opts)
	resp.BoundedOperations = boundedOperationsStatus(Options{StateDir: opts.StateDir, Name: eventFile.Identity.RuntimeID})
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
		if state, stateErr := ReadRuntimeState(Options{StateDir: opts.StateDir, Name: eventFile.Identity.RuntimeID}); stateErr == nil {
			observeOpts := opts
			observeOpts.Name = eventFile.Identity.RuntimeID
			observeEgressCapture(observeOpts, state, &report)
		}
		resp.EgressCapture = &report
		artifacts := RuntimeArtifacts(manifest.Artifacts)
		resp.Artifacts = &artifacts
		imageDefaults := effectiveWorkspaceImageDefaults(manifest.ImageDefaults, manifest.ImageEnv, manifest.ImageEntrypoint, manifest.ImageCmd)
		if !imageDefaults.IsZero() {
			resp.ImageDefaults = &imageDefaults
		}
		resp.RootfsBase = manifest.RootfsBase
		resp.Verification = VerificationForStatus(opts, eventFile.Identity.RuntimeID, manifest, eventFile.State)
		if history, historyErr := constraintHistoryStatus(opts.StateDir, eventFile.Identity.RuntimeID); historyErr == nil {
			resp.ConstraintHistory = history
		} else if errorText == "" {
			errorText = historyErr.Error()
			resp.OK = false
		}
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
		verification.Init = initArtifactForStatus(opts.StateDir, name, recorded.Init, &verification)
	}
	// The config disk is enforced strictly, like kernel and init — it is
	// host-generated and read-only per boot, so any divergence means the
	// record and the device the guest read no longer agree.
	if recorded != nil && recorded.Config != nil {
		verification.Config = currentArtifact("config", recorded.Config.Path, recorded.Config, &verification, true)
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
	return ShellReadinessSignalWithMode(context.Background(), state, 2*time.Second, ShellReadinessProbeCommand)
}

type ShellReadinessProbeMode int

const (
	ShellReadinessProbeTCP ShellReadinessProbeMode = iota
	ShellReadinessProbeCommand
)

func ShellReadinessSignal(ctx context.Context, state RuntimeState, probeTimeout time.Duration) (vmkit.ReadinessSignal, bool) {
	// A raw connect is not a harmless reachability check: the guest shell
	// endpoint starts an interactive shell for every accepted connection.
	// Require a command round trip whose protocol explicitly sends exit.
	return ShellReadinessSignalWithMode(ctx, state, probeTimeout, ShellReadinessProbeCommand)
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

// DefaultSerialLogMaxBytes bounds Result.SerialLog when the caller does not
// choose a limit. The structured result is the agent-facing surface, and a
// full Linux boot log inlined there made a two-word run cost ~39 KB of which
// ~86% was console noise; a tail keeps the part failures live in (the end)
// while the full log stays at SerialPath.
const DefaultSerialLogMaxBytes = 8192

func fillRunResult(result *Result, opts Options) {
	if serial, readErr := os.ReadFile(result.SerialPath); readErr == nil {
		result.SerialLogBytes = len(serial)
		limit := opts.SerialLogMaxBytes
		if limit == 0 {
			limit = DefaultSerialLogMaxBytes
		}
		if limit > 0 && len(serial) > limit {
			tail := serial[len(serial)-limit:]
			// Start at a line boundary so the excerpt never opens mid-line.
			if i := bytes.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
				tail = tail[i+1:]
			}
			result.SerialLog = string(tail)
			result.SerialLogTruncated = true
		} else {
			result.SerialLog = string(serial)
		}
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
	case "config":
		return recorded.Config
	default:
		return nil
	}
}

func shouldCompareRootfs(state vmkit.VMState) bool {
	return state == "" || state == vmkit.StateUnknown || liveWorkspaceUnavailableState(state)
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
	return currentArtifact("rootfs", path, recorded, verification, shouldCompareRootfs(state))
}

// initArtifactForStatus verifies the durable per-workspace init copy used by
// current manifests. Older manifests may name a package-manager installation
// path that was removed by an upgrade. The rootfs already contains those
// recorded bytes, so a missing legacy source path is not runtime divergence:
// retain the recorded SHA-256 as the content identity and omit the dead path
// from the live response.
func initArtifactForStatus(stateDir, name string, recorded *vmkit.VerifiedArtifact, verification *vmkit.RuntimeVerification) *vmkit.VerifiedArtifact {
	if recorded == nil {
		return nil
	}
	if strings.TrimSpace(recorded.Path) == "" {
		if recorded.SHA256 != "" {
			return &vmkit.VerifiedArtifact{SHA256: recorded.SHA256, RecordedSHA256: recorded.SHA256}
		}
		return currentArtifact("init", "", recorded, verification, true)
	}
	if _, err := os.Stat(recorded.Path); err != nil && os.IsNotExist(err) && recorded.SHA256 != "" {
		durableDir := filepath.Join(stateDir, "workspaces", name, "artifacts")
		rel, relErr := filepath.Rel(durableDir, recorded.Path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return &vmkit.VerifiedArtifact{SHA256: recorded.SHA256, RecordedSHA256: recorded.SHA256}
		}
	}
	return currentArtifact("init", recorded.Path, recorded, verification, true)
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
