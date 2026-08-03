package egress

import (
	"net/netip"
	"sync"
	"time"
)

// maxNameCacheEntries bounds the destination-IP -> hostname cache so a guest
// resolving many distinct names (each yielding fresh A/AAAA records) cannot grow
// the cache without limit. Entries are cheap to recreate on the next DNS answer,
// so eviction (arbitrary, single-entry, mirroring ca.go's leaf cache) trades a
// rare miss for bounded memory.
const maxNameCacheEntries = 4096

// nameEntry is a cached hostname plus the wall-clock time it stops being valid.
type nameEntry struct {
	host   string
	expiry time.Time
}

// NameCache maps a destination IP to the hostname most recently resolved to it,
// as observed in DNS answers. It is TTL-aware (lazy expiry) and bounded. The
// egress policy uses it to reverse-resolve a flow's destination IP back to the
// hostname the guest looked up, so UDP/raw-IP flows can be matched against the
// allowlist by hostname (consistent with TCP SNI/Host matching).
type NameCache struct {
	now func() time.Time

	mu       sync.Mutex
	entries  map[netip.Addr]nameEntry
	bindings map[nameBinding]time.Time
}

type nameBinding struct {
	host string
	ip   netip.Addr
}

// NewNameCache returns an empty cache that uses the real wall clock.
func NewNameCache() *NameCache {
	return newNameCacheWithClock(time.Now)
}

// newNameCacheWithClock returns a cache with an injectable clock for testable
// expiry. now must be non-nil.
func newNameCacheWithClock(now func() time.Time) *NameCache {
	return &NameCache{
		now:      now,
		entries:  map[netip.Addr]nameEntry{},
		bindings: map[nameBinding]time.Time{},
	}
}

// Put records ip -> {normalized host, now+ttl}. The host is normalized the same
// way the Policy normalizes allowlist/lookup hosts (lowercase, trim, strip
// trailing dot) so reverse lookups compare equal to allowlist entries.
//
// A non-positive ttl is treated as "don't cache": records carrying a zero or
// negative TTL are already expired, so storing them would only burn a cache slot
// on an entry HostForIP would immediately reject. Re-Put of an existing IP
// updates it in place (no growth).
func (c *NameCache) Put(host string, ip netip.Addr, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	host = normalizeHost(host)
	if host == "" {
		return
	}
	if !ip.IsValid() {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Updating an existing IP never grows the map, so only enforce the bound
	// when inserting a genuinely new key.
	if _, exists := c.entries[ip]; !exists && len(c.entries) >= maxNameCacheEntries {
		for k := range c.entries { // evict one arbitrary entry to stay bounded
			delete(c.entries, k)
			break
		}
	}
	c.entries[ip] = nameEntry{host: host, expiry: c.now().Add(ttl)}
	key := nameBinding{host: host, ip: ip}
	if _, exists := c.bindings[key]; !exists && len(c.bindings) >= maxNameCacheEntries {
		for k := range c.bindings {
			delete(c.bindings, k)
			break
		}
	}
	c.bindings[key] = c.now().Add(ttl)
}

// HostMatchesIP reports whether an unexpired DNS answer observed by the
// mediator bound host to ip. Unlike HostForIP, it preserves concurrent names
// that legitimately share an address and is therefore suitable for checking a
// guest-asserted HTTP Host or TLS SNI against the destination actually dialed.
func (c *NameCache) HostMatchesIP(host string, ip netip.Addr) bool {
	host = normalizeHost(host)
	if host == "" || !ip.IsValid() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := nameBinding{host: host, ip: ip}
	expiry, ok := c.bindings[key]
	if !ok {
		return false
	}
	if !c.now().Before(expiry) {
		delete(c.bindings, key)
		return false
	}
	return true
}

// HostForIP returns the cached hostname for ip if present and not expired.
// Expired entries are lazily deleted and reported as absent.
func (c *NameCache) HostForIP(ip netip.Addr) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[ip]
	if !ok {
		return "", false
	}
	if !c.now().Before(e.expiry) { // now >= expiry: expired
		delete(c.entries, ip)
		return "", false
	}
	return e.host, true
}

// size returns the number of entries currently held. It does not expire
// entries; it is used by tests to assert bounded growth.
func (c *NameCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
