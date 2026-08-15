package rootfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const (
	DefaultInitPath = "/sbin/microagent-init"
	DefaultSizeMiB  = 1024
	FormatExt4      = "ext4"
)

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type ProgressEvent struct {
	Phase         string
	Message       string
	Current       int64
	Total         int64
	Bytes         int64
	TotalBytes    int64
	Indeterminate bool
}

type ProgressFunc func(ProgressEvent)

type BuildRequest struct {
	// LocalImageLayout, when set, is a committed-OCI layout path
	// (commit.LayoutPath(stateDir)) consulted for the image ref BEFORE any
	// remote registry. A ref present there is used locally with no network —
	// standard local-first image resolution, the same as docker/containerd.
	// Security note: this means a locally committed image shadows a
	// same-ref remote image; that is expected local-first behavior, not a
	// bypass, since the local layout is only ever populated by this host's
	// own commits. Empty preserves remote-only behavior (callers opt in by
	// setting it). A local lookup miss or error falls back to the remote
	// registry rather than failing the build.
	LocalImageLayout string `json:"local_image_layout,omitempty"`
	// BaseCacheDir, when set, points at a digest-keyed cache of extracted
	// base image trees shared across builds. The manifest digest is still
	// resolved from the source on every build; a cache entry only ever
	// substitutes bytes for the digest that resolution just named. Empty
	// disables caching. BaseCacheDirFor derives the standard location for
	// a state directory, honoring the environment override.
	BaseCacheDir   string   `json:"base_cache_dir,omitempty"`
	ImageRef       string   `json:"image_ref"`
	Platform       Platform `json:"platform"`
	OutputPath     string   `json:"output_path"`
	Format         string   `json:"format,omitempty"`
	InitPath       string   `json:"init_path,omitempty"`
	InitBinaryPath string   `json:"init_binary_path,omitempty"`
	// Command/Env/ConsoleShell/Files apply only to the script-init fallback
	// (no InitBinaryPath): those images have no config channel, so the
	// script inlines them. Binary-init images carry nothing per-workspace —
	// boot config arrives on the per-boot config disk.
	Command        []string `json:"command,omitempty"`
	ConsoleShell   string   `json:"console_shell,omitempty"`
	NoImageCommand bool     `json:"no_image_command,omitempty"`
	StateDir       string   `json:"state_dir,omitempty"`
	Mke2fsPath     string   `json:"mke2fs_path,omitempty"`
	// DebugfsPath resolves the debugfs binary used after mke2fs to restore
	// OCI filesystem metadata onto the built ext4 image. mke2fs -d only
	// encodes the stage directory's host-observed metadata.
	DebugfsPath string `json:"debugfs_path,omitempty"`
	SizeMiB     int64  `json:"size_mib,omitempty"`
	// AutoSize treats SizeMiB as a starting point rather than a limit: when
	// the unpacked image doesn't fit, the disk grows to hold it plus free
	// space. Set it when the caller didn't pin a size explicitly.
	AutoSize bool `json:"auto_size,omitempty"`
	// DeriveSize sizes the disk from the image content alone: content plus
	// headroom, rounded up to a whole GiB, whether that lands below or above
	// SizeMiB. Set it when nothing pinned a size — no explicit size, no spec
	// size, no explicitly chosen profile — so a small image gets a small
	// disk instead of a profile constant. Implies the AutoSize grow
	// behavior.
	DeriveSize bool `json:"derive_size,omitempty"`
	// HeadroomMiB is the writable space the sized disk guarantees the guest
	// beyond the image content. Zero means the default (512 MiB).
	HeadroomMiB   int64             `json:"headroom_mib,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Files         []File            `json:"files,omitempty"`
	AllowMutable  bool              `json:"allow_mutable,omitempty"`
	KeepStage     bool              `json:"keep_stage,omitempty"`
	StageSnapshot string            `json:"stage_snapshot,omitempty"`
	// AllowGuestSetuid preserves setuid/setgid mode bits from the source
	// image (and declared Files) in the built rootfs. By default the builder
	// strips them: a guest workload runs as the user the workspace declares,
	// and a setuid binary inherited from a base image (su, mount, passwd in
	// any stock distro) is a privilege-escalation path inside the guest that
	// nothing asked for. Set this for workspaces that need the devcontainer
	// pattern — a non-root user with working sudo. Sticky bits are always
	// preserved. The choice is recorded in Provenance.SetuidPolicy and keys
	// both the base-stage cache and rootfs-baseline reuse, so stripped and
	// preserved builds never share artifacts.
	AllowGuestSetuid bool         `json:"allow_guest_setuid,omitempty"`
	Progress         ProgressFunc `json:"-"`
}

type File struct {
	SourcePath string `json:"source_path"`
	Path       string `json:"path"`
	Mode       string `json:"mode,omitempty"`
}

type Mount struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	Mode       string `json:"mode"`
}

type PortForward struct {
	Protocol  string `json:"protocol"`
	Host      string `json:"host,omitempty"`
	HostPort  uint16 `json:"hostPort"`
	GuestPort uint16 `json:"guestPort"`
}

// ImageDefaults is the durable subset of the OCI image configuration. It is
// shared with status responses so persisted and inspected values cannot drift.
type ImageDefaults = vmkit.OCIImageDefaults

type BundleRequest struct {
	SourcePath  string `json:"source_path"`
	OutputPath  string `json:"output_path"`
	Format      string `json:"format,omitempty"`
	StateDir    string `json:"state_dir,omitempty"`
	Mke2fsPath  string `json:"mke2fs_path,omitempty"`
	DebugfsPath string `json:"debugfs_path,omitempty"`
	SizeMiB     int64  `json:"size_mib,omitempty"`
	// AutoSize grows the disk past SizeMiB when the bundle contents don't fit.
	AutoSize bool `json:"auto_size,omitempty"`
	// AllowGuestSetuid preserves setuid/setgid bits from the bundle tar. The
	// default strips them, same policy as BuildRequest: a data disk mounted
	// into a guest is as much an escalation surface as the rootfs.
	AllowGuestSetuid bool `json:"allow_guest_setuid,omitempty"`
}

type BundleProvenance struct {
	SourcePath   string `json:"source_path"`
	OutputPath   string `json:"output_path"`
	Format       string `json:"format,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Builder      string `json:"builder"`
	BuilderPhase string `json:"builder_phase"`
	// SetuidPolicy / SetuidStripped*: see Provenance.
	SetuidPolicy        string   `json:"setuid_policy,omitempty"`
	SetuidStrippedCount int      `json:"setuid_stripped_count,omitempty"`
	SetuidStripped      []string `json:"setuid_stripped,omitempty"`
}

// BaseSource values recorded in Provenance: where the base image content
// came from this build. The manifest digest is resolved from the source on
// every build regardless, so BaseSourceCache never means "an old resolution
// was reused" — only that already-verified bytes for the freshly resolved
// digest were restored instead of downloaded again.
const (
	BaseSourceRegistry    = "registry"
	BaseSourceLocalLayout = "local-layout"
	BaseSourceCache       = "cache"
)

// SetuidPolicy values recorded in Provenance. An empty value means the build
// predates the policy — consumers deciding reuse must treat it as unknown and
// rebuild, never assume either behavior.
const (
	SetuidPolicyStripped  = "stripped"
	SetuidPolicyPreserved = "preserved"
)

// setuidStrippedListCap bounds the paths recorded in provenance and cache
// metadata; SetuidStrippedCount always carries the full total. A stock
// distro image strips about a dozen entries, so the cap only trims
// pathological images.
const setuidStrippedListCap = 32

type Provenance struct {
	ImageRef    string   `json:"image_ref"`
	ResolvedRef string   `json:"resolved_ref,omitempty"`
	Digest      string   `json:"digest,omitempty"`
	Platform    Platform `json:"platform"`
	OutputPath  string   `json:"output_path"`
	Format      string   `json:"format,omitempty"`
	InitPath    string   `json:"init_path,omitempty"`
	SizeBytes   int64    `json:"size_bytes,omitempty"`
	Builder     string   `json:"builder"`
	// BuilderPhase tracks the last phase the build reached; BaseSource is
	// the durable record of where the base content came from (one of the
	// BaseSource* constants), set once and never overwritten by later
	// phases.
	BuilderPhase  string   `json:"builder_phase"`
	BaseSource    string   `json:"base_source,omitempty"`
	StageDir      string   `json:"stage_dir,omitempty"`
	StageSnapshot string   `json:"stage_snapshot,omitempty"`
	LayerDigests  []string `json:"layer_digests,omitempty"`
	// ImageEnv/ImageEntrypoint/ImageCmd capture the OCI image config at the
	// only point it is available — the build. Boot-time guest config
	// assembly needs them (image env merge, --image-command) because
	// nothing is baked into the rootfs.
	ImageEnv        []string `json:"image_env,omitempty"`
	ImageEntrypoint []string `json:"image_entrypoint,omitempty"`
	ImageCmd        []string `json:"image_cmd,omitempty"`
	// SetuidPolicy records whether setuid/setgid bits were stripped
	// (SetuidPolicyStripped, the default) or preserved on request
	// (SetuidPolicyPreserved). Empty means the record predates the policy.
	// SetuidStripped lists the stripped paths (capped at
	// setuidStrippedListCap); SetuidStrippedCount is the uncapped total, so
	// "why doesn't sudo work in this image" is answerable from the record.
	SetuidPolicy        string        `json:"setuid_policy,omitempty"`
	SetuidStrippedCount int           `json:"setuid_stripped_count,omitempty"`
	SetuidStripped      []string      `json:"setuid_stripped,omitempty"`
	ImageDefaults       ImageDefaults `json:"image_defaults,omitempty"`
	// RootfsBase is present when OutputPath is a private writable derivation
	// of a sealed image-store baseline. It identifies the immutable source;
	// it never claims that OutputPath itself is immutable.
	RootfsBase *vmkit.RootfsBase `json:"rootfs_base,omitempty"`
}

func ValidateBundleRequest(req BundleRequest) error {
	if strings.TrimSpace(req.SourcePath) == "" {
		return errors.New("source_path is required")
	}
	if strings.TrimSpace(req.OutputPath) == "" {
		return errors.New("output_path is required")
	}
	switch req.Format {
	case "", FormatExt4:
	default:
		return fmt.Errorf("format must be %q", FormatExt4)
	}
	if req.SizeMiB < 0 {
		return errors.New("size_mib must not be negative")
	}
	return nil
}

func ValidateRequest(req BuildRequest) error {
	if strings.TrimSpace(req.ImageRef) == "" {
		return errors.New("image_ref is required")
	}
	if !req.AllowMutable && looksMutable(req.ImageRef) {
		return errors.New("image_ref must be immutable unless allow_mutable is true")
	}
	if strings.TrimSpace(req.Platform.OS) == "" {
		return errors.New("platform.os is required")
	}
	if strings.TrimSpace(req.Platform.Architecture) == "" {
		return errors.New("platform.architecture is required")
	}
	if strings.TrimSpace(req.OutputPath) == "" {
		return errors.New("output_path is required")
	}
	switch req.Format {
	case "", FormatExt4:
	default:
		return fmt.Errorf("format must be %q", FormatExt4)
	}
	if req.SizeMiB < 0 {
		return errors.New("size_mib must not be negative")
	}
	if shellPath := strings.TrimSpace(req.ConsoleShell); shellPath != "" {
		if !strings.HasPrefix(shellPath, "/") {
			return errors.New("console_shell must be an absolute guest path")
		}
		if path.Clean(shellPath) != shellPath {
			return errors.New("console_shell must be a clean absolute guest path")
		}
	}
	if err := ValidateFiles(req.Files); err != nil {
		return err
	}
	return nil
}

func ValidateFiles(files []File) error {
	seen := map[string]bool{}
	for _, file := range files {
		source := strings.TrimSpace(file.SourcePath)
		if source == "" {
			return errors.New("file src is required")
		}
		info, err := os.Stat(source)
		if err != nil {
			return fmt.Errorf("file src %q: %w", source, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file src must be a regular file: %s", source)
		}
		target := strings.TrimSpace(file.Path)
		if target == "" {
			return fmt.Errorf("file dst is required for %s", source)
		}
		clean := path.Clean(strings.ReplaceAll(target, "\\", "/"))
		if !path.IsAbs(clean) {
			return fmt.Errorf("file dst must be absolute: %s", target)
		}
		if strings.ContainsRune(target, 0) {
			return fmt.Errorf("file dst contains NUL")
		}
		if clean == "/" {
			return fmt.Errorf("file dst must name a file: %s", target)
		}
		if seen[clean] {
			return fmt.Errorf("duplicate file dst %q", clean)
		}
		seen[clean] = true
		if strings.TrimSpace(file.Mode) != "" {
			if _, err := ParseFileMode(file.Mode); err != nil {
				return fmt.Errorf("file %s mode: %w", target, err)
			}
		}
	}
	return nil
}

func ParseFileMode(raw string) (os.FileMode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("mode is required")
	}
	value, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, err
	}
	mode := os.FileMode(value)
	if mode&^os.ModePerm != 0 {
		return 0, fmt.Errorf("mode must be permission bits")
	}
	return mode, nil
}

func looksMutable(ref string) bool {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "@sha256:") {
		return false
	}
	if strings.HasSuffix(ref, ":latest") {
		return true
	}
	return !strings.Contains(ref, "@")
}

func NormalizeRequest(req BuildRequest) BuildRequest {
	req.ImageRef = strings.TrimSpace(req.ImageRef)
	req.OutputPath = strings.TrimSpace(req.OutputPath)
	req.Format = strings.TrimSpace(req.Format)
	req.InitPath = strings.TrimSpace(req.InitPath)
	req.InitBinaryPath = strings.TrimSpace(req.InitBinaryPath)
	req.StateDir = strings.TrimSpace(req.StateDir)
	req.Mke2fsPath = strings.TrimSpace(req.Mke2fsPath)
	req.DebugfsPath = strings.TrimSpace(req.DebugfsPath)
	req.StageSnapshot = strings.TrimSpace(req.StageSnapshot)
	req.LocalImageLayout = strings.TrimSpace(req.LocalImageLayout)
	for i := range req.Files {
		req.Files[i].SourcePath = strings.TrimSpace(req.Files[i].SourcePath)
		req.Files[i].Path = strings.TrimSpace(req.Files[i].Path)
		req.Files[i].Mode = strings.TrimSpace(req.Files[i].Mode)
	}
	if req.Platform.OS == "" {
		req.Platform.OS = "linux"
	}
	if req.Platform.Architecture == "" {
		req.Platform.Architecture = "arm64"
	}
	if req.InitPath == "" {
		req.InitPath = DefaultInitPath
	}
	if req.Format == "" {
		req.Format = FormatExt4
	}
	if req.SizeMiB == 0 {
		req.SizeMiB = DefaultSizeMiB
	}
	if req.Mke2fsPath == "" {
		req.Mke2fsPath = "mke2fs"
	}
	if req.DebugfsPath == "" {
		req.DebugfsPath = "debugfs"
	}
	return req
}

func NormalizeBundleRequest(req BundleRequest) BundleRequest {
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.OutputPath = strings.TrimSpace(req.OutputPath)
	req.Format = strings.TrimSpace(req.Format)
	req.StateDir = strings.TrimSpace(req.StateDir)
	req.Mke2fsPath = strings.TrimSpace(req.Mke2fsPath)
	req.DebugfsPath = strings.TrimSpace(req.DebugfsPath)
	if req.Format == "" {
		req.Format = FormatExt4
	}
	if req.SizeMiB == 0 {
		req.SizeMiB = 64
	}
	if req.Mke2fsPath == "" {
		req.Mke2fsPath = "mke2fs"
	}
	if req.DebugfsPath == "" {
		req.DebugfsPath = "debugfs"
	}
	return req
}
