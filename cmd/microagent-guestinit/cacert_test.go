//go:build linux

package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
)

const testEgressCACertPEM = `-----BEGIN CERTIFICATE-----
MIIBpTCCAUugAwIBAgIRAKSMVqEuVkabMbABCD123456789wCgYIKoZIzj0EAwIw
MTETMBEGA1UEChMKbWljcm9hZ2VudDEZMBcGA1UEAxMQZWdyZXNzLW1lZGlhdG9y
MB4XDTIwMDEwMTAwMDAwMFoXDTIxMDEwMTAwMDAwMFowMTETMBEGA1UEChMKbWlj
cm9hZ2VudDEZMBcGA1UEAxMQZWdyZXNzLW1lZGlhdG9yMFkwEwYHKoZIzj0CAQYI
KoZIzj0DAQcDQgAEfakePublicKeyMaterialForTestingPurposesOnlyXYZABC123
o2YwZDAOBgNVHQ8BAf8EBAMCAQYwEgYDVR0TAQH/BAgwBgEB/wIBATAdBgNVHQ4E
FgQUfakeKeyID1234567890ABCDEFwithPadMB8GA1UdIwQYMBaAFfakeKeyIDwith
PadMBcGA1UdEQQQMA6CDGVncmVzcy1jYS5wZW0wCgYIKoZIzj0EAwIDSAAwRQIh
AOFakeSignatureR1234567890ABCDEF0ABCDEFghijklmnAIgFakeS2abcdefghijk
-----END CERTIFICATE-----
`

const testSystemCABundle = "SYSTEM-CA-PLACEHOLDER\n"

// TestWriteCACertWithSystemBundle tests the common case: a system CA bundle
// exists and should be combined with our egress CA.
func TestWriteCACertWithSystemBundle(t *testing.T) {
	root := t.TempDir()
	pem := []byte(testEgressCACertPEM)

	// Pre-create a fake system bundle at the first candidate path.
	sysBundlePath := filepath.Join(root, "/etc/ssl/certs/ca-certificates.crt")
	if err := os.MkdirAll(filepath.Dir(sysBundlePath), 0o755); err != nil {
		t.Fatalf("create system bundle dir: %v", err)
	}
	if err := os.WriteFile(sysBundlePath, []byte(testSystemCABundle), 0o644); err != nil {
		t.Fatalf("write system bundle: %v", err)
	}

	envVars, err := writeCACert(root, pem)
	if err != nil {
		t.Fatalf("writeCACert: %v", err)
	}

	// egress-ca.pem must contain ONLY our CA.
	caDest := filepath.Join(root, caCertPath)
	caData, err := os.ReadFile(caDest)
	if err != nil {
		t.Fatalf("read %s: %v", caDest, err)
	}
	if !bytes.Equal(caData, pem) {
		t.Fatalf("egress-ca.pem content mismatch: got %d bytes, want %d", len(caData), len(pem))
	}
	if info, err := os.Stat(caDest); err != nil {
		t.Fatalf("stat %s: %v", caDest, err)
	} else if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("egress-ca.pem perm = %04o, want 0644", perm)
	}

	// egress-ca-bundle.pem must contain BOTH the system sentinel AND our CA.
	bundleDest := filepath.Join(root, caCertBundlePath)
	bundleData, err := os.ReadFile(bundleDest)
	if err != nil {
		t.Fatalf("read %s: %v", bundleDest, err)
	}
	if !strings.Contains(string(bundleData), testSystemCABundle) {
		t.Fatalf("bundle missing system CA sentinel; bundle = %q", string(bundleData))
	}
	if !strings.Contains(string(bundleData), testEgressCACertPEM) {
		t.Fatalf("bundle missing our egress CA PEM; bundle = %q", string(bundleData))
	}
	if info, err := os.Stat(bundleDest); err != nil {
		t.Fatalf("stat %s: %v", bundleDest, err)
	} else if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("egress-ca-bundle.pem perm = %04o, want 0644", perm)
	}

	// Env-var assertions:
	// Replace-trust-store vars → combined bundle path.
	// NODE_EXTRA_CA_CERTS → our-CA-only path.
	wantBundleVars := []string{
		"SSL_CERT_FILE",
		"CURL_CA_BUNDLE",
		"REQUESTS_CA_BUNDLE",
		"GIT_SSL_CAINFO",
	}
	for _, name := range wantBundleVars {
		val := findEnvVar(envVars, name)
		if val == "" {
			t.Fatalf("envVars missing %s: %v", name, envVars)
		}
		if val != caCertBundlePath {
			t.Fatalf("%s = %q, want %q", name, val, caCertBundlePath)
		}
		if strings.HasPrefix(val, root) {
			t.Fatalf("%s leaks test root %q: %q", name, root, val)
		}
	}

	nodeVal := findEnvVar(envVars, "NODE_EXTRA_CA_CERTS")
	if nodeVal == "" {
		t.Fatalf("envVars missing NODE_EXTRA_CA_CERTS: %v", envVars)
	}
	if nodeVal != caCertPath {
		t.Fatalf("NODE_EXTRA_CA_CERTS = %q, want %q", nodeVal, caCertPath)
	}
	if strings.HasPrefix(nodeVal, root) {
		t.Fatalf("NODE_EXTRA_CA_CERTS leaks test root %q: %q", root, nodeVal)
	}

	// SSL_CERT_DIR must NOT be set (we removed it to avoid confusing OpenSSL).
	if val := findEnvVar(envVars, "SSL_CERT_DIR"); val != "" {
		t.Fatalf("SSL_CERT_DIR should not be set, got %q", val)
	}
}

// TestWriteCACertNoSystemBundle tests the degraded case: no system CA bundle
// exists. The combined bundle must equal our CA only, and the function must
// not crash.
func TestWriteCACertNoSystemBundle(t *testing.T) {
	root := t.TempDir()
	// Do NOT pre-create any system bundle — empty root.
	pem := []byte(testEgressCACertPEM)

	envVars, err := writeCACert(root, pem)
	if err != nil {
		t.Fatalf("writeCACert (no system bundle): %v", err)
	}

	// egress-ca.pem must exist with our CA.
	caData, err := os.ReadFile(filepath.Join(root, caCertPath))
	if err != nil {
		t.Fatalf("read egress-ca.pem: %v", err)
	}
	if !bytes.Equal(caData, pem) {
		t.Fatalf("egress-ca.pem content mismatch")
	}

	// egress-ca-bundle.pem must exist and contain our CA.
	bundleData, err := os.ReadFile(filepath.Join(root, caCertBundlePath))
	if err != nil {
		t.Fatalf("read egress-ca-bundle.pem: %v", err)
	}
	if !strings.Contains(string(bundleData), testEgressCACertPEM) {
		t.Fatalf("bundle (no system) missing our CA PEM")
	}

	// Should not contain the sentinel.
	if strings.Contains(string(bundleData), testSystemCABundle) {
		t.Fatalf("bundle unexpectedly contains system sentinel when no bundle present")
	}

	// Env vars still point at the right places.
	for _, name := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"} {
		val := findEnvVar(envVars, name)
		if val != caCertBundlePath {
			t.Fatalf("%s = %q, want %q", name, val, caCertBundlePath)
		}
	}
	if nodeVal := findEnvVar(envVars, "NODE_EXTRA_CA_CERTS"); nodeVal != caCertPath {
		t.Fatalf("NODE_EXTRA_CA_CERTS = %q, want %q", nodeVal, caCertPath)
	}
	if val := findEnvVar(envVars, "SSL_CERT_DIR"); val != "" {
		t.Fatalf("SSL_CERT_DIR should not be set, got %q", val)
	}
}

// TestInstallCACertFromReaderEndToEnd exercises the full fetch+install path
// over a net.Pipe (no vsock required).
func TestInstallCACertFromReaderEndToEnd(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })

	pem := []byte(testEgressCACertPEM)
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		errCh <- secretxfer.ServeCACert(server, pem)
	}()

	root := t.TempDir()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	envVars, err := installCACertFromReader(client, root)
	if err != nil {
		t.Fatalf("installCACertFromReader: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("ServeCACert: %v", err)
	}

	// Cert must be written.
	dest := filepath.Join(root, caCertPath)
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	if !bytes.Equal(data, pem) {
		t.Fatalf("cert content mismatch")
	}

	// At least one env var must be present.
	if len(envVars) == 0 {
		t.Fatal("envVars is empty")
	}
}

// TestWriteCACertRejectsServerClose exercises error propagation when the
// server closes before delivering a payload.
func TestInstallCACertFromReaderRejectsServerClose(t *testing.T) {
	server, client := net.Pipe()
	server.Close()
	t.Cleanup(func() { client.Close() })

	root := t.TempDir()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := installCACertFromReader(client, root); err == nil {
		t.Fatal("installCACertFromReader error = nil, want error on closed server")
	}
}

// findEnvVar returns the value of the named env var from a KEY=VALUE slice,
// or "" if not found.
func findEnvVar(envVars []string, name string) string {
	prefix := name + "="
	for _, v := range envVars {
		if strings.HasPrefix(v, prefix) {
			return strings.TrimPrefix(v, prefix)
		}
	}
	return ""
}
