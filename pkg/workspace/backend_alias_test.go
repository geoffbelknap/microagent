package workspace

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestFirecrackerAliasKernelPathMatchesLinuxKVM guards the install-vs-run path
// divergence: a kernel installed under the deprecated --backend firecracker
// alias must land at the same writable path a linux-kvm run later reads.
func TestFirecrackerAliasKernelPathMatchesLinuxKVM(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		if got, want := WritableKernelPath("firecracker", arch), WritableKernelPath(vmkit.BackendLinuxKVM, arch); got != want {
			t.Errorf("WritableKernelPath(firecracker,%s)=%q, want %q", arch, got, want)
		}
	}
}

// TestValidateHostBackendAcceptsFirecrackerAlias confirms the gate accepts the
// deprecated alias wherever linux-kvm is the host backend.
func TestValidateHostBackendAcceptsFirecrackerAlias(t *testing.T) {
	if HostBackend() != vmkit.BackendLinuxKVM {
		t.Skipf("host backend is %q, not linux-kvm", HostBackend())
	}
	if err := ValidateHostBackend("firecracker"); err != nil {
		t.Errorf("ValidateHostBackend(firecracker) on a linux-kvm host: %v", err)
	}
}
