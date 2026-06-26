package vmkit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
		Tag:                 "snap-1",
		ImageRef:            "docker.io/library/nats:latest",
		NetworkMode:         "nat",
		GuestIP:             "169.254.0.2",
		KernelSHA256:        "abc123",
		VCPUCount:           2,
		MemoryMiB:           512,
		CreatedAt:           "2026-06-01T00:00:00Z",
		VsockUDSPath:        "/state/agent-1/vsock.sock",
		ShellPort:           28365,
		ExecPort:            48365,
		NetworkIP:           "10.43.220.2/29",
		NetworkGateway:      "10.43.220.1",
		NetworkSubnet:       "10.43.220.0/29",
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
		EgressMode:        EgressModeStrict,
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
	if err := ValidateSnapshotSecretCapture(cfg, false); err == nil {
		t.Fatal("expected secret-bearing snapshot without purge to fail closed")
	}
	if err := ValidateSnapshotSecretCapture(cfg, true); err != nil {
		t.Fatalf("purged secret-bearing snapshot should pass: %v", err)
	}
	if err := ValidateSnapshotSecretCapture(&Config{OnDemandSecrets: []SecretRef{{Name: "API", Ref: "env:TOKEN"}}}, false); err != nil {
		t.Fatalf("on-demand-only snapshot should not require purge: %v", err)
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
