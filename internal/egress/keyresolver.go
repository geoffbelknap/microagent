package egress

import (
	"context"
	"errors"

	"github.com/geoffbelknap/microagent/pkg/secret"
)

// KeyResolver dereferences a scheme-prefixed secret reference (e.g.
// "env:API_KEY", "file:/run/keys/api", "vault:secret/data/app#api_key") to its
// raw bytes by delegating to microagent's standard secret registry. It is the
// real resolver wired onto the Swapper so a swap's key_ref resolves host-side
// using the same providers as the rest of microagent.
//
// Construct it with the same registry the rest of microagent uses (see
// NewKeyResolver) so swap refs resolve identically to `microagent secret check`
// and the secretxfer server.
//
// Fail-closed: an empty ref, a nil Registry, an unknown scheme, a malformed
// reference, or an empty resolved value all return an error — never an empty
// credential. The underlying registry never logs or persists secret material,
// and KeyResolver adds no logging of its own.
type KeyResolver struct {
	// Registry resolves the reference. A nil Registry fails closed on Resolve.
	Registry *secret.Registry
}

// NewKeyResolver builds a KeyResolver over the standard secret registry: env,
// file, dotenv (plaintext) and vault schemes, reading the process environment
// via os.Getenv and routing plaintext warnings to warn (nil drops them). This
// is the same construction used by `microagent secret check` and the secretxfer
// server, so swap refs resolve identically.
func NewKeyResolver(warn func(string)) *KeyResolver {
	return &KeyResolver{Registry: secret.DefaultRegistry(nil, warn)}
}

// Resolve dereferences ref to its raw secret bytes, delegating to the registry.
// It satisfies the resolver interface the Swapper depends on. Fail-closed: a nil
// Registry errors (a wiring gap must never silently disable swaps); the registry
// itself errors on unknown scheme, malformed ref, or empty value. On any error
// the returned value is nil.
func (kr *KeyResolver) Resolve(ctx context.Context, ref string) ([]byte, error) {
	if kr.Registry == nil {
		return nil, errors.New("egress: KeyResolver has no secret registry")
	}
	return kr.Registry.Resolve(ctx, ref)
}
