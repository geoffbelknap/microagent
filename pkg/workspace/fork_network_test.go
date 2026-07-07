package workspace

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestAdoptSnapshotNetworkPreservesForwards proves a fork adopts the
// snapshot's baked addressing while keeping the caller's port forwards —
// forwards are realized host-side by the fork's own pasta/forwarder, so the
// source's addressing must not silently drop them (a hibernate/wake cycle
// would otherwise strand any service the workload exposes).
func TestAdoptSnapshotNetworkPreservesForwards(t *testing.T) {
	requested := vmkit.NetworkConfig{
		Mode: "user",
		PortForwards: []vmkit.PortForward{
			{Protocol: "tcp", Host: "127.0.0.1", HostPort: 28080, GuestPort: 8080},
		},
	}
	manifest := vmkit.SnapshotManifest{
		NetworkMode:    "user",
		NetworkIP:      "10.43.7.2/29",
		NetworkGateway: "10.43.7.1",
		NetworkSubnet:  "10.43.7.0/29",
	}

	got := adoptSnapshotNetwork(requested, manifest)
	if got.Mode != "user" || got.IP != "10.43.7.2/29" || got.Gateway != "10.43.7.1" || got.Subnet != "10.43.7.0/29" {
		t.Fatalf("addressing not adopted from manifest: %+v", got)
	}
	if len(got.PortForwards) != 1 || got.PortForwards[0].HostPort != 28080 || got.PortForwards[0].GuestPort != 8080 {
		t.Fatalf("caller port forwards dropped: %+v", got.PortForwards)
	}
}
