package egress

import "testing"

func TestProviderSwapEntryDefaults(t *testing.T) {
	entry, hosts, err := ProviderSwapEntry("openai", "")
	if err != nil {
		t.Fatalf("openai: %v", err)
	}
	if entry.Type != "static" {
		t.Errorf("type = %q, want static", entry.Type)
	}
	if entry.Header != "Authorization" || entry.Format != "Bearer {key}" {
		t.Errorf("openai header/format = %q / %q", entry.Header, entry.Format)
	}
	if entry.KeyRef != "env:OPENAI_API_KEY" {
		t.Errorf("default key_ref = %q, want env:OPENAI_API_KEY", entry.KeyRef)
	}
	if len(hosts) != 1 || hosts[0] != "api.openai.com" {
		t.Errorf("hosts = %v, want [api.openai.com]", hosts)
	}
}

func TestProviderSwapEntryAnthropicUsesXAPIKey(t *testing.T) {
	entry, _, err := ProviderSwapEntry("anthropic", "env:MY_KEY")
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if entry.Header != "x-api-key" || entry.Format != "{key}" {
		t.Errorf("anthropic injects %q: %q (want x-api-key: {key})", entry.Header, entry.Format)
	}
	if entry.KeyRef != "env:MY_KEY" {
		t.Errorf("explicit key_ref = %q, want env:MY_KEY", entry.KeyRef)
	}
}

func TestProviderSwapEntryUnknownProvider(t *testing.T) {
	if _, _, err := ProviderSwapEntry("nope", ""); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestProviderSwapEntryRejectsLiteral(t *testing.T) {
	// A raw secret value (no scheme) must be rejected so it can't reach history.
	if _, _, err := ProviderSwapEntry("openai", "sk-proj-totally-real-key"); err == nil {
		t.Fatal("expected a literal secret to be rejected")
	}
}

func TestValidCredRef(t *testing.T) {
	for _, ok := range []string{"env:OPENAI_API_KEY", "file:/run/secrets/key", "vault:secret/data/app#key"} {
		if !ValidCredRef(ok) {
			t.Errorf("ValidCredRef(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"sk-realkey", "OPENAI_API_KEY", "env:", ":foo", "bogus:x", ""} {
		if ValidCredRef(bad) {
			t.Errorf("ValidCredRef(%q) = true, want false", bad)
		}
	}
}
