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
	if err := validateGuestCommandInputs(opts); err != nil {
		return Result{}, err
	}
	if err := normalizeLifecycleOptions(&opts, true); err != nil {
		return Result{}, err
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
