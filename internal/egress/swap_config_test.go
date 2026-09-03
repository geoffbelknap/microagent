package egress

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadSwapConfig_IndexesByDomain(t *testing.T) {
	yml := []byte(`swaps:
  example:
    type: static
    domains: ["api.example.com", ".sub.example.com"]
    header: Authorization
    format: "Bearer {key}"
    key_ref: "env:EXAMPLE_KEY"
`)
	tbl, err := LoadSwapTable(yml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if e, ok := tbl.Match("api.example.com"); !ok || e.Type != "static" || e.KeyRef != "env:EXAMPLE_KEY" {
		t.Fatalf("exact match failed: %+v ok=%v", e, ok)
	}
	if _, ok := tbl.Match("a.b.sub.example.com"); !ok {
		t.Fatalf("suffix match failed")
	}
	if _, ok := tbl.Match("other.com"); ok {
		t.Fatalf("unexpected match for other.com")
	}
}

func TestLoadSwapConfig_RejectsUnknownType(t *testing.T) {
	if _, err := LoadSwapTable([]byte("swaps:\n  x:\n    type: bogus\n    domains: [\"h\"]\n")); err == nil {
		t.Fatal("expected error for unknown swap type")
	}
}

func TestLoadSwapConfig_RejectsUnknownField(t *testing.T) {
	_, err := LoadSwapTable([]byte(`swaps:
  example:
    type: static
    domains: [api.example.com]
    key_ref: env:EXAMPLE_KEY
    key_reff: env:TYPO
`))
	if err == nil || !strings.Contains(err.Error(), "key_reff") {
		t.Fatalf("LoadSwapTable error = %v, want unknown-field rejection", err)
	}
}

func TestLoadSwapConfig_ValidatesStrategyFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "static key", body: "type: static"},
		{name: "oauth token URL", body: "type: oauth2-cc\n    client_id_ref: env:CID\n    client_secret_ref: env:CSEC"},
		{name: "oauth client ID", body: "type: oauth2-cc\n    token_url: https://auth.example.com/token\n    client_secret_ref: env:CSEC"},
		{name: "oauth client secret", body: "type: oauth2-cc\n    token_url: https://auth.example.com/token\n    client_id_ref: env:CID"},
		{name: "JWT signing key", body: "type: jwt-bearer"},
		{name: "JWT algorithm", body: "type: jwt-bearer\n    signing_key_ref: env:KEY\n    algorithm: HS256"},
		{name: "negative token TTL", body: "type: static\n    key_ref: env:KEY\n    token_ttl_seconds: -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yml := fmt.Sprintf("swaps:\n  example:\n    %s\n    domains: [api.example.com]\n", tt.body)
			if _, err := LoadSwapTable([]byte(yml)); err == nil {
				t.Fatal("LoadSwapTable accepted an incomplete or unsupported strategy")
			}
		})
	}
}

func TestLoadSwapConfig_ValidatesOAuthTokenURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "HTTPS", url: "https://auth.example.com/token"},
		{name: "IPv4 loopback HTTP", url: "http://127.0.0.1:8080/token"},
		{name: "IPv6 loopback HTTP", url: "http://[::1]:8080/token"},
		{name: "localhost HTTP", url: "http://localhost:8080/token"},
		{name: "remote HTTP", url: "http://auth.example.com/token", wantErr: true},
		{name: "userinfo", url: "https://user:password@auth.example.com/token", wantErr: true},
		{name: "fragment", url: "https://auth.example.com/token#secret", wantErr: true},
		{name: "unsupported scheme", url: "ftp://auth.example.com/token", wantErr: true},
		{name: "relative URL", url: "/token", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yml := fmt.Sprintf(`swaps:
  example:
    type: oauth2-cc
    domains: [api.example.com]
    token_url: %q
    client_id_ref: env:CID
    client_secret_ref: env:CSEC
`, tt.url)
			_, err := LoadSwapTable([]byte(yml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadSwapTable error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
