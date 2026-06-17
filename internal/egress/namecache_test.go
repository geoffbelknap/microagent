package egress

import (
	"net/netip"
	"testing"
	"time"
)

func TestNameCachePutGet(t *testing.T) {
	c := NewNameCache()
	ip := netip.MustParseAddr("203.0.113.5")
	c.Put("API.Example.com", ip, time.Hour)

	host, ok := c.HostForIP(ip)
	if !ok {
		t.Fatalf("HostForIP(%v) ok=false, want true", ip)
	}
	if host != "api.example.com" {
		t.Fatalf("HostForIP(%v)=%q, want normalized %q", ip, host, "api.example.com")
	}

	if _, ok := c.HostForIP(netip.MustParseAddr("198.51.100.9")); ok {
		t.Fatalf("HostForIP for uncached IP ok=true, want false")
	}
}

func TestNameCacheExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := newNameCacheWithClock(func() time.Time { return now })
	ip := netip.MustParseAddr("203.0.113.7")
	c.Put("expired.example.com", ip, time.Minute)

	// Before expiry: present.
	if _, ok := c.HostForIP(ip); !ok {
		t.Fatalf("HostForIP before expiry ok=false, want true")
	}

	// Advance past TTL.
	now = now.Add(time.Minute + time.Second)
	if _, ok := c.HostForIP(ip); ok {
		t.Fatalf("HostForIP after expiry ok=true, want false")
	}

	// Lazily removed: a second lookup is still false and the entry is gone.
	if _, ok := c.HostForIP(ip); ok {
		t.Fatalf("HostForIP after lazy-expire ok=true, want false")
	}
	if got := c.size(); got != 0 {
		t.Fatalf("size after lazy-expire = %d, want 0", got)
	}
}

func TestNameCacheZeroTTLNotStored(t *testing.T) {
	c := NewNameCache()
	ip := netip.MustParseAddr("203.0.113.8")
	c.Put("zero.example.com", ip, 0)
	if _, ok := c.HostForIP(ip); ok {
		t.Fatalf("zero-ttl entry stored; want not stored")
	}
	c.Put("neg.example.com", ip, -time.Second)
	if _, ok := c.HostForIP(ip); ok {
		t.Fatalf("negative-ttl entry stored; want not stored")
	}
	if got := c.size(); got != 0 {
		t.Fatalf("size = %d, want 0", got)
	}
}

func TestNameCacheBounded(t *testing.T) {
	c := NewNameCache()
	for i := 0; i < maxNameCacheEntries+500; i++ {
		// Distinct IPv4 addresses derived from the loop index.
		ip := netip.AddrFrom4([4]byte{
			byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i),
		})
		c.Put("host.example.com", ip, time.Hour)
	}
	if got := c.size(); got > maxNameCacheEntries {
		t.Fatalf("size = %d, want <= %d", got, maxNameCacheEntries)
	}
}

func TestNameCacheReputUpdates(t *testing.T) {
	c := NewNameCache()
	ip := netip.MustParseAddr("203.0.113.9")
	c.Put("first.example.com", ip, time.Hour)
	c.Put("Second.Example.com", ip, time.Hour)

	host, ok := c.HostForIP(ip)
	if !ok {
		t.Fatalf("HostForIP ok=false, want true")
	}
	if host != "second.example.com" {
		t.Fatalf("HostForIP=%q, want latest normalized %q", host, "second.example.com")
	}
	if got := c.size(); got != 1 {
		t.Fatalf("size after re-put = %d, want 1 (no growth)", got)
	}
}
