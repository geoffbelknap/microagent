package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runHighLevelCreate(ctx context.Context, args []string, stdout *os.File) error {
	opts, err := parseWorkspaceOptions("create", stdout, args)
	if err != nil {
		return err
	}
	warnEgressOff(opts.EgressMode)
	opts.Progress = rootfsProgress(stdout, "create")
	// Model orchestration: resolve, pull if needed, and pair the setup boot so
	// the guest env is consistent across boots. The canonical ref is persisted
	// in the manifest; every start re-pairs from it. The setup boot's holder is
	// released when create returns (the workspace is left halted).
	modelToken, _ := flagValue(args, "model-token")
	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, modelToken)
	if err != nil {
		return err
	}
	defer releaseModel()
	wireRootfsBaseline(&opts)
	result, err := workspace.Create(ctx, opts)
	if err != nil && result.Workspace == "" {
		return err
	}
	if encodeErr := writeCreateResult(stdout, result, err); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runApply(ctx context.Context, args []string, stdout *os.File) error {
	opts := workspaceOptions{
		Backend:      hostBackend(),
		Architecture: defaultGuestArch(),
		StateDir:     defaultStateDir(),
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	specPath := ""
	fs := newCommandFlagSet("apply")
	fs.StringVar(&specPath, "file", "", "Workspace spec file")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected apply argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(specPath) == "" {
		return fmt.Errorf("apply requires --file path")
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	spec, err := readWorkspaceSpec(specPath)
	if err != nil {
		return err
	}
	result, err := workspace.Apply(ctx, opts, spec)
	if encodeErr := writeApplyResult(stdout, result); encodeErr != nil {
		return encodeErr
	}
	return err
}

// wireRootfsBaseline connects a workspace to the image-store baselines:
// reuse clones a recorded baseline whose guest init matches the one this
// workspace would inject, and save seeds the store from the first plain
// build of an image so later creates and runs clone instead of building.
// Wiring lives here so pkg/workspace stays free of a pkg/imagecache
// dependency.
func wireRootfsBaseline(opts *workspaceOptions) {
	opts.RootfsBaseline, opts.RootfsBaselineSave = rootfsBaselineHooks(opts.StateDir, opts.ImageRef, opts.Architecture, opts.GuestInitPath)
}

// rootfsBaselineHooks builds the reuse/save pair wireRootfsBaseline installs.
// It is separate so every command that boots a workspace — including
// `perf boot`, which measures what those hooks change — wires the same
// image-store behavior instead of its own.
func rootfsBaselineHooks(stateDir, imageRef, architecture, guestInitPath string) (
	func(rootfsPath string) (string, rootfs.Provenance, bool),
	func(rootfsPath string, prov rootfs.Provenance),
) {
	initSHA := workspace.GuestInitSHA256(guestInitPath)
	reuse := func(rootfsPath string) (string, rootfs.Provenance, bool) {
		rec, findErr := imagecache.Find(stateDir, imageRef, rootfs.Platform{OS: "linux", Architecture: architecture})
		if findErr != nil {
			return "", rootfs.Provenance{}, false
		}
		// A baseline built with a different (or unrecorded) guest init
		// must rebuild: cloning it would pin this workspace to a stale
		// init after a microagent upgrade.
		if rec.InitSHA256 == "" || initSHA == "" || rec.InitSHA256 != initSHA {
			return "", rootfs.Provenance{}, false
		}
		// Only stripped baselines are cloneable. An unrecorded policy means
		// the baseline predates the setuid strip and carries the image's
		// setuid bits; rebuild rather than hand those to a workspace that
		// asked for the default. (Setuid-preserving workspaces never reach
		// this hook — CanReuseRootfsBaseline gates them to fresh builds.)
		if rec.SetuidPolicy != rootfs.SetuidPolicyStripped {
			return "", rootfs.Provenance{}, false
		}
		// Legacy or host-writable cache entries are not immutable bases. Rebuild
		// them once so the workspace derives from a measured, sealed artifact.
		if imagecache.ValidateImmutableRootfs(rec) != nil {
			return "", rootfs.Provenance{}, false
		}
		return rec.OutputPath, imagecache.Provenance(rec, rootfsPath), true
	}
	save := func(rootfsPath string, prov rootfs.Provenance) {
		// Seeding is an optimization; a failure must not disturb the build
		// that just succeeded.
		if err := imagecache.SaveBaseline(stateDir, rootfsPath, prov, initSHA); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not record rootfs baseline: %v\n", err)
		}
	}
	return reuse, save
}
