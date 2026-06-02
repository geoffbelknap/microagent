package secret

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newVaultStub(t *testing.T, handler http.HandlerFunc) *VaultProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &VaultProvider{Addr: srv.URL, Token: "test-token", Client: srv.Client()}
}

func TestVaultProviderReadsKVv2Field(t *testing.T) {
	p := newVaultStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/secret/data/app" {
			t.Errorf("path = %q, want /v1/secret/data/app", r.URL.Path)
		}
		if r.Header.Get("X-Vault-Token") != "test-token" {
			t.Errorf("missing or wrong X-Vault-Token: %q", r.Header.Get("X-Vault-Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"api_key":"sekret"},"metadata":{}}}`))
	})
	got, err := p.Resolve(context.Background(), "secret/data/app#api_key")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if string(got) != "sekret" {
		t.Fatalf("value = %q, want %q", got, "sekret")
	}
	if p.Plaintext() {
		t.Fatal("vault provider must report Plaintext() == false")
	}
}

func TestVaultProviderMalformedReferenceErrors(t *testing.T) {
	p := &VaultProvider{Addr: "http://127.0.0.1", Token: "t"}
	if _, err := p.Resolve(context.Background(), "secret/data/app"); err == nil {
		t.Fatal("expected error for reference without #field")
	}
}

func TestVaultProviderMissingAddrErrors(t *testing.T) {
	p := &VaultProvider{Token: "t"}
	if _, err := p.Resolve(context.Background(), "secret/data/app#k"); err == nil {
		t.Fatal("expected error when VAULT_ADDR unset")
	}
}

func TestVaultProviderMissingTokenErrors(t *testing.T) {
	p := &VaultProvider{Addr: "http://127.0.0.1"}
	if _, err := p.Resolve(context.Background(), "secret/data/app#k"); err == nil {
		t.Fatal("expected error when VAULT_TOKEN unset")
	}
}

func TestVaultProviderPermissionDenied(t *testing.T) {
	p := newVaultStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	})
	if _, err := p.Resolve(context.Background(), "secret/data/app#k"); err == nil {
		t.Fatal("expected permission-denied error")
	}
}

func TestVaultProviderNotFound(t *testing.T) {
	p := newVaultStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := p.Resolve(context.Background(), "secret/data/missing#k"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestVaultProviderSealed(t *testing.T) {
	p := newVaultStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if _, err := p.Resolve(context.Background(), "secret/data/app#k"); err == nil {
		t.Fatal("expected sealed/unavailable error")
	}
}

func TestVaultProviderMissingFieldErrors(t *testing.T) {
	p := newVaultStub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"data":{"other":"x"}}}`))
	})
	if _, err := p.Resolve(context.Background(), "secret/data/app#api_key"); err == nil {
		t.Fatal("expected error for missing field")
	}
}

func TestVaultProviderNonStringFieldErrors(t *testing.T) {
	p := newVaultStub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"data":{"api_key":12345}}}`))
	})
	if _, err := p.Resolve(context.Background(), "secret/data/app#api_key"); err == nil {
		t.Fatal("expected error for non-string field value")
	}
}
