// Package secret resolves scheme-prefixed secret references to values held only
// in host process memory. microagent is a secret conduit, not a store: it never
// writes secret values to disk. Plaintext schemes (env/file/dotenv) read
// operator-owned files and warn on use; the vault scheme resolves from an
// external manager.
package secret

import (
	"context"
	"fmt"
	"strings"
)

// Resolver returns the secret value for a scheme-prefixed reference such as
// "vault:secret/data/app#api_key". The value is returned in memory and is never
// logged or persisted.
type Resolver interface {
	Resolve(ctx context.Context, ref string) ([]byte, error)
}

// Provider resolves the scheme-specific remainder of a reference (everything
// after "scheme:"). Implementations must fail closed: an unresolved secret is an
// error, never a silent empty value.
type Provider interface {
	Resolve(ctx context.Context, rest string) ([]byte, error)
	// Plaintext reports whether the provider reads operator-owned plaintext,
	// which triggers the not-for-production warning on every resolve.
	Plaintext() bool
}

// Registry maps a scheme to its Provider and dispatches references to it.
type Registry struct {
	providers map[string]Provider
	warn      func(string)
}

// NewRegistry returns an empty Registry. warn receives the plaintext warning
// text on every resolve of a plaintext scheme; if nil, warnings are dropped.
func NewRegistry(warn func(string)) *Registry {
	if warn == nil {
		warn = func(string) {}
	}
	return &Registry{providers: map[string]Provider{}, warn: warn}
}

// Register associates a scheme (e.g. "vault") with a provider. A later Register
// for the same scheme replaces the earlier one.
func (r *Registry) Register(scheme string, p Provider) {
	r.providers[scheme] = p
}

// Resolve dispatches ref to its provider and returns the value. Plaintext
// schemes emit the warning via the registry's warn sink. Unknown scheme,
// malformed reference, or an empty resolved value all fail closed.
func (r *Registry) Resolve(ctx context.Context, ref string) ([]byte, error) {
	value, _, warning, err := r.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if warning != "" {
		r.warn(warning)
	}
	return value, nil
}

// resolve performs dispatch without emitting warnings, returning the resolved
// scheme and any plaintext warning text so callers (Resolve, Check) decide how
// to surface it.
func (r *Registry) resolve(ctx context.Context, ref string) (value []byte, scheme, warning string, err error) {
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok || scheme == "" {
		return nil, "", "", fmt.Errorf("secret reference %q is missing a scheme (expected <scheme>:<ref>)", ref)
	}
	provider, ok := r.providers[scheme]
	if !ok {
		return nil, scheme, "", fmt.Errorf("unknown secret scheme %q", scheme)
	}
	value, err = provider.Resolve(ctx, rest)
	if err != nil {
		return nil, scheme, "", err
	}
	if len(value) == 0 {
		return nil, scheme, "", fmt.Errorf("secret %q resolved to an empty value", ref)
	}
	if provider.Plaintext() {
		warning = plaintextWarning(scheme)
	}
	return value, scheme, warning, nil
}

// ValidRef reports whether ref has the shape of a resolvable reference —
// "<scheme>:<rest>" with a registered scheme and a non-empty remainder —
// without resolving it. Callers use it to reject a pasted literal secret
// before it is ever processed as configuration.
func (r *Registry) ValidRef(ref string) bool {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || scheme == "" || rest == "" {
		return false
	}
	_, known := r.providers[scheme]
	return known
}

// plaintextWarning is the message emitted whenever a plaintext scheme is used.
func plaintextWarning(scheme string) string {
	return fmt.Sprintf("secret scheme %q is plaintext: not encrypted at rest, not for production", scheme)
}
