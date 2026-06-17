package egress

import (
	"context"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/secret"
)

// TestKeyResolver_ResolvesEnvRef verifies a KeyResolver over the standard
// secret registry resolves a known env-scheme ref to its real value.
func TestKeyResolver_ResolvesEnvRef(t *testing.T) {
	t.Setenv("EGRESS_TEST_KEY", "REALSECRET")
	reg := secret.DefaultRegistry(nil, nil) // nil getenv -> os.Getenv; nil warn -> dropped
	kr := &KeyResolver{Registry: reg}

	got, err := kr.Resolve(context.Background(), "env:EGRESS_TEST_KEY")
	if err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}
	if string(got) != "REALSECRET" {
		t.Fatalf("Resolve: got %q, want %q", got, "REALSECRET")
	}
}

// TestKeyResolver_FailsClosedOnMissingRef verifies an env ref whose variable is
// unset resolves to an empty value, which the registry rejects as an error — the
// resolver must never hand back an empty credential.
func TestKeyResolver_FailsClosedOnMissingRef(t *testing.T) {
	reg := secret.DefaultRegistry(nil, nil)
	kr := &KeyResolver{Registry: reg}

	got, err := kr.Resolve(context.Background(), "env:EGRESS_TEST_DEFINITELY_UNSET")
	if err == nil {
		t.Fatalf("Resolve: expected error for unset env var, got value %q", got)
	}
	if got != nil {
		t.Fatalf("Resolve: expected nil value on error, got %q", got)
	}
}

// TestKeyResolver_FailsClosedOnUnknownScheme verifies an unknown scheme errors
// rather than silently returning empty.
func TestKeyResolver_FailsClosedOnUnknownScheme(t *testing.T) {
	reg := secret.DefaultRegistry(nil, nil)
	kr := &KeyResolver{Registry: reg}

	if _, err := kr.Resolve(context.Background(), "bogus:whatever"); err == nil {
		t.Fatal("Resolve: expected error for unknown scheme, got nil")
	}
}

// TestKeyResolver_FailsClosedOnEmptyRef verifies an empty ref errors.
func TestKeyResolver_FailsClosedOnEmptyRef(t *testing.T) {
	reg := secret.DefaultRegistry(nil, nil)
	kr := &KeyResolver{Registry: reg}

	if _, err := kr.Resolve(context.Background(), ""); err == nil {
		t.Fatal("Resolve: expected error for empty ref, got nil")
	}
}

// TestKeyResolver_FailsClosedOnNilRegistry verifies a KeyResolver with no
// registry fails closed rather than panicking — defends the wiring seam.
func TestKeyResolver_FailsClosedOnNilRegistry(t *testing.T) {
	kr := &KeyResolver{}
	if _, err := kr.Resolve(context.Background(), "env:ANYTHING"); err == nil {
		t.Fatal("Resolve: expected error for nil registry, got nil")
	}
}

// TestKeyResolver_SatisfiesResolverInterface is a compile-time assertion that
// *KeyResolver satisfies the resolver interface the Swapper depends on.
func TestKeyResolver_SatisfiesResolverInterface(t *testing.T) {
	var _ resolver = (*KeyResolver)(nil)
}
