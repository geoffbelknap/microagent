package egress

import (
	"context"
	"crypto/x509"
	"net/netip"
)

// Brain is the transport-agnostic governance core shared by every microagent
// execution shape. It owns the security decision — the default-deny allowlist,
// the guarded inside/rebinding defense, credential swap, and audit — independent
// of how bytes are carried. The byte-stream transport (Handler, for microVM
// workspaces and the apple-vf host-fd datapath) and the structured transport
// (the wasm sandbox's host-fetch) both drive THIS one decision sequence, so a
// destination is allowed, denied, and audited identically regardless of shape.
//
// A Brain holds no per-connection state; it is cheap to construct as a view over
// a Handler's policy fields (Handler.brain) or stand-alone for the sandbox
// (NewBrain). The transport-specific machinery — MITM, the L4 splice, byte/rate
// caps, peer/DNS reverse resolution, the host-fetch round-trip — lives around the
// brain, not inside it.
type Brain struct {
	Mode     string // "guarded" (deny inside/infra) | "strict" (deny non-allowlisted) | "" (=> guarded)
	Policy   *Policy
	Swaps    *SwapTable
	Resolver resolver
	Cache    *tokenCache
	Logger   Logger
	Limits   Limits
	// UpstreamRoots optionally overrides the system roots used to verify the real
	// upstream certificate on the host-fetch path (Fetch). Nil uses the system
	// pool. It is never used to disable verification.
	UpstreamRoots *x509.CertPool
}

// NewBrain builds a stand-alone governance brain for a structured transport (the
// wasm sandbox). It wires the same secret resolver + token cache the byte-stream
// path uses (mirroring Handler.EnableSwaps), so credential swaps resolve
// host-side identically across shapes. Secret-resolution warnings are routed to
// the audit log, never the credential itself.
func NewBrain(mode string, policy *Policy, swaps *SwapTable, logger Logger, limits Limits) *Brain {
	b := &Brain{Mode: mode, Policy: policy, Swaps: swaps, Logger: logger, Limits: limits}
	if swaps != nil {
		b.Cache = newTokenCache()
		b.Resolver = NewKeyResolver(func(msg string) {
			if logger != nil {
				logger.Log("egress_secret_warning", map[string]any{"warning": msg})
			}
		})
	}
	return b
}

// Verdict is the outcome of Brain.Evaluate. Allowed excludes any passthrough
// override (a byte-stream-transport concern the caller applies separately), so a
// caller can keep its own passthrough/MITM branching while still sharing the
// allowlist+guarded decision.
type Verdict struct {
	Allowed  bool   // permitted by the allowlist or by guarded mode (NOT counting passthrough)
	Unlisted bool   // permitted ONLY by guarded mode — not on the allowlist (a looser grant, audited)
	Inside   bool   // guarded classified the resolved IP as inside/infrastructure
	Reason   string // policy reason for the allowlist result (stamped on a deny audit)
}

// Evaluate rules on an egress attempt: it checks the allowlist against host and
// any additional candidate identities (an east-west peer's workspace name and IP
// literal), applies the guarded inside-deny on the resolved connect IP (which
// also defeats DNS rebinding — the IP, not a guest-supplied name, is what gets
// classified), and returns the verdict. It is PURE: no audit, no I/O. That is
// what lets every transport — Handler.Handle, the UDP datapath, and the wasm
// sandbox — share one decision sequence. passthrough is supplied by the caller
// and excluded from Allowed so the caller keeps its own passthrough handling.
func (b *Brain) Evaluate(host string, candidates []string, dstIP netip.Addr, passthrough bool) Verdict {
	d := b.Policy.AllowHost(host)
	if !d.Allow {
		for _, c := range candidates {
			if c == "" || c == host {
				continue
			}
			if cd := b.Policy.AllowHost(c); cd.Allow {
				d = cd
				break
			}
		}
	}
	inside := allowsBroad(b.Mode) && isInsideAddr(dstIP)
	allowed := d.Allow || (allowsBroad(b.Mode) && !inside)
	unlisted := allowed && !d.Allow && !passthrough
	return Verdict{Allowed: allowed, Unlisted: unlisted, Inside: inside, Reason: d.Reason}
}

// AuditDeny records a fail-closed denial for the TCP byte-stream and structured
// (host-fetch) paths: egress_internal_deny when guarded classified the
// destination as inside/infrastructure, otherwise egress_deny, stamping the
// policy reason. fields carries the transport's identifying context (host, dst,
// and any peer fields); AuditDeny adds reason (and internal, when inside). The
// UDP path keeps its own egress_udp_* event names but shares Evaluate for the
// decision, so the allow/deny math is single-sourced even where the audit event
// names differ by transport.
func (b *Brain) AuditDeny(v Verdict, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	event := "egress_deny"
	fields["reason"] = v.Reason
	if v.Inside {
		event = "egress_internal_deny"
		fields["reason"] = "guarded: internal destination denied"
		fields["internal"] = true
	}
	b.Logger.Log(event, fields)
}

// SwapFor resolves the real credential to inject for host when a swap entry
// matches, acquiring it HOST-side so the secret never reaches the guest
// (cred-blind). matched is false when no entry applies (the caller forwards the
// request unchanged). A match whose acquisition fails returns matched=true with
// the error AND audits egress_swap_error; the caller MUST fail the request closed
// — never forward it unauthenticated. A successful acquisition audits egress_swap
// (host/swap/type only — never the credential or resolved secret).
//
// This is the structured-transport analogue of the per-request injection
// injectRequests performs on the byte-stream MITM path; both acquire through the
// same Swapper, so the credential-acquisition strategy is single-sourced.
func (b *Brain) SwapFor(ctx context.Context, host string) (header, value string, matched bool, err error) {
	if b.Swaps == nil {
		return "", "", false, nil
	}
	e, ok := b.Swaps.Match(host)
	if !ok {
		return "", "", false, nil
	}
	sw := &Swapper{Resolver: b.Resolver, Cache: b.Cache}
	hdr, val, aerr := sw.acquire(ctx, e)
	if aerr != nil {
		b.Logger.Log("egress_swap_error", map[string]any{"host": host, "swap": e.Name, "type": e.Type, "error": aerr.Error()})
		return "", "", true, aerr
	}
	b.Logger.Log("egress_swap", map[string]any{"host": host, "swap": e.Name, "type": e.Type})
	return hdr, val, true, nil
}
