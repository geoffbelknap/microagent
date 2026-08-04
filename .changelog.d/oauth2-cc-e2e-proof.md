### Prove the oauth2-cc credential-swap strategy end to end

`oauth2-cc` had acquisition unit-tested and injection proven only for the
`static` strategy through the real mediator — nothing established that a
guest request could traverse the complete oauth2-cc path (token exchange,
injection, caching) through a running mediator, or that it failed closed
correctly.

`internal/egress` now proves the full data path in-process against a
hermetic token endpoint: acquisition, header injection, cache reuse across a
second request, and three fail-closed cases (an unreachable token endpoint,
a token response missing `access_token`, and a token minted already within
the cache's expiry skew window, which must never be served twice). A new
`cred-swap-oauth2` Linux/KVM E2E scenario proves the CLI/lifecycle/mediator
wiring for a hand-authored oauth2-cc entry (there is no `--cred-swap`
provider shorthand for this strategy) boots correctly under `mitm` egress.
