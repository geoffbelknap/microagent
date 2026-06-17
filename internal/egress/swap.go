package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// errNoSecret is returned when a referenced secret resolves to an empty value.
// Acquisition treats it as fatal so a swap never injects a blank credential:
// the request is failed closed rather than reaching upstream unauthenticated.
var errNoSecret = errors.New("egress: secret resolved empty")

// resolver resolves a secret reference (e.g. "env:EXAMPLE_KEY") to its raw
// bytes. Phase 3 wires the real KeyResolver; tests inject a fake. A nil
// Resolver on a Swapper is a wiring gap and acquisition fails closed rather
// than dereferencing it.
type resolver interface {
	Resolve(ctx context.Context, ref string) ([]byte, error)
}

// Swapper acquires the real credential for a swap entry and renders the header
// value to inject. Resolver dereferences secret refs; Cache holds acquired
// tokens for the (later-phase) expiring strategies. The static strategy does
// not use Cache. A Swapper with a nil Resolver fails closed on acquire.
type Swapper struct {
	Resolver resolver
	Cache    *tokenCache
}

// acquire resolves and renders the credential header for entry e, returning the
// header name and value to set on the outbound request. It switches on e.Type
// so the set of strategies is total; an unknown type is rejected. The header
// defaults to "Authorization" when e.Header is empty.
//
// Security: acquire never logs and never returns secret material in its error;
// callers (injectRequests) audit only host/swap/type/error. On any error the
// caller fails the request closed — the credential never reaches upstream and
// the unauthenticated request never does either.
func (sw *Swapper) acquire(ctx context.Context, e SwapEntry) (header, value string, err error) {
	switch e.Type {
	case "static":
		key, err := sw.resolveStr(ctx, e.KeyRef)
		if err != nil {
			return "", "", err
		}
		return headerOrDefault(e.Header), render(e.Format, "key", key, key), nil
	case "oauth2-cc":
		return sw.acquireOAuth2CC(ctx, e)
	case "jwt-bearer":
		return sw.acquireJWTBearer(ctx, e)
	default:
		return "", "", fmt.Errorf("egress: swap %q: unknown type %q", e.Name, e.Type)
	}
}

// acquireOAuth2CC is a Phase 4 stub: it always errors so acquire's switch is
// total without yet performing an OAuth2 client-credentials exchange.
func (sw *Swapper) acquireOAuth2CC(_ context.Context, _ SwapEntry) (string, string, error) {
	return "", "", errors.New("not implemented")
}

// acquireJWTBearer is a Phase 5 stub: it always errors so acquire's switch is
// total without yet minting a signed JWT bearer assertion.
func (sw *Swapper) acquireJWTBearer(_ context.Context, _ SwapEntry) (string, string, error) {
	return "", "", errors.New("not implemented")
}

// resolveStr resolves ref to a non-empty string secret. An empty ref, a nil
// resolver, a resolver error, or an empty resolved value all fail closed —
// callers must never inject a blank credential.
func (sw *Swapper) resolveStr(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("egress: empty secret ref")
	}
	if sw.Resolver == nil {
		return "", errors.New("egress: no secret resolver configured")
	}
	b, err := sw.Resolver.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errNoSecret
	}
	return string(b), nil
}

// render substitutes "{placeholder}" in format with v. When format is empty it
// returns dflt unchanged — so a "static" entry with no format injects the raw
// key as the header value.
func render(format, placeholder, v, dflt string) string {
	if format == "" {
		return dflt
	}
	return strings.ReplaceAll(format, "{"+placeholder+"}", v)
}

// headerOrDefault returns h, or "Authorization" when h is empty.
func headerOrDefault(h string) string {
	if h == "" {
		return "Authorization"
	}
	return h
}

// injectRequests parses successive HTTP/1.x requests from guest, and for each
// request whose host matches a swap entry, replaces the credential header with
// the acquired real credential before writing the request to up. Requests to
// non-matching hosts are forwarded unchanged. It runs for the life of the
// connection and returns the first read/write error (io.EOF on a clean guest
// close).
//
// Fail-closed: if acquiring the credential errors, the request is NOT written
// upstream and injectRequests returns the error, tearing down the connection so
// the guest cannot reach upstream unauthenticated. Audit records only
// host/swap-name/type/error — never the credential value or resolved secret.
func injectRequests(guest io.Reader, up io.Writer, sni string, sw *Swapper, tbl *SwapTable, log Logger) error {
	br := bufio.NewReader(guest)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return err
		}
		host := req.Host
		if host == "" {
			host = sni
		}
		if e, ok := tbl.Match(host); ok {
			hdr, val, aerr := sw.acquire(req.Context(), e)
			if aerr != nil {
				log.Log("egress_swap_error", map[string]any{"host": host, "swap": e.Name, "type": e.Type, "error": aerr.Error()})
				return aerr // fail closed: request never reaches upstream
			}
			req.Header.Set(hdr, val)
			log.Log("egress_swap", map[string]any{"host": host, "swap": e.Name, "type": e.Type})
		}
		req.RequestURI = "" // Request.Write rejects a set RequestURI (origin-form)
		if err := req.Write(up); err != nil {
			return err
		}
	}
}
