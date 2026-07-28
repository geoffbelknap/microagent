package rootfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
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
	Command        []string `json:"command,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	ConsoleShell   string   `json:"console_shell,omitempty"`
	Hostname       string   `json:"hostname,omitempty"`
	ShellPort      uint16   `json:"shell_port,omitempty"`
	ExecPort       uint16   `json:"exec_port,omitempty"`
	NoImageCommand bool     `json:"no_image_command,omitempty"`
	ResultPort     uint32   `json:"result_port,omitempty"`
	StateDir       string   `json:"state_dir,omitempty"`
	Mke2fsPath     string   `json:"mke2fs_path,omitempty"`
	SizeMiB        int64    `json:"size_mib,omitempty"`
	// AutoSize treats SizeMiB as a starting point rather than a limit: when
	// the unpacked image doesn't fit, the disk grows to hold it plus free
	// space. Set it when the caller didn't pin a size explicitly.
	AutoSize      bool              `json:"auto_size,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Files         []File            `json:"files,omitempty"`
	Mounts        []Mount           `json:"mounts,omitempty"`
	HostForwards  []PortForward     `json:"host_forwards,omitempty"`
	AllowMutable  bool              `json:"allow_mutable,omitempty"`
	KeepStage     bool              `json:"keep_stage,omitempty"`
	StageSnapshot string            `json:"stage_snapshot,omitempty"`
	Progress      ProgressFunc      `json:"-"`
	// ResetFinalConfig appends a line to the Command shell script that
	// rewrites /etc/microagent/run.json so later boots run FinalCommand in
	// FinalMode. The builder composes the rewritten env from the image config
	// and Env — the same merge as the initial guest config — so a setup boot
	// never strips image env (PATH and friends) from the workspace.
	ResetFinalConfig bool     `json:"reset_final_config,omitempty"`
	FinalCommand     []string `json:"final_command,omitempty"`
	FinalMode        string   `json:"final_mode,omitempty"`
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
	HostPort  uint16 `json:"hostPort"`
	GuestPort uint16 `json:"guestPort"`
}

type BundleRequest struct {
	SourcePath string `json:"source_path"`
	OutputPath string `json:"output_path"`
	Format     string `json:"format,omitempty"`
	StateDir   string `json:"state_dir,omitempty"`
	Mke2fsPath string `json:"mke2fs_path,omitempty"`
	SizeMiB    int64  `json:"size_mib,omitempty"`
	// AutoSize grows the disk past SizeMiB when the bundle contents don't fit.
	AutoSize bool `json:"auto_size,omitempty"`
}

type BundleProvenance struct {
	SourcePath   string `json:"source_path"`
	OutputPath   string `json:"output_path"`
	Format       string `json:"format,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Builder      string `json:"builder"`
	BuilderPhase string `json:"builder_phase"`
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
	if hostname := strings.TrimSpace(req.Hostname); hostname != "" {
		if err := validateHostname(hostname); err != nil {
			return err
		}
	}
	if err := ValidateFiles(req.Files); err != nil {
		return err
	}
	return nil
}

func validateHostname(hostname string) error {
	if len(hostname) > 63 {
		return errors.New("hostname must be 63 characters or fewer")
	}
	if hostname == "" {
		return errors.New("hostname is required")
	}
	if hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return errors.New("hostname must not start or end with '-'")
	}
	for _, r := range hostname {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return errors.New("hostname must contain only letters, numbers, and '-'")
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
			if _, err := parseFileMode(file.Mode); err != nil {
				return fmt.Errorf("file %s mode: %w", target, err)
			}
		}
	}
	return nil
}

func parseFileMode(raw string) (os.FileMode, error) {
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
	return req
}

func NormalizeBundleRequest(req BundleRequest) BundleRequest {
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.OutputPath = strings.TrimSpace(req.OutputPath)
	req.Format = strings.TrimSpace(req.Format)
	req.StateDir = strings.TrimSpace(req.StateDir)
	req.Mke2fsPath = strings.TrimSpace(req.Mke2fsPath)
	if req.Format == "" {
		req.Format = FormatExt4
	}
	if req.SizeMiB == 0 {
		req.SizeMiB = 64
	}
	if req.Mke2fsPath == "" {
		req.Mke2fsPath = "mke2fs"
	}
	return req
}
