package rootfs

import (
	"errors"
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
	NoImageCommand bool              `json:"no_image_command,omitempty"`
	ResultPort     uint32            `json:"result_port,omitempty"`
	StateDir       string            `json:"state_dir,omitempty"`
	Mke2fsPath     string            `json:"mke2fs_path,omitempty"`
	SizeMiB        int64             `json:"size_mib,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Mounts         []Mount           `json:"mounts,omitempty"`
	AllowMutable   bool              `json:"allow_mutable,omitempty"`
	KeepStage      bool              `json:"keep_stage,omitempty"`
	StageSnapshot  string            `json:"stage_snapshot,omitempty"`
}

type Mount struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	Mode       string `json:"mode"`
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
	return nil
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
