package workspace

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteConfigDiskRoundTrip: the config disk is a raw tar whose first
// entry is the run config and whose remaining entries are the declared
// files captured at create — the exact wire shape guest init consumes.
func TestWriteConfigDiskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seed, []byte("seed-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Name:            "research",
		StateDir:        dir,
		Backend:         "linux-kvm",
		PrepareForStart: true,
		Entrypoint:      "/app/serve.sh",
		SetupComplete:   true,
		Env:             map[string]string{"GOOD": "ok", "bad-name": "dropped"},
		ImageEnv:        []string{"PATH=/usr/bin", "GOOD=image-loses"},
		ConsoleShell:    "/bin/bash",
		ResultPort:      1024,
		Files:           []File{{SourcePath: seed, Path: "/opt/seed.txt", Mode: "0600"}},
	}
	if err := WriteFilesArchive(opts); err != nil {
		t.Fatalf("WriteFilesArchive: %v", err)
	}
	path, err := WriteConfigDisk(opts)
	if err != nil {
		t.Fatalf("WriteConfigDisk: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < configDiskSizeFloor || info.Size()%512 != 0 {
		t.Fatalf("config disk size = %d, want >= %d and 512-aligned", info.Size(), configDiskSizeFloor)
	}

	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	tr := tar.NewReader(source)
	header, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != configDiskRunConfigName {
		t.Fatalf("first entry = %q, want %q", header.Name, configDiskRunConfigName)
	}
	var wire struct {
		Command      []string `json:"command"`
		Env          []string `json:"env"`
		Port         uint32   `json:"port"`
		ConsoleShell string   `json:"consoleShell"`
	}
	if err := json.NewDecoder(tr).Decode(&wire); err != nil {
		t.Fatalf("decode run config: %v", err)
	}
	if len(wire.Command) != 3 || wire.Command[2] != "/app/serve.sh" {
		t.Fatalf("command = %#v", wire.Command)
	}
	if wire.ConsoleShell != "/bin/bash" || wire.Port != 1024 {
		t.Fatalf("wire = %+v", wire)
	}
	// Merge order: request env overrides image env; invalid names dropped.
	wantEnv := map[string]bool{"GOOD=ok": true, "PATH=/usr/bin": true}
	for _, entry := range wire.Env {
		if !wantEnv[entry] {
			t.Fatalf("unexpected env entry %q (env=%v)", entry, wire.Env)
		}
		delete(wantEnv, entry)
	}
	if len(wantEnv) != 0 {
		t.Fatalf("missing env entries: %v", wantEnv)
	}

	fileEntry, err := tr.Next()
	if err != nil {
		t.Fatalf("expected a declared-file entry: %v", err)
	}
	if fileEntry.Name != "files/opt/seed.txt" || fileEntry.Mode != 0o600 {
		t.Fatalf("file entry = %q mode %o", fileEntry.Name, fileEntry.Mode)
	}
	data, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "seed-content\n" {
		t.Fatalf("file content = %q", data)
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after declared files, got %v", err)
	}
}

// TestWriteConfigDiskFilesCapturedAtCreate: boots splice the create-time
// archive; editing or deleting the declared source after create must not
// change what later boots deliver.
func TestWriteConfigDiskFilesCapturedAtCreate(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seed, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Name: "research", StateDir: dir, Backend: "linux-kvm",
		PrepareForStart: true,
		Files:           []File{{SourcePath: seed, Path: "/opt/seed.txt"}},
	}
	if err := WriteFilesArchive(opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(seed); err != nil {
		t.Fatal(err)
	}
	path, err := WriteConfigDisk(opts)
	if err != nil {
		t.Fatalf("WriteConfigDisk after source deletion: %v", err)
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	tr := tar.NewReader(source)
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	fileEntry, err := tr.Next()
	if err != nil || fileEntry.Name != "files/opt/seed.txt" {
		t.Fatalf("declared file lost after source deletion: %v %v", fileEntry, err)
	}
	data, _ := io.ReadAll(tr)
	if string(data) != "v1" {
		t.Fatalf("file content = %q, want the create-time capture", data)
	}
}

// TestGuestBootConfigMaintenance: a maintenance boot serves channels only —
// no command, no result port — with the maintenance flag in the payload.
func TestGuestBootConfigMaintenance(t *testing.T) {
	cfg, err := GuestBootConfig(Options{
		Name: "research", StateDir: "/tmp/x", Backend: "linux-kvm",
		MaintenanceBoot: true,
		ServiceCommand:  "/srv/run.sh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Maintenance || len(cfg.Command) != 0 || cfg.Port != 0 {
		t.Fatalf("maintenance cfg = %+v", cfg)
	}
	if cfg.ShellPort == 0 || cfg.ExecPort == 0 {
		t.Fatalf("maintenance boot must keep shell/exec channels: %+v", cfg)
	}
}
