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
	// SignalQUICUDP443 marks a UDP:443 (QUIC / HTTP-3) attempt. It is still
	// dropped — clients fall back to TCP/TLS where the broker governs them — but
	// the attempt is now a tagged event rather than a silent drop.
	SignalQUICUDP443 = "quic-udp443"
	// SignalForeignResolver marks a DNS query aimed at a resolver other than the
	// mediator. The guest cannot actually reach it (all DNS is TPROXY'd to the
	// mediator); the attempt itself is the tell.
	SignalForeignResolver = "foreign-resolver"
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
	SignalQUICUDP443,
	SignalForeignResolver,
	SignalUnresolvedSecretRef,
}
