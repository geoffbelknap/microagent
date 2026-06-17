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

func TestNewPolicyRejectsDotOnlyEntry(t *testing.T) {
	for _, e := range []string{".", " . ", ".."} {
		if _, err := NewPolicy([]string{e}); err == nil {
			t.Errorf("NewPolicy(%q): expected error", e)
		}
	}
}

func TestPolicyNormalizesTrailingDot(t *testing.T) {
	// entry has a trailing dot; lookup host plain — and vice versa
	p, err := NewPolicy([]string{"api.github.com.", ".example.com."})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	for _, h := range []string{"api.github.com", "api.github.com.", "a.example.com", "a.example.com."} {
		if !p.AllowHost(h).Allow {
			t.Errorf("AllowHost(%q) = deny, want allow", h)
		}
	}
}

func TestPolicyDeduplicatesSuffixEntries(t *testing.T) {
	p, err := NewPolicy([]string{".example.com", ".example.com", ".EXAMPLE.com"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	if len(p.suffix) != 1 {
		t.Fatalf("suffix set size = %d, want 1 (deduped)", len(p.suffix))
	}
	if !p.AllowHost("a.example.com").Allow {
		t.Error("AllowHost(a.example.com) = deny, want allow")
	}
}
