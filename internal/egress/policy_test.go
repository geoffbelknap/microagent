package egress

import "testing"

func TestPolicyDefaultDeny(t *testing.T) {
	p, err := NewPolicy([]string{"api.github.com", ".example.com"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	cases := []struct {
		host  string
		allow bool
	}{
		{"api.github.com", true},
		{"API.GitHub.com", true}, // case-insensitive
		{"example.com", true},    // dot-prefix matches apex
		{"a.example.com", true},  // and subdomain
		{"evil.com", false},      // default deny
		{"", false},              // empty host
	}
	for _, c := range cases {
		if got := p.AllowHost(c.host); got.Allow != c.allow {
			t.Errorf("AllowHost(%q).Allow = %v, want %v (reason %q)", c.host, got.Allow, c.allow, got.Reason)
		}
	}
}

func TestNewPolicyRejectsEmptyEntry(t *testing.T) {
	if _, err := NewPolicy([]string{"ok.com", "  "}); err == nil {
		t.Fatal("expected error for empty allowlist entry")
	}
}
