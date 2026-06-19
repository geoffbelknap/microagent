package vmkit

import "testing"

// TestNormalizeBackendFirecrackerAlias locks the deprecated "firecracker"
// alias to the canonical linux-kvm id (and confirms case/whitespace handling
// and pass-through of the other canonical ids).
func TestNormalizeBackendFirecrackerAlias(t *testing.T) {
	cases := map[string]string{
		"firecracker":    BackendLinuxKVM,
		"linux-kvm":      BackendLinuxKVM,
		" FireCracker ":  BackendLinuxKVM,
		"apple-vf":       BackendAppleVF,
		"windows-hyperv": BackendWindowsHyperV,
		"unknown":        "unknown",
	}
	for in, want := range cases {
		if got := NormalizeBackend(in); got != want {
			t.Errorf("NormalizeBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBackendCapabilitiesAliasMatchesLinuxKVM ensures the alias resolves to the
// exact same behavioral capabilities as linux-kvm (so a script that still
// passes --backend firecracker gets identical runtime behavior).
func TestBackendCapabilitiesAliasMatchesLinuxKVM(t *testing.T) {
	if BackendCapabilities("firecracker") != BackendCapabilities(BackendLinuxKVM) {
		t.Error("firecracker alias must resolve to identical linux-kvm capabilities")
	}
}
