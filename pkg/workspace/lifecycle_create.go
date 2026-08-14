package workspace

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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
	if err := validateGuestCommandInputs(opts); err != nil {
		return Result{}, err
	}
	if err := normalizeLifecycleOptions(&opts, true); err != nil {
		return Result{}, err
	}
	applyBoundedOperationsDefaults(&opts)
	if err := validateCapabilityComposition(opts); err != nil {
		return Result{CapabilityComposition: EvaluateCapabilityComposition(opts)}, err
	}
	// The image ref is offline-checkable: the same parse the builder runs
	// first. Rejecting it here means a dry run cannot bless a config whose
	// real run fails before pulling a byte.
	if err := rootfs.ValidateImageRef(opts.ImageRef); err != nil {
		return Result{}, err
	}
	// Validation is done and nothing has been written yet; EnsureKernel below is
	// the first side effect (it can install a kernel).
	if opts.DryRun {
		return dryRunResult(opts), nil
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
	capacity, err := reserveWorkspaceCapacity(opts)
	if err != nil {
		return Result{}, err
	}
	defer capacity.Release()
	if err := pinGuestInitArtifact(&opts); err != nil {
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
	result.CapabilityComposition = EvaluateCapabilityComposition(opts)
	// The build decides the final disk size: an auto-sized build may have
	// grown it, and a derived build sizes it from content in either
	// direction. Record what the workspace actually has, and whether it
	// was derived, so the manifest can explain the size.
	if result.Resources.SizeMiB > 0 && (result.Resources.SizeMiB > opts.SizeMiB || result.SizeDerived) {
		opts.SizeMiB = result.Resources.SizeMiB
	}
	opts.SizeDerived = result.SizeDerived
	result.Disks = disks
	// The image config captured at build (or carried by the cloned
	// baseline's record) is what boot-time config assembly merges env from
	// and resolves --image-command with; persist it with the workspace.
	opts.ImageEnv = result.Image.ImageEnv
	opts.ImageEntrypoint = result.Image.ImageEntrypoint
	opts.ImageCmd = result.Image.ImageCmd
	opts.ImageDefaults = result.Image.ImageDefaults
	// The config disk must exist before verification records it: it is the
	// fourth verified artifact, so the command and files the guest will run
	// never escape attestation.
	if err := WriteFilesArchive(opts); err != nil {
		return result, err
	}
	if _, err := writeConfigDisk(opts, "create_config"); err != nil {
		return result, err
	}
	verification, err := BuildVerification(opts, result)
	if err != nil {
		return result, err
	}
	opts.Verification = &verification
	result.Verification = &verification
	if err := writeManifest(opts, "create"); err != nil {
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
		// The setup boot is a one-shot run: the supervisor self-enforces the
		// same bound the host dispatch waits, so a setup whose host dies
		// cannot run forever.
		runReq.Config.RunBoundSeconds = runBoundSeconds(opts.Timeout)
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
		// A setup boot that exited cleanly flips the workspace to its final
		// boot config; a failed one leaves SetupComplete unset so the next
		// start retries the setup. The decision is host-side — the setup
		// script no longer rewrites its successor's config from inside the
		// guest, so a script that dies mid-run can no longer poison later
		// boots.
		if waitErr == nil && result.Result != nil && result.Result.ExitCode == 0 && result.Result.StartError == "" {
			opts.SetupComplete = true
			if _, err := writeConfigDisk(opts, "setup_complete_config"); err != nil {
				return result, err
			}
			verification, err := finalizeSetupVerification(opts.StateDir, opts.Name, result.RootfsPath)
			if err != nil {
				return result, err
			}
			result.Verification = verification
			result.Response.Verification = verification
		}
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

// applyBoundedOperationsDefaults resolves a brand-new workspace's lifetime lease
// and egress-cap bounds (ASK tenet 8: operations are bounded) before
// anything persists them, so create's manifest write captures the actual
// resolved value rather than the raw pre-default zero — which matters
// because Start restores these fields from the manifest afterward and never
// re-derives them. A caller that pinned a value, including the underlying
// field's own zero meaning "permanent"/"unlimited", is never overridden.
// Only called from Create and CreateFromSnapshot: Start's own restart path
// intentionally does not call this, so an existing workspace's bounds never
// change just because it was restarted after an upgrade.
func applyBoundedOperationsDefaults(opts *Options) {
	if opts.LeaseSeconds == 0 && !opts.LeaseSecondsExplicit {
		opts.LeaseSeconds = DefaultLeaseSeconds
	}
	if !vmkit.EgressMediationOn(vmkit.ResolveEgressModeDefault(opts.EgressMode)) {
		return
	}
	if opts.EgressMaxBytesPerSec == 0 && !opts.EgressMaxBytesPerSecExplicit {
		opts.EgressMaxBytesPerSec = DefaultEgressMaxBytesPerSec
	}
	if opts.EgressMaxTotalBytes == 0 && !opts.EgressMaxTotalBytesExplicit {
		opts.EgressMaxTotalBytes = DefaultEgressMaxTotalBytes
	}
	if opts.EgressMaxConcurrentConns == 0 && !opts.EgressMaxConcurrentConnsExplicit {
		opts.EgressMaxConcurrentConns = DefaultEgressMaxConcurrentConns
	}
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

// runBoundSeconds converts the host dispatch timeout into the supervisor
// run bound for one-shot shapes, rounding sub-second timeouts up so a
// positive timeout never becomes an unbounded run.
func runBoundSeconds(timeout time.Duration) int {
	if timeout <= 0 {
		return 0
	}
	seconds := int(timeout.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return seconds
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
	if err := validateGuestCommandInputs(opts); err != nil {
		return Result{}, err
	}
	if err := normalizeLifecycleOptions(&opts, true); err != nil {
		return Result{}, err
	}
	if err := validateCapabilityComposition(opts); err != nil {
		return Result{CapabilityComposition: EvaluateCapabilityComposition(opts)}, err
	}
	// Same offline image-ref check as Create, for the same reason: the dry
	// run below must not bless a ref the real build's first parse refuses.
	if err := rootfs.ValidateImageRef(opts.ImageRef); err != nil {
		return Result{}, err
	}
	if opts.DryRun {
		return dryRunResult(opts), nil
	}
	if err := EnsureKernel(ctx, &opts); err != nil {
		return Result{}, err
	}
	if err := materializeCredSwapConfig(&opts); err != nil {
		return Result{}, err
	}
	if err := pinGuestInitArtifact(&opts); err != nil {
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
	result.CapabilityComposition = EvaluateCapabilityComposition(opts)
	// The build decides the final disk size (grown or content-derived);
	// record what the workspace actually has.
	if result.Resources.SizeMiB > 0 && (result.Resources.SizeMiB > opts.SizeMiB || result.SizeDerived) {
		opts.SizeMiB = result.Resources.SizeMiB
	}
	opts.SizeDerived = result.SizeDerived
	result.Disks = disks
	opts.ImageEnv = result.Image.ImageEnv
	opts.ImageEntrypoint = result.Image.ImageEntrypoint
	opts.ImageCmd = result.Image.ImageCmd
	opts.ImageDefaults = result.Image.ImageDefaults
	// The config disk must exist before verification records it: it is the
	// fourth verified artifact, so the command and files the guest will run
	// never escape attestation.
	if err := WriteFilesArchive(opts); err != nil {
		return result, err
	}
	if _, err := writeConfigDisk(opts, "create_config"); err != nil {
		return result, err
	}
	verification, err := BuildVerification(opts, result)
	if err != nil {
		return result, err
	}
	opts.Verification = &verification
	result.Verification = &verification
	if err := writeManifest(opts, "create"); err != nil {
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
	if err := preflightBrokerSecrets(runCtx, runReq.Config.Brokers); err != nil {
		stopProgress("broker secret unresolvable")
		return result, err
	}
	// One-shot run: the supervisor self-enforces the dispatch bound so an
	// orphaned run (host process killed) cannot outlive its timeout.
	runReq.Config.RunBoundSeconds = runBoundSeconds(opts.Timeout)
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
		return result, err
	}
	// The persistence contract does not flip on failure. It used to: a failed
	// one-shot left a permanent `failed` record that gc (which reconciles
	// RUNNING workspaces against reality) would never reap — so iterating on
	// a broken image accumulated one orphan per attempt, exactly when the
	// user was already having a bad time, and cleanup was a manual delete per
	// generated name. The diagnostics are captured into the result FIRST —
	// serial log and guest result — so discarding the disk loses nothing the
	// caller was going to get; --keep preserves the workspace for inspection
	// the same way it does on success.
	fillRunResult(&result, opts)
	if !opts.Keep {
		Cleanup(opts.StateDir, opts.Name)
		result.SerialPath = ""
	}
	return result, err
}

func Start(ctx context.Context, opts Options) (Result, error) {
	return startWithCapacityReservation(ctx, opts, nil)
}

// startWithCapacityReservation boots a workspace using capacity held by its
// caller when non-nil. The caller retains ownership of a supplied reservation;
// an ordinary Start acquires and releases its own reservation here.
func startWithCapacityReservation(ctx context.Context, opts Options, capacity *workspaceCapacityReservation) (Result, error) {
	if opts.Name == "" {
		return Result{}, operation.New(operation.ErrorValidation, "start requires a name")
	}
	if err := ValidateName(opts.Name); err != nil {
		return Result{}, err
	}
	if tag := strings.TrimSpace(opts.FromSnapshot); tag != "" {
		if err := validateTag(tag); err != nil {
			return Result{}, err
		}
		opts.FromSnapshot = tag
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
	if capacity == nil {
		acquired, err := reserveWorkspaceCapacity(opts)
		if err != nil {
			return Result{}, err
		}
		capacity = acquired
		defer capacity.Release()
	} else if err := capacity.validate(opts); err != nil {
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
		if err := validateCapabilityComposition(opts); err != nil {
			return Result{CapabilityComposition: EvaluateCapabilityComposition(opts)}, err
		}
	}
	// Every workspace Start boots is a created (prepared) one, so boot
	// config assembly must use the prepared-workspace rules — most
	// importantly, a plain workspace suppresses the image command and
	// boots to its console shell rather than the image entrypoint.
	opts.PrepareForStart = true
	var sourceSessionID string
	if tag := strings.TrimSpace(opts.FromSnapshot); tag != "" {
		// Resume-in-place of a workspace that was itself a fork: the loaded
		// VM keeps its baked identity (ancestor vsock path, guest service
		// ports), exactly as CreateFromSnapshot adopts it for a new fork.
		// Without this, stop + start --from-snapshot of a fork bridges to
		// guest ports nobody listens on and its shell/exec are dead.
		if snapManifest, err := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(opts.StateDir, opts.Name, tag)); err == nil {
			adoptSnapshotIdentity(&opts, snapManifest)
			sourceSessionID = snapManifest.SourceSessionID
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
	// Every fresh boot gets a config disk assembled from the manifest the
	// host holds right now — restarts pick up the workspace's current boot
	// config, never a stale baked copy. Snapshot restores skip this: the
	// restored guest already consumed its config, and the VMM requires the
	// device geometry captured with the snapshot, so the copy restored
	// beside the rootfs is authoritative.
	if strings.TrimSpace(opts.FromSnapshot) == "" {
		if _, err := writeConfigDisk(opts, "start_config"); err != nil {
			return Result{}, err
		}
		if err := RefreshManifestVerificationConfig(opts.StateDir, opts.Name); err != nil {
			return Result{}, err
		}
	}
	startReq, err := Request(opts, "run", rootfsPath, NewRequestID())
	if err != nil {
		return Result{}, err
	}
	if sourceSessionID == "" {
		if previous, stateErr := ReadRuntimeState(opts); stateErr == nil {
			sourceSessionID = previous.Event.Identity.SessionID
		}
	}
	startReq.Identity.SourceSessionID = sourceSessionID
	// Fail closed before anything spawns: the companion would resolve these
	// same references moments later, but its failure is invisible to a start
	// that already returned success.
	if err := preflightBrokerSecrets(ctx, startReq.Config.Brokers); err != nil {
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
	// A restored guest resumes with the wall clock it was captured with;
	// push host time in before handing the workspace back. Best-effort and
	// snapshot-only: fresh boots read the clock at boot and need nothing.
	if err == nil && resp.OK && strings.TrimSpace(opts.FromSnapshot) != "" {
		syncGuestClockAfterResume(ctx, opts)
	}
	return Result{
		Workspace:             opts.Name,
		StateDir:              opts.StateDir,
		Profile:               opts.Profile,
		Restart:               opts.RestartPolicy,
		Resources:             ResourcesFromOptions(opts),
		Network:               NetworkSpecFromConfig(opts.Network),
		Service:               strings.TrimSpace(opts.ServiceCommand),
		ConsoleShell:          strings.TrimSpace(opts.ConsoleShell),
		Hostname:              strings.TrimSpace(opts.Hostname),
		RootfsPath:            rootfsPath,
		KernelPath:            opts.KernelPath,
		Disks:                 opts.Disks,
		Artifacts:             ArtifactsFromOptions(opts),
		SerialPath:            SerialLogPath(opts.StateDir, opts.Name),
		CapabilityComposition: EvaluateCapabilityComposition(opts),
		Response:              resp,
	}, err
}
