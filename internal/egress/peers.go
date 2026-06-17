package egress

import (
	"fmt"
	"net/netip"
	"strings"
)

// PeerCache is a static name↔IP roster of a named network's members. Unlike the
// DNS NameCache (which records observed DNS answers and expires), it is built
// once from the network roster at mediator start and is authoritative for the
// duration: east-west VM↔VM flows are often raw TCP or dialed by peer name →
// peer IP, with no DNS lookup the mediator can observe, so the bare destination
// IP is reverse-resolved here to the peer's workspace name and policed by name
// under the same default-deny allowlist as any external host.
type PeerCache struct {
	byIP map[netip.Addr]string
}

// NewPeerCache builds a PeerCache from "name=ip" pairs (one per network member,
// excluding this workspace's own entry). An entry missing the "=", with an empty
// name, or with an unparseable IP is rejected so a misconfigured roster cannot
// silently leave a peer unresolvable (and therefore policed by bare IP). A nil or
// empty slice yields an empty, always-miss cache (the nat/user call sites pass
// nil).
func NewPeerCache(pairs []string) (*PeerCache, error) {
	pc := &PeerCache{byIP: make(map[netip.Addr]string, len(pairs))}
	for _, raw := range pairs {
		name, ipStr, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("egress: peer entry %q missing '='", raw)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("egress: peer entry %q has empty name", raw)
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
		if err != nil {
			return nil, fmt.Errorf("egress: peer entry %q has invalid IP: %w", raw, err)
		}
		pc.byIP[addr] = name
	}
	return pc, nil
}

// NameByIP returns the peer workspace name for a, and whether a is a known peer.
func (c *PeerCache) NameByIP(a netip.Addr) (string, bool) {
	name, ok := c.byIP[a]
	return name, ok
}
