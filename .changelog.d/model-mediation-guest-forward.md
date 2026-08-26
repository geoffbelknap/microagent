### Model mediation is no longer bypassed by the guest forward

A workspace paired to a model with mediation on (`MICROAGENT_MODEL_MEDIATION`
set to `local-allow` or `policy`) started a host-worker mediator, and then sent
guest traffic straight past it. The guest-facing model forward re-resolves the
paired runner on every connection so a runner restart cannot strand the
workspace; under mediation the forward's target is the mediator, not the
runner, so that resolution dialed the runner directly. Every mediated request
reached the model with no decision evaluated and no audit record written — a
`policy` deny never saw the request it was meant to refuse. The mediator's
audit log showed only `mediator_start` and `mediator_stop`, and the guest saw
ordinary successful responses, so nothing surfaced as an error.

The forward now stays pinned to the mediator whenever one is in the path, and
the mediator resolves the current runner for its model before each proxied
request. Runner restarts are absorbed there instead, so paired workspaces keep
surviving a restart with mediation on or off. A resolved move is recorded as
`upstream_target_changed` in the mediation audit log.

This affected both supported backends, since each resolves the forward the
same way.
