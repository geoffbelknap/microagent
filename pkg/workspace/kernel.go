package workspace

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

// KernelInstaller installs a verified default kernel for backend/arch at
// outputPath. pkg/kernel registers the real implementation; the indirection
// exists because pkg/kernel depends on this package for path helpers.
type KernelInstaller func(ctx context.Context, backend, arch, outputPath string) error

// ProgressKernelInstaller is the typed-progress form used by built-in kernel
// installation. KernelInstaller remains supported for embedders that do not
// need progress callbacks.
type ProgressKernelInstaller func(ctx context.Context, backend, arch, outputPath string, progress operation.ProgressFunc) error

var defaultKernelInstaller KernelInstaller
var defaultProgressKernelInstaller ProgressKernelInstaller

// RegisterKernelInstaller sets the installer EnsureKernel uses for a missing
// default kernel. Programs that import pkg/kernel get one registered
// automatically; without one, EnsureKernel leaves a missing kernel alone and
// boot reports the missing file.
func RegisterKernelInstaller(install KernelInstaller) {
	defaultKernelInstaller = install
	defaultProgressKernelInstaller = nil
}

// RegisterProgressKernelInstaller sets the progress-aware installer
// EnsureKernel prefers for a missing default kernel.
func RegisterProgressKernelInstaller(install ProgressKernelInstaller) {
	defaultProgressKernelInstaller = install
	defaultKernelInstaller = nil
}

// EnsureKernel makes sure the kernel the workspace will boot with exists on
// disk. When no kernel is installed yet and the caller did not choose one
// explicitly, it installs the latest kernel from the signed manifest into the
// managed per-user path via the registered installer. An explicit kernel path
// (Options.KernelExplicit, or any path other than the managed one) is used
// as-is; if it is missing, boot reports the missing file.
func EnsureKernel(ctx context.Context, opts *Options) error {
	opts.Architecture = NormalizeArch(opts.Architecture)
	if err := ValidateArch(opts.Architecture); err != nil {
		return err
	}
	if opts.KernelExplicit {
		return nil
	}
	if strings.TrimSpace(opts.KernelPath) == "" {
		opts.KernelPath = KernelPath(opts.Backend, opts.Architecture)
	}
	if _, err := os.Stat(opts.KernelPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	writable := WritableKernelPath(opts.Backend, opts.Architecture)
	if writable == "" || opts.KernelPath != writable || (defaultKernelInstaller == nil && defaultProgressKernelInstaller == nil) {
		return nil
	}
	var installErr error
	if defaultProgressKernelInstaller != nil {
		installErr = defaultProgressKernelInstaller(ctx, opts.Backend, opts.Architecture, opts.KernelPath, opts.Progress)
	} else {
		installErr = defaultKernelInstaller(ctx, opts.Backend, opts.Architecture, opts.KernelPath)
	}
	if installErr != nil {
		return fmt.Errorf("install kernel for %s/%s: %w (or install one with `microagent kernel install`)", opts.Backend, opts.Architecture, installErr)
	}
	return nil
}
