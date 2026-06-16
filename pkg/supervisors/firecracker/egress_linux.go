//go:build linux

package firecracker

import (
	"fmt"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const nftNATPreroutingChain = "MICROAGENT-NAT-PREROUTING"

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
