package workspace

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// KernelInstaller installs a verified default kernel for backend/arch at
// outputPath. pkg/kernel registers the real implementation; the indirection
// exists because pkg/kernel depends on this package for path helpers.
type KernelInstaller func(ctx context.Context, backend, arch, outputPath string) error

var defaultKernelInstaller KernelInstaller

// RegisterKernelInstaller sets the installer EnsureKernel uses for a missing
// default kernel. Programs that import pkg/kernel get one registered
// automatically; without one, EnsureKernel leaves a missing kernel alone and
// boot reports the missing file.
func RegisterKernelInstaller(install KernelInstaller) {
	defaultKernelInstaller = install
}

// EnsureKernel makes sure the kernel the workspace will boot with exists on
// disk. When no kernel is installed yet and the caller did not choose one
// explicitly, it installs the latest kernel from the signed manifest into the
// managed per-user path via the registered installer. An explicit kernel path
// (Options.KernelExplicit, or any path other than the managed one) is used
// as-is; if it is missing, boot reports the missing file.
func EnsureKernel(ctx context.Context, opts *Options) error {
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
	if writable == "" || opts.KernelPath != writable || defaultKernelInstaller == nil {
		return nil
	}
	if err := defaultKernelInstaller(ctx, opts.Backend, opts.Architecture, opts.KernelPath); err != nil {
		return fmt.Errorf("install kernel for %s/%s: %w (or install one with `microagent kernel install`)", opts.Backend, opts.Architecture, err)
	}
	return nil
}
