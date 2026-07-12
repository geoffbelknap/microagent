package workspace

import (
	"fmt"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// ParseBrokerConfig builds a *vmkit.BrokerConfig from the raw pieces every
// broker-declaring surface (CLI flags, Agentfile agent.broker block, MCP
// params, and per-endpoint within ParseBrokerEndpoints) supplies, so all of
// them validate and construct a broker identically. It returns nil when
// nothing is declared, and fails closed on a partial declaration, a pasted
// literal secret, or a malformed name/reference/env key — before any state is
// written. Transport defaults (vsock port, guest listen address) are filled
// later by Request via normalizeBrokerConfig.
//
//   - upstream:   terminate-mode upstream base URL.
//   - secretSpec: "NAME=<scheme>:<ref>"; the credential is held host-side only.
//   - env:        base-URL env keys pointed at the broker, each "KEY[=VALUE]".
//   - proxy:      set HTTPS_PROXY/HTTP_PROXY in the guest to the broker.
//   - capture:    governed raw-capture opt-in (pre-swap requests to a
//     separate owner-only file); off by default.
//   - ca:         optional PEM bundle path this endpoint's upstream TLS client
//     trusts (maps to UpstreamCAFile); empty means system roots.
func ParseBrokerConfig(upstream, secretSpec string, env []string, proxy, capture bool, ca string) (*vmkit.BrokerConfig, error) {
	upstream = strings.TrimSpace(upstream)
	secretSpec = strings.TrimSpace(secretSpec)
	ca = strings.TrimSpace(ca)
	if upstream == "" && secretSpec == "" {
		if len(env) != 0 || proxy || capture || ca != "" {
			return nil, fmt.Errorf("broker env/proxy/capture/ca require a broker upstream and secret")
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
	return &vmkit.BrokerConfig{
		Upstream:       upstream,
		Secret:         vmkit.SecretRef{Name: name, Ref: ref},
		Proxy:          proxy,
		BaseURLEnv:     baseURLEnv,
		Capture:        capture,
		UpstreamCAFile: ca,
	}, nil
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
		upstream, secretSpec, env, ca, proxy, capture, err := parseBrokerEndpointSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("broker endpoint %d: %w", i+1, err)
		}
		cfg, err := ParseBrokerConfig(upstream, secretSpec, env, proxy, capture, ca)
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
func parseBrokerEndpointSpec(spec string) (upstream, secretSpec string, env []string, ca string, proxy, capture bool, err error) {
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
		case "proxy":
			if hasValue {
				return "", "", nil, "", false, false, fmt.Errorf("proxy takes no value: %s", pair)
			}
			proxy = true
		case "capture":
			if hasValue {
				return "", "", nil, "", false, false, fmt.Errorf("capture takes no value: %s", pair)
			}
			capture = true
		default:
			return "", "", nil, "", false, false, fmt.Errorf("unknown key %q in %q", key, pair)
		}
	}
	return upstream, secretSpec, env, ca, proxy, capture, nil
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
