package rootfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRequestRejectsMutableLatest(t *testing.T) {
	req := BuildRequest{
		ImageRef:   "ghcr.io/example/agent:latest",
		Platform:   Platform{OS: "linux", Architecture: "arm64"},
		OutputPath: "/tmp/rootfs.ext4",
	}

	if err := ValidateRequest(req); err == nil {
		t.Fatal("expected mutable latest tag to be rejected")
	}
}

func TestValidateRequestAcceptsDigestReference(t *testing.T) {
	req := BuildRequest{
		ImageRef:   "ghcr.io/example/agent@sha256:abc123",
		Platform:   Platform{OS: "linux", Architecture: "arm64"},
		OutputPath: "/tmp/rootfs.ext4",
	}

	if err := ValidateRequest(req); err != nil {
		t.Fatalf("expected digest reference to be accepted: %v", err)
	}
}

func TestValidateRequestRejectsInvalidConsoleShell(t *testing.T) {
	for _, shellPath := range []string{"bash", "/bin/../bin/bash"} {
		req := BuildRequest{
			ImageRef:     "ghcr.io/example/agent@sha256:abc123",
			Platform:     Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:   "/tmp/rootfs.ext4",
			ConsoleShell: shellPath,
			AllowMutable: true,
		}
		if err := ValidateRequest(req); err == nil {
			t.Fatalf("ValidateRequest accepted console shell %q", shellPath)
		}
	}
}

func TestValidateRequestRejectsInvalidHostname(t *testing.T) {
	for _, hostname := range []string{"bad_name", "-bad", strings.Repeat("a", 64)} {
		req := BuildRequest{
			ImageRef:     "ghcr.io/example/agent@sha256:abc123",
			Platform:     Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:   "/tmp/rootfs.ext4",
			Hostname:     hostname,
			AllowMutable: true,
		}
		if err := ValidateRequest(req); err == nil {
			t.Fatalf("ValidateRequest accepted hostname %q", hostname)
		}
	}
}

func TestNormalizeRequestSetsDefaults(t *testing.T) {
	req := NormalizeRequest(BuildRequest{
		ImageRef:   "ghcr.io/example/agent@sha256:abc123",
		OutputPath: "/tmp/rootfs.ext4",
	})

	if req.Platform.OS != "linux" {
		t.Fatalf("OS = %q, want linux", req.Platform.OS)
	}
	if req.Platform.Architecture != "arm64" {
		t.Fatalf("Architecture = %q, want arm64", req.Platform.Architecture)
	}
	if req.InitPath != DefaultInitPath {
		t.Fatalf("InitPath = %q, want %q", req.InitPath, DefaultInitPath)
	}
	if req.SizeMiB != DefaultSizeMiB {
		t.Fatalf("SizeMiB = %d, want %d", req.SizeMiB, DefaultSizeMiB)
	}
	if req.Format != FormatExt4 {
		t.Fatalf("Format = %q, want %q", req.Format, FormatExt4)
	}
}

func TestValidateRequestRejectsUnknownFormat(t *testing.T) {
	req := BuildRequest{
		ImageRef:   "ghcr.io/example/agent@sha256:abc123",
		Platform:   Platform{OS: "linux", Architecture: "arm64"},
		OutputPath: "/tmp/rootfs.raw",
		Format:     "raw",
	}

	err := ValidateRequest(req)
	if err == nil || !strings.Contains(err.Error(), `format must be "ext4"`) {
		t.Fatalf("ValidateRequest err = %v, want format validation", err)
	}
}

func TestValidateFilesRejectsDuplicateDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "agent.py")
	if err := os.WriteFile(src, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ValidateFiles([]File{
		{SourcePath: src, Path: "/app/agent.py"},
		{SourcePath: src, Path: "/app/agent.py"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate file dst") {
		t.Fatalf("err = %v, want duplicate destination", err)
	}
}

func TestValidateFilesRejectsDirectorySource(t *testing.T) {
	dir := t.TempDir()
	err := ValidateFiles([]File{{SourcePath: dir, Path: "/app/agent.py"}})
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v, want regular file validation", err)
	}
}

func TestValidateFilesRejectsInvalidMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "agent.py")
	if err := os.WriteFile(src, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ValidateFiles([]File{{SourcePath: src, Path: "/app/agent.py", Mode: "8888"}})
	if err == nil || !strings.Contains(err.Error(), "invalid syntax") {
		t.Fatalf("err = %v, want invalid mode", err)
	}
}
