package workspace

import (
	"regexp"
	"strings"
	"testing"
)

// TestValidateNameBoundsTheCharset pins the name contract: names travel
// into state-dir paths, serial-log paths, guest hostnames, and shell
// commands, so anything outside the bounded shape is refused up front — an
// unexpanded glob ("m2*") must fail validation, not become a workspace.
func TestValidateNameBoundsTheCharset(t *testing.T) {
	valid := []string{
		"research", "m2-diag", "run-happy-crane-zd49", "A1", "x",
		"has.dot", "has_underscore", "0starts-with-digit",
		strings.Repeat("a", 63),
	}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{
		"", "   ", "m2*", "a b", "-leading-hyphen", ".hidden", "..",
		"../escape", `back\slash`, "sub/dir", "café", "semi;colon",
		"dollar$", "quo\"te", "new\nline",
		strings.Repeat("a", 64),
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
	}
}

// TestValidateNameReservesStateDirEntries pins the collision guard: a
// workspace's runtime directory is <state-dir>/<name>/, so the state
// directory's own infrastructure entries can never be workspace names.
func TestValidateNameReservesStateDirEntries(t *testing.T) {
	for _, name := range []string{
		"build", "host-workers", "images", "kernels", "models", "oci",
		"runners", "volumes", "workspaces",
	} {
		err := ValidateName(name)
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("ValidateName(%q) = %v, want reserved-name error", name, err)
		}
	}
}

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
