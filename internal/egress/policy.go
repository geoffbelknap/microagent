// Package egress implements microagent's transparent egress mediator: it
// captures redirected guest connections, enforces a destination allowlist,
// audits every decision, and forwards or denies fail-closed. It holds no
// network-setup logic — the supervisor wires the nftables REDIRECT and spawns
// the mediator.
package egress

import (
	"fmt"
	"strings"
)

// Decision is the outcome of evaluating a destination against the policy.
type Decision struct {
	Allow  bool
	Reason string
}

// Policy is a default-deny destination allowlist. Matching is case-insensitive
// and exact; an entry beginning with a dot (".example.com") matches that apex
// and any subdomain. Trailing dots (FQDN form) are normalized away.
type Policy struct {
	exact  map[string]struct{}
	suffix map[string]struct{}
}

// normalizeHost lowercases, trims surrounding space, and strips any trailing
// dot so "api.example.com" and "api.example.com." compare equal.
func normalizeHost(h string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(h)), ".")
}

// NewPolicy builds a Policy from hostnames. It fails on an entry that is empty
// or normalizes to a bare "." so misconfiguration cannot silently widen access.
func NewPolicy(allow []string) (*Policy, error) {
	p := &Policy{exact: map[string]struct{}{}, suffix: map[string]struct{}{}}
	for _, raw := range allow {
		h := normalizeHost(raw)
		if h == "" {
			return nil, fmt.Errorf("egress: empty allowlist entry %q", raw)
		}
		if strings.HasPrefix(h, ".") {
			p.suffix[h] = struct{}{}
			continue
		}
		p.exact[h] = struct{}{}
	}
	return p, nil
}

// AllowHost evaluates a destination host (SNI or HTTP Host, without port).
func (p *Policy) AllowHost(host string) Decision {
	host = normalizeHost(host)
	if host == "" {
		return Decision{Reason: "empty host"}
	}
	if _, ok := p.exact[host]; ok {
		return Decision{Allow: true, Reason: "allowlisted"}
	}
	for s := range p.suffix {
		if host == s[1:] || strings.HasSuffix(host, s) {
			return Decision{Allow: true, Reason: "allowlisted suffix " + s}
		}
	}
	return Decision{Reason: "not allowlisted"}
}
