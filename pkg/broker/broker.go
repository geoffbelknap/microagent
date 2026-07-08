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
	// Tap PRE-SWAP: r.Header still holds the workload's own values.
	if t.OnTap != nil {
		t.OnTap(TapRecord{
			Mode: "terminate", Method: r.Method, Host: t.Upstream.Host,
			Path: r.URL.Path, Headers: r.Header.Clone(), At: time.Now(),
		})
	}

	out := *t.Upstream
	out.Path = singleJoiningSlash(t.Upstream.Path, r.URL.Path)
	out.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, out.String(), r.Body)
	if err != nil {
		http.Error(w, "broker: build upstream request", http.StatusBadGateway)
		return
	}

	// Copy headers, swapping references. Fail closed before any bytes leave.
	for k, vals := range r.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vals {
			swapped, err := t.swap(v)
			if err != nil {
				http.Error(w, "broker: unresolved secret reference", http.StatusBadGateway)
				return
			}
			req.Header.Add(k, swapped)
		}
	}

	resp, err := t.client().Do(req)
	if err != nil {
		http.Error(w, "broker: upstream: "+err.Error(), http.StatusBadGateway)
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
	_, _ = io.Copy(w, resp.Body)
}

// swap replaces every @secret:<name> token in a header value with the resolved
// live secret. An unknown name fails closed (returns an error), so the broker
// never forwards an unresolved or empty credential.
func (t *Terminate) swap(value string) (string, error) {
	if !strings.Contains(value, RefPrefix) {
		return value, nil
	}
	var b strings.Builder
	rest := value
	for {
		i := strings.Index(rest, RefPrefix)
		if i < 0 {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(rest[:i])
		nameStart := i + len(RefPrefix)
		j := nameStart
		for j < len(rest) && isRefNameChar(rest[j]) {
			j++
		}
		name := rest[nameStart:j]
		if name == "" {
			return "", fmt.Errorf("empty secret reference")
		}
		secret, ok := t.Resolve(name)
		if !ok {
			return "", fmt.Errorf("unknown secret reference %q", name)
		}
		b.WriteString(secret)
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
	if c.OnTap != nil {
		c.OnTap(TapRecord{Mode: "connect", Method: r.Method, Host: r.Host, At: time.Now()})
	}
	upstream, err := c.dial("tcp", r.Host)
	if err != nil {
		http.Error(w, "broker: connect upstream", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "broker: hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
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
