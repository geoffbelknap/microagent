//go:build linux

package firecracker

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestSnapshotForkBindDetectsCrossWorkspace(t *testing.T) {
	opts := Options{Name: "fork1", StateDir: "/state"}
	m := vmkit.SnapshotManifest{VsockUDSPath: "/state/forksrc/vsock.sock"}
	src, dst, need := snapshotForkBind(opts, m)
	if !need {
		t.Fatal("a snapshot baked at another workspace's vsock path should need a bind")
	}
	if src != "/state/forksrc" || dst != "/state/fork1" {
		t.Fatalf("bind = %q -> %q, want /state/forksrc -> /state/fork1", src, dst)
	}
}

func TestSnapshotForkBindSkipsResumeInPlace(t *testing.T) {
	opts := Options{Name: "ws", StateDir: "/state"}
	m := vmkit.SnapshotManifest{VsockUDSPath: "/state/ws/vsock.sock"}
	if _, _, need := snapshotForkBind(opts, m); need {
		t.Fatal("resume-in-place (same workspace) must not need a bind")
	}
}

func TestSnapshotForkBindSkipsWhenNoVsock(t *testing.T) {
	opts := Options{Name: "fork1", StateDir: "/state"}
	if _, _, need := snapshotForkBind(opts, vmkit.SnapshotManifest{}); need {
		t.Fatal("a snapshot with no vsock path needs no bind")
	}
}

func TestSnapshotForkBindSkipsJailRunVsock(t *testing.T) {
	opts := Options{Name: "fork1", StateDir: "/state"}
	if _, _, need := snapshotForkBind(opts, vmkit.SnapshotManifest{VsockUDSPath: "/run/vsock.sock"}); need {
		t.Fatal("a confined snapshot with a /run vsock path must load through the confined /run bind, not fork-mount host paths")
	}
}

func TestSnapshotAPIPathsTranslateOnlyWhenConfined(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: "/state"}
	vmstate := "/state/agent-1/snapshots/snap-1/vmstate"
	memory := "/state/agent-1/snapshots/snap-1/memory"

	gotVMState, gotMemory, err := snapshotAPIPaths(opts, false, vmstate, memory)
	if err != nil {
		t.Fatalf("snapshotAPIPaths unconfined: %v", err)
	}
	if gotVMState != vmstate || gotMemory != memory {
		t.Fatalf("unconfined paths = %q %q, want host paths", gotVMState, gotMemory)
	}

	gotVMState, gotMemory, err = snapshotAPIPaths(opts, true, vmstate, memory)
	if err != nil {
		t.Fatalf("snapshotAPIPaths confined: %v", err)
	}
	if gotVMState != "/run/snapshots/snap-1/vmstate" || gotMemory != "/run/snapshots/snap-1/memory" {
		t.Fatalf("confined paths = %q %q, want /run snapshot paths", gotVMState, gotMemory)
	}

	if _, err := snapshotAPIPath(opts, true, "/state/other/snapshots/snap-1/vmstate"); err == nil {
		t.Fatal("confined snapshot path outside workspace should be rejected")
	}
}

func TestForkMountExecArgsMapRoot(t *testing.T) {
	withRoot := forkMountExecArgs(true, "/sup", "/state/src", "/state/fork", "/fc", []string{"--api-sock", "/state/fork/api.sock"})
	if withRoot[0] != "--map-root-user" || withRoot[1] != "--mount" {
		t.Fatalf("host-side fork args = %v, want --map-root-user --mount first", withRoot)
	}
	if !containsSeq(withRoot, []string{"--", "/fc", "--api-sock", "/state/fork/api.sock"}) {
		t.Fatalf("firecracker argv missing after --: %v", withRoot)
	}

	// A user-networked fork is already root inside pasta's userns: no
	// --map-root-user, just a nested mount namespace.
	inNS := forkMountExecArgs(false, "/sup", "/state/src", "/state/fork", "/fc", []string{"--api-sock", "/state/fork/api.sock"})
	if inNS[0] != "--mount" {
		t.Fatalf("user-networked fork args = %v, want --mount first (no --map-root-user)", inNS)
	}
	for _, a := range inNS {
		if a == "--map-root-user" {
			t.Fatalf("user-networked fork must not remap root: %v", inNS)
		}
	}
	if !containsSeq(inNS, []string{"--bind-src", "/state/src", "--bind-dst", "/state/fork"}) {
		t.Fatalf("bind spec missing: %v", inNS)
	}
}

func containsSeq(haystack, needle []string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestPrepareSnapshotRestoreRollsBackRootfs(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	kernel := filepath.Join(dir, "kernel")
	if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(rootfs, []byte("LIVE-disk-with-marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelSHA, err := fileSHA256(kernel)
	if err != nil {
		t.Fatal(err)
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "base")
	if err := vmkit.WriteSnapshotManifest(snapDir, vmkit.SnapshotManifest{Tag: "base", NetworkMode: "isolated", KernelSHA256: kernelSHA}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, vmkit.SnapshotRootfsName), []byte("SNAPSHOT-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "r", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{KernelPath: kernel, RootfsPath: rootfs, StateDir: dir, Network: &vmkit.NetworkConfig{Mode: "isolated"}},
		Tag:      "base",
	}
	if err := prepareSnapshotRestore(opts, req); err != nil {
		t.Fatalf("prepareSnapshotRestore: %v", err)
	}
	data, err := os.ReadFile(rootfs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "SNAPSHOT-disk" {
		t.Fatalf("rootfs = %q, want SNAPSHOT-disk (rolled back)", data)
	}
}

func TestPrepareSnapshotRestoreRequiresSecretRehydrateConfig(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	kernel := filepath.Join(dir, "kernel")
	if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(rootfs, []byte("LIVE-disk-with-marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelSHA, err := fileSHA256(kernel)
	if err != nil {
		t.Fatal(err)
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "base")
	if err := vmkit.WriteSnapshotManifest(snapDir, vmkit.SnapshotManifest{
		Tag:                 "base",
		KernelSHA256:        kernelSHA,
		SecretsMaterialized: true,
		SecretsPurged:       true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, vmkit.SnapshotRootfsName), []byte("SNAPSHOT-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "r", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{KernelPath: kernel, RootfsPath: rootfs, StateDir: dir},
		Tag:      "base",
	}
	err = prepareSnapshotRestore(opts, req)
	if err == nil || !strings.Contains(err.Error(), "requires materialized secret references") {
		t.Fatalf("err = %v, want missing secret refs rejection", err)
	}
	data, readErr := os.ReadFile(rootfs)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "LIVE-disk-with-marker" {
		t.Fatalf("rootfs = %q, want live disk left untouched", data)
	}

	req.Config.Secrets = []vmkit.SecretRef{{Name: "API", Ref: "env:TOKEN"}}
	req.Config.SecretsControlPort = 1028
	if err := prepareSnapshotRestore(opts, req); err != nil {
		t.Fatalf("prepareSnapshotRestore with rehydrate config: %v", err)
	}
}

func TestPrepareSnapshotRestoreRejectsKernelSkew(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	kernel := filepath.Join(dir, "kernel")
	if err := os.WriteFile(kernel, []byte("the-real-kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "base")
	if err := vmkit.WriteSnapshotManifest(snapDir, vmkit.SnapshotManifest{Tag: "base", KernelSHA256: "deadbeef-different"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, vmkit.SnapshotRootfsName), []byte("snap"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "r", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{KernelPath: kernel, RootfsPath: filepath.Join(dir, "rootfs.ext4"), StateDir: dir},
		Tag:      "base",
	}
	err := prepareSnapshotRestore(opts, req)
	if err == nil || !strings.Contains(err.Error(), "kernel") {
		t.Fatalf("err = %v, want kernel skew rejection", err)
	}
}

func snapshotSourceRequest(t *testing.T, dir string) vmkit.Request {
	t.Helper()
	kernel := filepath.Join(dir, "kernel")
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfs, []byte("rootfs-bytes-coherent"), 0o644); err != nil {
		t.Fatal(err)
	}
	return vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: kernel,
			RootfsPath: rootfs,
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
			Network:    &vmkit.NetworkConfig{Mode: "user", IP: "10.43.0.2/29"},
		},
	}
}

func TestSnapshotCreateAutoPausesCreatesResumes(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	vmProcess := startSleepProcess(t)
	forwarder := startSleepProcess(t)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-1",
	})
	if err != nil {
		t.Fatalf("snapshot: resp=%+v err=%v", resp, err)
	}
	// Auto-pause then resume around the snapshot.
	if len(fake.states) != 2 || fake.states[0] != "Paused" || fake.states[1] != "Resumed" {
		t.Fatalf("controller states = %#v, want [Paused Resumed]", fake.states)
	}
	if len(fake.snapshots) != 1 {
		t.Fatalf("createSnapshot calls = %d, want 1", len(fake.snapshots))
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "snap-1")
	// Capture writes to a staging dir under the workspace; the snapshot is then
	// published atomically to SnapshotDir (verified via the rootfs copy + manifest
	// below, which read from snapDir).
	stagingRoot := filepath.Join(dir, "agent-1", ".snapshot-staging")
	if !strings.HasPrefix(fake.snapshots[0][0], stagingRoot+string(filepath.Separator)) || filepath.Base(fake.snapshots[0][0]) != vmkit.SnapshotVMStateName {
		t.Fatalf("snapshot vmstate path = %q, want %s under staging %q", fake.snapshots[0][0], vmkit.SnapshotVMStateName, stagingRoot)
	}
	if !strings.HasPrefix(fake.snapshots[0][1], stagingRoot+string(filepath.Separator)) || filepath.Base(fake.snapshots[0][1]) != vmkit.SnapshotMemoryName {
		t.Fatalf("snapshot memory path = %q, want %s under staging %q", fake.snapshots[0][1], vmkit.SnapshotMemoryName, stagingRoot)
	}
	// Coherent rootfs copy taken while paused.
	rootfsCopy := filepath.Join(snapDir, vmkit.SnapshotRootfsName)
	if data, err := os.ReadFile(rootfsCopy); err != nil || string(data) != "rootfs-bytes-coherent" {
		t.Fatalf("rootfs copy = %q err=%v", data, err)
	}
	manifest, err := vmkit.ReadSnapshotManifest(snapDir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tag != "snap-1" || manifest.NetworkMode != "user" || manifest.VCPUCount != 2 || manifest.MemoryMiB != 512 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.KernelSHA256 == "" {
		t.Fatal("manifest kernel sha256 is empty")
	}
	if manifest.CreatedAt == "" {
		t.Fatal("manifest createdAt is empty")
	}
	if manifest.RootfsArtifact != vmkit.SnapshotRootfsName {
		t.Fatalf("RootfsArtifact = %q, want %q", manifest.RootfsArtifact, vmkit.SnapshotRootfsName)
	}
	if got, want := manifest.MachineStateArtifacts, vmkit.FirecrackerSnapshotArtifacts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MachineStateArtifacts = %#v, want %#v", got, want)
	}
	if manifest.SecretsMaterialized || manifest.SecretsPurged {
		t.Fatalf("manifest secret fields = materialized:%t purged:%t, want both false", manifest.SecretsMaterialized, manifest.SecretsPurged)
	}
	// Workspace returns to running, aux processes preserved.
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateRunning || state.PID != vmProcess.Process.Pid || state.PortForwardPID != forwarder.Process.Pid {
		t.Fatalf("post-snapshot state = %#v", state)
	}
}

// TestSnapshotRefusesQuarantined: quarantine STOPS the runtime, so there is no
// live VM to capture memory from. Snapshot must fail closed and say so — with
// the capture-before-contain ordering named in the error, since that is the
// ordering incident response wants anyway (acquire volatile evidence first,
// then sever).
func TestSnapshotRefusesQuarantined(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	// Quarantine records no runtime PID: the VM process is gone.
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateQuarantined, 0, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := (Supervisor{}).Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-q",
	})
	if err == nil {
		t.Fatal("snapshot of a quarantined workspace must fail: containment stopped the runtime")
	}
	if resp.OK {
		t.Fatal("response OK = true, want false")
	}
	if !strings.Contains(err.Error(), "capture before quarantining") {
		t.Fatalf("err = %q, want it to name the capture-before-contain ordering", err.Error())
	}
	if len(fake.snapshots) != 0 {
		t.Fatalf("createSnapshot calls = %d, want 0 (nothing captured)", len(fake.snapshots))
	}
	// The refusal must not disturb the contained workspace.
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateQuarantined {
		t.Fatalf("post-refusal state = %s, want quarantined untouched", state.Event.State)
	}
}

// TestSnapshotResumesWithFreshContextWhenInterrupted proves the resume-on-failure
// path uses a context detached from the (cancelled) request context. It simulates
// the request being cancelled mid-capture — as happens when a `snapshot` is
// Ctrl-C'd or times out after the VM has been auto-paused — and asserts the
// supervisor still issues "Resumed" AND does so with a live context. If the resume
// reused the cancelled request ctx, the resume PATCH would fail and the guest would
// be left frozen; the detached resumeCtx is what un-freezes it.
func TestSnapshotResumesWithFreshContextWhenInterrupted(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeVMController{
		// Cancel the request the moment capture begins (after the auto-pause), then
		// fail the capture — the interruption the resume path must survive.
		onCreateSnapshot: cancel,
		snapErr:          errors.New("capture interrupted"),
	}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(ctx, vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-interrupted",
	})
	if err == nil {
		t.Fatal("expected snapshot to fail after interrupted capture")
	}

	// Paused (before cancel) then Resumed (after cancel) — the guest is un-frozen.
	if len(fake.states) != 2 || fake.states[0] != "Paused" || fake.states[1] != "Resumed" {
		t.Fatalf("controller states = %#v, want [Paused Resumed]", fake.states)
	}
	// The load-bearing assertion: the resume ran with a live context even though the
	// request ctx was cancelled. A cancelled resume ctx here would mean a frozen VM.
	if fake.stateCtxErr[1] != nil {
		t.Fatalf("resume ran with a cancelled context (%v); it must use a fresh context detached from the request", fake.stateCtxErr[1])
	}
	if ctx.Err() == nil {
		t.Fatal("test bug: request ctx should have been cancelled during capture")
	}
}

func TestSnapshotCreateUsesJailVisibleAPIPathsWhenConfined(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	req.Config.ExecPort = 25279
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)
	withFakeFirecrackerProcessConfined(t, true)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-confined",
	})
	if err != nil {
		t.Fatalf("snapshot: resp=%+v err=%v", resp, err)
	}
	if len(fake.snapshots) != 1 {
		t.Fatalf("createSnapshot calls = %d, want 1", len(fake.snapshots))
	}
	// Confined capture uses jail-visible (/run) paths under the staging dir; the
	// snapshot is then published to the host SnapshotDir (checked below).
	if !strings.HasPrefix(fake.snapshots[0][0], "/run/.snapshot-staging/") || filepath.Base(fake.snapshots[0][0]) != "vmstate" {
		t.Fatalf("snapshot API path = %q, want a jail-visible staging vmstate path", fake.snapshots[0][0])
	}
	if !strings.HasPrefix(fake.snapshots[0][1], "/run/.snapshot-staging/") || filepath.Base(fake.snapshots[0][1]) != "memory" {
		t.Fatalf("memory API path = %q, want a jail-visible staging memory path", fake.snapshots[0][1])
	}

	snapDir := vmkit.SnapshotDir(dir, "agent-1", "snap-confined")
	if _, err := os.Stat(filepath.Join(snapDir, vmkit.SnapshotRootfsName)); err != nil {
		t.Fatalf("host rootfs snapshot missing after publish: %v", err)
	}
	manifest, err := vmkit.ReadSnapshotManifest(snapDir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.VsockUDSPath != "/run/vsock.sock" {
		t.Fatalf("manifest vsock path = %q, want jail-visible /run/vsock.sock", manifest.VsockUDSPath)
	}
}

func TestRestoreFromSnapshotTranslatesLoadPathsWhenConfined(t *testing.T) {
	for _, tc := range []struct {
		name     string
		confined bool
		wantVM   string
		wantMem  string
	}{
		{name: "unconfined", confined: false},
		{name: "confined", confined: true, wantVM: "/run/snapshots/snap-1/vmstate", wantMem: "/run/snapshots/snap-1/memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "ma-fc-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			opts := Options{Name: "agent-1", StateDir: dir}
			snapDir := vmkit.SnapshotDir(dir, "agent-1", "snap-1")
			if err := vmkit.WriteSnapshotManifest(snapDir, vmkit.SnapshotManifest{Tag: "snap-1"}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(apiSocketPath(opts)), 0o700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", apiSocketPath(opts))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })

			fake := &fakeVMController{}
			withFakeVMController(t, fake)
			withFakeFirecrackerProcessConfined(t, tc.confined)
			if err := restoreFromSnapshot(context.Background(), opts, "snap-1", 1234, nil); err != nil {
				t.Fatalf("restoreFromSnapshot: %v", err)
			}
			if len(fake.loads) != 1 {
				t.Fatalf("loadSnapshot calls = %d, want 1", len(fake.loads))
			}
			wantVM := tc.wantVM
			if wantVM == "" {
				wantVM = filepath.Join(snapDir, vmkit.SnapshotVMStateName)
			}
			wantMem := tc.wantMem
			if wantMem == "" {
				wantMem = filepath.Join(snapDir, vmkit.SnapshotMemoryName)
			}
			if fake.loads[0][0] != wantVM || fake.loads[0][1] != wantMem {
				t.Fatalf("load paths = %q %q, want %q %q", fake.loads[0][0], fake.loads[0][1], wantVM, wantMem)
			}
			if !fake.loadResume {
				t.Fatal("loadSnapshot resume = false, want true")
			}
		})
	}
}

func TestSnapshotCreateRejectsMaterializedSecretsWithoutControlPort(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	req.Config.Secrets = []vmkit.SecretRef{{Name: "API", Ref: "env:TOKEN"}}
	req.Config.SecretsControlPort = 0
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-1",
	})
	if err == nil {
		t.Fatal("expected snapshot to fail closed without a secrets control port")
	}
	if resp.OK {
		t.Fatalf("response OK = true, want false")
	}
	for _, want := range []string{"cannot purge secrets for snapshot", "materialized secrets", "no secrets control port"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	}
	if len(fake.snapshots) != 0 {
		t.Fatalf("createSnapshot calls = %d, want 0", len(fake.snapshots))
	}
	if _, statErr := os.Stat(vmkit.SnapshotDir(dir, "agent-1", "snap-1")); !os.IsNotExist(statErr) {
		t.Fatalf("snapshot dir stat err = %v, want not exist", statErr)
	}
}

// TestSnapshotForensicRetainsSecretsAndIsNotRestorable: a forensic capture of a
// secret-bearing workspace succeeds WITHOUT purging (credential material is the
// evidence), records the truth in the manifest, and is refused by the restore
// path — so evidence can never be rehydrated as a workspace. The same workspace
// without the flag still fails closed.
func TestSnapshotForensicRetainsSecretsAndIsNotRestorable(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	req.Config.Secrets = []vmkit.SecretRef{{Name: "API", Ref: "env:TOKEN"}}
	req.Config.SecretsControlPort = 1028
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	base := vmkit.Request{Identity: req.Identity, Config: &vmkit.Config{StateDir: dir}}

	// Default mode: no purge channel reachable here, so it must fail closed and
	// capture nothing.
	plain := base
	plain.Command, plain.Tag = "snapshot", "plain"
	if _, err := (Supervisor{}).Do(context.Background(), plain); err == nil {
		t.Fatal("default capture of a secret-bearing workspace must fail closed")
	}
	if len(fake.snapshots) != 0 {
		t.Fatalf("default mode captured %d snapshots, want 0", len(fake.snapshots))
	}

	// Forensic mode: capture proceeds, secrets retained.
	forensic := base
	forensic.Command, forensic.Tag = "snapshot", "evidence"
	forensic.RetainSecrets = true
	resp, err := Supervisor{}.Do(context.Background(), forensic)
	if err != nil {
		t.Fatalf("forensic capture: resp=%+v err=%v", resp, err)
	}
	if len(fake.snapshots) != 1 {
		t.Fatalf("forensic createSnapshot calls = %d, want 1", len(fake.snapshots))
	}
	manifest, err := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(dir, "agent-1", "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.SecretsMaterialized || manifest.SecretsPurged {
		t.Fatalf("manifest = materialized:%t purged:%t, want materialized and NOT purged",
			manifest.SecretsMaterialized, manifest.SecretsPurged)
	}
	// The capture must be un-restorable, even with full rehydrate config.
	full := &vmkit.Config{Secrets: req.Config.Secrets, SecretsControlPort: 1028}
	if err := vmkit.ValidateSnapshotSecretRestore(manifest, full); err == nil {
		t.Fatal("a forensic capture must never be restorable as a workspace")
	}
	// The rootfs is part of the capture, so it is a complete memory+disk image.
	if _, err := os.Stat(filepath.Join(vmkit.SnapshotDir(dir, "agent-1", "evidence"), vmkit.SnapshotRootfsName)); err != nil {
		t.Fatalf("forensic capture must include the rootfs: %v", err)
	}
}

func TestSnapshotCreateKeepsRuntimeConfigPorts(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	req.Config.ShellPort = 24279
	req.Config.ExecPort = 25279
	req.Config.GuestShellPort = 22001
	req.Config.GuestExecPort = 42001
	req.Config.SecretsControlPort = 1028
	req.Config.ModelGuestPort = 11434
	req.Config.ModelVsockPort = 62100
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	withFakeVMController(t, &fakeVMController{})

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-1",
	})
	if err != nil {
		t.Fatalf("snapshot: resp=%+v err=%v", resp, err)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Config.ShellPort != 24279 || state.Config.ExecPort != 25279 {
		t.Fatalf("snapshot dropped shell/exec ports from runtime config: %#v", state.Config)
	}
	if state.Config.GuestShellPort != 22001 || state.Config.GuestExecPort != 42001 {
		t.Fatalf("snapshot dropped guest shell/exec ports: %#v", state.Config)
	}
	if state.Config.SecretsControlPort != 1028 {
		t.Fatalf("snapshot dropped secrets control port: %#v", state.Config)
	}
	if state.Config.ModelGuestPort != 11434 || state.Config.ModelVsockPort != 62100 {
		t.Fatalf("snapshot dropped model pairing ports: %#v", state.Config)
	}
}

func TestPauseResumeKeepsRuntimeConfigPorts(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	req.Config.ShellPort = 24279
	req.Config.ExecPort = 25279
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	withFakeVMController(t, &fakeVMController{})

	for _, command := range []string{"pause", "resume"} {
		resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
			Command:  command,
			Identity: req.Identity,
			Config:   &vmkit.Config{StateDir: dir},
		})
		if err != nil {
			t.Fatalf("%s: resp=%+v err=%v", command, resp, err)
		}
		state, err := readRuntimeState(opts)
		if err != nil {
			t.Fatal(err)
		}
		if state.Config.ShellPort != 24279 || state.Config.ExecPort != 25279 {
			t.Fatalf("%s dropped shell/exec ports from runtime config: %#v", command, state.Config)
		}
	}
}

func TestSnapshotCreateInPlaceWhenAlreadyPaused(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StatePaused, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-paused",
	})
	if err != nil {
		t.Fatalf("snapshot: resp=%+v err=%v", resp, err)
	}
	// Already paused: no pause/resume transitions, snapshot in place.
	if len(fake.states) != 0 {
		t.Fatalf("controller states = %#v, want none (already paused)", fake.states)
	}
	if len(fake.snapshots) != 1 {
		t.Fatalf("createSnapshot calls = %d, want 1", len(fake.snapshots))
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StatePaused {
		t.Fatalf("workspace should stay paused, got %s", state.Event.State)
	}
}

func TestSnapshotRejectsStoppedWorkspace(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	if err := writeProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-x",
	})
	if err == nil {
		t.Fatal("expected snapshot to reject a stopped workspace")
	}
	if len(fake.snapshots) != 0 {
		t.Fatalf("createSnapshot should not be called, got %#v", fake.snapshots)
	}
}
