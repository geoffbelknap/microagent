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

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
)

const (
	caCertConnectTimeout = 30 * time.Second
	// caCertPath is where the egress CA certificate is written inside the guest.
	// Environment variables for tools and runtimes all point here.
	caCertPath = "/etc/microagent/egress-ca.pem"
	// caCertSystemPath is the optional system trust store location. Copied on a
	// best-effort basis; the env-var approach is the portable guarantee.
	caCertSystemPath    = "/usr/local/share/ca-certificates/microagent-egress.crt"
	updateCACertsCmd    = "update-ca-certificates"
)

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
// attempts a best-effort system trust-store copy + update-ca-certificates, and
// returns the list of environment variable assignments that direct tools to the
// cert. This function is fully testable without a vsock or a real guest.
func writeCACert(root string, certPEM []byte) ([]string, error) {
	dest := filepath.Join(root, caCertPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("create CA cert dir: %w", err)
	}
	if err := os.WriteFile(dest, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write CA cert: %w", err)
	}
	log.Printf("microagent-init: CA cert written to %s (%d bytes)", dest, len(certPEM))

	// Best-effort: copy into system trust store and run update-ca-certificates.
	// Failures are logged but do not block boot; env-var injection is the
	// portable guarantee.
	sysPath := filepath.Join(root, caCertSystemPath)
	if err := os.MkdirAll(filepath.Dir(sysPath), 0o755); err == nil {
		if err := os.WriteFile(sysPath, certPEM, 0o644); err == nil {
			if updater, err := exec.LookPath(updateCACertsCmd); err == nil {
				if out, err := exec.Command(updater).CombinedOutput(); err != nil {
					log.Printf("microagent-init: update-ca-certificates: %v: %s", err, out)
				} else {
					log.Printf("microagent-init: update-ca-certificates: ok")
				}
			}
		}
	}

	// The canonical cert path (inside the guest, not under root) is what the
	// workload command will see — root is only used during install/test.
	guestPath := caCertPath
	guestDir := filepath.Dir(guestPath)
	envVars := []string{
		"SSL_CERT_FILE=" + guestPath,
		"SSL_CERT_DIR=" + guestDir,
		"CURL_CA_BUNDLE=" + guestPath,
		"REQUESTS_CA_BUNDLE=" + guestPath,
		"NODE_EXTRA_CA_CERTS=" + guestPath,
		"GIT_SSL_CAINFO=" + guestPath,
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
