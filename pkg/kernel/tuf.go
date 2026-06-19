package kernel

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

// Default locations of the signed kernel manifest on the permanent distribution.
const (
	DefaultMetadataURL = "https://kernels.microagent.sh/metadata/"
	DefaultTargetsURL  = "https://kernels.microagent.sh/"
)

// targetCustom is the per-kernel metadata carried in each TUF target's custom
// field, alongside TUF's native length + hashes.
type targetCustom struct {
	Backend        string         `json:"backend"`
	Arch           string         `json:"arch"`
	Channel        string         `json:"channel"`
	Version        string         `json:"version"`
	Classification Classification `json:"classification,omitempty"`
	SecurityFloor  string         `json:"securityFloor,omitempty"`
	CVEs           []string       `json:"cves,omitempty"`
}

// ManifestSource configures where the signed kernel manifest is fetched and the
// trust anchor (the embedded TUF root) used to verify it.
type ManifestSource struct {
	TrustedRoot []byte // embedded TUF root.json — the trust anchor
	MetadataURL string // base URL for TUF metadata (root/timestamp/snapshot/targets)
	TargetsURL  string // base URL for kernel target files
	CacheDir    string // local metadata cache dir; empty disables caching
}

// FetchTargets runs a TUF refresh against src and returns the verified list of
// available kernels. Every returned target passed TUF signature + hash
// verification against the embedded trusted root; an unsigned, expired, or
// tampered manifest yields an error and no targets (fail-closed).
func FetchTargets(src ManifestSource) ([]KernelTarget, error) {
	if len(src.TrustedRoot) == 0 {
		return nil, fmt.Errorf("no trusted root configured")
	}
	cfg, err := config.New(src.MetadataURL, src.TrustedRoot)
	if err != nil {
		return nil, fmt.Errorf("tuf config: %w", err)
	}
	cfg.RemoteTargetsURL = src.TargetsURL
	if src.CacheDir == "" {
		cfg.DisableLocalCache = true
	} else {
		cfg.LocalMetadataDir = src.CacheDir
		cfg.LocalTargetsDir = src.CacheDir
	}
	up, err := updater.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("tuf updater: %w", err)
	}
	if err := up.Refresh(); err != nil {
		return nil, fmt.Errorf("tuf refresh: %w", err)
	}

	return targetsToKernels(up.GetTopLevelTargets(), src.TargetsURL), nil
}

// targetsToKernels maps verified TUF targets into KernelTargets, sorted by
// backend/arch/version. Targets with missing or unparseable custom metadata are
// skipped. Pure (no I/O), so the mapping is unit-testable without signed
// metadata; TUF signature/hash verification is the updater's job, upstream.
func targetsToKernels(targets map[string]*metadata.TargetFiles, targetsURL string) []KernelTarget {
	var out []KernelTarget
	for path, tf := range targets {
		if tf.Custom == nil {
			continue
		}
		var c targetCustom
		if err := json.Unmarshal(*tf.Custom, &c); err != nil {
			continue
		}
		out = append(out, KernelTarget{
			Backend:        c.Backend,
			Arch:           c.Arch,
			Channel:        c.Channel,
			Version:        c.Version,
			URL:            strings.TrimSuffix(targetsURL, "/") + "/" + path,
			SHA256:         tf.Hashes["sha256"].String(),
			Classification: c.Classification,
			SecurityFloor:  c.SecurityFloor,
			CVEs:           c.CVEs,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Backend != out[j].Backend {
			return out[i].Backend < out[j].Backend
		}
		if out[i].Arch != out[j].Arch {
			return out[i].Arch < out[j].Arch
		}
		return CompareVersions(out[i].Version, out[j].Version) < 0
	})
	return out
}
