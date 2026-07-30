package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestMCPWorkspaceCreateRejectsSnapshotTagTraversal(t *testing.T) {
	_, err := runMCPWorkspaceCreate(t.Context(), map[string]any{
		"name":          "fork",
		"from_snapshot": "source:../../../planted",
		"state_dir":     t.TempDir(),
		"dry_run":       true,
	})
	if err == nil {
		t.Fatal("workspace.create accepted snapshot tag traversal")
	}
	if !operation.IsKind(err, operation.ErrorValidation) {
		t.Fatalf("workspace.create error = %#v, want typed validation error", err)
	}
}

func TestMCPWorkspaceCreateOptionsUseTypedConfiguration(t *testing.T) {
	opts, err := mcpWorkspaceCreateOptions(map[string]any{
		"name": "demo", "image": "docker.io/library/busybox:1.36",
		"network": "isolated", "dry_run": true,
		"model_runner_args": []any{"--max-model-len", "2048"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Name != "demo" || opts.ImageRef != "docker.io/library/busybox:1.36" ||
		opts.Network.Mode != "isolated" || !opts.DryRun ||
		!reflect.DeepEqual(opts.ModelRunner.Args, []string{"--max-model-len", "2048"}) {
		t.Fatalf("options = %+v", opts)
	}
}

func TestMCPWorkspaceEstimateCost(t *testing.T) {
	result := estimateWorkspaceCost(map[string]any{"profile": "tiny", "price_per_hour": 0.25})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "estimated_cost_hour") || !strings.Contains(string(data), "memory_mib") {
		t.Fatalf("estimate = %s", data)
	}
}

func TestMCPDeletePreview(t *testing.T) {
	result, err := runMCPTool(context.Background(), "workspace.delete", map[string]any{"name": "demo", "preview": true})
	if err != nil {
		t.Fatalf("runMCPTool preview: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"preview":true`) || !strings.Contains(string(data), "remove workspace disk and state") {
		t.Fatalf("preview = %s", data)
	}
}

func TestMCPManagementDeletePreview(t *testing.T) {
	for _, tool := range []string{"volume.delete", "snapshot.delete", "images.delete", "images.prune"} {
		t.Run(tool, func(t *testing.T) {
			args := map[string]any{"name": "demo", "tag": "snap", "image": "example.com/acme/demo:old", "preview": true, "force": true, "delete_files": true}
			result, err := runMCPTool(context.Background(), tool, args)
			if err != nil {
				t.Fatalf("runMCPTool preview: %v", err)
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"preview":true`) {
				t.Fatalf("preview = %s", data)
			}
		})
	}
}

func TestMCPReadPathsUseTypedHandlers(t *testing.T) {
	stateDir := t.TempDir()
	tests := []struct {
		name string
		args map[string]any
		key  string
	}{
		{name: "workspace.list", args: map[string]any{"state_dir": stateDir}, key: "workspaces"},
		{name: "volume.list", args: map[string]any{"state_dir": stateDir}, key: "volumes"},
		{name: "images.list", args: map[string]any{"state_dir": stateDir}, key: "images"},
		{name: "models.list", args: map[string]any{"state_dir": stateDir}, key: "models"},
		{name: "profiles.list", args: map[string]any{}, key: "profiles"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, handled, err := runDirectMCPTool(t.Context(), tc.name, tc.args)
			if err != nil {
				t.Fatalf("runDirectMCPTool: %v", err)
			}
			if !handled {
				t.Fatal("runDirectMCPTool handled = false")
			}
			object, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("result type = %T, want map", result)
			}
			if _, ok := object[tc.key]; !ok {
				t.Fatalf("result = %#v, want key %q", object, tc.key)
			}
		})
	}
}

func TestMCPHostDiagnosticsUseTypedHandlers(t *testing.T) {
	oldCheck := mcpDiagnosticsCheck
	oldVerify := mcpKernelVerify
	t.Cleanup(func() {
		mcpDiagnosticsCheck = oldCheck
		mcpKernelVerify = oldVerify
	})

	checkErr := errors.New("host unavailable")
	mcpDiagnosticsCheck = func(_ context.Context, opts diagnostics.Options) (vmkit.Response, error) {
		if opts.Backend != "apple-vf" || opts.Arch != "arm64" || opts.SupervisorPath != "/tmp/helper" {
			t.Fatalf("diagnostics opts = %#v", opts)
		}
		return vmkit.Response{OK: false, Backend: opts.Backend, Error: checkErr.Error()}, checkErr
	}
	result, handled, err := runDirectMCPTool(t.Context(), "host.inspect", map[string]any{
		"backend": "apple-vf", "arch": "arm64", "supervisor": "/tmp/helper",
	})
	if err != nil || !handled || result.(map[string]any)["error"] != checkErr.Error() {
		t.Fatalf("host.inspect: handled=%v err=%v result=%#v", handled, err, result)
	}
	_, handled, err = runDirectMCPTool(t.Context(), "doctor.check", map[string]any{
		"backend": "apple-vf", "arch": "arm64", "supervisor": "/tmp/helper",
	})
	if !handled || !errors.Is(err, checkErr) {
		t.Fatalf("doctor.check: handled=%v err=%v", handled, err)
	}

	mcpKernelVerify = func(opts kernel.VerifyOptions) (kernel.VerifyResult, error) {
		if opts.Path != "/tmp/vmlinux" || opts.SHA256 != "abc" || opts.Backend != "linux-kvm" || opts.Architecture != "amd64" {
			t.Fatalf("verify opts = %#v", opts)
		}
		return kernel.VerifyResult{OK: true, Verified: true, Path: opts.Path, SHA256: opts.SHA256}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "kernel.verify", map[string]any{
		"path": "/tmp/vmlinux", "sha256": "abc", "backend": "linux-kvm", "arch": "amd64",
	})
	if err != nil || !handled || result.(map[string]any)["verified"] != true {
		t.Fatalf("kernel.verify: handled=%v err=%v result=%#v", handled, err, result)
	}
}

func TestMCPKernelInstallUsesTypedHandler(t *testing.T) {
	oldInstall := mcpKernelInstall
	t.Cleanup(func() {
		mcpKernelInstall = oldInstall
	})

	mcpKernelInstall = func(_ context.Context, opts kernel.InstallOptions) (kernel.InstallResult, error) {
		if opts.URL != "https://example.test/vmlinux" || opts.FromPath != "" || opts.SHA256 != "abc" ||
			opts.OutputPath != "/tmp/vmlinux" || opts.Backend != "linux-kvm" || opts.Architecture != "amd64" {
			t.Fatalf("install opts = %#v", opts)
		}
		return kernel.InstallResult{Path: opts.OutputPath, SHA256: opts.SHA256}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "kernel.install", map[string]any{
		"url":     "https://example.test/vmlinux",
		"sha256":  "abc",
		"out":     "/tmp/vmlinux",
		"backend": "linux-kvm",
		"arch":    "amd64",
	})
	if err != nil || !handled || result.(map[string]any)["path"] != "/tmp/vmlinux" {
		t.Fatalf("kernel.install: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpKernelInstall = func(_ context.Context, opts kernel.InstallOptions) (kernel.InstallResult, error) {
		if opts.Backend != hostBackend() || opts.Architecture != defaultGuestArch() {
			t.Fatalf("default install opts = %#v", opts)
		}
		if opts.OutputPath != workspace.WritableKernelPath(opts.Backend, opts.Architecture) {
			t.Fatalf("default output path = %q", opts.OutputPath)
		}
		return kernel.InstallResult{Path: opts.OutputPath}, nil
	}
	_, handled, err = runDirectMCPTool(t.Context(), "kernel.install", map[string]any{})
	if err != nil || !handled {
		t.Fatalf("kernel.install defaults: handled=%v err=%v", handled, err)
	}
}

func TestMCPRootfsBuildUsesTypedHandler(t *testing.T) {
	oldBuild := mcpRootfsBuild
	t.Cleanup(func() {
		mcpRootfsBuild = oldBuild
	})

	mcpRootfsBuild = func(_ context.Context, req rootfs.BuildRequest) (rootfs.Provenance, error) {
		if req.ImageRef != "alpine:3.20" || req.Platform.OS != "linux" || req.Platform.Architecture != "amd64" ||
			req.OutputPath != "/tmp/rootfs.ext4" || req.InitPath != "/init" || req.StateDir != "/tmp/state" ||
			req.Mke2fsPath != "/usr/bin/mke2fs" || req.SizeMiB != 2048 || req.AutoSize ||
			!req.AllowMutable || !req.KeepStage || req.StageSnapshot != "/tmp/stage" {
			t.Fatalf("build req = %#v", req)
		}
		if !reflect.DeepEqual(req.Command, []string{"/bin/sh", "-lc", "echo ready"}) {
			t.Fatalf("build command = %#v", req.Command)
		}
		return rootfs.Provenance{ImageRef: req.ImageRef, OutputPath: req.OutputPath}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "rootfs.build", map[string]any{
		"image":          "alpine:3.20",
		"os":             "linux",
		"arch":           "amd64",
		"out":            "/tmp/rootfs.ext4",
		"init":           "/init",
		"state_dir":      "/tmp/state",
		"mke2fs":         "/usr/bin/mke2fs",
		"size_mib":       float64(2048),
		"exec":           "echo ready",
		"allow_mutable":  true,
		"keep_stage":     true,
		"stage_snapshot": "/tmp/stage",
	})
	if err != nil || !handled || result.(map[string]any)["output_path"] != "/tmp/rootfs.ext4" {
		t.Fatalf("rootfs.build: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpRootfsBuild = func(_ context.Context, req rootfs.BuildRequest) (rootfs.Provenance, error) {
		if req.Platform.OS != "linux" || req.Platform.Architecture != workspace.NormalizeArch(defaultGuestArch()) ||
			req.InitPath != rootfs.DefaultInitPath || req.Mke2fsPath != "mke2fs" ||
			req.SizeMiB != rootfs.DefaultSizeMiB || !req.AutoSize {
			t.Fatalf("default build req = %#v", req)
		}
		return rootfs.Provenance{ImageRef: req.ImageRef}, nil
	}
	_, handled, err = runDirectMCPTool(t.Context(), "rootfs.build", map[string]any{"image": "example@sha256:abc"})
	if err != nil || !handled {
		t.Fatalf("rootfs.build defaults: handled=%v err=%v", handled, err)
	}
}

func TestMCPWorkspaceCloneUsesTypedHandler(t *testing.T) {
	oldClone := mcpWorkspaceClone
	t.Cleanup(func() {
		mcpWorkspaceClone = oldClone
	})

	mcpWorkspaceClone = func(stateDir, source, target string) (workspace.Result, error) {
		if stateDir != "/tmp/state" || source != "demo" || target != "copy" {
			t.Fatalf("clone args: stateDir=%q source=%q target=%q", stateDir, source, target)
		}
		return workspace.Result{Workspace: target, StateDir: stateDir}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "workspace.clone", map[string]any{
		"source": "demo", "target": "copy", "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("workspace.clone: handled=%v err=%v", handled, err)
	}
	object := result.(map[string]any)
	if object["workspace"] != "copy" || object["state_dir"] != "/tmp/state" {
		t.Fatalf("workspace.clone result = %#v", result)
	}
}

func TestMCPWorkspaceApplyUsesTypedHandler(t *testing.T) {
	oldReadSpec := mcpWorkspaceReadSpec
	oldApply := mcpWorkspaceApply
	t.Cleanup(func() {
		mcpWorkspaceReadSpec = oldReadSpec
		mcpWorkspaceApply = oldApply
	})

	mcpWorkspaceReadSpec = func(path string) (workspace.Spec, error) {
		if path != "/tmp/microagent.yaml" {
			t.Fatalf("spec path = %q", path)
		}
		return workspace.Spec{Name: "demo"}, nil
	}
	mcpWorkspaceApply = func(_ context.Context, opts workspace.Options, spec workspace.Spec) (workspace.ApplyResult, error) {
		if opts.StateDir != "/tmp/state" || opts.Backend != "apple-vf" || opts.Architecture != "arm64" || opts.SupervisorPath != "/tmp/helper" {
			t.Fatalf("apply opts = %#v", opts)
		}
		if spec.Name != "demo" {
			t.Fatalf("apply spec = %#v", spec)
		}
		return workspace.ApplyResult{Workspace: spec.Name, Applied: []string{"network"}}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "workspace.apply", map[string]any{
		"file":       "/tmp/microagent.yaml",
		"state_dir":  "/tmp/state",
		"backend":    "apple-vf",
		"arch":       "arm64",
		"supervisor": "/tmp/helper",
	})
	if err != nil || !handled {
		t.Fatalf("workspace.apply: handled=%v err=%v", handled, err)
	}
	object := result.(map[string]any)
	if object["workspace"] != "demo" || len(object["applied"].([]any)) != 1 {
		t.Fatalf("workspace.apply result = %#v", result)
	}

	mcpWorkspaceApply = func(_ context.Context, opts workspace.Options, spec workspace.Spec) (workspace.ApplyResult, error) {
		if opts.Backend != hostBackend() || opts.Architecture != defaultGuestArch() {
			t.Fatalf("default apply opts = %#v", opts)
		}
		if opts.SupervisorPath != defaultSupervisorPath(opts.Backend) {
			t.Fatalf("default supervisor = %q", opts.SupervisorPath)
		}
		return workspace.ApplyResult{Workspace: spec.Name}, nil
	}
	_, handled, err = runDirectMCPTool(t.Context(), "workspace.apply", map[string]any{
		"file": "/tmp/microagent.yaml",
	})
	if err != nil || !handled {
		t.Fatalf("workspace.apply defaults: handled=%v err=%v", handled, err)
	}
}

func TestMCPWorkspaceCommitUsesTypedHandler(t *testing.T) {
	oldCommit := mcpWorkspaceCommit
	oldPush := mcpWorkspaceCommitPush
	t.Cleanup(func() {
		mcpWorkspaceCommit = oldCommit
		mcpWorkspaceCommitPush = oldPush
	})

	const imageRef = "example.com/acme/demo:rc"
	mcpWorkspaceCommit = func(_ context.Context, opts commit.Options) (commit.Result, error) {
		if opts.StateDir != "/tmp/state" || opts.DebugFSPath == "" || opts.Workspace != "demo" ||
			opts.Backend != hostBackend() || opts.Reference != imageRef || opts.Architecture != "arm64" {
			t.Fatalf("commit opts = %#v", opts)
		}
		return commit.Result{
			Reference:  opts.Reference,
			Digest:     "sha256:abc",
			SizeBytes:  42,
			LayoutPath: "/tmp/state/images/oci",
		}, nil
	}
	var pushed bool
	mcpWorkspaceCommitPush = func(_ context.Context, stateDir, ref string) error {
		if stateDir != "/tmp/state" || ref != imageRef {
			t.Fatalf("push args: stateDir=%q ref=%q", stateDir, ref)
		}
		pushed = true
		return nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "workspace.commit", map[string]any{
		"name": "demo", "image": imageRef, "state_dir": "/tmp/state", "arch": "arm64", "push": true,
	})
	if err != nil || !handled {
		t.Fatalf("workspace.commit: handled=%v err=%v", handled, err)
	}
	object := result.(map[string]any)
	if !pushed || object["reference"] != imageRef || object["pushed"] != true || object["size_bytes"] != int64(42) {
		t.Fatalf("workspace.commit pushed=%v result=%#v", pushed, result)
	}

	mcpWorkspaceCommit = func(_ context.Context, opts commit.Options) (commit.Result, error) {
		if opts.Architecture != defaultGuestArch() {
			t.Fatalf("default architecture = %q", opts.Architecture)
		}
		return commit.Result{Reference: opts.Reference}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "workspace.commit", map[string]any{
		"name": "demo", "image": imageRef,
	})
	if err != nil || !handled || result.(map[string]any)["pushed"] != false {
		t.Fatalf("workspace.commit defaults: handled=%v err=%v result=%#v", handled, err, result)
	}
}

func TestMCPLifecycleMutationsUseTypedHandlers(t *testing.T) {
	oldControl := mcpWorkspaceControl
	oldQuarantine := mcpWorkspaceQuarantine
	oldDelete := mcpWorkspaceDelete
	t.Cleanup(func() {
		mcpWorkspaceControl = oldControl
		mcpWorkspaceQuarantine = oldQuarantine
		mcpWorkspaceDelete = oldDelete
	})

	var commands []string
	mcpWorkspaceControl = func(_ context.Context, opts workspace.Options, command string) (vmkit.Response, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("control opts = %#v", opts)
		}
		commands = append(commands, command)
		return vmkit.Response{OK: true, Backend: opts.Backend}, nil
	}
	mcpWorkspaceQuarantine = func(_ context.Context, opts workspace.Options, qopts workspace.QuarantineOptions) (workspace.QuarantineResult, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" || qopts.SkipCapture {
			t.Fatalf("quarantine opts = %#v qopts=%#v", opts, qopts)
		}
		return workspace.QuarantineResult{
			Response: vmkit.Response{OK: true, Backend: opts.Backend},
			Captured: true,
		}, nil
	}
	var deleteForce bool
	mcpWorkspaceDelete = func(_ context.Context, opts workspace.Options, deleteOpts workspace.DeleteOptions) (workspace.DeleteResult, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("delete opts = %#v", opts)
		}
		deleteForce = deleteOpts.Force
		return workspace.DeleteResult{Response: vmkit.Response{OK: true, Backend: opts.Backend}, Deleted: true}, nil
	}

	for _, tool := range []string{"workspace.halt", "workspace.kill", "workspace.pause", "workspace.resume"} {
		result, handled, err := runDirectMCPTool(t.Context(), tool, map[string]any{"name": "demo", "state_dir": "/tmp/state"})
		if err != nil || !handled {
			t.Fatalf("%s: handled=%v err=%v", tool, handled, err)
		}
		if result.(map[string]any)["ok"] != true {
			t.Fatalf("%s result = %#v", tool, result)
		}
	}
	wantCommands := []string{"halt", "kill", "pause", "resume"}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", commands, wantCommands)
	}

	result, handled, err := runDirectMCPTool(t.Context(), "workspace.quarantine", map[string]any{"name": "demo", "state_dir": "/tmp/state"})
	if err != nil || !handled {
		t.Fatalf("quarantine: handled=%v err=%v", handled, err)
	}
	if result.(map[string]any)["captured"] != true {
		t.Fatalf("quarantine result = %#v", result)
	}

	_, handled, err = runDirectMCPTool(t.Context(), "workspace.delete", map[string]any{
		"name": "demo", "state_dir": "/tmp/state", "force": true,
	})
	if err != nil || !handled {
		t.Fatalf("delete: handled=%v err=%v", handled, err)
	}
	if !deleteForce {
		t.Fatal("delete force = false")
	}

}

func TestMCPVolumeMutationsUseTypedHandlers(t *testing.T) {
	oldCreate := mcpVolumeCreate
	oldRemove := mcpVolumeRemove
	t.Cleanup(func() {
		mcpVolumeCreate = oldCreate
		mcpVolumeRemove = oldRemove
	})

	mcpVolumeCreate = func(_ context.Context, stateDir, backend, name string, sizeMiB int64, mke2fsPath string) (volume.Record, error) {
		if stateDir != "/tmp/state" || backend != hostBackend() || name != "data" || sizeMiB != 2048 {
			t.Fatalf("create args: stateDir=%q backend=%q name=%q sizeMiB=%d", stateDir, backend, name, sizeMiB)
		}
		if mke2fsPath == "" {
			t.Fatal("create mke2fs path is empty")
		}
		return volume.Record{Name: name, SizeMiB: sizeMiB}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "volume.create", map[string]any{
		"name": "data", "size_mib": float64(2048), "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("volume.create: handled=%v err=%v", handled, err)
	}
	if result.(map[string]any)["name"] != "data" || result.(map[string]any)["size_mib"] != float64(2048) {
		t.Fatalf("volume.create result = %#v", result)
	}

	var removed bool
	mcpVolumeRemove = func(stateDir, name string, force bool, isRunning func(string) bool) error {
		if stateDir != "/tmp/state" || name != "data" || !force || isRunning == nil {
			t.Fatalf("remove args: stateDir=%q name=%q force=%v isRunningNil=%v", stateDir, name, force, isRunning == nil)
		}
		removed = true
		return nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "volume.delete", map[string]any{
		"name": "data", "force": true, "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("volume.delete: handled=%v err=%v", handled, err)
	}
	if !removed || result.(map[string]any)["removed"] != "data" {
		t.Fatalf("volume.delete removed=%v result=%#v", removed, result)
	}

}

func TestMCPImageManagementUsesTypedHandlers(t *testing.T) {
	oldPull := mcpImagePull
	oldList := mcpImageList
	oldPush := mcpImagePush
	oldTag := mcpImageTag
	oldRemove := mcpImageRemove
	oldPrune := mcpImagePrune
	t.Cleanup(func() {
		mcpImagePull = oldPull
		mcpImageList = oldList
		mcpImagePush = oldPush
		mcpImageTag = oldTag
		mcpImageRemove = oldRemove
		mcpImagePrune = oldPrune
	})

	const imageRef = "example.com/acme/demo:rc"
	mcpImagePull = func(_ context.Context, opts imagecache.PullOptions) (imagecache.Record, error) {
		if opts.StateDir != "/tmp/state" || opts.ImageRef != imageRef || opts.Architecture != "arm64" {
			t.Fatalf("pull opts = %#v", opts)
		}
		return imagecache.Record{ImageRef: opts.ImageRef}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "images.pull", map[string]any{
		"image": imageRef, "arch": "arm64", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["image_ref"] != imageRef {
		t.Fatalf("images.pull: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImageList = func(stateDir string) ([]imagecache.Record, error) {
		if stateDir != "/tmp/state" {
			t.Fatalf("list stateDir = %q", stateDir)
		}
		return []imagecache.Record{{ImageRef: imageRef}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.list", map[string]any{"state_dir": "/tmp/state"})
	if err != nil || !handled || len(result.(map[string]any)["images"].([]any)) != 1 {
		t.Fatalf("images.list: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImagePush = func(_ context.Context, stateDir, image string) error {
		if stateDir != "/tmp/state" || image != imageRef {
			t.Fatalf("push args: stateDir=%q image=%q", stateDir, image)
		}
		return nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.push", map[string]any{
		"image": imageRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["pushed"] != imageRef {
		t.Fatalf("images.push: handled=%v err=%v result=%#v", handled, err, result)
	}

	const targetRef = "example.com/acme/demo:stable"
	mcpImageTag = func(stateDir, source, target string) (imagecache.Record, error) {
		if stateDir != "/tmp/state" || source != imageRef || target != targetRef {
			t.Fatalf("tag args: stateDir=%q source=%q target=%q", stateDir, source, target)
		}
		return imagecache.Record{ImageRef: target}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.tag", map[string]any{
		"source": imageRef, "target": targetRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["image_ref"] != targetRef {
		t.Fatalf("images.tag: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImageRemove = func(stateDir, image string, deleteFiles bool) (imagecache.PruneResult, error) {
		if stateDir != "/tmp/state" || image != imageRef || !deleteFiles {
			t.Fatalf("remove args: stateDir=%q image=%q deleteFiles=%v", stateDir, image, deleteFiles)
		}
		return imagecache.PruneResult{Deleted: []imagecache.Record{{ImageRef: image}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.delete", map[string]any{
		"image": imageRef, "delete_files": true, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["deleted"].([]any)) != 1 {
		t.Fatalf("images.delete: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpImagePrune = func(stateDir string, deleteFiles bool) (imagecache.PruneResult, error) {
		if stateDir != "/tmp/state" || !deleteFiles {
			t.Fatalf("prune args: stateDir=%q deleteFiles=%v", stateDir, deleteFiles)
		}
		return imagecache.PruneResult{Removed: []imagecache.Record{{ImageRef: imageRef}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "images.prune", map[string]any{
		"delete_files": true, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["removed"].([]any)) != 1 {
		t.Fatalf("images.prune: handled=%v err=%v result=%#v", handled, err, result)
	}

}

func TestMCPModelManagementUsesTypedHandlers(t *testing.T) {
	oldPull := mcpModelPull
	oldRemove := mcpModelRemove
	oldPrune := mcpModelPrune
	oldStop := mcpModelStop
	t.Cleanup(func() {
		mcpModelPull = oldPull
		mcpModelRemove = oldRemove
		mcpModelPrune = oldPrune
		mcpModelStop = oldStop
	})

	const modelRef = "acme/demo/model.gguf"
	const canonicalRef = "hf.co/acme/demo@main/model.gguf"
	mcpModelPull = func(_ context.Context, opts model.PullOptions) (model.Record, error) {
		if opts.StateDir != "/tmp/state" || opts.ModelRef != modelRef || opts.Token != "secret" {
			t.Fatalf("pull opts = %#v", opts)
		}
		return model.Record{ModelRef: opts.ModelRef}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "models.pull", map[string]any{
		"model": modelRef, "token": "secret", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["model_ref"] != modelRef {
		t.Fatalf("models.pull: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpModelRemove = func(stateDir, ref string, deleteFiles bool) (model.PruneResult, error) {
		if stateDir != "/tmp/state" || ref != modelRef || !deleteFiles {
			t.Fatalf("remove args: stateDir=%q ref=%q deleteFiles=%v", stateDir, ref, deleteFiles)
		}
		return model.PruneResult{Removed: []model.Record{{ModelRef: ref}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "models.remove", map[string]any{
		"model": modelRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["removed"].([]any)) != 1 {
		t.Fatalf("models.remove: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpModelPrune = func(stateDir string, deleteFiles bool) (model.PruneResult, error) {
		if stateDir != "/tmp/state" || deleteFiles {
			t.Fatalf("prune args: stateDir=%q deleteFiles=%v", stateDir, deleteFiles)
		}
		return model.PruneResult{Removed: []model.Record{{ModelRef: modelRef}}}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "models.prune", map[string]any{
		"state_dir": "/tmp/state",
	})
	if err != nil || !handled || len(result.(map[string]any)["removed"].([]any)) != 1 {
		t.Fatalf("models.prune: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpModelStop = func(stateDir, ref string) (int, error) {
		if stateDir != "/tmp/state" || ref != canonicalRef {
			t.Fatalf("stop args: stateDir=%q ref=%q", stateDir, ref)
		}
		return 2, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "models.stop", map[string]any{
		"model": modelRef, "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["stopped"] != 2 {
		t.Fatalf("models.stop: handled=%v err=%v result=%#v", handled, err, result)
	}

}

func TestMCPFileTransfersUseTypedHandlers(t *testing.T) {
	oldCopy := mcpWorkspaceCopy
	oldGetArtifact := mcpWorkspaceGetArtifact
	t.Cleanup(func() {
		mcpWorkspaceCopy = oldCopy
		mcpWorkspaceGetArtifact = oldGetArtifact
	})

	mcpWorkspaceCopy = func(_ context.Context, stateDir, debugfsPath, source, target string) (workspace.CopyResult, error) {
		if stateDir != "/tmp/state" || debugfsPath == "" || source != "input.txt" || target != "demo:/workspace/input.txt" {
			t.Fatalf("copy args: stateDir=%q debugfsPath=%q source=%q target=%q", stateDir, debugfsPath, source, target)
		}
		return workspace.CopyResult{
			Workspace: "demo",
			Direction: "to-workspace",
			Source:    source,
			Target:    target,
		}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "cp", map[string]any{
		"source": "input.txt", "target": "demo:/workspace/input.txt", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["direction"] != "to-workspace" {
		t.Fatalf("cp: handled=%v err=%v result=%#v", handled, err, result)
	}

	mcpWorkspaceGetArtifact = func(_ context.Context, stateDir, debugfsPath, name, artifact, target string) (workspace.CopyResult, error) {
		if stateDir != "/tmp/state" || debugfsPath == "" || name != "demo" || artifact != "report" || target != "report.json" {
			t.Fatalf("artifact args: stateDir=%q debugfsPath=%q name=%q artifact=%q target=%q", stateDir, debugfsPath, name, artifact, target)
		}
		return workspace.CopyResult{
			Artifact:  artifact,
			Workspace: name,
			Direction: "from-workspace",
			Target:    target,
		}, nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "artifacts.get", map[string]any{
		"name": "demo", "artifact": "report", "target": "report.json", "state_dir": "/tmp/state",
	})
	if err != nil || !handled || result.(map[string]any)["artifact"] != "report" {
		t.Fatalf("artifacts.get: handled=%v err=%v result=%#v", handled, err, result)
	}

}

func TestMCPSnapshotMutationsUseTypedHandlers(t *testing.T) {
	oldCreate := mcpSnapshotCreate
	oldDelete := mcpSnapshotDelete
	t.Cleanup(func() {
		mcpSnapshotCreate = oldCreate
		mcpSnapshotDelete = oldDelete
	})

	var createdTag string
	mcpSnapshotCreate = func(_ context.Context, opts workspace.Options, tag string) (vmkit.SnapshotManifest, error) {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("create opts = %#v", opts)
		}
		createdTag = tag
		return vmkit.SnapshotManifest{Tag: "snap-library-default"}, nil
	}
	result, handled, err := runDirectMCPTool(t.Context(), "snapshot.create", map[string]any{
		"name": "demo", "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("snapshot.create: handled=%v err=%v", handled, err)
	}
	if createdTag != "" || result.(map[string]any)["tag"] != "snap-library-default" {
		t.Fatalf("snapshot.create tag=%q result=%#v", createdTag, result)
	}

	var deletedTag string
	mcpSnapshotDelete = func(opts workspace.Options, tag string) error {
		if opts.Name != "demo" || opts.StateDir != "/tmp/state" {
			t.Fatalf("delete opts = %#v", opts)
		}
		deletedTag = tag
		return nil
	}
	result, handled, err = runDirectMCPTool(t.Context(), "snapshot.delete", map[string]any{
		"name": "demo", "tag": "before-upgrade", "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("snapshot.delete: handled=%v err=%v", handled, err)
	}
	if deletedTag != "before-upgrade" || result.(map[string]any)["removed"] != deletedTag {
		t.Fatalf("snapshot.delete tag=%q result=%#v", deletedTag, result)
	}

}

func TestMCPHostMutationPreviewAndConfirmation(t *testing.T) {
	args := map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "preview": true}
	preview, err := runMCPTool(context.Background(), "kernel.install", args)
	if err != nil {
		t.Fatalf("runMCPTool preview: %v", err)
	}
	result, ok := preview["result"].(map[string]any)
	if !ok {
		t.Fatalf("preview result type = %T", preview["result"])
	}
	token, ok := result["confirmation_token"].(string)
	if !ok || token == "" {
		t.Fatalf("confirmation_token = %#v", result["confirmation_token"])
	}
	confirmedArgs := map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "confirm_token": token}
	if confirmation, err := requireConfirmedMCPHostMutation("kernel.install", confirmedArgs); err != nil || confirmation != nil {
		t.Fatalf("confirmed mutation: confirmation=%#v err=%v", confirmation, err)
	}
	if _, err := runMCPTool(context.Background(), "kernel.install", map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc"}); err == nil {
		t.Fatal("runMCPTool without confirm_token err = nil, want confirmation error")
	}
}
