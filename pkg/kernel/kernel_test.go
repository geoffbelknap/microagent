package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestInstallFromPathAndVerify(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source-kernel")
	if err := os.WriteFile(source, []byte("kernel bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "kernels", "Image")

	installed, err := Install(t.Context(), InstallOptions{
		FromPath:     source,
		OutputPath:   target,
		Architecture: "amd64",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.Path != target || installed.SHA256 == "" {
		t.Fatalf("installed = %#v", installed)
	}
	verified, err := Verify(VerifyOptions{Path: target, SHA256: installed.SHA256})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verified.OK || verified.Path != target {
		t.Fatalf("verified = %#v", verified)
	}
}

func TestSupportReportsDownloadableDefault(t *testing.T) {
	support := SupportForPath(vmkit.BackendLinuxKVM, "amd64", filepath.Join(t.TempDir(), "missing"))
	if support.Status != "downloadable" || support.SHA256 == "" {
		t.Fatalf("support = %#v", support)
	}
}
