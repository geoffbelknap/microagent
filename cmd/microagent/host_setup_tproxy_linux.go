//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// The TPROXY host prerequisites this command provisions (Task 3.3c) are the same
// values the Firecracker supervisor verifies (fail-closed) before starting a
// nat-mode mediated workspace: the fwmark, routing table, and sysctls. Both
// sides read them from pkg/egressprereq so the provisioner and the verifier can
// never drift. See pkg/supervisors/firecracker/egress_linux.go.

// loadModule and the netlink/sysctl seams below are package vars so unit tests
// can intercept the privileged operations instead of requiring root.
var (
	// loadModule loads one kernel module by name. A built-in (already-compiled)
	// module makes `modprobe` succeed as a no-op; an already-loaded module is
	// likewise a no-op. Only a genuine failure (module truly absent and not
	// built-in) returns an error.
	loadModule = defaultLoadModule

	// netlinkRuleAdd / netlinkRuleDel / netlinkRouteAdd / netlinkRouteDel /
	// netlinkLinkByName mirror the supervisor's netlink helpers so the CLI can
	// provision the same ip rule + local route without importing the supervisor
	// binary's package.
	netlinkRuleAdd    = netlink.RuleAdd
	netlinkRuleDel    = netlink.RuleDel
	netlinkRouteAdd   = netlink.RouteAdd
	netlinkRouteDel   = netlink.RouteDel
	netlinkLinkByName = netlink.LinkByName

	// readSysctl / writeSysctl seam the /proc/sys reads and writes.
	readSysctl  = os.ReadFile
	writeSysctl = func(path, value string) error { return os.WriteFile(path, []byte(value+"\n"), 0o644) }
)

// printTProxyNATCheck reports (without mutating) whether the nat-mode TPROXY
// host prerequisites — the four sysctls and the fwmark ip rule -> table route —
// are present. It uses the same egressprereq values the supervisor verifies, so
// "present" here means a nat-mode mediated workspace would pass the supervisor's
// fail-closed verify. The kernel-module status is reported by the networking
// section (doctor) above; this covers the nat routing prereqs.
func printTProxyNATCheck(stdout *os.File) {
	missing := tproxyNATPrereqsMissing()
	if len(missing) == 0 {
		fmt.Fprintln(stdout, "Egress TPROXY nat prereqs: present (sysctls + fwmark route)")
		return
	}
	fmt.Fprintf(stdout, "Egress TPROXY nat prereqs: missing (%s)\n", strings.Join(missing, "; "))
	fmt.Fprintln(stdout, "  run `microagent host setup-networking` to provision nat-mode UDP egress mediation")
}

// tproxyNATPrereqsMissing returns human descriptions of any absent nat prereq,
// in a stable order. Empty means every prereq is present.
func tproxyNATPrereqsMissing() []string {
	var missing []string
	for _, path := range sortedSysctlPaths() {
		want := egressprereq.TProxySysctls[path]
		data, err := readSysctl(path)
		if err != nil {
			missing = append(missing, fmt.Sprintf("sysctl %s unreadable", path))
			continue
		}
		if got := trimSysctlValue(data); got != want {
			missing = append(missing, fmt.Sprintf("sysctl %s is %q want %q", path, got, want))
		}
	}
	if !tproxyRulePresent() {
		missing = append(missing, fmt.Sprintf("ip rule fwmark %#x -> table %d absent", egressprereq.TProxyMark, egressprereq.TProxyTable))
	}
	return missing
}

// tproxyRulePresent reports whether the fwmark -> table ip rule exists. Seamed
// via netlinkRuleList for tests.
func tproxyRulePresent() bool {
	rules, err := netlinkRuleList(netlink.FAMILY_V4)
	if err != nil {
		return false
	}
	for _, r := range rules {
		if r.Mark == egressprereq.TProxyMark && r.Table == egressprereq.TProxyTable {
			return true
		}
	}
	return false
}

var netlinkRuleList = netlink.RuleList

// sortedSysctlPaths returns the TProxySysctls keys in a stable order so the
// --check report is deterministic.
func sortedSysctlPaths() []string {
	paths := make([]string, 0, len(egressprereq.TProxySysctls))
	for p := range egressprereq.TProxySysctls {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func defaultLoadModule(name string) error {
	out, err := exec.Command("modprobe", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("modprobe %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyTProxyPrereqs loads the TPROXY kernel modules and provisions the nat-mode
// host-global routing prerequisites (ip rule, local route, sysctls). Idempotent:
// already-present state is left untouched. Called from applyHostNetworking under
// root.
func applyTProxyPrereqs() error {
	if _, err := exec.LookPath("modprobe"); err != nil {
		return fmt.Errorf("modprobe not found (install kmod): %w", err)
	}
	for _, mod := range egressprereq.TProxyModules {
		if err := loadModule(mod); err != nil {
			return fmt.Errorf("load TPROXY module: %w", err)
		}
	}
	if err := setTProxySysctls(); err != nil {
		return err
	}
	if err := addTProxyRouting(); err != nil {
		return err
	}
	return nil
}

// revertTProxyPrereqs removes the ip rule + local route this command added and
// restores the sysctls to their kernel defaults. It does NOT unload the kernel
// modules: other things on the host may depend on them, and unloading is not the
// inverse of `modprobe` (the module may have been built-in or pre-loaded).
func revertTProxyPrereqs() error {
	delTProxyRouting() // best-effort, idempotent
	return restoreTProxySysctls()
}

// setTProxySysctls writes each TPROXY sysctl only when it does not already hold
// the desired value, matching the supervisor's writeSysctlIfNeeded.
func setTProxySysctls() error {
	for path, want := range egressprereq.TProxySysctls {
		data, err := readSysctl(path)
		if err != nil {
			return fmt.Errorf("inspect %s for egress TPROXY: %w", path, err)
		}
		if trimSysctlValue(data) == want {
			continue
		}
		if err := writeSysctl(path, want); err != nil {
			return fmt.Errorf("set %s for egress TPROXY: %w", path, err)
		}
	}
	return nil
}

// tproxySysctlReverts are the kernel-default values to restore on --revert. They
// match the documented defaults for these knobs (route_localnet/accept_local
// default off, rp_filter defaults to strict (1), ip_forward defaults off).
var tproxySysctlReverts = map[string]string{
	"/proc/sys/net/ipv4/conf/all/route_localnet": "0",
	"/proc/sys/net/ipv4/conf/all/rp_filter":      "1",
	"/proc/sys/net/ipv4/conf/all/accept_local":   "0",
	"/proc/sys/net/ipv4/ip_forward":              "0",
}

func restoreTProxySysctls() error {
	for path, want := range tproxySysctlReverts {
		// Only restore knobs we actually provision (the keys are identical).
		if _, ours := egressprereq.TProxySysctls[path]; !ours {
			continue
		}
		if err := writeSysctl(path, want); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restore %s: %w", path, err)
		}
	}
	return nil
}

// tproxyRule builds the policy rule (fwmark -> table) that the local route lives
// behind — identical to the supervisor's egressTProxyRule.
func tproxyRule() *netlink.Rule {
	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V4
	rule.Mark = egressprereq.TProxyMark
	rule.Table = egressprereq.TProxyTable
	return rule
}

// tproxyLocalRoute builds the `local 0.0.0.0/0 dev lo table <table>` route —
// identical to the supervisor's egressTProxyLocalRoute.
func tproxyLocalRoute(loIndex int) *netlink.Route {
	_, defaultNet, _ := net.ParseCIDR("0.0.0.0/0")
	return &netlink.Route{
		LinkIndex: loIndex,
		Dst:       defaultNet,
		Table:     egressprereq.TProxyTable,
		Type:      unix.RTN_LOCAL,
		Scope:     unix.RT_SCOPE_HOST,
	}
}

// addTProxyRouting installs the ip rule and local route idempotently (EEXIST is
// not an error). On a route failure after the rule was added, it unwinds the
// rule so it leaves no orphaned host state — mirrors the supervisor.
func addTProxyRouting() error {
	lo, err := netlinkLinkByName("lo")
	if err != nil {
		return fmt.Errorf("inspect lo for egress TPROXY routing: %w", err)
	}
	if err := netlinkRuleAdd(tproxyRule()); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("add egress TPROXY ip rule fwmark %#x -> table %d: %w", egressprereq.TProxyMark, egressprereq.TProxyTable, err)
	}
	if err := netlinkRouteAdd(tproxyLocalRoute(lo.Attrs().Index)); err != nil && !isAlreadyExists(err) {
		_ = netlinkRuleDel(tproxyRule())
		return fmt.Errorf("add egress TPROXY local route in table %d: %w", egressprereq.TProxyTable, err)
	}
	return nil
}

// delTProxyRouting removes the local route and ip rule. Best-effort/idempotent.
func delTProxyRouting() {
	if lo, err := netlinkLinkByName("lo"); err == nil {
		_ = netlinkRouteDel(tproxyLocalRoute(lo.Attrs().Index))
	}
	_ = netlinkRuleDel(tproxyRule())
}

// isAlreadyExists recognizes the EEXIST netlink returns when the ip rule/route
// is already present, so a repeat provisioning converges instead of failing.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrExist) || errors.Is(err, unix.EEXIST) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "file exists")
}

func trimSysctlValue(data []byte) string {
	return strings.TrimRight(string(data), "\n \t\r")
}
