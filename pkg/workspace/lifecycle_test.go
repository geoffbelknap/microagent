package workspace

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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
	if len(artifacts) != 2 ||
		artifacts[0].Kind != "apple-vf-machine-state" ||
		artifacts[0].Path != vmkit.SnapshotAppleVFMachineState ||
		artifacts[1].Kind != "apple-vf-restore-config" ||
		artifacts[1].Path != vmkit.SnapshotAppleVFConfig {
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

func TestPauseAndResumeDispatchControlCommands(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "agent-1",
		StateDir:       dir,
		Backend:        vmkit.BackendLinuxKVM,
		SupervisorPath: filepath.Join(dir, "no-such-supervisor"),
	}
	// With a missing supervisor binary, both calls fail at dispatch — but they
	// must get past Control's command whitelist, proving pause/resume are wired
	// through as supervisor commands rather than rejected as unsupported.
	if _, err := Pause(context.Background(), opts); err == nil || strings.Contains(err.Error(), "unsupported workspace control command") {
		t.Fatalf("Pause not wired to a pause control command: %v", err)
	}
	if _, err := Resume(context.Background(), opts); err == nil || strings.Contains(err.Error(), "unsupported workspace control command") {
		t.Fatalf("Resume not wired to a resume control command: %v", err)
	}
}

func TestDeleteBlockedByStateOnlyBlocksLiveStates(t *testing.T) {
	want := map[vmkit.VMState]bool{
		vmkit.StateRunning:     true,
		vmkit.StateStarting:    true,
		vmkit.StatePaused:      true,
		vmkit.StateStopped:     false,
		vmkit.StateHalted:      false,
		vmkit.StateFailed:      false,
		vmkit.StateStopping:    false,
		vmkit.StateQuarantined: false,
		vmkit.StatePrepared:    false,
		vmkit.StateUnknown:     false,
	}
	for state, blocked := range want {
		if got := deleteBlockedByState(state); got != blocked {
			t.Errorf("deleteBlockedByState(%s) = %v, want %v", state, got, blocked)
		}
	}
}

// writeFakeControlSupervisor writes an executable stub supervisor that answers
// inspect with the given state and records each delete into deleteLog. It lets a
// test drive the shared control-layer delete guard without a real backend.
func writeFakeControlSupervisor(t *testing.T, dir, inspectState, deleteLog string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-supervisor")
	backend := HostBackend()
	event := func(state string) string {
		return `{"ok":true,"backend":"` + backend + `","event":{"identity":{"requestID":"r","runtimeID":"agent-1","role":"workload","backend":"` + backend + `"},"state":"` + state + `","observedAt":"2026-01-01T00:00:00Z"}}`
	}
	body := "#!/bin/sh\nreq=$(cat)\ncase \"$req\" in\n" +
		"  *'\"command\":\"inspect\"'*) printf '%s' '" + event(inspectState) + "' ;;\n" +
		"  *'\"command\":\"delete\"'*) printf x >> " + shellQuoteForTest(deleteLog) + "; printf '%s' '" + event("stopped") + "' ;;\n" +
		"  *) printf '%s' '{\"ok\":true,\"backend\":\"linux-kvm\"}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestControlDeleteRefusesLiveWorkspaceBeforeDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell supervisor is POSIX-only")
	}
	dir := t.TempDir()
	deleteLog := filepath.Join(dir, "delete.log")

	// A live workspace: delete is refused and never reaches the supervisor. Use
	// the host backend (both linux-kvm and apple-vf route delete through the same
	// shared control guard) so the fake supervisor passes ValidateHostBackend.
	opts := Options{
		Name:           "agent-1",
		StateDir:       dir,
		Backend:        HostBackend(),
		SupervisorPath: writeFakeControlSupervisor(t, dir, "running", deleteLog),
	}
	resp, err := Control(context.Background(), opts, "delete")
	if err == nil || !strings.Contains(err.Error(), "stop or kill it before delete") {
		t.Fatalf("delete of running workspace err=%v resp=%#v, want refusal", err, resp)
	}
	if _, statErr := os.Stat(deleteLog); !os.IsNotExist(statErr) {
		t.Fatalf("supervisor delete was dispatched for a running workspace")
	}

	// A stopped workspace: delete proceeds to the supervisor.
	opts.SupervisorPath = writeFakeControlSupervisor(t, dir, "stopped", deleteLog)
	if resp, err := Control(context.Background(), opts, "delete"); err != nil || !resp.OK {
		t.Fatalf("delete of stopped workspace err=%v resp=%#v, want success", err, resp)
	}
	if _, statErr := os.Stat(deleteLog); statErr != nil {
		t.Fatalf("supervisor delete was not dispatched for a stopped workspace: %v", statErr)
	}
}

func TestDeleteNeedsStoppedRecognizesLiveStates(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"workspace agent-1 is running; stop or kill it before delete", true},
		{"workspace agent-1 is paused; stop or kill it before delete", true},
		{"workspace agent-1 is starting; stop or kill it before delete", true},
		{"firecracker workspace agent-1 is running; stop or kill it before delete", true},
		{"workspace agent-1 is quarantined; stop it before delete", false},
		{"workspace agent-1 not found", false},
		{"some unrelated failure", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := deleteNeedsStopped(errors.New(tc.text), vmkit.Response{}); got != tc.want {
			t.Errorf("deleteNeedsStopped(%q) = %v, want %v", tc.text, got, tc.want)
		}
		if got := deleteNeedsStopped(nil, vmkit.Response{Error: tc.text}); got != tc.want {
			t.Errorf("deleteNeedsStopped(resp=%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestPauseAndResumeUseDedicatedCapability(t *testing.T) {
	resp, err := unsupportedControlCapability(vmkit.BackendAppleVF, "pause")
	if err != nil || resp.Error != "" {
		t.Fatalf("Apple VF pause err=%v resp=%#v, want supported", err, resp)
	}
	resp, err = unsupportedControlCapability(vmkit.BackendAppleVF, "resume")
	if err != nil || resp.Error != "" {
		t.Fatalf("Apple VF resume err=%v resp=%#v, want supported", err, resp)
	}
	if resp, err := unsupportedControlCapability(vmkit.BackendLinuxKVM, "pause"); err != nil || resp.Error != "" {
		t.Fatalf("Linux pause capability err=%v resp=%#v, want supported", err, resp)
	}
}

func TestManifestAndStatusLifecycleAreLibraryOwned(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "agency-task",
		StateDir:       dir,
		Backend:        HostBackend(),
		Profile:        "small",
		RestartPolicy:  "never",
		MemoryMiB:      512,
		CPUCount:       2,
		SizeMiB:        1024,
		Network:        vmkit.NetworkConfig{Mode: "user"},
		ServiceCommand: "/opt/homebridge/start.sh --allow-root",
		Disks: []Disk{{
			Name:       "work",
			Path:       "/tmp/work.ext4",
			Mountpoint: "/work",
			Mode:       "rw",
		}},
		Outputs: []Output{{Name: "result", Path: "/work/result.txt"}},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "agency-task")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Name != "agency-task" || manifest.Artifacts.Egress[0].Name != "result" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.Service != "/opt/homebridge/start.sh --allow-root" {
		t.Fatalf("manifest Service = %q", manifest.Service)
	}

	req, err := Request(opts, "run", filepath.Join(dir, "workspaces", "agency-task", "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("status response = %#v", resp)
	}
	if resp.Artifacts == nil || len(resp.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", resp.Artifacts)
	}
	// Status surfaces the machine-readable egress capture report (provider +
	// coverage), computed from the recorded backend/network/egress mode.
	if resp.EgressCapture == nil || resp.EgressCapture.Provider == "" || resp.EgressCapture.Mode == "" {
		t.Fatalf("status response missing egress capture report: %#v", resp.EgressCapture)
	}
	if _, err := os.Stat(filepath.Join(dir, "agency-task", "runtime.json")); err != nil {
		t.Fatalf("runtime.json not written: %v", err)
	}
	artifacts, err := ArtifactsFor(dir, "agency-task")
	if err != nil {
		t.Fatalf("ArtifactsFor: %v", err)
	}
	if len(artifacts.Egress) != 1 || artifacts.Egress[0].Name != "result" {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "agency-task" || entries[0].State != string(vmkit.StateRunning) {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestListIgnoresTerminalRuntimeOnlyRecords(t *testing.T) {
	dir := t.TempDir()
	writeState := func(name string, state vmkit.VMState) {
		t.Helper()
		opts := Options{StateDir: dir, Name: name}
		req := vmkit.Request{
			Identity: &vmkit.Identity{RequestID: "req-" + name, RuntimeID: name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
			Config:   &vmkit.Config{StateDir: dir},
		}
		if err := WriteProcessState(opts, req, state, 1234, ""); err != nil {
			t.Fatalf("WriteProcessState(%s): %v", name, err)
		}
	}

	writeState("deleted", vmkit.StateStopped)
	writeState("live", vmkit.StateRunning)

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "live" {
		t.Fatalf("entries = %#v, want only live runtime-only workspace", entries)
	}

	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "saved"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeState("saved", vmkit.StateStopped)

	entries, err = List(dir)
	if err != nil {
		t.Fatalf("List after saved manifest: %v", err)
	}
	got := map[string]string{}
	for _, entry := range entries {
		got[entry.Name] = entry.State
	}
	if len(got) != 2 || got["live"] != string(vmkit.StateRunning) || got["saved"] != string(vmkit.StateStopped) {
		t.Fatalf("entries = %#v, want live runtime-only and saved terminal workspace", entries)
	}
}

func TestDefaultHostnameSanitizesWorkspaceName(t *testing.T) {
	tests := map[string]string{
		"homebridge":            "homebridge",
		"Home_Bridge.local":     "home-bridge-local",
		"---":                   "microagent",
		strings.Repeat("a", 70): strings.Repeat("a", 63),
	}
	for name, want := range tests {
		if got := DefaultHostname(name); got != want {
			t.Fatalf("DefaultHostname(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestValidateHostnameRejectsInvalidValues(t *testing.T) {
	for _, hostname := range []string{"bad_name", "-bad", "bad-", "", strings.Repeat("a", 64)} {
		if err := ValidateHostname(hostname); err == nil {
			t.Fatalf("ValidateHostname(%q) error = nil", hostname)
		}
	}
}

func TestBackendOwnsRuntimeState(t *testing.T) {
	if !backendOwnsRuntimeState(vmkit.BackendLinuxKVM) {
		t.Fatalf("backendOwnsRuntimeState(%q) = false, want true", vmkit.BackendLinuxKVM)
	}
	if backendOwnsRuntimeState(vmkit.BackendAppleVF) {
		t.Fatalf("backendOwnsRuntimeState(%q) = true, want false", vmkit.BackendAppleVF)
	}
}

func TestStatusNonLiveStatesUseFastReadinessAndRecordedRootfs(t *testing.T) {
	for _, state := range []vmkit.VMState{vmkit.StatePrepared, vmkit.StateHalted} {
		t.Run(string(state), func(t *testing.T) {
			dir := t.TempDir()
			kernelPath := filepath.Join(dir, "Image")
			if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
				t.Fatal(err)
			}
			missingRootfs := filepath.Join(dir, "workspaces", "agent", "rootfs.ext4")
			opts := Options{
				Name:          "agent",
				StateDir:      dir,
				Backend:       HostBackend(),
				KernelPath:    kernelPath,
				Profile:       "tiny",
				RestartPolicy: DefaultRestartPolicy,
				Verification: &vmkit.RuntimeVerification{
					OK: true,
					Kernel: &vmkit.VerifiedArtifact{
						Path:   kernelPath,
						SHA256: "recorded-kernel",
					},
					Rootfs: &vmkit.VerifiedArtifact{
						Path:   missingRootfs,
						SHA256: "recorded-rootfs",
					},
				},
			}
			if err := WriteManifest(opts); err != nil {
				t.Fatalf("WriteManifest: %v", err)
			}
			req, err := Request(opts, "inspect", missingRootfs, "req-1")
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			req.Config.KernelPath = kernelPath
			req.Config.RootfsPath = missingRootfs
			if err := WriteProcessState(opts, req, state, 0, ""); err != nil {
				t.Fatalf("WriteProcessState: %v", err)
			}

			start := time.Now()
			resp, err := Status(opts)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if elapsed := time.Since(start); elapsed >= time.Second {
				t.Fatalf("Status elapsed = %s, want < 1s", elapsed)
			}
			if resp.Verification == nil || resp.Verification.Rootfs == nil {
				t.Fatalf("verification = %#v", resp.Verification)
			}
			if resp.Verification.Rootfs.Error != "" {
				t.Fatalf("rootfs verification error = %q, want fast recorded metadata", resp.Verification.Rootfs.Error)
			}
			if resp.Verification.Rootfs.SHA256 != "recorded-rootfs" || resp.Verification.Rootfs.RecordedSHA256 != "recorded-rootfs" {
				t.Fatalf("rootfs verification = %#v, want recorded checksum", resp.Verification.Rootfs)
			}
			if resp.Readiness == nil {
				t.Fatal("readiness missing")
			}
			if resp.Readiness.ExecReady.Ready || !strings.Contains(resp.Readiness.ExecReady.Detail, "live readiness unavailable") {
				t.Fatalf("exec readiness = %#v, want fast unavailable detail", resp.Readiness.ExecReady)
			}
			if resp.Readiness.ShellReady.Ready || !strings.Contains(resp.Readiness.ShellReady.Detail, "live readiness unavailable") {
				t.Fatalf("shell readiness = %#v, want fast unavailable detail", resp.Readiness.ShellReady)
			}
			if resp.Readiness.ResultReady.Ready || !strings.Contains(resp.Readiness.ResultReady.Detail, "live readiness unavailable") {
				t.Fatalf("result readiness = %#v, want fast unavailable detail", resp.Readiness.ResultReady)
			}
		})
	}
}

func TestStatusMissingWorkspaceReturnsNotFound(t *testing.T) {
	_, err := Status(Options{Name: "no-such-workspace", StateDir: t.TempDir(), Backend: HostBackend()})
	var notFound WorkspaceNotFoundError
	if !errors.As(err, &notFound) || notFound.Name != "no-such-workspace" {
		t.Fatalf("Status err = %v, want WorkspaceNotFoundError", err)
	}
}

func TestStatusMalformedRuntimeStateIsNotNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "runtime.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Status(Options{Name: "agent", StateDir: dir, Backend: HostBackend()})
	if err == nil {
		t.Fatal("Status err = nil, want malformed-state error")
	}
	var notFound WorkspaceNotFoundError
	if errors.As(err, &notFound) {
		t.Fatalf("Status err = %v, want corrupt state surfaced, not WorkspaceNotFoundError", err)
	}
}

func TestStatusRunningWorkspaceStillChecksCurrentRootfs(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingRootfs := filepath.Join(dir, "workspaces", "agent", "rootfs.ext4")
	opts := Options{
		Name:          "agent",
		StateDir:      dir,
		Backend:       HostBackend(),
		KernelPath:    kernelPath,
		Profile:       "tiny",
		RestartPolicy: DefaultRestartPolicy,
		Verification: &vmkit.RuntimeVerification{
			OK:     true,
			Rootfs: &vmkit.VerifiedArtifact{Path: missingRootfs, SHA256: "recorded-rootfs"},
		},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	req, err := Request(opts, "inspect", missingRootfs, "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	req.Config.KernelPath = kernelPath
	req.Config.RootfsPath = missingRootfs
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 0, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Verification == nil || resp.Verification.Rootfs == nil || resp.Verification.Rootfs.Error == "" {
		t.Fatalf("rootfs verification = %#v, want current rootfs error for running workspace", resp.Verification)
	}
	// POSIX reports "no such file"; Windows reports "cannot find the file".
	if !strings.Contains(resp.Verification.Rootfs.Error, "no such file") && !strings.Contains(resp.Verification.Rootfs.Error, "cannot find the file") {
		t.Fatalf("rootfs error = %q", resp.Verification.Rootfs.Error)
	}
}

func TestReadinessFromRuntimeRequiresLiveShellTarget(t *testing.T) {
	for _, backend := range []string{vmkit.BackendLinuxKVM, vmkit.BackendAppleVF} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			runtimeDir := filepath.Join(dir, "agent")
			inputPath := filepath.Join(runtimeDir, "serial.in")
			serialPath := filepath.Join(runtimeDir, "serial.log")
			if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inputPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			state := RuntimeState{
				Event: EventFile{
					Identity:   vmkit.Identity{RuntimeID: "agent", Backend: backend},
					State:      vmkit.StateRunning,
					ObservedAt: time.Now().UTC().Format(time.RFC3339),
				},
				Config:          vmkit.Config{StateDir: dir, SerialInput: true, ShellPort: 24279},
				SerialInputPath: inputPath,
				SerialLogPath:   serialPath,
				StartedAt:       time.Now().UTC().Format(time.RFC3339),
			}
			readiness := readinessFromRuntime(state)
			if readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want not ready before shell target is reachable", readiness.ShellReady)
			}
			if !strings.Contains(readiness.ShellReady.Detail, "command probe failed") {
				t.Fatalf("shell readiness detail = %q, want command probe failure detail", readiness.ShellReady.Detail)
			}
			if err := os.WriteFile(serialPath, []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			readiness = readinessFromRuntime(state)
			if readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want not ready when only the guest helper log exists", readiness.ShellReady)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serveDone := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serveDone <- err
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				var command strings.Builder
				buf := make([]byte, 1024)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						command.Write(buf[:n])
						if strings.Contains(command.String(), "exit\r") {
							break
						}
					}
					if err != nil {
						serveDone <- err
						return
					}
				}
				text := command.String()
				tokenStart := strings.Index(text, "__ma_token=")
				if tokenStart == -1 {
					serveDone <- fmt.Errorf("command %q missing token assignment", text)
					return
				}
				tokenStart += len("__ma_token=")
				tokenEnd := strings.Index(text[tokenStart:], ";")
				if tokenEnd == -1 {
					serveDone <- fmt.Errorf("command %q missing token terminator", text)
					return
				}
				token := text[tokenStart : tokenStart+tokenEnd]
				_, err = fmt.Fprintf(conn, "\r\n__MICROAGENT_DONE_%s__0\r\n", token)
				serveDone <- err
			}()
			_, portText, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				t.Fatal(err)
			}
			state.Config.ShellPort = uint16(port)
			readiness = readinessFromRuntime(state)
			if !readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want ready when shell target completes a command probe", readiness.ShellReady)
			}
			if !strings.Contains(readiness.ShellReady.Detail, "command round-trip ready at") {
				t.Fatalf("shell readiness detail = %q", readiness.ShellReady.Detail)
			}
			if err := <-serveDone; err != nil {
				t.Fatalf("shell target probe server: %v", err)
			}
		})
	}
}

func TestBuildRootfsRequestAllowsMutableWorkspaceImages(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:         "research",
		StateDir:     "/tmp/microagent",
		ImageRef:     "docker.io/library/ubuntu:24.04",
		Architecture: "arm64",
		SizeMiB:      1024,
	}, "/tmp/microagent/workspaces/research/rootfs.ext4")

	if !req.AllowMutable {
		t.Fatal("workspace rootfs builds should allow mutable image tags")
	}
}

func TestBuildRootfsRequestSetsLocalImageLayout(t *testing.T) {
	opts := Options{
		Name:         "research",
		StateDir:     "/tmp/microagent",
		ImageRef:     "docker.io/library/ubuntu:24.04",
		Architecture: "arm64",
		SizeMiB:      1024,
	}
	req := buildRootfsRequest(opts, "/tmp/microagent/workspaces/research/rootfs.ext4")

	// Same value commit.LayoutPath(opts.StateDir) produces; asserted as a
	// literal rather than by importing pkg/commit, since pkg/commit imports
	// pkg/workspace (importing it back here would be a cycle).
	want := filepath.Join(opts.StateDir, "images", "oci")
	if req.LocalImageLayout != want {
		t.Fatalf("LocalImageLayout = %q, want %q", req.LocalImageLayout, want)
	}
}

func TestBuildRootfsRequestBakesBrokerGuestEnv(t *testing.T) {
	opts := Options{
		Name:         "research",
		StateDir:     "/tmp/microagent",
		ImageRef:     "docker.io/library/ubuntu:24.04",
		Architecture: "arm64",
		// Brokers are gated on the backend capability at request-build time,
		// so the fixture must name a backend that serves broker endpoints.
		Backend: vmkit.BackendLinuxKVM,
		Env:     map[string]string{"FOO": "bar"},
		Broker: &vmkit.BrokerConfig{
			Upstream:   "https://api.example.com",
			Secret:     vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"},
			BaseURLEnv: map[string]string{"EXAMPLE_BASE_URL": ""},
		},
	}
	req, err := rootfsRequest(opts, "/tmp/microagent/workspaces/research/rootfs.ext4")
	if err != nil {
		t.Fatalf("rootfsRequest: %v", err)
	}
	if req.Env["FOO"] != "bar" {
		t.Fatalf("operator env not preserved: %v", req.Env)
	}
	wantBridge := DefaultBrokerGuestListen + "=1032"
	if req.Env["MICROAGENT_VSOCK_TCP_LISTENERS"] != wantBridge {
		t.Fatalf("bridge env = %q, want %q", req.Env["MICROAGENT_VSOCK_TCP_LISTENERS"], wantBridge)
	}
	if req.Env["EXAMPLE_BASE_URL"] != "http://"+DefaultBrokerGuestListen {
		t.Fatalf("base URL env = %q", req.Env["EXAMPLE_BASE_URL"])
	}
	if opts.Env["MICROAGENT_VSOCK_TCP_LISTENERS"] != "" {
		t.Fatal("caller's Env map mutated")
	}

	// Invalid broker config fails the build request, not silently skipped.
	bad := opts
	bad.Broker = &vmkit.BrokerConfig{Upstream: "https://api.example.com", Secret: vmkit.SecretRef{Name: "api", Ref: "sk-literal"}}
	if _, err := rootfsRequest(bad, "/tmp/microagent/workspaces/research/rootfs.ext4"); err == nil {
		t.Fatal("literal broker secret must fail the rootfs request")
	}

	// No broker: env passes through untouched.
	plain := opts
	plain.Broker = nil
	req, err = rootfsRequest(plain, "/tmp/microagent/workspaces/research/rootfs.ext4")
	if err != nil {
		t.Fatalf("rootfsRequest: %v", err)
	}
	if len(req.Env) != 1 || req.Env["FOO"] != "bar" {
		t.Fatalf("no-broker env = %v, want only FOO", req.Env)
	}
}

func TestBuildRootfsRequestCarriesFinalConfigForSetupCreates(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "research",
		StateDir:        "/tmp/microagent",
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "arm64",
		SetupCommands:   []string{"echo setup"},
		Entrypoint:      "/app/entrypoint.sh",
		PrepareForStart: true,
	}, "/tmp/microagent/workspaces/research/rootfs.ext4")

	if !req.ResetFinalConfig {
		t.Fatal("setup creates must request a guest config reset from the builder")
	}
	if strings.Join(req.FinalCommand, " ") != "/bin/sh -lc /app/entrypoint.sh" || req.FinalMode != "" {
		t.Fatalf("final = %#v mode %q", req.FinalCommand, req.FinalMode)
	}
	if strings.Contains(strings.Join(req.Command, " "), "/etc/microagent/run.json") {
		t.Fatalf("setup script should not embed guest config reset: %#v", req.Command)
	}
}

func TestApplySpecFilePopulatesWorkspaceOptions(t *testing.T) {
	dir := t.TempDir()
	setupPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(setupPath, []byte("apt-get update\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "microagent.yaml")
	if err := os.WriteFile(specPath, []byte(`
name: demo
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
setupFiles:
  - setup.sh
env:
  FOO: bar
resources:
  memoryMiB: 1024
network:
  mode: user
files:
  - src: config.txt
    dst: /etc/demo/config.txt
outputs:
  - name: result
    path: /workspace/result.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	if err := ApplySpecFile(&opts, specPath, SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpecFile: %v", err)
	}
	if opts.Name != "demo" || opts.ImageRef != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("spec identity not applied: %+v", opts)
	}
	if opts.Profile != "medium" || opts.MemoryMiB != 1024 || opts.RestartPolicy != "on-failure" {
		t.Fatalf("spec resources not applied: %+v", opts)
	}
	if len(opts.SetupCommands) != 1 || opts.SetupCommands[0] != "apt-get update" {
		t.Fatalf("setup commands = %#v", opts.SetupCommands)
	}
	if opts.Env["FOO"] != "bar" {
		t.Fatalf("env = %#v", opts.Env)
	}
	if len(opts.Files) != 1 || opts.Files[0].SourcePath != filePath || opts.Files[0].Path != "/etc/demo/config.txt" {
		t.Fatalf("files = %#v", opts.Files)
	}
	if len(opts.Outputs) != 1 || opts.Outputs[0].Name != "result" {
		t.Fatalf("outputs = %#v", opts.Outputs)
	}
}

func TestApplySpecParsesModelRef(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "microagent.yaml")
	if err := os.WriteFile(specPath, []byte(`
name: demo
image: docker.io/library/ubuntu:24.04
model: Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf
modelRunner:
  backend: vllm
  gpu: on
  backendModel: Qwen/Qwen2.5-0.5B-Instruct
  servedModel: local-chat
  args: ["--max-model-len", "2048"]
modelMediation:
  mode: policy
  policyFile: model-policy.json
  policyTimeout: 250ms
`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	if err := ApplySpecFile(&opts, specPath, SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpecFile: %v", err)
	}
	if opts.Model != "Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf" {
		t.Fatalf("spec model not applied: %q", opts.Model)
	}
	if opts.ModelRunner.Backend != "vllm" || opts.ModelRunner.BackendModel != "Qwen/Qwen2.5-0.5B-Instruct" || opts.ModelRunner.ServedModel != "local-chat" {
		t.Fatalf("spec model runner not applied: %+v", opts.ModelRunner)
	}
	wantPolicy := filepath.Join(filepath.Dir(specPath), "model-policy.json")
	if opts.ModelMediation.Mode != "policy" || opts.ModelMediation.PolicyFile != wantPolicy || opts.ModelMediation.PolicyTimeout != "250ms" {
		t.Fatalf("spec model mediation not applied: %+v", opts.ModelMediation)
	}
}

func TestReadSpecReportsUnknownField(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "microagent.yaml")
	if err := os.WriteFile(specPath, []byte("resources:\n  network: user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSpec(specPath)
	if err == nil || !strings.Contains(err.Error(), `unknown field "network" under resources`) {
		t.Fatalf("ReadSpec error = %v", err)
	}
}

func TestApplyUpdatesStoppedWorkspaceNetwork(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network: vmkit.NetworkConfig{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
		},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(t.Context(), Options{StateDir: dir, Backend: vmkit.BackendLinuxKVM}, Spec{
		Name: "homebridge",
		Network: NetworkSpec{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", Host: "0.0.0.0", HostPort: 8581, GuestPort: 8581}},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Workspace != "homebridge" || len(result.Applied) != 1 || result.Applied[0] != "network" {
		t.Fatalf("result = %#v", result)
	}
	manifest, err := ReadManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].Host; got != "0.0.0.0" {
		t.Fatalf("forward host = %q, want 0.0.0.0", got)
	}
}

func TestApplyRejectsLiveNonHostNetworkChange(t *testing.T) {
	dir := t.TempDir()
	originalNetwork := vmkit.NetworkConfig{
		Mode:         "user",
		PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
	}
	opts := Options{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network:       originalNetwork,
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	req.Config.Network = &originalNetwork
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(t.Context(), Options{StateDir: dir, Backend: vmkit.BackendLinuxKVM}, Spec{
		Name: "homebridge",
		Network: NetworkSpec{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", Host: "0.0.0.0", HostPort: 8581, GuestPort: 8582}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "host bind changes") {
		t.Fatalf("err = %v, want host-bind-only rejection", err)
	}
	manifest, err := ReadManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].GuestPort; got != uint16(8581) {
		t.Fatalf("guest port changed to %d", got)
	}
}

func TestWorkspaceRootfsPathUsesBackendFormat(t *testing.T) {
	tests := []struct {
		name       string
		backend    string
		wantSuffix string
		wantFormat string
	}{
		{
			name:       "linux-kvm",
			backend:    vmkit.BackendLinuxKVM,
			wantSuffix: filepath.Join("workspaces", "research", "rootfs.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "apple-vf",
			backend:    vmkit.BackendAppleVF,
			wantSuffix: filepath.Join("workspaces", "research", "rootfs.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath := WorkspaceRootfsPath("/tmp/microagent", "research", tt.backend)
			if !strings.HasSuffix(gotPath, tt.wantSuffix) {
				t.Fatalf("WorkspaceRootfsPath = %q, want suffix %q", gotPath, tt.wantSuffix)
			}
			req := buildRootfsRequest(Options{
				Name:         "research",
				StateDir:     "/tmp/microagent",
				Backend:      tt.backend,
				ImageRef:     "docker.io/library/ubuntu:24.04",
				Architecture: "arm64",
				SizeMiB:      1024,
			}, gotPath)
			if req.Format != tt.wantFormat {
				t.Fatalf("BuildRequest.Format = %q, want %q", req.Format, tt.wantFormat)
			}
		})
	}
}

func TestWorkspaceDiskPathUsesBackendFormat(t *testing.T) {
	tests := []struct {
		name       string
		backend    string
		wantSuffix string
		wantFormat string
	}{
		{
			name:       "linux-kvm",
			backend:    vmkit.BackendLinuxKVM,
			wantSuffix: filepath.Join("workspaces", "research", "disks", "work.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
		{
			name:       "apple-vf",
			backend:    vmkit.BackendAppleVF,
			wantSuffix: filepath.Join("workspaces", "research", "disks", "work.ext4"),
			wantFormat: rootfs.FormatExt4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath := WorkspaceDiskPath("/tmp/microagent", "research", tt.backend, "work")
			if !strings.HasSuffix(gotPath, tt.wantSuffix) {
				t.Fatalf("WorkspaceDiskPath = %q, want suffix %q", gotPath, tt.wantSuffix)
			}
			if got := WorkspaceDiskFormat(tt.backend); got != tt.wantFormat {
				t.Fatalf("WorkspaceDiskFormat = %q, want %q", got, tt.wantFormat)
			}
		})
	}
}

func TestBuildRootfsRequestCanUseImageCommandForPreparedWorkspace(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "homebridge/homebridge:latest",
		Architecture:    "arm64",
		SizeMiB:         4096,
		PrepareForStart: true,
		UseImageCommand: true,
	}, "/tmp/microagent/workspaces/homebridge/rootfs.ext4")

	if req.NoImageCommand {
		t.Fatal("NoImageCommand = true, want image Entrypoint/Cmd preserved")
	}
	if req.Mode != "service" {
		t.Fatalf("Mode = %q, want service", req.Mode)
	}
	if req.ShellPort != ShellPortForName("homebridge") {
		t.Fatalf("ShellPort = %d, want %d", req.ShellPort, ShellPortForName("homebridge"))
	}
	if len(req.Command) != 0 {
		t.Fatalf("Command = %#v, want OCI image command", req.Command)
	}
}

func TestBuildRootfsRequestCanUseServiceCommandForPreparedWorkspace(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "homebridge/homebridge:latest",
		Architecture:    "arm64",
		SizeMiB:         4096,
		PrepareForStart: true,
		ServiceCommand:  "/opt/homebridge/start.sh --allow-root",
	}, "/tmp/microagent/workspaces/homebridge/rootfs.ext4")

	if req.Mode != "managed-service" {
		t.Fatalf("Mode = %q, want managed-service", req.Mode)
	}
	if strings.Join(req.Command, " ") != "/bin/sh -lc /opt/homebridge/start.sh --allow-root" {
		t.Fatalf("Command = %#v", req.Command)
	}
	if req.ResultPort != 0 {
		t.Fatalf("ResultPort = %d, want 0", req.ResultPort)
	}
}

func TestBuildRootfsRequestRunsSetupBeforeManagedService(t *testing.T) {
	req := buildRootfsRequest(Options{
		Name:            "homebridge",
		StateDir:        "/tmp/microagent",
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "arm64",
		SizeMiB:         4096,
		ResultPort:      1024,
		PrepareForStart: true,
		SetupCommands:   []string{"echo setup"},
		ServiceCommand:  "/usr/local/bin/microagent-homebridge",
	}, "/tmp/microagent/workspaces/homebridge/rootfs.ext4")

	if req.Mode != "" {
		t.Fatalf("Mode = %q, want setup foreground mode", req.Mode)
	}
	if req.ResultPort != 1024 {
		t.Fatalf("ResultPort = %d, want 1024", req.ResultPort)
	}
	joined := strings.Join(req.Command, " ")
	if !strings.Contains(joined, "echo setup") {
		t.Fatalf("Command = %#v", req.Command)
	}
	if !req.ResetFinalConfig || req.FinalMode != "managed-service" {
		t.Fatalf("final reset = %v mode %q, want managed-service reset", req.ResetFinalConfig, req.FinalMode)
	}
	if !strings.Contains(strings.Join(req.FinalCommand, " "), "/usr/local/bin/microagent-homebridge") {
		t.Fatalf("FinalCommand = %#v", req.FinalCommand)
	}
}

func TestEnsureCanCreateRejectsRunningWorkspace(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "homebridge"}
	req, err := Request(opts, "start", filepath.Join(dir, "rootfs.ext4"), NewRequestID())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	err = EnsureCanCreate(opts)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("EnsureCanCreate err = %v", err)
	}
}

func TestDetachedSupervisorCommandUsesStartForPersistentBackends(t *testing.T) {
	if got := detachedSupervisorCommand(vmkit.BackendLinuxKVM); got != "start" {
		t.Fatalf("detachedSupervisorCommand(%q) = %q, want start", vmkit.BackendLinuxKVM, got)
	}
	if got := detachedSupervisorCommand(vmkit.BackendAppleVF); got != "run" {
		t.Fatalf("detachedSupervisorCommand(%q) = %q, want run", vmkit.BackendAppleVF, got)
	}
}

func TestAppleVFStartFailsBeforeDetachedRunWhenKernelMissing(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "missing-kernel",
		StateDir:       dir,
		Backend:        vmkit.BackendAppleVF,
		Architecture:   "arm64",
		KernelPath:     filepath.Join(dir, "missing-kernel"),
		SupervisorPath: filepath.Join(dir, "missing-supervisor"),
		Profile:        "small",
		RestartPolicy:  "never",
		MemoryMiB:      512,
		CPUCount:       2,
		SizeMiB:        128,
		Network:        vmkit.NetworkConfig{Mode: "isolated"},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	rootfsPath := WorkspaceRootfsPath(dir, opts.Name, opts.Backend)
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	req, err := Request(opts, "run", rootfsPath, "req-missing-kernel")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	_, err = startDetached(opts, req)
	if err == nil || !strings.Contains(err.Error(), "kernel is not readable") {
		t.Fatalf("Start err = %v, want missing kernel preflight", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, opts.Name, "runtime.json")); !os.IsNotExist(statErr) {
		t.Fatalf("runtime state exists after preflight failure: %v", statErr)
	}
}

func TestEnsureCanCreateRejectsUnavailableHostPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureCanCreate(Options{
		StateDir: t.TempDir(),
		Name:     "homebridge",
		Network: vmkit.NetworkConfig{
			Mode:         "nat",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: uint16(port), GuestPort: 8581}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "host port 127.0.0.1:"+portText+" is unavailable") {
		t.Fatalf("EnsureCanCreate err = %v", err)
	}
}

func TestStatusDoesNotTreatStartedRootfsMutationAsDivergence(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	initPath := filepath.Join(dir, "microagent-init")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("kernel-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initPath, []byte("init-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Name:          "research",
		StateDir:      dir,
		Backend:       HostBackend(),
		KernelPath:    kernelPath,
		GuestInitPath: initPath,
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}
	result := Result{
		Workspace:  "research",
		RootfsPath: rootfsPath,
		Image: rootfs.Provenance{
			ImageRef:    "docker.io/library/busybox:1.36",
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
		},
	}
	verification, err := BuildVerification(opts, result)
	if err != nil {
		t.Fatal(err)
	}
	opts.Verification = &verification
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	req, err := Request(opts, "run", rootfsPath, "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	req.Config.KernelPath = kernelPath
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verification == nil || !resp.Verification.OK {
		t.Fatalf("verification = %#v, want ok after started rootfs mutation", resp.Verification)
	}
	if resp.Verification.Rootfs == nil || resp.Verification.Rootfs.RecordedSHA256 == "" || resp.Verification.Rootfs.SHA256 == "" {
		t.Fatalf("rootfs verification details missing: %#v", resp.Verification)
	}
}

// TestApplyManifestNormalizesEgressModeForStart asserts the start path's
// manifest-load chokepoint carries the secure default into the request: a
// manifest with an unspecified egress mode yields a started workspace that is
// guarded (mediator provisioned + CA-cert vsock listener re-allocated), mirroring
// create. Start() does applyManifest(&opts) -> Request(opts); this exercises that
// composition without spinning up a VM. INV1 (start side).
func TestApplyManifestNormalizesEgressModeForStart(t *testing.T) {
	opts := Options{
		Name:       "agent-1",
		Backend:    vmkit.BackendLinuxKVM,
		KernelPath: "/k",
		StateDir:   t.TempDir(),
		MemoryMiB:  512,
		CPUCount:   2,
		Network:    vmkit.NetworkConfig{Mode: "user"},
	}
	// Manifest with an unspecified egress mode (broker is now the default).
	applyManifest(&opts, Manifest{Network: NetworkSpec{Mode: "user"}})
	if opts.EgressMode != vmkit.EgressModeBroker {
		t.Fatalf("applyManifest left EgressMode = %q, want %q", opts.EgressMode, vmkit.EgressModeBroker)
	}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !vmkit.EgressMediationOn(req.Config.EgressMode) {
		t.Fatalf("started workspace not mediated: EgressMode = %q", req.Config.EgressMode)
	}
	// The broker default mediates but forges no certificates, so it allocates
	// no CA-cert listener (unlike the retired guarded default).
	if req.Config.CACertPort != 0 {
		t.Fatalf("started broker-default workspace CACertPort = %d, want 0", req.Config.CACertPort)
	}
	if hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("started broker-default workspace must not allocate a CA-cert listener: %#v", req.Config.VsockListeners)
	}
}

// TestApplyManifestPreservesOffForStart asserts an explicit "off" manifest is not
// silently promoted to mediated on start. INV2 (start side).
func TestApplyManifestPreservesOffForStart(t *testing.T) {
	opts := Options{
		Name:       "agent-1",
		Backend:    vmkit.BackendLinuxKVM,
		KernelPath: "/k",
		StateDir:   t.TempDir(),
		MemoryMiB:  512,
		CPUCount:   2,
		Network:    vmkit.NetworkConfig{Mode: "user"},
	}
	applyManifest(&opts, Manifest{Network: NetworkSpec{Mode: "user"}, EgressMode: vmkit.EgressModeOff})
	if opts.EgressMode != vmkit.EgressModeOff {
		t.Fatalf("applyManifest changed off mode to %q", opts.EgressMode)
	}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if vmkit.EgressMediationOn(req.Config.EgressMode) {
		t.Fatalf("off workspace should not be mediated on start")
	}
	if req.Config.CACertPort != 0 || hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("off workspace allocated CA-cert listener on start: port=%d listeners=%#v", req.Config.CACertPort, req.Config.VsockListeners)
	}
}

// TestCopyForkEgressCABringsCAIntoForkDir proves that forking a mediated
// workspace copies the source's persisted egress CA cert+key into the fork's
// workspace dir (with the correct perms), so the fork's restore path can reuse
// the SAME CA the guest's baked trust store was built against rather than
// failing closed or re-minting.
func TestCopyForkEgressCABringsCAIntoForkDir(t *testing.T) {
	stateDir := t.TempDir()
	srcDir := filepath.Join(stateDir, "source")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	certBytes := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	keyBytes := []byte("-----BEGIN EC PRIVATE KEY-----\nfake\n-----END EC PRIVATE KEY-----\n")
	if err := os.WriteFile(filepath.Join(srcDir, "egress-ca.pem"), certBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "egress-ca-key.pem"), keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copyForkEgressCA(stateDir, "source", "fork"); err != nil {
		t.Fatalf("copyForkEgressCA: %v", err)
	}
	forkDir := filepath.Join(stateDir, "fork")
	gotCert, err := os.ReadFile(filepath.Join(forkDir, "egress-ca.pem"))
	if err != nil {
		t.Fatalf("fork CA cert not copied: %v", err)
	}
	if string(gotCert) != string(certBytes) {
		t.Error("fork CA cert bytes differ from source")
	}
	keyInfo, err := os.Stat(filepath.Join(forkDir, "egress-ca-key.pem"))
	if err != nil {
		t.Fatalf("fork CA key not copied: %v", err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("fork CA key perm = %o, want 0600", keyInfo.Mode().Perm())
	}
}

// TestCopyForkEgressCAFailsClosedWhenSourceMissing proves a mediated fork whose
// source CA is gone is refused rather than booting with no reusable CA.
func TestCopyForkEgressCAFailsClosedWhenSourceMissing(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "source"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyForkEgressCA(stateDir, "source", "fork"); err == nil {
		t.Fatal("expected error forking mediated workspace with missing source CA, got nil")
	}
}
