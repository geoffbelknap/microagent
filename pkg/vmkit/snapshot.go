package vmkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Snapshot file names within a snapshot directory.
const (
	SnapshotManifestName        = "manifest.json"
	SnapshotVMStateName         = "vmstate"
	SnapshotMemoryName          = "memory"
	SnapshotRootfsName          = "rootfs.ext4"
	SnapshotAppleVFMachineState = "machine-state.vz"
	SnapshotAppleVFConfig       = "apple-vf-config.json"
	// SnapshotConfigDiskName is the captured per-boot guest config disk. The
	// VMM records the device's geometry in machine state, so a restore must
	// re-attach a byte-identical file — the captured copy is authoritative,
	// never a regenerated one.
	SnapshotConfigDiskName = "config.disk"
)

// SnapshotManifest records the metadata needed to restore or fork a workspace
// snapshot: the image and network identity it was taken from, the guest IP to
// re-establish on load, the kernel hash that guards against skew, the VM
// sizing, and when it was created. It is written alongside a coherent rootfs
// copy and backend-defined machine-state artifacts under the snapshot
// directory. The type is backend-neutral so the host-side CLI can list and
// remove snapshots without a running VM, while only snapshot create needs the
// backend supervisor.
type SnapshotManifest struct {
	Tag             string `json:"tag"`
	SourceSessionID string `json:"sourceSessionID,omitempty"`
	ImageRef        string `json:"imageRef,omitempty"`
	NetworkMode     string `json:"networkMode,omitempty"`
	GuestIP         string `json:"guestIP,omitempty"`
	KernelSHA256    string `json:"kernelSHA256,omitempty"`
	VCPUCount       int    `json:"vcpuCount"`
	MemoryMiB       int    `json:"memoryMiB"`
	CreatedAt       string `json:"createdAt"`
	// ShellPort and ExecPort are the guest vsock service ports baked into the
	// snapshot. A fork derives different ports from its own name, so it must
	// adopt these to reach the resumed guest's shell and exec services.
	ShellPort uint16 `json:"shellPort,omitempty"`
	ExecPort  uint16 `json:"execPort,omitempty"`
	// NetworkIP/NetworkGateway/NetworkSubnet capture the source's runtime
	// network addressing (user/nat modes). The resumed guest keeps the baked IP,
	// so a fork configures its own tap/pasta with this same addressing (in its
	// own namespace) rather than deriving a new subnet from its name.
	NetworkIP          string `json:"networkIP,omitempty"`
	NetworkGateway     string `json:"networkGateway,omitempty"`
	NetworkSubnet      string `json:"networkSubnet,omitempty"`
	NetworkIPv6        string `json:"networkIPv6,omitempty"`
	NetworkIPv6Gateway string `json:"networkIPv6Gateway,omitempty"`
	NetworkIPv6Subnet  string `json:"networkIPv6Subnet,omitempty"`
	// RootfsArtifact names the coherent rootfs copy inside the snapshot
	// directory. Empty means the legacy/default SnapshotRootfsName.
	RootfsArtifact string `json:"rootfsArtifact,omitempty"`
	// MachineStateArtifacts names backend-defined machine-state artifacts inside
	// the snapshot directory. Firecracker uses separate vmstate and memory files;
	// other backends may use a different shape, such as a single saved-state
	// file. Empty means the legacy Firecracker vmstate+memory pair.
	MachineStateArtifacts []SnapshotArtifact `json:"machineStateArtifacts,omitempty"`
	// VsockUDSPath is the absolute host path the snapshot's vsock device is
	// bound to (the source workspace's vsock socket). It is baked into the
	// Firecracker snapshot and cannot be remapped on load, so a fork into a
	// different workspace bind-mounts its own directory over the source's to
	// make this path resolve to the fork's socket.
	VsockUDSPath string `json:"vsockUDSPath,omitempty"`
	// SecretsMaterialized records that this snapshot source had secrets written
	// into guest memory. When true, SecretsPurged must also be true: a backend
	// must fail closed rather than capture a memory image that may contain guest
	// secret material.
	SecretsMaterialized bool `json:"secretsMaterialized,omitempty"`
	// SecretsPurged records that the guest tmpfs secrets were scrubbed before
	// the memory image was captured.
	SecretsPurged bool `json:"secretsPurged,omitempty"`
	// Egress posture captured at snapshot time so a restore/fork re-arms the
	// mediator with the SAME per-workspace CA the guest's baked trust store was
	// built against. EgressMode/EgressAllow/EgressPassthrough reproduce the
	// mediator's policy; EgressCASHA256 is the hex SHA-256 of the persisted CA
	// cert's DER, used at restore as a fail-closed integrity check before the CA
	// is reused (a fresh-minted CA would silently break every MITM handshake of
	// the restored guest).
	EgressMode            string   `json:"egressMode,omitempty"`
	EgressAllow           []string `json:"egressAllow,omitempty"`
	EgressPassthrough     []string `json:"egressPassthrough,omitempty"`
	EgressAllowlistLocked bool     `json:"egressAllowlistLocked,omitempty"`
	EgressSwapConfigPath  string   `json:"egressSwapConfigPath,omitempty"`
	EgressCASHA256        string   `json:"egressCASHA256,omitempty"`
	// Bounded-operations caps (ASK tenet 8) captured at snapshot time so a restored
	// workspace keeps the SAME bounds it was running under. All are per-mediator-
	// process and reset on restart; a zero value means unlimited. Re-applied on
	// restore by threading them back through the mediator flags.
	EgressMaxBytesPerSec     int64 `json:"egressMaxBytesPerSec,omitempty"`
	EgressMaxTotalBytes      int64 `json:"egressMaxTotalBytes,omitempty"`
	EgressMaxConcurrentConns int32 `json:"egressMaxConcurrentConns,omitempty"`
	EgressAuditMaxBytes      int64 `json:"egressAuditMaxBytes,omitempty"`
	EgressAuditMaxBackups    int   `json:"egressAuditMaxBackups,omitempty"`
}

// SnapshotArtifact describes one backend artifact stored under a snapshot
// directory. Path is relative to the snapshot directory; Kind is a stable
// backend-defined role such as "firecracker-vmstate" or
// "firecracker-memory".
type SnapshotArtifact struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// SnapshotInfo is a manifest plus the on-disk size of its snapshot directory,
// reported by snapshot list so operators can see what each tag costs.
type SnapshotInfo struct {
	SnapshotManifest
	SizeBytes int64 `json:"sizeBytes"`
}

// SnapshotRootfsArtifact returns the rootfs artifact path for this manifest,
// preserving compatibility with manifests written before RootfsArtifact existed.
func SnapshotRootfsArtifact(manifest SnapshotManifest) string {
	if manifest.RootfsArtifact != "" {
		return manifest.RootfsArtifact
	}
	return SnapshotRootfsName
}

// SnapshotMachineStateArtifacts returns backend machine-state artifact paths
// for this manifest, preserving compatibility with legacy Firecracker
// vmstate+memory snapshots.
func SnapshotMachineStateArtifacts(manifest SnapshotManifest) []SnapshotArtifact {
	if len(manifest.MachineStateArtifacts) > 0 {
		return append([]SnapshotArtifact(nil), manifest.MachineStateArtifacts...)
	}
	return FirecrackerSnapshotArtifacts()
}

// FirecrackerSnapshotArtifacts returns the default Firecracker machine-state
// artifacts: vmstate metadata plus the guest memory file.
func FirecrackerSnapshotArtifacts() []SnapshotArtifact {
	return []SnapshotArtifact{
		{Kind: "firecracker-vmstate", Path: SnapshotVMStateName},
		{Kind: "firecracker-memory", Path: SnapshotMemoryName},
		{Kind: "config-disk", Path: SnapshotConfigDiskName},
	}
}

// AppleVFSnapshotArtifacts returns the saved Virtualization.framework machine
// state artifact written by saveMachineStateTo.
func AppleVFSnapshotArtifacts() []SnapshotArtifact {
	return []SnapshotArtifact{
		{Kind: "apple-vf-machine-state", Path: SnapshotAppleVFMachineState},
		{Kind: "apple-vf-restore-config", Path: SnapshotAppleVFConfig},
		{Kind: "config-disk", Path: SnapshotConfigDiskName},
	}
}

// MaterializedSecretsDeclared reports whether the config declares secrets that
// are written into the guest tmpfs and therefore must be purged before memory
// snapshot capture. On-demand-only secrets are fetched per request and are not
// materialized into the snapshot source by default.
func MaterializedSecretsDeclared(config *Config) bool {
	if config == nil {
		return false
	}
	return len(config.Secrets) > 0 || len(config.SecretEnvFiles) > 0
}

// ValidateSnapshotSecretCapture enforces the backend-neutral secret safety
// invariant for memory snapshots: if the source had materialized guest secrets,
// the backend must prove it purged them before writing the memory image.
// ValidateSnapshotSecretCapture gates capturing a workspace whose secrets were
// materialized into guest memory. The default is fail-closed: no purge, no
// capture — a hibernation bundle lands in a shared store and must never carry
// plaintext.
//
// retainSecrets relaxes that gate for a FORENSIC capture, where credential
// material is the evidence: keys, tokens, and injected state exist only in
// volatile memory, so scrubbing them defeats the purpose of the capture. The
// resulting manifest records the truth (materialized, NOT purged), which
// ValidateSnapshotSecretRestore refuses — so a forensic capture can never be
// rehydrated as a workspace, and its manifest flags tell a consumer the
// artifact is secret-bearing and belongs in protected custody.
//
// Note the purge was never a guarantee of a secret-free image: it clears
// declared secrets from the guest tmpfs, but a credential the guest obtained at
// runtime lives in process memory and survives it. Treat every memory capture
// as potentially secret-bearing; this flag makes that explicit rather than
// changing whether it is true.
func ValidateSnapshotSecretCapture(config *Config, purged, retainSecrets bool) error {
	if retainSecrets {
		return nil
	}
	if MaterializedSecretsDeclared(config) && !purged {
		return fmt.Errorf("snapshot of secret-bearing workspace requires guest secret purge before memory capture")
	}
	return nil
}

// ValidateSnapshotSecretRestore enforces the backend-neutral secret safety
// invariant for snapshot restore/fork: a snapshot that captured a workspace with
// materialized guest secrets must have been purged at capture time, and the
// restore config must provide both the secret references and a guest control
// channel so the backend can rehydrate before treating the workspace as ready.
func ValidateSnapshotSecretRestore(manifest SnapshotManifest, config *Config) error {
	if !manifest.SecretsMaterialized {
		return nil
	}
	if !manifest.SecretsPurged {
		return fmt.Errorf("snapshot %q has materialized secrets but does not record guest secret purge; refusing restore", manifest.Tag)
	}
	if !MaterializedSecretsDeclared(config) {
		return fmt.Errorf("snapshot %q requires materialized secret references for restore rehydrate", manifest.Tag)
	}
	if config == nil || config.SecretsControlPort == 0 {
		return fmt.Errorf("snapshot %q requires a guest secrets control port for restore rehydrate", manifest.Tag)
	}
	return nil
}

// SnapshotsDir is the directory holding all snapshots for a workspace.
func SnapshotsDir(stateDir, name string) string {
	return filepath.Join(stateDir, name, "snapshots")
}

// SnapshotDir is the directory holding a single snapshot tag.
func SnapshotDir(stateDir, name, tag string) string {
	return filepath.Join(SnapshotsDir(stateDir, name), tag)
}

// SnapshotStagingParent is the directory snapshot captures stage into before
// being published atomically into SnapshotsDir. It lives under the workspace
// directory (same filesystem, so the publish renames never cross devices) but
// outside SnapshotsDir so a partially captured snapshot never appears in
// snapshot listings.
func SnapshotStagingParent(stateDir, name string) string {
	return filepath.Join(stateDir, name, ".snapshot-staging")
}

// PublishSnapshotDir atomically installs a freshly captured staging dir as the
// snapshot at finalDir. Any existing snapshot at the tag is moved aside first
// and removed only after the new one is renamed into place, so a failure never
// destroys a prior good snapshot at the tag (the data-loss this guards). All
// renames are within the same workspace directory (one filesystem), so there
// is no cross-device copy.
func PublishSnapshotDir(stagingDir, finalDir string) error {
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o700); err != nil {
		return err
	}
	// Keep the backup in the staging area (not under SnapshotsDir) so it never
	// shows up in ListSnapshots during the swap.
	backup := filepath.Join(filepath.Dir(stagingDir), filepath.Base(finalDir)+".superseded")
	_ = os.RemoveAll(backup) // clear any leftover from an interrupted publish
	moved := false
	if _, err := os.Stat(finalDir); err == nil {
		if err := os.Rename(finalDir, backup); err != nil {
			return fmt.Errorf("move existing snapshot aside: %w", err)
		}
		moved = true
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		if moved {
			_ = os.Rename(backup, finalDir) // roll back so the tag is never left empty
		}
		return fmt.Errorf("publish snapshot: %w", err)
	}
	if moved {
		_ = os.RemoveAll(backup)
	}
	return nil
}

// WriteSnapshotManifest writes the manifest into the snapshot directory,
// creating the directory if needed.
func WriteSnapshotManifest(dir string, manifest SnapshotManifest) error {
	if err := validateSnapshotManifestArtifacts(manifest); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SnapshotManifestName), append(data, '\n'), 0o600)
}

// ReadSnapshotManifest reads the manifest from a snapshot directory.
func ReadSnapshotManifest(dir string) (SnapshotManifest, error) {
	var manifest SnapshotManifest
	data, err := os.ReadFile(filepath.Join(dir, SnapshotManifestName))
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	if err := validateSnapshotManifestArtifacts(manifest); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

// validateSnapshotManifestArtifacts ensures every manifest-provided artifact
// path stays within its snapshot directory. Nested relative paths are allowed,
// but absolute, parent-relative, backslash, and non-canonical paths fail
// closed before any caller joins or opens them.
func validateSnapshotManifestArtifacts(manifest SnapshotManifest) error {
	if err := validateSnapshotArtifactPath("rootfs", SnapshotRootfsArtifact(manifest)); err != nil {
		return err
	}
	for _, artifact := range SnapshotMachineStateArtifacts(manifest) {
		if err := validateSnapshotArtifactPath("machine-state", artifact.Path); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotArtifactPath(kind, artifactPath string) error {
	if artifactPath == "" ||
		strings.ContainsRune(artifactPath, '\\') ||
		!filepath.IsLocal(artifactPath) ||
		filepath.Clean(artifactPath) != artifactPath ||
		artifactPath == "." {
		return fmt.Errorf("invalid snapshot %s artifact path %q: path must stay within the snapshot directory", kind, artifactPath)
	}
	return nil
}

// ListSnapshots returns the snapshots recorded for a workspace, ordered by
// creation time then tag, each with its total on-disk size. A workspace with no
// snapshots directory yields an empty slice, not an error.
func ListSnapshots(stateDir, name string) ([]SnapshotInfo, error) {
	root := SnapshotsDir(stateDir, name)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	infos := make([]SnapshotInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		manifest, err := ReadSnapshotManifest(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		size, err := dirSize(dir)
		if err != nil {
			return nil, err
		}
		infos = append(infos, SnapshotInfo{SnapshotManifest: manifest, SizeBytes: size})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].CreatedAt != infos[j].CreatedAt {
			return infos[i].CreatedAt < infos[j].CreatedAt
		}
		return infos[i].Tag < infos[j].Tag
	})
	return infos, nil
}

// RemoveSnapshot deletes a single snapshot tag. The tag must be a safe basename
// so a caller cannot escape the snapshots directory, and the tag must exist.
func RemoveSnapshot(stateDir, name, tag string) error {
	if !SafeSnapshotTag(tag) {
		return invalidSnapshotTagError(tag)
	}
	dir := SnapshotDir(stateDir, name, tag)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot %q not found for workspace %s", tag, name)
		}
		return err
	}
	return os.RemoveAll(dir)
}

func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
