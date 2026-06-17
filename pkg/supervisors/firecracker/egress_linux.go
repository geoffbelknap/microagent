//go:build linux

package firecracker

import (
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const nftNATPreroutingChain = "MICROAGENT-NAT-PREROUTING"

// nftManglePreroutingChain is the type-filter / hook-prerouting / priority-mangle
// chain that the UDP TPROXY rule lives in. TPROXY requires a "mangle" chain (it
// cannot run from a nat chain), so it is kept separate from the REDIRECT chain.
const nftManglePreroutingChain = "MICROAGENT-MANGLE-PREROUTING"

// nftFilterPreroutingChain is the type-filter / hook-prerouting / priority-filter
// chain the fail-closed IPv6 drop rule lives in. The v6 drop is a verdict (DROP),
// not a NAT/mangle action, so it belongs in a plain filter chain rather than the
// nat REDIRECT chain or the mangle TPROXY chain. Prerouting is the earliest hook
// that sees guest-sourced packets arriving on the tap, so a v6 datagram is dropped
// before any forwarding/NAT decision — fail-closed for the not-yet-mediated v6
// path (see buildEgressV6DropRule).
const nftFilterPreroutingChain = "MICROAGENT-FILTER-PREROUTING"

// egressTProxyMark / egressTProxyTable are the fwmark stamped on TPROXY-steered
// datagrams and the policy routing table the local route lives in. They alias
// the authoritative values in pkg/egressprereq, which `host setup-networking`
// also provisions and `doctor` reports against — sharing the constants is what
// keeps the supervisor's verify and the provisioner from ever drifting apart.
// See egressprereq.TProxyMark for why a single fixed value is correct.
const (
	egressTProxyMark  = egressprereq.TProxyMark
	egressTProxyTable = egressprereq.TProxyTable
)

// buildEgressRedirectRule builds a nat/prerouting rule that REDIRECTs guest TCP
// (arriving on tap, sourced from the guest subnet) to the local mediator port.
// REDIRECT is DNAT-to-localhost, so the mediator (in the same netns) recovers
// the original destination via SO_ORIGINAL_DST.
func buildEgressRedirectRule(tap, subnet string, port uint16) (nftFirewallRule, error) {
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nftFirewallRule{}, fmt.Errorf("parse egress subnet %q: %w", subnet, err)
	}
	if network.IP.To4() == nil {
		return nftFirewallRule{}, fmt.Errorf("egress subnet %q is not IPv4", subnet)
	}
	exprs := append(ifNameMatchExprs(expr.MetaKeyIIFNAME, tap), ipv4SubnetMatchExprs(12, network)...)
	exprs = append(exprs,
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(port)},
		&expr.Redir{RegisterProtoMin: 1, RegisterProtoMax: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
	)
	return nftFirewallRule{
		transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftNATPreroutingChain, Comment: nftRuleComment(tap, "egress-redirect")},
		Exprs:                 exprs,
	}, nil
}

// ensureEgressNATChain creates the nat/prerouting chain the redirect rule lives in.
func ensureEgressNATChain(conn *nftables.Conn) error {
	table := microagentNFTTable()
	conn.AddTable(table)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables table "+nftMicroagentTable, err)
	}
	chain := &nftables.Chain{
		Name:     nftNATPreroutingChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityNATDest,
	}
	if _, err := conn.ListChain(table, chain.Name); err == nil {
		return nil
	}
	conn.AddChain(chain)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables chain "+chain.Name, err)
	}
	return nil
}

// installEgressRedirectRule installs the redirect rule and returns it as a
// transient rule for teardown (mirrors installNATFirewallRules).
func installEgressRedirectRule(tap, subnet string, port uint16) (transientFirewallRule, error) {
	rule, err := buildEgressRedirectRule(tap, subnet, port)
	if err != nil {
		return transientFirewallRule{}, err
	}
	conn := &nftables.Conn{}
	if err := ensureEgressNATChain(conn); err != nil {
		return transientFirewallRule{}, err
	}
	table := nftRuleTable(rule.transientFirewallRule)
	chain := &nftables.Chain{Name: rule.Chain, Table: table}
	exists, err := nftRuleExists(conn, table, chain, rule.Comment)
	if err != nil {
		return transientFirewallRule{}, networkPrivilegeError("inspect egress redirect rule", err)
	}
	if !exists {
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: rule.Exprs, UserData: nftRuleUserData(rule.Comment)})
		if err := conn.Flush(); err != nil {
			return transientFirewallRule{}, networkPrivilegeError("install egress redirect rule", err)
		}
	}
	return rule.transientFirewallRule, nil
}

// buildEgressV6DropRule builds a filter/prerouting rule that DROPs ALL guest
// IPv6 egress arriving on the tap. It is the fail-closed half of "ship v4-only
// mediation now": the steering rules (REDIRECT/TPROXY) match nfproto ipv4 only,
// and the tap plan hands the guest an IPv4-only address, so there is no live v6
// leak today. But if a guest ever acquired an IPv6 address while mediated, its
// v6 egress would slip past the v4-only capture — an unmediated channel that
// violates "mediation is complete". Dropping every guest v6 packet at the
// firewall closes that channel until real v6 mediation (a v6 REDIRECT/TPROXY
// path + a v6 tap plan) lands. See the "Future: IPv6 mediation" block in
// internal/egress/origdst_linux.go for the deferred enable path.
//
// The match is deliberately coarse — iifname == tap AND nfproto == ipv6 — so it
// catches TCP, UDP, ICMPv6, and anything else the guest emits over v6. It lives
// in a plain filter chain (a DROP verdict, not NAT/mangle) at the prerouting
// hook so the packet is dropped before any forward/NAT decision.
func buildEgressV6DropRule(tap string) (nftFirewallRule, error) {
	exprs := append(ifNameMatchExprs(expr.MetaKeyIIFNAME, tap),
		// nfproto == ipv6
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV6}},
		&expr.Verdict{Kind: expr.VerdictDrop},
	)
	return nftFirewallRule{
		transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftFilterPreroutingChain, Comment: nftRuleComment(tap, "egress-v6-drop")},
		Exprs:                 exprs,
	}, nil
}

// ensureEgressFilterChain creates the filter/prerouting chain the IPv6 drop rule
// lives in (type filter, hook prerouting, priority filter (0)). It mirrors
// ensureEgressNATChain/ensureEgressMangleChain but uses ChainTypeFilter so a plain
// DROP verdict is valid (NAT/mangle chains constrain the verdicts available).
func ensureEgressFilterChain(conn *nftables.Conn) error {
	table := microagentNFTTable()
	conn.AddTable(table)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables table "+nftMicroagentTable, err)
	}
	chain := &nftables.Chain{
		Name:     nftFilterPreroutingChain,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityFilter,
	}
	if _, err := conn.ListChain(table, chain.Name); err == nil {
		return nil
	}
	conn.AddChain(chain)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables chain "+chain.Name, err)
	}
	return nil
}

// installEgressV6DropRule ensures the filter chain and installs the IPv6 drop
// rule, returning it as a transient rule for teardown (mirrors
// installEgressRedirectRule / installEgressTProxyRule).
func installEgressV6DropRule(tap string) (transientFirewallRule, error) {
	rule, err := buildEgressV6DropRule(tap)
	if err != nil {
		return transientFirewallRule{}, err
	}
	conn := &nftables.Conn{}
	if err := ensureEgressFilterChain(conn); err != nil {
		return transientFirewallRule{}, err
	}
	table := nftRuleTable(rule.transientFirewallRule)
	chain := &nftables.Chain{Name: rule.Chain, Table: table}
	exists, err := nftRuleExists(conn, table, chain, rule.Comment)
	if err != nil {
		return transientFirewallRule{}, networkPrivilegeError("inspect egress v6 drop rule", err)
	}
	if !exists {
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: rule.Exprs, UserData: nftRuleUserData(rule.Comment)})
		if err := conn.Flush(); err != nil {
			return transientFirewallRule{}, networkPrivilegeError("install egress v6 drop rule", err)
		}
	}
	return rule.transientFirewallRule, nil
}

// buildEgressTProxyRule builds a mangle/prerouting rule that TPROXYs guest UDP
// (arriving on tap, sourced from the guest subnet) to the local mediator's
// transparent socket at mediator (gateway:port). Unlike TCP REDIRECT (DNAT),
// TPROXY does NOT rewrite the packet's destination: it stamps the fwmark so the
// policy route (ip rule fwmark -> table) delivers the datagram locally while the
// mediator recovers the untouched original destination via IP_ORIGDSTADDR.
//
// The reply leg does NOT traverse this chain: the mediator forwards upstream over
// a separate connected UDP socket (kernel-routed replies return on that socket),
// and it answers the guest from a fresh IP_TRANSPARENT socket bound to origDst —
// a locally generated packet, not one re-entering prerouting on the tap. So no
// DIVERT/socket-match shortcut is needed (it would only matter for a single-
// socket connected-UDP TPROXY design where established-flow replies re-enter
// prerouting).
func buildEgressTProxyRule(tap, subnet string, mark uint32, mediator netip.AddrPort) (nftFirewallRule, error) {
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nftFirewallRule{}, fmt.Errorf("parse egress subnet %q: %w", subnet, err)
	}
	if network.IP.To4() == nil {
		return nftFirewallRule{}, fmt.Errorf("egress subnet %q is not IPv4", subnet)
	}
	if !mediator.Addr().Is4() {
		return nftFirewallRule{}, fmt.Errorf("egress mediator addr %q is not IPv4", mediator.Addr())
	}
	addr4 := mediator.Addr().As4()
	exprs := append(ifNameMatchExprs(expr.MetaKeyIIFNAME, tap), ipv4SubnetMatchExprs(12, network)...)
	exprs = append(exprs,
		// l4proto == udp
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},
		// meta mark set <mark>
		&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(mark)},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
		// tproxy ip to <gateway>:<port> (registers carry addr in reg 1, port in reg 2)
		&expr.Immediate{Register: 1, Data: addr4[:]},
		&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(mediator.Port())},
		&expr.TProxy{Family: unix.NFPROTO_IPV4, TableFamily: unix.NFPROTO_IPV4, RegAddr: 1, RegPort: 2},
	)
	return nftFirewallRule{
		transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftManglePreroutingChain, Comment: nftRuleComment(tap, "egress-tproxy")},
		Exprs:                 exprs,
	}, nil
}

// ensureEgressMangleChain creates the mangle/prerouting chain the TPROXY rule
// lives in (type filter, hook prerouting, priority mangle (-150)). TPROXY is only
// valid from a mangle chain, so it cannot share the nat-typed REDIRECT chain.
func ensureEgressMangleChain(conn *nftables.Conn) error {
	table := microagentNFTTable()
	conn.AddTable(table)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables table "+nftMicroagentTable, err)
	}
	chain := &nftables.Chain{
		Name:     nftManglePreroutingChain,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityMangle,
	}
	if _, err := conn.ListChain(table, chain.Name); err == nil {
		return nil
	}
	conn.AddChain(chain)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables chain "+chain.Name, err)
	}
	return nil
}

// installEgressTProxyRule ensures the mangle chain and installs the TPROXY rule,
// returning it as a transient rule for teardown (mirrors installEgressRedirectRule).
func installEgressTProxyRule(tap, subnet string, mark uint32, mediator netip.AddrPort) (transientFirewallRule, error) {
	rule, err := buildEgressTProxyRule(tap, subnet, mark, mediator)
	if err != nil {
		return transientFirewallRule{}, err
	}
	conn := &nftables.Conn{}
	if err := ensureEgressMangleChain(conn); err != nil {
		return transientFirewallRule{}, err
	}
	table := nftRuleTable(rule.transientFirewallRule)
	chain := &nftables.Chain{Name: rule.Chain, Table: table}
	exists, err := nftRuleExists(conn, table, chain, rule.Comment)
	if err != nil {
		return transientFirewallRule{}, networkPrivilegeError("inspect egress tproxy rule", err)
	}
	if !exists {
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: rule.Exprs, UserData: nftRuleUserData(rule.Comment)})
		if err := conn.Flush(); err != nil {
			return transientFirewallRule{}, networkPrivilegeError("install egress tproxy rule", err)
		}
	}
	return rule.transientFirewallRule, nil
}

// egressTProxySysctls are the per-namespace knobs TPROXY delivery to a local
// transparent socket on lo requires (proven by the rootless spike). They alias
// the authoritative map in pkg/egressprereq so the supervisor verifies exactly
// the sysctl keys/values `host setup-networking` provisions:
//   - route_localnet: allow routing of 0.0.0.0/8 (the local TPROXY route on lo)
//   - rp_filter=0: the spoofed-source reply leg would otherwise be dropped by
//     reverse-path filtering
//   - accept_local: accept packets whose source is a local address
//   - ip_forward: guest egress is forwarded through the host
var egressTProxySysctls = egressprereq.TProxySysctls

// setEgressTProxySysctls writes the TPROXY sysctls. In the user (pasta) netns
// these are namespace-local and reaped with the netns; in host (nat) mode they
// are host-global infrastructure and must be provisioned by `host
// setup-networking`, not toggled per-workspace (see prepareEgressTProxyNetns).
func setEgressTProxySysctls() error {
	for path, want := range egressTProxySysctls {
		if err := writeSysctlIfNeeded(path, want); err != nil {
			return err
		}
	}
	return nil
}

// writeSysctlIfNeeded sets a /proc/sys knob only when it does not already hold
// the desired value, so an already-correct host-global setting is not rewritten.
func writeSysctlIfNeeded(path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("inspect %s for egress TPROXY: %w", path, err)
	}
	if trimSysctl(data) == want {
		return nil
	}
	if err := os.WriteFile(path, []byte(want+"\n"), 0o644); err != nil {
		return networkPrivilegeError("set "+path+" for egress TPROXY", err)
	}
	return nil
}

func trimSysctl(data []byte) string {
	s := string(data)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// egressTProxyRule builds the policy rule (fwmark mark -> table) that the local
// route lives behind. Identical for every workspace, so it is idempotent.
func egressTProxyRule(mark uint32, table int) *netlink.Rule {
	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V4
	rule.Mark = mark
	rule.Table = table
	return rule
}

// egressTProxyLocalRoute builds the `local 0.0.0.0/0 dev lo table <table>` route
// that delivers marked TPROXY packets to the local transparent socket.
func egressTProxyLocalRoute(table, loIndex int) *netlink.Route {
	_, defaultNet, _ := net.ParseCIDR("0.0.0.0/0")
	return &netlink.Route{
		LinkIndex: loIndex,
		Dst:       defaultNet,
		Table:     table,
		Type:      unix.RTN_LOCAL,
		Scope:     unix.RT_SCOPE_HOST,
	}
}

// addEgressTProxyRouting installs the ip rule (fwmark -> table) and the local
// route in that table. Idempotent: an already-present rule/route (EEXIST) is not
// an error, so concurrent same-netns/same-host installs converge. On partial
// failure (rule added but route fails) it unwinds the rule it just added so it
// leaves no orphaned state for the caller to forget.
func addEgressTProxyRouting(mark uint32, table int) error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return networkPrivilegeError("inspect lo for egress TPROXY routing", err)
	}
	if err := netlink.RuleAdd(egressTProxyRule(mark, table)); err != nil && !alreadyExistsError(err) {
		return networkPrivilegeError(fmt.Sprintf("add egress TPROXY ip rule fwmark %#x -> table %d", mark, table), err)
	}
	if err := netlink.RouteAdd(egressTProxyLocalRoute(table, lo.Attrs().Index)); err != nil && !alreadyExistsError(err) {
		// Unwind the ip rule we just added so a route failure does not orphan it.
		_ = netlink.RuleDel(egressTProxyRule(mark, table))
		return networkPrivilegeError(fmt.Sprintf("add egress TPROXY local route in table %d", table), err)
	}
	return nil
}

// delEgressTProxyRouting removes the local route and ip rule added by
// addEgressTProxyRouting. Best-effort and idempotent: it is only called for the
// netns-local (user mode) provisioning, whose failure paths must fully unwind
// even though the ephemeral netns would also reap them on teardown. Errors are
// swallowed because the netns disappearing is the authoritative cleanup.
func delEgressTProxyRouting(mark uint32, table int) {
	if lo, err := netlink.LinkByName("lo"); err == nil {
		_ = netlink.RouteDel(egressTProxyLocalRoute(table, lo.Attrs().Index))
	}
	_ = netlink.RuleDel(egressTProxyRule(mark, table))
}

// prepareEgressTProxyNetns provisions the per-namespace TPROXY prerequisites
// (sysctls, ip rule, local route) and returns a teardown func that unwinds them.
//
// netnsLocal distinguishes the two callers:
//   - user (pasta) mode: netnsLocal == true. The sysctls/rule/route are
//     namespace-local and reaped when the ephemeral netns dies; we still install
//     them here (the netns starts clean) and return a teardown that unwinds them
//     so an early start failure leaves nothing dangling.
//   - nat (host) mode: netnsLocal == false. The rule/route/sysctls are
//     host-global. We do NOT toggle them per-workspace (that would race sibling
//     workspaces and leave host state to refcount); instead we VERIFY the
//     prerequisites are present and fail-closed pointing at `host
//     setup-networking` if not. The returned teardown is a no-op (host infra is
//     not owned by a single workspace).
func prepareEgressTProxyNetns(netnsLocal bool, mark uint32, table int) (func(), error) {
	if !netnsLocal {
		if err := verifyEgressTProxyHostPrereqs(mark, table); err != nil {
			return func() {}, err
		}
		return func() {}, nil
	}
	if err := setEgressTProxySysctls(); err != nil {
		return func() {}, err
	}
	if err := addEgressTProxyRouting(mark, table); err != nil {
		return func() {}, err
	}
	return func() { delEgressTProxyRouting(mark, table) }, nil
}

// verifyEgressTProxyHostPrereqs checks (without mutating) that the host-global
// TPROXY prerequisites are in place for nat mode. These are owned by `host
// setup-networking` (Task 3.3c). A missing prerequisite is fail-closed: the
// caller wraps the error with the operator-facing remediation hint.
func verifyEgressTProxyHostPrereqs(mark uint32, table int) error {
	return verifyEgressTProxyPrereqs(mark, table, os.ReadFile, func() ([]netlink.Rule, error) {
		return netlink.RuleList(netlink.FAMILY_V4)
	})
}

// verifyEgressTProxyPrereqs is the injectable core of verifyEgressTProxyHostPrereqs.
// readFile and listRules are seams so the fail-closed decision (any missing
// sysctl or the absent ip rule -> error) is unit-testable without touching real
// host state. The first missing prerequisite wins, surfacing a specific error.
func verifyEgressTProxyPrereqs(mark uint32, table int, readFile func(string) ([]byte, error), listRules func() ([]netlink.Rule, error)) error {
	for path, want := range egressTProxySysctls {
		data, err := readFile(path)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		if got := trimSysctl(data); got != want {
			return fmt.Errorf("sysctl %s is %q, want %q", path, got, want)
		}
	}
	rules, err := listRules()
	if err != nil {
		return fmt.Errorf("list ip rules: %w", err)
	}
	for _, r := range rules {
		if r.Mark == mark && r.Table == table {
			return nil
		}
	}
	return fmt.Errorf("ip rule fwmark %#x -> table %d is absent", mark, table)
}
