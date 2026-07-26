package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func createWorkspaceRootfs(ctx context.Context, opts workspaceOptions) (workspaceResult, error) {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	rootfsPath := filepath.Join(workspaceDir, "rootfs.ext4")
	if canUseImageBaseline(opts) {
		if record, err := imagecache.Find(opts.StateDir, opts.ImageRef, rootfs.Platform{OS: "linux", Architecture: opts.Architecture}); err == nil {
			if err := workspace.CopyFile(record.OutputPath, rootfsPath, 0o644); err != nil {
				return workspaceResult{}, err
			}
			return workspaceResult{
				Workspace:    opts.Name,
				StateDir:     opts.StateDir,
				Profile:      opts.Profile,
				Restart:      opts.RestartPolicy,
				Resources:    workspaceResources(opts),
				Network:      networkSpecFromConfig(opts.Network),
				ConsoleShell: strings.TrimSpace(opts.ConsoleShell),
				Hostname:     strings.TrimSpace(opts.Hostname),
				RootfsPath:   rootfsPath,
				KernelPath:   opts.KernelPath,
				Artifacts:    workspaceArtifactsFromOptions(opts),
				Image:        imagecache.Provenance(record, rootfsPath),
			}, nil
		}
	}
	command, resultPort := workspaceBuildCommandAndPort(opts)
	mode := ""
	if opts.PrepareForStart && opts.UseImageCommand {
		mode = "service"
	} else if opts.PrepareForStart && strings.TrimSpace(opts.ServiceCommand) != "" && !workspace.HasSetupCommand(opts) && strings.TrimSpace(opts.ExecCommand) == "" {
		mode = "managed-service"
	}
	finalCommand, finalMode, resetFinal := workspace.FinalCommandAndMode(opts)
	req := rootfs.BuildRequest{
		ImageRef:         opts.ImageRef,
		Platform:         rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:       rootfsPath,
		InitPath:         rootfs.DefaultInitPath,
		Command:          command,
		Mode:             mode,
		ConsoleShell:     opts.ConsoleShell,
		Hostname:         opts.Hostname,
		ShellPort:        workspace.ShellPort(opts),
		ExecPort:         workspace.ExecPort(opts),
		InitBinaryPath:   opts.GuestInitPath,
		ResultPort:       resultPort,
		NoImageCommand:   opts.PrepareForStart && !workspaceHasGuestCommand(opts) && !opts.UseImageCommand,
		StateDir:         filepath.Join(opts.StateDir, "build"),
		LocalImageLayout: commit.LayoutPath(opts.StateDir),
		Mke2fsPath:       opts.Mke2fsPath,
		SizeMiB:          opts.SizeMiB,
		Env:              opts.Env,
		Files:            workspace.RootfsFiles(opts.Files),
		Mounts:           workspaceMounts(opts.Disks),
		HostForwards:     rootfsPortForwards(opts.Network.PortForwards),
		AllowMutable:     true,
		Progress:         opts.Progress,
		ResetFinalConfig: resetFinal,
		FinalCommand:     finalCommand,
		FinalMode:        finalMode,
	}
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	result := workspaceResult{
		Workspace:    opts.Name,
		StateDir:     opts.StateDir,
		Profile:      opts.Profile,
		Restart:      opts.RestartPolicy,
		Resources:    workspaceResources(opts),
		Network:      networkSpecFromConfig(opts.Network),
		ConsoleShell: strings.TrimSpace(opts.ConsoleShell),
		Hostname:     strings.TrimSpace(opts.Hostname),
		RootfsPath:   rootfsPath,
		KernelPath:   opts.KernelPath,
		Artifacts:    workspaceArtifactsFromOptions(opts),
		Image:        provenance,
	}
	if err != nil {
		return result, err
	}
	if err := imagecache.RecordProvenance(opts.StateDir, provenance); err != nil {
		return result, err
	}
	return result, nil
}

func workspaceMounts(disks []workspaceDisk) []rootfs.Mount {
	return workspace.Mounts(disks)
}

func rootfsPortForwards(forwards []vmkit.PortForward) []rootfs.PortForward {
	return workspace.RootfsPortForwards(forwards)
}

func writeWorkspaceManifest(opts workspaceOptions) error {
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(workspaceDir, "workspace.json"), workspaceManifest{
		Name:           opts.Name,
		Profile:        opts.Profile,
		Restart:        normalizeRestartPolicy(opts.RestartPolicy),
		Resources:      workspaceResources(opts),
		Network:        networkSpecFromConfig(opts.Network),
		Service:        strings.TrimSpace(opts.ServiceCommand),
		Model:          strings.TrimSpace(opts.Model),
		ModelRunner:    workspaceModelRunnerManifest(opts.ModelRunner),
		ModelMediation: workspaceModelMediationManifest(opts.ModelMediation),
		Mediation:      opts.Mediation,
		Disks:          opts.Disks,
		Artifacts:      workspaceArtifactsFromOptions(opts),
		Verification:   opts.Verification,
	})
}

func workspaceModelRunnerManifest(spec workspace.ModelRunnerSpec) *workspace.ModelRunnerSpec {
	if strings.TrimSpace(spec.Backend) == "" &&
		strings.TrimSpace(spec.GPU) == "" &&
		strings.TrimSpace(spec.BackendModel) == "" &&
		strings.TrimSpace(spec.ServedModel) == "" &&
		len(spec.Command) == 0 &&
		strings.TrimSpace(spec.Name) == "" &&
		strings.TrimSpace(spec.HealthPath) == "" &&
		len(spec.Args) == 0 {
		return nil
	}
	spec.Env = nil
	return &spec
}

func workspaceModelMediationManifest(spec workspace.ModelMediationSpec) *workspace.ModelMediationSpec {
	if strings.TrimSpace(spec.Mode) == "" &&
		strings.TrimSpace(spec.PolicyURL) == "" &&
		strings.TrimSpace(spec.PolicyFile) == "" &&
		strings.TrimSpace(spec.PolicyTimeout) == "" {
		return nil
	}
	return &spec
}

func readWorkspaceManifest(stateDir, name string) (workspaceManifest, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "workspaces", name, "workspace.json"))
	if err != nil {
		return workspaceManifest{}, err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return workspaceManifest{}, err
	}
	return manifest, nil
}

func workspaceRequest(opts workspaceOptions, command, rootfsPath string) (vmkit.Request, error) {
	return workspace.Request(opts, command, rootfsPath, newRequestID())
}

func workspaceOptionsFromRequest(req vmkit.Request, supervisorPath string) (workspaceOptions, error) {
	return workspace.OptionsFromRequest(req, supervisorPath)
}

func workspaceSupervisor(opts workspaceOptions) (vmkit.Supervisor, error) {
	return workspace.Supervisor(opts)
}
