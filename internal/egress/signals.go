package egress

// Non-cooperation signals. A well-behaved workload never tries to route around
// the mediator, so any of these is anomalous by definition — evasion made
// conspicuous. They ride the "signal" field on the audit records that mediation
// already writes (tenet 2); the mediator only detects and emits, it never
// decides the response (that is the consumer's policy, e.g. planed onSignal).
const (
	// SignalDenied marks any fail-closed drop: an inside/metadata destination,
	// or an off-allowlist destination under a locked allowlist.
	SignalDenied = "denied"
	// SignalDirectIPNoSNI marks an allowed connection to a bare public IP with
	// no SNI — permitted (broker is allow-broad) but conspicuous, because a
	// cooperative client resolves names first.
	SignalDirectIPNoSNI = "direct-ip-no-sni"
	// SignalNameDestinationMismatch marks a guest-asserted HTTP Host or TLS SNI
	// that was not bound by observed DNS to the address actually dialed.
	SignalNameDestinationMismatch = "name-destination-mismatch"
	// SignalQUICUDP443 marks a UDP:443 datagram that cannot be authenticated as
	// a supported QUIC Initial. It fails closed. Valid QUIC and STUN datagrams
	// continue through normal destination policy.
	SignalQUICUDP443 = "quic-udp443"
	// SignalForeignResolver marks a DNS query aimed at a resolver other than the
	// mediator. The guest cannot actually reach it (all DNS is TPROXY'd to the
	// mediator); the attempt itself is the tell.
	SignalForeignResolver = "foreign-resolver"
	// SignalResolverDenied marks a DNS query the mediator REFUSED to forward
	// because the target resolver address is not permitted: it is not in the
	// workspace's configured resolver set (when one is configured) or, absent an
	// explicit set, it is an internal/loopback/link-local/metadata address the
	// mediator — unlike the guest — could route to. Refusing here stops the
	// mediator being used as a confused-deputy relay to an arbitrary :53.
	SignalResolverDenied = "resolver-denied"
	// SignalDNSOverHTTPS marks an HTTP request whose path or media type identifies
	// DNS-over-HTTPS. MITM mode denies the request before any bytes reach upstream;
	// opaque broker TLS cannot inspect request semantics and does not emit it.
	SignalDNSOverHTTPS = "dns-over-https"
	// SignalUnmediatableProtocol marks guest egress dropped at the datapath
	// because its protocol carries no destination identity the mediator could
	// allowlist (IPv4 ICMP and any other non-TCP/UDP L4). These packets never
	// reach the mediator — they are dropped at the firewall — so the mediator
	// learns of them from a datapath drop counter rather than from a flow, and
	// reports counts rather than destinations. See Options.DropCounters.
	SignalUnmediatableProtocol = "unmediatable-protocol"
	// SignalUnresolvedSecretRef marks a broker request carrying a credential
	// reference the broker could not resolve — a fail-closed workload error that
	// joins the same taxonomy.
	SignalUnresolvedSecretRef = "unresolved-secret-ref"
)

// AllSignals is the closed vocabulary, so a consumer (planed onSignal) can map
// every kind exhaustively.
var AllSignals = []string{
	SignalDenied,
	SignalDirectIPNoSNI,
	SignalNameDestinationMismatch,
	SignalQUICUDP443,
	SignalForeignResolver,
	SignalResolverDenied,
	SignalDNSOverHTTPS,
	SignalUnmediatableProtocol,
	SignalUnresolvedSecretRef,
}
