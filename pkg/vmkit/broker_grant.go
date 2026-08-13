package vmkit

import (
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"strings"
)

// BrokerAssurance states what the terminating endpoint promises. Semantic
// endpoints enforce a finite operation and response contract. TrustedUpstream
// endpoints preserve the historical broad relay and therefore rely on the
// upstream not returning or transforming the injected credential.
type BrokerAssurance string

const (
	BrokerAssuranceSemantic        BrokerAssurance = "semantic"
	BrokerAssuranceTrustedUpstream BrokerAssurance = "trusted-upstream"
)

// BrokerEffect classifies the externally visible effect of an operation.
// microagent enforces the declared operation; the governing caller decides
// whether a particular read or write belongs in its policy.
type BrokerEffect string

const (
	BrokerEffectRead  BrokerEffect = "read"
	BrokerEffectWrite BrokerEffect = "write"
)

type BrokerCredentialDisclosure string

const BrokerCredentialDisclosureDenyExact BrokerCredentialDisclosure = "deny-exact"

// BrokerGrant is a finite semantic capability for one terminating endpoint.
// A request must match exactly one operation. Redirects and responses are
// checked before another hop or any response byte reaches the guest.
type BrokerGrant struct {
	Operations []BrokerOperationGrant `json:"operations" yaml:"operations"`
	Redirects  BrokerRedirectGrant    `json:"redirects,omitempty" yaml:"redirects,omitempty"`
}

type BrokerOperationGrant struct {
	Name           string              `json:"name" yaml:"name"`
	Effect         BrokerEffect        `json:"effect" yaml:"effect"`
	Method         string              `json:"method" yaml:"method"`
	Route          string              `json:"route" yaml:"route"`
	PathParameters map[string][]string `json:"pathParameters,omitempty" yaml:"pathParameters,omitempty"`
	Query          []BrokerValueGrant  `json:"query,omitempty" yaml:"query,omitempty"`
	Headers        []BrokerValueGrant  `json:"headers,omitempty" yaml:"headers,omitempty"`
	Body           *BrokerBodyGrant    `json:"body,omitempty" yaml:"body,omitempty"`
	Response       BrokerResponseGrant `json:"response" yaml:"response"`
}

// BrokerValueGrant constrains one query parameter or request header. Values
// is an exact allowlist; Pattern is an optional RE2 expression applied in
// addition. URL-shaped query values are denied unless AllowURL is explicit.
type BrokerValueGrant struct {
	Name     string   `json:"name" yaml:"name"`
	Required bool     `json:"required,omitempty" yaml:"required,omitempty"`
	Values   []string `json:"values,omitempty" yaml:"values,omitempty"`
	Pattern  string   `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	MaxBytes int      `json:"maxBytes,omitempty" yaml:"maxBytes,omitempty"`
	AllowURL bool     `json:"allowURL,omitempty" yaml:"allowURL,omitempty"`
}

type BrokerBodyGrant struct {
	MaxBytes     int64             `json:"maxBytes" yaml:"maxBytes"`
	ContentTypes []string          `json:"contentTypes,omitempty" yaml:"contentTypes,omitempty"`
	JSON         *BrokerJSONSchema `json:"json,omitempty" yaml:"json,omitempty"`
}

// BrokerJSONSchema is a deliberately small, deterministic JSON-object schema.
// It constrains top-level property names and primitive/container types without
// embedding a general schema interpreter in the VM substrate.
type BrokerJSONSchema struct {
	Type                 string            `json:"type" yaml:"type"`
	Properties           map[string]string `json:"properties,omitempty" yaml:"properties,omitempty"`
	Required             []string          `json:"required,omitempty" yaml:"required,omitempty"`
	AdditionalProperties bool              `json:"additionalProperties,omitempty" yaml:"additionalProperties,omitempty"`
}

type BrokerResponseGrant struct {
	Statuses             []int                      `json:"statuses" yaml:"statuses"`
	ContentTypes         []string                   `json:"contentTypes" yaml:"contentTypes"`
	MaxBytes             int64                      `json:"maxBytes" yaml:"maxBytes"`
	JSON                 *BrokerJSONSchema          `json:"json,omitempty" yaml:"json,omitempty"`
	CredentialDisclosure BrokerCredentialDisclosure `json:"credentialDisclosure" yaml:"credentialDisclosure"`
}

type BrokerRedirectGrant struct {
	Allow          bool     `json:"allow,omitempty" yaml:"allow,omitempty"`
	MaxHops        int      `json:"maxHops,omitempty" yaml:"maxHops,omitempty"`
	AllowedOrigins []string `json:"allowedOrigins,omitempty" yaml:"allowedOrigins,omitempty"`
}

// ValidateBrokerSecurity validates the assurance declaration independently of
// any CLI or supervisor. Both supported backends call the same endpoint server,
// which calls this function again before accepting a guest connection.
func ValidateBrokerSecurity(cfg *BrokerConfig) error {
	if cfg == nil {
		return fmt.Errorf("broker: endpoint is required")
	}
	u, err := url.Parse(cfg.Upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("broker: assurance requires an http or https upstream URL without embedded credentials")
	}
	switch cfg.Assurance {
	case BrokerAssuranceSemantic:
		if u.Scheme != "https" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && (pathpkg.Clean(u.Path) != u.Path || strings.Contains(u.Path, "//"))) {
			return fmt.Errorf("broker: semantic assurance requires an https upstream URL")
		}
		if cfg.Grant == nil {
			return fmt.Errorf("broker: semantic assurance requires a grant")
		}
		if cfg.Proxy {
			return fmt.Errorf("broker: semantic assurance cannot enable opaque CONNECT proxying; use a terminating base URL")
		}
		return validateBrokerGrant(cfg.Upstream, cfg.Grant)
	case BrokerAssuranceTrustedUpstream:
		if cfg.Grant != nil {
			return fmt.Errorf("broker: trusted-upstream assurance cannot carry a semantic grant")
		}
		return nil
	case "":
		return fmt.Errorf("broker: assurance is required (semantic with a grant, or explicit trusted-upstream for the broad relay)")
	default:
		return fmt.Errorf("broker: unknown assurance %q (want semantic or trusted-upstream)", cfg.Assurance)
	}
}

func validateBrokerGrant(upstream string, grant *BrokerGrant) error {
	if len(grant.Operations) == 0 {
		return fmt.Errorf("broker: semantic grant must declare at least one operation")
	}
	seen := map[string]bool{}
	for i, op := range grant.Operations {
		prefix := fmt.Sprintf("broker: operation %d", i+1)
		if strings.TrimSpace(op.Name) == "" || seen[op.Name] {
			return fmt.Errorf("%s: name is empty or duplicated", prefix)
		}
		seen[op.Name] = true
		if op.Effect != BrokerEffectRead && op.Effect != BrokerEffectWrite {
			return fmt.Errorf("%s %q: effect must be read or write", prefix, op.Name)
		}
		if !validBrokerMethod(op.Method) {
			return fmt.Errorf("%s %q: method must contain uppercase ASCII letters only", prefix, op.Name)
		}
		if err := validateRoute(op.Route, op.PathParameters); err != nil {
			return fmt.Errorf("%s %q: %w", prefix, op.Name, err)
		}
		if err := validateValueGrants(op.Query, false); err != nil {
			return fmt.Errorf("%s %q query: %w", prefix, op.Name, err)
		}
		if err := validateValueGrants(op.Headers, true); err != nil {
			return fmt.Errorf("%s %q headers: %w", prefix, op.Name, err)
		}
		if op.Body != nil {
			if err := validateBodyGrant(*op.Body); err != nil {
				return fmt.Errorf("%s %q body: %w", prefix, op.Name, err)
			}
		}
		if err := validateResponseGrant(op.Response); err != nil {
			return fmt.Errorf("%s %q response: %w", prefix, op.Name, err)
		}
	}
	if grant.Redirects.Allow {
		if grant.Redirects.MaxHops <= 0 {
			return fmt.Errorf("broker: redirect maxHops must be positive when redirects are allowed")
		}
		base, err := url.Parse(upstream)
		if err != nil {
			return fmt.Errorf("broker: parse upstream for redirect policy: %w", err)
		}
		origins := append([]string{origin(base)}, grant.Redirects.AllowedOrigins...)
		for _, raw := range origins {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("broker: redirect allowed origin %q must be an https origin with no path, query, or fragment", raw)
			}
		}
	} else if grant.Redirects.MaxHops != 0 || len(grant.Redirects.AllowedOrigins) != 0 {
		return fmt.Errorf("broker: redirect maxHops/origins require allow=true")
	}
	return nil
}

func validateRoute(route string, params map[string][]string) error {
	if route == "" || !strings.HasPrefix(route, "/") || strings.Contains(route, "//") {
		return fmt.Errorf("route %q must be an absolute normalized path template", route)
	}
	declared := map[string]bool{}
	for _, part := range strings.Split(strings.Trim(route, "/"), "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			if name == "" || declared[name] {
				return fmt.Errorf("route %q has an empty or duplicated path parameter", route)
			}
			declared[name] = true
		} else if strings.ContainsAny(part, "{}") || part == "." || part == ".." {
			return fmt.Errorf("route %q contains an invalid segment", route)
		}
	}
	for name, values := range params {
		if !declared[name] || len(values) == 0 {
			return fmt.Errorf("path parameter %q is undeclared or has no allowed namespace values", name)
		}
		for _, value := range values {
			if value == "" || strings.Contains(value, "/") || value == "." || value == ".." {
				return fmt.Errorf("path parameter %q has invalid namespace value %q", name, value)
			}
		}
	}
	for name := range declared {
		if len(params[name]) == 0 {
			return fmt.Errorf("path parameter %q requires an explicit namespace allowlist", name)
		}
	}
	return nil
}

func validateValueGrants(grants []BrokerValueGrant, header bool) error {
	seen := map[string]bool{}
	for _, rule := range grants {
		name := strings.TrimSpace(rule.Name)
		if header {
			name = http.CanonicalHeaderKey(name)
		}
		if name == "" || seen[name] {
			return fmt.Errorf("name is empty or duplicated: %q", rule.Name)
		}
		seen[name] = true
		if header && forbiddenBrokerRequestHeader(name) {
			return fmt.Errorf("header %q is controlled by HTTP framing or transport and cannot be granted", rule.Name)
		}
		if len(rule.Values) == 0 && rule.Pattern == "" {
			return fmt.Errorf("%q must constrain values or pattern", rule.Name)
		}
		if rule.Pattern != "" {
			if rule.MaxBytes <= 0 {
				return fmt.Errorf("%q uses a pattern and therefore requires positive maxBytes", rule.Name)
			}
			if _, err := regexp.Compile("^(?:" + rule.Pattern + ")$"); err != nil {
				return fmt.Errorf("%q has invalid pattern: %w", rule.Name, err)
			}
		}
		if header && rule.AllowURL {
			return fmt.Errorf("header %q cannot set allowURL", rule.Name)
		}
		if rule.MaxBytes < 0 {
			return fmt.Errorf("%q maxBytes cannot be negative", rule.Name)
		}
	}
	return nil
}

func validBrokerMethod(method string) bool {
	if method == "" {
		return false
	}
	for _, c := range method {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func forbiddenBrokerRequestHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Content-Length", "Host", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func validateBodyGrant(body BrokerBodyGrant) error {
	if body.MaxBytes <= 0 {
		return fmt.Errorf("maxBytes must be positive")
	}
	if len(body.ContentTypes) == 0 {
		return fmt.Errorf("contentTypes must be non-empty")
	}
	if body.JSON == nil || len(body.ContentTypes) != 1 || !strings.EqualFold(body.ContentTypes[0], "application/json") {
		return fmt.Errorf("semantic request bodies require one application/json content type and a JSON schema")
	}
	if err := validateJSONSchema(body.JSON); err != nil {
		return err
	}
	return nil
}

func validateResponseGrant(response BrokerResponseGrant) error {
	if len(response.Statuses) == 0 || len(response.ContentTypes) == 0 || response.MaxBytes <= 0 {
		return fmt.Errorf("statuses, contentTypes, and positive maxBytes are required")
	}
	for _, status := range response.Statuses {
		if status < 100 || status > 599 {
			return fmt.Errorf("invalid status %d", status)
		}
	}
	if response.CredentialDisclosure != BrokerCredentialDisclosureDenyExact {
		return fmt.Errorf("credentialDisclosure must be deny-exact")
	}
	if response.JSON == nil || len(response.ContentTypes) != 1 || !strings.EqualFold(response.ContentTypes[0], "application/json") {
		return fmt.Errorf("semantic responses require one application/json content type and a JSON schema")
	}
	return validateJSONSchema(response.JSON)
}

func validateJSONSchema(schema *BrokerJSONSchema) error {
	if schema.Type != "object" {
		return fmt.Errorf("JSON schema type must be object")
	}
	valid := map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "object": true, "array": true, "null": true}
	for name, kind := range schema.Properties {
		if name == "" || !valid[kind] {
			return fmt.Errorf("JSON property %q has unsupported type %q", name, kind)
		}
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			return fmt.Errorf("required JSON property %q is not declared", name)
		}
	}
	return nil
}

func origin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}
