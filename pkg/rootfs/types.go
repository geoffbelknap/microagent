package rootfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultInitPath = "/sbin/microagent-init"
	DefaultSizeMiB  = 1024
)

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type BuildRequest struct {
	ImageRef       string            `json:"image_ref"`
	Platform       Platform          `json:"platform"`
	OutputPath     string            `json:"output_path"`
	InitPath       string            `json:"init_path,omitempty"`
	InitBinaryPath string            `json:"init_binary_path,omitempty"`
	Command        []string          `json:"command,omitempty"`
	ConsoleShell   string            `json:"console_shell,omitempty"`
	Hostname       string            `json:"hostname,omitempty"`
	NoImageCommand bool              `json:"no_image_command,omitempty"`
	ResultPort     uint32            `json:"result_port,omitempty"`
	StateDir       string            `json:"state_dir,omitempty"`
	Mke2fsPath     string            `json:"mke2fs_path,omitempty"`
	SizeMiB        int64             `json:"size_mib,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Files          []File            `json:"files,omitempty"`
	Mounts         []Mount           `json:"mounts,omitempty"`
	HostForwards   []PortForward     `json:"host_forwards,omitempty"`
	AllowMutable   bool              `json:"allow_mutable,omitempty"`
	KeepStage      bool              `json:"keep_stage,omitempty"`
	StageSnapshot  string            `json:"stage_snapshot,omitempty"`
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
	StateDir   string `json:"state_dir,omitempty"`
	Mke2fsPath string `json:"mke2fs_path,omitempty"`
	SizeMiB    int64  `json:"size_mib,omitempty"`
}

type BundleProvenance struct {
	SourcePath   string `json:"source_path"`
	OutputPath   string `json:"output_path"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Builder      string `json:"builder"`
	BuilderPhase string `json:"builder_phase"`
}

type Provenance struct {
	ImageRef      string   `json:"image_ref"`
	ResolvedRef   string   `json:"resolved_ref,omitempty"`
	Digest        string   `json:"digest,omitempty"`
	Platform      Platform `json:"platform"`
	OutputPath    string   `json:"output_path"`
	InitPath      string   `json:"init_path,omitempty"`
	SizeBytes     int64    `json:"size_bytes,omitempty"`
	Builder       string   `json:"builder"`
	BuilderPhase  string   `json:"builder_phase"`
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
		if !filepath.IsAbs(target) {
			return fmt.Errorf("file dst must be absolute: %s", target)
		}
		if strings.ContainsRune(target, 0) {
			return fmt.Errorf("file dst contains NUL")
		}
		clean := filepath.Clean(target)
		if clean == string(os.PathSeparator) {
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
	req.InitPath = strings.TrimSpace(req.InitPath)
	req.InitBinaryPath = strings.TrimSpace(req.InitBinaryPath)
	req.StateDir = strings.TrimSpace(req.StateDir)
	req.Mke2fsPath = strings.TrimSpace(req.Mke2fsPath)
	req.StageSnapshot = strings.TrimSpace(req.StageSnapshot)
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
	req.StateDir = strings.TrimSpace(req.StateDir)
	req.Mke2fsPath = strings.TrimSpace(req.Mke2fsPath)
	if req.SizeMiB == 0 {
		req.SizeMiB = 64
	}
	if req.Mke2fsPath == "" {
		req.Mke2fsPath = "mke2fs"
	}
	return req
}
