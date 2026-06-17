package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultTokenTTL is the assumed lifetime of an acquired token when the token
// endpoint omits expires_in and the entry sets no token_ttl_seconds. One hour
// is the OAuth2 convention; the cache's skew window still forces a refresh
// before it lapses.
const defaultTokenTTL = 3600

// tokenResp is the standard OAuth2 token-endpoint success body. Only the fields
// the swapper needs are decoded; unknown fields are ignored.
type tokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// httpClient returns the Swapper's injected client, or a fresh client with a
// 10s timeout so a stuck token endpoint cannot wedge a request indefinitely.
func (sw *Swapper) httpClient() *http.Client {
	if sw.HTTP != nil {
		return sw.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// cacheKey derives the token-cache key for entry e. It folds in every input
// that changes which token is minted (endpoint, scopes, client id, signing key)
// so a config change can never serve a stale token acquired under different
// parameters.
func cacheKey(e SwapEntry) string {
	return e.Name + "|" + e.TokenURL + "|" + strings.Join(e.Scopes, ",") + "|" + e.ClientIDRef + "|" + e.SigningKeyRef
}

// acquireOAuth2CC performs an OAuth2 client-credentials exchange for entry e and
// returns the bearer access token. It serves a cached token when one is still
// valid (single fetch until near expiry); otherwise it resolves the client id
// and secret (fail-closed on any resolve error), POSTs the token request, and
// caches the result.
//
// Security: the client id/secret and the resulting token are never logged or
// returned in an error. Any resolve/HTTP/parse failure returns an error and no
// token, so the caller fails the request closed rather than reaching upstream
// unauthenticated.
func (sw *Swapper) acquireOAuth2CC(ctx context.Context, e SwapEntry) (string, error) {
	key := cacheKey(e)
	if tok, ok := sw.Cache.get(key); ok {
		return tok, nil
	}

	clientID, err := sw.resolveStr(ctx, e.ClientIDRef)
	if err != nil {
		return "", err
	}
	clientSecret, err := sw.resolveStr(ctx, e.ClientSecretRef)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	if len(e.Scopes) > 0 {
		form.Set("scope", strings.Join(e.Scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("egress: swap %q: build token request: %w", e.Name, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := sw.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("egress: swap %q: token request: %w", e.Name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("egress: swap %q: read token response: %w", e.Name, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Status only — the body may echo back credentials, so it is not logged
		// or surfaced.
		return "", fmt.Errorf("egress: swap %q: token endpoint status %d", e.Name, resp.StatusCode)
	}

	tok, expiry, err := parseToken(body, e)
	if err != nil {
		return "", err
	}
	sw.Cache.set(key, tok, expiry)
	return tok, nil
}

// parseToken extracts the access token and its absolute expiry from a token
// response body. When e.TokenResponseField is empty or "access_token" it
// decodes the standard tokenResp; otherwise it pulls the operator-named string
// field and a numeric expires_in from a generic object. An empty token is an
// error so a blank credential is never injected.
func parseToken(body []byte, e SwapEntry) (string, time.Time, error) {
	if e.TokenResponseField == "" || e.TokenResponseField == "access_token" {
		var tr tokenResp
		if err := json.Unmarshal(body, &tr); err != nil {
			return "", time.Time{}, fmt.Errorf("egress: swap %q: decode token response: %w", e.Name, err)
		}
		if tr.AccessToken == "" {
			return "", time.Time{}, fmt.Errorf("egress: swap %q: token response missing access_token", e.Name)
		}
		return tr.AccessToken, expiryFrom(tr.ExpiresIn, e), nil
	}

	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		return "", time.Time{}, fmt.Errorf("egress: swap %q: decode token response: %w", e.Name, err)
	}
	tok, _ := generic[e.TokenResponseField].(string)
	if tok == "" {
		return "", time.Time{}, fmt.Errorf("egress: swap %q: token response field %q missing or empty", e.Name, e.TokenResponseField)
	}
	expiresIn := 0
	if v, ok := generic["expires_in"].(float64); ok {
		expiresIn = int(v)
	}
	return tok, expiryFrom(expiresIn, e), nil
}

// expiryFrom computes a token's absolute expiry. It prefers the endpoint's
// expires_in, falls back to the entry's token_ttl_seconds, then to
// defaultTokenTTL, so a cached token always has a bounded lifetime.
func expiryFrom(expiresIn int, e SwapEntry) time.Time {
	ttl := expiresIn
	if ttl <= 0 {
		ttl = e.TokenTTLSeconds
	}
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	return time.Now().Add(time.Duration(ttl) * time.Second)
}
