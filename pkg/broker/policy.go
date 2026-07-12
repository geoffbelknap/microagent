package broker

import "net"

// Verdict is a policy's answer about one request: allow or deny, the rule
// that decided it, and optional classification labels. It is the ONLY thing
// that leaves the policy evaluation — the evaluator runs inside the broker
// trust boundary and sees the pre-swap request, but content never crosses
// out; the verdict annotates the decision stream instead.
type Verdict struct {
	Allow  bool
	Rule   string
	Labels []string
}

// Policy evaluates a pre-swap request inside the broker trust boundary. The
// broker ships the seam, not a policy: a nil Policy allows everything
// (mechanism, not policy).
type Policy func(TapRecord) (Verdict, error)

// AllowlistPolicy builds a Policy that permits only the given upstream hosts and
// denies every other tunnel. An entry matches the CONNECT target either exactly
// ("host:port") or by host alone (the port stripped), so an operator can
// allowlist "api.example.com" without pinning a port. An empty allowlist yields
// a nil Policy — no host restriction — leaving the guarded dialer's inside-deny
// as the only constraint; the caller opts into locking by supplying hosts. It is
// the seam a caller threads an operator-provided per-endpoint CONNECT allowlist
// through; the broker enforces it but does not decide its contents.
func AllowlistPolicy(hosts []string) Policy {
	if len(hosts) == 0 {
		return nil
	}
	set := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		set[h] = true
	}
	return func(tap TapRecord) (Verdict, error) {
		if set[tap.Host] {
			return Verdict{Allow: true, Rule: "allowlist"}, nil
		}
		if host, _, err := net.SplitHostPort(tap.Host); err == nil && set[host] {
			return Verdict{Allow: true, Rule: "allowlist"}, nil
		}
		return Verdict{Allow: false, Rule: "allowlist"}, nil
	}
}

// evaluate runs a policy fail-closed: an error or a panic denies — an
// evaluation failure can never widen what the workload can reach.
func evaluate(p Policy, tap TapRecord) (v Verdict) {
	if p == nil {
		return Verdict{Allow: true}
	}
	defer func() {
		if recover() != nil {
			v = Verdict{Allow: false, Rule: "policy-error"}
		}
	}()
	verdict, err := p(tap)
	if err != nil {
		return Verdict{Allow: false, Rule: "policy-error"}
	}
	if !verdict.Allow && verdict.Rule == "" {
		verdict.Rule = "policy"
	}
	return verdict
}
