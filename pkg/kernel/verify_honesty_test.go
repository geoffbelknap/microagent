package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// TestVerifyOnlyReportsVerifiedWhenChecked is the B20 guard: `kernel verify`
// without an expected sha256 is a hash computation, not a verification, so OK
// and Verified must be false — not a bare OK:true that implies the kernel was
// verified against a trusted source.
func TestVerifyOnlyReportsVerifiedWhenChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Image")
	if err := os.WriteFile(path, []byte("kernel-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := workspace.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	base := VerifyOptions{Path: path, Backend: workspace.HostBackend()}

	// Matching expected hash: verified.
	withSum := base
	withSum.SHA256 = sum
	res, err := Verify(withSum)
	if err != nil {
		t.Fatalf("Verify(matching): %v", err)
	}
	if !res.OK || !res.Verified {
		t.Fatalf("matching hash: result = %+v, want OK && Verified", res)
	}

	// No expected hash: NOT verified, but the computed hash is still reported.
	res, err = Verify(base)
	if err != nil {
		t.Fatalf("Verify(no sha): %v", err)
	}
	if res.OK || res.Verified {
		t.Fatalf("no expected hash: result = %+v, want OK=false Verified=false", res)
	}
	if res.SHA256 != sum {
		t.Fatalf("SHA256 = %q, want %q", res.SHA256, sum)
	}

	// Wrong expected hash: error.
	wrong := base
	wrong.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := Verify(wrong); err == nil {
		t.Fatal("Verify(wrong hash) = nil, want mismatch error")
	}
}
