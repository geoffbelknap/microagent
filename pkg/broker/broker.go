// Package broker is the per-workspace egress broker: a cooperative forward
// proxy the workload is pointed at (via HTTPS_PROXY and per-endpoint base
// URLs) instead of a transparent MITM. It does two jobs without forging any
// certificate or injecting a CA into the guest:
//
//   - Credential isolation. The workload never holds a live secret; its
//     request carries only a reference (@secret:<name>). The broker swaps the
//     reference for the live secret in terminate mode, just before it
//     originates the upstream TLS connection itself.
//   - Observability. Every request is tapped PRE-SWAP — the header values are
//     exactly as the workload sent them, so the reference appears verbatim and
//     the live secret never does. The tap is the substrate for the decision +
//     minimized-metadata stream; raw content is a separate, governed concern.
//
// Enforcement (deny-by-default egress, DNS-controlled resolution) and the
// per-workspace lifecycle are layered on top of this datapath.
package broker

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RefPrefix marks a credential reference inside a header value. The workload
// holds only the reference; the broker swaps it for the live secret.
const RefPrefix = "@secret:"

// SecretResolver returns the live secret for a reference name, or ok=false if
// the name is unknown — in which case the broker fails the request closed
// rather than forward an unresolved (or empty) credential.
type SecretResolver func(name string) (secret string, ok bool)

// TapRecord is emitted once per request, captured PRE-SWAP. Headers are the
// workload's own, before any reference is substituted, so a live secret can
// never appear here by construction.
type TapRecord struct {
	Mode    string      // "terminate" | "connect"
	Method  string      // request method (CONNECT for tunnels)
	Host    string      // upstream host
	Path    string      // request path (terminate only)
	Headers http.Header // pre-swap, cloned; references, never live secrets
	At      time.Time
}

// Tap receives a TapRecord per request. It must treat the record as read-only.
type Tap func(TapRecord)

// hopByHop headers are connection-scoped and must not be forwarded upstream.
var hopByHop = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true,
	"Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// Terminate is the base-URL datapath: the workload speaks to the broker as its
// endpoint in cleartext (a trusted local hop); the broker swaps credential
// references, then originates the upstream request over its own TLS client.
// Because the broker is the TLS client, it sees plaintext without forging
// anything — it is not a man-in-the-middle.
type Terminate struct {
	Upstream *url.URL
	Resolve  SecretResolver
	OnTap    Tap
	// OnDecision receives the per-request decision record — the minimized
	// default emission (verdict + metadata, no content).
	OnDecision OnDecision
	// Policy, when set, judges each pre-swap request before any bytes go
	// upstream. Fail-closed: an error or panic denies.
	Policy Policy
	// OnCapture, when set, receives the governed raw capture of each request
	// (pre-swap; request-only). Nil means capture is off — the default.
	OnCapture OnCapture
	// CaptureBodyLimit bounds the captured body prefix; 0 means
	// DefaultCaptureBodyLimit.
	CaptureBodyLimit int64
	// Client originates the upstream request. Defaults to http.DefaultClient
	// (its own TLS). Injected in tests to point at a mock upstream.
	Client *http.Client
}

// NewTerminate builds a terminate-mode handler forwarding to upstream (e.g.
// "https://api.anthropic.com").
func NewTerminate(upstream string, resolve SecretResolver, tap Tap) (*Terminate, error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("broker: invalid upstream %q", upstream)
	}
	return &Terminate{Upstream: u, Resolve: resolve, OnTap: tap}, nil
}

func (t *Terminate) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

func (t *Terminate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	// Tap PRE-SWAP: r.Header still holds the workload's own values.
	tap := TapRecord{
		Mode: "terminate", Method: r.Method, Host: t.Upstream.Host,
		Path: r.URL.Path, Headers: r.Header.Clone(), At: time.Now(),
	}
	if t.OnTap != nil {
		t.OnTap(tap)
	}
	var labels []string
	deny := func(rule string, signals ...string) {
		if t.OnDecision == nil {
			return
		}
		t.OnDecision(DecisionRecord{
			Event: EventRequestDeny, TS: time.Now(), Mode: "terminate",
			Host: t.Upstream.Host, Method: r.Method, Verdict: "deny",
			Rule: rule, Signals: signals, Labels: labels,
			DurationMs: time.Since(start).Milliseconds(),
		})
	}

	// The policy sees the pre-swap request inside the boundary; only its
	// verdict has any effect outside.
	verdict := evaluate(t.Policy, tap)
	labels = verdict.Labels
	if !verdict.Allow {
		http.Error(w, "broker: denied by policy", http.StatusForbidden)
		deny(verdict.Rule)
		return
	}

	out := *t.Upstream
	out.Path = singleJoiningSlash(t.Upstream.Path, r.URL.Path)
	out.RawQuery = r.URL.RawQuery
	body := &countingReader{r: r.Body}
	var upstreamBody io.Reader = body
	if t.OnCapture != nil {
		limit := t.CaptureBodyLimit
		if limit <= 0 {
			limit = DefaultCaptureBodyLimit
		}
		capBuf := &captureBuffer{limit: limit}
		upstreamBody = io.TeeReader(body, capBuf)
		// Emit on every exit path, with however much body was read by then.
		defer func() {
			t.OnCapture(CaptureRecord{
				TS: time.Now(), Mode: "terminate", Host: t.Upstream.Host,
				Method: r.Method, Path: r.URL.Path, Headers: tap.Headers,
				Body: capBuf.buf.Bytes(), Truncated: capBuf.truncated,
			})
		}()
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, out.String(), upstreamBody)
	if err != nil {
		http.Error(w, "broker: build upstream request", http.StatusBadGateway)
		deny("bad-request")
		return
	}

	// Copy headers, swapping references. Fail closed before any bytes leave.
	var refs []string
	seenRef := map[string]bool{}
	for k, vals := range r.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vals {
			swapped, names, err := t.swap(v)
			if err != nil {
				http.Error(w, "broker: unresolved secret reference", http.StatusBadGateway)
				deny("unresolved-secret-ref", "unresolved-secret-ref")
				return
			}
			for _, n := range names {
				if !seenRef[n] {
					seenRef[n] = true
					refs = append(refs, n)
				}
			}
			req.Header.Add(k, swapped)
		}
	}

	resp, err := t.client().Do(req)
	if err != nil {
		http.Error(w, "broker: upstream: "+err.Error(), http.StatusBadGateway)
		deny("upstream-error")
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	respBytes, _ := streamCopy(w, resp.Body)
	if t.OnDecision != nil {
		t.OnDecision(DecisionRecord{
			Event: EventRequestAllow, TS: time.Now(), Mode: "terminate",
			Host: t.Upstream.Host, Method: r.Method, Verdict: "allow",
			Rule: verdict.Rule, Labels: labels,
			Status: resp.StatusCode, BytesOut: body.n, BytesIn: respBytes,
			DurationMs: time.Since(start).Milliseconds(), SecretRefs: refs,
		})
	}
}

// streamCopy relays src to w, flushing after each chunk so a streaming
// upstream response (e.g. text/event-stream) reaches the client promptly
// instead of buffering. Falls back to a plain copy when w is not a Flusher.
func streamCopy(w http.ResponseWriter, src io.Reader) (int64, error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			total += int64(wn)
			if flusher != nil {
				flusher.Flush()
			}
			if werr != nil {
				return total, werr
			}
			if wn != n {
				return total, io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}

// swap replaces every @secret:<name> token in a header value with the resolved
// live secret and returns the reference names it substituted (for the decision
// record — names only, never values). An unknown name fails closed (returns an
// error), so the broker never forwards an unresolved or empty credential.
func (t *Terminate) swap(value string) (string, []string, error) {
	if !strings.Contains(value, RefPrefix) {
		return value, nil, nil
	}
	var b strings.Builder
	var names []string
	rest := value
	for {
		i := strings.Index(rest, RefPrefix)
		if i < 0 {
			b.WriteString(rest)
			return b.String(), names, nil
		}
		b.WriteString(rest[:i])
		nameStart := i + len(RefPrefix)
		j := nameStart
		for j < len(rest) && isRefNameChar(rest[j]) {
			j++
		}
		name := rest[nameStart:j]
		if name == "" {
			return "", nil, fmt.Errorf("empty secret reference")
		}
		secret, ok := t.Resolve(name)
		if !ok {
			return "", nil, fmt.Errorf("unknown secret reference %q", name)
		}
		b.WriteString(secret)
		names = append(names, name)
		rest = rest[j:]
	}
}

func isRefNameChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.' || c == '/':
		return true
	}
	return false
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash && b != "":
		return a + "/" + b
	}
	return a + b
}

// Connect is the HTTPS_PROXY datapath: the workload sends CONNECT host:port and
// the broker tunnels opaquely. It sees the destination (for enforcement and the
// metadata stream) but never the tunnelled contents — which is exactly the
// honest limit: an agent doing its own end-to-end TLS through the tunnel keeps
// its plaintext, and gets no credential injection.
type Connect struct {
	OnTap Tap
	// OnDecision receives one record per tunnel, at tunnel close — byte
	// counts and timing for an opaque stream, never its contents.
	OnDecision OnDecision
	// Policy, when set, judges each tunnel before it dials. Fail-closed.
	Policy Policy
	// Dial opens the upstream connection; an enforcement-aware dialer
	// (deny-by-default, allowlist) can be injected here. Defaults to a plain
	// TCP dialer.
	Dial func(network, addr string) (net.Conn, error)
}

func (c *Connect) dial(network, addr string) (net.Conn, error) {
	if c.Dial != nil {
		return c.Dial(network, addr)
	}
	return net.DialTimeout(network, addr, 10*time.Second)
}

func (c *Connect) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	tap := TapRecord{Mode: "connect", Method: r.Method, Host: r.Host, At: time.Now()}
	if c.OnTap != nil {
		c.OnTap(tap)
	}
	emit := func(rec DecisionRecord) {
		if c.OnDecision == nil {
			return
		}
		rec.TS = time.Now()
		rec.Mode = "connect"
		rec.Host = r.Host
		rec.Method = r.Method
		rec.DurationMs = time.Since(start).Milliseconds()
		c.OnDecision(rec)
	}
	verdict := evaluate(c.Policy, tap)
	if !verdict.Allow {
		http.Error(w, "broker: denied by policy", http.StatusForbidden)
		emit(DecisionRecord{Event: EventRequestDeny, Verdict: "deny", Rule: verdict.Rule, Labels: verdict.Labels, Signals: []string{SignalDenied}})
		return
	}
	upstream, err := c.dial("tcp", r.Host)
	if err != nil {
		// A dialer refusal (ErrTunnelDenied) is a fail-closed governance denial
		// of an inside/off-allowlist destination — 403 with the denied signal so
		// it is not confused with a transient upstream failure. Any other dial
		// error is an ordinary upstream problem.
		if errors.Is(err, ErrTunnelDenied) {
			http.Error(w, "broker: denied", http.StatusForbidden)
			emit(DecisionRecord{Event: EventRequestDeny, Verdict: "deny", Rule: "denied", Signals: []string{SignalDenied}})
			return
		}
		http.Error(w, "broker: connect upstream", http.StatusBadGateway)
		emit(DecisionRecord{Event: EventRequestDeny, Verdict: "deny", Rule: "upstream-error"})
		return
	}
	defer func() { _ = upstream.Close() }()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "broker: hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	// Splice with counted copies; when one direction ends, close both sides,
	// wait for the other to drain, then emit the tunnel's decision record with
	// final byte totals.
	var toUpstream, toClient int64
	done := make(chan struct{}, 2)
	go func() { toUpstream, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { toClient, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	_ = upstream.Close()
	_ = client.Close()
	<-done
	emit(DecisionRecord{
		Event: EventRequestAllow, Verdict: "allow",
		Rule: verdict.Rule, Labels: verdict.Labels,
		BytesOut: toUpstream, BytesIn: toClient,
	})
}

// Handler routes CONNECT tunnels to conn and everything else to term, so one
// listener can serve both HTTPS_PROXY and base-URL workloads. Either may be nil
// if only one mode is configured.
func Handler(term *Terminate, conn *Connect) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			if conn == nil {
				http.Error(w, "broker: CONNECT not enabled", http.StatusMethodNotAllowed)
				return
			}
			conn.ServeHTTP(w, r)
			return
		}
		if term == nil {
			http.Error(w, "broker: terminate not enabled", http.StatusMethodNotAllowed)
			return
		}
		term.ServeHTTP(w, r)
	})
}
