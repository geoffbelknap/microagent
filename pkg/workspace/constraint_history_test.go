package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestConstraintHistoryReconstructsManifestAndConfigRevisions(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		StateDir: dir, Name: "agent", Purpose: "review records", CorrelationID: "task-42",
		RestartPolicy: "no", MemoryMiB: 512, CPUCount: 1, SizeMiB: 64,
		Env: map[string]string{"MODE": "review"},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteConfigDisk(opts); err != nil {
		t.Fatal(err)
	}

	revisions, err := ReadConstraintHistory(dir, opts.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 {
		t.Fatalf("revisions = %d, want manifest and config-disk writes", len(revisions))
	}
	first, latest := revisions[0], revisions[1]
	if first.Trigger != "manifest_write" || latest.Trigger != "config_disk_write" {
		t.Fatalf("triggers = %q, %q", first.Trigger, latest.Trigger)
	}
	if latest.Manifest == nil || latest.Manifest.Env["MODE"] != "review" {
		t.Fatalf("latest manifest snapshot = %#v", latest.Manifest)
	}
	if latest.ConfigDiskSHA256 == "" || first.ConfigDiskSHA256 != "" {
		t.Fatalf("config hashes = first %q latest %q", first.ConfigDiskSHA256, latest.ConfigDiskSHA256)
	}
	if latest.RuntimeID != opts.Name || latest.Purpose != opts.Purpose || latest.CorrelationID != opts.CorrelationID {
		t.Fatalf("revision attribution = %#v", latest.ConstraintRevisionRef)
	}
	if latest.EventID == "" || latest.RequestID == "" || latest.ObservedAt.IsZero() {
		t.Fatalf("revision identity incomplete: %#v", latest.ConstraintRevisionRef)
	}
	encoded, err := json.Marshal(latest.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ManifestSHA256 != sha256Hex(encoded) {
		t.Fatalf("manifest hash = %q, want snapshot hash %q", latest.ManifestSHA256, sha256Hex(encoded))
	}
}

func TestConstraintHistoryRecordsSuccessiveVerificationSets(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	configPath := ConfigDiskFile(dir, name)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Name: name, Restart: "no", Verification: &vmkit.RuntimeVerification{
		OK: true, Config: &vmkit.VerifiedArtifact{Path: configPath, SHA256: "first"},
	}}
	if err := writeManifestRecord(Options{StateDir: dir, Name: name}, manifest, "boot_verification"); err != nil {
		t.Fatal(err)
	}
	manifest.Verification.Config.SHA256 = "second"
	if err := writeManifestRecord(Options{StateDir: dir, Name: name}, manifest, "boot_verification"); err != nil {
		t.Fatal(err)
	}

	revisions, err := ReadConstraintHistory(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Verification.Config.SHA256 != "first" || revisions[1].Verification.Config.SHA256 != "second" {
		t.Fatalf("verification history = %#v", revisions)
	}
}

func TestConstraintHistoryIsBoundedWithIndependentSnapshots(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent"}
	const maxEntries = 3
	for i := 0; i < maxEntries+2; i++ {
		manifest := Manifest{Name: opts.Name, Restart: "no", Env: map[string]string{"REV": string(rune('a' + i%26))}}
		if err := appendConstraintRevisionWithLimit(opts, "test", &manifest, maxEntries); err != nil {
			t.Fatal(err)
		}
	}
	revisions, err := ReadConstraintHistory(dir, opts.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != maxEntries {
		t.Fatalf("retained revisions = %d, want %d", len(revisions), maxEntries)
	}
	if revisions[0].Manifest == nil || revisions[0].Manifest.Name != opts.Name {
		t.Fatalf("oldest retained revision is not independently reconstructable: %#v", revisions[0])
	}
}

func TestConstraintHistoryMalformedFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent", RestartPolicy: "no", Env: map[string]string{"REV": "original"}}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(dir, "workspaces", opts.Name, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := ConstraintHistoryPath(dir, "agent")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Env["REV"] = "replacement"
	if err := WriteManifest(opts); err == nil {
		t.Fatal("append should refuse to replace malformed constraint history")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{not-json" {
		t.Fatalf("malformed history was replaced: %q", data)
	}
	current, err := os.ReadFile(filepath.Join(dir, "workspaces", opts.Name, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(original) {
		t.Fatal("current manifest changed without a committed constraint revision")
	}
}

func TestStatusResponseSummarizesConstraintHistory(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "agent", RestartPolicy: "no", Backend: vmkit.BackendLinuxKVM}
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	event := EventFile{Identity: vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend}, State: vmkit.StatePrepared}
	response := responseFromEvent(opts, event, "")
	if response.ConstraintHistory == nil || response.ConstraintHistory.Count != 1 || response.ConstraintHistory.Latest == nil {
		t.Fatalf("constraint history status = %#v", response.ConstraintHistory)
	}
	if response.ConstraintHistory.Latest.Trigger != "manifest_write" || response.ConstraintHistory.MaxEntries != DefaultMaxConstraintRevisions {
		t.Fatalf("constraint history latest = %#v", response.ConstraintHistory)
	}
}
