//go:build linux

package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// egress_linux.go is the in-guest half of windows-hyperv egress mediation (P4).
//
// When the host signals mediation is on (microagent_egress_mediator_port=<P> on
// the kernel cmdline), the guest must, BEFORE handing off to the workload,
// transparently capture guest-originated egress and ship it to the host mediator
// over hvsock. The guest's own apps generate traffic locally, so it traverses the
// OUTPUT path, NOT prerouting — this is the key divergence from the firecracker
// model (pkg/supervisors/firecracker/egress_linux.go), which steers guest packets
// arriving on a host tap in PREROUTING. TPROXY is invalid in OUTPUT, so:
//
//   - TCP: an inet nat/output chain REDIRECTs guest-originated TCP to a local
//     forwarder port. The forwarder accepts, recovers the original destination via
//     SO_ORIGINAL_DST (internal/egress.OriginalDestination), dials the host
//     mediator at AF_VSOCK egress.DefaultMediatorVsockPort, writes a DestHeader,
//     and pumps bytes. Loops are avoided by skipping (a) loopback output and (b)
//     traffic from the forwarder's own uid.
//   - Fail-closed: an inet filter/output chain DROPs all guest IPv6 egress and ALL
//     non-TCP L4 (including UDP/53 — no unmediated DNS leak). Guest DNS is forced
//     onto TCP via /etc/resolv.conf ("options use-vc"), so the resolver opens a
//     TCP/53 connection; the nat/output REDIRECT captures it like any other TCP and
//     it is shipped as a proto-"tcp" DestHeader to port 53, where the host mediator
//     resolves it through its filtering DNS-over-TCP handler. UDP DNS cannot be
//     mediated from the OUTPUT chain (TPROXY is invalid there), so it is dropped.
//
// All rules are inet-family, matching the firecracker expr style (REDIRECT via
// expr.Redir, DROP verdicts in a filter chain), and are installed ONLY when the
// cmdline param is present.

// egressForwarderUID is the dedicated uid the forwarder helper subprocess runs as.
// The nat/output REDIRECT skips this uid so the forwarder's OWN outbound traffic is
// never re-redirected (defense-in-depth loop avoidance; the forwarder dials the
// host over AF_VSOCK, which an inet/output chain cannot see anyway, but matching on
// uid is the standard transparent-proxy guard and is correct if the dial path ever
// becomes IP-based). It is a fixed nonzero uid distinct from root so init (uid 0)
// and the workload still hit the REDIRECT.
const egressForwarderUID uint32 = 30000

// egressForwarderPort is the loopback TCP port the forwarder listens on and the
// REDIRECT targets. Fixed and guest-local; nothing else in the guest binds it.
const egressForwarderPort uint16 = 41032

// dnsPort is the DNS service port. Guest DNS is forced onto TCP (resolv.conf
// "options use-vc"), so a TCP/53 connection is captured by the nat/output
// REDIRECT and mediated; UDP/53 is dropped by the fail-closed filter chain.
const dnsPort uint16 = 53

// nftInet aliases the google/nftables inet table family so tests and the builder
// reference a single value.
const nftInet = nftables.TableFamilyINet

const (
	guestEgressTableName   = "microagent-egress"
	guestEgressNATChain    = "output-redirect"
	guestEgressFilterChain = "output-failclosed"
)

// guestEgressRuleset is the concrete nft program the forwarder installs, plus
// boolean attestations of the security-relevant properties the unit test asserts
// without needing a live kernel. The table/chains/exprs are exactly what is
// flushed to netfilter; the flags describe what those exprs encode.
type guestEgressRuleset struct {
	table       *nftables.Table
	natChain    *nftables.Chain
	filterChain *nftables.Chain

	// nat/output REDIRECT split into three separate rules (one match -> verdict
	// each), so the kernel does NOT AND the leading oifname/skuid matches with the
	// REDIRECT. Concatenating them into a single rule made the leading oifname=="lo"
	// (false for real egress on eth0) gate the whole rule, so the REDIRECT never
	// fired and traffic fell through policy accept unmediated.
	skipLoExprs   []expr.Any // nat/output rule 1: oifname "lo" -> return
	skipUIDExprs  []expr.Any // nat/output rule 2: meta skuid <forwarderUID> -> return
	redirectExprs []expr.Any // nat/output rule 3: meta l4proto tcp -> redirect to forwarderPort

	v6DropExprs   []expr.Any // filter/output: drop all ipv6
	tcpAllowExprs []expr.Any // filter/output: accept tcp (already REDIRECTed)
	otherL4Exprs  []expr.Any // filter/output: drop everything else (incl UDP/53)

	skipsForwarderUID bool
	skipsLoopback     bool
	dropsIPv6         bool
	permitsDNSUDP     bool // false now: UDP/53 is dropped, DNS goes over TCP through the mediator
	dropsOtherL4      bool
}

// buildGuestEgressRuleset constructs the inet-family OUTPUT-path ruleset for the
// given forwarder uid and TCP port. It mirrors the firecracker expr style but in
// the OUTPUT hook: nat/output REDIRECT for TCP (with loopback + own-uid skips for
// loop avoidance) and a filter/output fail-closed program (drop v6, accept udp/53,
// accept tcp, drop the rest).
func buildGuestEgressRuleset(forwarderUID uint32, forwarderPort uint16) guestEgressRuleset {
	table := &nftables.Table{Family: nftInet, Name: guestEgressTableName}
	natChain := &nftables.Chain{
		Name:     guestEgressNATChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityNATDest,
	}
	filterChain := &nftables.Chain{
		Name:     guestEgressFilterChain,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
	}

	// nat/output REDIRECT, split into THREE separate rules so the kernel evaluates
	// each match->verdict independently (insertion order). A single concatenated
	// rule ANDs all its matches: the leading oifname=="lo" (false for real egress on
	// eth0) would gate the REDIRECT, so it would never fire and traffic would fall
	// through policy accept unmediated. nft(8) rejects the concatenated form as
	// "Statement after terminal statement has no effect"; the netlink path installs
	// it silently. Mirroring the firecracker fail-closed loop (each rule is one
	// match->verdict) keeps the two guards as their own RETURNs preceding the
	// REDIRECT:
	//   1. oifname "lo" -> return    (do not redirect guest-local loopback)
	//   2. meta skuid <uid> -> return (the forwarder's own traffic; loop avoidance)
	//   3. meta l4proto tcp -> redirect to :forwarderPort
	// REDIRECT is DNAT-to-localhost, so the forwarder recovers the original
	// destination via SO_ORIGINAL_DST.
	skipLoExprs := []expr.Any{
		// oifname == "lo" -> return (do not redirect guest-local loopback)
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: nftIfName("lo")},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
	skipUIDExprs := []expr.Any{
		// meta skuid == forwarderUID -> return (the forwarder's own traffic)
		&expr.Meta{Key: expr.MetaKeySKUID, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(forwarderUID)},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
	redirectExprs := []expr.Any{
		// l4proto == tcp
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		// redirect to :forwarderPort
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(forwarderPort)},
		&expr.Redir{RegisterProtoMin: 1, RegisterProtoMax: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
	}

	// filter/output fail-closed program. Rules are evaluated in insertion order:
	//  1. nfproto == ipv6 -> drop   (no v6 channel escapes)
	//  2. l4proto == tcp -> accept  (already REDIRECTed at the nat hook; incl TCP/53)
	//  3. (catch-all) -> drop       (ICMP, ALL UDP incl UDP/53, other L4: fail closed)
	// There is NO UDP/53 carve-out: guest DNS is forced onto TCP (resolv.conf
	// "options use-vc"), so it is captured by the TCP REDIRECT and mediated. Any
	// UDP/53 that still escapes the resolver (e.g. a statically-configured app)
	// hits the catch-all drop — no unmediated DNS leak.
	v6DropExprs := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV6}},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
	tcpAllowExprs := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
	otherL4Exprs := []expr.Any{
		// Catch-all: anything reaching here is neither ipv6 nor tcp — drop it
		// (all UDP including UDP/53, ICMP, and any other L4).
		&expr.Verdict{Kind: expr.VerdictDrop},
	}

	return guestEgressRuleset{
		table:         table,
		natChain:      natChain,
		filterChain:   filterChain,
		skipLoExprs:   skipLoExprs,
		skipUIDExprs:  skipUIDExprs,
		redirectExprs: redirectExprs,
		v6DropExprs:   v6DropExprs,
		tcpAllowExprs: tcpAllowExprs,
		otherL4Exprs:  otherL4Exprs,

		skipsForwarderUID: true,
		skipsLoopback:     true,
		dropsIPv6:         true,
		permitsDNSUDP:     false,
		dropsOtherL4:      true,
	}
}

// nftIfName encodes an interface name to the fixed 16-byte buffer nft compares
// against (IFNAMSIZ), NUL-padded. Mirrors the firecracker supervisor's helper.
func nftIfName(name string) []byte {
	buf := make([]byte, 16)
	copy(buf, name)
	return buf
}

// installGuestEgress installs the ruleset (buildGuestEgressRuleset) into netfilter
// and starts the transparent forwarder subprocess. It is the orchestration entry
// the boot path calls when cfg.EgressMediatorPort != 0. The forwarder runs as a
// subprocess (like the model forwarder) so it survives modes where run() hands off
// via syscall.Exec, AND so it can run under egressForwarderUID — the uid the
// REDIRECT skips for loop avoidance.
func installGuestEgress(mediatorVsockPort uint32) error {
	if mediatorVsockPort == 0 {
		return nil
	}
	if err := bringUpLoopback(); err != nil {
		return fmt.Errorf("egress forwarder: bring up loopback: %w", err)
	}
	// Allow the nat/output REDIRECT to steer guest-originated TCP to the loopback
	// forwarder. The REDIRECT rewrites the destination to 127.0.0.1:<forwarder>,
	// but the kernel martian-drops a packet routed to 127.0.0.0/8 that did not
	// originate on loopback UNLESS route_localnet is enabled on the egress
	// interface. Without this every captured connection is silently dropped before
	// it reaches the forwarder. Enable it before installing the ruleset so there is
	// never a window where the REDIRECT fires but the packet is dropped.
	if err := enableRouteLocalnet(); err != nil {
		return fmt.Errorf("egress forwarder: enable route_localnet: %w", err)
	}
	rs := buildGuestEgressRuleset(egressForwarderUID, egressForwarderPort)
	if err := applyGuestEgressRuleset(rs); err != nil {
		return fmt.Errorf("egress forwarder: install nft ruleset: %w", err)
	}
	// Force guest DNS onto TCP so it is captured by the TCP REDIRECT and mediated.
	// The fail-closed filter chain now drops UDP/53, so without this the guest
	// resolver (which defaults to UDP) would be silently blocked. Must run AFTER
	// the ruleset is in place so there is never a window where UDP DNS both works
	// and is unmediated.
	if err := writeMediatedResolvConf(); err != nil {
		return fmt.Errorf("egress forwarder: write mediated resolv.conf: %w", err)
	}
	cmd := exec.Command(os.Args[0], "egress-forward-helper",
		strconv.Itoa(int(egressForwarderPort)),
		strconv.FormatUint(uint64(mediatorVsockPort), 10))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: egressForwarderUID, Gid: egressForwarderUID},
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("egress forwarder: start helper: %w", err)
	}
	log.Printf("microagent-init: egress forwarder listening on 127.0.0.1:%d, mediator hvsock port %d", egressForwarderPort, mediatorVsockPort)
	return nil
}

// routeLocalnetPath is the sysctl that gates routing to 127.0.0.0/8 from a
// non-loopback path. The "all" knob applies to every interface, which is what
// the OUTPUT-hook REDIRECT to the loopback forwarder needs.
const routeLocalnetPath = "/proc/sys/net/ipv4/conf/all/route_localnet"

// enableRouteLocalnet sets net.ipv4.conf.all.route_localnet=1 so the nat/output
// REDIRECT's rewritten 127.0.0.1 destination is routed to the loopback forwarder
// instead of being martian-dropped. Split from the path so the write is
// unit-testable without /proc.
func enableRouteLocalnet() error {
	return writeRouteLocalnetAt(routeLocalnetPath)
}

// writeRouteLocalnetAt writes "1" to path (the route_localnet sysctl).
func writeRouteLocalnetAt(path string) error {
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// mediatedResolvConfNameserver is the placeholder nameserver written into the
// mediated guest's resolv.conf. The actual address is irrelevant: the nft
// nat/output REDIRECT rewrites the destination of any guest-originated TCP/53
// connection to the local forwarder regardless of the configured server, so the
// resolver only needs SOME address to open a TCP virtual circuit to. A
// documentation-range TEST-NET-1 address (RFC 5737) makes it obvious in the
// guest's resolv.conf that the value is a stand-in, not a real resolver.
const mediatedResolvConfNameserver = "192.0.2.53"

// writeMediatedResolvConf writes /etc/resolv.conf for a mediated guest. It is
// called from installGuestEgress after the fail-closed ruleset (which drops
// UDP/53) is installed.
func writeMediatedResolvConf() error {
	return writeMediatedResolvConfAt("/etc/resolv.conf")
}

// writeMediatedResolvConfAt writes the mediated resolv.conf to path. It forces
// the guest resolver onto TCP so DNS leaves as TCP/53 (captured by the REDIRECT
// and mediated) rather than UDP/53 (dropped by the fail-closed chain):
//
//   - "options use-vc" makes glibc/musl use a TCP virtual circuit for queries
//     instead of UDP.
//   - "options single-request" sends the A and AAAA lookups sequentially rather
//     than parallelizing them on one socket, which is the more robust pattern for
//     a serial TCP resolver and avoids a known glibc parallel-A/AAAA stall.
//
// The nameserver address is a placeholder (see mediatedResolvConfNameserver):
// the REDIRECT rewrites the TCP/53 destination regardless. Split out from
// writeMediatedResolvConf so the content is unit-testable without touching /etc.
func writeMediatedResolvConfAt(path string) error {
	content := "nameserver " + mediatedResolvConfNameserver + "\n" +
		"options use-vc\n" +
		"options single-request\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// applyGuestEgressRuleset flushes the built ruleset to netfilter via google/nftables.
//
// The nat/output REDIRECT is installed as THREE separate rules in order
// (skip-lo return, skip-uid return, tcp redirect): nft evaluates rules in
// insertion order, so the two RETURN guards precede the REDIRECT and each is its
// own match->verdict. They MUST NOT be concatenated into one rule — the kernel
// would AND the matches, so the leading oifname=="lo" (false for real egress)
// would gate the REDIRECT and it would never fire (the original capture bug).
//
// The filter-chain rules are likewise added in precedence order (v6 drop, tcp
// accept, catch-all drop): the drop must be last. A single Flush commits the
// whole program atomically.
func applyGuestEgressRuleset(rs guestEgressRuleset) error {
	conn := &nftables.Conn{}
	conn.AddTable(rs.table)
	conn.AddChain(rs.natChain)
	conn.AddChain(rs.filterChain)
	for _, exprs := range [][]expr.Any{rs.skipLoExprs, rs.skipUIDExprs, rs.redirectExprs} {
		conn.AddRule(&nftables.Rule{Table: rs.table, Chain: rs.natChain, Exprs: exprs})
	}
	for _, exprs := range [][]expr.Any{rs.v6DropExprs, rs.tcpAllowExprs, rs.otherL4Exprs} {
		conn.AddRule(&nftables.Rule{Table: rs.table, Chain: rs.filterChain, Exprs: exprs})
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush guest egress nftables: %w", err)
	}
	return nil
}

// runEgressDump prints the installed guest egress nft table/chains/rules to
// stdout. It is a diagnostic subcommand (microagent-init egress-dump) for the
// common case where the guest image ships no nft(8) binary (e.g. busybox): it
// reads the live ruleset back via the same google/nftables netlink interface the
// forwarder installed it with, so an operator can verify the inet OUTPUT
// REDIRECT + fail-closed drops came up inside a booted guest.
func runEgressDump([]string) int {
	conn := &nftables.Conn{}
	tables, err := conn.ListTablesOfFamily(nftInet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list inet tables: %v\n", err)
		return 1
	}
	for _, t := range tables {
		if t.Name != guestEgressTableName {
			continue
		}
		fmt.Printf("table inet %s\n", t.Name)
		chains, err := conn.ListChainsOfTableFamily(nftInet)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list chains: %v\n", err)
			return 1
		}
		for _, c := range chains {
			if c.Table == nil || c.Table.Name != t.Name {
				continue
			}
			fmt.Printf("  chain %s (type %v hook %v prio %d)\n", c.Name, c.Type, hookName(c.Hooknum), chainPriority(c.Priority))
			rules, err := conn.GetRules(t, c)
			if err != nil {
				fmt.Fprintf(os.Stderr, "get rules for %s: %v\n", c.Name, err)
				return 1
			}
			for i, r := range rules {
				fmt.Printf("    rule %d: %d exprs %s\n", i, len(r.Exprs), summarizeExprs(r.Exprs))
			}
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "no inet table %q installed\n", guestEgressTableName)
	return 1
}

func hookName(h *nftables.ChainHook) string {
	if h == nil {
		return "?"
	}
	switch *h {
	case *nftables.ChainHookOutput:
		return "output"
	case *nftables.ChainHookPrerouting:
		return "prerouting"
	default:
		return fmt.Sprintf("%d", *h)
	}
}

func chainPriority(p *nftables.ChainPriority) int32 {
	if p == nil {
		return 0
	}
	return int32(*p)
}

// summarizeExprs renders a compact, human-checkable tag list for a rule's exprs
// (verdict kind, redir, key metas) so the dump is legible without nft(8).
func summarizeExprs(exprs []expr.Any) string {
	var tags []string
	for _, e := range exprs {
		switch v := e.(type) {
		case *expr.Verdict:
			switch v.Kind {
			case expr.VerdictDrop:
				tags = append(tags, "drop")
			case expr.VerdictAccept:
				tags = append(tags, "accept")
			case expr.VerdictReturn:
				tags = append(tags, "return")
			}
		case *expr.Redir:
			tags = append(tags, "redirect")
		case *expr.Meta:
			tags = append(tags, "meta:"+metaKeyName(v.Key))
		case *expr.Payload:
			tags = append(tags, "payload")
		}
	}
	return strings.Join(tags, ",")
}

func metaKeyName(k expr.MetaKey) string {
	switch k {
	case expr.MetaKeyOIFNAME:
		return "oifname"
	case expr.MetaKeySKUID:
		return "skuid"
	case expr.MetaKeyL4PROTO:
		return "l4proto"
	case expr.MetaKeyNFPROTO:
		return "nfproto"
	default:
		return fmt.Sprintf("%d", k)
	}
}

// runEgressForwardHelper is the blocking accept loop the egress-forward-helper
// subprocess runs. It listens on 127.0.0.1:forwarderPort (the REDIRECT target) and
// for each captured connection recovers the original destination, dials the host
// mediator over hvsock, and bridges the two with forwardEgressConn.
func runEgressForwardHelper(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: microagent-init egress-forward-helper <forwarderPort> <mediatorVsockPort>")
		return 127
	}
	forwarderPort, err := parseUint16(args[0])
	if err != nil || forwarderPort == 0 {
		fmt.Fprintf(os.Stderr, "parse forwarder port: %v\n", err)
		return 127
	}
	mediatorVsockPort, err := strconv.ParseUint(strings.TrimSpace(args[1]), 10, 32)
	if err != nil || mediatorVsockPort == 0 {
		fmt.Fprintf(os.Stderr, "parse mediator vsock port: %v\n", err)
		return 127
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", forwarderPort))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen egress forwarder on 127.0.0.1:%d: %v\n", forwarderPort, err)
		return 127
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return 0
		}
		go serveCapturedEgressConn(conn, uint32(mediatorVsockPort))
	}
}

// serveCapturedEgressConn handles one REDIRECTed TCP connection: recover the
// pre-REDIRECT destination, dial the host mediator over hvsock, and bridge.
func serveCapturedEgressConn(conn net.Conn, mediatorVsockPort uint32) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		_ = conn.Close()
		return
	}
	origDst, err := egress.OriginalDestination(tcpConn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recover egress original destination: %v\n", err)
		_ = conn.Close()
		return
	}
	fd, err := dialHostVsock(mediatorVsockPort, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial egress mediator hvsock port %d: %v\n", mediatorVsockPort, err)
		_ = conn.Close()
		return
	}
	mediator := os.NewFile(uintptr(fd), "egress-mediator-vsock")
	if mediator == nil {
		_ = unix.Close(fd)
		_ = conn.Close()
		return
	}
	forwardEgressConn(conn, origDst, mediator)
}

// forwardEgressConn writes the egress DestHeader naming origDst to the mediator
// stream, then pumps bytes bidirectionally between the captured local connection
// and the mediator. It closes both ends on return. Extracted so the per-connection
// logic (header framing + pump) is unit-testable with a net.Pipe and a fake
// mediator, independent of SO_ORIGINAL_DST recovery and the hvsock dial.
func forwardEgressConn(local net.Conn, origDst netip.AddrPort, mediator io.ReadWriteCloser) {
	defer func() { _ = local.Close() }()
	defer func() { _ = mediator.Close() }()
	hdr := egress.DestHeader{
		Proto: "tcp",
		Host:  origDst.Addr().String(),
		Port:  origDst.Port(),
	}
	if err := egress.WriteDestHeader(mediator, hdr); err != nil {
		fmt.Fprintf(os.Stderr, "write egress dest header %s: %v\n", origDst, err)
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(mediator, local)
		closeWriteRW(mediator)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(local, mediator)
		closeWriteConn(local)
		done <- struct{}{}
	}()
	<-done
	<-done
}

// closeWriteRW half-closes the write side of the mediator stream when it is an
// *os.File (the hvsock fd) so the peer sees EOF after the request body. net.Pipe
// (used in tests) has no half-close; a full Close on return is the fallback.
func closeWriteRW(rw io.ReadWriteCloser) {
	if f, ok := rw.(*os.File); ok {
		_ = unix.Shutdown(int(f.Fd()), unix.SHUT_WR)
		return
	}
	if cw, ok := rw.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
