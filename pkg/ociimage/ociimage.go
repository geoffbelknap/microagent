// Package ociimage assembles a single-layer OCI image from a directory tree:
// a gzip-compressed tar layer, an image config carrying the layer diff ID, and
// an image manifest. It is the reverse of the OCI->rootfs realize path and the
// core of `microagent commit`. Assembly is pure (filesystem read of the staged
// tree only); writing blobs to a store and pushing to a registry are layered on
// top by the caller.
package ociimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Blob is an assembled content-addressed object plus its descriptor.
type Blob struct {
	Descriptor ocispec.Descriptor
	Data       []byte
}

// Image is a fully assembled single-layer OCI image.
type Image struct {
	Layer    Blob // gzipped tar layer
	Config   Blob // image config JSON
	Manifest Blob // image manifest JSON
	DiffID   string
}

// Options configures assembly.
type Options struct {
	// Dir is the staged root filesystem tree to pack as one layer.
	Dir string
	// LayerTar, when set, is a pre-built layer tar used verbatim instead of
	// packing Dir — for callers that already hold the filesystem as a tar
	// stream (guest-mediated commit) and must not round-trip it through a
	// host filesystem that cannot represent symlinks unprivileged.
	LayerTar []byte
	// Architecture is the OCI architecture (e.g. "amd64", "arm64").
	Architecture string
	// CreatedAt stamps the config; caller supplies it for determinism.
	CreatedAt time.Time
	// Config carries OCI runtime defaults into the committed image. RootFS,
	// history, platform, and creation time remain owned by Assemble.
	Config ocispec.ImageConfig
}

// Assemble packs Dir (or the pre-built LayerTar) into a one-layer OCI image
// and returns its blobs.
func Assemble(opts Options) (Image, error) {
	if (opts.Dir == "") == (len(opts.LayerTar) == 0) {
		return Image{}, fmt.Errorf("exactly one of dir or layer tar is required")
	}
	if opts.Architecture == "" {
		return Image{}, fmt.Errorf("architecture is required")
	}
	tarBytes := opts.LayerTar
	if opts.Dir != "" {
		var err error
		tarBytes, err = buildTar(opts.Dir)
		if err != nil {
			return Image{}, err
		}
	}
	diffID := digest.FromBytes(tarBytes)

	gzBytes, err := gzipBytes(tarBytes)
	if err != nil {
		return Image{}, err
	}
	layer := Blob{
		Data: gzBytes,
		Descriptor: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Digest:    digest.FromBytes(gzBytes),
			Size:      int64(len(gzBytes)),
		},
	}

	created := opts.CreatedAt.UTC()
	config := ocispec.Image{
		Created:  &created,
		Platform: ocispec.Platform{OS: "linux", Architecture: opts.Architecture},
		Config:   opts.Config,
		RootFS:   ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{diffID}},
		History:  []ocispec.History{{Created: &created, CreatedBy: "microagent commit", Comment: "rootfs committed by microagent"}},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		return Image{}, fmt.Errorf("marshal config: %w", err)
	}
	configBlob := Blob{
		Data: configBytes,
		Descriptor: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.FromBytes(configBytes),
			Size:      int64(len(configBytes)),
		},
	}

	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configBlob.Descriptor,
		Layers:    []ocispec.Descriptor{layer.Descriptor},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Image{}, fmt.Errorf("marshal manifest: %w", err)
	}
	manifestBlob := Blob{
		Data: manifestBytes,
		Descriptor: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    digest.FromBytes(manifestBytes),
			Size:      int64(len(manifestBytes)),
		},
	}

	return Image{Layer: layer, Config: configBlob, Manifest: manifestBlob, DiffID: diffID.String()}, nil
}

// buildTar walks dir and writes a deterministic (path-sorted) tar of its
// regular files, directories, and symlinks. Special files (devices, sockets,
// fifos) are skipped — they cannot be represented unprivileged and do not
// belong in a committed image layer.
func buildTar(dir string) ([]byte, error) {
	type entry struct {
		path string
		rel  string
	}
	var entries []entry
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		entries = append(entries, entry{path: path, rel: filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		info, err := os.Lstat(e.path)
		if err != nil {
			return nil, err
		}
		mode := info.Mode()
		var link string
		if mode&os.ModeSymlink != 0 {
			link, err = os.Readlink(e.path)
			if err != nil {
				return nil, err
			}
		} else if !mode.IsRegular() && !mode.IsDir() {
			// Skip devices, sockets, fifos.
			continue
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil, err
		}
		hdr.Name = e.rel
		if mode.IsDir() {
			hdr.Name += "/"
		}
		// Normalize timestamps for reproducibility.
		hdr.ModTime = time.Unix(0, 0).UTC()
		hdr.AccessTime = time.Time{}
		hdr.ChangeTime = time.Time{}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if mode.IsRegular() {
			f, err := os.Open(e.path)
			if err != nil {
				return nil, err
			}
			if _, err := io.Copy(tw, f); err != nil {
				_ = f.Close()
				return nil, err
			}
			_ = f.Close()
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
