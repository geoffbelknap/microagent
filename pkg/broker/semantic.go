package broker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func authorizeSemanticRequest(grant *vmkit.BrokerGrant, r *http.Request, effectiveURL *url.URL) (*vmkit.BrokerOperationGrant, []byte, error) {
	op, err := matchSemanticOperation(grant, r.Method, effectiveURL)
	if err != nil {
		return nil, nil, err
	}
	if err := matchValues(op.Headers, headerValues(r.Header), true); err != nil {
		return op, nil, fmt.Errorf("request headers: %w", err)
	}
	body, err := readRequestBody(r.Body, r.ContentLength, op.Body)
	if err != nil {
		return op, nil, err
	}
	if op.Body != nil {
		if err := validateMediaAndJSON(r.Header.Get("Content-Type"), body, op.Body.ContentTypes, op.Body.JSON); err != nil {
			return op, nil, fmt.Errorf("request body: %w", err)
		}
	}
	return op, body, nil
}

func matchSemanticOperation(grant *vmkit.BrokerGrant, method string, target *url.URL) (*vmkit.BrokerOperationGrant, error) {
	if grant == nil {
		return nil, fmt.Errorf("semantic grant is absent")
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("query is malformed")
	}
	var matched *vmkit.BrokerOperationGrant
	for i := range grant.Operations {
		op := &grant.Operations[i]
		if op.Method != method {
			continue
		}
		if !matchRoute(op.Route, op.PathParameters, target.EscapedPath()) {
			continue
		}
		if err := matchValues(op.Query, query, false); err != nil {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("request ambiguously matches operations %q and %q", matched.Name, op.Name)
		}
		matched = op
	}
	if matched != nil {
		return matched, nil
	}
	return nil, fmt.Errorf("method, route, namespace, or query is outside the semantic grant")
}

func matchRoute(template string, allowed map[string][]string, escapedPath string) bool {
	decoded, err := url.PathUnescape(escapedPath)
	if err != nil || !strings.HasPrefix(decoded, "/") || path.Clean(decoded) != decoded {
		return false
	}
	want := strings.Split(strings.Trim(template, "/"), "/")
	got := strings.Split(strings.Trim(decoded, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		segment := want[i]
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
			if !slices.Contains(allowed[name], got[i]) {
				return false
			}
			continue
		}
		if segment != got[i] {
			return false
		}
	}
	return true
}

// semanticResourceDigest returns a stable, content-free identity for the
// authorized path namespace selected by a semantic operation. It hashes the
// upstream origin plus the names and concrete values of granted path
// parameters. The decision stream can therefore correlate a read and write to
// the same object without persisting the workload's concrete request path.
// Static routes carry no object selector and deliberately return no digest.
func semanticResourceDigest(op *vmkit.BrokerOperationGrant, target *url.URL) string {
	if op == nil || target == nil {
		return ""
	}
	decoded, err := url.PathUnescape(target.EscapedPath())
	if err != nil || path.Clean(decoded) != decoded {
		return ""
	}
	template := strings.Split(strings.Trim(op.Route, "/"), "/")
	actual := strings.Split(strings.Trim(decoded, "/"), "/")
	if len(template) != len(actual) {
		return ""
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, originOf(target))
	selected := false
	for i, segment := range template {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		selected = true
		name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		_, _ = io.WriteString(hash, "\x00"+name+"\x00"+actual[i])
	}
	if !selected {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// semanticResourceSignals classifies suspicious authorized selectors without
// retaining their values. The fixed vocabulary lets campaign-level consumers
// notice encoded or unusually high-entropy object names while the minimized
// decision record continues to omit the concrete request path.
func semanticResourceSignals(op *vmkit.BrokerOperationGrant, target *url.URL) []string {
	if op == nil || target == nil {
		return nil
	}
	decoded, err := url.PathUnescape(target.EscapedPath())
	if err != nil || path.Clean(decoded) != decoded {
		return nil
	}
	template := strings.Split(strings.Trim(op.Route, "/"), "/")
	actual := strings.Split(strings.Trim(decoded, "/"), "/")
	escaped := strings.Split(strings.Trim(target.EscapedPath(), "/"), "/")
	if len(template) != len(actual) || len(template) != len(escaped) {
		return nil
	}
	seen := map[string]bool{}
	for i, segment := range template {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		if strings.Contains(escaped[i], "%") || looksEncodedSelector(actual[i]) {
			seen["encoded-selector"] = true
		}
		if len(actual[i]) >= 24 && selectorEntropy(actual[i]) >= 4.0 {
			seen["high-entropy-selector"] = true
		}
	}
	out := make([]string, 0, len(seen))
	for signal := range seen {
		out = append(out, signal)
	}
	slices.Sort(out)
	return out
}

func looksEncodedSelector(value string) bool {
	if len(value) < 24 {
		return false
	}
	hex := true
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			hex = false
			break
		}
	}
	if hex && len(value)%2 == 0 {
		return true
	}
	for _, r := range strings.TrimRight(value, "=") {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_+/", r) {
			return false
		}
	}
	return selectorEntropy(value) >= 3.5
}

func selectorEntropy(value string) float64 {
	if value == "" {
		return 0
	}
	counts := map[byte]int{}
	for i := 0; i < len(value); i++ {
		counts[value[i]]++
	}
	var entropy float64
	for _, count := range counts {
		p := float64(count) / float64(len(value))
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func headerValues(header http.Header) url.Values {
	values := make(url.Values, len(header))
	for name, vals := range header {
		canonical := http.CanonicalHeaderKey(name)
		// Content-Length and Host are transport framing, just like the
		// connection-scoped headers above. Grant validation intentionally
		// forbids callers from declaring them, so semantic matching must not
		// mistake Go's parsed framing metadata for workload-controlled input.
		if hopByHop[canonical] || canonical == "Content-Length" || canonical == "Host" {
			continue
		}
		values[canonical] = vals
	}
	return values
}

func matchValues(rules []vmkit.BrokerValueGrant, values url.Values, header bool) error {
	byName := make(map[string]vmkit.BrokerValueGrant, len(rules))
	for _, rule := range rules {
		name := rule.Name
		if header {
			name = http.CanonicalHeaderKey(name)
		}
		byName[name] = rule
	}
	for name, vals := range values {
		key := name
		if header {
			key = http.CanonicalHeaderKey(name)
		}
		rule, ok := byName[key]
		if !ok {
			return fmt.Errorf("%q is not declared", name)
		}
		if len(vals) != 1 {
			return fmt.Errorf("%q must appear exactly once", name)
		}
		for _, value := range vals {
			if rule.MaxBytes > 0 && len(value) > rule.MaxBytes {
				return fmt.Errorf("%q value exceeds maxBytes", name)
			}
			if !rule.AllowURL && !header && isURLValue(value) {
				return fmt.Errorf("%q contains a URL-shaped value", name)
			}
			if len(rule.Values) != 0 && !slices.Contains(rule.Values, value) {
				return fmt.Errorf("%q value is outside its allowlist", name)
			}
			if rule.Pattern != "" {
				re := regexp.MustCompile("^(?:" + rule.Pattern + ")$")
				if !re.MatchString(value) {
					return fmt.Errorf("%q value does not match its pattern", name)
				}
			}
		}
	}
	for name, rule := range byName {
		if rule.Required && len(values[name]) == 0 {
			return fmt.Errorf("%q is required", rule.Name)
		}
	}
	return nil
}

func isURLValue(value string) bool {
	candidate := strings.TrimSpace(value)
	for range 4 {
		if strings.HasPrefix(candidate, "//") || strings.HasPrefix(candidate, `\\`) {
			return true
		}
		u, err := url.Parse(candidate)
		if err == nil && u.IsAbs() {
			return true
		}
		decoded, err := url.QueryUnescape(candidate)
		if err != nil || decoded == candidate {
			return false
		}
		candidate = strings.TrimSpace(decoded)
	}
	return false
}

func readRequestBody(body io.ReadCloser, contentLength int64, grant *vmkit.BrokerBodyGrant) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	max := int64(0)
	if grant != nil {
		max = grant.MaxBytes
	}
	if contentLength > max {
		return nil, fmt.Errorf("request body exceeds maxBytes before upstream dispatch")
	}
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("request body exceeds maxBytes before upstream dispatch")
	}
	if len(data) != 0 && grant == nil {
		return nil, fmt.Errorf("request body is not declared")
	}
	return data, nil
}

func validateMediaAndJSON(rawContentType string, body []byte, allowed []string, schema *vmkit.BrokerJSONSchema) error {
	mediaType, _, err := mime.ParseMediaType(rawContentType)
	if err != nil || !slices.ContainsFunc(allowed, func(v string) bool { return strings.EqualFold(v, mediaType) }) {
		return fmt.Errorf("content type %q is outside the grant", rawContentType)
	}
	if schema != nil {
		if err := validateJSONObject(body, schema); err != nil {
			return fmt.Errorf("JSON schema: %w", err)
		}
	}
	return nil
}

func validateJSONObject(body []byte, schema *vmkit.BrokerJSONSchema) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("value is not an object")
	}
	for _, name := range schema.Required {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("required property %q is absent", name)
		}
	}
	for name, value := range object {
		kind, declared := schema.Properties[name]
		if !declared {
			if schema.AdditionalProperties {
				continue
			}
			return fmt.Errorf("property %q is not declared", name)
		}
		if !jsonKind(value, kind) {
			return fmt.Errorf("property %q is not %s", name, kind)
		}
	}
	return nil
}

func jsonKind(value any, kind string) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		n, ok := value.(json.Number)
		return ok && !strings.ContainsAny(string(n), ".eE")
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func responseContainsSecret(header http.Header, body []byte, secrets []string) bool {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if bytes.Contains(body, []byte(secret)) {
			return true
		}
		for name, vals := range header {
			if strings.Contains(name, secret) {
				return true
			}
			for _, value := range vals {
				if strings.Contains(value, secret) {
					return true
				}
			}
		}
	}
	return false
}

func originOf(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

type semanticRedirectError struct{ reason string }

func (e semanticRedirectError) Error() string { return "broker: redirect denied: " + e.reason }

func redirectPolicy(grant *vmkit.BrokerGrant, base *url.URL, initial *vmkit.BrokerOperationGrant, preSwapHeaders, outgoingHeaders http.Header, onAuthorize func(*vmkit.BrokerOperationGrant, *url.URL)) func(*http.Request, []*http.Request) error {
	previousOp := initial
	return func(req *http.Request, via []*http.Request) error {
		if !grant.Redirects.Allow {
			return semanticRedirectError{reason: "redirects are disabled by the semantic grant"}
		}
		if len(via) > grant.Redirects.MaxHops {
			return semanticRedirectError{reason: "redirect exceeds maxHops"}
		}
		allowed := append([]string{originOf(base)}, grant.Redirects.AllowedOrigins...)
		if !slices.ContainsFunc(allowed, func(v string) bool { return strings.EqualFold(v, originOf(req.URL)) }) {
			return semanticRedirectError{reason: "origin is outside the semantic grant"}
		}
		op, err := matchSemanticOperation(grant, req.Method, req.URL)
		if err != nil {
			return semanticRedirectError{reason: "operation is outside the semantic grant: " + err.Error()}
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			return semanticRedirectError{reason: "semantic redirects are limited to bodyless GET/HEAD operations"}
		}
		if op.Body != nil || previousOp == nil || previousOp.Body != nil {
			return semanticRedirectError{reason: "semantic redirect operations cannot declare a request body"}
		}
		for _, previous := range via {
			if previous.Method != http.MethodGet && previous.Method != http.MethodHead {
				return semanticRedirectError{reason: "semantic redirect chains cannot originate from a request with a body"}
			}
		}
		// Reauthorize the guest's pre-swap header shape against the next
		// operation. Then replace net/http's synthesized redirect headers with
		// the exact broker-approved, post-swap set. This both removes Referer and
		// prevents redirect machinery from widening the request.
		if err := matchValues(op.Headers, headerValues(preSwapHeaders), true); err != nil {
			return semanticRedirectError{reason: "headers are outside the semantic grant: " + err.Error()}
		}
		req.Header = outgoingHeaders.Clone()
		previousOp = op
		onAuthorize(op, req.URL)
		return nil
	}
}
