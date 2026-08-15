package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func stageCapacityFork(t *testing.T, supervisorPath string) Options {
	t.Helper()
	stateDir := t.TempDir()
	backend := HostBackend()
	snapshotDir := vmkit.SnapshotDir(stateDir, "source", "base")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := vmkit.FirecrackerSnapshotArtifacts()
	if backend == vmkit.BackendAppleVF {
		artifacts = vmkit.AppleVFSnapshotArtifacts()
	}
	manifest := vmkit.SnapshotManifest{
		Tag:                   "base",
		ImageRef:              "docker.io/library/busybox:latest",
		NetworkMode:           "isolated",
		MemoryMiB:             512,
		VCPUCount:             1,
		RootfsArtifact:        vmkit.SnapshotRootfsName,
		MachineStateArtifacts: artifacts,
	}
	if err := vmkit.WriteSnapshotManifest(snapshotDir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, vmkit.SnapshotRootfsName), []byte("snapshot-rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		path := filepath.Join(snapshotDir, artifact.Path)
		if artifact.Path == vmkit.SnapshotAppleVFConfig {
			if err := writeJSONFile(path, vmkit.Config{}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte(artifact.Kind), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kernelPath := filepath.Join(stateDir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Name = "fork-target"
	opts.StateDir = stateDir
	opts.Backend = backend
	opts.KernelPath = kernelPath
	opts.KernelExplicit = true
	opts.SupervisorPath = supervisorPath
	opts.EgressMode = vmkit.EgressModeOff
	opts.Network = vmkit.NetworkConfig{Mode: "isolated"}
	return opts
}

func capacitySuccessSupervisor(t *testing.T, backend string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capacity-supervisor")
	body := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\nprintf '%%s\\n' '%s'\n", `{"ok":true,"backend":"`+backend+`"}`)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertCapacityReservationReleased(t *testing.T, opts Options) {
	t.Helper()
	path := filepath.Join(opts.StateDir, capacityReservationDir, opts.Name+".lock")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("capacity reservation remains at %s after operation (err=%v)", path, err)
	}
}

func TestCreateFromSnapshotHandsReservationToStart(t *testing.T) {
	t.Setenv(MaxWorkspacesEnv, "1")
	opts := stageCapacityFork(t, "")
	opts.SupervisorPath = capacitySuccessSupervisor(t, opts.Backend)
	var phases []string
	opts.Progress = func(event operation.ProgressEvent) {
		phases = append(phases, event.Phase)
	}

	result, err := CreateFromSnapshot(context.Background(), opts, "source", "base")
	if err != nil {
		t.Fatalf("CreateFromSnapshot: %v", err)
	}
	if !result.Response.OK || result.Workspace != opts.Name {
		t.Fatalf("result = %#v", result)
	}
	assertCapacityReservationReleased(t, opts)
	assertProgressPhaseOrder(t, phases, []string{
		"fork_validate",
		"fork_rootfs",
		"fork_metadata",
		"fork_start",
		"start_validate",
		"start_prepare",
		"snapshot_restore_prepare",
		"start_vm",
		"snapshot_clock",
	})
}

func TestStartStillRejectsDuplicateCapacityReservation(t *testing.T) {
	t.Setenv(MaxWorkspacesEnv, "2")
	opts := stageCapacityFork(t, filepath.Join(t.TempDir(), "missing-supervisor"))
	reservation, err := reserveWorkspaceCapacity(opts)
	if err != nil {
		t.Fatalf("reserve workspace: %v", err)
	}
	defer reservation.Release()

	_, err = Start(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "already has a capacity reservation") {
		t.Fatalf("Start with duplicate reservation = %v, want duplicate rejection", err)
	}
}

func TestCreateFromSnapshotReleasesReservationAfterStartFailure(t *testing.T) {
	t.Setenv(MaxWorkspacesEnv, "1")
	opts := stageCapacityFork(t, filepath.Join(t.TempDir(), "missing-supervisor"))

	_, err := CreateFromSnapshot(context.Background(), opts, "source", "base")
	if err == nil {
		t.Fatal("CreateFromSnapshot with missing supervisor succeeded")
	}
	if strings.Contains(err.Error(), "capacity reservation") {
		t.Fatalf("CreateFromSnapshot reacquired its own capacity reservation: %v", err)
	}
	assertCapacityReservationReleased(t, opts)
}

func TestCapacityReservationCannotBeHandedToAnotherWorkspace(t *testing.T) {
	opts := stageCapacityFork(t, filepath.Join(t.TempDir(), "missing-supervisor"))
	reservation, err := reserveWorkspaceCapacity(opts)
	if err != nil {
		t.Fatalf("reserve workspace: %v", err)
	}
	defer reservation.Release()

	other := opts
	other.Name = "other-workspace"
	_, err = startWithCapacityReservation(context.Background(), other, reservation)
	if err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("mismatched reservation handoff = %v, want rejection", err)
	}
}
