package kernel

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"6.18.35", "6.18.35", 0},
		{"6.18.34", "6.18.35", -1},
		{"6.18.35", "6.18.34", 1},
		{"6.1.155", "6.18.35", -1}, // 6.1.x is older than 6.18.x despite "155" > "35"
		{"6.18", "6.18.0", 0},      // missing trailing segment == 0
		{"6.18.1", "6.18", 1},
		{"6.2.0", "6.18.0", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckUpdate(t *testing.T) {
	targets := []KernelTarget{
		{Backend: "linux-kvm", Arch: "amd64", Version: "6.18.30", Classification: ClassificationRoutine},
		{Backend: "linux-kvm", Arch: "amd64", Version: "6.18.35", Classification: ClassificationSecurity,
			SecurityFloor: "6.18.32", CVEs: []string{"CVE-2026-12345"}},
		{Backend: "apple-vf", Arch: "arm64", Version: "6.12.22", Classification: ClassificationRoutine},
	}

	check := func(installed string) UpdateCheck {
		return CheckUpdate(targets, "linux-kvm", "amd64", installed)
	}

	if got := check("6.18.35"); got.Status != StatusCurrent {
		t.Errorf("installed==latest: status=%q, want current", got.Status)
	}
	if got := check("6.18.31"); got.Status != StatusSecurity || got.LatestVersion != "6.18.35" {
		t.Errorf("below floor: status=%q latest=%q, want security/6.18.35", got.Status, got.LatestVersion)
	}
	if got := check("6.18.31"); len(got.CVEs) != 1 || got.CVEs[0] != "CVE-2026-12345" {
		t.Errorf("below floor should carry CVEs, got %v", got.CVEs)
	}
	if got := check("6.18.33"); got.Status != StatusOptional {
		t.Errorf("at/above floor but behind latest: status=%q, want optional", got.Status)
	}
	if got := check(""); got.Status != StatusUnknown {
		t.Errorf("empty installed: status=%q, want unknown", got.Status)
	}
	// latest is picked across multiple versions
	if got := check("6.18.40"); got.Status != StatusCurrent || got.LatestVersion != "6.18.35" {
		t.Errorf("ahead of latest: status=%q latest=%q, want current/6.18.35", got.Status, got.LatestVersion)
	}
	// no targets for backend/arch
	if got := CheckUpdate(targets, "windows-hyperv", "amd64", "6.12.22"); got.Status != StatusUnknown {
		t.Errorf("no data: status=%q, want unknown", got.Status)
	}
}
