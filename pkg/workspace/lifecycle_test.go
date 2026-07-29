package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestDefaultSnapshotTagsAreLibraryOwnedAndDistinct(t *testing.T) {
	now := time.Date(2026, 7, 25, 3, 4, 5, 0, time.FixedZone("offset", -7*60*60))
	if got, want := DefaultSnapshotTag(now), "snap-20260725-100405"; got != want {
		t.Fatalf("DefaultSnapshotTag = %q, want %q", got, want)
	}
	if got, want := DefaultForensicSnapshotTag(now), "forensic-20260725-100405"; got != want {
		t.Fatalf("DefaultForensicSnapshotTag = %q, want %q", got, want)
	}
}

func TestSnapshotLibraryResolvesEmptyTagBeforeDispatch(t *testing.T) {
	opts := Options{Name: "agent-1", Backend: "unsupported-backend"}
	for name, snapshot := range map[string]func(context.Context, Options, string) (vmkit.SnapshotManifest, error){
		"ordinary": Snapshot,
		"forensic": SnapshotForensic,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := snapshot(t.Context(), opts, "")
			if err == nil {
				t.Fatal("snapshot error = nil")
			}
			if strings.Contains(err.Error(), "snapshot tag is required") {
				t.Fatalf("empty tag was rejected by library instead of resolved: %v", err)
			}
		})
	}
}

func TestSnapshotListAndRemoveAreHostSide(t *testing.T) {
	dir := t.TempDir()
	name := "agent-1"
	for _, tag := range []string{"snap-a", "snap-b"} {
		sdir := vmkit.SnapshotDir(dir, name, tag)
		if err := vmkit.WriteSnapshotManifest(sdir, vmkit.SnapshotManifest{Tag: tag, MemoryMiB: 512, CreatedAt: "2026-06-01T00:00:0" + tag[len(tag)-1:] + "Z"}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sdir, vmkit.SnapshotMemoryName), make([]byte, 256), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM}

	infos, err := SnapshotList(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("SnapshotList = %d entries, want 2", len(infos))
	}

	if err := SnapshotRemove(opts, "snap-a"); err != nil {
		t.Fatal(err)
	}
	infos, err = SnapshotList(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Tag != "snap-b" {
		t.Fatalf("after remove = %#v, want only snap-b", infos)
	}
}

func TestSnapshotRemoveRejectsMissingTag(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir(), Backend: vmkit.BackendLinuxKVM}
	if err := SnapshotRemove(opts, "ghost"); err == nil {
		t.Fatal("expected error removing a missing snapshot")
	}
}

func TestSnapshotCreateAppleVFRequiresRuntimeState(t *testing.T) {
	_, err := Snapshot(context.Background(), Options{Name: "agent-1", StateDir: t.TempDir(), Backend: vmkit.BackendAppleVF}, "base")
	if err == nil {
		t.Fatal("expected Apple VF snapshot create to require a live runtime state")
	}
	var unsupported vmkit.UnsupportedFeatureError
	if errors.As(err, &unsupported) {
		t.Fatalf("err = %#v, did not expect unsupported feature gap", unsupported)
	}
	if runtime.GOOS != "darwin" {
		if !strings.Contains(err.Error(), "is not available in this") {
			t.Fatalf("err = %q, want host backend rejection on %s", err.Error(), runtime.GOOS)
		}
		return
	}
	if !strings.Contains(err.Error(), "runtime.json") {
		t.Fatalf("err = %q, want missing runtime state", err.Error())
	}
}

func TestApplyForkSecretManifestCopiesSecretRefsForRehydrate(t *testing.T) {
	opts := Options{Name: "fork"}
	source := Manifest{
		Secrets:         []vmkit.SecretRef{{Name: "API", Ref: "env:API_TOKEN"}},
		SecretEnvFiles:  []string{"/tmp/app.env"},
		OnDemandSecrets: []vmkit.SecretRef{{Name: "DB", Ref: "env:DB_TOKEN"}},
		SecretsAudit:    true,
	}
	snapshot := vmkit.SnapshotManifest{Tag: "base", SecretsMaterialized: true, SecretsPurged: true}
	if err := applyForkSecretManifest(&opts, source, snapshot); err != nil {
		t.Fatalf("applyForkSecretManifest: %v", err)
	}
	if opts.Secrets["API"] != "env:API_TOKEN" {
		t.Fatalf("Secrets = %#v", opts.Secrets)
	}
	if len(opts.SecretEnvFiles) != 1 || opts.SecretEnvFiles[0] != "/tmp/app.env" {
		t.Fatalf("SecretEnvFiles = %#v", opts.SecretEnvFiles)
	}
	if opts.OnDemandSecrets["DB"] != "env:DB_TOKEN" || !opts.SecretsAudit {
		t.Fatalf("OnDemandSecrets = %#v SecretsAudit = %t", opts.OnDemandSecrets, opts.SecretsAudit)
	}
}

func TestApplyForkSecretManifestFailsWithoutMaterializedRefs(t *testing.T) {
	opts := Options{Name: "fork"}
	snapshot := vmkit.SnapshotManifest{Tag: "base", SecretsMaterialized: true, SecretsPurged: true}
	if err := applyForkSecretManifest(&opts, Manifest{}, snapshot); err == nil {
		t.Fatal("expected missing source secret refs to fail closed")
	}
}

func TestCopySnapshotIntoUsesManifestArtifacts(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	manifest := vmkit.SnapshotManifest{
		Tag:            "base",
		RootfsArtifact: "rootfs-copy.ext4",
		MachineStateArtifacts: []vmkit.SnapshotArtifact{
			{Kind: "apple-vf-machine-state", Path: "machine-state.vz"},
		},
	}
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		vmkit.SnapshotManifestName: "manifest",
		"rootfs-copy.ext4":         "rootfs",
		"machine-state.vz":         "state",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copySnapshotInto(src, dst, manifest); err != nil {
		t.Fatalf("copySnapshotInto: %v", err)
	}
	for name, want := range map[string]string{
		vmkit.SnapshotManifestName: "manifest",
		"rootfs-copy.ext4":         "rootfs",
		"machine-state.vz":         "state",
	} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestAppleVFSnapshotManifestFromStateRecordsRestoreContract(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := RuntimeState{
		Event: EventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendAppleVF},
			State:      vmkit.StateRunning,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config: vmkit.Config{
			StateDir:   dir,
			KernelPath: kernelPath,
			CPUCount:   4,
			MemoryMiB:  1024,
			ShellPort:  31001,
			ExecPort:   31002,
			Network: &vmkit.NetworkConfig{
				Mode:    "user",
				IP:      "10.0.2.15/24",
				Gateway: "10.0.2.2",
				Subnet:  "10.0.2.0/24",
			},
		},
	}
	manifest, err := appleVFSnapshotManifestFromState("base", state, Options{StateDir: dir, Name: "agent-1"}, nil, false)
	if err != nil {
		t.Fatalf("appleVFSnapshotManifestFromState: %v", err)
	}
	kernelSHA, err := fileSHA256(kernelPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tag != "base" || manifest.KernelSHA256 != kernelSHA || manifest.MemoryMiB != 1024 || manifest.VCPUCount != 4 {
		t.Fatalf("manifest core fields = %#v", manifest)
	}
	if manifest.RootfsArtifact != vmkit.SnapshotRootfsName {
		t.Fatalf("RootfsArtifact = %q", manifest.RootfsArtifact)
	}
	artifacts := vmkit.SnapshotMachineStateArtifacts(manifest)
	if len(artifacts) != 3 ||
		artifacts[0].Kind != "apple-vf-machine-state" ||
		artifacts[0].Path != vmkit.SnapshotAppleVFMachineState ||
		artifacts[1].Kind != "apple-vf-restore-config" ||
		artifacts[1].Path != vmkit.SnapshotAppleVFConfig ||
		artifacts[2].Kind != "config-disk" ||
		artifacts[2].Path != vmkit.SnapshotConfigDiskName {
		t.Fatalf("MachineStateArtifacts = %#v", artifacts)
	}
	if manifest.NetworkMode != "user" || manifest.GuestIP != "10.0.2.15" || manifest.NetworkGateway != "10.0.2.2" {
		t.Fatalf("network fields = %#v", manifest)
	}
}

// TestAppleVFSnapshotManifestRecordsPurgeReport: the manifest records the
// supervisor's own report of whether the guest secret purge ran, not an
// assumption about backend behavior. A forensic capture records secrets
// retained (which the restore path refuses); a missing report fails a forensic
// capture rather than mislabeling a purged image as evidence; an ordinary
// capture whose purge did not run fails closed.
func TestAppleVFSnapshotManifestRecordsPurgeReport(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	secretState := func() RuntimeState {
		return RuntimeState{
			Event: EventFile{
				Identity:   vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendAppleVF},
				State:      vmkit.StateRunning,
				ObservedAt: time.Now().UTC().Format(time.RFC3339),
			},
			Config: vmkit.Config{
				StateDir:           t.TempDir(),
				CPUCount:           2,
				MemoryMiB:          512,
				Secrets:            []vmkit.SecretRef{{Name: "API", Ref: "env:TOKEN"}},
				SecretsControlPort: 3100,
			},
		}
	}
	opts := Options{Name: "agent-1"}

	forensic, err := appleVFSnapshotManifestFromState("eve", secretState(), opts, boolPtr(false), true)
	if err != nil {
		t.Fatalf("forensic capture with retained-secrets report: %v", err)
	}
	if !forensic.SecretsMaterialized || forensic.SecretsPurged {
		t.Fatalf("forensic manifest must record secrets materialized and NOT purged, got %#v", forensic)
	}
	if err := vmkit.ValidateSnapshotSecretRestore(forensic, &vmkit.Config{}); err == nil {
		t.Fatal("a forensic capture must never validate for restore")
	}

	if _, err := appleVFSnapshotManifestFromState("eve", secretState(), opts, nil, true); err == nil || !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("forensic capture without a purge report must fail naming the supervisor, got %v", err)
	}

	ordinary, err := appleVFSnapshotManifestFromState("normal", secretState(), opts, boolPtr(true), false)
	if err != nil {
		t.Fatalf("ordinary capture with purge report: %v", err)
	}
	if !ordinary.SecretsMaterialized || !ordinary.SecretsPurged {
		t.Fatalf("ordinary manifest must record the reported purge, got %#v", ordinary)
	}

	if _, err := appleVFSnapshotManifestFromState("normal", secretState(), opts, boolPtr(false), false); err == nil {
		t.Fatal("an ordinary capture whose purge did not run must fail closed")
	}
}

func TestPrepareAppleVFSnapshotRestoreCopiesRootfsAndCaps(t *testing.T) {
	dir := t.TempDir()
	name := "agent-1"
	snapshotDir := vmkit.SnapshotDir(dir, name, "base")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	kernelSHA, err := fileSHA256(kernelPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, vmkit.SnapshotRootfsName), []byte("snapshot-rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := vmkit.SnapshotManifest{
		Tag:                      "base",
		KernelSHA256:             kernelSHA,
		RootfsArtifact:           vmkit.SnapshotRootfsName,
		MachineStateArtifacts:    vmkit.AppleVFSnapshotArtifacts(),
		EgressMaxBytesPerSec:     1024,
		EgressMaxTotalBytes:      2048,
		EgressMaxConcurrentConns: 3,
		EgressAuditMaxBytes:      4096,
		EgressAuditMaxBackups:    5,
	}
	if err := vmkit.WriteSnapshotManifest(snapshotDir, manifest); err != nil {
		t.Fatal(err)
	}
	rootfsPath := filepath.Join(dir, name, "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("old-rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &vmkit.Config{KernelPath: kernelPath, RootfsPath: rootfsPath}
	req := vmkit.Request{Tag: "base", Config: config}
	if err := prepareAppleVFSnapshotRestore(Options{StateDir: dir, Name: name}, req); err != nil {
		t.Fatalf("prepareAppleVFSnapshotRestore: %v", err)
	}
	data, err := os.ReadFile(rootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "snapshot-rootfs" {
		t.Fatalf("restored rootfs = %q", data)
	}
	if config.EgressMaxBytesPerSec != 1024 || config.EgressMaxTotalBytes != 2048 || config.EgressMaxConcurrentConns != 3 || config.EgressAuditMaxBytes != 4096 || config.EgressAuditMaxBackups != 5 {
		t.Fatalf("egress caps = %#v", config)
	}
}

func TestPrepareAppleVFSnapshotRestoreAppliesSavedVZConfig(t *testing.T) {
	dir := t.TempDir()
	name := "fork"
	snapshotDir := vmkit.SnapshotDir(dir, name, "base")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	kernelSHA, err := fileSHA256(kernelPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, vmkit.SnapshotRootfsName), []byte("snapshot-rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vmkit.WriteSnapshotManifest(snapshotDir, vmkit.SnapshotManifest{Tag: "base", KernelSHA256: kernelSHA, RootfsArtifact: vmkit.SnapshotRootfsName}); err != nil {
		t.Fatal(err)
	}
	saved := vmkit.Config{
		KernelPath:               "/source/Image",
		RootfsPath:               "/source/rootfs.ext4",
		StateDir:                 "/source/state",
		AppleVFMachineIdentifier: "saved-machine-id",
		MemoryMiB:                512,
		CPUCount:                 2,
		ShellPort:                31001,
		ExecPort:                 51001,
		CACertPort:               1027,
		SecretsControlPort:       1028,
		VsockListeners:           []vmkit.VsockListener{{Port: 1024, Target: "/source/result.json"}},
		EgressMaxBytesPerSec:     12,
	}
	if err := writeJSONFile(filepath.Join(snapshotDir, vmkit.SnapshotAppleVFConfig), saved); err != nil {
		t.Fatal(err)
	}
	rootfsPath := filepath.Join(dir, name, "rootfs.ext4")
	config := &vmkit.Config{
		KernelPath:     kernelPath,
		RootfsPath:     rootfsPath,
		StateDir:       dir,
		ShellPort:      32001,
		ExecPort:       52001,
		GuestShellPort: 31001,
		GuestExecPort:  51001,
		VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(dir, name, "result.json")}},
	}
	if err := prepareAppleVFSnapshotRestore(Options{StateDir: dir, Name: name}, vmkit.Request{Tag: "base", Config: config}); err != nil {
		t.Fatalf("prepareAppleVFSnapshotRestore: %v", err)
	}
	if config.KernelPath != kernelPath || config.RootfsPath != rootfsPath || config.StateDir != dir {
		t.Fatalf("identity paths not preserved: %#v", config)
	}
	if config.AppleVFMachineIdentifier != "saved-machine-id" {
		t.Fatalf("AppleVFMachineIdentifier = %q", config.AppleVFMachineIdentifier)
	}
	if config.ShellPort != 32001 || config.ExecPort != 52001 || config.GuestShellPort != 31001 || config.GuestExecPort != 51001 {
		t.Fatalf("port mapping = %#v", config)
	}
	if len(config.VsockListeners) != 1 || config.VsockListeners[0].Target != filepath.Join(dir, name, "result.json") {
		t.Fatalf("vsock listeners = %#v", config.VsockListeners)
	}
	if config.CACertPort != 1027 || config.SecretsControlPort != 1028 {
		t.Fatalf("saved VZ-sensitive ports not applied: %#v", config)
	}
}
