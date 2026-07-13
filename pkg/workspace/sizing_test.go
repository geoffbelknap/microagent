package workspace

import "testing"

func TestBuildRootfsRequestAutoSizeTracksExplicitSize(t *testing.T) {
	opts := Options{Backend: "linux-kvm", Architecture: "arm64", SizeMiB: 1024}
	if req := buildRootfsRequest(opts, "/tmp/rootfs.ext4"); !req.AutoSize {
		t.Fatal("default-sized workspace should auto-size its rootfs disk")
	}
	opts.SizeExplicit = true
	if req := buildRootfsRequest(opts, "/tmp/rootfs.ext4"); req.AutoSize {
		t.Fatal("explicit --size-mib must pin the rootfs disk size")
	}
	opts.SizeExplicit = false
	opts.SpecSize = true
	if req := buildRootfsRequest(opts, "/tmp/rootfs.ext4"); req.AutoSize {
		t.Fatal("spec-file sizeMiB must pin the rootfs disk size")
	}
}

func TestNormalizeArchAcceptsUnameSpellings(t *testing.T) {
	cases := map[string]string{
		"aarch64": "arm64",
		"arm64":   "arm64",
		"x86_64":  "amd64",
		"amd64":   "amd64",
		" arm64 ": "arm64",
		"riscv64": "riscv64",
	}
	for in, want := range cases {
		if got := NormalizeArch(in); got != want {
			t.Fatalf("NormalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}
