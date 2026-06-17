package vmkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Snapshot file names within a snapshot directory.
const (
	SnapshotManifestName = "manifest.json"
	SnapshotVMStateName  = "vmstate"
	SnapshotMemoryName   = "memory"
	SnapshotRootfsName   = "rootfs.ext4"
)

// SnapshotManifest records the metadata needed to restore or fork a workspace
// snapshot: the image and network identity it was taken from, the guest IP to
// re-establish on load, the kernel hash that guards against skew, the VM
// sizing, and when it was created. It is written alongside the backend vmstate,
// memory file, and rootfs copy under the snapshot directory. The type is
// backend-neutral so the host-side CLI can list and remove snapshots without a
// running VM, while only snapshot create needs the backend supervisor.
type SnapshotManifest struct {
	Tag          string `json:"tag"`
	ImageRef     string `json:"imageRef,omitempty"`
	NetworkMode  string `json:"networkMode,omitempty"`
	GuestIP      string `json:"guestIP,omitempty"`
	KernelSHA256 string `json:"kernelSHA256,omitempty"`
	VCPUCount    int    `json:"vcpuCount"`
	MemoryMiB    int    `json:"memoryMiB"`
	CreatedAt    string `json:"createdAt"`
	// ShellPort and ExecPort are the guest vsock service ports baked into the
	// snapshot. A fork derives different ports from its own name, so it must
	// adopt these to reach the resumed guest's shell and exec services.
	ShellPort uint16 `json:"shellPort,omitempty"`
	ExecPort  uint16 `json:"execPort,omitempty"`
	// NetworkIP/NetworkGateway/NetworkSubnet capture the source's runtime
	// network addressing (user/nat modes). The resumed guest keeps the baked IP,
	// so a fork configures its own tap/pasta with this same addressing (in its
	// own namespace) rather than deriving a new subnet from its name.
	NetworkIP      string `json:"networkIP,omitempty"`
	NetworkGateway string `json:"networkGateway,omitempty"`
	NetworkSubnet  string `json:"networkSubnet,omitempty"`
	// VsockUDSPath is the absolute host path the snapshot's vsock device is
	// bound to (the source workspace's vsock socket). It is baked into the
	// Firecracker snapshot and cannot be remapped on load, so a fork into a
	// different workspace bind-mounts its own directory over the source's to
	// make this path resolve to the fork's socket.
	VsockUDSPath string `json:"vsockUDSPath,omitempty"`
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
	EgressMode        string   `json:"egressMode,omitempty"`
	EgressAllow       []string `json:"egressAllow,omitempty"`
	EgressPassthrough []string `json:"egressPassthrough,omitempty"`
	EgressCASHA256    string   `json:"egressCASHA256,omitempty"`
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

// SnapshotInfo is a manifest plus the on-disk size of its snapshot directory,
// reported by snapshot list so operators can see what each tag costs.
type SnapshotInfo struct {
	SnapshotManifest
	SizeBytes int64 `json:"sizeBytes"`
}

// SnapshotsDir is the directory holding all snapshots for a workspace.
func SnapshotsDir(stateDir, name string) string {
	return filepath.Join(stateDir, name, "snapshots")
}

// SnapshotDir is the directory holding a single snapshot tag.
func SnapshotDir(stateDir, name, tag string) string {
	return filepath.Join(SnapshotsDir(stateDir, name), tag)
}

// WriteSnapshotManifest writes the manifest into the snapshot directory,
// creating the directory if needed.
func WriteSnapshotManifest(dir string, manifest SnapshotManifest) error {
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
	return manifest, json.Unmarshal(data, &manifest)
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
	if !SafeIdentifier(tag) {
		return fmt.Errorf("snapshot tag must be a safe basename: %s", tag)
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
