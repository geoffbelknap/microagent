package egress

import (
	"crypto/x509"
	"encoding/pem"
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
