package secret

import (
	"context"
	"strings"
	"testing"
)

func TestCheckReportsSourceBytesAndNoValue(t *testing.T) {
	r := DefaultRegistry(func(k string) string {
		if k == "TOK" {
			return "abcdef"
		}
		return ""
	}, nil)
	res := r.Check(context.Background(), "API=env:TOK")
	if !res.OK {
		t.Fatalf("ok = false, error = %q", res.Error)
	}
	if res.Name != "API" {
		t.Fatalf("name = %q, want API", res.Name)
	}
	if res.Source != "env" {
		t.Fatalf("source = %q, want env", res.Source)
	}
	if res.Bytes != 6 {
		t.Fatalf("bytes = %d, want 6", res.Bytes)
	}
	if res.Warning == "" {
		t.Fatal("expected a plaintext warning for env scheme")
	}
}

func TestCheckNeverContainsValue(t *testing.T) {
	r := DefaultRegistry(func(k string) string {
		if k == "TOK" {
			return "super-secret-value"
		}
		return ""
	}, nil)
	res := r.Check(context.Background(), "API=env:TOK")
	blob := res.Name + res.Source + res.Warning + res.Error
	if strings.Contains(blob, "super-secret-value") {
		t.Fatal("CheckResult leaked the secret value")
	}
}

func TestCheckUnknownSchemeReportsNotOK(t *testing.T) {
	r := DefaultRegistry(func(string) string { return "" }, nil)
	res := r.Check(context.Background(), "API=bogus:x")
	if res.OK {
		t.Fatal("ok = true, want false for unknown scheme")
	}
	if res.Error == "" {
		t.Fatal("expected an error message")
	}
}

func TestCheckMalformedEntryReportsNotOK(t *testing.T) {
	r := DefaultRegistry(func(string) string { return "" }, nil)
	res := r.Check(context.Background(), "noequalshere")
	if res.OK {
		t.Fatal("ok = true, want false for entry without NAME=")
	}
}

func TestCheckSecureSchemeHasNoWarning(t *testing.T) {
	r := NewRegistry(nil)
	r.Register("stub", &stubProvider{value: []byte("v")})
	res := r.Check(context.Background(), "X=stub:ref")
	if !res.OK {
		t.Fatalf("ok = false, error = %q", res.Error)
	}
	if res.Warning != "" {
		t.Fatalf("warning = %q, want empty for secure scheme", res.Warning)
	}
}
