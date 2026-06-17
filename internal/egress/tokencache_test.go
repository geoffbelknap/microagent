package egress

import (
	"testing"
	"time"
)

// TestTokenCache_HitWithinTTLMissNearExpiry asserts the cache hits for a token
// comfortably inside its TTL and misses once the clock advances into the skew
// window before true expiry, so a fresh token is acquired before the live
// credential lapses mid-flight.
func TestTokenCache_HitWithinTTLMissNearExpiry(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	c := newTokenCache()
	c.now = func() time.Time { return now }

	// Token expires 120s out: well outside the 60s skew window -> hit.
	c.set("k", "tok", base.Add(120*time.Second))
	if v, ok := c.get("k"); !ok || v != "tok" {
		t.Fatalf("get within TTL = (%q,%v), want (tok,true)", v, ok)
	}

	// Advance to base+70s: 50s before expiry, inside the 60s skew window -> miss.
	now = base.Add(70 * time.Second)
	if v, ok := c.get("k"); ok {
		t.Fatalf("get near expiry = (%q,%v), want miss", v, ok)
	}
}
