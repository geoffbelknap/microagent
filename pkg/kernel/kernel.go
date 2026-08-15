package kernel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

const maxKernelDownloadBytes = 512 * 1024 * 1024

type InstallOptions struct {
	URL          string
	FromPath     string
	SHA256       string
	OutputPath   string
	Backend      string
	Architecture string
	Channel      string // kernel channel (default "lts")
	Version      string // optional: pick a specific manifest version (default: latest)
	Progress     operation.ProgressFunc
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
	// OK and Verified are true ONLY when the kernel's hash was checked against an
	// expected sha256. Without one (no -sha256) this is a hash computation, not a
	// verification, so both are false and only SHA256 is reported.
	OK       bool   `json:"ok"`
	Verified bool   `json:"verified"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
}

// resolveTarget fetches the signed manifest and returns the chosen kernel for
// backend/arch: the exact version when given, otherwise the latest available.
func resolveTarget(backend, arch, channel, version string) (KernelTarget, error) {
	if channel == "" {
		channel = "lts"
	}
	targets, err := FetchTargets(DefaultSource())
	if err != nil {
		return KernelTarget{}, fmt.Errorf("fetch signed kernel manifest: %w", err)
	}
	targets = FilterChannel(targets, channel)
	if version != "" {
		for _, t := range targets {
			if t.Backend == backend && t.Arch == arch && t.Version == version {
				return t, nil
			}
		}
		return KernelTarget{}, fmt.Errorf("no %s/%s/%s kernel %s in the signed manifest", backend, arch, channel, version)
	}
	latest := LatestTarget(targets, backend, arch)
	if latest == nil {
		return KernelTarget{}, fmt.Errorf("no %s/%s/%s kernel in the signed manifest", backend, arch, channel)
	}
	return *latest, nil
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
	return support
}

// Programs that link this package can boot workspaces on a host with no
// kernel installed yet: workspace.EnsureKernel fetches, verifies, and installs
// the latest kernel from the signed manifest through this hook.
func init() {
	workspace.RegisterProgressKernelInstaller(func(ctx context.Context, backend, arch, outputPath string, progress operation.ProgressFunc) error {
		_, err := Install(ctx, InstallOptions{
			Backend:      backend,
			Architecture: arch,
			OutputPath:   outputPath,
			Progress:     progress,
		})
		return err
	})
}

func Install(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	progress := operation.NewReporter(opts.Progress)
	emitKernelProgress(progress, "kernel_validate", "validating kernel install", 0, 0)
	if opts.Backend == "" {
		opts.Backend = workspace.HostBackend()
	}
	if err := workspace.ValidateHostBackend(opts.Backend); err != nil {
		return InstallResult{}, err
	}
	if opts.Architecture == "" {
		opts.Architecture = workspace.GuestArch()
	}
	opts.Architecture = workspace.NormalizeArch(opts.Architecture)
	if err := workspace.ValidateArch(opts.Architecture); err != nil {
		return InstallResult{}, err
	}
	if opts.OutputPath == "" {
		opts.OutputPath = workspace.WritableKernelPath(opts.Backend, opts.Architecture)
	}
	if opts.URL == "" && opts.FromPath == "" {
		emitKernelProgress(progress, "kernel_resolve", "resolving signed kernel manifest", 0, 0)
		target, err := resolveTarget(opts.Backend, opts.Architecture, opts.Channel, opts.Version)
		if err != nil {
			return InstallResult{}, err
		}
		opts.URL = target.URL
		opts.SHA256 = target.SHA256
		// Fail closed: a signed target that carries no sha256 hash would otherwise
		// be installed with the hash check skipped (opts.SHA256 == "" below).
		if strings.TrimSpace(opts.SHA256) == "" {
			return InstallResult{}, fmt.Errorf("kernel target for %s/%s has no sha256 in the signed manifest; refusing to install unverified", opts.Backend, opts.Architecture)
		}
	}
	if err := install(ctx, opts); err != nil {
		return InstallResult{}, err
	}
	sum, err := workspace.FileSHA256(opts.OutputPath)
	if err != nil {
		return InstallResult{}, err
	}
	emitKernelProgress(progress, "kernel_installed", "kernel installed", 0, 0)
	return InstallResult{Path: opts.OutputPath, SHA256: sum}, nil
}

const kernelProgressInterval = 500 * time.Millisecond

func emitKernelProgress(reporter *operation.Reporter, phase, message string, bytes, totalBytes int64) {
	reporter.Emit(operation.ProgressEvent{
		Operation:     "kernel_install",
		Phase:         phase,
		Label:         "Install kernel",
		Message:       message,
		Bytes:         bytes,
		TotalBytes:    totalBytes,
		Indeterminate: totalBytes <= 0,
	})
}

type kernelProgressWriter struct {
	reporter *operation.Reporter
	total    int64
	written  int64
}

var publishKernel = os.Rename

func (w *kernelProgressWriter) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	w.reporter.EmitThrottled(kernelProgressInterval, operation.ProgressEvent{
		Operation:     "kernel_install",
		Phase:         "kernel_transfer",
		Label:         "Install kernel",
		Message:       "transferring kernel",
		Bytes:         w.written,
		TotalBytes:    w.total,
		Indeterminate: w.total <= 0,
	})
	return len(p), nil
}

func Verify(opts VerifyOptions) (VerifyResult, error) {
	if opts.Backend == "" {
		opts.Backend = workspace.HostBackend()
	}
	if err := workspace.ValidateHostBackend(opts.Backend); err != nil {
		return VerifyResult{}, err
	}
	if opts.Architecture == "" {
		opts.Architecture = workspace.GuestArch()
	}
	opts.Architecture = workspace.NormalizeArch(opts.Architecture)
	if err := workspace.ValidateArch(opts.Architecture); err != nil {
		return VerifyResult{}, err
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
	// Report OK/Verified only when an expected sha256 was actually matched — a
	// bare `kernel verify` (no -sha256) must not imply the kernel was verified
	// against a trusted source. Pass -sha256 (from `kernel list`) to verify.
	verified := opts.SHA256 != "" && strings.EqualFold(opts.SHA256, sum)
	return VerifyResult{OK: verified, Verified: verified, Path: opts.Path, SHA256: sum}, nil
}

func install(ctx context.Context, opts InstallOptions) error {
	progress := operation.NewReporter(opts.Progress)
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
		totalBytes := int64(0)
		if info, statErr := in.Stat(); statErr == nil {
			totalBytes = info.Size()
		}
		emitKernelProgress(progress, "kernel_transfer", "copying local kernel", 0, totalBytes)
		counter := &kernelProgressWriter{reporter: progress, total: totalBytes}
		written, err := io.Copy(io.MultiWriter(tmp, counter), in)
		closeErr := in.Close()
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if closeErr != nil {
			_ = tmp.Close()
			return closeErr
		}
		emitKernelProgress(progress, "kernel_transfer", "kernel copy complete", written, totalBytes)
	} else {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if token := githubTokenForURL(opts.URL); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		client := &http.Client{Timeout: 10 * time.Minute}
		resp, err := client.Do(req)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			_ = tmp.Close()
			return fmt.Errorf("download kernel: %s", resp.Status)
		}
		totalBytes := resp.ContentLength
		if totalBytes < 0 {
			totalBytes = 0
		}
		emitKernelProgress(progress, "kernel_transfer", "downloading kernel", 0, totalBytes)
		limited := &io.LimitedReader{R: resp.Body, N: maxKernelDownloadBytes + 1}
		counter := &kernelProgressWriter{reporter: progress, total: totalBytes}
		written, err := io.Copy(io.MultiWriter(tmp, counter), limited)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if limited.N == 0 {
			_ = tmp.Close()
			return fmt.Errorf("download kernel exceeds %d bytes", maxKernelDownloadBytes)
		}
		emitKernelProgress(progress, "kernel_transfer", "kernel download complete", written, totalBytes)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	emitKernelProgress(progress, "kernel_verify", "verifying kernel digest", 0, 0)
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
	emitKernelProgress(progress, "kernel_publish", "publishing verified kernel", 0, 0)
	if err := publishKernel(tmpPath, opts.OutputPath); err != nil {
		// Windows refuses to replace a file another process holds open
		// without FILE_SHARE_DELETE — most likely a running VM booted from
		// this kernel. Name the likely cause instead of the bare rename.
		return fmt.Errorf("replace kernel at %s: %w (a running VM may still hold the existing kernel open; stop microagent workspaces and retry)", opts.OutputPath, err)
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

func githubTokenForURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "github.com" && host != "objects.githubusercontent.com" && !strings.HasSuffix(host, ".githubusercontent.com") {
		return ""
	}
	return githubToken()
}
