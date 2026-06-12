package ociimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func stageTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "hostname"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
		if runtime.GOOS == "windows" {
			// Symlink creation needs a privilege (or Developer Mode) on
			// Windows; the staged-tree tests are about tar assembly, not
			// host symlink rights.
			t.Skipf("symlink creation is not permitted on this host: %v", err)
		}
		t.Fatal(err)
	}
	return dir
}

func TestAssembleProducesValidImage(t *testing.T) {
	img, err := Assemble(Options{Dir: stageTree(t), Architecture: "arm64", CreatedAt: time.Unix(1000, 0)})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Descriptor digests must match their bytes and media types.
	if img.Layer.Descriptor.Digest != digest.FromBytes(img.Layer.Data) {
		t.Error("layer digest mismatch")
	}
	if img.Layer.Descriptor.MediaType != ocispec.MediaTypeImageLayerGzip {
		t.Errorf("layer media type = %s", img.Layer.Descriptor.MediaType)
	}
	if img.Config.Descriptor.Digest != digest.FromBytes(img.Config.Data) {
		t.Error("config digest mismatch")
	}
	if img.Manifest.Descriptor.Digest != digest.FromBytes(img.Manifest.Data) {
		t.Error("manifest digest mismatch")
	}

	// Config: arch/os and diff_ids must match the uncompressed layer.
	var cfg ocispec.Image
	if err := json.Unmarshal(img.Config.Data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Architecture != "arm64" || cfg.OS != "linux" {
		t.Errorf("config platform = %s/%s", cfg.OS, cfg.Architecture)
	}
	if len(cfg.RootFS.DiffIDs) != 1 || cfg.RootFS.DiffIDs[0].String() != img.DiffID {
		t.Errorf("diff_ids = %v, want [%s]", cfg.RootFS.DiffIDs, img.DiffID)
	}

	// The diff ID must equal the digest of the uncompressed tar.
	gz, err := gzip.NewReader(bytes.NewReader(img.Layer.Data))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if digest.FromBytes(raw).String() != img.DiffID {
		t.Errorf("diff ID %s does not match uncompressed tar digest %s", img.DiffID, digest.FromBytes(raw))
	}

	// Manifest references config + the one layer.
	var manifest ocispec.Manifest
	if err := json.Unmarshal(img.Manifest.Data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Config.Digest != img.Config.Descriptor.Digest {
		t.Error("manifest config digest mismatch")
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].Digest != img.Layer.Descriptor.Digest {
		t.Error("manifest layer mismatch")
	}
	if manifest.SchemaVersion != 2 {
		t.Errorf("schema version = %d", manifest.SchemaVersion)
	}
}

func TestAssembleTarContents(t *testing.T) {
	img, err := Assemble(Options{Dir: stageTree(t), Architecture: "amd64", CreatedAt: time.Unix(0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(bytes.NewReader(img.Layer.Data))
	tr := tar.NewReader(gz)
	found := map[string]string{} // name -> linkname (or "" )
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found[hdr.Name] = hdr.Linkname
		names = append(names, hdr.Name)
		if !hdr.ModTime.Equal(time.Unix(0, 0).UTC()) {
			t.Errorf("entry %s modtime not normalized: %v", hdr.Name, hdr.ModTime)
		}
	}
	if _, ok := found["etc/"]; !ok {
		t.Errorf("missing etc/ dir entry; got %v", names)
	}
	if _, ok := found["etc/hostname"]; !ok {
		t.Errorf("missing etc/hostname; got %v", names)
	}
	if link := found["link"]; link != "/etc/hostname" {
		t.Errorf("symlink target = %q, want /etc/hostname", link)
	}
	// Sorted order: app, etc/, etc/hostname, link.
	want := []string{"app", "etc/", "etc/hostname", "link"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entry order = %v, want %v", names, want)
		}
	}
}

func TestAssembleDeterministic(t *testing.T) {
	dir := stageTree(t)
	a, err := Assemble(Options{Dir: dir, Architecture: "amd64", CreatedAt: time.Unix(5, 0)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Assemble(Options{Dir: dir, Architecture: "amd64", CreatedAt: time.Unix(5, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.Descriptor.Digest != b.Manifest.Descriptor.Digest {
		t.Error("assembly is not deterministic for identical inputs")
	}
}

func TestAssembleRejectsBadInput(t *testing.T) {
	if _, err := Assemble(Options{Architecture: "amd64"}); err == nil {
		t.Error("empty dir should error")
	}
	if _, err := Assemble(Options{Dir: t.TempDir()}); err == nil {
		t.Error("empty architecture should error")
	}
}
