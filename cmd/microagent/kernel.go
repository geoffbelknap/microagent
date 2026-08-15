package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runKernel(ctx context.Context, args []string, stdout *os.File) error {
	// wantsHelp scans the whole argument list, like every other group: a
	// --help after a mistyped subverb is still a question, and four groups
	// used to answer it with "unknown command" exit 1 while their siblings
	// explained themselves.
	if len(args) == 0 || wantsHelp(args) {
		printKernelHelp(stdout)
		return nil
	}
	// canonicalSubverb applies the shared ls/rm/log/inspect alias vocabulary
	// (subverbAliases in command_registry.go) so "kernel ls" works like every
	// other resource subtree's list alias. kernel has no verbs named "rm",
	// "logs", or "status" for that vocabulary to collide with, so this is a
	// plain canonicalization, not a collapse of a distinct alias pair - check
	// this switch for any such pair before adding new kernel verbs; do not
	// collapse one into the shared map (the model.go evaluate/eval collapse
	// was a past regression of exactly that kind).
	switch canonicalSubverb(args[0]) {
	case "install":
		return runKernelInstall(ctx, args[1:], stdout)
	case "verify":
		return runKernelVerify(args[1:], stdout)
	case "list":
		return runKernelList(args[1:], stdout)
	case "check":
		return runKernelCheck(args[1:], stdout)
	default:
		return fmt.Errorf("unknown kernel command: %s", args[0])
	}
}

func runKernelInstall(ctx context.Context, args []string, stdout *os.File) error {
	opts := kernel.InstallOptions{Backend: hostBackend(), Architecture: defaultGuestArch()}
	opts.OutputPath = workspace.WritableKernelPath(opts.Backend, opts.Architecture)
	outputExplicit := hasFlagValue(args, "out")
	fs := newCommandFlagSet("kernel install")
	fs.StringVar(&opts.URL, "url", "", "Kernel URL")
	fs.StringVar(&opts.FromPath, "from", "", "Local kernel path")
	fs.StringVar(&opts.Version, "version", "", "Install a specific manifest version (default: latest)")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.OutputPath, "out", opts.OutputPath, "Output path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.Channel, "channel", "lts", "Kernel channel (e.g. lts)")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected kernel install argument: %s", fs.Arg(0))
	}
	opts.Architecture = workspace.NormalizeArch(opts.Architecture)
	if err := workspace.ValidateArch(opts.Architecture); err != nil {
		return err
	}
	if !outputExplicit || opts.OutputPath == "" {
		opts.OutputPath = workspace.WritableKernelPath(opts.Backend, opts.Architecture)
	}
	progress, finishProgress := commandProgressFor(stdout, "kernel-install", "Install kernel")
	opts.Progress = progress
	result, err := kernel.Install(ctx, opts)
	finishProgress(err)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func runKernelVerify(args []string, stdout *os.File) error {
	opts := kernel.VerifyOptions{Backend: hostBackend(), Architecture: defaultGuestArch()}
	opts.Path = defaultKernelPath(opts.Backend, opts.Architecture)
	pathExplicit := hasFlagValue(args, "path")
	fs := newCommandFlagSet("kernel verify")
	fs.StringVar(&opts.Path, "path", opts.Path, "Kernel path")
	fs.StringVar(&opts.SHA256, "sha256", "", "Expected SHA-256")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected kernel verify argument: %s", fs.Arg(0))
	}
	opts.Architecture = workspace.NormalizeArch(opts.Architecture)
	if err := workspace.ValidateArch(opts.Architecture); err != nil {
		return err
	}
	if !pathExplicit || opts.Path == "" {
		opts.Path = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	result, err := kernel.Verify(opts)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

// runKernelList fetches the signed kernel manifest and lists the available
// kernels for the host backend/arch (or all of them with --all).
func runKernelList(args []string, stdout *os.File) error {
	backend := hostBackend()
	arch := defaultGuestArch()
	all := false
	fs := newCommandFlagSet("kernel list")
	fs.StringVar(&backend, "backend", backend, "Backend identity")
	fs.StringVar(&arch, "arch", arch, "Guest architecture")
	fs.BoolVar(&all, "all", false, "List kernels for all backends/architectures")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	arch = workspace.NormalizeArch(arch)
	if err := workspace.ValidateArch(arch); err != nil {
		return err
	}
	targets, err := kernel.FetchTargets(kernel.DefaultSource())
	if err != nil {
		return fmt.Errorf("fetch signed kernel manifest: %w", err)
	}
	out := make([]kernel.KernelTarget, 0, len(targets))
	for _, t := range targets {
		if all || (t.Backend == backend && t.Arch == arch) {
			out = append(out, t)
		}
	}
	return writeJSON(stdout, out)
}

// runKernelCheck reports whether the installed kernel is current, behind but
// safe (optional), or behind a security floor, driven by the signed manifest.
func runKernelCheck(args []string, stdout *os.File) error {
	backend := hostBackend()
	arch := defaultGuestArch()
	fs := newCommandFlagSet("kernel check")
	fs.StringVar(&backend, "backend", backend, "Backend identity")
	fs.StringVar(&arch, "arch", arch, "Guest architecture")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	arch = workspace.NormalizeArch(arch)
	if err := workspace.ValidateArch(arch); err != nil {
		return err
	}
	targets, err := kernel.FetchTargets(kernel.DefaultSource())
	if err != nil {
		return fmt.Errorf("fetch signed kernel manifest: %w", err)
	}
	// Resolve the installed version by matching the local kernel's SHA-256
	// against the verified manifest (the install path is not version-stamped).
	installedVersion := ""
	if sum, err := workspace.FileSHA256(workspace.KernelPath(backend, arch)); err == nil {
		for _, t := range targets {
			if t.Backend == backend && t.Arch == arch && strings.EqualFold(t.SHA256, sum) {
				installedVersion = t.Version
				break
			}
		}
	}
	return writeJSON(stdout, kernel.CheckUpdate(kernel.FilterChannel(targets, "lts"), backend, arch, installedVersion))
}

func printKernelHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "kernel")
	printUsageBlock(stdout, "kernel", "kernel")
	fmt.Fprint(stdout, `
Advanced kernel commands. Most users can start with microagent run IMAGE ...
and skip this.

Commands:
  list                 List available kernels from the signed manifest
  check                Report whether the installed kernel is current or behind
  install              Install a custom kernel
  verify               Verify a custom kernel

Install options:
  With no options, install the latest kernel from the signed manifest.
  -version <version>   Install a specific manifest version
  -url <url>           Download URL (custom kernel)
  -from <path>         Local kernel path (custom kernel)
  -sha256 <sha256>     Expected SHA-256
  -out <path>          Output path

Verify options:
  -path <path>         Kernel path
  -sha256 <sha256>     Expected SHA-256

List options:
  -all                 List kernels for all backends/architectures

list and check read the cryptographically signed manifest from
kernels.microagent.sh and verify it against the embedded TUF root before use.
`)
}
