package egress

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

// jwtBearerTTL is the default lifetime, in seconds, of a signed JWT assertion
// when the entry sets no token_ttl_seconds. Short by convention for a bearer
// assertion; the cache's skew window still forces a re-sign before it lapses.
const jwtBearerTTL = 600

// jwtIATBackdate backdates the assertion's iat by a small margin so minor clock
// skew between this host and the verifier cannot reject a freshly minted token
// as "not yet valid".
const jwtIATBackdate = 60 * time.Second

// acquireJWTBearer mints an RS256-signed JWT assertion for entry e and returns
// it as the bearer credential. It serves a cached assertion while one is still
// valid; otherwise it resolves the signing key (fail-closed on any resolve
// error), builds and signs the claims, caches the result, and returns it.
//
// v1 injects the signed assertion directly as the bearer credential; exchanging
// it at a token endpoint (RFC 7523 jwt-bearer grant) is a documented follow-up
// and not in scope here.
//
// Security: the private key and the signed assertion are never logged or
// returned in an error. Any resolve/parse/sign failure returns an error and no
// token, so the caller fails the request closed rather than reaching upstream
// unauthenticated.
func (sw *Swapper) acquireJWTBearer(ctx context.Context, e SwapEntry) (string, error) {
	key := cacheKey(e)
	if tok, ok := sw.Cache.get(key); ok {
		return tok, nil
	}

	keyPEM, err := sw.resolveStr(ctx, e.SigningKeyRef)
	if err != nil {
		return "", err
	}
	pk, err := parseRSAPrivateKey([]byte(keyPEM))
	if err != nil {
		// The error is intentionally generic: a parse error must never echo
		// back any portion of the key material.
		return "", fmt.Errorf("egress: swap %q: parse signing key: %w", e.Name, err)
	}

	ttl := e.TokenTTLSeconds
	if ttl <= 0 {
		ttl = jwtBearerTTL
	}
	now := time.Now()
	expiry := now.Add(time.Duration(ttl) * time.Second)

	claims := map[string]any{
		"iat": now.Add(-jwtIATBackdate).Unix(),
		"exp": expiry.Unix(),
	}
	// Operator claims are merged last so iss/aud/sub/etc. are set, but they
	// cannot silently widen the lifetime past the bounded exp computed above.
	for k, v := range e.Claims {
		if k == "exp" {
			continue
		}
		claims[k] = interpolateEnv(v)
	}

	jwt, err := signRS256JWT(pk, claims)
	if err != nil {
		return "", fmt.Errorf("egress: swap %q: sign assertion: %w", e.Name, err)
	}

	sw.Cache.set(key, jwt, expiry)
	return jwt, nil
}

// signRS256JWT encodes header and claims, signs header.payload with pk using
// RS256 (RSASSA-PKCS1-v1_5 over SHA-256), and returns the compact JWT
// "header.payload.signature" (each part base64url, no padding).
func signRS256JWT(pk *rsa.PrivateKey, claims map[string]any) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64url(headerJSON) + "." + b64url(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, pk, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64url(sig), nil
}

// parseRSAPrivateKey decodes a PEM-encoded RSA private key, accepting either
// PKCS#1 ("RSA PRIVATE KEY") or PKCS#8 ("PRIVATE KEY") encoding. It fails
// closed: a missing PEM block, an unparseable key, or a non-RSA PKCS#8 key all
// return an error so no assertion is signed with an unusable key.
//
// Errors never include any portion of the key bytes.
func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if pk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return pk, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a valid PKCS#1 or PKCS#8 RSA private key")
	}
	pk, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PKCS#8 key is not RSA")
	}
	return pk, nil
}

// interpolateEnv resolves a whole-value "${ENV}" reference to os.Getenv("ENV");
// any other value (including one merely containing "${...}" amongst other text)
// is returned literally. Only the exact "${NAME}" form is treated as an
// environment reference so claim values are predictable.
func interpolateEnv(v string) string {
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") && len(v) > 3 {
		return os.Getenv(v[2 : len(v)-1])
	}
	return v
}

// b64url base64url-encodes b without padding (JWS encoding per RFC 7515).
func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
