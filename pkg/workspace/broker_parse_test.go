package workspace

import (
	"strings"
	"testing"
)

// TestParseBrokerEndpointsTwoEndpoints is the truth set: two endpoint specs
// exercising every grammar key (upstream, secret, base-url-env repeated, ca,
// proxy, capture) parse to two correctly-populated *vmkit.BrokerConfig,
// each built by delegating to ParseBrokerConfig (proving reuse, not a
// parallel parser).
func TestParseBrokerEndpointsTwoEndpoints(t *testing.T) {
	specs := []string{
		"upstream=https://a.example.com;secret=a=env:A_TOKEN;base-url-env=A_BASE_URL;ca=/etc/ssl/a.pem",
		"upstream=https://b.example.com;secret=b=env:B_TOKEN;base-url-env=B_BASE_URL;base-url-env=OTHER_BASE_URL=http://127.0.0.1:1/v1;proxy;capture",
	}
	out, err := ParseBrokerEndpoints(specs)
	if err != nil {
		t.Fatalf("ParseBrokerEndpoints: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("ParseBrokerEndpoints returned %d endpoints, want 2", len(out))
	}

	first := out[0]
	if first.Upstream != "https://a.example.com" {
		t.Fatalf("first.Upstream = %q", first.Upstream)
	}
	if first.Secret.Name != "a" || first.Secret.Ref != "env:A_TOKEN" {
		t.Fatalf("first.Secret = %+v", first.Secret)
	}
	if _, ok := first.BaseURLEnv["A_BASE_URL"]; !ok {
		t.Fatalf("first.BaseURLEnv missing A_BASE_URL: %+v", first.BaseURLEnv)
	}
	if first.UpstreamCAFile != "/etc/ssl/a.pem" {
		t.Fatalf("first.UpstreamCAFile = %q, want /etc/ssl/a.pem", first.UpstreamCAFile)
	}
	if first.Proxy || first.Capture {
		t.Fatalf("first endpoint should not have proxy/capture set: %+v", first)
	}

	second := out[1]
	if second.Upstream != "https://b.example.com" {
		t.Fatalf("second.Upstream = %q", second.Upstream)
	}
	if second.Secret.Name != "b" || second.Secret.Ref != "env:B_TOKEN" {
		t.Fatalf("second.Secret = %+v", second.Secret)
	}
	if _, ok := second.BaseURLEnv["B_BASE_URL"]; !ok {
		t.Fatalf("second.BaseURLEnv missing B_BASE_URL: %+v", second.BaseURLEnv)
	}
	if second.BaseURLEnv["OTHER_BASE_URL"] != "http://127.0.0.1:1/v1" {
		t.Fatalf("second.BaseURLEnv[OTHER_BASE_URL] = %q", second.BaseURLEnv["OTHER_BASE_URL"])
	}
	if !second.Proxy {
		t.Fatal("second.Proxy not set")
	}
	if !second.Capture {
		t.Fatal("second.Capture not set")
	}
	if second.UpstreamCAFile != "" {
		t.Fatalf("second.UpstreamCAFile = %q, want empty", second.UpstreamCAFile)
	}
}

// TestParseBrokerEndpointsRejectsSecondProxy mirrors normalizeBrokers' single
// HTTPS_PROXY-slot rule at parse time, so the declaring surface gets a clear
// message instead of failing later at Request.
func TestParseBrokerEndpointsRejectsSecondProxy(t *testing.T) {
	specs := []string{
		"upstream=https://a.example.com;secret=a=env:A_TOKEN;proxy",
		"upstream=https://b.example.com;secret=b=env:B_TOKEN;proxy",
	}
	_, err := ParseBrokerEndpoints(specs)
	if err == nil {
		t.Fatal("ParseBrokerEndpoints accepted two endpoints with proxy set")
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("error = %q, want it to mention proxy", err)
	}
}

// TestParseBrokerEndpointsRejectsUnknownKey fails closed on a typo'd key
// rather than silently ignoring it.
func TestParseBrokerEndpointsRejectsUnknownKey(t *testing.T) {
	specs := []string{"upstream=https://a.example.com;secret=a=env:A_TOKEN;bogus=x"}
	_, err := ParseBrokerEndpoints(specs)
	if err == nil {
		t.Fatal("ParseBrokerEndpoints accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %q, want it to name the unknown key", err)
	}
}

// TestParseBrokerEndpointsRejectsMalformedSecret proves per-endpoint
// delegation to ParseBrokerConfig: a missing-scheme (literal) secret is
// rejected the same way the single-broker path rejects it.
func TestParseBrokerEndpointsRejectsMalformedSecret(t *testing.T) {
	specs := []string{"upstream=https://a.example.com;secret=a=sk-pasted-literal"}
	_, err := ParseBrokerEndpoints(specs)
	if err == nil {
		t.Fatal("ParseBrokerEndpoints accepted a literal (non-reference) secret")
	}
	if !strings.Contains(err.Error(), "literal") {
		t.Fatalf("error = %q, want it to surface ParseBrokerConfig's literal-secret message", err)
	}

	if _, err := ParseBrokerEndpoints([]string{"upstream=https://a.example.com;secret=no-equals"}); err == nil {
		t.Fatal("ParseBrokerEndpoints accepted a secret with no scheme separator")
	}
}

// TestParseBrokerEndpointsEmpty asserts a nil/empty spec list yields nil, no
// error — no declaration, no broker.
func TestParseBrokerEndpointsEmpty(t *testing.T) {
	out, err := ParseBrokerEndpoints(nil)
	if err != nil || out != nil {
		t.Fatalf("ParseBrokerEndpoints(nil) = %+v, %v; want nil, nil", out, err)
	}
	out, err = ParseBrokerEndpoints([]string{})
	if err != nil || out != nil {
		t.Fatalf("ParseBrokerEndpoints([]) = %+v, %v; want nil, nil", out, err)
	}
}

// TestParseBrokerEndpointsRejectsEmptyEntry fails closed on a spec that
// declares nothing (e.g. a stray blank --broker-endpoint), rather than
// silently producing an incomplete endpoint.
func TestParseBrokerEndpointsRejectsEmptyEntry(t *testing.T) {
	if _, err := ParseBrokerEndpoints([]string{""}); err == nil {
		t.Fatal("ParseBrokerEndpoints accepted a blank endpoint spec")
	}
}
