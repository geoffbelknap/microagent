package vmkit

import (
	"fmt"
	"strings"
)

// EgressCaps constrains the resource usage of the egress mediator.
// Zero means unlimited for each field. All fields must be non-negative.
type EgressCaps struct {
	MaxBytesPerSec     int64
	MaxTotalBytes      int64
	MaxConcurrentConns int32
	AuditMaxBytes      int64
	AuditMaxBackups    int
}

// EgressPolicy is the launch-time egress policy bundle the host sets for a
// workspace. It is host-sourced and never derived from guest-controlled input.
type EgressPolicy struct {
	Mode           string   // "mediated" | "strict" | "off"
	Allow          []string // allowlisted egress destination hosts
	Passthrough    []string // hosts to L4-splice (no MITM)
	SwapConfigPath string   // path to the operator credential-swap config; may be empty
	Caps           EgressCaps
	DNS            []string // guest resolvers; may be empty (caller supplies a default)
}

// NormalizeEgressPolicy returns a copy with Mode resolved via NormalizeEgressMode
// (empty/unknown -> "mediated", the secure default) and Allow/Passthrough/DNS each
// trimmed of surrounding whitespace, empty entries dropped, duplicates removed,
// original order preserved.
func NormalizeEgressPolicy(p EgressPolicy) EgressPolicy {
	p.Mode = NormalizeEgressMode(p.Mode)
	p.Allow = cleanList(p.Allow)
	p.Passthrough = cleanList(p.Passthrough)
	p.DNS = cleanList(p.DNS)
	return p
}

// cleanList trims whitespace, drops empty entries, and removes duplicates
// while preserving the original order of first occurrence.
func cleanList(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Validate reports a policy that cannot be enforced. It returns an error when:
//   - Mode is not one of mediated/strict/off (call NormalizeEgressPolicy first)
//   - any Caps field is negative
//
// Allow/Passthrough/DNS are assumed already cleaned by NormalizeEgressPolicy.
func (p EgressPolicy) Validate() error {
	switch p.Mode {
	case EgressModeMediated, EgressModeStrict, EgressModeOff:
		// valid
	default:
		return fmt.Errorf("vmkit: invalid egress mode %q: must be one of mediated, strict, off", p.Mode)
	}
	if p.Caps.MaxBytesPerSec < 0 {
		return fmt.Errorf("vmkit: Caps.MaxBytesPerSec must be non-negative, got %d", p.Caps.MaxBytesPerSec)
	}
	if p.Caps.MaxTotalBytes < 0 {
		return fmt.Errorf("vmkit: Caps.MaxTotalBytes must be non-negative, got %d", p.Caps.MaxTotalBytes)
	}
	if p.Caps.MaxConcurrentConns < 0 {
		return fmt.Errorf("vmkit: Caps.MaxConcurrentConns must be non-negative, got %d", p.Caps.MaxConcurrentConns)
	}
	if p.Caps.AuditMaxBytes < 0 {
		return fmt.Errorf("vmkit: Caps.AuditMaxBytes must be non-negative, got %d", p.Caps.AuditMaxBytes)
	}
	if p.Caps.AuditMaxBackups < 0 {
		return fmt.Errorf("vmkit: Caps.AuditMaxBackups must be non-negative, got %d", p.Caps.AuditMaxBackups)
	}
	return nil
}
