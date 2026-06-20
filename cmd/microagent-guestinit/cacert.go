//go:build linux

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
)

const (
	caCertConnectTimeout = 30 * time.Second
	// caCertPath is where the egress CA certificate is written inside the guest.
	// NODE_EXTRA_CA_CERTS points here (Node adds this ON TOP of its built-in roots).
	caCertPath = "/etc/microagent/egress-ca.pem"
	// caCertBundlePath is the combined bundle (system CAs + our CA). Tools that
	// REPLACE the trust store (SSL_CERT_FILE, CURL_CA_BUNDLE, etc.) point here so
	// that public-CA-signed certs (passthrough hosts) still verify correctly.
	caCertBundlePath = "/etc/microagent/egress-ca-bundle.pem"
	// caCertSystemPath is the optional system trust store location. Copied on a
	// best-effort basis; the env-var approach is the portable guarantee.
	caCertSystemPath = "/usr/local/share/ca-certificates/microagent-egress.crt"
	updateCACertsCmd = "update-ca-certificates"
)

// systemCABundleCandidates is the ordered list of system CA bundle paths to
// search for inside the guest root. First match wins.
var systemCABundleCandidates = []string{
	"/etc/ssl/certs/ca-certificates.crt", // Debian / Ubuntu / Alpine
	"/etc/ssl/cert.pem",                  // Alpine (alternate) / macOS / BSD
	"/etc/pki/tls/certs/ca-bundle.crt",   // RHEL / CentOS / Fedora
}

// installCACert fetches the PEM from the host via vsock port, writes it to
// the guest CA cert path, and returns the env-var additions that tell tools
// and runtimes to trust it. It is the testable core of the CA delivery step.
//
// root is the filesystem root under which paths are resolved (always "/" in
// production, a t.TempDir() in tests). The vsock dial is performed by the
// caller via the io.Reader; pass a net.Conn wrapping the vsock fd.
func installCACertFromReader(r io.Reader, root string) ([]string, error) {
	certPEM, err := secretxfer.FetchCACert(r)
	if err != nil {
		return nil, fmt.Errorf("fetch CA cert: %w", err)
	}
	return writeCACert(root, certPEM)
}

// writeCACert writes certPEM to <root>/etc/microagent/egress-ca.pem (0644),
// builds a combined bundle (system CAs + our CA) at
// <root>/etc/microagent/egress-ca-bundle.pem, attempts a best-effort system
// trust-store copy + update-ca-certificates, and returns the list of
// environment variable assignments that direct tools to the correct bundle.
//
// Tools that REPLACE the trust store (SSL_CERT_FILE, CURL_CA_BUNDLE,
// REQUESTS_CA_BUNDLE, GIT_SSL_CAINFO) point at the COMBINED bundle so that
// passthrough hosts with public-CA-signed certs still verify. NODE_EXTRA_CA_CERTS
// points at the our-CA-only file because Node adds it ON TOP of its built-in roots.
//
// This function is fully testable without a vsock or a real guest.
func writeCACert(root string, certPEM []byte) ([]string, error) {
	// 1. Write the our-CA-only file.
	dest := filepath.Join(root, caCertPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("create CA cert dir: %w", err)
	}
	if err := fsutil.WriteFile(dest, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write CA cert: %w", err)
	}
	log.Printf("microagent-init: CA cert written to %s (%d bytes)", dest, len(certPEM))

	// 2. Build the combined bundle: system CAs (if any) + our CA.
	var bundleContents []byte
	for _, candidate := range systemCABundleCandidates {
		sysBundle, err := os.ReadFile(filepath.Join(root, candidate))
		if err == nil {
			bundleContents = append(bundleContents, sysBundle...)
			bundleContents = append(bundleContents, '\n')
			log.Printf("microagent-init: system CA bundle found at %s (%d bytes)", candidate, len(sysBundle))
			break
		}
	}
	if len(bundleContents) == 0 {
		log.Printf("microagent-init: WARNING: no system CA bundle found — public-CA trust unavailable (MITM-only mode); passthrough TLS will fail")
	}
	bundleContents = append(bundleContents, certPEM...)

	bundleDest := filepath.Join(root, caCertBundlePath)
	if err := fsutil.WriteFile(bundleDest, bundleContents, 0o644); err != nil {
		return nil, fmt.Errorf("write CA bundle: %w", err)
	}
	log.Printf("microagent-init: combined CA bundle written to %s (%d bytes)", bundleDest, len(bundleContents))

	// 3. Best-effort: copy into system trust store and run update-ca-certificates.
	// Failures are logged but do not block boot; env-var injection is the
	// portable guarantee.
	sysPath := filepath.Join(root, caCertSystemPath)
	if err := os.MkdirAll(filepath.Dir(sysPath), 0o755); err == nil {
		if err := fsutil.WriteFile(sysPath, certPEM, 0o644); err == nil {
			if updater, err := exec.LookPath(updateCACertsCmd); err == nil {
				if out, err := exec.Command(updater).CombinedOutput(); err != nil {
					log.Printf("microagent-init: update-ca-certificates: %v: %s", err, out)
				} else {
					log.Printf("microagent-init: update-ca-certificates: ok")
				}
			}
		}
	}

	// 4. Return env-var assignments using absolute guest paths (not root-prefixed).
	// Tools that replace the trust store → combined bundle (system + our CA).
	// NODE_EXTRA_CA_CERTS → our-CA-only file (Node appends to built-in roots).
	envVars := []string{
		"SSL_CERT_FILE=" + caCertBundlePath,
		"CURL_CA_BUNDLE=" + caCertBundlePath,
		"REQUESTS_CA_BUNDLE=" + caCertBundlePath,
		"GIT_SSL_CAINFO=" + caCertBundlePath,
		"NODE_EXTRA_CA_CERTS=" + caCertPath,
	}
	return envVars, nil
}

// fetchAndInstallCACert dials the host vsock port, fetches the CA certificate,
// writes it to the guest filesystem, and returns env-var additions for the
// workload. Mirror of fetchAndWriteSecrets but simpler: no tmpfs, no audit.
func fetchAndInstallCACert(port uint16) ([]string, error) {
	fd, err := dialHostVsock(uint32(port), caCertConnectTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect CA cert vsock port %d: %w", port, err)
	}
	conn := os.NewFile(uintptr(fd), "cacert-vsock")
	defer func() { _ = conn.Close() }()
	return installCACertFromReader(conn, "/")
}
