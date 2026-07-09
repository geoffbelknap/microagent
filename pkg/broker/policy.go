package broker

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
