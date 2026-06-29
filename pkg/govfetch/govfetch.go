// Package govfetch is microagent's public, cross-module seam for HOST-SIDE
// governed fetch. It wraps the shared egress brain (internal/egress) so a
// SEPARATE module — e.g. microagency — can perform a default-deny, cred-blind,
// audited HTTP fetch host-side: the trusted half of the cheap composed path
//
//	govfetch.Fetch (host, cred-blind) → bytes → pkg/sandbox (pure compute) → summary → model
//
// The wasm sandbox stays pure compute (no network, no credentials). ALL
// credentialed I/O happens here, host-side, governed by the SAME Brain decision
// sequence the microVM datapath uses — one brain, not a parallel path.
//
// # Cred-blindness (load-bearing)
//
// The credential named by Spec.SwapConfigPath is resolved host-side and injected
// into the UPSTREAM request only. It MUST NEVER appear in Result — not in Body,
// not in the audit. Result carries the fetched data and the decision trail, never
// the secret. (govfetch never copies the injected credential into Result; Body is
// the upstream's own response bytes.)
//
// # Default-deny, fail-closed
//
// The URL host MUST be on Spec.EgressAllow, or the fetch is refused before any
// dial (a strict allowlist — guarded mode's "allow public" is deliberately NOT
// used here). A swap is https-only: govfetch refuses to send a real credential
// over plaintext. Any host-internal fault (unreadable/invalid swap config,
// unparseable URL) returns a non-nil error and no data — never a fallback to an
// unmediated fetch (ASK tenets 3, 4).
package govfetch

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
)

// defaultMaxBytes bounds the response body when Spec.MaxBytes is 0 (ASK tenet 8 —
// operations are bounded). A caller expecting larger data sets MaxBytes explicitly.
const defaultMaxBytes = 64 << 20 // 64 MiB

// upstreamRootsForTest overrides the roots used to verify the upstream
// certificate. It is nil in production (system roots) and exists only so tests
// can target a loopback TLS server; it is never part of the public contract and
// is never used to disable verification.
var upstreamRootsForTest *x509.CertPool

// Spec describes one governed fetch. EgressAllow and SwapConfigPath map 1:1 onto a
// microagency source config: the allowlist is the source's, and the swap config is
// what the credential broker materializes per run.
type Spec struct {
	// URL is the request URL. Its host MUST be on EgressAllow or the fetch is denied.
	URL string
	// Method defaults to GET when empty.
	Method string
	// Headers are request headers. A swapped credential overrides any same-named
	// header host-side; the caller never supplies the real secret here.
	Headers map[string]string
	// Body is the request body (e.g. for POST).
	Body []byte
	// EgressAllow is the default-deny destination allowlist. An empty list denies
	// everything (fail-closed).
	EgressAllow []string
	// SwapConfigPath points at a credential-swap YAML config. The real secret it
	// references is resolved host-side and injected into the upstream request; it is
	// NEVER returned in Result. Empty means no credential injection.
	SwapConfigPath string
	// MaxBytes caps the response body (ASK tenet 8). 0 uses defaultMaxBytes.
	MaxBytes int64
	// Timeout bounds the whole fetch. 0 relies on the internal client timeout (~30s).
	Timeout time.Duration
}

// AuditEvent is one egress decision (allow / deny / swap / close / cap) the brain
// recorded during the fetch. Fields never contain secret material. It is
// govfetch's public projection of the brain's internal audit records.
type AuditEvent struct {
	Event  string
	Fields map[string]any
}

// Result is the outcome of a governed fetch. Status mirrors HTTP: 200 on success;
// 403 for a policy denial (no upstream was dialed — distinguishable from an
// upstream 403 by the presence of an egress_deny/egress_internal_deny event in
// Audit and an empty Body); 502 for an upstream or credential failure. Body is the
// upstream response DATA — never the injected credential. Audit is the complete
// decision trail.
type Result struct {
	Status int
	Body   []byte
	Audit  []AuditEvent
}

// Fetch performs one host-side governed fetch through the shared egress brain. The
// returned error is non-nil only for a host-internal fault the caller should log
// (bad config, unparseable URL); a policy denial or an upstream failure is carried
// in Result (Status + Audit), not as an error — and always fail-closed (no data).
func Fetch(ctx context.Context, spec Spec) (Result, error) {
	policy, err := egress.NewPolicy(spec.EgressAllow)
	if err != nil {
		return Result{}, fmt.Errorf("govfetch: allowlist: %w", err)
	}

	var swaps *egress.SwapTable
	if spec.SwapConfigPath != "" {
		data, rerr := os.ReadFile(spec.SwapConfigPath)
		if rerr != nil {
			return Result{}, fmt.Errorf("govfetch: read swap config: %w", rerr)
		}
		swaps, rerr = egress.LoadSwapTable(data)
		if rerr != nil {
			return Result{}, fmt.Errorf("govfetch: swap config: %w", rerr)
		}
	}

	maxBytes := spec.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	logger := &egress.BufferLogger{}
	// strict mode: the URL host must be on the allowlist (default-deny). guarded
	// mode would permit public hosts that are NOT on the list — exactly what this
	// seam must not do, since the allowlist is the source's egress boundary.
	brain := egress.NewBrain("strict", policy, swaps, logger, egress.Limits{MaxTotalBytes: maxBytes})
	brain.UpstreamRoots = upstreamRootsForTest // nil in production (system roots)

	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	resp, ferr := brain.Fetch(ctx, egress.FetchRequest{
		Method: spec.Method,
		URL:    spec.URL,
		Header: spec.Headers,
		Body:   spec.Body,
	})
	result := Result{Status: resp.Status, Body: resp.Body, Audit: toAudit(logger.Snapshot())}
	if ferr != nil {
		return result, fmt.Errorf("govfetch: %w", ferr)
	}
	return result, nil
}

// toAudit projects the brain's internal audit records (event name + fields in one
// map) into the public AuditEvent type. The brain never records secret material,
// so the projected Fields are safe to surface to the caller.
func toAudit(events []map[string]any) []AuditEvent {
	out := make([]AuditEvent, 0, len(events))
	for _, e := range events {
		name, _ := e["event"].(string)
		fields := make(map[string]any, len(e))
		for k, v := range e {
			if k == "event" {
				continue
			}
			fields[k] = v
		}
		out = append(out, AuditEvent{Event: name, Fields: fields})
	}
	return out
}
