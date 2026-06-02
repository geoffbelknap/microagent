package secret

import (
	"context"
	"errors"
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
