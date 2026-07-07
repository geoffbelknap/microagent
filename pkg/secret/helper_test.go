package secret

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeHelper(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("helper scripts in this test are POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHelperProviderResolves(t *testing.T) {
	p := &HelperProvider{Command: writeHelper(t, `[ "$1" = "gcp-sm:projects/p/secrets/s" ] || exit 9
printf 'the-secret\n'`)}
	got, err := p.Resolve(context.Background(), "gcp-sm:projects/p/secrets/s")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one trailing newline is trimmed; the value itself is verbatim.
	if string(got) != "the-secret" {
		t.Fatalf("value %q", got)
	}
	if p.Plaintext() {
		t.Fatal("helper scheme must not be flagged plaintext")
	}
}

func TestHelperProviderFailsClosed(t *testing.T) {
	// Unconfigured host.
	p := &HelperProvider{}
	if _, err := p.Resolve(context.Background(), "ref"); err == nil || !strings.Contains(err.Error(), "MICROAGENT_SECRET_HELPER") {
		t.Fatalf("unconfigured: %v", err)
	}

	// Nonzero exit surfaces stderr, never a partial value.
	p = &HelperProvider{Command: writeHelper(t, `echo "permission denied on ref $1" >&2; exit 3`)}
	if _, err := p.Resolve(context.Background(), "ref"); err == nil || !strings.Contains(err.Error(), "permission denied on ref") {
		t.Fatalf("nonzero exit: %v", err)
	}

	// Empty stdout is an error, not an empty secret.
	p = &HelperProvider{Command: writeHelper(t, `exit 0`)}
	if _, err := p.Resolve(context.Background(), "ref"); err == nil || !strings.Contains(err.Error(), "empty secret") {
		t.Fatalf("empty stdout: %v", err)
	}

	// Empty reference is rejected before exec.
	if _, err := p.Resolve(context.Background(), "  "); err == nil {
		t.Fatal("empty reference must fail")
	}
}

func TestDefaultRegistryWiresHelper(t *testing.T) {
	helper := writeHelper(t, `printf 'wired'`)
	getenv := func(k string) string {
		if k == "MICROAGENT_SECRET_HELPER" {
			return helper
		}
		return ""
	}
	r := DefaultRegistry(getenv, nil)
	got, err := r.Resolve(context.Background(), "helper:anything")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "wired" {
		t.Fatalf("value %q", got)
	}
}
