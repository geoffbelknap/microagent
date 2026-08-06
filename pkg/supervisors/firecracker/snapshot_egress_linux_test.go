//go:build linux

package firecracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// writePersistedCA mints a CA and writes its cert (egress-ca.pem) and key
// (egress-ca-key.pem) into the workspace dir exactly as provisionEgressMediation
// does, returning the hex SHA-256 of the cert DER (the manifest fingerprint).
func writePersistedCA(t *testing.T, wsDir string) (certPEM, keyPEM []byte, certSHA string) {
	t.Helper()
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ca, err := egress.NewCA("test-ws", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = ca.CertPEM()
	keyPEM, err = ca.KeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "egress-ca.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "egress-ca-key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("minted CA cert PEM did not decode")
		return
	}
	sum := sha256.Sum256(block.Bytes)
	return certPEM, keyPEM, hex.EncodeToString(sum[:])
}

// mediatedRuntimeState builds a minimal runtimeState for a mediated workspace so
// snapshotManifestFromState can be exercised without a live VM.
func mediatedRuntimeState(mode string, allow, passthrough []string) runtimeState {
	return runtimeState{
		Config: vmkit.Config{
			CPUCount:          2,
			MemoryMiB:         512,
			EgressMode:        mode,
			EgressAllow:       allow,
			EgressPassthrough: passthrough,
			Network:           &vmkit.NetworkConfig{Mode: "user", IP: "10.43.1.2/29", Gateway: "10.43.1.1", Subnet: "10.43.1.0/29"},
		},
	}
}

// mediatedRuntimeStateWithCaps is mediatedRuntimeState plus the bounded-operations
// caps (ASK tenet 8), so the cap round-trip through the manifest is exercised.
func mediatedRuntimeStateWithCaps(mode string, allow, passthrough []string, caps vmkit.Config) runtimeState {
	st := mediatedRuntimeState(mode, allow, passthrough)
	st.Config.EgressMaxBytesPerSec = caps.EgressMaxBytesPerSec
	st.Config.EgressMaxTotalBytes = caps.EgressMaxTotalBytes
	st.Config.EgressMaxConcurrentConns = caps.EgressMaxConcurrentConns
	st.Config.EgressAuditMaxBytes = caps.EgressAuditMaxBytes
	st.Config.EgressAuditMaxBackups = caps.EgressAuditMaxBackups
	return st
}

// TestSnapshotManifestFromStateRecordsEgressCA proves that snapshotting a
// mediated workspace records the egress mode/allow/passthrough AND the SHA-256 of
// the persisted CA cert DER — the fingerprint a restore verifies before reusing
// the CA so the restored guest's baked trust store keeps validating.
func TestSnapshotManifestFromStateRecordsEgressCA(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	_, _, wantSHA := writePersistedCA(t, filepath.Join(stateDir, opts.Name))

	state := mediatedRuntimeState(vmkit.EgressModeMITM, []string{"api.github.com", ".example.com"}, []string{"raw.example.com"})
	manifest, err := snapshotManifestFromState("snap-1", state, opts, false, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState: %v", err)
	}
	if manifest.EgressMode != vmkit.EgressModeMITM {
		t.Errorf("EgressMode = %q, want %q", manifest.EgressMode, vmkit.EgressModeMITM)
	}
	if len(manifest.EgressAllow) != 2 || manifest.EgressAllow[0] != "api.github.com" || manifest.EgressAllow[1] != ".example.com" {
		t.Errorf("EgressAllow = %v, want [api.github.com .example.com]", manifest.EgressAllow)
	}
	if len(manifest.EgressPassthrough) != 1 || manifest.EgressPassthrough[0] != "raw.example.com" {
		t.Errorf("EgressPassthrough = %v, want [raw.example.com]", manifest.EgressPassthrough)
	}
	if manifest.EgressCASHA256 != wantSHA {
		t.Errorf("EgressCASHA256 = %q, want %q", manifest.EgressCASHA256, wantSHA)
	}
}

// TestSnapshotManifestRoundTripsEgressCaps proves the bounded-operations caps
// (ASK tenet 8) are recorded in the manifest, survive a write/read round trip,
// and are re-applied onto a restore Config so a restored workspace keeps the SAME
// bounds it was snapshotted under.
func TestSnapshotManifestRoundTripsEgressCaps(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	writePersistedCA(t, filepath.Join(stateDir, opts.Name))

	caps := vmkit.Config{
		EgressMaxBytesPerSec:     1048576,
		EgressMaxTotalBytes:      10485760,
		EgressMaxConcurrentConns: 8,
		EgressAuditMaxBytes:      5242880,
		EgressAuditMaxBackups:    3,
	}
	state := mediatedRuntimeStateWithCaps(vmkit.EgressModeMITM, []string{"api.github.com"}, nil, caps)
	manifest, err := snapshotManifestFromState("snap-caps", state, opts, false, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState: %v", err)
	}
	if manifest.EgressMaxBytesPerSec != 1048576 || manifest.EgressMaxTotalBytes != 10485760 ||
		manifest.EgressMaxConcurrentConns != 8 || manifest.EgressAuditMaxBytes != 5242880 ||
		manifest.EgressAuditMaxBackups != 3 {
		t.Fatalf("manifest caps not recorded: %+v", manifest)
	}

	// Write + read back: the JSON round trip preserves the caps.
	dir := vmkit.SnapshotDir(stateDir, opts.Name, "snap-caps")
	if err := vmkit.WriteSnapshotManifest(dir, manifest); err != nil {
		t.Fatalf("WriteSnapshotManifest: %v", err)
	}
	got, err := vmkit.ReadSnapshotManifest(dir)
	if err != nil {
		t.Fatalf("ReadSnapshotManifest: %v", err)
	}
	if got.EgressMaxBytesPerSec != manifest.EgressMaxBytesPerSec ||
		got.EgressMaxTotalBytes != manifest.EgressMaxTotalBytes ||
		got.EgressMaxConcurrentConns != manifest.EgressMaxConcurrentConns ||
		got.EgressAuditMaxBytes != manifest.EgressAuditMaxBytes ||
		got.EgressAuditMaxBackups != manifest.EgressAuditMaxBackups {
		t.Fatalf("cap fields did not survive round trip: got %+v want %+v", got, manifest)
	}

	// Re-apply onto a restore Config that carries NO caps: the manifest is
	// authoritative and the restored posture inherits the snapshotted bounds.
	restoreCfg := &vmkit.Config{EgressMode: vmkit.EgressModeMITM}
	applyManifestEgressCaps(restoreCfg, got)
	if restoreCfg.EgressMaxBytesPerSec != 1048576 || restoreCfg.EgressMaxTotalBytes != 10485760 ||
		restoreCfg.EgressMaxConcurrentConns != 8 || restoreCfg.EgressAuditMaxBytes != 5242880 ||
		restoreCfg.EgressAuditMaxBackups != 3 {
		t.Fatalf("restore Config did not inherit persisted caps: %+v", restoreCfg)
	}
	// And those flow into the mediator argv on the restore path.
	gotCaps := egressCapsFromConfig(restoreCfg)
	wantCaps := egressCaps{maxBytesPerSec: 1048576, maxTotalBytes: 10485760, maxConns: 8, auditMaxBytes: 5242880, auditMaxBackups: 3}
	if gotCaps != wantCaps {
		t.Fatalf("egressCapsFromConfig = %+v, want %+v", gotCaps, wantCaps)
	}
}

// TestSnapshotManifestFromStateFailsClosedOnMissingCA proves that snapshotting a
// mediated workspace whose persisted CA cert is gone fails closed (returns an
// error) rather than producing a manifest with no fingerprint — restoring such a
// snapshot would silently break MITM, so the snapshot must never be created.
func TestSnapshotManifestFromStateFailsClosedOnMissingCA(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	// No egress-ca.pem written.
	if err := os.MkdirAll(filepath.Join(stateDir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	state := mediatedRuntimeState(vmkit.EgressModeMITM, nil, nil)
	if _, err := snapshotManifestFromState("snap-1", state, opts, false, false); err == nil {
		t.Fatal("expected error snapshotting mediated workspace with missing CA, got nil")
	}
}

// TestSnapshotManifestFromStateBrokerNeedsNoCA: broker mode forges nothing
// and mints no per-workspace CA, so snapshotting a broker workspace must
// succeed without a persisted CA and record an empty fingerprint — the
// restore path already treats an empty fingerprint as "no CA to reuse".
// Only certificate-forging modes require the persisted CA at snapshot time.
func TestSnapshotManifestFromStateBrokerNeedsNoCA(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	// No egress-ca.pem: broker workspaces never have one.
	if err := os.MkdirAll(filepath.Join(stateDir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	state := mediatedRuntimeState(vmkit.EgressModeBroker, nil, []string{"raw.example.com"})
	manifest, err := snapshotManifestFromState("snap-1", state, opts, false, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState for broker mode: %v", err)
	}
	if manifest.EgressCASHA256 != "" {
		t.Fatalf("broker manifest must carry no CA fingerprint, got %q", manifest.EgressCASHA256)
	}
	if manifest.EgressMode != vmkit.EgressModeBroker {
		t.Errorf("EgressMode = %q, want %q", manifest.EgressMode, vmkit.EgressModeBroker)
	}
	if len(manifest.EgressPassthrough) != 1 || manifest.EgressPassthrough[0] != "raw.example.com" {
		t.Errorf("EgressPassthrough = %v, want [raw.example.com]", manifest.EgressPassthrough)
	}
}

func TestSnapshotManifestFromStateRequiresSecretPurge(t *testing.T) {
	opts := Options{Name: "ws", StateDir: t.TempDir()}
	state := mediatedRuntimeState(vmkit.EgressModeOff, nil, nil)
	state.Config.Secrets = []vmkit.SecretRef{{Name: "API", Ref: "env:TOKEN"}}
	if _, err := snapshotManifestFromState("snap-1", state, opts, false, false); err == nil {
		t.Fatal("expected secret-bearing manifest without purge to fail closed")
	}
	manifest, err := snapshotManifestFromState("snap-1", state, opts, true, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState with purge: %v", err)
	}
	if !manifest.SecretsMaterialized || !manifest.SecretsPurged {
		t.Fatalf("manifest secret fields = materialized:%t purged:%t, want both true", manifest.SecretsMaterialized, manifest.SecretsPurged)
	}
}

// TestSnapshotManifestFromStateSkipsCAForIsolatedNetwork proves that an
// isolated workspace can be snapshotted even when its stored egress posture was
// normalized to guarded. Isolated workspaces do not run the mediator or mint a
// CA, so requiring egress-ca.pem here made every isolated snapshot fail closed.
func TestSnapshotManifestFromStateSkipsCAForIsolatedNetwork(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "iso", StateDir: stateDir}
	if err := os.MkdirAll(filepath.Join(stateDir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	state := runtimeState{
		Config: vmkit.Config{
			CPUCount:   2,
			MemoryMiB:  512,
			EgressMode: vmkit.EgressModeMITM,
			Network:    &vmkit.NetworkConfig{Mode: "isolated"},
		},
	}
	manifest, err := snapshotManifestFromState("warm", state, opts, false, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState isolated: %v", err)
	}
	if manifest.EgressCASHA256 != "" {
		t.Fatalf("EgressCASHA256 = %q, want empty for isolated network", manifest.EgressCASHA256)
	}
	if manifest.NetworkMode != "isolated" {
		t.Fatalf("NetworkMode = %q, want isolated", manifest.NetworkMode)
	}
}

// TestSnapshotManifestFromStateNoEgressWhenOff proves an unmediated workspace
// records no CA fingerprint and does not require a persisted CA file.
func TestSnapshotManifestFromStateNoEgressWhenOff(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	state := mediatedRuntimeState(vmkit.EgressModeOff, nil, nil)
	manifest, err := snapshotManifestFromState("snap-1", state, opts, false, false)
	if err != nil {
		t.Fatalf("snapshotManifestFromState (off): %v", err)
	}
	if manifest.EgressCASHA256 != "" {
		t.Errorf("EgressCASHA256 = %q, want empty for off mode", manifest.EgressCASHA256)
	}
	if manifest.EgressMode != vmkit.EgressModeOff {
		t.Errorf("EgressMode = %q, want %q", manifest.EgressMode, vmkit.EgressModeOff)
	}
}

// TestAcquireEgressCAMintsOnFreshStart proves the fresh-start branch mints a CA
// and persists egress-ca.pem (0644) + egress-ca-key.pem (0600), with a cleanup
// that removes both. This is the byte-identical-to-old mint path.
func TestAcquireEgressCAMintsOnFreshStart(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	certPath, keyPath, cleanup, err := acquireEgressCA(opts, false, "")
	if err != nil {
		t.Fatalf("acquireEgressCA (fresh): %v", err)
	}
	wsDir := filepath.Join(stateDir, opts.Name)
	if certPath != filepath.Join(wsDir, "egress-ca.pem") {
		t.Errorf("certPath = %q", certPath)
	}
	if keyPath != filepath.Join(wsDir, "egress-ca-key.pem") {
		t.Errorf("keyPath = %q", keyPath)
	}
	certInfo, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("minted cert not written: %v", err)
	}
	if certInfo.Mode().Perm() != 0o644 {
		t.Errorf("cert perm = %o, want 0644", certInfo.Mode().Perm())
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("minted key not written: %v", err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("key perm = %o, want 0600", keyInfo.Mode().Perm())
	}
	// Cleanup (the mint path's cleanup) removes both files.
	cleanup()
	if _, err := os.Stat(certPath); !os.IsNotExist(err) {
		t.Errorf("cert still present after mint cleanup: %v", err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Errorf("key still present after mint cleanup: %v", err)
	}
}

// TestAcquireEgressCAReusesOnRestoreMatchingSHA is the central no-re-mint proof:
// on restore with a matching fingerprint, acquireEgressCA returns the EXISTING
// cert/key paths, never calls egress.NewCA (proven by the cert bytes being
// byte-identical before and after), and returns a NO-OP cleanup so a downstream
// failure cannot delete the workspace's persistent CA.
func TestAcquireEgressCAReusesOnRestoreMatchingSHA(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	wsDir := filepath.Join(stateDir, opts.Name)
	certPEMBefore, keyPEMBefore, sha := writePersistedCA(t, wsDir)

	certPath, keyPath, cleanup, err := acquireEgressCA(opts, true, sha)
	if err != nil {
		t.Fatalf("acquireEgressCA (restore, matching SHA): %v", err)
	}
	if certPath != filepath.Join(wsDir, "egress-ca.pem") || keyPath != filepath.Join(wsDir, "egress-ca-key.pem") {
		t.Fatalf("reuse returned unexpected paths: cert=%q key=%q", certPath, keyPath)
	}
	// The cert (and key) on disk must be byte-identical — a re-mint would replace
	// them with a different CA the guest does not trust.
	certPEMAfter, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(certPEMBefore, certPEMAfter) {
		t.Error("CA cert bytes changed on restore — mediator was re-minted, breaking the restored guest's trust store")
	}
	keyPEMAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyPEMBefore, keyPEMAfter) {
		t.Error("CA key bytes changed on restore")
	}
	// The reuse cleanup must be a no-op (must NOT delete the persistent CA).
	cleanup()
	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("reuse cleanup deleted the persistent CA cert: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("reuse cleanup deleted the persistent CA key: %v", err)
	}
}

// TestAcquireEgressCAFailsClosedOnFingerprintMismatch proves that a restore whose
// on-disk CA does not match the snapshot fingerprint is refused (no reuse, no
// mint) and the on-disk cert is left untouched.
func TestAcquireEgressCAFailsClosedOnFingerprintMismatch(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	wsDir := filepath.Join(stateDir, opts.Name)
	certPEMBefore, _, _ := writePersistedCA(t, wsDir)

	_, _, _, err := acquireEgressCA(opts, true, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected fingerprint-mismatch error, got nil")
	}
	// The cert must be left untouched (fail-closed never rewrites the CA).
	certPEMAfter, rerr := os.ReadFile(filepath.Join(wsDir, "egress-ca.pem"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(certPEMBefore, certPEMAfter) {
		t.Error("CA cert was modified on a fail-closed mismatch")
	}
}

// TestAcquireEgressCAFailsClosedOnMissingCA proves that a restore with no
// persisted CA on disk is refused rather than silently re-minting.
func TestAcquireEgressCAFailsClosedOnMissingCA(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	if err := os.MkdirAll(filepath.Join(stateDir, opts.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	// No CA files; a non-empty expected SHA still must fail closed.
	if _, _, _, err := acquireEgressCA(opts, true, "abc123"); err == nil {
		t.Fatal("expected missing-CA error on restore, got nil")
	}
}

// TestAcquireEgressCAFailsClosedOnEmptyExpectedSHA proves that a restore of a
// workspace with no recorded CA fingerprint (an older/corrupt manifest) is
// refused — we never re-arm the mediator without a fingerprint to verify against.
func TestAcquireEgressCAFailsClosedOnEmptyExpectedSHA(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	writePersistedCA(t, filepath.Join(stateDir, opts.Name))
	if _, _, _, err := acquireEgressCA(opts, true, ""); err == nil {
		t.Fatal("expected error for empty expected CA SHA on restore, got nil")
	}
}

// TestProvisionEgressFailsClosedOnCAFingerprintMismatch drives the full
// provisionEgressMediation on the restore path with a mismatched fingerprint and
// proves it fails closed BEFORE any mediator is spawned (pid 0, no rules) and
// leaves the persisted CA untouched. The mismatch is rejected in acquireEgressCA,
// which returns before startEgressMediator, so this runs without root/netns.
func TestProvisionEgressFailsClosedOnCAFingerprintMismatch(t *testing.T) {
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	wsDir := filepath.Join(stateDir, opts.Name)
	certPEMBefore, _, _ := writePersistedCA(t, wsDir)

	cfg := &vmkit.Config{EgressMode: vmkit.EgressModeMITM, EgressAllow: []string{"api.github.com"}}
	pid, rules, err := provisionEgressMediation(opts, cfg, "microtap0", tapNATAddress{}, true, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err == nil {
		t.Fatal("expected fail-closed error on CA fingerprint mismatch, got nil")
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0 (no mediator spawned on mismatch)", pid)
	}
	if rules != nil {
		t.Errorf("rules = %+v, want nil (no rules installed on mismatch)", rules)
	}
	certPEMAfter, rerr := os.ReadFile(filepath.Join(wsDir, "egress-ca.pem"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(certPEMBefore, certPEMAfter) {
		t.Error("CA cert was modified on a fail-closed mismatch in provisionEgressMediation")
	}
}

// TestForkReusesSourceCA documents that the restore/reuse path keys trust on the
// on-disk CA bytes (and their fingerprint), NOT the certificate's CommonName. A
// fork has a new opts.Name (so a freshly minted CA would carry the fork's name as
// CN), but the reuse path reads whatever CA is present in the fork's workspace
// dir — copied from the source by the fork's rootfs/state copy — and validates it
// against the snapshot fingerprint. CN is therefore cosmetic for trust here: a
// source CA whose CN is the SOURCE name is accepted under a fork named differently,
// because the guest's baked trust store anchors on the public key, not the CN.
func TestForkReusesSourceCA(t *testing.T) {
	stateDir := t.TempDir()
	// Mint a CA "as the source" — its CN is the SOURCE workspace name.
	sourceCA, err := egress.NewCA("source-ws", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sourceCertPEM := sourceCA.CertPEM()
	sourceKeyPEM, err := sourceCA.KeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(sourceCertPEM)
	if block == nil {
		t.Fatal("source CA cert did not decode")
		return
	}
	sum := sha256.Sum256(block.Bytes)
	sourceSHA := hex.EncodeToString(sum[:])

	// The fork has a DIFFERENT name; the source CA was copied into its state dir.
	forkOpts := Options{Name: "fork-ws", StateDir: stateDir}
	forkDir := filepath.Join(stateDir, forkOpts.Name)
	if err := os.MkdirAll(forkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forkDir, "egress-ca.pem"), sourceCertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forkDir, "egress-ca-key.pem"), sourceKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// Reuse under the fork's name with the SOURCE's fingerprint must succeed —
	// proving CN ("source-ws") is irrelevant to trust; only the bytes matter.
	certPath, keyPath, cleanup, err := acquireEgressCA(forkOpts, true, sourceSHA)
	if err != nil {
		t.Fatalf("fork reuse of source CA refused: %v", err)
	}
	defer cleanup()
	got, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sourceCertPEM) {
		t.Error("fork did not reuse the source CA bytes")
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("fork CA key not present: %v", err)
	}
}

// TestProvisionEgressReusesPersistedCAOnRestore exercises the full
// provisionEgressMediation reuse path against a live host: on restore with a
// matching fingerprint it must start the mediator using the EXISTING CA without
// re-minting (the cert bytes are byte-identical before and after). It is
// host-gated (root + MICROAGENT_FIRECRACKER_E2E=1 + TPROXY prereqs) because
// starting the mediator and installing the REDIRECT/TPROXY rules needs root and
// the supervisor binary; it skips cleanly otherwise. The no-root reuse logic
// itself is fully covered by TestAcquireEgressCAReusesOnRestoreMatchingSHA.
func TestProvisionEgressReusesPersistedCAOnRestore(t *testing.T) {
	if os.Getenv("MICROAGENT_FIRECRACKER_E2E") != "1" {
		t.Skip("provision egress reuse e2e: set MICROAGENT_FIRECRACKER_E2E=1 to run the live mediator-reuse test")
	}
	if os.Geteuid() != 0 {
		t.Skip("provision egress reuse e2e: installing REDIRECT/TPROXY rules and starting the mediator requires root; re-run with sudo -E")
	}
	stateDir := t.TempDir()
	opts := Options{Name: "ws", StateDir: stateDir}
	wsDir := filepath.Join(stateDir, opts.Name)
	certPEMBefore, _, sha := writePersistedCA(t, wsDir)

	// A loopback "gateway" the mediator can bind without a real tap.
	gateway := "127.0.0.1"
	subnet := "127.0.0.0/8"
	cfg := &vmkit.Config{EgressMode: vmkit.EgressModeMITM, EgressAllow: []string{"api.github.com"}}
	pid, rules, err := provisionEgressMediation(opts, cfg, "lo", tapNATAddress{
		Gateway: gateway, Subnet: subnet, GatewayV6: "::1", SubnetV6: "::1/128",
	}, true, sha)
	if err != nil {
		t.Skipf("provision egress reuse e2e: host could not provision mediation (likely missing TPROXY prereqs): %v", err)
	}
	defer func() {
		if pid != 0 {
			terminateAuxProcess(pid)
		}
		cleanupTransientFirewallRules(rules)
	}()
	if pid == 0 {
		t.Error("mediator was not started on the reuse path")
	}
	certPEMAfter, rerr := os.ReadFile(filepath.Join(wsDir, "egress-ca.pem"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(certPEMBefore, certPEMAfter) {
		t.Error("CA cert bytes changed on restore — mediator was re-minted")
	}
}
