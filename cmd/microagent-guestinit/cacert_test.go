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

// TestWriteCACertWritesFileAndReturnsEnvVars exercises the testable install
// helper against a t.TempDir() root without requiring a vsock or real guest.
func TestWriteCACertWritesFileAndReturnsEnvVars(t *testing.T) {
	root := t.TempDir()
	pem := []byte(testEgressCACertPEM)

	envVars, err := writeCACert(root, pem)
	if err != nil {
		t.Fatalf("writeCACert: %v", err)
	}

	// File must exist at <root>/etc/microagent/egress-ca.pem with correct content.
	dest := filepath.Join(root, caCertPath)
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read %s: %v", dest, err)
	}
	if !bytes.Equal(data, pem) {
		t.Fatalf("cert file content mismatch: got %d bytes, want %d", len(data), len(pem))
	}
	// File mode must be 0644.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat %s: %v", dest, err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("cert file perm = %04o, want 0644", perm)
	}

	// Must return exactly the 6 env vars, all pointing at caCertPath (not root-prefixed).
	wantVarPrefixes := []string{
		"SSL_CERT_FILE=",
		"SSL_CERT_DIR=",
		"CURL_CA_BUNDLE=",
		"REQUESTS_CA_BUNDLE=",
		"NODE_EXTRA_CA_CERTS=",
		"GIT_SSL_CAINFO=",
	}
	if len(envVars) != len(wantVarPrefixes) {
		t.Fatalf("envVars len = %d, want %d: %v", len(envVars), len(wantVarPrefixes), envVars)
	}
	for _, prefix := range wantVarPrefixes {
		found := false
		for _, v := range envVars {
			if strings.HasPrefix(v, prefix) {
				found = true
				// The value must reference caCertPath or its directory, not the temp root.
				val := strings.TrimPrefix(v, prefix)
				if strings.HasPrefix(val, root) {
					t.Fatalf("env var %q leaks test root %q — should use guest path", v, root)
				}
				break
			}
		}
		if !found {
			t.Fatalf("envVars missing %q: %v", prefix, envVars)
		}
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
