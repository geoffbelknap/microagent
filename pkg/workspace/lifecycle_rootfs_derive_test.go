package workspace

import (
	"testing"
)

// TestSizeIsDerived pins the derivation predicate: content decides the disk
// size only when nothing pinned one — no explicit size, no spec size, no
// explicitly chosen profile.
func TestSizeIsDerived(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want bool
	}{
		{"nothing pinned", Options{}, true},
		{"explicit size", Options{SizeExplicit: true}, false},
		{"spec size", Options{SpecSize: true}, false},
		{"explicit profile", Options{ProfileExplicit: true}, false},
	}
	for _, tc := range cases {
		if got := sizeIsDerived(tc.opts); got != tc.want {
			t.Fatalf("%s: sizeIsDerived = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildRootfsRequestCarriesDerivation(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "derive-ws"
	opts.StateDir = t.TempDir()
	opts.HeadroomMiB = 768
	req := buildRootfsRequest(opts, "/tmp/rootfs.ext4")
	if !req.DeriveSize {
		t.Fatal("DeriveSize = false for unpinned size")
	}
	if req.HeadroomMiB != 768 {
		t.Fatalf("HeadroomMiB = %d, want 768", req.HeadroomMiB)
	}
	opts.ProfileExplicit = true
	req = buildRootfsRequest(opts, "/tmp/rootfs.ext4")
	if req.DeriveSize {
		t.Fatal("DeriveSize = true despite explicit profile")
	}
	if !req.AutoSize {
		t.Fatal("AutoSize = false; explicit profile without explicit size must keep grow-to-fit")
	}
}

func TestManifestRoundTripsSizeDerived(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "derive-manifest", StateDir: dir, Backend: startableBackend(), SizeMiB: 1024, SizeDerived: true, HeadroomMiB: 640}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err := ReadManifest(dir, opts.Name)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if !manifest.SizeDerived || manifest.Resources.HeadroomMiB != 640 {
		t.Fatalf("manifest = derived:%v headroom:%d, want derived:true headroom:640", manifest.SizeDerived, manifest.Resources.HeadroomMiB)
	}
	var restored Options
	restored.StateDir = dir
	applyManifest(&restored, manifest)
	if !restored.SizeDerived || restored.HeadroomMiB != 640 {
		t.Fatalf("restored = derived:%v headroom:%d, want derived:true headroom:640", restored.SizeDerived, restored.HeadroomMiB)
	}
}
