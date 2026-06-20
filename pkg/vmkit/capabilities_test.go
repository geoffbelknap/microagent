package vmkit

import "testing"

func TestBackendCapabilitiesMatrix(t *testing.T) {
	tests := []struct {
		backend string
		want    Capabilities
	}{
		{
			backend: BackendLinuxKVM,
			want: Capabilities{
				StructuredExec:       true,
				LiveNetworkApply:     true,
				OwnsRuntimeState:     true,
				DetachedStartCommand: "start",
				ShellNetwork:         "tcp",
				ShellReadinessProbe:  true,
				Snapshot:             true,
			},
		},
		{
			backend: BackendAppleVF,
			want: Capabilities{
				StructuredExec:         true,
				LiveNetworkApply:       true,
				DetachedStartCommand:   "run",
				DetachedHostSupervisor: true,
				ShellNetwork:           "tcp",
				ShellReadinessProbe:    true,
			},
		},
		{
			backend: BackendWindowsHyperV,
			want: Capabilities{
				StructuredExec:       true,
				LiveNetworkApply:     true,
				VHDRootfs:            true,
				OwnsRuntimeState:     true,
				DetachedStartCommand: "start",
				ShellNetwork:         "hvsock",
				ShellReadinessProbe:  true,
				SCSIBlockDevices:     true,
				GuestMediatedCopy:    true,
				NatReliablyMediated:  true,
			},
		},
	}
	for _, tt := range tests {
		if got := BackendCapabilities(tt.backend); got != tt.want {
			t.Errorf("BackendCapabilities(%q) = %+v, want %+v", tt.backend, got, tt.want)
		}
	}
}

func TestBackendCapabilitiesUnknownBackendFailsClosed(t *testing.T) {
	for _, backend := range []string{"", "docker", "qemu"} {
		if got := BackendCapabilities(backend); got != (Capabilities{}) {
			t.Errorf("BackendCapabilities(%q) = %+v, want zero value", backend, got)
		}
	}
}
