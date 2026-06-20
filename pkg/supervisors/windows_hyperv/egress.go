package windows_hyperv

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// egressCAKeyLifetime matches the firecracker supervisor's per-workspace CA
// lifetime (30 days). The leaf certs the mediator signs inherit the CA's
// NotAfter, so this also bounds intercepted-TLS leaf validity.
const egressCAKeyLifetime = 720 * time.Hour

// egressCACertPath returns the path to the per-workspace egress CA certificate
// PEM file that is served to the guest over the cacert://serve hvsock listener
// and loaded by the host mediator front-end for per-SNI TLS interception.
func egressCACertPath(req vmkit.Request) string {
	return filepath.Join(runtimeDir(req), "egress-ca.pem")
}

// egressCAKeyPath returns the path to the per-workspace egress CA private key
// PEM. It stays host-side (0600) and is never delivered to the guest; the
// mediator front-end pairs it with egressCACertPath to sign per-SNI leaves.
// Kept in lockstep with the firecracker supervisor's egress-ca-key.pem name.
func egressCAKeyPath(req vmkit.Request) string {
	return filepath.Join(runtimeDir(req), "egress-ca-key.pem")
}

// egressMediationActive reports whether this workspace provisions the egress
// mediator: the egress mode must be ON ("mediated"/"strict") AND the network
// mode must actually route guest egress through a mediator. Both gates mirror
// the firecracker provisioning decision so the windows-hyperv front-end never
// mints a CA (or binds the mediator service) for a mediator that will not exist.
func egressMediationActive(config *vmkit.Config) bool {
	if config == nil {
		return false
	}
	networkMode := ""
	if config.Network != nil {
		networkMode = config.Network.Mode
	}
	return vmkit.EgressMediationOn(config.EgressMode) && vmkit.NetworkModeMediates(networkMode)
}

// mintEgressCAHook lets tests substitute a deterministic CA mint.
var mintEgressCAHook = mintEgressCA

// mintEgressCA mints a fresh per-workspace egress CA (reusing egress.NewCA — the
// security primitive is never hand-rolled) and writes the public cert to
// egressCACertPath (0644, delivered to the guest over the cacert hvsock listener)
// and the private key to egressCAKeyPath (0600, host-only). It is a no-op when
// mediation is not active so an unmediated workspace mints nothing. Called before
// the CA-cert serve goroutine and the mediator front-end load so all three agree
// on the same on-disk cert/key.
func mintEgressCA(req vmkit.Request) error {
	if !egressMediationActive(req.Config) {
		return nil
	}
	ca, err := egress.NewCA(req.Identity.RuntimeID, egressCAKeyLifetime)
	if err != nil {
		return fmt.Errorf("mint egress CA for %s: %w", req.Identity.RuntimeID, err)
	}
	keyPEM, err := ca.KeyPEM()
	if err != nil {
		return fmt.Errorf("encode egress CA key for %s: %w", req.Identity.RuntimeID, err)
	}
	if err := os.MkdirAll(runtimeDir(req), 0o700); err != nil {
		return fmt.Errorf("create workspace dir for egress CA: %w", err)
	}
	if err := os.WriteFile(egressCACertPath(req), ca.CertPEM(), 0o644); err != nil {
		return fmt.Errorf("write egress CA cert: %w", err)
	}
	if err := os.WriteFile(egressCAKeyPath(req), keyPEM, 0o600); err != nil {
		_ = os.Remove(egressCACertPath(req))
		return fmt.Errorf("write egress CA key: %w", err)
	}
	return nil
}
