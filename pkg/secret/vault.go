package secret

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// VaultProvider resolves a KV v2 secret field from HashiCorp Vault. The
// reference is "<mount>/data/<path>#<field>", e.g. "secret/data/app#api_key".
// It is read-only and holds the value only in memory. Addr is VAULT_ADDR and
// Token is VAULT_TOKEN.
type VaultProvider struct {
	Addr   string
	Token  string
	Client *http.Client
}

func (p *VaultProvider) Plaintext() bool { return false }

func (p *VaultProvider) Resolve(ctx context.Context, rest string) ([]byte, error) {
	path, field, ok := strings.Cut(rest, "#")
	if !ok || path == "" || field == "" {
		return nil, fmt.Errorf("vault reference %q must be <mount>/data/<path>#<field>", rest)
	}
	if p.Addr == "" {
		return nil, fmt.Errorf("vault: VAULT_ADDR is not set")
	}
	if p.Token == "" {
		return nil, fmt.Errorf("vault: VAULT_TOKEN is not set")
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimRight(p.Addr, "/") + "/v1/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("vault: build request: %w", err)
	}
	req.Header.Set("X-Vault-Token", p.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault: request failed: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// fall through to decode
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, fmt.Errorf("vault: permission denied (check VAULT_TOKEN) for %q", path)
	case http.StatusNotFound:
		return nil, fmt.Errorf("vault: secret %q not found", path)
	case http.StatusServiceUnavailable:
		return nil, fmt.Errorf("vault: server unavailable or sealed")
	default:
		return nil, fmt.Errorf("vault: unexpected status %d for %q", resp.StatusCode, path)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("vault: read response: %w", err)
	}
	var payload struct {
		Data struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("vault: decode response: %w", err)
	}
	raw, ok := payload.Data.Data[field]
	if !ok {
		return nil, fmt.Errorf("vault: field %q not present at %q", field, path)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("vault: field %q is not a string", field)
	}
	return []byte(s), nil
}
