package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent-kit/pkg/rootfs"
	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestManifestAndStatusLifecycleAreLibraryOwned(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "agency-task",
		StateDir:       dir,
		Backend:        vmkit.BackendFirecracker,
		Profile:        "small",
		RestartPolicy:  "never",
		MemoryMiB:      512,
		CPUCount:       2,
		SizeMiB:        1024,
		Network:        vmkit.NetworkConfig{Mode: "nat"},
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

	req := Request(opts, "run", filepath.Join(dir, "workspaces", "agency-task", "rootfs.ext4"), "req-1")
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
		Backend:       vmkit.BackendFirecracker,
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
	req := Request(opts, "run", rootfsPath, "req-1")
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
