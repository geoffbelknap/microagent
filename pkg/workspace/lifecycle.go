package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
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
	StartedAt string `json:"started_at"`
	ExitedAt  string `json:"exited_at"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
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
		return Result{}, fmt.Errorf("create requires a name")
	}
	if err := ValidateName(opts.Name); err != nil {
		return Result{}, err
	}
	if opts.ImageRef == "" {
		opts.ImageRef = DefaultImage(opts.Architecture)
	}
	if opts.UseImageCommand && strings.TrimSpace(opts.ServiceCommand) != "" {
		return Result{}, fmt.Errorf("create cannot use both image command and service command")
	}
	if err := normalizeLifecycleOptions(&opts, true); err != nil {
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
		resp, runErr := runForeground(runCtx, opts, Request(opts, "run", result.RootfsPath, NewRequestID()))
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
	resp, err := Dispatch(ctx, opts, Request(opts, "prepare", result.RootfsPath, NewRequestID()))
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
		return fmt.Errorf("workspace %s is already %s; stop or delete it before create", opts.Name, state)
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
		return Result{}, fmt.Errorf("run requires ExecCommand")
	}
	if opts.Name == "" {
		opts.Name = fmt.Sprintf("run-%d", time.Now().UnixNano())
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
	disks, err := PrepareDisks(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	opts.Disks = disks
	result, err := BuildRootfs(ctx, opts)
	if err != nil {
		return result, err
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
	resp, err := runForeground(runCtx, opts, Request(opts, "run", result.RootfsPath, NewRequestID()))
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
		if !opts.Keep {
			Cleanup(opts.StateDir, opts.Name)
			result.SerialPath = ""
		}
	}
	return result, err
}

func Start(ctx context.Context, opts Options) (Result, error) {
	if opts.Name == "" {
		return Result{}, fmt.Errorf("start requires a name")
	}
	if err := ValidateName(opts.Name); err != nil {
		return Result{}, err
	}
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return Result{}, err
	}
	if opts.ResultPort == 0 {
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
	applyManifest(&opts, manifest)
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
	startReq := Request(opts, "run", rootfsPath, NewRequestID())
	startReq.Tag = strings.TrimSpace(opts.FromSnapshot)
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
	req := Request(opts, "inspect", "", NewRequestID())
	return Dispatch(ctx, opts, req)
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
			if _, err := os.Stat(filepath.Join(stateDir, entry.Name(), "event.json")); err == nil {
				names[entry.Name()] = true
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

func Control(ctx context.Context, opts Options, command string) (vmkit.Response, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.Response{}, err
	}
	if err := ValidateName(opts.Name); err != nil {
		return vmkit.Response{}, err
	}
	switch command {
	case "halt", "quarantine", "pause", "resume", "stop", "kill", "delete":
	default:
		return vmkit.Response{}, fmt.Errorf("unsupported workspace control command: %s", command)
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
// around the capture; an already-paused workspace stays paused.
func Snapshot(ctx context.Context, opts Options, tag string) (vmkit.SnapshotManifest, error) {
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	if err := ValidateName(opts.Name); err != nil {
		return vmkit.SnapshotManifest{}, err
	}
	if strings.TrimSpace(tag) == "" {
		return vmkit.SnapshotManifest{}, fmt.Errorf("snapshot tag is required")
	}
	req := vmkit.Request{
		Command: "snapshot",
		Identity: &vmkit.Identity{
			RequestID: NewRequestID(),
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   opts.Backend,
		},
		Config: &vmkit.Config{StateDir: opts.StateDir},
		Tag:    tag,
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
	srcDir := vmkit.SnapshotDir(opts.StateDir, sourceWorkspace, tag)
	manifest, err := vmkit.ReadSnapshotManifest(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, fmt.Errorf("snapshot %q not found for workspace %s", tag, sourceWorkspace)
		}
		return Result{}, err
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
		// The resumed guest keeps the source's baked IP, so the fork configures
		// its own tap/pasta (in its own namespace) with the source's addressing
		// rather than deriving a fresh subnet from the fork's name.
		opts.Network = vmkit.NetworkConfig{
			Mode:    manifest.NetworkMode,
			IP:      manifest.NetworkIP,
			Gateway: manifest.NetworkGateway,
			Subnet:  manifest.NetworkSubnet,
		}
	}
	if opts.ImageRef == "" {
		opts.ImageRef = manifest.ImageRef
	}
	// The resumed guest listens on the source's baked vsock service ports. The
	// fork keeps its own unique host ports (name-derived) and bridges them to
	// the source's guest ports, so concurrent forks don't collide on the host.
	opts.GuestShellPort = manifest.ShellPort
	opts.GuestExecPort = manifest.ExecPort
	if err := normalizeLifecycleOptions(&opts, false); err != nil {
		return Result{}, err
	}
	if err := EnsureCanCreate(opts); err != nil {
		return Result{}, err
	}
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		return Result{}, err
	}
	if err := CopyFile(filepath.Join(srcDir, vmkit.SnapshotRootfsName), rootfsPath, 0o644); err != nil {
		return Result{}, fmt.Errorf("copy snapshot rootfs into fork: %w", err)
	}
	if err := WriteManifest(opts); err != nil {
		return Result{}, err
	}
	if err := copySnapshotInto(srcDir, vmkit.SnapshotDir(opts.StateDir, opts.Name, tag)); err != nil {
		return Result{}, err
	}
	opts.FromSnapshot = tag
	return Start(ctx, opts)
}

func copySnapshotInto(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{vmkit.SnapshotVMStateName, vmkit.SnapshotMemoryName, vmkit.SnapshotRootfsName, vmkit.SnapshotManifestName} {
		if err := CopyFile(filepath.Join(srcDir, name), filepath.Join(dstDir, name), 0o644); err != nil {
			return fmt.Errorf("copy snapshot %s into fork: %w", name, err)
		}
	}
	return nil
}

func BuildRootfs(ctx context.Context, opts Options) (Result, error) {
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)
	req := buildRootfsRequest(opts, rootfsPath)
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	result := Result{
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
		Image:        provenance,
	}
	return result, err
}

func buildRootfsRequest(opts Options, rootfsPath string) rootfs.BuildRequest {
	command, resultPort := BuildCommandAndPort(opts)
	mode := ""
	if opts.PrepareForStart && opts.UseImageCommand {
		mode = "service"
	} else if opts.PrepareForStart && strings.TrimSpace(opts.ServiceCommand) != "" && !HasSetupCommand(opts) && strings.TrimSpace(opts.ExecCommand) == "" {
		mode = "managed-service"
	}
	return rootfs.BuildRequest{
		ImageRef:       opts.ImageRef,
		Platform:       rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:     rootfsPath,
		Format:         WorkspaceRootfsFormat(opts.Backend),
		InitPath:       rootfs.DefaultInitPath,
		Command:        command,
		Mode:           mode,
		ConsoleShell:   opts.ConsoleShell,
		Hostname:       opts.Hostname,
		ShellPort:      ShellPort(opts),
		ExecPort:       ExecPort(opts),
		InitBinaryPath: opts.GuestInitPath,
		ResultPort:     resultPort,
		NoImageCommand: opts.PrepareForStart && !HasGuestCommand(opts) && !opts.UseImageCommand,
		StateDir:       filepath.Join(opts.StateDir, "build"),
		Mke2fsPath:     opts.Mke2fsPath,
		SizeMiB:        opts.SizeMiB,
		Env:            opts.Env,
		Files:          RootfsFiles(opts.Files),
		Mounts:         MountsForBackend(opts.Backend, opts.Disks),
		HostForwards:   RootfsPortForwards(opts.Network.PortForwards),
		AllowMutable:   true,
		Progress:       opts.Progress,
	}
}

func WorkspaceRootfsFormat(backend string) string {
	if backend == vmkit.BackendWindowsHyperV {
		return rootfs.FormatVHD
	}
	return rootfs.FormatExt4
}

func WorkspaceDiskFormat(backend string) string {
	return WorkspaceRootfsFormat(backend)
}

func WorkspaceRootfsFilename(backend string) string {
	if WorkspaceRootfsFormat(backend) == rootfs.FormatVHD {
		return "rootfs.vhd"
	}
	return "rootfs.ext4"
}

func WorkspaceDiskFilename(backend, name string) string {
	if WorkspaceDiskFormat(backend) == rootfs.FormatVHD {
		return name + ".vhd"
	}
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
		return []string{primary, filepath.Join(stateDir, "workspaces", name, "rootfs.vhd")}
	}
	return []string{primary, secondary}
}

func PrepareDisks(ctx context.Context, opts Options) ([]Disk, error) {
	if len(opts.Disks) == 0 {
		return nil, nil
	}
	disks := make([]Disk, 0, len(opts.Disks))
	seenNames := map[string]bool{}
	seenMountpoints := map[string]bool{}
	for _, disk := range opts.Disks {
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

func WriteManifest(opts Options) error {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(workspaceDir, "workspace.json"), Manifest{
		Name:            opts.Name,
		Profile:         opts.Profile,
		Restart:         NormalizeRestartPolicy(opts.RestartPolicy),
		Resources:       ResourcesFromOptions(opts),
		Network:         NetworkSpecFromConfig(opts.Network),
		Service:         strings.TrimSpace(opts.ServiceCommand),
		ConsoleShell:    strings.TrimSpace(opts.ConsoleShell),
		Hostname:        strings.TrimSpace(opts.Hostname),
		Mediation:       opts.Mediation,
		Disks:           opts.Disks,
		Artifacts:       ArtifactsFromOptions(opts),
		Verification:    opts.Verification,
		Secrets:         secretRefsFromOptions(opts),
		SecretEnvFiles:  opts.SecretEnvFiles,
		OnDemandSecrets: onDemandRefsFromOptions(opts),
		SecretsAudit:    opts.SecretsAudit,
	})
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
		return fmt.Errorf("workspace name is required")
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("invalid workspace name: %s", name)
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
			return fmt.Errorf("workspace %s is quarantined with preserved pid %d; halt, stop, or kill it before start", name, pid)
		}
		return fmt.Errorf("workspace %s is quarantined; halt, stop, or kill it before start", name)
	case vmkit.StateStarting, vmkit.StateRunning:
		return fmt.Errorf("workspace %s is already %s", name, state)
	default:
		return fmt.Errorf("workspace %s cannot start from state %s", name, state)
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
	if manifest.Network.Mode != "" || manifest.Network.Interface != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" || manifest.Network.Subnet != "" || manifest.Network.Gateway != "" {
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
	return backend == vmkit.BackendFirecracker || backend == vmkit.BackendWindowsHyperV
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
	if opts.Backend != vmkit.BackendAppleVF {
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
	if err := os.MkdirAll(filepath.Join(opts.StateDir, opts.Name), 0o755); err != nil {
		return vmkit.Response{}, err
	}
	supervisorLogPath := filepath.Join(opts.StateDir, opts.Name, "supervisor.log")
	supervisorLog, err := os.OpenFile(supervisorLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return vmkit.Response{}, err
	}
	defer supervisorLog.Close()
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Stdout = supervisorLog
	cmd.Stderr = supervisorLog
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
	switch backend {
	case vmkit.BackendFirecracker, vmkit.BackendWindowsHyperV:
		return "start"
	default:
		return "run"
	}
}

func WriteProcessState(opts Options, req vmkit.Request, state vmkit.VMState, pid int, errorText string) error {
	if req.Identity == nil || req.Config == nil {
		return fmt.Errorf("workspace request is missing identity or config")
	}
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	if state.Event.Identity.Backend != vmkit.BackendWindowsHyperV && state.Config.ShellPort != 0 {
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
	const maxEvents = 1024
	var events []EventFile
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && len(bytes.TrimSpace(data)) != 0 {
		if err := json.Unmarshal(data, &events); err != nil {
			return err
		}
	}
	events = append(events, event)
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	return writeJSONFile(path, events)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	if err := tmp.Chmod(0o644); err != nil {
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
	defer f.Close()
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
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
