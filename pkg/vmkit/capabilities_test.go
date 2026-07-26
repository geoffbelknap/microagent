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
				NetworkPublish:       true,
				OfflineFileCopy:      true,
				OwnsRuntimeState:     true,
				DetachedStartCommand: "start",
				ShellNetwork:         "tcp",
				ShellReadinessProbe:  true,
				Snapshot:             true,
				PauseResume:          true,
				SnapshotCreate:       true,
				SnapshotRestore:      true,
				SnapshotFork:         true,
				BrokerEndpoints:      true,
			},
		},
		{
			backend: BackendAppleVF,
			want: Capabilities{
				StructuredExec:         true,
				LiveNetworkApply:       true,
				NetworkPublish:         true,
				OfflineFileCopy:        true,
				DetachedStartCommand:   "run",
				DetachedHostSupervisor: true,
				ShellNetwork:           "tcp",
				ShellReadinessProbe:    true,
				Snapshot:               true,
				PauseResume:            true,
				SnapshotCreate:         true,
				SnapshotRestore:        true,
				SnapshotFork:           true,
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

func TestSnapshotAggregateMatchesOperationFacets(t *testing.T) {
	for _, backend := range []string{BackendLinuxKVM, BackendAppleVF, "unknown"} {
		caps := BackendCapabilities(backend)
		want := caps.PauseResume && caps.SnapshotCreate && caps.SnapshotRestore && caps.SnapshotFork
		if caps.Snapshot != want {
			t.Errorf("%s Snapshot = %v, want conjunction of operation facets (%v)", backend, caps.Snapshot, want)
		}
	}
}
