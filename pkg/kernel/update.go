package kernel

import (
	"strconv"
	"strings"
)

// Classification is a kernel release's CVE-driven severity, carried in the
// signed manifest (TUF target custom metadata).
type Classification string

const (
	ClassificationRoutine  Classification = "routine"
	ClassificationSecurity Classification = "security"
	ClassificationCritical Classification = "critical"
)

// KernelTarget is one available kernel as listed in the verified manifest.
// The TUF client populates these from the signed targets metadata; downstream
// code only ever sees targets that passed signature + hash verification.
type KernelTarget struct {
	Backend        string         `json:"backend"`
	Arch           string         `json:"arch"`
	Version        string         `json:"version"`
	URL            string         `json:"url"`
	SHA256         string         `json:"sha256"`
	Classification Classification `json:"classification,omitempty"`
	// SecurityFloor is the lowest version that still carries all current
	// security fixes for this backend/arch. An installed version below the
	// floor is missing security fixes; at or above it is merely behind latest.
	SecurityFloor string   `json:"securityFloor,omitempty"`
	CVEs          []string `json:"cves,omitempty"`
}

// UpdateStatus is the result of comparing an installed kernel to what's
// available for its backend/arch.
type UpdateStatus string

const (
	StatusCurrent  UpdateStatus = "current"  // installed >= latest
	StatusOptional UpdateStatus = "optional" // behind latest, but at/above the security floor
	StatusSecurity UpdateStatus = "security" // below the security floor — missing security fixes
	StatusUnknown  UpdateStatus = "unknown"  // no data for this backend/arch, or unknown installed version
)

// UpdateCheck is the rendered answer for "is my kernel behind, and does it
// matter for security?" — the supply/consumer differentiation surface.
type UpdateCheck struct {
	Backend          string         `json:"backend"`
	Arch             string         `json:"arch"`
	InstalledVersion string         `json:"installedVersion,omitempty"`
	LatestVersion    string         `json:"latestVersion,omitempty"`
	Status           UpdateStatus   `json:"status"`
	Classification   Classification `json:"classification,omitempty"`
	CVEs             []string       `json:"cves,omitempty"`
	Target           *KernelTarget  `json:"-"`
}

// LatestTarget returns the highest-versioned target for the backend/arch, or
// nil if none is available.
func LatestTarget(targets []KernelTarget, backend, arch string) *KernelTarget {
	var latest *KernelTarget
	for i := range targets {
		t := &targets[i]
		if t.Backend != backend || t.Arch != arch {
			continue
		}
		if latest == nil || CompareVersions(t.Version, latest.Version) > 0 {
			latest = t
		}
	}
	return latest
}

// CheckUpdate compares an installed kernel version against the available
// targets and classifies the gap: current, optional, or security-relevant.
// An empty installedVersion or a backend/arch with no targets yields Unknown.
func CheckUpdate(targets []KernelTarget, backend, arch, installedVersion string) UpdateCheck {
	res := UpdateCheck{Backend: backend, Arch: arch, InstalledVersion: installedVersion, Status: StatusUnknown}
	latest := LatestTarget(targets, backend, arch)
	if latest == nil {
		return res
	}
	res.LatestVersion = latest.Version
	res.Classification = latest.Classification
	res.CVEs = latest.CVEs
	res.Target = latest
	if strings.TrimSpace(installedVersion) == "" {
		return res
	}
	switch {
	case CompareVersions(installedVersion, latest.Version) >= 0:
		res.Status = StatusCurrent
	case latest.SecurityFloor != "" && CompareVersions(installedVersion, latest.SecurityFloor) < 0:
		res.Status = StatusSecurity
	default:
		res.Status = StatusOptional
	}
	return res
}

// CompareVersions compares dotted numeric kernel versions like "6.18.35".
// It returns -1 if a < b, 0 if equal, +1 if a > b. Missing trailing segments
// are treated as 0 (so "6.18" == "6.18.0"); non-numeric segments parse as 0.
func CompareVersions(a, b string) int {
	as := strings.Split(strings.TrimSpace(a), ".")
	bs := strings.Split(strings.TrimSpace(b), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}
