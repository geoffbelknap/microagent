package vmkit

// This file is the single source of truth behind the supervisor parity tests
// (supervisorparity_test.go), extending the EgressDatapathFields pattern to
// the rest of the host→guest contract. Two silent-divergence classes produced
// real bugs on apple-vf: a vmkit.Config field the Swift supervisor never
// decoded (TimeoutSeconds), and a guest boot parameter one cmdline builder
// emitted but the other silently dropped (microagent_ca_cert_port, which
// disabled mitm CA delivery on macOS). Registering the contract here makes
// the next drop fail CI instead of shipping as a platform mystery.

// GuestBootParam describes one microagent_* kernel command-line key guest
// init parses. Every key must be registered with an explicit per-backend
// emission decision; an asymmetric decision requires a reason.
type GuestBootParam struct {
	// Key is the kernel command-line key exactly as guest init parses it.
	Key string
	// Firecracker reports whether the Firecracker boot-args builder emits it.
	Firecracker bool
	// AppleVF reports whether the apple-vf supervisor's kernel command line
	// emits it.
	AppleVF bool
	// Reason documents WHY the emitters differ. Required when they do.
	Reason string
}

// GuestBootParams is the canonical registry of guest boot parameters. The
// parity tests assert this list matches exactly what guest init parses and
// what each backend's builder emits, so adding a key to any of the three
// without registering the decision here fails CI.
func GuestBootParams() []GuestBootParam {
	return []GuestBootParam{
		{Key: "microagent_config", Firecracker: true, AppleVF: true},
		{Key: "microagent_shell_port", Firecracker: true, AppleVF: true},
		{Key: "microagent_exec_port", Firecracker: true, AppleVF: true},
		{Key: "microagent_secrets_port", Firecracker: true, AppleVF: true},
		{Key: "microagent_ca_cert_port", Firecracker: true, AppleVF: true},
		{Key: "microagent_secrets_api", Firecracker: true, AppleVF: true},
		{Key: "microagent_secrets_ctl_port", Firecracker: true, AppleVF: true},
		{Key: "microagent_model_fwd", Firecracker: true, AppleVF: true},
		{Key: "microagent_hostname", Firecracker: true, AppleVF: true},
		{Key: "microagent_net_if", Firecracker: true, AppleVF: true},
		{Key: "microagent_net_ip", Firecracker: true, AppleVF: true},
		{Key: "microagent_net_gw", Firecracker: true, AppleVF: true},
		{Key: "microagent_net_dns", Firecracker: true, AppleVF: true},
		{Key: "microagent_dns", Firecracker: false, AppleVF: true,
			Reason: "nameservers for the ip=dhcp path; the firecracker builder always emits static addressing, so the DHCP DNS channel is apple-vf-only"},
		{Key: "microagent_dns_fallback_gateway", Firecracker: false, AppleVF: true,
			Reason: "companion to microagent_dns on the apple-vf DHCP path"},
		{Key: "microagent_shutdown", Firecracker: true, AppleVF: false,
			Reason: "i8042-reset shutdown ordering is a Firecracker VMM behavior; Virtualization.framework guests power off cleanly without it"},
	}
}

// AppleVFUndecodedConfigFields maps vmkit.Config JSON field names the apple-vf
// supervisor intentionally does NOT decode to the reason. The parity test
// asserts every Config field is either declared in the Swift Config struct or
// listed here — a new field silently dropped by the supervisor (and, via its
// runtime.json round-trip, erased from persisted state on macOS) fails CI
// until the decision is made explicitly.
func AppleVFUndecodedConfigFields() map[string]string {
	return map[string]string{
		"timeoutSeconds":    "the run bound is enforced by the host dispatch today; supervisor-side enforcement awaits the one-shot/persistent request split so persistent workspaces are not killed at the dispatch timeout",
		"broker":            "broker endpoints are a recorded backend gap (gap.broker.apple-vf); the workspace layer composes no broker listener on apple-vf",
		"brokers":           "broker endpoints are a recorded backend gap (gap.broker.apple-vf); the workspace layer composes no broker listener on apple-vf",
		"bakedVsockUDSPath": "firecracker snapshot mechanics (the vsock UDS path baked into saved VM state); Virtualization.framework save/restore has no equivalent",
		"maintenanceBoot":   "consumed host-side before dispatch on every backend; no supervisor reads it",
	}
}
