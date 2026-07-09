package workspace

import (
	"strings"
	"testing"
)

// TestParseBrokerConfig is the shared parser both the CLI flags and the
// Agentfile agent.broker block route through, so the two surfaces validate and
// build a broker config identically.
func TestParseBrokerConfig(t *testing.T) {
	cfg, err := ParseBrokerConfig("https://api.example.com", "api=env:MY_TOKEN",
		[]string{"EXAMPLE_BASE_URL", "OTHER_BASE_URL=http://127.0.0.1:18888/v1"}, true)
	if err != nil {
		t.Fatalf("ParseBrokerConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("nil config for a valid declaration")
	}
	if cfg.Upstream != "https://api.example.com" {
		t.Fatalf("Upstream = %q", cfg.Upstream)
	}
	if cfg.Secret.Name != "api" || cfg.Secret.Ref != "env:MY_TOKEN" {
		t.Fatalf("Secret = %+v", cfg.Secret)
	}
	if !cfg.Proxy {
		t.Fatal("Proxy not set")
	}
	if cfg.BaseURLEnv["EXAMPLE_BASE_URL"] != "" {
		t.Fatalf("EXAMPLE_BASE_URL = %q, want empty (filled with broker URL later)", cfg.BaseURLEnv["EXAMPLE_BASE_URL"])
	}
	if cfg.BaseURLEnv["OTHER_BASE_URL"] != "http://127.0.0.1:18888/v1" {
		t.Fatalf("OTHER_BASE_URL = %q", cfg.BaseURLEnv["OTHER_BASE_URL"])
	}
}

func TestParseBrokerConfigNilWhenEmpty(t *testing.T) {
	cfg, err := ParseBrokerConfig("", "", nil, false)
	if err != nil {
		t.Fatalf("ParseBrokerConfig: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for an empty declaration, got %+v", cfg)
	}
}

func TestParseBrokerConfigValidation(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
		secret   string
		env      []string
		proxy    bool
		wantErr  string
	}{
		{"upstream without secret", "https://api.example.com", "", nil, false, "together"},
		{"secret without upstream", "", "api=env:MY_TOKEN", nil, false, "together"},
		{"env without broker", "", "", []string{"X_BASE_URL"}, false, "require"},
		{"proxy without broker", "", "", nil, true, "require"},
		{"literal secret", "https://api.example.com", "api=sk-real", nil, false, "literal"},
		{"malformed secret", "https://api.example.com", "no-equals", nil, false, "NAME="},
		{"invalid secret name", "https://api.example.com", "1bad=env:X", nil, false, "name"},
		{"invalid env key", "https://api.example.com", "api=env:MY_TOKEN", []string{"1BAD=x"}, false, "env"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseBrokerConfig(c.upstream, c.secret, c.env, c.proxy)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, c.wantErr)
			}
		})
	}
}
