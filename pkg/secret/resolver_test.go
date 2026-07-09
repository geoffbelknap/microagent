package secret

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubProvider returns a fixed value/error and reports its plaintext flag.
type stubProvider struct {
	value     []byte
	err       error
	plaintext bool
	gotRest   string
}

func (s *stubProvider) Resolve(_ context.Context, rest string) ([]byte, error) {
	s.gotRest = rest
	return s.value, s.err
}
func (s *stubProvider) Plaintext() bool { return s.plaintext }

func TestRegistryDispatchesToProviderByScheme(t *testing.T) {
	p := &stubProvider{value: []byte("hunter2")}
	r := NewRegistry(nil)
	r.Register("stub", p)

	got, err := r.Resolve(context.Background(), "stub:some/ref#field")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("value = %q, want %q", got, "hunter2")
	}
	if p.gotRest != "some/ref#field" {
		t.Fatalf("provider got rest %q, want %q", p.gotRest, "some/ref#field")
	}
}

func TestRegistryUnknownSchemeFailsClosed(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Resolve(context.Background(), "nope:x"); err == nil {
		t.Fatal("expected error for unknown scheme, got nil")
	}
}

func TestRegistryMissingSchemeFailsClosed(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Resolve(context.Background(), "no-colon-here"); err == nil {
		t.Fatal("expected error for reference without scheme, got nil")
	}
}

func TestRegistryEmptyValueFailsClosed(t *testing.T) {
	r := NewRegistry(nil)
	r.Register("stub", &stubProvider{value: nil})
	if _, err := r.Resolve(context.Background(), "stub:x"); err == nil {
		t.Fatal("expected error for empty resolved value, got nil")
	}
}

func TestRegistryProviderErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	r := NewRegistry(nil)
	r.Register("stub", &stubProvider{err: sentinel})
	if _, err := r.Resolve(context.Background(), "stub:x"); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped %v", err, sentinel)
	}
}

func TestRegistryValidRef(t *testing.T) {
	r := NewRegistry(nil)
	r.Register("stub", &stubProvider{})
	cases := []struct {
		ref  string
		want bool
	}{
		{"stub:some/ref#field", true},
		{" stub:padded ", true},
		{"nope:x", false},            // unregistered scheme
		{"sk-pasted-literal", false}, // no scheme at all
		{"stub:", false},             // empty remainder
		{":rest", false},             // empty scheme
		{"", false},
	}
	for _, c := range cases {
		if got := r.ValidRef(c.ref); got != c.want {
			t.Fatalf("ValidRef(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

func TestRegistryPlaintextEmitsWarning(t *testing.T) {
	var warned string
	r := NewRegistry(func(msg string) { warned = msg })
	r.Register("stub", &stubProvider{value: []byte("v"), plaintext: true})
	if _, err := r.Resolve(context.Background(), "stub:x"); err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if warned == "" {
		t.Fatal("expected a plaintext warning, got none")
	}
}

func TestRegistrySecureSchemeNoWarning(t *testing.T) {
	var warned string
	r := NewRegistry(func(msg string) { warned = msg })
	r.Register("stub", &stubProvider{value: []byte("v"), plaintext: false})
	if _, err := r.Resolve(context.Background(), "stub:x"); err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if warned != "" {
		t.Fatalf("expected no warning for secure scheme, got %q", warned)
	}
}

func TestDefaultRegistryResolvesEnvAndVaultSchemes(t *testing.T) {
	getenv := func(k string) string {
		switch k {
		case "MY_SECRET":
			return "from-env"
		case "VAULT_ADDR":
			return "http://127.0.0.1:8200"
		case "VAULT_TOKEN":
			return "tok"
		}
		return ""
	}
	r := DefaultRegistry(getenv, nil)

	got, err := r.Resolve(context.Background(), "env:MY_SECRET")
	if err != nil {
		t.Fatalf("env resolve error: %v", err)
	}
	if string(got) != "from-env" {
		t.Fatalf("env value = %q, want %q", got, "from-env")
	}
	// vault scheme is registered (a bad ref errors at the provider, not as
	// "unknown scheme").
	_, err = r.Resolve(context.Background(), "vault:no-hash")
	if err == nil {
		t.Fatal("expected a vault reference error")
	}
	if strings.Contains(err.Error(), "unknown secret scheme") {
		t.Fatalf("vault scheme not registered: %v", err)
	}
}
