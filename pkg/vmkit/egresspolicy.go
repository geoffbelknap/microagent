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
// workspace. It is host-sourced and never derived from guest-controlled input;
// the agent or guest cannot influence or change it at runtime (ASK Tenets 1 &
// 18: enforcement is external and inviolable; the governance hierarchy is
// inviolable from below).
type EgressPolicy struct {
	Mode           string   // "broker" | "mitm" | "off"
	Allow          []string // allowlisted egress destination hosts
	Passthrough    []string // hosts to L4-splice (no MITM)
	SwapConfigPath string   // path to the operator credential-swap config; may be empty
	Caps           EgressCaps
	DNS            []string // guest resolvers; may be empty (caller supplies a default)
	// AllowlistLocked, in broker mode, restricts egress to allowlisted
	// destinations only (drops the allow-broad grant). No effect otherwise.
	AllowlistLocked bool
}

// NormalizeEgressPolicy returns a copy with Mode's default resolved (empty ->
// "broker") and Allow/Passthrough/DNS each trimmed of surrounding whitespace,
// empty entries dropped, duplicates removed, original order preserved. It does
// NOT validate the mode — call Validate for that (a retired/unknown mode
// survives normalization so Validate can reject it with a clear error).
func NormalizeEgressPolicy(p EgressPolicy) EgressPolicy {
	p.Mode = ResolveEgressModeDefault(p.Mode)
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

// ValidateForNetworkMode rejects a policy that claims mediation on a network mode
// that cannot mediate. Mediation on (broker/mitm) requires the user network mode
// unless the workspace is isolated (no egress at all). Off egress, isolated, and
// the mediating user mode all pass.
func (p EgressPolicy) ValidateForNetworkMode(networkMode string) error {
	// Lowercase the isolated check to match NetworkModeMediates' case handling, so
	// the fail-closed guard stays correct even if upstream mode validation is relaxed.
	nm := strings.ToLower(strings.TrimSpace(networkMode))
	if EgressMediationOn(p.Mode) && !NetworkModeMediates(networkMode) && nm != "isolated" {
		return fmt.Errorf("vmkit: egress mode %q requires the user network mode or isolated; network mode %q cannot mediate; use user mode or set egress off", p.Mode, networkMode)
	}
	return nil
}

// ValidateForCaptureProvider fails closed when a mediated egress policy
// (broker/mitm) has no capture provider that can cover it on the given backend
// and network mode. It is the backend-aware successor to the NetworkModeMediates
// heuristic: a provider that leaves any protocol class uncovered — e.g. Apple
// VF's native NAT, which exposes no microagent-owned capture point and leaves
// the guest a direct uplink — cannot honor mediated egress, so the workspace
// must not start under a false mediation claim (ASK Tenet 4: enforcement failure
// defaults to denial). egress=off and isolated/no-egress modes pass.
func (p EgressPolicy) ValidateForCaptureProvider(backend, networkMode string) error {
	if !EgressMediationOn(p.Mode) {
		return nil
	}
	report := NegotiateEgressCapture(backend, networkMode, p.Mode)
	if report.HasUncoveredClass() {
		reason := strings.Join(report.Limitations, "; ")
		if reason == "" {
			reason = "no egress capture provider is available"
		}
		return fmt.Errorf("vmkit: egress mode %q is not available on backend %q: %s; re-run with --egress off for explicit unmediated networking", p.Mode, backend, reason)
	}
	return nil
}

// Validate reports a policy that cannot be enforced. It returns an error when:
//   - Mode is not one of broker/mitm/off (retired names and junk are rejected
//     with the successor-naming message from ValidateEgressMode)
//   - any Caps field is negative
//
// Allow/Passthrough/DNS are assumed already cleaned by NormalizeEgressPolicy.
func (p EgressPolicy) Validate() error {
	if _, err := ValidateEgressMode(p.Mode); err != nil {
		return err
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
