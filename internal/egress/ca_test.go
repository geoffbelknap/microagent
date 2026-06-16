package egress

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"testing"
	"time"
)

func TestCASignsVerifiableLeaf(t *testing.T) {
	ca, err := NewCA("microagent-egress-test", time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	leaf, err := ca.LeafFor("example.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}
	x, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	cb, _ := pem.Decode(ca.CertPEM())
	caCert, _ := x509.ParseCertificate(cb.Bytes)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := x.Verify(x509.VerifyOptions{DNSName: "example.com", Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("leaf does not chain to CA: %v", err)
	}
}

func TestCALeafCachedBySNI(t *testing.T) {
	ca, _ := NewCA("cn", time.Hour)
	a, _ := ca.LeafFor("api.github.com")
	b, _ := ca.LeafFor("api.github.com")
	if a != b {
		t.Fatal("expected cached leaf to be reused for same SNI")
	}
}

func TestCARoundTripPEM(t *testing.T) {
	ca, _ := NewCA("cn", time.Hour)
	keyPEM, err := ca.KeyPEM()
	if err != nil {
		t.Fatalf("KeyPEM: %v", err)
	}
	ca2, err := LoadCA(ca.CertPEM(), keyPEM)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	if _, err := ca2.LeafFor("api.github.com"); err != nil {
		t.Fatalf("loaded CA cannot sign: %v", err)
	}
}

func TestLoadCARejectsMismatchedKey(t *testing.T) {
	ca1, _ := NewCA("ca1", time.Hour)
	ca2, _ := NewCA("ca2", time.Hour)
	key2, err := ca2.KeyPEM()
	if err != nil {
		t.Fatalf("KeyPEM: %v", err)
	}
	if _, err := LoadCA(ca1.CertPEM(), key2); err == nil {
		t.Fatal("expected LoadCA to reject cert/key from different CAs")
	}
}

func TestLeafCacheBounded(t *testing.T) {
	ca, _ := NewCA("cn", time.Hour)
	// Sign well past the cap to force eviction.
	for i := 0; i < maxCachedLeaves+50; i++ {
		if _, err := ca.LeafFor(fmt.Sprintf("h%d.example.com", i)); err != nil {
			t.Fatalf("LeafFor: %v", err)
		}
	}
	ca.mu.Lock()
	n := len(ca.leaves)
	ca.mu.Unlock()
	if n > maxCachedLeaves {
		t.Fatalf("leaf cache grew unbounded: %d entries (cap %d)", n, maxCachedLeaves)
	}
}

func TestLeafExpiryCappedAtCA(t *testing.T) {
	ca, _ := NewCA("cn", 2*time.Hour)
	leaf, err := ca.LeafFor("example.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}
	x, _ := x509.ParseCertificate(leaf.Certificate[0])
	// leaf must not outlive the CA
	if x.NotAfter.After(ca.cert.NotAfter) {
		t.Fatalf("leaf NotAfter %v exceeds CA NotAfter %v", x.NotAfter, ca.cert.NotAfter)
	}
}
