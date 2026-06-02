//go:build linux

package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestResolveSecretsBundleEnvRefAndEnvFile(t *testing.T) {
	t.Setenv("MA_TEST_TOK", "from-env")
	dir := t.TempDir()
	envFile := filepath.Join(dir, "app.env")
	if err := os.WriteFile(envFile, []byte("FILE_TOK=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &vmkit.Config{
		Secrets:        []vmkit.SecretRef{{Name: "API", Ref: "env:MA_TEST_TOK"}},
		SecretEnvFiles: []string{envFile},
	}
	bundle, err := resolveSecretsBundle(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := map[string]string{}
	for _, e := range bundle.Secrets {
		got[e.Name] = string(e.Value)
	}
	if got["API"] != "from-env" || got["FILE_TOK"] != "from-file" {
		t.Fatalf("unexpected bundle: %v", got)
	}
}

func TestResolveSecretsBundleFailsClosed(t *testing.T) {
	cfg := &vmkit.Config{Secrets: []vmkit.SecretRef{{Name: "API", Ref: "env:MA_DEFINITELY_UNSET"}}}
	if _, err := resolveSecretsBundle(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-closed error for unresolved reference")
	}
}

func TestResolveSecretsBundleRejectsBadName(t *testing.T) {
	t.Setenv("MA_TEST_TOK", "x")
	cfg := &vmkit.Config{Secrets: []vmkit.SecretRef{{Name: "../bad", Ref: "env:MA_TEST_TOK"}}}
	if _, err := resolveSecretsBundle(context.Background(), cfg); err == nil {
		t.Fatal("expected error for invalid secret name")
	}
}
