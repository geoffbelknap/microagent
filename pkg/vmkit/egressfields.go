package vmkit

// EgressDatapathField describes one egress-policy control that must be forwarded
// from vmkit.Config to BOTH mediator datapaths: the Firecracker supervisor's
// --egress-mediator and the Apple VF host-fd --egress-datapath. It is the single
// source of truth behind the datapath parity tests.
//
// A control honored by one datapath but silently dropped by the other is a
// security fail-open — the operator asks for a bound (an allowlist, a rate cap,
// a resolver restriction) and gets none, while state still reports it as set.
// That exact class produced review findings B1 (egress mode), B22
// (--lock-allowlist), and B23 (caps + resolvers). Enumerating the controls here
// lets the parity tests assert every one is present on both flag surfaces, so
// the next dropped field fails CI instead of a live security repro.
type EgressDatapathField struct {
	// ConfigField is the vmkit.Config field the control derives from, or "" for a
	// control sourced elsewhere (resolvers come from Network.DNS, not an Egress*
	// field).
	ConfigField string
	// MediatorFlag is the Firecracker --egress-mediator flag name (no leading --).
	MediatorFlag string
	// DatapathFlag is the Apple VF --egress-datapath flag name (no leading --).
	DatapathFlag string
	// Security marks a control whose omission is a fail-open (allowlist, caps,
	// resolver restriction) rather than a cosmetic difference.
	Security bool
}

// EgressDatapathFields is the canonical set of egress-policy controls both
// mediator datapaths must accept. Add an entry here whenever you add an egress
// field to vmkit.Config; the parity tests fail until both datapaths forward it.
func EgressDatapathFields() []EgressDatapathField {
	return []EgressDatapathField{
		{ConfigField: "EgressMode", MediatorFlag: "mode", DatapathFlag: "egress-mode", Security: true},
		{ConfigField: "EgressAllow", MediatorFlag: "allow", DatapathFlag: "allow", Security: true},
		{ConfigField: "EgressPassthrough", MediatorFlag: "passthrough", DatapathFlag: "passthrough", Security: true},
		{ConfigField: "EgressAllowlistLocked", MediatorFlag: "lock-allowlist", DatapathFlag: "lock-allowlist", Security: true},
		{ConfigField: "EgressSwapConfigPath", MediatorFlag: "swap-config", DatapathFlag: "swap-config", Security: true},
		{ConfigField: "EgressMaxBytesPerSec", MediatorFlag: "max-bps", DatapathFlag: "max-bps", Security: true},
		{ConfigField: "EgressMaxTotalBytes", MediatorFlag: "max-bytes", DatapathFlag: "max-bytes", Security: true},
		{ConfigField: "EgressMaxConcurrentConns", MediatorFlag: "max-conns", DatapathFlag: "max-conns", Security: true},
		{ConfigField: "EgressAuditMaxBytes", MediatorFlag: "audit-max-bytes", DatapathFlag: "audit-max-bytes", Security: false},
		{ConfigField: "EgressAuditMaxBackups", MediatorFlag: "audit-max-backups", DatapathFlag: "audit-max-backups", Security: false},
		// Resolvers derive from the workspace nameservers (Network.DNS), not an
		// Egress* field, so ConfigField is empty; both datapaths still forward it.
		{ConfigField: "", MediatorFlag: "resolver", DatapathFlag: "resolver", Security: true},
	}
}
