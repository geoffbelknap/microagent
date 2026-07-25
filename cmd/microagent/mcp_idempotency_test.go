package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPIdempotencyStoreReplaysIdenticalRequest(t *testing.T) {
	store := newMCPIdempotencyStore(time.Minute, 8)
	args := mcpIdempotencyTestArgs("demo", "key-1", "agent-a")
	var calls int
	execute := func() (map[string]any, error) {
		calls++
		return map[string]any{"ok": true, "result": map[string]any{"call": calls}}, nil
	}

	first, err, replay := store.Do(context.Background(), "workspace.exec", args, execute)
	if err != nil || replay {
		t.Fatalf("first call: replay=%v err=%v", replay, err)
	}
	second, err, replay := store.Do(context.Background(), "workspace.exec", args, execute)
	if err != nil || !replay {
		t.Fatalf("second call: replay=%v err=%v", replay, err)
	}
	if calls != 1 {
		t.Fatalf("execute calls = %d, want 1", calls)
	}
	if first["result"].(map[string]any)["call"] != second["result"].(map[string]any)["call"] {
		t.Fatalf("replayed result differs: first=%#v second=%#v", first, second)
	}
}

func TestMCPIdempotencyStoreRejectsDifferentArguments(t *testing.T) {
	store := newMCPIdempotencyStore(time.Minute, 8)
	firstArgs := mcpIdempotencyTestArgs("demo-a", "key-1", "agent-a")
	secondArgs := mcpIdempotencyTestArgs("demo-b", "key-1", "agent-a")
	if _, err, _ := store.Do(context.Background(), "workspace.exec", firstArgs, mcpIdempotencyTestResult); err != nil {
		t.Fatalf("first call: %v", err)
	}

	called := false
	_, err, replay := store.Do(context.Background(), "workspace.exec", secondArgs, func() (map[string]any, error) {
		called = true
		return mcpIdempotencyTestResult()
	})
	var conflict mcpIdempotencyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %T %v, want mcpIdempotencyConflictError", err, err)
	}
	if replay {
		t.Fatal("argument conflict reported as replay")
	}
	if called {
		t.Fatal("conflicting request executed")
	}
	mapped := mcpStructuredErrorFor(err)
	if mapped.Kind != errorKindConflict || mapped.Retryable {
		t.Fatalf("structured error = %#v", mapped)
	}
}

func TestMCPIdempotencyStoreSeparatesPrincipals(t *testing.T) {
	store := newMCPIdempotencyStore(time.Minute, 8)
	var calls int
	execute := func() (map[string]any, error) {
		calls++
		return mcpIdempotencyTestResult()
	}
	for _, principal := range []string{"agent-a", "agent-b"} {
		if _, err, replay := store.Do(
			context.Background(),
			"workspace.exec",
			mcpIdempotencyTestArgs("demo", "shared-key", principal),
			execute,
		); err != nil || replay {
			t.Fatalf("principal %s: replay=%v err=%v", principal, replay, err)
		}
	}
	if calls != 2 {
		t.Fatalf("execute calls = %d, want 2", calls)
	}
}

func TestMCPIdempotencyStoreSingleFlightsConcurrentCalls(t *testing.T) {
	store := newMCPIdempotencyStore(time.Minute, 8)
	args := mcpIdempotencyTestArgs("demo", "key-1", "agent-a")
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	var calls atomic.Int32
	execute := func() (map[string]any, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return mcpIdempotencyTestResult()
	}

	type result struct {
		err    error
		replay bool
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			ready.Done()
			<-start
			_, err, replay := store.Do(context.Background(), "workspace.exec", args, execute)
			results <- result{err: err, replay: replay}
		}()
	}
	ready.Wait()
	close(start)
	<-entered
	select {
	case <-entered:
		t.Fatal("concurrent identical request executed twice")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent errors: first=%v second=%v", first.err, second.err)
	}
	if first.replay == second.replay {
		t.Fatalf("replay flags = %v and %v, want one original and one replay", first.replay, second.replay)
	}
	if calls.Load() != 1 {
		t.Fatalf("execute calls = %d, want 1", calls.Load())
	}
}

func TestMCPIdempotencyStoreExpiresEntries(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := newMCPIdempotencyStore(time.Minute, 8)
	store.now = func() time.Time { return now }
	args := mcpIdempotencyTestArgs("demo", "key-1", "agent-a")
	var calls int
	execute := func() (map[string]any, error) {
		calls++
		return mcpIdempotencyTestResult()
	}

	if _, err, replay := store.Do(context.Background(), "workspace.exec", args, execute); err != nil || replay {
		t.Fatalf("first call: replay=%v err=%v", replay, err)
	}
	now = now.Add(2 * time.Minute)
	if _, err, replay := store.Do(context.Background(), "workspace.exec", args, execute); err != nil || replay {
		t.Fatalf("expired call: replay=%v err=%v", replay, err)
	}
	if calls != 2 {
		t.Fatalf("execute calls = %d, want 2", calls)
	}
}

func TestMCPIdempotencyStoreEvictsOldestCompletedEntry(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := newMCPIdempotencyStore(time.Hour, 2)
	store.now = func() time.Time { return now }
	var calls int
	execute := func() (map[string]any, error) {
		calls++
		return mcpIdempotencyTestResult()
	}

	for _, key := range []string{"key-1", "key-2", "key-3"} {
		args := mcpIdempotencyTestArgs("demo", key, "agent-a")
		if _, err, replay := store.Do(context.Background(), "workspace.exec", args, execute); err != nil || replay {
			t.Fatalf("%s: replay=%v err=%v", key, replay, err)
		}
		now = now.Add(time.Second)
	}
	if len(store.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(store.entries))
	}
	if _, err, replay := store.Do(
		context.Background(),
		"workspace.exec",
		mcpIdempotencyTestArgs("demo", "key-1", "agent-a"),
		execute,
	); err != nil || replay {
		t.Fatalf("evicted key: replay=%v err=%v", replay, err)
	}
	if calls != 4 {
		t.Fatalf("execute calls = %d, want 4", calls)
	}
}

func TestMCPIdempotencyStoreRejectsCapacityWhenAllEntriesAreRunning(t *testing.T) {
	store := newMCPIdempotencyStore(time.Hour, 1)
	release := make(chan struct{})
	entered := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err, _ := store.Do(
			context.Background(),
			"workspace.exec",
			mcpIdempotencyTestArgs("demo", "key-1", "agent-a"),
			func() (map[string]any, error) {
				close(entered)
				<-release
				return mcpIdempotencyTestResult()
			},
		)
		firstDone <- err
	}()
	<-entered

	_, err, replay := store.Do(
		context.Background(),
		"workspace.exec",
		mcpIdempotencyTestArgs("demo", "key-2", "agent-a"),
		mcpIdempotencyTestResult,
	)
	var capacity mcpIdempotencyCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("error = %T %v, want mcpIdempotencyCapacityError", err, err)
	}
	if replay {
		t.Fatal("capacity failure reported as replay")
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %d, want hard bound of 1", len(store.entries))
	}
	mapped := mcpStructuredErrorFor(err)
	if mapped.Kind != errorKindResourceExhausted || !mapped.Retryable {
		t.Fatalf("structured error = %#v", mapped)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first call: %v", err)
	}
}

func TestMCPIdempotencyIdentityIgnoresCorrelationID(t *testing.T) {
	first := mcpIdempotencyTestArgs("demo", "key-1", "agent-a")
	first["principal"].(map[string]any)["correlation_id"] = "request-1"
	second := mcpIdempotencyTestArgs("demo", "key-1", "agent-a")
	second["principal"].(map[string]any)["correlation_id"] = "request-2"

	firstScope, firstDigest, err := mcpIdempotencyIdentity("workspace.exec", first)
	if err != nil {
		t.Fatal(err)
	}
	secondScope, secondDigest, err := mcpIdempotencyIdentity("workspace.exec", second)
	if err != nil {
		t.Fatal(err)
	}
	if firstScope != secondScope || firstDigest != secondDigest {
		t.Fatalf("correlation ID changed identity: first=(%s,%s) second=(%s,%s)", firstScope, firstDigest, secondScope, secondDigest)
	}
}

func mcpIdempotencyTestArgs(name, key, principal string) map[string]any {
	return map[string]any{
		"name":            name,
		"idempotency_key": key,
		"principal": map[string]any{
			"workload_identity": principal,
			"purpose":           "test",
		},
	}
}

func mcpIdempotencyTestResult() (map[string]any, error) {
	return map[string]any{
		"ok":     true,
		"result": map[string]any{"workspace": "demo"},
		"meta":   map[string]any{"timing_ms": int64(1)},
	}, nil
}
