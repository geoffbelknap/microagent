package egress

import (
	"fmt"
	"sort"
	"strings"
)

// providerSwap is the built-in knowledge for one credential-swap provider: which
// egress host(s) the credential is for, how to inject it, and the conventional
// env var to read it from when the caller names no reference. It turns the
// verbose swap-config YAML into a one-word `--cred-swap <provider>`.
type providerSwap struct {
	hosts      []string // egress destinations; also auto-allowlisted
	header     string   // request header the credential is injected into
	format     string   // header value template; "{key}" -> the resolved secret
	defaultRef string   // key reference used when the caller gives none
}

// builtinProviders maps a provider name to its swap shape. All are API-key
// providers (the credential is a static bearer/header key), which is exactly the
// case credential swap protects: the guest never holds the key.
var builtinProviders = map[string]providerSwap{
	"openai":     {hosts: []string{"api.openai.com"}, header: "Authorization", format: "Bearer {key}", defaultRef: "env:OPENAI_API_KEY"},
	"anthropic":  {hosts: []string{"api.anthropic.com"}, header: "x-api-key", format: "{key}", defaultRef: "env:ANTHROPIC_API_KEY"},
	"gemini":     {hosts: []string{"generativelanguage.googleapis.com"}, header: "x-goog-api-key", format: "{key}", defaultRef: "env:GEMINI_API_KEY"},
	"groq":       {hosts: []string{"api.groq.com"}, header: "Authorization", format: "Bearer {key}", defaultRef: "env:GROQ_API_KEY"},
	"openrouter": {hosts: []string{"openrouter.ai"}, header: "Authorization", format: "Bearer {key}", defaultRef: "env:OPENROUTER_API_KEY"},
	"deepseek":   {hosts: []string{"api.deepseek.com"}, header: "Authorization", format: "Bearer {key}", defaultRef: "env:DEEPSEEK_API_KEY"},
}

// credRefSchemes are the secret-reference schemes a --cred-swap key reference may
// use. A reference (env:NAME / file:PATH / vault:PATH#field) keeps the secret out
// of the command line and shell history; a raw literal is rejected.
var credRefSchemes = map[string]struct{}{"env": {}, "file": {}, "vault": {}}

// ValidCredRef reports whether ref is a credential reference (scheme:rest with a
// known scheme), not a raw literal. Used to reject `--cred-swap p=sk-realkey`.
func ValidCredRef(ref string) bool {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || scheme == "" || rest == "" {
		return false
	}
	_, known := credRefSchemes[strings.ToLower(scheme)]
	return known
}

// KnownProviders returns the built-in provider names, sorted, for help text and
// error messages.
func KnownProviders() []string {
	names := make([]string, 0, len(builtinProviders))
	for name := range builtinProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ProviderSwapEntry resolves a built-in provider and an optional key reference
// into a ready static SwapEntry plus the host(s) to allowlist. An empty keyRef
// falls back to the provider's conventional env var. A non-empty keyRef must be
// a reference (env:/file:/vault:), never a literal — callers should validate
// with ValidCredRef first; this rejects a bad ref defensively too.
func ProviderSwapEntry(provider, keyRef string) (SwapEntry, []string, error) {
	p, ok := builtinProviders[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return SwapEntry{}, nil, fmt.Errorf("unknown cred-swap provider %q; known providers: %s", provider, strings.Join(KnownProviders(), ", "))
	}
	ref := strings.TrimSpace(keyRef)
	if ref == "" {
		ref = p.defaultRef
	} else if !ValidCredRef(ref) {
		return SwapEntry{}, nil, fmt.Errorf("cred-swap reference %q must be env:NAME, file:PATH, or vault:PATH (never a literal secret)", ref)
	}
	return SwapEntry{
		Type:    "static",
		Domains: append([]string(nil), p.hosts...),
		Header:  p.header,
		Format:  p.format,
		KeyRef:  ref,
	}, append([]string(nil), p.hosts...), nil
}
