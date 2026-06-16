package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

// CA is a per-workspace certificate authority that signs short-lived leaf
// certificates per SNI for TLS interception. The private key stays on the host
// mediator; only CertPEM() (the public cert) is delivered to the guest.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// NewCA mints a fresh ECDSA P-256 per-workspace CA valid for lifetime.
func NewCA(commonName string, lifetime time.Duration) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("egress: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"microagent egress"}},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("egress: create CA cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

// CertPEM returns the CA public certificate (PEM) for the guest trust store.
func (c *CA) CertPEM() []byte { return c.certPEM }

// KeyPEM returns the CA private key (PEM, SEC1/EC). The supervisor persists it
// 0600 and never delivers it to the guest.
func (c *CA) KeyPEM() ([]byte, error) {
	b, err := x509.MarshalECPrivateKey(c.key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b}), nil
}

// LoadCA reconstructs a CA from cert+key PEM (as written by the supervisor).
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("egress: invalid CA cert PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("egress: invalid CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, fmt.Errorf("egress: CA key does not match cert public key")
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cb.Bytes}),
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

// LeafFor returns a tls.Certificate for serverName signed by the CA, cached by name.
func (c *CA) LeafFor(serverName string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tc, ok := c.leaves[serverName]; ok {
		return tc, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: serverName},
		NotBefore:    now.Add(-time.Minute),
		// Leaf is valid for the CA's lifetime so a cached leaf never expires
		// before the per-workspace CA (and thus the workspace) does.
		NotAfter:     c.cert.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(serverName); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{serverName}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("egress: sign leaf for %q: %w", serverName, err)
	}
	tc := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
	c.leaves[serverName] = tc
	return tc, nil
}
