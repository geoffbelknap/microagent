package egress

import "testing"

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
