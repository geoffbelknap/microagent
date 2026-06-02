//go:build linux

package firecracker

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
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

func TestServeSecretsListenerEndToEnd(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "secrets.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	bundle := secretxfer.Bundle{
		ProtocolVersion: secretxfer.ProtocolVersion,
		Secrets:         []secretxfer.Entry{{Name: "API", Value: []byte("sekret")}},
	}
	go serveSecretsListener(ln, bundle)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := secretxfer.FetchBundle(conn)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Secrets) != 1 || string(got.Secrets[0].Value) != "sekret" {
		t.Fatalf("unexpected bundle: %+v", got)
	}
}

// TestResolveAndServeLiveVault validates the full host path against a real Vault
// when VAULT_ADDR/VAULT_TOKEN are set (run `vault server -dev` and
// `vault kv put secret/app api_key=...` first). It is skipped otherwise, so CI
// without Vault is unaffected.
func TestResolveAndServeLiveVault(t *testing.T) {
	if os.Getenv("VAULT_ADDR") == "" || os.Getenv("VAULT_TOKEN") == "" {
		t.Skip("set VAULT_ADDR and VAULT_TOKEN (and write secret/app api_key) to run the live Vault check")
	}
	cfg := &vmkit.Config{Secrets: []vmkit.SecretRef{{Name: "API", Ref: "vault:secret/data/app#api_key"}}}
	bundle, err := resolveSecretsBundle(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolve live vault: %v", err)
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "secrets.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go serveSecretsListener(ln, bundle)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := secretxfer.FetchBundle(conn)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Secrets) != 1 || got.Secrets[0].Name != "API" || len(got.Secrets[0].Value) == 0 {
		t.Fatalf("unexpected live bundle: %+v", got)
	}
}

func TestStartVsockListenersSecretsSocketIsOwnerOnly(t *testing.T) {
	t.Setenv("MA_TEST_TOK", "sekret")
	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		SecretsPort:    1026,
		Secrets:        []vmkit.SecretRef{{Name: "API", Ref: "env:MA_TEST_TOK"}},
		VsockListeners: []vmkit.VsockListener{{Port: 1026, Target: secretsListenerTarget}},
	}
	set, err := startVsockListeners(opts, cfg)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	defer set.Close()

	info, err := os.Stat(firecrackerGuestVsockPath(opts, 1026))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets socket mode = %v, want 0600 (owner-only; the bundle is plaintext)", info.Mode().Perm())
	}
}

func TestStartVsockListenersServesSecrets(t *testing.T) {
	t.Setenv("MA_TEST_TOK", "sekret")
	dir := t.TempDir()
	opts := Options{Name: "ws", StateDir: dir}
	cfg := &vmkit.Config{
		SecretsPort:    1026,
		Secrets:        []vmkit.SecretRef{{Name: "API", Ref: "env:MA_TEST_TOK"}},
		VsockListeners: []vmkit.VsockListener{{Port: 1026, Target: secretsListenerTarget}},
	}
	set, err := startVsockListeners(opts, cfg)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	defer set.Close()

	conn, err := net.Dial("unix", firecrackerGuestVsockPath(opts, 1026))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := secretxfer.FetchBundle(conn)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Secrets) != 1 || string(got.Secrets[0].Value) != "sekret" {
		t.Fatalf("unexpected bundle: %+v", got)
	}
}
