package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

var mcpWorkspaceExec = workspace.ExecWithMetadata
var mcpWorkspaceControl = workspace.Control
var mcpWorkspaceQuarantine = workspace.Quarantine
var mcpWorkspaceDelete = workspace.Delete
var mcpWorkspaceClone = workspace.Clone
var mcpWorkspaceApply = workspace.Apply
var mcpWorkspaceReadSpec = workspace.ReadSpec
var mcpWorkspaceCommit = commit.Commit
var mcpWorkspaceCommitPush = commit.Push
var mcpSnapshotCreate = workspace.Snapshot
var mcpSnapshotForensic = workspace.SnapshotForensic
var mcpSnapshotDelete = workspace.SnapshotRemove
var mcpVolumeCreate = volume.Create
var mcpVolumeRemove = volume.Remove
var mcpImagePull = imagecache.Pull
var mcpImageList = imagecache.List
var mcpImagePush = commit.Push
var mcpImageTag = imagecache.Tag
var mcpImageRemove = imagecache.Remove
var mcpImagePrune = imagecache.Prune
var mcpModelPull = model.Pull
var mcpModelServe = model.Serve
var mcpPolicyValidate = hostworker.ValidateFilePolicy
var mcpPolicyEvaluate = hostworker.EvaluateFilePolicy
var mcpModelRemove = model.Remove
var mcpModelPrune = model.Prune
var mcpModelStop = modelrunner.Stop
var mcpWorkspaceCopy = workspace.Copy
var mcpWorkspaceGetArtifact = workspace.GetArtifact
var mcpDiagnosticsCheck = diagnostics.Check
var mcpKernelVerify = kernel.Verify
var mcpKernelInstall = kernel.Install
var mcpRootfsBuild = func(ctx context.Context, req rootfs.BuildRequest) (rootfs.Provenance, error) {
	return rootfs.NewBuilder().Build(ctx, req)
}

func estimateWorkspaceCost(args map[string]any) map[string]any {
	resources := resourceConfig{MemoryMiB: defaultWorkspaceMemoryMiB, CPUCount: defaultWorkspaceCPUCount, SizeMiB: rootfs.DefaultSizeMiB}
	profileName := stringArg(args, "profile")
	if profileName == "" {
		profileName = defaultWorkspaceProfile
	}
	if profile, ok := lookupResourceProfile(profileName); ok {
		resources = profile.Resources
	}
	if memory := intArg(args, "memory_mib"); memory > 0 {
		resources.MemoryMiB = memory
	}
	if cpus := intArg(args, "cpus"); cpus > 0 {
		resources.CPUCount = cpus
	}
	if size := int64Arg(args, "size_mib"); size > 0 {
		resources.SizeMiB = size
	}
	pricePerHour := floatArg(args, "price_per_hour")
	estimate := map[string]any{
		"profile":             profileName,
		"memory_mib":          resources.MemoryMiB,
		"cpus":                resources.CPUCount,
		"disk_mib":            resources.SizeMiB,
		"estimated_boot_ms":   0,
		"price_per_hour":      pricePerHour,
		"estimated_cost_hour": float64(0),
	}
	if pricePerHour > 0 {
		estimate["estimated_cost_hour"] = pricePerHour
	}
	return mcpSuccessEnvelope(estimate, mcpZeroMeta(args))
}

// runDirectMCPTool contains agent-facing operations whose inputs map directly
// onto typed library calls. These handlers deliberately bypass CLI parsing,
// rendering, output modes, temporary files, and exit-code policy.
func runDirectMCPTool(ctx context.Context, name string, args map[string]any) (any, bool, error) {
	opts := workspace.DefaultOptions()
	applyMCPHostOptions(&opts, args)
	stateDir := opts.StateDir
	workspaceName := stringArg(args, "name")
	opts.Name = workspaceName

	switch name {
	case "workspace.halt", "workspace.kill":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		command := strings.TrimPrefix(name, "workspace.")
		releaseModel := pendingModelRelease(stateDir, workspaceName, opts.Backend)
		result, err := mcpWorkspaceControl(ctx, opts, command)
		if err == nil && result.OK {
			releaseModel()
		}
		return jsonCompatible(result), true, err
	case "workspace.quarantine":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceQuarantine(ctx, opts, workspace.QuarantineOptions{})
		return jsonCompatible(result), true, err
	case "workspace.pause", "workspace.resume":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		command := strings.TrimPrefix(name, "workspace.")
		result, err := mcpWorkspaceControl(ctx, opts, command)
		return jsonCompatible(result), true, err
	case "workspace.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		releaseModel := pendingModelRelease(stateDir, workspaceName, opts.Backend)
		result, err := mcpWorkspaceDelete(ctx, opts, workspace.DeleteOptions{Force: boolArg(args, "force")})
		if err == nil && result.OK {
			releaseModel()
		}
		return jsonCompatible(result), true, err
	case "workspace.list":
		entries, err := workspace.List(stateDir)
		if err == nil {
			reconcileLiveWorkspaces(ctx, stateDir, entries)
			entries, err = workspace.List(stateDir)
		}
		return map[string]any{"workspaces": jsonCompatible(entries)}, true, err
	case "workspace.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		resp, err := workspace.Status(opts)
		if err == nil && resp.Event != nil && isLiveRecordedState(resp.Event.State) {
			if _, inspectErr := workspace.Inspect(ctx, opts); inspectErr == nil {
				resp, err = workspace.Status(opts)
			}
		}
		result := jsonCompatible(resp)
		if err == nil && !strings.EqualFold(stringArg(args, "format"), "full") {
			result = summarizeWorkspaceInspect(result, stateDir, workspaceName)
		}
		return result, true, err
	case "workspace.wait":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		timeout, err := optionalMCPDuration(args, "timeout")
		if err != nil {
			return nil, true, err
		}
		interval, err := optionalMCPDuration(args, "interval")
		if err != nil {
			return nil, true, err
		}
		result, err := workspace.Wait(ctx, opts, workspace.WaitOptions{Timeout: timeout, Interval: interval})
		return jsonCompatible(result), true, err
	case "workspace.result":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.ResultStatus(opts)
		return jsonCompatible(result), true, err
	case "workspace.stats":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.SampleStats(stateDir, workspaceName)
		return jsonCompatible(result), true, err
	case "workspace.logs":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		data, err := workspace.ReadLogs(stateDir, workspaceName)
		result := map[string]any{"workspace": workspaceName, "logs": string(data)}
		if err == nil && !strings.EqualFold(stringArg(args, "format"), "full") {
			return summarizeWorkspaceLogs(result, intArg(args, "tail_lines")), true, nil
		}
		return result, true, err
	case "workspace.events":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		events, err := workspace.ReadEvents(stateDir, workspaceName)
		result := map[string]any{"workspace": workspaceName, "events": jsonCompatible(events)}
		if err == nil && !strings.EqualFold(stringArg(args, "format"), "full") {
			return summarizeWorkspaceEvents(result, intArg(args, "limit"), intArg(args, "after_index")), true, nil
		}
		return result, true, err
	case "workspace.egress":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		mediator, err := workspace.ReadEgressAudit(stateDir, workspaceName)
		if err != nil {
			return nil, true, err
		}
		brokered, err := workspace.ReadBrokerAccess(stateDir, workspaceName)
		return map[string]any{
			"workspace": workspaceName,
			"egress":    jsonCompatible(workspace.MergeEgressEvents(mediator, brokered)),
		}, true, err
	case "workspace.clone":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceClone(
			stateDir,
			stringArg(args, "source"),
			stringArg(args, "target"),
		)
		return jsonCompatible(result), true, err
	case "workspace.apply":
		if err := requireToolArgs(args, name, "file"); err != nil {
			return nil, true, err
		}
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		supervisorPath := stringArg(args, "supervisor")
		if supervisorPath == "" {
			supervisorPath = defaultSupervisorPath(backend)
		}
		spec, err := mcpWorkspaceReadSpec(stringArg(args, "file"))
		if err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceApply(ctx, workspace.Options{
			Backend:        backend,
			Architecture:   architecture,
			StateDir:       stateDir,
			SupervisorPath: supervisorPath,
		}, spec)
		return jsonCompatible(result), true, err
	case "workspace.commit":
		if err := requireToolArgs(args, name, "name", "image"); err != nil {
			return nil, true, err
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		result, err := mcpWorkspaceCommit(ctx, commit.Options{
			StateDir:            stateDir,
			DebugFSPath:         defaultDebugFSPath(),
			Workspace:           workspaceName,
			Backend:             hostBackend(),
			Reference:           stringArg(args, "image"),
			AllowRegistryShadow: boolArg(args, "allow_registry_shadow"),
			Architecture:        architecture,
		})
		if err != nil {
			return nil, true, err
		}
		pushed := false
		if boolArg(args, "push") {
			if err := mcpWorkspaceCommitPush(ctx, stateDir, result.Reference); err != nil {
				return nil, true, err
			}
			pushed = true
		}
		return map[string]any{
			"reference":   result.Reference,
			"digest":      result.Digest,
			"size_bytes":  result.SizeBytes,
			"layout_path": result.LayoutPath,
			"pushed":      pushed,
		}, true, nil
	case "network.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.Network(stateDir, workspaceName)
		return jsonCompatible(result), true, err
	case "artifacts.list":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := workspace.ArtifactsFor(stateDir, workspaceName)
		return jsonCompatible(artifactsResult{Workspace: workspaceName, Artifacts: result}), true, err
	case "cp":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceCopy(
			ctx,
			stateDir,
			defaultDebugFSPath(),
			stringArg(args, "source"),
			stringArg(args, "target"),
		)
		return jsonCompatible(result), true, err
	case "artifacts.get":
		if err := requireToolArgs(args, name, "name", "artifact", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpWorkspaceGetArtifact(
			ctx,
			stateDir,
			defaultDebugFSPath(),
			workspaceName,
			stringArg(args, "artifact"),
			stringArg(args, "target"),
		)
		return jsonCompatible(result), true, err
	case "snapshot.list":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := vmkit.ListSnapshots(stateDir, workspaceName)
		return map[string]any{"workspace": workspaceName, "snapshots": jsonCompatible(result)}, true, err
	case "snapshot.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		tag := stringArg(args, "tag")
		create := mcpSnapshotCreate
		if boolArg(args, "forensic") {
			create = mcpSnapshotForensic
		}
		result, err := create(ctx, opts, tag)
		return jsonCompatible(result), true, err
	case "snapshot.delete":
		if err := requireToolArgs(args, name, "name", "tag"); err != nil {
			return nil, true, err
		}
		tag := stringArg(args, "tag")
		err := mcpSnapshotDelete(opts, tag)
		return map[string]any{"workspace": workspaceName, "removed": tag}, true, err
	case "volume.create":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := mcpVolumeCreate(
			ctx,
			stateDir,
			hostBackend(),
			workspaceName,
			int64Arg(args, "size_mib"),
			defaultMke2fsPath(),
		)
		return jsonCompatible(result), true, err
	case "volume.list":
		result, err := volume.List(stateDir)
		return map[string]any{"volumes": jsonCompatible(result)}, true, err
	case "volume.inspect":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		result, err := volume.Get(stateDir, workspaceName)
		return jsonCompatible(result), true, err
	case "volume.delete":
		if err := requireToolArgs(args, name, "name"); err != nil {
			return nil, true, err
		}
		err := mcpVolumeRemove(
			stateDir,
			workspaceName,
			boolArg(args, "force"),
			workspaceRunningPredicate(stateDir),
		)
		return map[string]any{"removed": workspaceName}, true, err
	case "images.pull":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		result, err := mcpImagePull(ctx, imagecache.PullOptions{
			StateDir:     stateDir,
			ImageRef:     stringArg(args, "image"),
			Architecture: stringArg(args, "arch"),
		})
		return jsonCompatible(result), true, err
	case "images.list":
		result, err := mcpImageList(stateDir)
		return map[string]any{"images": jsonCompatible(result)}, true, err
	case "images.push":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		image := stringArg(args, "image")
		err := mcpImagePush(ctx, stateDir, image)
		return map[string]any{"pushed": image}, true, err
	case "images.tag":
		if err := requireToolArgs(args, name, "source", "target"); err != nil {
			return nil, true, err
		}
		result, err := mcpImageTag(stateDir, stringArg(args, "source"), stringArg(args, "target"))
		return jsonCompatible(result), true, err
	case "images.delete":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		result, err := mcpImageRemove(stateDir, stringArg(args, "image"), boolArg(args, "delete_files"))
		return jsonCompatible(result), true, err
	case "images.prune":
		result, err := mcpImagePrune(stateDir, boolArg(args, "delete_files"))
		return jsonCompatible(result), true, err
	case "models.pull":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		result, err := mcpModelPull(ctx, model.PullOptions{
			StateDir: stateDir,
			ModelRef: stringArg(args, "model"),
			Token:    stringArg(args, "token"),
		})
		return jsonCompatible(result), true, err
	case "models.list":
		result, err := model.List(stateDir)
		return map[string]any{"models": jsonCompatible(result)}, true, err
	case "models.remove":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		result, err := mcpModelRemove(stateDir, stringArg(args, "model"), true)
		return jsonCompatible(result), true, err
	case "models.prune":
		result, err := mcpModelPrune(stateDir, false)
		return jsonCompatible(result), true, err
	case "models.serve":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		runnerArgs, _, err := stringSliceArg(args, "runner_args")
		if err != nil {
			return nil, true, err
		}
		runnerEnv, _, err := stringSliceArg(args, "runner_env")
		if err != nil {
			return nil, true, err
		}
		result, err := mcpModelServe(ctx, model.ServeOptions{
			StateDir: stateDir, ModelRef: stringArg(args, "model"),
			Token: stringArg(args, "token"), Dedicated: boolArg(args, "dedicated"),
			Runner: modelrunner.RunnerOverrides{
				Backend: stringArg(args, "runner"), GPU: stringArg(args, "runner_gpu"),
				BackendModel: stringArg(args, "runner_model"),
				ServedModel:  stringArg(args, "runner_served_model"),
				CommandRaw:   stringArg(args, "runner_command"),
				Name:         stringArg(args, "runner_name"),
				HealthPath:   stringArg(args, "runner_health_path"),
				Args:         runnerArgs, Env: runnerEnv,
			},
		})
		return result, true, err
	case "models.stop":
		if err := requireToolArgs(args, name, "model"); err != nil {
			return nil, true, err
		}
		canonical, _, err := model.Resolve(stringArg(args, "model"))
		if err != nil {
			return nil, true, err
		}
		stopped, err := mcpModelStop(stateDir, canonical)
		return map[string]any{"stopped": stopped}, true, err
	case "models.runners":
		result, err := modelrunner.List(stateDir)
		return map[string]any{"runners": jsonCompatible(result)}, true, err
	case "models.policy.validate":
		result, err := mcpPolicyValidate(stringArg(args, "policy_file"))
		return result, true, err
	case "models.policy.evaluate":
		var maxTokens *int
		if _, ok := args["max_tokens"]; ok {
			value := intArg(args, "max_tokens")
			maxTokens = &value
		}
		var stream *bool
		if _, ok := args["stream"]; ok {
			value := boolArg(args, "stream")
			stream = &value
		}
		tools, _, err := stringSliceArg(args, "tools")
		if err != nil {
			return nil, true, err
		}
		result, err := mcpPolicyEvaluate(stringArg(args, "policy_file"), hostworker.FilePolicyEvaluationOptions{
			Method: stringArg(args, "method"), Path: stringArg(args, "request_path"),
			WorkspaceID: stringArg(args, "workspace_id"), Capability: stringArg(args, "capability"),
			WorkerID: stringArg(args, "worker_id"), Model: stringArg(args, "model"),
			RequestBytes: int64Arg(args, "request_bytes"), TextBytes: int64Arg(args, "text_bytes"),
			Messages: intArg(args, "messages"), MaxTokens: maxTokens, Stream: stream,
			Tools: tools, Expect: stringArg(args, "expect"),
		})
		if err == nil && !result.MatchedExpect {
			err = fmt.Errorf("policy decision %s did not match expected %s", result.Decision, result.Expected)
		}
		return result, true, err
	case "profiles.list":
		return map[string]any{"profiles": jsonCompatible(resourceProfiles)}, true, nil
	case "contract.get":
		return jsonCompatible(vmkit.NewRuntimeContract()), true, nil
	case "host.inspect", "doctor.check":
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		supervisorPath := stringArg(args, "supervisor")
		if supervisorPath == "" {
			supervisorPath = defaultSupervisorPath(backend)
		}
		stateDir := stringArg(args, "state_dir")
		if stateDir == "" {
			stateDir = defaultStateDir()
		}
		result, err := mcpDiagnosticsCheck(ctx, diagnostics.Options{
			Backend:        backend,
			Arch:           architecture,
			SupervisorPath: supervisorPath,
			StateDir:       stateDir,
		})
		if name == "host.inspect" {
			err = nil
		}
		return jsonCompatible(result), true, err
	case "kernel.verify":
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		architecture = workspace.NormalizeArch(architecture)
		if err := workspace.ValidateArch(architecture); err != nil {
			return nil, true, err
		}
		path := stringArg(args, "path")
		if path == "" {
			path = defaultKernelPath(backend, architecture)
		}
		result, err := mcpKernelVerify(kernel.VerifyOptions{
			Path:         path,
			SHA256:       stringArg(args, "sha256"),
			Backend:      backend,
			Architecture: architecture,
		})
		return jsonCompatible(result), true, err
	case "kernel.install":
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = hostBackend()
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		architecture = workspace.NormalizeArch(architecture)
		if err := workspace.ValidateArch(architecture); err != nil {
			return nil, true, err
		}
		outputPath := stringArg(args, "out")
		if outputPath == "" {
			outputPath = workspace.WritableKernelPath(backend, architecture)
		}
		result, err := mcpKernelInstall(ctx, kernel.InstallOptions{
			URL:          stringArg(args, "url"),
			FromPath:     stringArg(args, "from"),
			SHA256:       stringArg(args, "sha256"),
			OutputPath:   outputPath,
			Backend:      backend,
			Architecture: architecture,
		})
		return jsonCompatible(result), true, err
	case "rootfs.build":
		if err := requireToolArgs(args, name, "image"); err != nil {
			return nil, true, err
		}
		architecture := stringArg(args, "arch")
		if architecture == "" {
			architecture = defaultGuestArch()
		}
		sizeMiB := int64(rootfs.DefaultSizeMiB)
		autoSize := true
		if value := int64Arg(args, "size_mib"); value > 0 {
			sizeMiB = value
			autoSize = false
		}
		req := rootfs.BuildRequest{
			ImageRef: stringArg(args, "image"),
			Platform: rootfs.Platform{
				OS:           firstNonEmpty(stringArg(args, "os"), "linux"),
				Architecture: workspace.NormalizeArch(architecture),
			},
			OutputPath:    stringArg(args, "out"),
			InitPath:      firstNonEmpty(stringArg(args, "init"), rootfs.DefaultInitPath),
			StateDir:      stringArg(args, "state_dir"),
			BaseCacheDir:  rootfs.BaseCacheDirFor(stringArg(args, "state_dir")),
			Mke2fsPath:    firstNonEmpty(stringArg(args, "mke2fs"), defaultMke2fsPath()),
			SizeMiB:       sizeMiB,
			AutoSize:      autoSize,
			AllowMutable:  boolArg(args, "allow_mutable"),
			KeepStage:     boolArg(args, "keep_stage"),
			StageSnapshot: stringArg(args, "stage_snapshot"),
		}
		if command := stringArg(args, "exec"); command != "" {
			req.Command = []string{"/bin/sh", "-lc", command}
		}
		result, err := mcpRootfsBuild(ctx, req)
		return jsonCompatible(result), true, err
	default:
		return nil, false, nil
	}
}

func optionalMCPDuration(args map[string]any, name string) (time.Duration, error) {
	raw := stringArg(args, name)
	if raw == "" {
		return 0, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, operation.New(operation.ErrorValidation, "%s must be a positive Go duration such as 250ms or 5m", name)
	}
	return value, nil
}
