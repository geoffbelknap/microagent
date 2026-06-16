package secretxfer

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"
)

const testCACertPEM = `-----BEGIN CERTIFICATE-----
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

func TestServeCACertAndFetchRoundTrip(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })

	pem := []byte(testCACertPEM)
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		errCh <- ServeCACert(server, pem)
	}()

	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	got, err := FetchCACert(client)
	if err != nil {
		t.Fatalf("FetchCACert: %v", err)
	}
	if !bytes.Equal(got, pem) {
		t.Fatalf("fetched PEM does not match: got %d bytes, want %d", len(got), len(pem))
	}
	if err := <-errCh; err != nil {
		t.Fatalf("ServeCACert: %v", err)
	}
}

func TestServeCACertRejectsEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := ServeCACert(&buf, nil); err == nil {
		t.Fatal("ServeCACert(nil) error = nil, want error")
	}
	if err := ServeCACert(&buf, []byte{}); err == nil {
		t.Fatal("ServeCACert(empty) error = nil, want error")
	}
}

func TestFetchCACertRejectsZeroLength(t *testing.T) {
	// A 4-byte prefix of all zeros means length=0.
	r := bytes.NewReader([]byte{0, 0, 0, 0})
	if _, err := FetchCACert(r); err == nil {
		t.Fatal("FetchCACert(zero-length) error = nil, want error")
	}
}

func TestFetchCACertRejectsTruncated(t *testing.T) {
	// Prefix says 100 bytes but there is no payload.
	r := bytes.NewReader([]byte{0, 0, 0, 100})
	if _, err := FetchCACert(r); err == nil {
		t.Fatal("FetchCACert(truncated) error = nil, want error")
	}
}

func TestFetchCACertRejectsOversized(t *testing.T) {
	// Prefix claims more than MaxCACertBytes.
	tooBig := MaxCACertBytes + 1
	prefix := []byte{
		byte(tooBig >> 24),
		byte(tooBig >> 16),
		byte(tooBig >> 8),
		byte(tooBig),
	}
	r := bytes.NewReader(prefix)
	_, err := FetchCACert(r)
	if err == nil {
		t.Fatal("FetchCACert(oversized) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("FetchCACert(oversized) error = %v, want 'exceeds maximum'", err)
	}
}
