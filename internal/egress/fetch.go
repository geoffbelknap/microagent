package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// fetchClientTimeout bounds one governed round-trip when the caller's ctx has no
// earlier deadline. A oneshot wasm fetch is not a long-lived stream.
const fetchClientTimeout = 30 * time.Second

// FetchRequest is a structured HTTP request a non-byte-stream transport (the wasm
// sandbox) hands the brain to perform on its behalf. The host owns the HTTP
// client, so the brain governs the request directly — allowlist + cred-blind
// swap + audit — with no MITM (there is no guest-owned TLS to terminate) and no
// netns/TPROXY.
type FetchRequest struct {
	Method string
	URL    string
	Header map[string]string
	Body   []byte
}

// FetchResponse is always guest-deliverable: a governance denial or a host-side
// failure is returned as a structured response (Denied/Error set, no upstream
// contacted on denial), never as a silent success. Status mirrors HTTP (403 for
// a policy denial, 502 for an upstream/credential failure).
type FetchResponse struct {
	Status int
	Header map[string]string
	Body   []byte
	Denied bool   // true when policy denied the destination (no upstream dial)
	Reason string // denial/failure reason (safe for the guest; never a secret)
}

// Fetch performs ONE governed HTTP round-trip for a structured caller. It is the
// brain's high-level egress capability, sharing the SAME decision (Evaluate),
// credential acquisition (SwapFor → Swapper), and audit the byte-stream mediator
// uses — so a wasm sandbox's network is governed identically to a microVM's:
//
//   - the destination host is resolved to an IP, evaluated under the allowlist +
//     guarded inside-deny, and a denial is audited and returned fail-closed with
//     NO upstream dial;
//   - the actual TCP connection is PINNED to the evaluated IP (the Transport dials
//     that IP while TLS verifies the real hostname's certificate), so a name that
//     re-resolves to an inside address between the check and the dial — DNS
//     rebinding — cannot bypass the decision;
//   - a matching credential swap injects the REAL secret host-side (cred-blind):
//     the secret is resolved here, never placed in FetchRequest, never seen by the
//     guest; injection is refused over plaintext http so a real credential is
//     never sent in the clear;
//   - the response body is bounded by Limits.MaxTotalBytes.
//
// A non-nil error is a host-internal fault (e.g. an unparseable URL) for the
// caller to log; the returned FetchResponse is still safe to hand the guest.
func (b *Brain) Fetch(ctx context.Context, req FetchRequest) (FetchResponse, error) {
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return FetchResponse{Status: 400, Reason: "invalid url"}, fmt.Errorf("egress: fetch: invalid url %q", req.URL)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	// Resolve to a concrete connect IP. The IP — not the guest-supplied name — is
	// what guarded classifies and what the dial is pinned to, which is what defeats
	// rebinding. A resolution failure is fail-closed.
	connectIP, err := b.resolveConnectIP(ctx, host)
	if err != nil {
		b.Logger.Log("egress_fetch_error", map[string]any{"host": host, "stage": "resolve", "error": err.Error()})
		return FetchResponse{Status: 502, Reason: "resolve failed"}, nil
	}
	dst := netip.AddrPortFrom(connectIP, portU16(port))

	v := b.Evaluate(host, nil, connectIP, false)
	if !v.Allowed {
		b.AuditDeny(v, map[string]any{"host": host, "dst": dst.String(), "shape": "fetch"})
		reason := v.Reason
		if v.Inside {
			reason = "guarded: internal destination denied"
		}
		return FetchResponse{Status: 403, Denied: true, Reason: reason}, nil
	}

	// Credential swap is cred-blind and https-only: refuse to inject a real
	// credential over plaintext http (it would leak on the wire). The secret is
	// resolved host-side and never returned to the guest.
	header := map[string]string{}
	for k, val := range req.Header {
		header[k] = val
	}
	if matched, mErr := b.applySwap(ctx, u.Scheme, host, header); mErr != nil {
		// applySwap already audited egress_swap_error; fail closed — never issue.
		return FetchResponse{Status: 502, Reason: "credential unavailable"}, nil
	} else if matched && u.Scheme != "https" {
		b.Logger.Log("egress_swap_error", map[string]any{"host": host, "error": "refusing to send swapped credential over plaintext http"})
		return FetchResponse{Status: 403, Denied: true, Reason: "plaintext credential refused"}, nil
	}

	allowFields := map[string]any{"host": host, "dst": dst.String(), "shape": "fetch"}
	if v.Unlisted {
		allowFields["unlisted"] = true
	}
	b.Logger.Log("egress_allow", allowFields)

	resp, n, ferr := b.issue(ctx, req, u, header, dst)
	if ferr != nil {
		b.Logger.Log("egress_fetch_error", map[string]any{"host": host, "dst": dst.String(), "error": ferr.Error()})
		return FetchResponse{Status: 502, Reason: "upstream error"}, nil
	}
	if b.Limits.MaxTotalBytes > 0 && n > b.Limits.MaxTotalBytes {
		b.Logger.Log("egress_cap_exceeded", map[string]any{
			"host": host, "dst": dst.String(), "proto": "tcp", "reason": "volume", "limit": b.Limits.MaxTotalBytes,
		})
		return FetchResponse{Status: 502, Reason: "response too large"}, nil
	}
	b.Logger.Log("egress_close", map[string]any{"host": host, "dst": dst.String(), "shape": "fetch", "bytes": n})
	return resp, nil
}

// resolveConnectIP resolves host to a single connect IP, accepting an IP literal
// directly. The first resolved address is used; the point is to pin SOME concrete
// evaluated IP for both the guarded decision and the dial.
func (b *Brain) resolveConnectIP(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("egress: fetch: no addresses for %q", host)
	}
	return ips[0].Unmap(), nil
}

// applySwap injects the swapped credential header for host when a swap entry
// matches, resolving the real secret host-side. It returns whether a swap matched
// so the caller can enforce the https-only rule. Injection itself only happens
// for https (the caller refuses a plaintext match before issuing).
func (b *Brain) applySwap(ctx context.Context, scheme, host string, header map[string]string) (matched bool, err error) {
	hdr, val, matched, err := b.SwapFor(ctx, host)
	if err != nil {
		return matched, err
	}
	if matched && scheme == "https" {
		header[hdr] = val
	}
	return matched, nil
}

// issue performs the actual upstream request with the connection PINNED to dst
// (the evaluated IP), TLS verifying the real hostname's certificate. It returns
// the structured response and the number of body bytes read.
func (b *Brain) issue(ctx context.Context, req FetchRequest, u *url.URL, header map[string]string, dst netip.AddrPort) (FetchResponse, int64, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if len(req.Body) > 0 {
		body = strings.NewReader(string(req.Body))
	}
	hreq, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return FetchResponse{}, 0, err
	}
	for k, val := range header {
		hreq.Header.Set(k, val)
	}

	pinned := dst.String()
	transport := &http.Transport{
		// Pin the dial to the evaluated IP regardless of the address net/http would
		// otherwise resolve, so the bytes go exactly where the decision was made.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, pinned)
		},
		TLSClientConfig:     &tls.Config{ServerName: u.Hostname(), RootCAs: b.UpstreamRoots},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: fetchClientTimeout,
	}
	client := &http.Client{Transport: transport, Timeout: fetchClientTimeout}

	hresp, err := client.Do(hreq)
	if err != nil {
		return FetchResponse{}, 0, err
	}
	defer func() { _ = hresp.Body.Close() }()

	limit := b.Limits.MaxTotalBytes
	var r io.Reader = hresp.Body
	if limit > 0 {
		// Read one extra byte so the caller can detect an over-cap body.
		r = io.LimitReader(hresp.Body, limit+1)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return FetchResponse{}, int64(len(data)), err
	}
	respHeader := map[string]string{}
	for k := range hresp.Header {
		respHeader[k] = hresp.Header.Get(k)
	}
	return FetchResponse{Status: hresp.StatusCode, Header: respHeader, Body: data}, int64(len(data)), nil
}

// portU16 parses a numeric port string into a uint16, returning 0 on failure
// (the dst is still legible in audit; the dial would fail downstream).
func portU16(p string) uint16 {
	var n uint32
	for _, c := range p {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + uint32(c-'0')
		if n > 65535 {
			return 0
		}
	}
	return uint16(n)
}
