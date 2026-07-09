package workspace

import (
	"fmt"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// ParseBrokerConfig builds a *vmkit.BrokerConfig from the raw pieces both the
// CLI flags (--broker-*) and the Agentfile agent.broker block supply, so the
// two surfaces validate and construct a broker identically. It returns nil when
// nothing is declared, and fails closed on a partial declaration, a pasted
// literal secret, or a malformed name/reference/env key — before any state is
// written. Transport defaults (vsock port, guest listen address) are filled
// later by Request via normalizeBrokerConfig.
//
//   - upstream:   terminate-mode upstream base URL.
//   - secretSpec: "NAME=<scheme>:<ref>"; the credential is held host-side only.
//   - env:        base-URL env keys pointed at the broker, each "KEY[=VALUE]".
//   - proxy:      set HTTPS_PROXY/HTTP_PROXY in the guest to the broker.
func ParseBrokerConfig(upstream, secretSpec string, env []string, proxy bool) (*vmkit.BrokerConfig, error) {
	upstream = strings.TrimSpace(upstream)
	secretSpec = strings.TrimSpace(secretSpec)
	if upstream == "" && secretSpec == "" {
		if len(env) != 0 || proxy {
			return nil, fmt.Errorf("broker env/proxy require a broker upstream and secret")
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
		Upstream:   upstream,
		Secret:     vmkit.SecretRef{Name: name, Ref: ref},
		Proxy:      proxy,
		BaseURLEnv: baseURLEnv,
	}, nil
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
