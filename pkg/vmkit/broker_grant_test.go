package vmkit

import "testing"

func validBrokerGrant() *BrokerGrant {
	return &BrokerGrant{Operations: []BrokerOperationGrant{{
		Name: "read", Effect: BrokerEffectRead, Method: "GET", Route: "/repos/{owner}/{repo}",
		PathParameters: map[string][]string{"owner": {"acme"}, "repo": {"widgets"}},
		Headers:        []BrokerValueGrant{{Name: "Authorization", Required: true, Pattern: `Bearer @secret:.+`, MaxBytes: 128}},
		Response: BrokerResponseGrant{
			Statuses: []int{200}, ContentTypes: []string{"application/json"}, MaxBytes: 4096,
			CredentialDisclosure: "deny-exact", JSON: &BrokerJSONSchema{Type: "object"},
		},
	}}}
}

func TestValidateBrokerSecurityRequiresExplicitAssurance(t *testing.T) {
	base := BrokerConfig{Upstream: "https://api.example.com", Secret: SecretRef{Name: "api", Ref: "env:TOKEN"}}
	if err := ValidateBrokerSecurity(&base); err == nil {
		t.Fatal("endpoint without assurance was accepted")
	}
	base.Assurance = BrokerAssuranceTrustedUpstream
	if err := ValidateBrokerSecurity(&base); err != nil {
		t.Fatalf("explicit trusted-upstream: %v", err)
	}
	base.Assurance = BrokerAssuranceSemantic
	if err := ValidateBrokerSecurity(&base); err == nil {
		t.Fatal("semantic endpoint without grant was accepted")
	}
	base.Grant = validBrokerGrant()
	if err := ValidateBrokerSecurity(&base); err != nil {
		t.Fatalf("valid semantic endpoint: %v", err)
	}
}

func TestValidateBrokerSecurityRejectsAmbiguousOrBroadSemanticGrant(t *testing.T) {
	cases := []struct {
		name string
		edit func(*BrokerConfig)
	}{
		{"non-TLS upstream", func(c *BrokerConfig) { c.Upstream = "http://api.example.com" }},
		{"opaque proxy", func(c *BrokerConfig) { c.Proxy = true }},
		{"unbounded namespace", func(c *BrokerConfig) { c.Grant.Operations[0].PathParameters["repo"] = nil }},
		{"missing response bound", func(c *BrokerConfig) { c.Grant.Operations[0].Response.MaxBytes = 0 }},
		{"missing disclosure rule", func(c *BrokerConfig) { c.Grant.Operations[0].Response.CredentialDisclosure = "" }},
		{"invalid method token", func(c *BrokerConfig) { c.Grant.Operations[0].Method = "GET\n" }},
		{"transport-controlled header", func(c *BrokerConfig) { c.Grant.Operations[0].Headers[0].Name = "Host" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BrokerConfig{Upstream: "https://api.example.com", Assurance: BrokerAssuranceSemantic, Grant: validBrokerGrant()}
			tc.edit(&cfg)
			if err := ValidateBrokerSecurity(&cfg); err == nil {
				t.Fatal("invalid semantic endpoint was accepted")
			}
		})
	}
}
