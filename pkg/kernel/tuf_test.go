package kernel

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func makeTarget(t *testing.T, c targetCustom, sha string) *metadata.TargetFiles {
	t.Helper()
	cb, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(cb)
	shaBytes, err := hex.DecodeString(sha)
	if err != nil {
		t.Fatal(err)
	}
	return &metadata.TargetFiles{
		Custom: &raw,
		Hashes: metadata.Hashes{"sha256": metadata.HexBytes(shaBytes)},
	}
}

func TestTargetsToKernels(t *testing.T) {
	const sha = "4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"
	targets := map[string]*metadata.TargetFiles{
		"linux-kvm/amd64/6.18.35/vmlinux": makeTarget(t, targetCustom{
			Backend: "linux-kvm", Arch: "amd64", Version: "6.18.35",
			Classification: ClassificationSecurity, SecurityFloor: "6.18.32",
			CVEs: []string{"CVE-2026-1"},
		}, sha),
		"linux-kvm/amd64/6.18.30/vmlinux": makeTarget(t, targetCustom{
			Backend: "linux-kvm", Arch: "amd64", Version: "6.18.30",
			Classification: ClassificationRoutine,
		}, sha),
		// no custom metadata → skipped
		"junk": {Hashes: metadata.Hashes{"sha256": metadata.HexBytes{0x01}}},
	}

	got := targetsToKernels(targets, "https://kernels.microagent.sh/")
	if len(got) != 2 {
		t.Fatalf("got %d kernels, want 2 (junk skipped)", len(got))
	}
	if got[0].Version != "6.18.30" || got[1].Version != "6.18.35" {
		t.Errorf("not sorted ascending by version: %q, %q", got[0].Version, got[1].Version)
	}
	latest := got[1]
	if latest.URL != "https://kernels.microagent.sh/linux-kvm/amd64/6.18.35/vmlinux" {
		t.Errorf("URL = %q", latest.URL)
	}
	if latest.SHA256 != sha {
		t.Errorf("SHA256 = %q, want %q", latest.SHA256, sha)
	}
	if latest.Classification != ClassificationSecurity || latest.SecurityFloor != "6.18.32" {
		t.Errorf("classification/floor = %q/%q", latest.Classification, latest.SecurityFloor)
	}
	// the differentiation logic flows over the mapped targets end-to-end
	if c := CheckUpdate(got, "linux-kvm", "amd64", "6.18.31"); c.Status != StatusSecurity {
		t.Errorf("CheckUpdate over mapped targets: status=%q, want security", c.Status)
	}
}
