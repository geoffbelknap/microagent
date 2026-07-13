package workspace

import (
	"regexp"
	"strings"
	"testing"
)

func TestRandomNameFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^run-[a-z]+-[a-z]+-[0-9a-z]{4}$`)
	for i := 0; i < 100; i++ {
		name := RandomName("run")
		if !pattern.MatchString(name) {
			t.Fatalf("RandomName produced %q, want prefix-adjective-noun-suffix", name)
		}
		if err := ValidateName(name); err != nil {
			t.Fatalf("RandomName produced invalid workspace name %q: %v", name, err)
		}
		if hostname := DefaultHostname(name); hostname != name {
			t.Fatalf("RandomName %q is not hostname-safe: DefaultHostname returned %q", name, hostname)
		}
	}
}

func TestRandomNamePrefix(t *testing.T) {
	if name := RandomName("dispatch"); !strings.HasPrefix(name, "dispatch-") {
		t.Fatalf("RandomName(\"dispatch\") = %q, want dispatch- prefix", name)
	}
}

func TestRandomNameCollisionSuffix(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		name := RandomName("run")
		if seen[name] {
			t.Fatalf("RandomName repeated %q within 1000 draws", name)
		}
		seen[name] = true
	}
}
