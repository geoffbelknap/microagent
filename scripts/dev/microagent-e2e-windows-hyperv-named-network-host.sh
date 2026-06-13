#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# Windows-hyperv user-defined named networks: `network create`, two workspaces
# join one private network with --network-name, get distinct stable 10.44.x
# member IPs on a shared HNS network, and one reaches the other by IP. The named
# network is private (no NAT egress), realized as a static-IPAM HNS network.
# Creating the private HNS network needs an elevated host, so gate on elevation
# the same way the NAT networking segments do; honest skip when unavailable.
cd "$ROOT"
e2e_is_windows || e2e_skip "windows-hyperv host probes require a Windows host"
e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
e2e_is_windows_elevated || e2e_skip "named-network HNS realization requires an elevated (administrator) shell"

e2e_windows_hyperv_host_probe MICROAGENT_WINDOWS_HYPERV_SMOKE ./cmd/microagent 'TestWindowsHyperVNamedNetworkSmoke$'
