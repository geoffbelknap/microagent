package vmkit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidateSnapshotManifestArtifacts(t *testing.T) {
	valid := SnapshotManifest{
		RootfsArtifact: "disks/rootfs.ext4",
		MachineStateArtifacts: []SnapshotArtifact{
			{Kind: "state", Path: "state/vmstate"},
		},
	}
	if err := validateSnapshotManifestArtifacts(valid); err != nil {
		t.Fatalf("valid nested artifacts rejected: %v", err)
	}

	tests := []SnapshotManifest{
		{RootfsArtifact: "../victim"},
		{RootfsArtifact: "/tmp/victim"},
		{RootfsArtifact: `..\victim`},
		{RootfsArtifact: "nested/../victim"},
		{
			RootfsArtifact: SnapshotRootfsName,
			MachineStateArtifacts: []SnapshotArtifact{
				{Kind: "state", Path: "../../victim"},
			},
		},
	}
	for _, manifest := range tests {
		if err := validateSnapshotManifestArtifacts(manifest); err == nil {
			t.Errorf("validateSnapshotManifestArtifacts accepted %#v", manifest)
		}
	}
}

func TestReadSnapshotManifestRejectsEscapingArtifact(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"tag":"base","rootfsArtifact":"../victim"}`)
	if err := os.WriteFile(filepath.Join(dir, SnapshotManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshotManifest(dir); err == nil {
		t.Fatal("ReadSnapshotManifest accepted an escaping rootfs artifact")
	}
}

func TestWriteSnapshotManifestRejectsEscapingArtifactBeforeCreatingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snapshot")
	err := WriteSnapshotManifest(dir, SnapshotManifest{Tag: "base", RootfsArtifact: "../victim"})
	if err == nil {
		t.Fatal("WriteSnapshotManifest accepted an escaping rootfs artifact")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("snapshot directory exists after rejected manifest: %v", statErr)
	}
}

func writeFakeSnapshot(t *testing.T, stateDir, name, tag string, manifest SnapshotManifest, memBytes int) {
	t.Helper()
	dir := SnapshotDir(stateDir, name, tag)
	if err := WriteSnapshotManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SnapshotVMStateName), make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SnapshotMemoryName), make([]byte, memBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SnapshotRootfsName), make([]byte, 128), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotManifestRoundTrip(t *testing.T) {
	dir := SnapshotDir(t.TempDir(), "agent-1", "snap-1")
	manifest := SnapshotManifest{
		Tag:                "snap-1",
		ImageRef:           "docker.io/library/nats:latest",
		NetworkMode:        "nat",
		GuestIP:            "169.254.0.2",
		KernelSHA256:       "abc123",
		VCPUCount:          2,
		MemoryMiB:          512,
		CreatedAt:          "2026-06-01T00:00:00Z",
		VsockUDSPath:       "/state/agent-1/vsock.sock",
		ShellPort:          28365,
		ExecPort:           48365,
		NetworkIP:          "10.43.220.2/29",
		NetworkGateway:     "10.43.220.1",
		NetworkSubnet:      "10.43.220.0/29",
		NetworkIPv6:        "fd00:6d69:6372:dc::2/64",
		NetworkIPv6Gateway: "fd00:6d69:6372:dc::1",
		NetworkIPv6Subnet:  "fd00:6d69:6372:dc::/64",
		RootfsArtifact:     SnapshotRootfsName,
		MachineStateArtifacts: []SnapshotArtifact{
			{Kind: "firecracker-vmstate", Path: SnapshotVMStateName},
			{Kind: "firecracker-memory", Path: SnapshotMemoryName},
		},
		SecretsMaterialized: true,
		SecretsPurged:       true,
	}
	if err := WriteSnapshotManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshotManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, manifest) {
		t.Fatalf("round-trip = %#v, want %#v", got, manifest)
	}
}

// TestSnapshotManifestRoundTripsEgress proves the egress posture fields
// (mode/allow/passthrough and the CA cert DER fingerprint) survive the
// write→read cycle intact, so a restore/fork can re-arm the mediator with the
// recorded policy and verify the persisted CA against the recorded fingerprint.
func TestSnapshotManifestRoundTripsEgress(t *testing.T) {
	dir := SnapshotDir(t.TempDir(), "agent-1", "snap-egress")
	manifest := SnapshotManifest{
		Tag:               "snap-egress",
		NetworkMode:       "nat",
		KernelSHA256:      "abc123",
		VCPUCount:         2,
		MemoryMiB:         512,
		CreatedAt:         "2026-06-01T00:00:00Z",
		EgressMode:        EgressModeMITM,
		EgressAllow:       []string{"api.github.com", ".example.com"},
		EgressPassthrough: []string{"raw.example.com"},
		EgressCASHA256:    "deadbeefcafebabe0011223344556677889900aabbccddeeff00112233445566",
	}
	if err := WriteSnapshotManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshotManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, manifest) {
		t.Fatalf("round-trip = %#v, want %#v", got, manifest)
	}
}

func TestSnapshotArtifactDefaultsPreserveLegacyFirecrackerManifests(t *testing.T) {
	manifest := SnapshotManifest{Tag: "legacy"}
	if got := SnapshotRootfsArtifact(manifest); got != SnapshotRootfsName {
		t.Fatalf("SnapshotRootfsArtifact = %q, want %q", got, SnapshotRootfsName)
	}
	got := SnapshotMachineStateArtifacts(manifest)
	want := FirecrackerSnapshotArtifacts()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SnapshotMachineStateArtifacts = %#v, want %#v", got, want)
	}
}

func TestAppleVFSnapshotArtifacts(t *testing.T) {
	got := AppleVFSnapshotArtifacts()
	want := []SnapshotArtifact{
		{Kind: "apple-vf-machine-state", Path: SnapshotAppleVFMachineState},
		{Kind: "apple-vf-restore-config", Path: SnapshotAppleVFConfig},
		{Kind: "config-disk", Path: SnapshotConfigDiskName},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AppleVFSnapshotArtifacts = %#v, want %#v", got, want)
	}
}

func TestSnapshotArtifactHelpersUseManifestShape(t *testing.T) {
	manifest := SnapshotManifest{
		RootfsArtifact: "disks/rootfs.ext4",
		MachineStateArtifacts: []SnapshotArtifact{
			{Kind: "apple-vf-machine-state", Path: "machine-state.vz"},
		},
	}
	if got := SnapshotRootfsArtifact(manifest); got != "disks/rootfs.ext4" {
		t.Fatalf("SnapshotRootfsArtifact = %q", got)
	}
	got := SnapshotMachineStateArtifacts(manifest)
	if len(got) != 1 || got[0].Kind != "apple-vf-machine-state" || got[0].Path != "machine-state.vz" {
		t.Fatalf("SnapshotMachineStateArtifacts = %#v", got)
	}
	got[0].Path = "mutated"
	if manifest.MachineStateArtifacts[0].Path != "machine-state.vz" {
		t.Fatal("SnapshotMachineStateArtifacts returned alias of manifest slice")
	}
}

func TestMaterializedSecretsDeclared(t *testing.T) {
	if MaterializedSecretsDeclared(&Config{}) {
		t.Fatal("empty config should not need snapshot purge")
	}
	if !MaterializedSecretsDeclared(&Config{Secrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}}) {
		t.Fatal("materialized --secret should need snapshot purge")
	}
	if !MaterializedSecretsDeclared(&Config{SecretEnvFiles: []string{"/tmp/app.env"}}) {
		t.Fatal("materialized secrets env file should need snapshot purge")
	}
	if MaterializedSecretsDeclared(&Config{OnDemandSecrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}}) {
		t.Fatal("on-demand-only secrets should not need snapshot purge")
	}
}

func TestValidateSnapshotSecretCaptureFailsClosed(t *testing.T) {
	cfg := &Config{Secrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}}
	if err := ValidateSnapshotSecretCapture(cfg, false, false); err == nil {
		t.Fatal("expected secret-bearing snapshot without purge to fail closed")
	}
	if err := ValidateSnapshotSecretCapture(cfg, true, false); err != nil {
		t.Fatalf("purged secret-bearing snapshot should pass: %v", err)
	}
	if err := ValidateSnapshotSecretCapture(&Config{OnDemandSecrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}}, false, false); err != nil {
		t.Fatalf("on-demand-only snapshot should not require purge: %v", err)
	}
}

// TestValidateSnapshotSecretCaptureAllowsForensicRetention: a forensic capture
// deliberately keeps guest secrets — credential material is evidence, and it
// exists only in volatile memory. The purge gate is relaxed ONLY in that mode;
// the default capture path keeps failing closed. Safety comes from the restore
// side: such a capture records materialized-but-not-purged, which
// ValidateSnapshotSecretRestore already refuses, so evidence can never be
// rehydrated as a workspace.
func TestValidateSnapshotSecretCaptureAllowsForensicRetention(t *testing.T) {
	cfg := &Config{Secrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}}

	// Default mode is unchanged: no purge, no capture.
	if err := ValidateSnapshotSecretCapture(cfg, false, false); err == nil {
		t.Fatal("default capture of a secret-bearing workspace without purge must fail closed")
	}
	// Forensic mode: un-purged capture is permitted.
	if err := ValidateSnapshotSecretCapture(cfg, false, true); err != nil {
		t.Fatalf("forensic capture must permit retaining secrets: %v", err)
	}
	// Purged still passes in either mode.
	if err := ValidateSnapshotSecretCapture(cfg, true, false); err != nil {
		t.Fatalf("purged capture should pass: %v", err)
	}

	// The resulting manifest must be refused by the restore path.
	forensic := SnapshotManifest{Tag: "evidence", SecretsMaterialized: true, SecretsPurged: false}
	full := &Config{Secrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}, SecretsControlPort: 1028}
	if err := ValidateSnapshotSecretRestore(forensic, full); err == nil {
		t.Fatal("a forensic capture must never be restorable as a workspace")
	}
}

func TestValidateSnapshotSecretRestoreFailsClosed(t *testing.T) {
	manifest := SnapshotManifest{Tag: "base", SecretsMaterialized: true, SecretsPurged: true}
	if err := ValidateSnapshotSecretRestore(manifest, nil); err == nil {
		t.Fatal("expected missing restore config to fail closed")
	}
	if err := ValidateSnapshotSecretRestore(manifest, &Config{}); err == nil {
		t.Fatal("expected missing materialized refs to fail closed")
	}
	if err := ValidateSnapshotSecretRestore(manifest, &Config{Secrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}}); err == nil {
		t.Fatal("expected missing secrets control port to fail closed")
	}
	cfg := &Config{Secrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}, SecretsControlPort: 1028}
	if err := ValidateSnapshotSecretRestore(manifest, cfg); err != nil {
		t.Fatalf("purged secret-bearing snapshot with rehydrate config should pass: %v", err)
	}
	manifest.SecretsPurged = false
	if err := ValidateSnapshotSecretRestore(manifest, cfg); err == nil {
		t.Fatal("expected unpurged secret-bearing snapshot to fail closed")
	}
	if err := ValidateSnapshotSecretRestore(SnapshotManifest{Tag: "old"}, &Config{}); err != nil {
		t.Fatalf("snapshot without materialized secret marker should remain compatible: %v", err)
	}
}

func TestListSnapshotsReturnsTagsWithSize(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeSnapshot(t, stateDir, "agent-1", "snap-a", SnapshotManifest{Tag: "snap-a", MemoryMiB: 512, CreatedAt: "2026-06-01T00:00:00Z"}, 1024)
	writeFakeSnapshot(t, stateDir, "agent-1", "snap-b", SnapshotManifest{Tag: "snap-b", MemoryMiB: 512, CreatedAt: "2026-06-01T01:00:00Z"}, 2048)

	infos, err := ListSnapshots(stateDir, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("ListSnapshots returned %d entries, want 2", len(infos))
	}
	// Ordered by CreatedAt: snap-a first, snap-b second.
	if infos[0].Tag != "snap-a" || infos[1].Tag != "snap-b" {
		t.Fatalf("order = %s, %s; want snap-a, snap-b", infos[0].Tag, infos[1].Tag)
	}
	// vmstate(64) + memory(1024) + rootfs(128) = 1216, plus manifest.json bytes.
	if infos[0].SizeBytes < 1216 {
		t.Fatalf("snap-a size = %d, want >= 1216", infos[0].SizeBytes)
	}
	if infos[1].SizeBytes <= infos[0].SizeBytes {
		t.Fatalf("snap-b size %d should exceed snap-a size %d (larger memory file)", infos[1].SizeBytes, infos[0].SizeBytes)
	}
}

func TestListSnapshotsEmptyWhenNoneExist(t *testing.T) {
	infos, err := ListSnapshots(t.TempDir(), "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected no snapshots, got %#v", infos)
	}
}

func TestRemoveSnapshotDeletesTag(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeSnapshot(t, stateDir, "agent-1", "snap-a", SnapshotManifest{Tag: "snap-a"}, 256)
	writeFakeSnapshot(t, stateDir, "agent-1", "snap-b", SnapshotManifest{Tag: "snap-b"}, 256)

	if err := RemoveSnapshot(stateDir, "agent-1", "snap-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SnapshotDir(stateDir, "agent-1", "snap-a")); !os.IsNotExist(err) {
		t.Fatalf("snap-a stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(SnapshotDir(stateDir, "agent-1", "snap-b")); err != nil {
		t.Fatalf("snap-b should survive: %v", err)
	}
}

func TestRemoveSnapshotRejectsMissingTag(t *testing.T) {
	if err := RemoveSnapshot(t.TempDir(), "agent-1", "ghost"); err == nil {
		t.Fatal("expected error removing a missing snapshot tag")
	}
}

func TestRemoveSnapshotRejectsUnsafeTag(t *testing.T) {
	if err := RemoveSnapshot(t.TempDir(), "agent-1", "../escape"); err == nil {
		t.Fatal("expected error for an unsafe snapshot tag")
	}
}
