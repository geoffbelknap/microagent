package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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
