package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestStatusDoesNotTreatStartedRootfsMutationAsDivergence(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	initPath := filepath.Join(dir, "microagent-init")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("kernel-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initPath, []byte("init-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Name:          "research",
		StateDir:      dir,
		Backend:       HostBackend(),
		KernelPath:    kernelPath,
		GuestInitPath: initPath,
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}
	result := Result{
		Workspace:  "research",
		RootfsPath: rootfsPath,
		Image: rootfs.Provenance{
			ImageRef:    "docker.io/library/busybox:1.36",
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
		},
	}
	verification, err := BuildVerification(opts, result)
	if err != nil {
		t.Fatal(err)
	}
	opts.Verification = &verification
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	req, err := Request(opts, "run", rootfsPath, "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	req.Config.KernelPath = kernelPath
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verification == nil || !resp.Verification.OK {
		t.Fatalf("verification = %#v, want ok after started rootfs mutation", resp.Verification)
	}
	if resp.Verification.Rootfs == nil || resp.Verification.Rootfs.RecordedSHA256 == "" || resp.Verification.Rootfs.SHA256 == "" {
		t.Fatalf("rootfs verification details missing: %#v", resp.Verification)
	}
}

// TestApplyManifestNormalizesEgressModeForStart asserts the start path's
// manifest-load chokepoint carries the secure default into the request: a
// manifest with an unspecified egress mode yields a started workspace that is
// guarded (mediator provisioned + CA-cert vsock listener re-allocated), mirroring
// create. Start() does applyManifest(&opts) -> Request(opts); this exercises that
// composition without spinning up a VM. INV1 (start side).
func TestApplyManifestNormalizesEgressModeForStart(t *testing.T) {
	opts := Options{
		Name:       "agent-1",
		Backend:    vmkit.BackendLinuxKVM,
		KernelPath: "/k",
		StateDir:   t.TempDir(),
		MemoryMiB:  512,
		CPUCount:   2,
		Network:    vmkit.NetworkConfig{Mode: "user"},
	}
	// Manifest with an unspecified egress mode (broker is now the default).
	applyManifest(&opts, Manifest{Network: NetworkSpec{Mode: "user"}})
	if opts.EgressMode != vmkit.EgressModeBroker {
		t.Fatalf("applyManifest left EgressMode = %q, want %q", opts.EgressMode, vmkit.EgressModeBroker)
	}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !vmkit.EgressMediationOn(req.Config.EgressMode) {
		t.Fatalf("started workspace not mediated: EgressMode = %q", req.Config.EgressMode)
	}
	// The broker default mediates but forges no certificates, so it allocates
	// no CA-cert listener (unlike the retired guarded default).
	if req.Config.CACertPort != 0 {
		t.Fatalf("started broker-default workspace CACertPort = %d, want 0", req.Config.CACertPort)
	}
	if hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("started broker-default workspace must not allocate a CA-cert listener: %#v", req.Config.VsockListeners)
	}
}

// TestApplyManifestPreservesOffForStart asserts an explicit "off" manifest is not
// silently promoted to mediated on start. INV2 (start side).
func TestApplyManifestPreservesOffForStart(t *testing.T) {
	opts := Options{
		Name:       "agent-1",
		Backend:    vmkit.BackendLinuxKVM,
		KernelPath: "/k",
		StateDir:   t.TempDir(),
		MemoryMiB:  512,
		CPUCount:   2,
		Network:    vmkit.NetworkConfig{Mode: "user"},
	}
	applyManifest(&opts, Manifest{Network: NetworkSpec{Mode: "user"}, EgressMode: vmkit.EgressModeOff})
	if opts.EgressMode != vmkit.EgressModeOff {
		t.Fatalf("applyManifest changed off mode to %q", opts.EgressMode)
	}
	req, err := Request(opts, "run", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if vmkit.EgressMediationOn(req.Config.EgressMode) {
		t.Fatalf("off workspace should not be mediated on start")
	}
	if req.Config.CACertPort != 0 || hasCACertListener(req.Config.VsockListeners) {
		t.Fatalf("off workspace allocated CA-cert listener on start: port=%d listeners=%#v", req.Config.CACertPort, req.Config.VsockListeners)
	}
}

// TestCopyForkEgressCABringsCAIntoForkDir proves that forking a mediated
// workspace copies the source's persisted egress CA cert+key into the fork's
// workspace dir (with the correct perms), so the fork's restore path can reuse
// the SAME CA the guest's baked trust store was built against rather than
// failing closed or re-minting.
func TestCopyForkEgressCABringsCAIntoForkDir(t *testing.T) {
	stateDir := t.TempDir()
	srcDir := filepath.Join(stateDir, "source")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	certBytes := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	keyBytes := []byte("-----BEGIN EC PRIVATE KEY-----\nfake\n-----END EC PRIVATE KEY-----\n")
	if err := os.WriteFile(filepath.Join(srcDir, "egress-ca.pem"), certBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "egress-ca-key.pem"), keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copyForkEgressCA(stateDir, "source", "fork"); err != nil {
		t.Fatalf("copyForkEgressCA: %v", err)
	}
	forkDir := filepath.Join(stateDir, "fork")
	gotCert, err := os.ReadFile(filepath.Join(forkDir, "egress-ca.pem"))
	if err != nil {
		t.Fatalf("fork CA cert not copied: %v", err)
	}
	if string(gotCert) != string(certBytes) {
		t.Error("fork CA cert bytes differ from source")
	}
	keyInfo, err := os.Stat(filepath.Join(forkDir, "egress-ca-key.pem"))
	if err != nil {
		t.Fatalf("fork CA key not copied: %v", err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("fork CA key perm = %o, want 0600", keyInfo.Mode().Perm())
	}
}

// TestCopyForkEgressCAFailsClosedWhenSourceMissing proves a mediated fork whose
// source CA is gone is refused rather than booting with no reusable CA.
func TestCopyForkEgressCAFailsClosedWhenSourceMissing(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "source"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyForkEgressCA(stateDir, "source", "fork"); err == nil {
		t.Fatal("expected error forking mediated workspace with missing source CA, got nil")
	}
}
