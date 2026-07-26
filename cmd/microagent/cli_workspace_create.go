package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runHighLevelCreate(ctx context.Context, args []string, stdout *os.File) error {
	opts, err := parseWorkspaceOptions("create", stdout, args)
	if err != nil {
		return err
	}
	warnEgressOff(opts.EgressMode)
	opts.Progress = rootfsProgress(stdout, "create")
	if opts.DryRun {
		if opts.Name == "" {
			return fmt.Errorf("create requires a name")
		}
		if err := validateWorkspaceName(opts.Name); err != nil {
			return err
		}
		result := workspaceResult{
			Workspace:  opts.Name,
			StateDir:   opts.StateDir,
			Profile:    opts.Profile,
			Restart:    opts.RestartPolicy,
			Resources:  workspaceResources(opts),
			Network:    networkSpecFromConfig(opts.Network),
			Disks:      opts.Disks,
			Artifacts:  workspaceArtifactsFromOptions(opts),
			KernelPath: opts.KernelPath,
			Response: vmkit.Response{
				OK:      true,
				Backend: opts.Backend,
				Event: &vmkit.Event{
					Identity: vmkit.Identity{
						RequestID: newRequestID(),
						RuntimeID: opts.Name,
						Role:      vmkit.RoleWorkload,
						Backend:   opts.Backend,
					},
					State:      vmkit.StatePrepared,
					Detail:     "dry run validated workspace config",
					ObservedAt: time.Now().UTC(),
				},
			},
		}
		return writeWorkspaceResult(stdout, result)
	}
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
	// Reuse a previously pulled/tagged baseline rootfs for a plain workspace
	// instead of pulling and rebuilding. The library gates this on the workspace
	// being plain (canReuseRootfsBaseline) and calls this resolver; wiring it here
	// keeps pkg/workspace free of a pkg/imagecache dependency.
	opts.RootfsBaseline = func(rootfsPath string) (string, rootfs.Provenance, bool) {
		rec, findErr := imagecache.Find(opts.StateDir, opts.ImageRef, rootfs.Platform{OS: "linux", Architecture: opts.Architecture})
		if findErr != nil {
			return "", rootfs.Provenance{}, false
		}
		return rec.OutputPath, imagecache.Provenance(rec, rootfsPath), true
	}
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
