### Every workspace is bounded by default, not just event retention

ASK tenet 8 (`operations-bounded`) requires every operational dimension to
have a limit that holds by default. Only event retention did; a persistent
workspace's idle TTL defaulted to permanent, egress byte/rate/concurrency
caps existed in the mediator but had no operator-facing surface to set them
at all, and nothing capped how many workspaces a host could run at once.

- **Idle TTL** now defaults to 7 days for a workspace created without
  `--ttl` (`create`/`create --from-snapshot`), instead of permanent.
  `--ttl 0` still means permanent — the default only fills in when the
  operator pinned nothing, including a genuine `0`.
- **Egress caps** (`--egress-max-total-bytes`, new; `--egress-max-conns`,
  new) now default to 50 GiB cumulative / 256 concurrent connections under
  `broker` or `mitm` mediation, instead of unlimited. `0` still means
  unlimited when set explicitly. A cap resolved at create time is fixed for
  that workspace's lifetime and round-trips through every later `start`
  unchanged.
- **Workspace count** is now capped host-wide: `create`, `create
  --from-snapshot`, and `start` fail closed once the number of
  running/starting/paused workspaces reaches the ceiling, computed from
  detected host memory (clamped to 4-100) or set explicitly with
  `MICROAGENT_MAX_WORKSPACES=<n>`.
- `microagent inspect`/`microagent status` report every bound actually in
  force under a new `boundedOperations` field, so an operator can see what's
  applied without reading defaults out of the source.

A workspace created before this change keeps its existing (unbounded)
behavior when restarted — these defaults only apply to newly created
workspaces, never retroactively.
