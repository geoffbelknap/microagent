package workspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"gopkg.in/yaml.v3"
)

// preflightBrokerSecrets resolves every broker endpoint secret reference and
// checks upstream CA readability before any supervisor process spawns. The
// broker companion resolves the same references at startup in the same host
// environment this process runs in — but by then start has already reported
// success, so an unresolvable reference surfaces as a silent guest death
// half a minute later with the reason buried in supervisor.log. Failing
// closed here turns that into a structured error at the boundary that asked
// for the start. The check never returns or logs the secret value.
func preflightBrokerSecrets(ctx context.Context, brokers []*vmkit.BrokerConfig) error {
	var registry *secret.Registry
	for _, broker := range brokers {
		if broker == nil {
			continue
		}
		if ref := strings.TrimSpace(broker.Secret.Ref); ref != "" {
			if registry == nil {
				registry = secret.DefaultRegistry(os.Getenv, nil)
			}
			result := registry.Check(ctx, broker.Secret.Name+"="+ref)
			if !result.OK {
				return operation.New(operation.ErrorValidation,
					"broker endpoint %s: secret %q did not resolve: %s; fix the reference source, verify with `microagent secret check %s=%s`, then start again",
					broker.Upstream, broker.Secret.Name, result.Error, broker.Secret.Name, ref)
			}
		}
		if ca := strings.TrimSpace(broker.UpstreamCAFile); ca != "" {
			if err := requireReadableFile(ca, "broker upstream CA bundle"); err != nil {
				return operation.New(operation.ErrorValidation, "broker endpoint %s: %v", broker.Upstream, err)
			}
		}
	}
	return nil
}

// BrokerSecurityOptions declares a parsed endpoint's trust contract. Assurance
// is semantic or trusted-upstream. GrantPath names the required YAML/JSON grant
// for semantic assurance and stays empty for trusted-upstream.
type BrokerSecurityOptions struct {
	Assurance string
	GrantPath string
}

// ParseBrokerConfig builds one validated *vmkit.BrokerConfig for CLI,
// Agentfile, MCP, and Go callers. It fails closed on partial declarations,
// literal secrets, malformed fields, missing assurance, or an invalid grant.
// The variadic security argument preserves source compatibility, but a
// declared endpoint without exactly one explicit security declaration is not
// accepted. Transport defaults are filled later by Request.
func ParseBrokerConfig(upstream, secretSpec string, env []string, proxy, capture bool, ca string, security ...BrokerSecurityOptions) (*vmkit.BrokerConfig, error) {
	if len(security) > 1 {
		return nil, fmt.Errorf("broker: at most one security declaration is allowed")
	}
	var assurance, grantPath string
	if len(security) == 1 {
		assurance = security[0].Assurance
		grantPath = security[0].GrantPath
	}
	upstream = strings.TrimSpace(upstream)
	secretSpec = strings.TrimSpace(secretSpec)
	ca = strings.TrimSpace(ca)
	assurance = strings.TrimSpace(assurance)
	grantPath = strings.TrimSpace(grantPath)
	if upstream == "" && secretSpec == "" {
		if len(env) != 0 || proxy || capture || ca != "" || assurance != "" || grantPath != "" {
			return nil, fmt.Errorf("broker env/proxy/capture/ca/assurance/grant require a broker upstream and secret")
		}
		return nil, nil
	}
	if upstream == "" || secretSpec == "" {
		return nil, fmt.Errorf("broker upstream and secret are required together")
	}
	name, ref, ok := strings.Cut(secretSpec, "=")
	name = strings.TrimSpace(name)
	ref = strings.TrimSpace(ref)
	if !ok || name == "" || ref == "" {
		return nil, fmt.Errorf("broker secret must be NAME=<scheme>:<ref>: %s", secretSpec)
	}
	if !secretxfer.ValidName(name) {
		return nil, fmt.Errorf("broker secret name is invalid: %s", name)
	}
	if !secret.DefaultRegistry(nil, nil).ValidRef(ref) {
		return nil, fmt.Errorf("broker secret reference %q must be <scheme>:<ref> (env:/file:/dotenv:/vault:/helper:), never a literal secret", ref)
	}
	var baseURLEnv map[string]string
	for _, raw := range env {
		key, value, _ := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !validBrokerEnvName(key) {
			return nil, fmt.Errorf("broker env key is invalid: %s", raw)
		}
		if baseURLEnv == nil {
			baseURLEnv = map[string]string{}
		}
		baseURLEnv[key] = value
	}
	var grant *vmkit.BrokerGrant
	if grantPath != "" {
		data, err := os.ReadFile(grantPath)
		if err != nil {
			return nil, fmt.Errorf("read broker grant %q: %w", grantPath, err)
		}
		grant = &vmkit.BrokerGrant{}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(grant); err != nil {
			return nil, fmt.Errorf("parse broker grant %q: %w", grantPath, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				err = fmt.Errorf("multiple YAML documents are not allowed")
			}
			return nil, fmt.Errorf("parse broker grant %q: %w", grantPath, err)
		}
	}
	cfg := &vmkit.BrokerConfig{
		Upstream:       upstream,
		Secret:         vmkit.SecretRef{Name: name, Ref: ref},
		Proxy:          proxy,
		BaseURLEnv:     baseURLEnv,
		Capture:        capture,
		UpstreamCAFile: ca,
		Assurance:      vmkit.BrokerAssurance(assurance),
		Grant:          grant,
	}
	if err := vmkit.ValidateBrokerSecurity(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ParseBrokerEndpoints parses N broker endpoint specs — the string form the
// CLI (--broker-endpoint, repeatable) and MCP (a "brokers" array of the same
// spec strings) declare multiple endpoints with — into the multi-endpoint
// vmkit.BrokerConfig set. Each spec is a `;`-separated list of key=value
// pairs (`;` rather than `,` because secret/env values contain `=`); each
// pair is split on its FIRST `=` so a secret value's own `=` survives intact.
// Recognized keys:
//
//   - upstream=<url>              (required)
//   - secret=NAME=<scheme>:<ref>  (required; the value keeps its inner "=")
//   - base-url-env=KEY[=VALUE]    (optional, repeatable within one endpoint)
//   - ca=<path>                   (optional -> UpstreamCAFile)
//   - assurance=<mode>            (required: semantic|trusted-upstream)
//   - grant=<path>                (required for semantic)
//   - proxy                       (bare key -> Proxy=true)
//   - capture                     (bare key -> Capture=true)
//
// An unknown key, or a spec that fails ParseBrokerConfig's own validation
// (partial declaration, literal secret, malformed name/env key), fails
// closed. Every endpoint delegates to ParseBrokerConfig for construction —
// this is the only parser for endpoint content, grammar splitting aside — so
// CLI, MCP, and (structurally) the Agentfile agent.brokers block all build
// identical configs. It does not assign transport ports/guest-listen
// addresses; that is normalizeBrokers' job at Request time. It returns nil
// for an empty spec list (no declaration, no error).
func ParseBrokerEndpoints(specs []string) ([]*vmkit.BrokerConfig, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]*vmkit.BrokerConfig, 0, len(specs))
	for i, spec := range specs {
		upstream, secretSpec, env, ca, assurance, grantPath, proxy, capture, err := parseBrokerEndpointSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("broker endpoint %d: %w", i+1, err)
		}
		cfg, err := ParseBrokerConfig(upstream, secretSpec, env, proxy, capture, ca, BrokerSecurityOptions{Assurance: assurance, GrantPath: grantPath})
		if err != nil {
			return nil, fmt.Errorf("broker endpoint %d: %w", i+1, err)
		}
		if cfg == nil {
			return nil, fmt.Errorf("broker endpoint %d: declares nothing (upstream and secret are required)", i+1)
		}
		out = append(out, cfg)
	}
	if err := validateBrokerEndpointSet(out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseBrokerEndpointSpec splits one `;`-separated endpoint spec into the raw
// pieces ParseBrokerConfig expects. See ParseBrokerEndpoints for the grammar.
func parseBrokerEndpointSpec(spec string) (upstream, secretSpec string, env []string, ca, assurance, grantPath string, proxy, capture bool, err error) {
	for _, pair := range strings.Split(spec, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, hasValue := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		switch key {
		case "upstream":
			upstream = value
		case "secret":
			secretSpec = value
		case "base-url-env":
			env = append(env, value)
		case "ca":
			ca = value
		case "assurance":
			assurance = value
		case "grant":
			grantPath = value
		case "proxy":
			if hasValue {
				return "", "", nil, "", "", "", false, false, fmt.Errorf("proxy takes no value: %s", pair)
			}
			proxy = true
		case "capture":
			if hasValue {
				return "", "", nil, "", "", "", false, false, fmt.Errorf("capture takes no value: %s", pair)
			}
			capture = true
		default:
			return "", "", nil, "", "", "", false, false, fmt.Errorf("unknown key %q in %q", key, pair)
		}
	}
	return upstream, secretSpec, env, ca, assurance, grantPath, proxy, capture, nil
}

// validateBrokerEndpointSet fails closed when more than one endpoint in a set
// claims the single guest-wide HTTPS_PROXY/HTTP_PROXY slot. It mirrors
// normalizeBrokers' ≤1-proxy rule, run here too so every declaring surface
// (ParseBrokerEndpoints for CLI/MCP; the Agentfile agent.brokers block, which
// calls this directly) gives a clear message at parse time instead of only
// failing later at Request.
func validateBrokerEndpointSet(brokers []*vmkit.BrokerConfig) error {
	proxyCount := 0
	for _, b := range brokers {
		if b.Proxy {
			proxyCount++
		}
	}
	if proxyCount > 1 {
		return fmt.Errorf("broker: only one endpoint may set proxy (HTTPS_PROXY/HTTP_PROXY is a single guest-wide slot)")
	}
	return nil
}

// validBrokerEnvName reports whether key is a legal environment variable name
// (letters, digits, underscore; not starting with a digit).
func validBrokerEnvName(key string) bool {
	for i, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z' && i > 0:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return key != ""
}
