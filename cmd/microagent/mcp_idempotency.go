package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	mcpIdempotencyTTL        = 15 * time.Minute
	mcpIdempotencyMaxEntries = 1024
)

var mcpIdempotencyCache = newMCPIdempotencyStore(mcpIdempotencyTTL, mcpIdempotencyMaxEntries)

type mcpIdempotencyConflictError struct {
	Tool string
	Key  string
}

func (e mcpIdempotencyConflictError) Error() string {
	return fmt.Sprintf("idempotency conflict for %s: key %q was already used with different arguments", e.Tool, e.Key)
}

type mcpIdempotencyCapacityError struct {
	Limit int
}

func (e mcpIdempotencyCapacityError) Error() string {
	return fmt.Sprintf("idempotency capacity exhausted: %d requests are still in flight", e.Limit)
}

type mcpIdempotencyEntry struct {
	digest      string
	envelope    map[string]any
	err         error
	ready       chan struct{}
	running     bool
	createdAt   time.Time
	completedAt time.Time
	expiresAt   time.Time
}

type mcpIdempotencyStore struct {
	mu         sync.Mutex
	entries    map[string]*mcpIdempotencyEntry
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

func newMCPIdempotencyStore(ttl time.Duration, maxEntries int) *mcpIdempotencyStore {
	return &mcpIdempotencyStore{
		entries:    map[string]*mcpIdempotencyEntry{},
		ttl:        ttl,
		maxEntries: maxEntries,
		now:        time.Now,
	}
}

func (s *mcpIdempotencyStore) Do(
	ctx context.Context,
	name string,
	args map[string]any,
	execute func() (map[string]any, error),
) (map[string]any, error, bool) {
	key := mcpIdempotencyCacheKey(name, args)
	if key == "" {
		envelope, err := execute()
		return envelope, err, false
	}
	scope, digest, err := mcpIdempotencyIdentity(name, args)
	if err != nil {
		return nil, err, false
	}
	cacheKey := name + "\x00" + scope + "\x00" + key

	s.mu.Lock()
	now := s.now()
	s.pruneExpiredLocked(now)
	if entry, ok := s.entries[cacheKey]; ok {
		if entry.digest != digest {
			s.mu.Unlock()
			return nil, mcpIdempotencyConflictError{Tool: name, Key: key}, false
		}
		ready := entry.ready
		s.mu.Unlock()
		select {
		case <-ready:
			return cloneMCPMap(entry.envelope), entry.err, true
		case <-ctx.Done():
			return nil, ctx.Err(), false
		}
	}
	if !s.makeRoomLocked() {
		s.mu.Unlock()
		return nil, mcpIdempotencyCapacityError{Limit: s.maxEntries}, false
	}
	entry := &mcpIdempotencyEntry{
		digest:    digest,
		ready:     make(chan struct{}),
		running:   true,
		createdAt: now,
	}
	s.entries[cacheKey] = entry
	s.mu.Unlock()

	envelope, executeErr := execute()

	s.mu.Lock()
	completedAt := s.now()
	entry.envelope = cloneMCPMap(envelope)
	entry.err = executeErr
	entry.running = false
	entry.completedAt = completedAt
	entry.expiresAt = completedAt.Add(s.ttl)
	close(entry.ready)
	s.mu.Unlock()
	return envelope, executeErr, false
}

func (s *mcpIdempotencyStore) pruneExpiredLocked(now time.Time) {
	for key, entry := range s.entries {
		if !entry.running && !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}

func (s *mcpIdempotencyStore) makeRoomLocked() bool {
	if s.maxEntries <= 0 {
		return false
	}
	for len(s.entries) >= s.maxEntries {
		var oldestKey string
		var oldest *mcpIdempotencyEntry
		for key, entry := range s.entries {
			if entry.running {
				continue
			}
			if oldest == nil || entry.completedAt.Before(oldest.completedAt) {
				oldestKey = key
				oldest = entry
			}
		}
		if oldest == nil {
			return false
		}
		delete(s.entries, oldestKey)
	}
	return true
}

func mcpIdempotencyIdentity(name string, args map[string]any) (string, string, error) {
	principal := principalContextArg(args)
	delete(principal, "correlation_id")

	arguments := make(map[string]any, len(args))
	for key, value := range args {
		switch key {
		case "idempotency_key", "principal":
			continue
		default:
			arguments[key] = value
		}
	}

	scope, err := canonicalMCPDigest(map[string]any{
		"tool":      name,
		"principal": principal,
	})
	if err != nil {
		return "", "", err
	}
	digest, err := canonicalMCPDigest(arguments)
	if err != nil {
		return "", "", err
	}
	return scope, digest, nil
}

func canonicalMCPDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize idempotency input: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func mcpIdempotencyCacheKey(name string, args map[string]any) string {
	key := stringArg(args, "idempotency_key")
	if key == "" || !mcpMutationTool(name) {
		return ""
	}
	return key
}

func principalContextArg(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	raw, ok := args["principal"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	for _, key := range []string{"workload_identity", "delegated_authority", "purpose", "correlation_id"} {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	return out
}
