// Package egressprereq holds the host-level prerequisites that UDP egress
// mediation (TPROXY) depends on. These values are the single source of truth
// shared by three consumers so they can never drift apart:
//
//   - the Firecracker supervisor, which VERIFIES them (fail-closed) before
//     starting a nat-mode mediated workspace;
//   - `microagent host setup-networking`, which PROVISIONS them once as root;
//   - `microagent doctor`, which REPORTS whether the kernel modules are loaded.
//
// The package is a dependency-free leaf (no other microagent imports, no build
// tag) so it compiles on every platform. The privileged operations that act on
// these values (modprobe, netlink, sysctl writes) live behind Linux build tags
// in the consuming packages; only the constants and pure decision logic live
// here.
package egressprereq

import "strings"

// TProxyMark is the fwmark stamped on TPROXY-steered datagrams. The ip rule
// (fwmark TProxyMark -> table TProxyTable) plus the local route in that table
// deliver the marked packet to the mediator's transparent socket on lo. It is a
// fixed value (not per-workspace): in user (pasta) mode it is netns-local, and
// in nat mode it is host-global infrastructure provisioned once, so a single
// stable value is correct and collision-free across workspaces.
const TProxyMark uint32 = 0x1

// TProxyTable is the policy routing table that holds the
// `local 0.0.0.0/0 dev lo` route behind the fwmark rule.
const TProxyTable = 100

// TProxyModules are the kernel modules TPROXY egress mediation requires. A
// rootless user namespace cannot modprobe, so these must be loaded by root once
// (via `host setup-networking`). They are needed for BOTH user and nat egress
// modes. A module compiled into the kernel (built-in, not loadable) counts as
// present — see ParseLoadedModules / builtin detection in the consumers.
var TProxyModules = []string{
	"nft_tproxy",
	"nf_tproxy_ipv4",
	"xt_socket",
	"nf_socket_ipv4",
}

// TProxySysctls are the kernel knobs TPROXY delivery to a local transparent
// socket on lo requires. In user (pasta) mode these are set per-netns at runtime
// and are not a host concern; in nat mode they are host-global infrastructure
// that `host setup-networking` provisions and the supervisor verifies.
//
//   - route_localnet: allow routing of 0.0.0.0/8 (the local TPROXY route on lo)
//   - rp_filter=0: the spoofed-source reply leg would otherwise be dropped by
//     reverse-path filtering
//   - accept_local: accept packets whose source is a local address
//   - ip_forward: guest egress is forwarded through the host
var TProxySysctls = map[string]string{
	"/proc/sys/net/ipv4/conf/all/route_localnet": "1",
	"/proc/sys/net/ipv4/conf/all/rp_filter":      "0",
	"/proc/sys/net/ipv4/conf/all/accept_local":   "1",
	"/proc/sys/net/ipv4/ip_forward":              "1",
}

// ParseLoadedModules extracts the set of loaded module names from the contents
// of /proc/modules. Each line begins with the module name followed by
// whitespace; blank lines are ignored. The returned set has normalized
// (underscore) names so callers can probe membership directly.
func ParseLoadedModules(procModules []byte) map[string]bool {
	loaded := make(map[string]bool)
	for _, line := range strings.Split(string(procModules), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		loaded[NormalizeModuleName(fields[0])] = true
	}
	return loaded
}

// NormalizeModuleName maps the kernel's two interchangeable spellings (the
// modprobe alias uses '-', /proc/modules and /sys use '_') to a single form so
// presence checks are spelling-insensitive.
func NormalizeModuleName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// MissingModules returns the configured TProxyModules that are neither present
// in the loaded set nor reported built-in. The result preserves TProxyModules
// order so callers report missing modules deterministically. An empty result
// means every module is available.
//
// loaded is typically ParseLoadedModules(/proc/modules); isBuiltin reports
// whether a (normalized) module name is compiled into the kernel. isBuiltin may
// be nil, in which case no module is treated as built-in.
func MissingModules(loaded map[string]bool, isBuiltin func(string) bool) []string {
	var missing []string
	for _, mod := range TProxyModules {
		norm := NormalizeModuleName(mod)
		if loaded[norm] {
			continue
		}
		if isBuiltin != nil && isBuiltin(norm) {
			continue
		}
		missing = append(missing, mod)
	}
	return missing
}

// ModulesReady reports whether every required TPROXY module is loaded or
// built-in. It is the boolean form of MissingModules for the doctor PASS/WARN
// decision and the --check report.
func ModulesReady(loaded map[string]bool, isBuiltin func(string) bool) bool {
	return len(MissingModules(loaded, isBuiltin)) == 0
}
