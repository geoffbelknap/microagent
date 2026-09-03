package egress

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// SwapConfigFile is the on-disk credential-swap configuration: a map of named
// swap entries keyed by an operator-chosen identifier (the map key is copied
// into each entry's unexported Name for diagnostics/audit).
type SwapConfigFile struct {
	Swaps map[string]SwapEntry `yaml:"swaps"`
}

// SwapEntry describes one credential-swap rule: which destination hosts it
// applies to and how the real credential is acquired and injected. THIS phase
// only loads and indexes entries — no acquire or injection happens yet — so the
// acquisition fields (token_url, client_*_ref, scopes, signing_key_ref, claims,
// algorithm, token_response_field, token_ttl_seconds) are carried for later
// phases.
type SwapEntry struct {
	// Type selects the credential-acquisition strategy. Must be one of
	// validSwapTypes ("static", "oauth2-cc", "jwt-bearer").
	Type string `yaml:"type"`
	// Domains lists destination hosts this entry applies to. A leading-dot
	// entry (".sub.example.com") matches that apex and any subdomain; an
	// entry without a leading dot is an exact host match.
	Domains []string `yaml:"domains"`
	// Header is the request header the credential is injected into (e.g.
	// "Authorization"). Used by a later injection phase.
	Header string `yaml:"header"`
	// Format is the header value template, with "{key}" replaced by the
	// acquired credential (e.g. "Bearer {key}"). Used by a later phase.
	Format string `yaml:"format"`
	// KeyRef references the static credential source (e.g. "env:EXAMPLE_KEY")
	// for the "static" type.
	KeyRef string `yaml:"key_ref"`

	// --- Acquisition fields for non-static types (later phases). ---

	// TokenURL is the OAuth2 token endpoint ("oauth2-cc").
	TokenURL string `yaml:"token_url"`
	// ClientIDRef references the OAuth2 client id ("oauth2-cc").
	ClientIDRef string `yaml:"client_id_ref"`
	// ClientSecretRef references the OAuth2 client secret ("oauth2-cc").
	ClientSecretRef string `yaml:"client_secret_ref"`
	// Scopes are the OAuth2 scopes requested ("oauth2-cc").
	Scopes []string `yaml:"scopes"`
	// SigningKeyRef references the JWT signing key ("jwt-bearer").
	SigningKeyRef string `yaml:"signing_key_ref"`
	// Claims are the JWT claims to sign ("jwt-bearer").
	Claims map[string]string `yaml:"claims"`
	// Algorithm is the JWT signing algorithm ("jwt-bearer").
	Algorithm string `yaml:"algorithm"`
	// TokenResponseField names the field in the token response holding the
	// credential ("oauth2-cc").
	TokenResponseField string `yaml:"token_response_field"`
	// TokenTTLSeconds bounds how long an acquired token is cached.
	TokenTTLSeconds int `yaml:"token_ttl_seconds"`

	// Name is the map key the entry was declared under, copied in by
	// LoadSwapTable for diagnostics/audit. Not read from YAML.
	Name string `yaml:"-"`
}

// validSwapTypes is the closed set of accepted swap types. An entry with any
// other type is rejected at load so misconfiguration fails closed.
var validSwapTypes = map[string]struct{}{
	"static":     {},
	"oauth2-cc":  {},
	"jwt-bearer": {},
}

// SwapTable is a host-indexed view of the loaded swap entries. Matching mirrors
// Policy: case-insensitive, exact-then-suffix. Suffix keys keep their leading
// dot. An entry may appear in both maps if it lists both exact and dotted
// domains; the same SwapEntry value is stored under each key.
type SwapTable struct {
	exact  map[string]SwapEntry
	suffix map[string]SwapEntry
}

// LoadSwapTable parses the credential-swap YAML, validates each entry, and
// indexes it by destination host. It fails closed: any entry with an unknown
// type, empty domains, or a domain that normalizes to empty is an error so a
// later phase never injects against a misconfigured table.
func LoadSwapTable(data []byte) (*SwapTable, error) {
	var cfg SwapConfigFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("egress: parse swap config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("egress: parse swap config: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("egress: parse swap config: %w", err)
	}
	tbl := &SwapTable{exact: map[string]SwapEntry{}, suffix: map[string]SwapEntry{}}
	for name, entry := range cfg.Swaps {
		entry.Name = name
		if err := validateSwapEntry(entry); err != nil {
			return nil, err
		}
		if len(entry.Domains) == 0 {
			return nil, fmt.Errorf("egress: swap %q: no domains", name)
		}
		for _, raw := range entry.Domains {
			leadingDot := strings.HasPrefix(strings.TrimSpace(raw), ".")
			h := normalizeHost(raw)
			if h == "" {
				return nil, fmt.Errorf("egress: swap %q: empty domain %q", name, raw)
			}
			if leadingDot {
				// normalizeHost strips nothing from the leading dot; keep it so
				// Match's suffix walk works on ".sub.example.com".
				tbl.suffix["."+strings.TrimPrefix(h, ".")] = entry
				continue
			}
			tbl.exact[h] = entry
		}
	}
	return tbl, nil
}

func validateSwapEntry(entry SwapEntry) error {
	if _, ok := validSwapTypes[entry.Type]; !ok {
		return fmt.Errorf("egress: swap %q: unknown type %q", entry.Name, entry.Type)
	}
	if entry.TokenTTLSeconds < 0 {
		return fmt.Errorf("egress: swap %q: token_ttl_seconds must not be negative", entry.Name)
	}
	switch entry.Type {
	case "static":
		if strings.TrimSpace(entry.KeyRef) == "" {
			return fmt.Errorf("egress: swap %q: static strategy requires key_ref", entry.Name)
		}
	case "oauth2-cc":
		if err := validateOAuth2Entry(entry); err != nil {
			return err
		}
	case "jwt-bearer":
		if strings.TrimSpace(entry.SigningKeyRef) == "" {
			return fmt.Errorf("egress: swap %q: jwt-bearer strategy requires signing_key_ref", entry.Name)
		}
		if entry.Algorithm != "" && entry.Algorithm != "RS256" {
			return fmt.Errorf("egress: swap %q: unsupported JWT algorithm %q", entry.Name, entry.Algorithm)
		}
	}
	return nil
}

func validateOAuth2Entry(entry SwapEntry) error {
	if strings.TrimSpace(entry.ClientIDRef) == "" {
		return fmt.Errorf("egress: swap %q: oauth2-cc strategy requires client_id_ref", entry.Name)
	}
	if strings.TrimSpace(entry.ClientSecretRef) == "" {
		return fmt.Errorf("egress: swap %q: oauth2-cc strategy requires client_secret_ref", entry.Name)
	}
	tokenURL := strings.TrimSpace(entry.TokenURL)
	if tokenURL == "" {
		return fmt.Errorf("egress: swap %q: oauth2-cc strategy requires token_url", entry.Name)
	}
	if tokenURL != entry.TokenURL {
		return fmt.Errorf("egress: swap %q: token_url must not contain surrounding whitespace", entry.Name)
	}
	parsed, err := url.Parse(tokenURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" {
		return fmt.Errorf("egress: swap %q: invalid token_url", entry.Name)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("egress: swap %q: token_url must not contain userinfo or a fragment", entry.Name)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		ip := net.ParseIP(host)
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("egress: swap %q: token_url must use HTTPS except for loopback endpoints", entry.Name)
	default:
		return fmt.Errorf("egress: swap %q: token_url must use HTTPS", entry.Name)
	}
}

// Match returns the swap entry for host, if any. Lookup is case-insensitive
// (via normalizeHost) and exact-then-suffix: an exact host wins, otherwise a
// suffix entry (".sub.example.com") matches that apex or any subdomain by the
// parent-domain walk h == s[1:] || strings.HasSuffix(h, s).
func (t *SwapTable) Match(host string) (SwapEntry, bool) {
	host = normalizeHost(host)
	if host == "" {
		return SwapEntry{}, false
	}
	if e, ok := t.exact[host]; ok {
		return e, true
	}
	for s, e := range t.suffix {
		if host == s[1:] || strings.HasSuffix(host, s) {
			return e, true
		}
	}
	return SwapEntry{}, false
}
