package broker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// GuestConfig describes how a workspace's guest is wired to reach the broker
// with no manual steps. It produces only non-secret environment: the guest
// holds a credential *reference*, never the live secret, so nothing here is
// sensitive.
type GuestConfig struct {
	// GuestListen is the in-guest address the vsock bridge listens on and the
	// workload is pointed at, e.g. "127.0.0.1:8888".
	GuestListen string
	// VsockPort is the host vsock port the bridge forwards to — where the
	// broker's host-side listener sits (the Firecracker vsock UDS
	// <uds>_<VsockPort>).
	VsockPort uint32
	// Proxy sets HTTPS_PROXY / HTTP_PROXY to the broker so proxy-honoring
	// clients route their HTTPS through it (CONNECT / tunnel + enforcement).
	// Uppercase only: microagent rejects lowercase env keys, and terminate-
	// mode credential injection uses BaseURL, not the proxy env, anyway.
	Proxy bool
	// BaseURL points per-SDK base-URL envs at the broker for terminate-mode
	// credential injection, e.g. {"ANTHROPIC_BASE_URL": ""}. An empty value is
	// filled with the guest listen URL; a non-empty value is used verbatim
	// (e.g. to add a path suffix the SDK expects).
	BaseURL map[string]string
}

// ListenerTarget marks a vsock listener the supervisor serves the egress
// broker on, rather than forwarding to a TCP target or writing a result file.
// The canonical constant lives in vmkit so the raw request surface can gate
// on it without importing this package.
const ListenerTarget = vmkit.BrokerListenerTarget

// vsockListenersEnv is the guest env var the guestinit bridge reads; it must
// match cmd/microagent-guestinit (MICROAGENT_VSOCK_TCP_LISTENERS), format
// "listen=vsockPort[,listen=vsockPort...]".
const vsockListenersEnv = "MICROAGENT_VSOCK_TCP_LISTENERS"

// GuestEnv returns the environment the guest needs so its egress goes through
// the broker automatically: the vsock bridge listener, optional proxy envs,
// and per-SDK base URLs. Deterministic (sorted) so callers and tests get a
// stable result.
func (c GuestConfig) GuestEnv() (map[string]string, error) {
	if strings.TrimSpace(c.GuestListen) == "" {
		return nil, fmt.Errorf("broker guest config: empty GuestListen")
	}
	if c.VsockPort == 0 {
		return nil, fmt.Errorf("broker guest config: zero VsockPort")
	}
	url := "http://" + c.GuestListen
	env := map[string]string{
		vsockListenersEnv: fmt.Sprintf("%s=%d", c.GuestListen, c.VsockPort),
	}
	if c.Proxy {
		env["HTTPS_PROXY"] = url
		env["HTTP_PROXY"] = url
	}
	for k, v := range c.BaseURL {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("broker guest config: empty BaseURL key")
		}
		if v == "" {
			v = url
		}
		env[k] = v
	}
	return env, nil
}

// MergeGuestEnv applies GuestEnv onto an existing env slice ("K=V" entries),
// overriding any key the broker owns so the workload cannot pre-set a proxy
// or base URL to escape mediation. Existing vsock bridge entries are merged,
// not replaced: they only function if the host serves that vsock port, so
// preserving them cannot widen access. Returned sorted for determinism.
func (c GuestConfig) MergeGuestEnv(existing []string) ([]string, error) {
	add, err := c.GuestEnv()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(existing)+len(add))
	for _, kv := range existing {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			if k == vsockListenersEnv {
				add[k] = mergeVsockListenerSpecs(v, add[k])
				continue
			}
			if _, owned := add[k]; owned {
				continue // broker-owned key: replace, never inherit
			}
		}
		out = append(out, kv)
	}
	for k, v := range add {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out, nil
}

// MergeGuestEnvMap is MergeGuestEnv for a key→value env map (the shape rootfs
// build requests use). It returns a new map; the input is not mutated.
func (c GuestConfig) MergeGuestEnvMap(existing map[string]string) (map[string]string, error) {
	add, err := c.GuestEnv()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(existing)+len(add))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range add {
		if k == vsockListenersEnv {
			out[k] = mergeVsockListenerSpecs(existing[k], v)
			continue
		}
		out[k] = v
	}
	return out, nil
}

// mergeVsockListenerSpecs joins two comma-separated bridge spec lists,
// preserving order and dropping duplicate entries.
func mergeVsockListenerSpecs(existing, added string) string {
	seen := map[string]bool{}
	var entries []string
	for _, e := range strings.Split(existing+","+added, ",") {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		entries = append(entries, e)
	}
	return strings.Join(entries, ",")
}
