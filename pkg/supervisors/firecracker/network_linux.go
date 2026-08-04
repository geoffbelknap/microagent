//go:build linux

package firecracker

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/google/nftables/userdata"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func prepareNetworkForStart(opts Options, config *vmkit.Config, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	switch networkMode(config) {
	case "isolated":
		return nil, nil, nil, 0, nil
	case "user":
		return prepareUserNetworkForStart(opts, config, restore, expectedCASHA)
	default:
		return nil, nil, nil, 0, fmt.Errorf("firecracker network.mode %q is unsupported; use user or isolated", networkMode(config))
	}
}

func prepareUserNetworkForStart(opts Options, config *vmkit.Config, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	if !insideUserNetworkNamespace() {
		return nil, nil, nil, 0, fmt.Errorf("firecracker user networking must run inside a pasta user network namespace")
	}
	if err := enableNamespaceIPv4Forwarding(); err != nil {
		return nil, nil, nil, 0, err
	}
	devices, rules, network, egressPID, err := prepareTAPNATForStart(opts, config, "user", restore, expectedCASHA)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return attachUserNetworkPID(devices), rules, network, egressPID, nil
}

func prepareTAPNATForStart(opts Options, config *vmkit.Config, mode string, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	plan, err := tapNATAddressPlan(opts, config)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	tap := tapName(opts)
	device := transientNetworkDevice{Name: tap, Mode: "tap", Interface: plan.Subnet, Created: true}
	if err := createTap(tap); err != nil {
		return nil, nil, nil, 0, networkPrivilegeError("create firecracker nat tap "+tap, err)
	}
	cleanupDevices := []transientNetworkDevice{device}
	link, err := netlink.LinkByName(tap)
	if err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, networkPrivilegeError("inspect firecracker nat tap "+tap, err)
	}
	addr, err := netlink.ParseAddr(plan.HostCIDR)
	if err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, fmt.Errorf("parse firecracker nat tap address %s: %w", plan.HostCIDR, err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil && !alreadyExistsError(err) {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, networkPrivilegeError("assign firecracker nat tap address "+plan.HostCIDR, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, networkPrivilegeError("bring firecracker nat tap up", err)
	}
	rules, err := installNATFirewallRules(tap, plan.Subnet)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, err
	}
	network := runtimeNetworkConfig(config, plan.Subnet, plan.GuestCIDR, plan.Gateway)
	network.Mode = mode
	egressPID, egressRules, err := provisionEgressMediation(opts, config, tap, plan.Gateway, plan.Subnet, restore, expectedCASHA)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, err
	}
	rules = append(rules, egressRules...)
	return cleanupDevices, rules, &network, egressPID, nil
}

// provisionEgressMediation provisions the egress mediator and its steering rules
// (per-workspace CA, mediator process, TCP REDIRECT, UDP TPROXY) for a guest
// reachable on the given tap, gateway, and subnet. It returns the mediator PID
// and the nft rules the caller must append to its transient-firewall slice (so
// the standard stop/quarantine/failed-start teardown removes them).
//
// When egress mediation is off (EgressMediationOn(config.EgressMode) is false)
// it is a no-op: it returns (0, nil, nil) and the guest's egress is unmediated.
//
// Fail-closed: on ANY failure it tears down everything IT started (CA files,
// mediator process, TPROXY ip rule/route) and returns the error. It does NOT
// touch the caller's tap or base NAT rules — the caller unwinds those with its
// own cleanupTransient* discipline, exactly as the inline path did.
//
// This runs only for user (pasta) mode: the TPROXY sysctls/ip-rule/local-route
// are netns-local and reaped with the ephemeral netns, so we provision them here.
func provisionEgressMediation(opts Options, config *vmkit.Config, tap, gateway, subnet string, restore bool, expectedCASHA string) (int, []transientFirewallRule, error) {
	if config == nil || !vmkit.EgressMediationOn(config.EgressMode) {
		return 0, nil, nil
	}
	var rules []transientFirewallRule
	// Acquire the per-workspace CA — but only for modes that forge certificates
	// (mitm). broker mode splices allowed flows opaquely and forges
	// nothing, so it mints no CA and delivers none to the guest (the workspace
	// layer also allocates no CA-cert listener for it). An empty caCertPath makes
	// startEgressMediator omit --ca-cert, so the mediator's shouldMITM stays off.
	//
	// On a fresh start we mint a CA and persist it; on a snapshot restore/fork we
	// REUSE the persisted CA the guest's baked trust store was built against
	// (re-minting would silently break every MITM handshake of the restored
	// guest). cleanupCA removes the CA files only when we minted them this call —
	// on reuse it is a no-op so a downstream failure never deletes the workspace's
	// persistent CA.
	caCertPath, caKeyPath := "", ""
	cleanupCA := func() {}
	if vmkit.EgressModeForgesCerts(config.EgressMode) {
		var caErr error
		caCertPath, caKeyPath, cleanupCA, caErr = acquireEgressCA(opts, restore, expectedCASHA)
		if caErr != nil {
			return 0, nil, caErr
		}
	}
	// Resolver allowlist: the workspace's configured nameservers are the only
	// addresses the mediator will forward guest DNS to (confused-deputy guard).
	// Nil-safe: an absent Network leaves it empty, keeping the mediator's
	// internal-address floor.
	var dnsResolvers []string
	if config.Network != nil {
		dnsResolvers = config.Network.DNS
	}
	pid, port, eerr := startEgressMediator(opts, gateway, config.EgressMode, config.EgressAllowlistLocked, config.EgressAllow, config.EgressPassthrough, dnsResolvers, config.EgressSwapConfigPath, nil, caCertPath, caKeyPath, tap, egressCapsFromConfig(config))
	if eerr != nil {
		cleanupCA()
		return 0, nil, eerr
	}
	redirect, rerr := installEgressRedirectRule(tap, subnet, uint16(port))
	if rerr != nil {
		terminateAuxProcess(pid)
		cleanupCA()
		return 0, nil, rerr
	}
	rules = append(rules, redirect)

	// UDP mediation via TPROXY. The mediator already binds a transparent UDP
	// socket on gateway:port (same addr:port as its TCP listener); the supervisor
	// steers guest UDP there. This is fail-closed: any failure here (TPROXY
	// modules absent, missing host prerequisites, etc.) tears down EVERYTHING this
	// helper already provisioned for the workspace and returns the guiding error
	// so the start aborts rather than booting a guest whose UDP escapes the
	// mediator.
	//
	// user (pasta) mode: the sysctls/ip-rule/local-route are netns-local and
	// reaped with the ephemeral netns; we provision them here.
	undoRouting, perr := prepareEgressTProxyNetns(true, egressTProxyMark, egressTProxyTable)
	if perr != nil {
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: UDP mediation (TPROXY) unavailable for workspace %s — ensure the host kernel provides TPROXY support (e.g. the nft_tproxy/xt_TPROXY module) or use --egress off: %w", opts.Name, perr)
	}
	mediatorAddr := netip.AddrPortFrom(netip.MustParseAddr(gateway), uint16(port))
	tproxy, terr := installEgressTProxyRule(tap, subnet, egressTProxyMark, mediatorAddr)
	if terr != nil {
		undoRouting()
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: UDP mediation (TPROXY) unavailable for workspace %s — ensure the host kernel provides TPROXY support (e.g. the nft_tproxy/xt_TPROXY module) or use --egress off: %w", opts.Name, terr)
	}
	// The nft tproxy rule joins the returned rules slice so the standard firewall
	// teardown (stop/quarantine/failed-start) removes it. The ip rule/local route
	// are NOT firewall rules: they vanish with the ephemeral pasta netns (no
	// per-stop teardown needed). undoRouting is therefore only meaningful on the
	// failure paths above.
	rules = append(rules, tproxy)

	// Fail-closed IPv6 drop. The REDIRECT/TPROXY steering above is IPv4-only
	// (nfproto ipv4) and the tap plan hands the guest an IPv4-only address, so v6
	// is not a live leak today. But a guest that ever acquired a v6 address while
	// mediated would have its v6 egress slip past the v4-only capture — an
	// unmediated channel. We drop ALL guest v6 egress at the firewall so the
	// "mediation is complete" invariant holds for the not-yet-mediated v6 path.
	// Same fail-closed discipline as the steering rules: on failure tear down
	// everything this helper provisioned and abort the start.
	v6drop, v6err := installEgressV6DropRule(tap)
	if v6err != nil {
		undoRouting()
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: IPv6 fail-closed drop unavailable for workspace %s: %w", opts.Name, v6err)
	}
	rules = append(rules, v6drop)

	// Tier 5: drop-and-audit guest IPv4 L4 traffic that is neither TCP
	// (REDIRECT-mediated above) nor UDP (TPROXY-mediated above) — ICMP and any
	// other protocol with no allowlistable destination identity. With TCP and UDP
	// already mediated, dropping the rest at the firewall completes IPv4 mediation
	// ("mediation is complete"): allowing ICMP echo etc. would be an unmediated
	// covert/exfil + liveness-leak channel. The three precedence rules (accept
	// tcp, accept udp, catch-all nflog+drop) share the filter chain with the v6
	// drop and audit drops via nflog, not the mediator JSONL. Same fail-closed
	// discipline: on failure tear down everything this helper provisioned and
	// abort the start.
	l4drops, l4err := installEgressL4DropRule(tap, subnet)
	if l4err != nil {
		undoRouting()
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: non-TCP/UDP fail-closed drop unavailable for workspace %s: %w", opts.Name, l4err)
	}
	rules = append(rules, l4drops...)
	return pid, rules, nil
}

// acquireEgressCA returns the on-disk paths to the per-workspace egress CA cert
// and key for the mediator, plus a cleanup closure to invoke on a downstream
// failure. It has two clearly separated branches:
//
//   - Fresh start (restore=false): mint a new ECDSA CA, persist egress-ca.pem
//     (0644, public — delivered to the guest) and egress-ca-key.pem (0600, host
//     only), and return a cleanup that removes BOTH files (we created them).
//     This path is byte-identical to the pre-restore implementation.
//
//   - Restore/fork (restore=true): REUSE the persisted CA the guest's baked trust
//     store was built against. Read egress-ca.pem + egress-ca-key.pem, compute the
//     cert DER SHA-256, and fail closed if either file is missing or the
//     fingerprint differs from the snapshot manifest's expectedCASHA — a mismatch
//     means the on-disk CA is not the one the guest trusts, so minting/serving any
//     other CA would silently break MITM. No egress.NewCA call, no write. The
//     returned cleanup is a NO-OP so a downstream failure never deletes the
//     workspace's persistent CA.
func acquireEgressCA(opts Options, restore bool, expectedCASHA string) (caCertPath, caKeyPath string, cleanup func(), err error) {
	wsDir := filepath.Join(opts.StateDir, opts.Name)
	caCertPath = filepath.Join(wsDir, "egress-ca.pem")
	caKeyPath = filepath.Join(wsDir, "egress-ca-key.pem")
	noop := func() {}

	if restore {
		// Reuse the persisted CA. Fail closed on any divergence from the manifest.
		if expectedCASHA == "" {
			return "", "", noop, fmt.Errorf("egress: restore of mediated workspace %s has no recorded CA fingerprint; refusing to re-arm the mediator", opts.Name)
		}
		if _, statErr := os.Stat(caKeyPath); statErr != nil {
			return "", "", noop, fmt.Errorf("egress: restore of workspace %s cannot reuse CA key: %w", opts.Name, statErr)
		}
		gotSHA, shaErr := egressCACertSHA256(wsDir)
		if shaErr != nil {
			return "", "", noop, fmt.Errorf("egress: restore of workspace %s cannot reuse CA cert: %w", opts.Name, shaErr)
		}
		if gotSHA != expectedCASHA {
			return "", "", noop, fmt.Errorf("egress: restore of workspace %s refused — persisted CA fingerprint %s does not match snapshot fingerprint %s; the guest's baked trust store would reject the mediator", opts.Name, gotSHA, expectedCASHA)
		}
		return caCertPath, caKeyPath, noop, nil
	}

	// Fresh start: mint a per-workspace CA. The cert (public) is delivered to the
	// guest over the cacert vsock listener so guestinit installs it in the trust
	// store. The key stays on the host and is passed to the mediator for TLS MITM.
	ca, caErr := egress.NewCA(opts.Name, 720*time.Hour)
	if caErr != nil {
		return "", "", noop, fmt.Errorf("mint egress CA for %s: %w", opts.Name, caErr)
	}
	caKeyPEM, caErr := ca.KeyPEM()
	if caErr != nil {
		return "", "", noop, fmt.Errorf("encode egress CA key for %s: %w", opts.Name, caErr)
	}
	if caErr = os.MkdirAll(wsDir, 0o700); caErr != nil {
		return "", "", noop, fmt.Errorf("create workspace dir for egress CA: %w", caErr)
	}
	if caErr = fsutil.WriteFile(caCertPath, ca.CertPEM(), 0o644); caErr != nil {
		return "", "", noop, fmt.Errorf("write egress CA cert: %w", caErr)
	}
	if caErr = os.WriteFile(caKeyPath, caKeyPEM, 0o600); caErr != nil {
		_ = os.Remove(caCertPath)
		return "", "", noop, fmt.Errorf("write egress CA key: %w", caErr)
	}
	cleanup = func() {
		_ = os.Remove(caCertPath)
		_ = os.Remove(caKeyPath)
	}
	return caCertPath, caKeyPath, cleanup, nil
}

type tapNATAddress struct {
	Subnet    string
	GuestCIDR string
	Gateway   string
	HostCIDR  string
}

func tapNATAddressPlan(opts Options, config *vmkit.Config) (tapNATAddress, error) {
	if config != nil && config.Network != nil && (config.Network.IP != "" || config.Network.Gateway != "" || config.Network.Subnet != "") {
		return staticTAPNATAddressPlan(*config.Network)
	}
	subnetOctet, err := allocateNATSubnetOctet(opts)
	if err != nil {
		return tapNATAddress{}, err
	}
	subnet := fmt.Sprintf("10.43.%d.0/29", subnetOctet)
	hostIP := fmt.Sprintf("10.43.%d.1", subnetOctet)
	guestIP := fmt.Sprintf("10.43.%d.2", subnetOctet)
	return tapNATAddress{
		Subnet:    subnet,
		GuestCIDR: guestIP + "/29",
		Gateway:   hostIP,
		HostCIDR:  hostIP + "/29",
	}, nil
}

func staticTAPNATAddressPlan(network vmkit.NetworkConfig) (tapNATAddress, error) {
	if strings.TrimSpace(network.IP) == "" || strings.TrimSpace(network.Gateway) == "" {
		return tapNATAddress{}, fmt.Errorf("firecracker static nat/user networking requires network.ip and network.gateway")
	}
	guestIP, guestNet, err := net.ParseCIDR(strings.TrimSpace(network.IP))
	if err != nil {
		return tapNATAddress{}, fmt.Errorf("parse firecracker static network.ip %q: %w", network.IP, err)
	}
	if guestIP.To4() == nil {
		return tapNATAddress{}, fmt.Errorf("firecracker static network.ip %q must be IPv4 CIDR", network.IP)
	}
	gateway := net.ParseIP(strings.TrimSpace(network.Gateway)).To4()
	if gateway == nil {
		return tapNATAddress{}, fmt.Errorf("firecracker static network.gateway %q must be IPv4", network.Gateway)
	}
	subnet := strings.TrimSpace(network.Subnet)
	if subnet == "" {
		subnet = guestNet.String()
	} else {
		_, declaredSubnet, err := net.ParseCIDR(subnet)
		if err != nil {
			return tapNATAddress{}, fmt.Errorf("parse firecracker static network.subnet %q: %w", network.Subnet, err)
		}
		if declaredSubnet.IP.To4() == nil {
			return tapNATAddress{}, fmt.Errorf("firecracker static network.subnet %q must be IPv4 CIDR", network.Subnet)
		}
		if !declaredSubnet.Contains(guestIP) || !declaredSubnet.Contains(gateway) {
			return tapNATAddress{}, fmt.Errorf("firecracker static network.subnet %q must contain network.ip and network.gateway", network.Subnet)
		}
	}
	_, hostNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return tapNATAddress{}, fmt.Errorf("parse firecracker static network.subnet %q: %w", subnet, err)
	}
	prefix, _ := hostNet.Mask.Size()
	return tapNATAddress{
		Subnet:    subnet,
		GuestCIDR: guestIP.String() + "/" + strconv.Itoa(prefix),
		Gateway:   gateway.String(),
		HostCIDR:  gateway.String() + "/" + strconv.Itoa(prefix),
	}, nil
}

func runtimeNetworkConfig(config *vmkit.Config, subnet, ip, gateway string) vmkit.NetworkConfig {
	network := vmkit.NetworkConfig{Mode: "nat"}
	if config != nil && config.Network != nil {
		network = *config.Network
	}
	network.Mode = "nat"
	network.IP = ip
	network.Subnet = subnet
	network.Gateway = gateway
	if len(network.DNS) == 0 {
		network.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}
	network.Routes = []string{"0.0.0.0/0 via " + gateway}
	return network
}

func createTap(name string) error {
	if _, err := netlink.LinkByName(name); err == nil {
		return fmt.Errorf("network link %q already exists", name)
	} else if !linkNotFoundError(err) {
		return err
	}
	return netlink.LinkAdd(&netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Flags:     netlink.TUNTAP_DEFAULTS | netlink.TUNTAP_NO_PI,
	})
}

func enableNamespaceIPv4Forwarding() error {
	path := "/proc/sys/net/ipv4/ip_forward"
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("inspect net.ipv4.ip_forward for firecracker user networking: %w", err)
	}
	if strings.TrimSpace(string(data)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
		return networkPrivilegeError("enable net.ipv4.ip_forward in firecracker user network namespace", err)
	}
	return nil
}

func allocateNATSubnetOctet(opts Options) (int, error) {
	used := map[int]bool{}
	links, err := netlink.LinkList()
	if err != nil {
		return 0, fmt.Errorf("list host network interfaces for nat subnet allocation: %w", err)
	}
	for _, link := range links {
		name := link.Attrs().Name
		if !strings.HasPrefix(name, "magtap") {
			continue
		}
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			v4 := addr.IP.To4()
			if len(v4) == net.IPv4len && v4[0] == 10 && v4[1] == 43 {
				used[int(v4[2])] = true
			}
		}
	}
	sum := sha1.Sum([]byte(opts.Name + opts.StateDir))
	start := int(sum[0])%254 + 1
	for offset := 0; offset < 254; offset++ {
		octet := ((start - 1 + offset) % 254) + 1
		if !used[octet] {
			return octet, nil
		}
	}
	return 0, fmt.Errorf("no free firecracker nat subnet in 10.43.1.0/29 through 10.43.254.0/29")
}

const (
	nftMicroagentTable      = "microagent"
	nftNATPostroutingChain  = "MICROAGENT-NAT-POSTROUTING"
	nftForwardChain         = "MICROAGENT-FORWARD"
	nftUFWFilterTable       = "filter"
	nftUFWUserForwardChain  = "ufw-user-forward"
	nftRuleCommentPrefix    = "microagent:"
	nftRuleCommentSeparator = ":"
	nftForwardPriority      = -1
)

type nftFirewallRule struct {
	transientFirewallRule
	Exprs []expr.Any
}

func installNATFirewallRules(tap, subnet string) ([]transientFirewallRule, error) {
	nftRules, err := buildNATFirewallRules(tap, subnet)
	if err != nil {
		return nil, err
	}
	conn := &nftables.Conn{}
	if err := ensureNATFirewallChains(conn); err != nil {
		return nil, err
	}
	transientRules := make([]transientFirewallRule, 0, len(nftRules))
	for i, rule := range nftRules {
		table := nftRuleTable(rule.transientFirewallRule)
		chain := &nftables.Chain{Name: rule.Chain, Table: table}
		exists, err := nftRuleExists(conn, table, chain, rule.Comment)
		if err != nil {
			cleanupTransientFirewallRules(transientRules)
			return transientRules, networkPrivilegeError("inspect firecracker nat firewall", err)
		}
		if !exists {
			conn.AddRule(&nftables.Rule{
				Table:    table,
				Chain:    chain,
				Exprs:    rule.Exprs,
				UserData: nftRuleUserData(rule.Comment),
			})
			if err := conn.Flush(); err != nil {
				cleanupTransientFirewallRules(transientRules)
				return transientRules, networkPrivilegeError("configure firecracker nat firewall", err)
			}
		}
		transientRules = append(transientRules, nftRules[i].transientFirewallRule)
	}
	ufwRules, err := buildUFWForwardRules(conn, tap, subnet)
	if err != nil {
		cleanupTransientFirewallRules(transientRules)
		return transientRules, err
	}
	for _, rule := range ufwRules {
		table := nftRuleTable(rule.transientFirewallRule)
		chain := &nftables.Chain{Name: rule.Chain, Table: table}
		exists, err := nftRuleExists(conn, table, chain, rule.Comment)
		if err != nil {
			cleanupTransientFirewallRules(transientRules)
			return transientRules, networkPrivilegeError("inspect firecracker ufw forward firewall", err)
		}
		if !exists {
			conn.AddRule(&nftables.Rule{
				Table:    table,
				Chain:    chain,
				Exprs:    rule.Exprs,
				UserData: nftRuleUserData(rule.Comment),
			})
			if err := conn.Flush(); err != nil {
				cleanupTransientFirewallRules(transientRules)
				return transientRules, networkPrivilegeError("configure firecracker ufw forward firewall", err)
			}
		}
		transientRules = append(transientRules, rule.transientFirewallRule)
	}
	return transientRules, nil
}

func buildNATFirewallRules(tap, subnet string) ([]nftFirewallRule, error) {
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("parse firecracker nat subnet %q: %w", subnet, err)
	}
	if network.IP.To4() == nil {
		return nil, fmt.Errorf("firecracker nat subnet %q is not IPv4", subnet)
	}
	return []nftFirewallRule{
		{
			transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftNATPostroutingChain, Comment: nftRuleComment(tap, "masquerade")},
			Exprs: append(append(ipv4SubnetMatchExprs(12, network),
				&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: nftIfName(tap)},
			), &expr.Masq{}),
		},
		{
			transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftForwardChain, Comment: nftRuleComment(tap, "forward-out")},
			Exprs:                 append(append(ifNameMatchExprs(expr.MetaKeyIIFNAME, tap), ipv4SubnetMatchExprs(12, network)...), &expr.Verdict{Kind: expr.VerdictAccept}),
		},
		{
			transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftForwardChain, Comment: nftRuleComment(tap, "forward-established")},
			Exprs:                 append(append(append(ifNameMatchExprs(expr.MetaKeyOIFNAME, tap), ipv4SubnetMatchExprs(16, network)...), establishedRelatedExprs()...), &expr.Verdict{Kind: expr.VerdictAccept}),
		},
	}, nil
}

func buildUFWForwardRules(conn *nftables.Conn, tap, subnet string) ([]nftFirewallRule, error) {
	table := &nftables.Table{Name: nftUFWFilterTable, Family: nftables.TableFamilyIPv4}
	if _, err := conn.ListChain(table, nftUFWUserForwardChain); err != nil {
		return nil, nil
	}
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("parse firecracker nat subnet %q: %w", subnet, err)
	}
	if network.IP.To4() == nil {
		return nil, fmt.Errorf("firecracker nat subnet %q is not IPv4", subnet)
	}
	return []nftFirewallRule{
		{
			transientFirewallRule: transientFirewallRule{Family: "ip", Table: nftUFWFilterTable, Chain: nftUFWUserForwardChain, Comment: nftRuleComment(tap, "ufw-forward-out")},
			Exprs:                 append(append(ifNameMatchExprs(expr.MetaKeyIIFNAME, tap), ipv4SubnetMatchExprs(12, network)...), &expr.Verdict{Kind: expr.VerdictAccept}),
		},
	}, nil
}

func ensureNATFirewallChains(conn *nftables.Conn) error {
	table := microagentNFTTable()
	conn.AddTable(table)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables table "+nftMicroagentTable, err)
	}
	chains := []*nftables.Chain{
		{
			Name:     nftNATPostroutingChain,
			Table:    table,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookPostrouting,
			Priority: nftables.ChainPriorityNATSource,
		},
		{
			Name:     nftForwardChain,
			Table:    table,
			Type:     nftables.ChainTypeFilter,
			Hooknum:  nftables.ChainHookForward,
			Priority: nftables.ChainPriorityRef(nftForwardPriority),
		},
	}
	for _, chain := range chains {
		if _, err := conn.ListChain(table, chain.Name); err == nil {
			continue
		}
		conn.AddChain(chain)
		if err := conn.Flush(); err != nil {
			return networkPrivilegeError("create firecracker nftables chain "+chain.Name, err)
		}
	}
	return nil
}

func alreadyExistsError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "chain already exists") || strings.Contains(text, "file exists") || strings.Contains(text, "object already exists")
}

func cleanupTransientNetworkDevices(devices []transientNetworkDevice) {
	for _, device := range devices {
		if device.PID > 0 {
			_ = syscall.Kill(device.PID, syscall.SIGTERM)
		}
		if device.Created && device.Name != "" && device.Mode == "tap" {
			if !validMicroagentTapName(device.Name) {
				continue
			}
			_ = deleteTap(device.Name)
		}
	}
}

func cleanupTransientFirewallRules(rules []transientFirewallRule) {
	for i := len(rules) - 1; i >= 0; i-- {
		rule := rules[i]
		if !validMicroagentFirewallRule(rule) {
			continue
		}
		_ = deleteNFTFirewallRule(rule)
	}
}

func validMicroagentTapName(name string) bool {
	if !strings.HasPrefix(name, "magtap") || len(name) != len("magtap")+8 {
		return false
	}
	for _, r := range strings.TrimPrefix(name, "magtap") {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validMicroagentFirewallRule(rule transientFirewallRule) bool {
	if rule.Comment == "" {
		return false
	}
	if rule.Table == nftMicroagentTable {
		if rule.Chain != nftNATPostroutingChain && rule.Chain != nftForwardChain && rule.Chain != nftNATPreroutingChain && rule.Chain != nftManglePreroutingChain && rule.Chain != nftFilterPreroutingChain {
			return false
		}
	} else if rule.Family == "ip" && rule.Table == nftUFWFilterTable && rule.Chain == nftUFWUserForwardChain {
		// microagent may add a tagged allow rule to UFW's user-forward chain
		// so UFW does not drop packets accepted by microagent's own base chain.
	} else {
		return false
	}
	parts := strings.Split(rule.Comment, nftRuleCommentSeparator)
	return len(parts) == 3 && parts[0] == strings.TrimSuffix(nftRuleCommentPrefix, nftRuleCommentSeparator) && validMicroagentTapName(parts[1])
}

func deleteTap(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if linkNotFoundError(err) {
			return nil
		}
		return err
	}
	return netlink.LinkDel(link)
}

func microagentNFTTable() *nftables.Table {
	return &nftables.Table{Name: nftMicroagentTable, Family: nftables.TableFamilyINet}
}

func nftRuleTable(rule transientFirewallRule) *nftables.Table {
	family := nftables.TableFamilyINet
	if rule.Family == "ip" {
		family = nftables.TableFamilyIPv4
	}
	return &nftables.Table{Name: rule.Table, Family: family}
}

func nftRuleExists(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, comment string) (bool, error) {
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return false, err
	}
	for _, rule := range rules {
		if nftRuleCommentFromUserData(rule.UserData) == comment {
			return true, nil
		}
	}
	return false, nil
}

func deleteNFTFirewallRule(rule transientFirewallRule) error {
	conn := &nftables.Conn{}
	table := nftRuleTable(rule)
	chain := &nftables.Chain{Name: rule.Chain, Table: table}
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return err
	}
	deleted := false
	for _, candidate := range rules {
		if nftRuleCommentFromUserData(candidate.UserData) != rule.Comment {
			continue
		}
		if err := conn.DelRule(candidate); err != nil {
			return err
		}
		deleted = true
	}
	if !deleted {
		return nil
	}
	return conn.Flush()
}

func nftRuleComment(tap, kind string) string {
	return nftRuleCommentPrefix + tap + nftRuleCommentSeparator + kind
}

func nftRuleUserData(comment string) []byte {
	return userdata.AppendString(nil, userdata.TypeComment, comment)
}

func nftRuleCommentFromUserData(data []byte) string {
	comment, ok := userdata.GetString(data, userdata.TypeComment)
	if !ok {
		return ""
	}
	return comment
}

func nftIfName(name string) []byte {
	data := make([]byte, 16)
	copy(data, []byte(name+"\x00"))
	return data
}

func ifNameMatchExprs(key expr.MetaKey, name string) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: nftIfName(name)},
	}
}

func ipv4SubnetMatchExprs(offset uint32, network *net.IPNet) []expr.Any {
	mask := []byte(network.Mask)
	if len(mask) != net.IPv4len {
		mask = []byte{255, 255, 255, 255}
	}
	networkIP := network.IP.To4()
	masked := make([]byte, net.IPv4len)
	for i := 0; i < net.IPv4len; i++ {
		masked[i] = networkIP[i] & mask[i]
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: net.IPv4len},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: net.IPv4len, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: masked},
	}
}

func establishedRelatedExprs() []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:            binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
	}
}

func linkNotFoundError(err error) bool {
	var notFound netlink.LinkNotFoundError
	return errors.As(err, &notFound)
}

func networkPrivilegeError(action string, err error) error {
	text := strings.ToLower(err.Error())
	if errors.Is(err, syscall.EPERM) || strings.Contains(text, "operation not permitted") || strings.Contains(text, "permission denied") {
		return fmt.Errorf("%s: firecracker user networking creates a TAP device inside an unprivileged user network namespace; this host may lack unprivileged user namespaces or /dev/net/tun — use --network isolated if outbound network is not needed: %w", action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func tapName(opts Options) string {
	seed := opts.Name
	if seed == "" {
		seed = opts.StateDir
	}
	sum := sha1.Sum([]byte(seed))
	return "magtap" + hexPrefix(sum[:], 8)
}

func firecrackerGuestMAC(name string) string {
	sum := sha1.Sum([]byte(name))
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", sum[0], sum[1], sum[2], sum[3])
}

func firecrackerGuestCID(opts Options) uint32 {
	seed := opts.Name + "\x00" + opts.StateDir
	sum := sha1.Sum([]byte(seed))
	value := uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
	return 3 + value%((1<<31)-3)
}

func hexPrefix(data []byte, n int) string {
	const digits = "0123456789abcdef"
	if n > len(data)*2 {
		n = len(data) * 2
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		b := data[i/2]
		if i%2 == 0 {
			out[i] = digits[b>>4]
		} else {
			out[i] = digits[b&0x0f]
		}
	}
	return string(out)
}
