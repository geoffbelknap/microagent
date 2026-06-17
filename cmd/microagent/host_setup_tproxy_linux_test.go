//go:build linux

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/vishvananda/netlink"
)

// withTProxyStubs swaps every privileged seam the TPROXY provisioning uses for
// in-memory fakes so the decision logic runs without root, modprobe, or netlink.
// It returns the recorded sysctl state (path->value) and the slice of modules
// modprobe was asked to load.
func withTProxyStubs(t *testing.T) (sysctls map[string]string, loaded *[]string) {
	t.Helper()
	old := struct {
		loadModule  func(string) error
		ruleAdd     func(*netlink.Rule) error
		ruleDel     func(*netlink.Rule) error
		routeAdd    func(*netlink.Route) error
		routeDel    func(*netlink.Route) error
		linkByName  func(string) (netlink.Link, error)
		readSysctl  func(string) ([]byte, error)
		writeSysctl func(string, string) error
		ruleList    func(int) ([]netlink.Rule, error)
	}{loadModule, netlinkRuleAdd, netlinkRuleDel, netlinkRouteAdd, netlinkRouteDel, netlinkLinkByName, readSysctl, writeSysctl, netlinkRuleList}
	t.Cleanup(func() {
		loadModule = old.loadModule
		netlinkRuleAdd = old.ruleAdd
		netlinkRuleDel = old.ruleDel
		netlinkRouteAdd = old.routeAdd
		netlinkRouteDel = old.routeDel
		netlinkLinkByName = old.linkByName
		readSysctl = old.readSysctl
		writeSysctl = old.writeSysctl
		netlinkRuleList = old.ruleList
	})

	state := map[string]string{
		"/proc/sys/net/ipv4/conf/all/route_localnet": "0",
		"/proc/sys/net/ipv4/conf/all/rp_filter":      "1",
		"/proc/sys/net/ipv4/conf/all/accept_local":   "0",
		"/proc/sys/net/ipv4/ip_forward":              "0",
	}
	var mods []string
	loadModule = func(name string) error { mods = append(mods, name); return nil }
	netlinkLinkByName = func(string) (netlink.Link, error) {
		return &netlink.Device{LinkAttrs: netlink.LinkAttrs{Index: 1, Name: "lo"}}, nil
	}
	netlinkRuleAdd = func(*netlink.Rule) error { return nil }
	netlinkRuleDel = func(*netlink.Rule) error { return nil }
	netlinkRouteAdd = func(*netlink.Route) error { return nil }
	netlinkRouteDel = func(*netlink.Route) error { return nil }
	netlinkRuleList = func(int) ([]netlink.Rule, error) { return nil, nil }
	readSysctl = func(path string) ([]byte, error) {
		v, ok := state[path]
		if !ok {
			return nil, errors.New("no such sysctl")
		}
		return []byte(v + "\n"), nil
	}
	writeSysctl = func(path, value string) error { state[path] = value; return nil }
	return state, &mods
}

func TestApplyTProxyPrereqsLoadsModulesAndSetsState(t *testing.T) {
	state, loaded := withTProxyStubs(t)
	if err := applyTProxyPrereqs(); err != nil {
		t.Fatalf("applyTProxyPrereqs: %v", err)
	}
	// Every required module asked for, in order.
	if strings.Join(*loaded, ",") != strings.Join(egressprereq.TProxyModules, ",") {
		t.Errorf("loaded modules = %v, want %v", *loaded, egressprereq.TProxyModules)
	}
	// Sysctls now match the shared desired values exactly.
	for path, want := range egressprereq.TProxySysctls {
		if got := state[path]; got != want {
			t.Errorf("sysctl %s = %q after apply, want %q", path, got, want)
		}
	}
}

func TestApplyTProxyPrereqsModuleFailureAborts(t *testing.T) {
	_, _ = withTProxyStubs(t)
	loadModule = func(name string) error { return errors.New("module not found") }
	err := applyTProxyPrereqs()
	if err == nil || !strings.Contains(err.Error(), "load TPROXY module") {
		t.Fatalf("err = %v, want module load failure", err)
	}
}

func TestSetTProxySysctlsIdempotent(t *testing.T) {
	state, _ := withTProxyStubs(t)
	// Pre-set everything to the desired value; a second pass must not rewrite.
	writes := 0
	writeSysctl = func(path, value string) error { writes++; state[path] = value; return nil }
	for path, want := range egressprereq.TProxySysctls {
		state[path] = want
	}
	if err := setTProxySysctls(); err != nil {
		t.Fatalf("setTProxySysctls: %v", err)
	}
	if writes != 0 {
		t.Errorf("already-correct sysctls were rewritten %d times, want 0", writes)
	}
}

func TestTProxyNATPrereqsMissingReportsGaps(t *testing.T) {
	state, _ := withTProxyStubs(t)
	// Default fixture: all sysctls at kernel defaults (wrong), no rule.
	missing := tproxyNATPrereqsMissing()
	if len(missing) != 5 { // 4 sysctls + the absent ip rule
		t.Fatalf("missing = %v, want 5 entries", missing)
	}
	joined := strings.Join(missing, "; ")
	if !strings.Contains(joined, "fwmark 0x1 -> table 100") {
		t.Errorf("missing report %q should name the fwmark rule", joined)
	}

	// Now satisfy everything and confirm it reports clean.
	for path, want := range egressprereq.TProxySysctls {
		state[path] = want
	}
	netlinkRuleList = func(int) ([]netlink.Rule, error) {
		return []netlink.Rule{{Mark: egressprereq.TProxyMark, Table: egressprereq.TProxyTable}}, nil
	}
	if got := tproxyNATPrereqsMissing(); len(got) != 0 {
		t.Errorf("after provisioning, missing = %v, want none", got)
	}
}

func TestRevertTProxyPrereqsRestoresSysctls(t *testing.T) {
	state, _ := withTProxyStubs(t)
	// Simulate a provisioned host.
	for path, want := range egressprereq.TProxySysctls {
		state[path] = want
	}
	if err := revertTProxyPrereqs(); err != nil {
		t.Fatalf("revertTProxyPrereqs: %v", err)
	}
	for path, def := range tproxySysctlReverts {
		if got := state[path]; got != def {
			t.Errorf("sysctl %s = %q after revert, want default %q", path, got, def)
		}
	}
}

// TestProvisionedValuesMatchSupervisorVerify is the anti-drift guard: the values
// this CLI provisions (mark, table, sysctl keys) are the SAME shared constants
// the Firecracker supervisor verifies fail-closed. Both read pkg/egressprereq,
// so this test fails loudly if anyone forks a local copy of the values.
func TestProvisionedValuesMatchSupervisorVerify(t *testing.T) {
	if egressprereq.TProxyMark != 0x1 {
		t.Errorf("provisioned mark %#x != 0x1", egressprereq.TProxyMark)
	}
	if egressprereq.TProxyTable != 100 {
		t.Errorf("provisioned table %d != 100", egressprereq.TProxyTable)
	}
	// The ip rule we build must carry exactly those values.
	r := tproxyRule()
	if r.Mark != egressprereq.TProxyMark || r.Table != egressprereq.TProxyTable {
		t.Errorf("tproxyRule mark/table = %#x/%d, want %#x/%d", r.Mark, r.Table, egressprereq.TProxyMark, egressprereq.TProxyTable)
	}
	// The local route must target the shared table.
	route := tproxyLocalRoute(1)
	if route.Table != egressprereq.TProxyTable {
		t.Errorf("tproxyLocalRoute table = %d, want %d", route.Table, egressprereq.TProxyTable)
	}
}
