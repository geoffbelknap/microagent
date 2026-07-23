package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/model"
)

func TestShortDigest(t *testing.T) {
	cases := []struct {
		name   string
		digest string
		want   string
	}{
		{
			name:   "sha256 prefix trimmed to 12 hex",
			digest: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
			want:   "abcdef012345",
		},
		{
			name:   "no colon prefix still trims to 12",
			digest: "abcdef0123456789",
			want:   "abcdef012345",
		},
		{
			name:   "shorter than 12 hex chars returned as-is",
			digest: "sha256:abc",
			want:   "abc",
		},
		{
			name:   "empty digest stays empty",
			digest: "",
			want:   "",
		},
		{
			name:   "exactly 12 hex chars unchanged",
			digest: "sha256:abcdef012345",
			want:   "abcdef012345",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shortDigest(tc.digest); got != tc.want {
				t.Fatalf("shortDigest(%q) = %q, want %q", tc.digest, got, tc.want)
			}
		})
	}
}

// TestModelListShortensDigestKeepsTabFormat asserts model list's digest
// column shortens like every other human list view, while its
// tab-separated, headerless shape (which predates the width-aware table and
// has no fixed-width contract to preserve) is otherwise untouched.
func TestModelListShortensDigestKeepsTabFormat(t *testing.T) {
	full := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	list := []model.Record{
		{ModelRef: "org/model", SizeBytes: 42, Digest: full},
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = io.ReadAll(r)
		close(done)
	}()
	if err := writeModelList(w, list); err != nil {
		t.Fatalf("writeModelList: %v", err)
	}
	w.Close()
	<-done
	r.Close()

	want := "org/model\t42\tabcdef012345\n"
	if string(out) != want {
		t.Fatalf("writeModelList = %q, want %q", out, want)
	}
	if strings.Contains(string(out), full) {
		t.Fatalf("expected the full digest to be shortened, found it verbatim in %q", out)
	}
}

// TestImageListJSONKeepsFullDigest is the explicit digest-specific
// coverage the JSON envelope must not lose: `image list --json` (and
// `image inspect`, exercised via writeImageRecord) always carry the full
// digest string, even though the human list view shortens it.
func TestImageListJSONKeepsFullDigest(t *testing.T) {
	full := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	prevFormat := outputFormat
	t.Cleanup(func() { outputFormat = prevFormat })
	outputFormat = "json"

	images := []imageRecord{{ImageRef: "docker.io/library/alpine:3.19", Digest: full}}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = io.ReadAll(r)
		close(done)
	}()
	if err := writeImageList(w, images); err != nil {
		t.Fatalf("writeImageList: %v", err)
	}
	w.Close()
	<-done
	r.Close()

	if !strings.Contains(string(out), full) {
		t.Fatalf("expected image list --json to carry the full digest %q, got %q", full, out)
	}

	// image inspect (writeImageRecord) must also carry the full digest.
	prevFormat2 := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = prevFormat2 }()
	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done2 := make(chan struct{})
	var out2 []byte
	go func() {
		out2, _ = io.ReadAll(r2)
		close(done2)
	}()
	if err := writeImageRecord(w2, imageRecord{ImageRef: "docker.io/library/alpine:3.19", Digest: full}); err != nil {
		t.Fatalf("writeImageRecord: %v", err)
	}
	w2.Close()
	<-done2
	r2.Close()
	if !strings.Contains(string(out2), full) {
		t.Fatalf("expected image inspect to carry the full digest %q, got %q", full, out2)
	}
}
