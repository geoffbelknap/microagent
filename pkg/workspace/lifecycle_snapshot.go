package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// Snapshot captures a tagged snapshot of a running or paused workspace via the
// backend supervisor and returns the resulting manifest, enriched with the
// workspace image reference. An empty tag receives DefaultSnapshotTag. A
// running workspace is briefly paused and resumed around the capture; an
// already-paused workspace stays paused. Memory comes from a live VM, so
// quarantine (which stops the runtime) makes a workspace uncapturable —
// capture BEFORE containing when volatile state matters.
func Snapshot(ctx context.Context, opts Options, tag string) (vmkit.SnapshotManifest, error) {
	return snapshotWith(ctx, opts, tag, false)
}

// SnapshotForensic captures for INVESTIGATION rather than restore: the guest
// secret purge is skipped, because credential material is the evidence and
// exists only in volatile memory. An empty tag receives
// DefaultForensicSnapshotTag. The resulting manifest records secrets as
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
	tag = strings.TrimSpace(tag)
	if tag == "" {
		now := time.Now()
		tag = DefaultSnapshotTag(now)
		if retainSecrets {
			tag = DefaultForensicSnapshotTag(now)
		}
	}
	if err := validateTag(tag); err != nil {
		return vmkit.SnapshotManifest{}, err
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
	// The saved machine state records the config disk's device geometry;
	// restores must re-attach a byte-identical file.
	if state.Config.ConfigDiskPath != "" {
		if err := CopyFile(state.Config.ConfigDiskPath, filepath.Join(dir, vmkit.SnapshotConfigDiskName), 0o600); err != nil {
			return fmt.Errorf("copy config disk into snapshot: %w", err)
		}
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
	netIPv6, netIPv6Gateway, netIPv6Subnet := "", "", ""
	if state.Config.Network != nil {
		mode = strings.TrimSpace(state.Config.Network.Mode)
		guestIP = guestIPFromNetwork(*state.Config.Network)
		netIP = strings.TrimSpace(state.Config.Network.IP)
		netGateway = strings.TrimSpace(state.Config.Network.Gateway)
		netSubnet = strings.TrimSpace(state.Config.Network.Subnet)
		netIPv6 = strings.TrimSpace(state.Config.Network.IPv6)
		netIPv6Gateway = strings.TrimSpace(state.Config.Network.IPv6Gateway)
		netIPv6Subnet = strings.TrimSpace(state.Config.Network.IPv6Subnet)
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
		SourceSessionID:          state.Event.Identity.SessionID,
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
		NetworkIPv6:              netIPv6,
		NetworkIPv6Gateway:       netIPv6Gateway,
		NetworkIPv6Subnet:        netIPv6Subnet,
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
	tag = strings.TrimSpace(tag)
	if err := validateTag(tag); err != nil {
		return Result{}, err
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
	applyBoundedOperationsDefaults(&opts)
	if err := validateCapabilityComposition(opts); err != nil {
		return Result{CapabilityComposition: EvaluateCapabilityComposition(opts)}, err
	}
	// Same contract as Create: a dry run stops after validation, before the
	// first side effect. Everything above is checks and local reads — the
	// snapshot manifest included, so a dry run still reports a missing or
	// unsupported snapshot. This path used to ignore DryRun entirely, so the
	// MCP adapter (whose workspace.create documents dry_run for snapshot
	// forks) performed the real fork when asked not to.
	if opts.DryRun {
		return dryRunResult(opts), nil
	}
	if err := EnsureKernel(ctx, &opts); err != nil {
		return Result{}, err
	}
	if err := EnsureCanCreate(opts); err != nil {
		return Result{}, err
	}
	if err := EnsureWorkspaceCapacity(opts); err != nil {
		return Result{}, err
	}
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o700); err != nil {
		return Result{}, err
	}
	if err := CopyFile(filepath.Join(srcDir, vmkit.SnapshotRootfsArtifact(manifest)), rootfsPath, 0o600); err != nil {
		return Result{}, fmt.Errorf("copy snapshot rootfs into fork: %w", err)
	}
	if err := writeManifest(opts, "snapshot_fork"); err != nil {
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
		IPv6:         manifest.NetworkIPv6,
		IPv6Gateway:  manifest.NetworkIPv6Gateway,
		IPv6Subnet:   manifest.NetworkIPv6Subnet,
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
	if err := CopyFileReplace(filepath.Join(dir, vmkit.SnapshotRootfsArtifact(manifest)), req.Config.RootfsPath, 0o600); err != nil {
		return fmt.Errorf("restore snapshot rootfs: %w", err)
	}
	// Restore the captured config disk beside the rootfs; a
	// pre-config-disk snapshot has none and its machine state expects no
	// config device, so absence is legitimate.
	captured := filepath.Join(dir, vmkit.SnapshotConfigDiskName)
	if _, statErr := os.Stat(captured); statErr == nil && req.Config.ConfigDiskPath != "" {
		if err := CopyFileReplace(captured, req.Config.ConfigDiskPath, 0o600); err != nil {
			return fmt.Errorf("restore snapshot config disk: %w", err)
		}
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
	configDiskPath := config.ConfigDiskPath
	vsockListeners := config.VsockListeners
	identityShellPort := config.ShellPort
	identityExecPort := config.ExecPort
	guestShellPort := config.GuestShellPort
	guestExecPort := config.GuestExecPort
	saved.KernelPath = kernelPath
	saved.RootfsPath = rootfsPath
	saved.StateDir = stateDir
	// The fork/restore attaches ITS OWN copy of the captured config disk —
	// the saved config points at the source workspace's path. A snapshot
	// taken before config disks existed keeps none: its machine state
	// expects no config device, and attaching one would fail the restore.
	if saved.ConfigDiskPath != "" {
		saved.ConfigDiskPath = configDiskPath
	}
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
			// A pre-config-disk snapshot has no captured config disk — and
			// its machine state expects no config device either, so absence
			// is legitimate for that artifact alone.
			if name == vmkit.SnapshotConfigDiskName && os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("copy snapshot %s into fork: %w", name, err)
		}
	}
	return nil
}
