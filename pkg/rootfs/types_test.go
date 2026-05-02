package rootfs

import "testing"

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
}
