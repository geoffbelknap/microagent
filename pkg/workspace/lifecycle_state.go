package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func WriteManifest(opts Options) error {
	return writeManifest(opts, "manifest_write")
}

func writeManifest(opts Options, trigger string) error {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return err
	}
	manifest := manifestFromOptions(opts)
	return writeManifestRecord(opts, manifest, trigger)
}

func manifestFromOptions(opts Options) Manifest {
	return Manifest{
		Name:                          opts.Name,
		Purpose:                       opts.Purpose,
		CorrelationID:                 opts.CorrelationID,
		Profile:                       opts.Profile,
		Restart:                       NormalizeRestartPolicy(opts.RestartPolicy),
		Resources:                     ResourcesFromOptions(opts),
		SizeDerived:                   opts.SizeDerived,
		Network:                       NetworkSpecFromConfig(opts.Network),
		Service:                       strings.TrimSpace(opts.ServiceCommand),
		ConsoleShell:                  strings.TrimSpace(opts.ConsoleShell),
		Hostname:                      strings.TrimSpace(opts.Hostname),
		Model:                         strings.TrimSpace(opts.Model),
		ModelRunner:                   modelRunnerManifest(opts.ModelRunner),
		ModelMediation:                modelMediationManifest(opts.ModelMediation),
		Mediation:                     opts.Mediation,
		Health:                        healthManifest(opts.Health),
		Disks:                         opts.Disks,
		Artifacts:                     ArtifactsFromOptions(opts),
		Verification:                  opts.Verification,
		RootfsBase:                    opts.RootfsBase,
		Secrets:                       secretRefsFromOptions(opts),
		SecretEnvFiles:                opts.SecretEnvFiles,
		AllowGuestSetuid:              opts.AllowGuestSetuid,
		OnDemandSecrets:               onDemandRefsFromOptions(opts),
		SecretsAudit:                  opts.SecretsAudit,
		CapabilityComposition:         EvaluateCapabilityComposition(opts),
		CapabilityRiskAcknowledgement: strings.TrimSpace(opts.CapabilityRiskAcknowledgement),
		EgressMode:                    opts.EgressMode,
		EgressAllow:                   opts.EgressAllow,
		EgressPassthrough:             opts.EgressPassthrough,
		EgressAllowlistLocked:         opts.EgressAllowlistLocked,
		EgressSwapConfigPath:          opts.EgressSwapConfigPath,
		EgressMaxBytesPerSec:          opts.EgressMaxBytesPerSec,
		EgressMaxTotalBytes:           opts.EgressMaxTotalBytes,
		EgressMaxConcurrentConns:      opts.EgressMaxConcurrentConns,
		Broker:                        opts.Broker,
		Brokers:                       opts.Brokers,
		Entrypoint:                    strings.TrimSpace(opts.Entrypoint),
		Env:                           opts.Env,
		UseImageCommand:               opts.UseImageCommand,
		ImageEnv:                      opts.ImageEnv,
		ImageEntrypoint:               opts.ImageEntrypoint,
		ImageCmd:                      opts.ImageCmd,
		ImageDefaults:                 opts.ImageDefaults,
		Files:                         opts.Files,
		SetupCommands:                 opts.SetupCommands,
		ExecCommand:                   strings.TrimSpace(opts.ExecCommand),
		SetupComplete:                 opts.SetupComplete,
	}
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
		StartError:  guest.StartError,
	}, nil
}

func BuildVerification(opts Options, result Result) (vmkit.RuntimeVerification, error) {
	verification := vmkit.RuntimeVerification{
		OK:          true,
		ImageRef:    result.Image.ImageRef,
		ResolvedRef: result.Image.ResolvedRef,
		ImageDigest: result.Image.Digest,
		Kernel:      recordedArtifact(opts.KernelPath),
		Rootfs:      recordedRootfsArtifact(result),
	}
	if opts.GuestInitPath != "" {
		if info, err := os.Stat(opts.GuestInitPath); err == nil && !info.IsDir() {
			verification.Init = recordedArtifact(opts.GuestInitPath)
		}
	}
	if configDisk := ConfigDiskFile(opts.StateDir, opts.Name); configDisk != "" {
		if info, err := os.Stat(configDisk); err == nil && !info.IsDir() {
			verification.Config = recordedArtifact(configDisk)
		}
	}
	for _, artifact := range []struct {
		name     string
		artifact *vmkit.VerifiedArtifact
	}{
		{name: "kernel", artifact: verification.Kernel},
		{name: "rootfs", artifact: verification.Rootfs},
		{name: "init", artifact: verification.Init},
		{name: "config", artifact: verification.Config},
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

// recordedRootfsArtifact avoids re-reading a multi-gigabyte private disk at
// the exact moment BuildRootfs has cloned it from a measured immutable base.
// CopyFile either completes the reflink/byte copy or fails, and no guest has
// run yet, so the new disk's initial content identity is the base identity.
// Later stopped-workspace verification still hashes the private disk and
// reports divergence after legitimate or unexpected guest writes.
func recordedRootfsArtifact(result Result) *vmkit.VerifiedArtifact {
	base := result.Image.RootfsBase
	if result.Image.BuilderPhase != "copy-baseline" || base == nil || !base.Immutable || len(base.SHA256) != sha256.Size*2 {
		return recordedArtifact(result.RootfsPath)
	}
	if _, err := hex.DecodeString(base.SHA256); err != nil {
		return recordedArtifact(result.RootfsPath)
	}
	artifact := &vmkit.VerifiedArtifact{Path: result.RootfsPath, SHA256: strings.ToLower(base.SHA256)}
	info, err := os.Stat(result.RootfsPath)
	if err != nil {
		artifact.Error = err.Error()
		return artifact
	}
	if !info.Mode().IsRegular() {
		artifact.Error = "private rootfs is not a regular file"
		return artifact
	}
	if result.Image.SizeBytes > 0 && info.Size() != result.Image.SizeBytes {
		artifact.Error = fmt.Sprintf("private rootfs size changed during derivation: expected %d bytes, got %d", result.Image.SizeBytes, info.Size())
	}
	return artifact
}

// RefreshManifestVerificationConfig re-records the config-disk artifact in
// the workspace manifest's verification block after a boot regenerates the
// disk. It is a targeted read-modify-write on purpose: applyManifest is
// lossy (it never restores verification, declared outputs, or health into
// Options), so a wholesale WriteManifest from Start would erase create-time
// records.
func RefreshManifestVerificationConfig(stateDir, name string) error {
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return err
	}
	if manifest.Verification == nil {
		return nil
	}
	recorded := recordedArtifact(ConfigDiskFile(stateDir, name))
	if recorded != nil && recorded.Error != "" {
		return fmt.Errorf("record config disk verification: %s", recorded.Error)
	}
	manifest.Verification.Config = recorded
	opts := Options{StateDir: stateDir, Name: name, Purpose: manifest.Purpose, CorrelationID: manifest.CorrelationID}
	return writeManifestRecord(opts, manifest, "boot_verification")
}

// finalizeSetupVerification commits a successful setup boot as one coherent
// checkpoint. Setup is allowed to change the writable rootfs, and the final
// config disk differs from the one-shot setup config, so both artifacts must
// be measured before SetupComplete becomes durable. Kernel and guest init are
// deliberately retained from the pre-boot record: setup does not authorize
// replacing either host-owned artifact.
func finalizeSetupVerification(stateDir, name, rootfsPath string) (*vmkit.RuntimeVerification, error) {
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return nil, err
	}
	if manifest.Verification == nil {
		return nil, fmt.Errorf("finalize setup verification: workspace verification record is missing")
	}
	rootfs := recordedArtifact(rootfsPath)
	if rootfs.Error != "" {
		return nil, fmt.Errorf("record setup rootfs verification: %s", rootfs.Error)
	}
	config := recordedArtifact(ConfigDiskFile(stateDir, name))
	if config.Error != "" {
		return nil, fmt.Errorf("record setup config disk verification: %s", config.Error)
	}

	manifest.SetupComplete = true
	manifest.Verification.Rootfs = rootfs
	manifest.Verification.Config = config
	manifest.Verification.OK = true
	manifest.Verification.Divergence = nil
	opts := Options{StateDir: stateDir, Name: name, Purpose: manifest.Purpose, CorrelationID: manifest.CorrelationID}
	if err := writeManifestRecord(opts, manifest, "setup_complete"); err != nil {
		return nil, err
	}
	return manifest.Verification, nil
}

func writeManifestRecord(opts Options, manifest Manifest, trigger string) error {
	path := filepath.Join(opts.StateDir, "workspaces", opts.Name, "workspace.json")
	previous, existed, err := readConstraintCurrent(path)
	if err != nil {
		return err
	}
	if err := writeJSONFile(path, manifest); err != nil {
		return err
	}
	if err := appendConstraintRevision(opts, trigger, &manifest); err != nil {
		if rollbackErr := rollbackConstraintCurrent(path, previous, existed); rollbackErr != nil {
			return fmt.Errorf("record constraint revision: %v (rollback current manifest: %w)", err, rollbackErr)
		}
		return err
	}
	return nil
}

// workspaceNameRE bounds workspace names to a shell-, path-, and
// hostname-safe shape: start with a letter or digit, then letters, digits,
// '.', '_' or '-', 63 characters total at most. Anything looser leaks into
// every surface a name touches — state-dir paths, serial-log paths, guest
// hostnames, CLI arguments — and lets an unexpanded shell glob ("m2*") pass
// as a plausible name.
var workspaceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// reservedWorkspaceNames are the state-directory infrastructure entries a
// workspace's own runtime directory (<state-dir>/<name>/) would collide
// with.
var reservedWorkspaceNames = map[string]bool{
	"build":        true,
	"host-workers": true,
	"images":       true,
	"kernels":      true,
	"models":       true,
	"oci":          true,
	"runners":      true,
	"volumes":      true,
	"workspaces":   true,
}

func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return operation.New(operation.ErrorValidation, "workspace name is required")
	}
	if !workspaceNameRE.MatchString(name) {
		return operation.New(operation.ErrorValidation, "invalid workspace name %q: use letters, digits, '.', '_' or '-', starting with a letter or digit, 63 characters max", name)
	}
	if reservedWorkspaceNames[name] {
		return operation.New(operation.ErrorValidation, "invalid workspace name %q: reserved for microagent state", name)
	}
	return nil
}

// validateTag enforces the backend-neutral snapshot tag grammar at the public
// library boundary. Tags become host directory names, so callers must never
// pass arbitrary path components through to a backend.
func validateTag(tag string) error {
	if strings.TrimSpace(tag) == "" {
		return operation.New(operation.ErrorValidation, "snapshot tag is required")
	}
	if !vmkit.SafeSnapshotTag(tag) {
		return operation.New(operation.ErrorValidation, "invalid snapshot tag %q: use letters, digits, '.', '_' or '-', starting with a letter or digit, 63 characters max", tag)
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
	if disk.Name == "rootfs" || disk.Name == "config" {
		return fmt.Errorf("disk name %s is reserved", disk.Name)
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
	if err := containmentBlocked(stateDir, name); err != nil {
		return err
	}
	state, pid, err := LatestStartState(stateDir, name)
	if err != nil {
		return err
	}
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed:
		held, err := RuntimeLeaseHeld(stateDir, name)
		if err != nil {
			return fmt.Errorf("check workspace %s runtime lease: %w", name, err)
		}
		if held {
			return operation.New(operation.ErrorConflict, "workspace %s still holds its runtime lease; a VM may be running outside this process namespace", name)
		}
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

// RuntimeLeasePath is the namespace-independent lifetime lock for a workspace.
// Unlike a recorded PID, a flock remains visible across PID namespaces.
func RuntimeLeasePath(stateDir, name string) string {
	return filepath.Join(stateDir, name, ".runtime.lock")
}

// EgressMediatorLeasePath is the namespace-independent liveness token for a
// workspace's egress mediator, held by the mediator process itself for its whole
// life.
//
// It exists because the recorded mediator PID is not a handle on the mediator
// from here. The supervisor that spawns the mediator records the PID it sees,
// and under user networking that supervisor runs inside the nested PID
// namespace pasta created — so the number it records is small and namespace
// local. Resolving it against this process's /proc answers for whatever
// unrelated process wears that number here, which is how a healthy mediator
// reads as an enforcement failure and how a dead one can read as alive. Like the
// runtime lease, an flock crosses that boundary: it is held by the mediator, on
// this workspace's own path, and is visible from any namespace.
func EgressMediatorLeasePath(stateDir, name string) string {
	return filepath.Join(stateDir, name, ".egress-mediator.lock")
}

// RuntimeLeaseHeld reports whether a live runtime owns the workspace lease.
// It never waits: observation and start admission must fail promptly when a
// different process owns the lock.
func RuntimeLeaseHeld(stateDir, name string) (bool, error) {
	path := RuntimeLeasePath(stateDir, name)
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	release, acquired, err := fsutil.TryLock(path)
	if err != nil {
		return false, err
	}
	if !acquired {
		return true, nil
	}
	if err := release(); err != nil {
		return false, err
	}
	return false, nil
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

// NewSessionID identifies one concrete VM execution lifetime. A resumed,
// restored, or forked VM receives a new ID and links back through
// Identity.SourceSessionID rather than reusing the prior execution identity.
func NewSessionID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "session-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("session-%d", time.Now().UnixNano())
}

func normalizeLifecycleOptions(opts *Options, requireDisk bool) error {
	defaults := DefaultOptions()
	if opts.HeadroomMiB < 0 {
		return operation.New(operation.ErrorValidation, "headroom must be zero or positive, got %d MiB", opts.HeadroomMiB)
	}
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
	if err := ValidateArch(opts.Architecture); err != nil {
		return err
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
	if opts.DebugfsPath == "" {
		opts.DebugfsPath = DebugfsPath()
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
	if manifest.Network.Mode != "" || len(manifest.Network.PortForwards) != 0 || len(manifest.Network.DNS) != 0 || len(manifest.Network.Routes) != 0 || manifest.Network.IP != "" || manifest.Network.Subnet != "" || manifest.Network.Gateway != "" || manifest.Network.IPv6 != "" || manifest.Network.IPv6Subnet != "" || manifest.Network.IPv6Gateway != "" {
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
	if manifest.Resources.HeadroomMiB != 0 {
		opts.HeadroomMiB = manifest.Resources.HeadroomMiB
	}
	opts.SizeDerived = manifest.SizeDerived
	opts.Disks = manifest.Disks
	opts.RootfsBase = manifest.RootfsBase
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
	opts.AllowGuestSetuid = manifest.AllowGuestSetuid
	if len(manifest.OnDemandSecrets) > 0 {
		opts.OnDemandSecrets = make(map[string]string, len(manifest.OnDemandSecrets))
		for _, ref := range manifest.OnDemandSecrets {
			opts.OnDemandSecrets[ref.Name] = ref.Ref
		}
	} else {
		opts.OnDemandSecrets = nil
	}
	opts.SecretsAudit = manifest.SecretsAudit
	opts.CapabilityRiskAcknowledgement = manifest.CapabilityRiskAcknowledgement
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
	// Egress caps were resolved once at create time (default, explicit value,
	// or explicit disable) and are restored as-is; marking them Explicit
	// stops EgressPolicyFromOptions from re-deriving a fresh default on top
	// of an already-decided value on every subsequent start. A workspace
	// created before this field existed restores a genuine zero and stays
	// unbounded, matching its pre-existing behavior rather than retroactively
	// bounding it.
	opts.EgressMaxBytesPerSec = manifest.EgressMaxBytesPerSec
	opts.EgressMaxTotalBytes = manifest.EgressMaxTotalBytes
	opts.EgressMaxConcurrentConns = manifest.EgressMaxConcurrentConns
	opts.EgressMaxTotalBytesExplicit = true
	opts.EgressMaxConcurrentConnsExplicit = true
	opts.Broker = manifest.Broker
	opts.Brokers = manifest.Brokers
	// Boot config: the manifest is authoritative on start — nothing is
	// baked into the rootfs, so these fields ARE the workspace's boot
	// behavior and must be assigned unconditionally.
	if strings.TrimSpace(manifest.Entrypoint) != "" {
		opts.Entrypoint = strings.TrimSpace(manifest.Entrypoint)
	}
	opts.Env = manifest.Env
	opts.UseImageCommand = manifest.UseImageCommand
	opts.ImageEnv = manifest.ImageEnv
	opts.ImageEntrypoint = manifest.ImageEntrypoint
	opts.ImageCmd = manifest.ImageCmd
	opts.ImageDefaults = effectiveWorkspaceImageDefaults(manifest.ImageDefaults, manifest.ImageEnv, manifest.ImageEntrypoint, manifest.ImageCmd)
	opts.Files = manifest.Files
	opts.SetupCommands = manifest.SetupCommands
	if strings.TrimSpace(manifest.ExecCommand) != "" {
		opts.ExecCommand = strings.TrimSpace(manifest.ExecCommand)
	}
	opts.SetupComplete = manifest.SetupComplete
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
	waitForDatapath := appleVFDatapathStartupRequired(opts, req)
	startupPath := filepath.Join(opts.StateDir, opts.Name, "datapath-startup.json")
	if waitForDatapath {
		if err := os.Remove(startupPath); err != nil && !os.IsNotExist(err) {
			return vmkit.Response{}, fmt.Errorf("remove stale Apple VF datapath startup status: %w", err)
		}
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
	if waitForDatapath {
		emitWorkspaceProgress(opts, progressOperationStart, "Start workspace", "start_interface", "waiting for the mediated network interface")
		status, waitErr := waitForAppleVFDatapathStartup(startupPath, opts)
		if waitErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			_ = WriteProcessState(opts, req, vmkit.StateFailed, 0, waitErr.Error())
			return vmkit.Response{Backend: opts.Backend, Error: waitErr.Error()}, waitErr
		}
		if !status.OK {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			failure := status.Failure
			if failure == nil {
				failure = &vmkit.DatapathStartupFailure{
					Boundary:        "apple-vf.host-fd.datapath",
					ExecutablePath:  vmkit.ResolveEgressDatapathBin(),
					DiagnosticsPath: filepath.Join(opts.StateDir, opts.Name, "datapath.log"),
					Reason:          "datapath startup failed without a diagnostic record",
				}
			}
			err := fmt.Errorf("%s", failure.Error())
			_ = WriteProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return vmkit.Response{
				Backend:                opts.Backend,
				DatapathStartupFailure: failure,
				Error:                  err.Error(),
			}, err
		}
	}
	_ = cmd.Process.Release()
	event := vmkit.Event{
		EventID:    fmt.Sprintf("event-%d", time.Now().UnixNano()),
		Identity:   *req.Identity,
		State:      vmkit.StateRunning,
		Detail:     "serial=" + SerialLogPath(opts.StateDir, opts.Name),
		ObservedAt: time.Now().UTC(),
		Lifecycle:  req.Lifecycle,
	}
	return vmkit.Response{OK: true, Backend: opts.Backend, Event: &event}, nil
}

type appleVFDatapathStartupStatus struct {
	OK      bool                          `json:"ok"`
	Failure *vmkit.DatapathStartupFailure `json:"failure,omitempty"`
}

func appleVFDatapathStartupRequired(opts Options, req vmkit.Request) bool {
	if opts.Backend != vmkit.BackendAppleVF || req.Config == nil || !vmkit.EgressMediationOn(req.Config.EgressMode) {
		return false
	}
	networkMode := ""
	if req.Config.Network != nil {
		networkMode = req.Config.Network.Mode
	}
	return vmkit.NetworkModeMediates(networkMode)
}

func waitForAppleVFDatapathStartup(path string, opts Options) (appleVFDatapathStartupStatus, error) {
	const timeout = 6 * time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var status appleVFDatapathStartupStatus
			if err := json.Unmarshal(data, &status); err != nil {
				return appleVFDatapathStartupStatus{}, fmt.Errorf("decode Apple VF datapath startup status: %w", err)
			}
			return status, nil
		}
		if !os.IsNotExist(err) {
			return appleVFDatapathStartupStatus{}, fmt.Errorf("read Apple VF datapath startup status: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	failure := vmkit.DatapathStartupFailure{
		Boundary:        "apple-vf.host-fd.datapath",
		ExecutablePath:  vmkit.ResolveEgressDatapathBin(),
		DiagnosticsPath: filepath.Join(opts.StateDir, opts.Name, "datapath.log"),
		Reason:          fmt.Sprintf("supervisor did not report datapath startup within %s", timeout),
	}
	return appleVFDatapathStartupStatus{OK: false, Failure: &failure}, nil
}

func supervisorEnvironment(opts Options) []string {
	env := os.Environ()
	if opts.Backend != vmkit.BackendAppleVF {
		return env
	}
	return env
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
		Lifecycle:  req.Lifecycle,
	}
	fileEvent := EventFile{
		EventID:    event.EventID,
		Identity:   *req.Identity,
		State:      state,
		Detail:     event.Detail,
		ObservedAt: event.ObservedAt.Format(time.RFC3339),
		Lifecycle:  req.Lifecycle,
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
