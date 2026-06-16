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
// and any subdomain.
type Policy struct {
	exact  map[string]struct{}
	suffix []string
}

// NewPolicy builds a Policy from hostnames. It fails on an empty entry so
// misconfiguration cannot silently widen access.
func NewPolicy(allow []string) (*Policy, error) {
	p := &Policy{exact: make(map[string]struct{}, len(allow))}
	for _, raw := range allow {
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" {
			return nil, fmt.Errorf("egress: empty allowlist entry")
		}
		if strings.HasPrefix(h, ".") {
			p.suffix = append(p.suffix, h)
			continue
		}
		p.exact[h] = struct{}{}
	}
	return p, nil
}

// AllowHost evaluates a destination host (SNI or HTTP Host, without port).
func (p *Policy) AllowHost(host string) Decision {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return Decision{Reason: "empty host"}
	}
	if _, ok := p.exact[host]; ok {
		return Decision{Allow: true, Reason: "allowlisted"}
	}
	for _, s := range p.suffix {
		if host == s[1:] || strings.HasSuffix(host, s) {
			return Decision{Allow: true, Reason: "allowlisted suffix " + s}
		}
	}
	return Decision{Reason: "not allowlisted"}
}
