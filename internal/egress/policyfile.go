package egress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PolicyFile is an optional, reusable declaration of the egress allowlist and
// passthrough set, an ergonomic alternative to repeating --egress-allow /
// --egress-passthrough for large lists. It only adds reachability: its entries
// are unioned with the flag- and manifest-supplied lists. Default-deny means a
// policy file can never widen access beyond what its declared hosts permit, and
// it cannot grant anything when mediation is off.
//
// The file is decoded strictly: any unknown top-level key is an error so a typo
// (e.g. "allowed" instead of "allow") fails closed rather than silently leaving
// a host unreachable while the operator believes it was allowlisted.
type PolicyFile struct {
	Allow       []string `yaml:"allow" json:"allow"`
	Passthrough []string `yaml:"passthrough" json:"passthrough"`
}

// LoadPolicyFile reads and decodes a policy file. The decoder is chosen by
// extension: ".yaml"/".yml" use YAML with KnownFields(true); ".json" uses a JSON
// decoder with DisallowUnknownFields. Both reject unknown top-level keys. Empty
// or whitespace-only entries in either list are rejected so misconfiguration
// cannot silently widen (or fail to widen) access.
func LoadPolicyFile(path string) (*PolicyFile, error) {
	rawPath := strings.TrimSpace(path)
	if rawPath == "" {
		return nil, fmt.Errorf("egress policy file path is required")
	}
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return nil, fmt.Errorf("resolve egress policy file path: %w", err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read egress policy file %s: %w", absPath, err)
	}

	var pf PolicyFile
	switch ext := strings.ToLower(filepath.Ext(absPath)); ext {
	case ".yaml", ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&pf); err != nil {
			return nil, fmt.Errorf("parse egress policy file %s: %w", absPath, err)
		}
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&pf); err != nil {
			return nil, fmt.Errorf("parse egress policy file %s: %w", absPath, err)
		}
	default:
		return nil, fmt.Errorf("egress policy file %s: unsupported extension %q (use .yaml, .yml, or .json)", absPath, ext)
	}

	if err := validatePolicyEntries("allow", pf.Allow); err != nil {
		return nil, fmt.Errorf("egress policy file %s: %w", absPath, err)
	}
	if err := validatePolicyEntries("passthrough", pf.Passthrough); err != nil {
		return nil, fmt.Errorf("egress policy file %s: %w", absPath, err)
	}
	return &pf, nil
}

// validatePolicyEntries rejects empty or whitespace-only hosts. A blank entry
// is meaningless and almost always a mistake (stray list item, trailing comma);
// failing closed keeps the allowlist honest.
func validatePolicyEntries(field string, entries []string) error {
	for i, e := range entries {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("%s[%d] is empty", field, i)
		}
	}
	return nil
}

// DedupeHosts returns the input hosts with case-insensitive duplicates removed,
// preserving first-seen order. Two entries are considered equal when they
// normalize equal under normalizeHost (lowercase, trim, trailing-dot strip), so
// "API.Example.com" and "api.example.com." collapse to a single entry. Empty or
// whitespace-only entries are dropped. It is used to union flag-, file-, and
// manifest-supplied allow/passthrough lists without introducing redundant
// entries; because the policy is default-deny, deduping can never widen access.
func DedupeHosts(hosts []string) []string {
	seen := make(map[string]struct{}, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		key := normalizeHost(h)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, h)
	}
	return out
}
