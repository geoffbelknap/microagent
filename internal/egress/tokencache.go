package egress

import (
	"sync"
	"time"
)

// tokenSkew is how far ahead of an entry's true expiry it is treated as
// already expired. A cached token within this window of expiring is reported as
// a miss so a fresh one is acquired before the live credential lapses mid-flight.
const tokenSkew = 60 * time.Second

// cachedToken is one acquired credential value with its absolute expiry.
type cachedToken struct {
	value     string
	expiresAt time.Time
}

// tokenCache caches acquired credential values keyed by an acquisition-strategy
// identifier. It is safe for concurrent use. The static strategy does not use
// the cache (a static key never expires and is cheap to resolve); it exists for
// the OAuth2/JWT strategies of later phases, but is constructed now so the
// Swapper type is complete. now is injectable for deterministic tests.
type tokenCache struct {
	mu  sync.Mutex
	m   map[string]cachedToken
	now func() time.Time
}

// newTokenCache returns an empty cache using the real clock.
func newTokenCache() *tokenCache {
	return &tokenCache{m: map[string]cachedToken{}, now: time.Now}
}

// get returns the cached value for key, or ("", false) when absent or within
// tokenSkew of expiry. A near-expiry entry is a miss so the caller re-acquires.
func (c *tokenCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.m[key]
	if !ok {
		return "", false
	}
	if !c.now().Add(tokenSkew).Before(t.expiresAt) {
		return "", false
	}
	return t.value, true
}

// set stores value under key with the given absolute expiry.
func (c *tokenCache) set(key, value string, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = cachedToken{value: value, expiresAt: expiresAt}
}
