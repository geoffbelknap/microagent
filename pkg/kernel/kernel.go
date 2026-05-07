package kernel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
	"github.com/geoffbelknap/microagent-kit/pkg/workspace"
)

type InstallOptions struct {
	URL          string
	FromPath     string
	SHA256       string
	OutputPath   string
	Backend      string
	Architecture string
}

type InstallResult struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type VerifyOptions struct {
	Path         string
	SHA256       string
	Backend      string
	Architecture string
}

type VerifyResult struct {
	OK     bool   `json:"ok"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type ManifestEntry struct {
	Backend      string
	Architecture string
	URL          string
	SHA256       string
}

var Defaults = []ManifestEntry{
	{
		Backend:      vmkit.BackendAppleVF,
		Architecture: "arm64",
		URL:          "https://github.com/geoffbelknap/microagent-kit/releases/download/kernels-6.12.22-r1/microagent-kernel-6.12.22-apple-vf-arm64",
		SHA256:       "73fe78e51a8ce348e69311d376a02114440eee6b60bf2e91af54bdf2dfb405ec",
	},
	{
		Backend:      vmkit.BackendFirecracker,
		Architecture: "amd64",
		URL:          "https://github.com/geoffbelknap/microagent-kernels/releases/download/kernels-6.1.155-r2/microagent-kernel-6.1.155-firecracker-amd64",
		SHA256:       "4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0",
	},
}

func Default(backend, arch string) (ManifestEntry, bool) {
	for _, kernel := range Defaults {
		if kernel.Backend == backend && kernel.Architecture == arch {
			return kernel, true
		}
	}
	return ManifestEntry{}, false
}

func Support(backend, arch string) *vmkit.KernelSupport {
	return SupportForPath(backend, arch, workspace.KernelPath(backend, arch))
}

func SupportForPath(backend, arch, path string) *vmkit.KernelSupport {
	support := &vmkit.KernelSupport{
		Backend:      backend,
		Architecture: arch,
		Path:         path,
		Status:       "unavailable",
	}
	if support.Path != "" {
		if _, err := os.Stat(support.Path); err == nil {
			support.Status = "present"
		} else if !os.IsNotExist(err) {
			support.Status = "error"
			support.Error = err.Error()
			return support
		}
	}
	if kernel, ok := Default(backend, arch); ok {
		support.SHA256 = kernel.SHA256
		if support.Status == "unavailable" {
			support.Status = "downloadable"
		}
	}
	return support
}

func Install(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	if opts.Backend == "" {
		opts.Backend = workspace.HostBackend()
	}
	if opts.Architecture == "" {
		opts.Architecture = workspace.GuestArch()
	}
	if opts.OutputPath == "" {
		opts.OutputPath = workspace.WritableKernelPath(opts.Backend, opts.Architecture)
	}
	if opts.URL == "" && opts.FromPath == "" && opts.SHA256 == "" {
		kernel, ok := Default(opts.Backend, opts.Architecture)
		if !ok {
			return InstallResult{}, fmt.Errorf("no default kernel for %s/%s; use URL or FromPath", opts.Backend, opts.Architecture)
		}
		opts.URL = kernel.URL
		opts.SHA256 = kernel.SHA256
	}
	if err := install(ctx, opts); err != nil {
		return InstallResult{}, err
	}
	sum, err := workspace.FileSHA256(opts.OutputPath)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Path: opts.OutputPath, SHA256: sum}, nil
}

func Verify(opts VerifyOptions) (VerifyResult, error) {
	if opts.Backend == "" {
		opts.Backend = workspace.HostBackend()
	}
	if opts.Architecture == "" {
		opts.Architecture = workspace.GuestArch()
	}
	if opts.Path == "" {
		opts.Path = workspace.KernelPath(opts.Backend, opts.Architecture)
	}
	sum, err := workspace.FileSHA256(opts.Path)
	if err != nil {
		return VerifyResult{}, err
	}
	if opts.SHA256 != "" && !strings.EqualFold(opts.SHA256, sum) {
		return VerifyResult{}, fmt.Errorf("kernel sha256 = %s, want %s", sum, opts.SHA256)
	}
	return VerifyResult{OK: true, Path: opts.Path, SHA256: sum}, nil
}

func install(ctx context.Context, opts InstallOptions) error {
	if (opts.URL == "") == (opts.FromPath == "") {
		return fmt.Errorf("kernel install requires exactly one of URL or FromPath")
	}
	if err := os.MkdirAll(filepath.Dir(opts.OutputPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(opts.OutputPath), ".kernel-*")
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
	if opts.FromPath != "" {
		in, err := os.Open(opts.FromPath)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		_, err = io.Copy(tmp, in)
		closeErr := in.Close()
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if closeErr != nil {
			_ = tmp.Close()
			return closeErr
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if token := githubToken(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			_ = tmp.Close()
			return fmt.Errorf("download kernel: %s", resp.Status)
		}
		if _, err := io.Copy(tmp, resp.Body); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	sum, err := workspace.FileSHA256(tmpPath)
	if err != nil {
		return err
	}
	if opts.SHA256 != "" && !strings.EqualFold(opts.SHA256, sum) {
		return fmt.Errorf("kernel sha256 = %s, want %s", sum, opts.SHA256)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, opts.OutputPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func githubToken() string {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}
