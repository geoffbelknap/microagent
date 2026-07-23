# Migration notes

Breaking changes by release. Written for downstream consumers
(microagency, microplane, scripts driving the `microagent` CLI).

## Unreleased

### Summary

| Old                                                                       | New                                                                                          |
| -------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `image delete --delete` / `image prune --delete`                          | Use `--purge` instead. Old spelling fails with the subcommand's usage error naming `--purge`.  |
| `--text` / `--human` (global flags)                                       | Removed. Use `--output text`. Leading placement errors `unknown command "--text"` (points at `help all`); after a subcommand, the unknown-flag error points at `--help`. |
| `--output human`                                                          | Removed. Use `--output text`.                                                                 |
| `--mode human\|agent\|text\|json` (synonyms)                              | Removed. `--mode` only accepts `ux` or `ax`.                                                   |
| `MICROAGENT_OUTPUT=human`                                                 | Removed. `MICROAGENT_OUTPUT` only accepts `json` or `text`.                                    |
| `<cmd> --json <path\|->` / `-json <path\|->` (request-input alias)        | Removed. Use `--request-json <path\|->`; a following `--json` is always the global output flag.|
| AX success: bare object on stdout                                        | AX success: `{"ok": true, "result": {...}}` envelope on stdout.                                |
| AX error: `{"ok": false, "error": {...}}` on stderr                       | AX error: same envelope, now on stdout; exit code still signals failure.                       |
| `--mode ax` always forced JSON                                           | `--mode ax` defaults to JSON but is overridable: explicit `--output text`/`MICROAGENT_OUTPUT=text` wins over the AX default. |
| AX envelope emitted regardless of effective format                       | AX envelope (success and error) is emitted only when the effective format is JSON; `--mode ax --output text` renders plain text with AX exit semantics. |
| `start --wait` under AX: boot envelope, then wait envelope (two documents) | `start --wait` under AX: only the final wait-outcome envelope (one document). UX/`--json` still write both. |
| MCP success: `.timing_ms`/`.principal_context`/`.idempotency_replay`/`.retry_*`/`.metadata` beside `.result` | Moved under a sibling `.meta` block; `.metadata` (exec) is gone, folded into `.meta`. Every response gains `.ok`. |
| MCP error: custom `mcpStructuredError` shape in `error.data`             | Plain `structuredError` shape (same field names) plus a sibling `.meta` block in `error.data`. |
| `microagent.describe` manifest `correlation_id_key: "error.correlation_id"` | `correlation_id_key: "error.data.correlation_id"` (per-operation, in the manifest).            |
| `microagent.describe` MCP response: bare manifest object                 | Same unified `{ok: true, result: <manifest>, meta: {timing_ms, principal_context}}` envelope as every other tool; the manifest moves under `.result`. |
| Bare `context.DeadlineExceeded` (no wrapping timeout/retry type): `kind: "permanent"`, `retryable: false` | `kind: "transient"`, `retryable: true`, `retry_after_ms: 1000`. |
| `stop` (standalone verb: SIGTERM, ~5s graceful window, records `stopped` on clean exit) | `stop` is now an alias of `halt` and behaves identically: same graceful shutdown, but a clean exit now records `halted` instead of `stopped`. There is no separate stop page. |
| `halt` graceful window | Unchanged: a fixed backend graceful window (~5s); the guest is asked to exit and `halt` returns an error without escalating if it does not. A configurable timeout is planned as a library feature. |
| MCP `workspace.stop` tool | Removed. Call `workspace.halt` instead — identical semantics (same graceful shutdown; a clean exit records `halted`). Calling `workspace.stop` now returns a JSON-RPC tool-call error (`kind: "unsupported"`) instead of running the alias. |

The sections below give the full detail for each row, ordered flags → CLI-AX
→ MCP. The checklists after that translate the table into concrete follow-up
work for microagency and microplane.

### `image delete` and `image prune` flag `--delete` renamed to `--purge`

The flag `--delete` on both `image delete` and `image prune` subcommands has
been renamed to `--purge` to eliminate the design defect of a self-shadowing
name: `--delete` on the `delete` command was confusing (what does "delete" mean
within a delete operation?).

**Migration:** replace `--delete` with `--purge` on both subcommands:

```bash
# Old
microagent image delete <image> --delete
microagent image prune --delete --yes

# New
microagent image delete <image> --purge
microagent image prune --purge --yes
```

The old spelling now fails with the subcommand's usage error, which names the
current `--purge` flag. The MCP tools `images.delete`/`images.prune` keep
their `delete_files` argument; only the CLI flag spelling changed.

### Output format flags consolidated to `--output`

- `microagent --text <cmd>` / `--human` → `microagent --output text <cmd>`.
  The `--text`/`--human` global flags are removed. In the leading position
  (the old global placement) the CLI reports `unknown command "--text"` and
  points at `microagent help all`; after a subcommand it reports an
  unknown-flag error pointing at that command's `--help`.
- `--json` remains as the alias for `--output json`.
- `MICROAGENT_OUTPUT` accepts `json` or `text` only (`human` removed).
- `MICROAGENT_MODE` accepts `ux` or `ax` only (`human`, `agent`, `text`,
  `json` synonyms removed).
- `--output human` removed; use `--output text`.
- Precedence is now: explicit format flag > `MICROAGENT_OUTPUT` >
  (`--mode ax` defaults to json) > TTY detection. In particular
  `--mode ax --output text` now renders text; previously AX forced JSON.

### Request-JSON alias removed

- `microagent <cmd> --json <path|->` and `-json <path|->` (the
  `--request-json` compat alias on create/start and the lifecycle verbs) are
  removed. Use `--request-json <path|->`.
- A post-command `--json` is now always the global output-format flag, on
  every command.

**Migration hazard.** After this change a bare token following the removed
alias parses as a workspace name/ID, not a request path. Invocations whose
path ends in `.json` fail loudly (the CLI refuses to treat `--json` as
global there). The bare stdin marker (`--json -`) also fails loudly. Only a
suffix-less file token (neither `-` nor ending in `.json`) remains
indistinguishable from the legitimate `status --json <workspace>` output
form — in particular `delete --json <token> --yes` in an unattended script
will delete a workspace named `<token>` if one exists. Audit scripts for the
old alias before upgrading; `grep -rn -- '--json' | grep -v 'microagent --json'`
finds the risky shapes.

### AX output is one envelope on stdout

- In `--mode ax` (and over MCP), every response is now a single JSON
  envelope on stdout: `{"ok": true, "result": {...}}` on success,
  `{"ok": false, "error": {...}}` on failure. Previously success bodies
  were bare objects and error envelopes went to stderr.
- Parse stdout only; the exit code answers "did microagent itself work".
  Guest/workload outcomes ride inside the envelope, unchanged.
- Plain `--json` (UX profile) output is NOT wrapped; only the AX profile
  changes.
- Exactly one envelope per invocation. On failure the `{ok:false, error}`
  envelope is the only document: commands that previously printed a partial
  result before the error (`run`, `dispatch`, `create`, `start`, `rootfs
  build`) now suppress that result under AX. Guest/workload outcomes that
  ride inside a successful envelope (a nonzero guest exit under `run`/`exec`)
  are unchanged.
- `start --wait` under AX now emits ONLY the final wait-outcome envelope; the
  intermediate boot/create envelope is suppressed so the stream stays one
  document. UX/plain `--json` still writes the boot result followed by the
  wait result (two documents) — decode it as a stream, or run `wait` as its
  own command.
- `--mode ax --output text` renders human output with AX exit semantics: the
  envelope (success and error) is emitted only when the effective format is
  JSON (the default under AX). With `--output text`, success renders as text
  and a failure prints the plain error to stderr, but the process still exits
  nonzero.

### MCP tool responses use the unified `{ok, result, meta}` envelope

**Breaking for MCP clients.** The MCP tool payload now matches the CLI-AX
envelope: transport concerns move under a `meta` block instead of living
beside `result`, and every response carries an `ok` discriminator. Gateways
(microagency) parsing tool output must repoint their field access.

Success — the JSON object inside the tool-call `result.content[].text`:

| Old field                    | New field                       |
| ---------------------------- | -------------------------------- |
| `.result`                    | `.result` (unchanged)           |
| `.timing_ms`                 | `.meta.timing_ms`               |
| `.principal_context`         | `.meta.principal_context`       |
| `.idempotency_replay`        | `.meta.idempotency_replay`      |
| `.retry_count` (exec)        | `.meta.retry_count`             |
| `.retry_wall_clock_ms` (exec)| `.meta.retry_wall_clock_ms`     |
| `.retry_exhausted` (exec)    | `.meta.retry_exhausted`         |
| `.metadata` (exec, nested)   | removed (folded into `.meta`)   |
| (new)                        | `.ok` = `true`                  |

The exec `metadata: {retry_count, retry_wall_clock_ms}` sub-object is gone;
read those values from `.meta`.

Error — the JSON-RPC `error.data` object (unchanged transport: failures are
still delivered as a JSON-RPC error, not a tool payload):

| Old `error.data` field                              | New `error.data` field           |
| ---------------------------------------------------- | --------------------------------- |
| `{kind, message, remediation, retryable, correlation_id, retry_after_ms, partial_output}` (custom `mcpStructuredError` shape) | same fields, now the plain `structuredError` shape (identical keys) |
| `.retry_count` / `.retry_wall_clock_ms` / `.retry_exhausted` (exec, top-level of `error.data`) | `.meta.retry_count` / `.meta.retry_wall_clock_ms` / `.meta.retry_exhausted` |
| (new)                                               | `.meta.timing_ms`, `.meta.principal_context` |

`error.data` gains the same `meta` transport block as success responses. The
`kind`, `message`, `remediation`, `retryable`, and `correlation_id` fields are
unchanged and stay at the top of `error.data`; the correlation id is at
`error.data.correlation_id`. This is itself a breaking move: the
`microagent.describe` manifest's per-operation `correlation_id_key` field
used to read `error.correlation_id` and now reads `error.data.correlation_id`.

The `microagent.describe` manifest's per-operation `output_schema` now
describes `{ok, result, meta}`, and a top-level `response_envelope` documents
both the success payload and the `error.data` shape.

**`microagent.describe` itself is now enveloped too.** Previously
`microagent.describe` was the one tool that returned the bare manifest object
as its tool-call payload — every other tool already returned `{ok, result,
meta}`. It now matches: the manifest moves under `.result`, alongside a
`.meta` block (`timing_ms`, `principal_context`), and the response gains
`.ok: true`. Read `schema_version`, `service`, `operations`, and the other
manifest fields from `.result`, not from the top level of the tool payload.

### Bare `context.DeadlineExceeded` is now transient/retryable

A CLI/MCP error whose root cause is a bare `context.DeadlineExceeded` (no
wrapping `WaitTimeoutError`, `ExecRetryExhaustedError`, or other typed
timeout) previously fell through to the classifier's default: `kind:
"permanent"`, `retryable: false`. It is now classified `kind: "transient"`,
`retryable: true`, `retry_after_ms: 1000`, matching the other timeout-shaped
error types the classifier already treats as retryable. Gateways (microagency)
that branch on `retryable` to decide whether to retry a call should account
for this: a request that previously surfaced as a non-retryable deadline
error may now come back marked retryable.

### `stop` is now an alias of `halt`

The CLI has one graceful-shutdown verb: `halt`. `stop` is retained as a pure
alias of `halt` and produces identical behavior, so existing `microagent stop
<name>` invocations keep working unchanged.

What actually changes for a caller: the standalone `stop` verb and `halt`
already shared the same mechanism (SIGTERM to the guest, a fixed backend
graceful window of roughly five seconds, and an error returned without
escalation if the guest does not exit). They differed only in the state
recorded on a clean exit — `stop` recorded `stopped`, `halt` records `halted`.
Now that `stop` routes through `halt`, **a clean `stop <name>` records the
`halted` state instead of `stopped`.** For a hard termination when the guest
does not exit, follow up with `kill` (which still records `stopped`). The
`stopped` state itself is unchanged and still produced by other paths (for
example `kill` and `delete`).

There is no `docs/cli/stop.md` page anymore; the guidance lives in
`docs/cli/halt.md`, and `stop` renders as an alias in `microagent help`.

The graceful window remains a fixed backend value (~5s) and is not yet
configurable from the CLI. A configurable shutdown timeout is planned as a
microagent library feature (plumbing a grace duration through the supervisor
control path); until it lands, `halt`/`stop` use the fixed window.

### MCP `workspace.stop` is removed; call `workspace.halt`

This is the MCP-surface counterpart to the CLI change above, and it is a
breaking change for MCP clients (unlike the CLI `stop` alias, which keeps
working): the `workspace.stop` tool is gone from `tools/list` and from the
`microagent.describe` capability manifest's `operations`. Call
`workspace.halt` instead — same arguments (`name`, `state_dir`), same
graceful-shutdown mechanism, and a clean exit records `halted`, exactly as
`workspace.halt` already did.

Calling `workspace.stop` over MCP now returns a JSON-RPC tool-call error
(`error.code: -32602`) whose `error.data` is a standard `structuredError`
(`kind: "unsupported"`, a fixed remediation string, `retryable: false`, and a
`correlation_id`) — the generic unknown-tool path, not a special case built
for this removal. That error `data` does **not** carry the usual sibling
`meta` block (`timing_ms`, `principal_context`): argument-validation-time
tool-call errors (an unknown tool name, or a missing required argument for a
known tool) return before the MCP layer ever computes `meta`, so they are
bare `structuredError` objects. This gap predates this change and applies to
every tool at the argument-validation stage, not just `workspace.stop`.

### Checklist for microagency

microagency consumes microagent through its public Go library surface
(`pkg/workspace`, `pkg/diagnostics`, `pkg/sandbox`), behind its sandbox seam.
It does not connect to `microagent serve` — its upstream MCP connections are
HTTP, and `serve mcp` is stdio — and it does not shell out to the CLI. An
audit at the b16d6b2 upgrade confirmed none of the items below applied: no
envelope-field parsing, no `workspace.stop` references, no CLI invocations
anywhere in that repo, including tests and fixtures. The checklist is kept as
the contract points for any MCP-gateway consumer, so a future MCP or CLI
coupling starts from the right list. Before upgrading the vendored/pinned
`microagent` version:

- [ ] Update MCP success-response field access: read `timing_ms`,
      `principal_context`, `idempotency_replay`, and (for `workspace.exec`)
      `retry_count`/`retry_wall_clock_ms`/`retry_exhausted` from `.meta`, not
      beside `.result`. Drop any code that reads the old nested
      `.metadata.retry_count`/`.metadata.retry_wall_clock_ms` — that
      sub-object no longer exists.
- [ ] Update MCP error-response field access: `error.data` keeps `kind`,
      `message`, `remediation`, `retryable`, `retry_after_ms`,
      `partial_output`, and `correlation_id` at the same top-level keys, but
      the same transport `meta` block (`timing_ms`, `principal_context`, and
      for exec the retry fields) is now a sibling of those fields under
      `error.data.meta`.
- [ ] Read `correlation_id_key` from the `microagent.describe` manifest at
      startup (or on version bump) rather than hardcoding a path. **Do not
      hardcode `error.data.correlation_id`** — the manifest's
      `correlation_id_key` is the contract, and it has already moved once in
      this branch (`error.correlation_id` → `error.data.correlation_id`);
      a hardcoded path will silently break again on the next transport
      change.
- [ ] Every response now carries `.ok` — safe to ignore, but available as a
      cheaper discriminator than "did this arrive as a JSON-RPC error".
- [ ] Update `microagent.describe` field access: the manifest (`schema_version`,
      `service`, `operations`, etc.) now lives under `.result`, not at the top
      level of the tool payload, matching every other tool's `{ok, result,
      meta}` shape.
- [ ] If the gateway branches on `retryable` to decide whether to retry a
      failed call, note that a bare deadline-exceeded error now reports
      `retryable: true` (previously `false`) — see "Bare `context.
      DeadlineExceeded` is now transient/retryable" above.
- [ ] If the gateway or its setup scripts invoke the `microagent` CLI
      directly (not just over MCP) — for example in `doctor`/`up` health
      checks or install scripts — audit those invocations for the removed
      flag spellings: `--text`/`--human`, `--output human`, any `--mode`
      value other than `ux`/`ax`, `MICROAGENT_OUTPUT=human`, and the bare
      `-json`/`--json <path>` request-input alias on
      create/start/status/halt/stop/kill/pause/resume/quarantine/delete/result.
      None were found in this repo's own `cmd/`, `scripts/`, or `docs/` at
      this commit, and the b16d6b2 upgrade audit of microagency found no
      microagent CLI invocations in that repo either.
- [ ] `workspace.stop` removed from the MCP tool surface; call
      `workspace.halt` instead (identical semantics; a clean shutdown records
      `halted`). Update any tool-name literals, `tools/list`/manifest
      allowlists, or dispatch tables that reference `workspace.stop`.

### Checklist for microplane

microplane consumes microagent through its public Go library surface
(`pkg/*`), not the CLI or MCP transport, so CLI/MCP breaking changes in this
branch are not automatically its concern.

- [x] Library surface: the output-mode/envelope work touched only
      `cmd/microagent/*` (CLI and MCP adapter code), `docs/`, and
      `scripts/dev/*`. The lifecycle-verb work additionally removed the
      `workspace.stop` entry from `vmkit.FeatureContracts().MCPTools` (with
      its test line) — descriptive contract metadata only; no behavior,
      signature, or state-vocabulary change anywhere in `pkg/`. **No
      functional library change — CLI, MCP, and contract-metadata readers
      are the affected consumers.** No action needed in
      `internal/controlplane`, `cmd/planed`, or `cmd/plane` beyond noting
      the `MCPTools` list no longer includes `workspace.stop`.
- [ ] If `cmd/plane` or `planed` shell out to the `microagent` CLI anywhere
      (rather than importing `pkg/workspace` etc. directly), audit those
      invocations for the same removed flag spellings listed in the
      microagency checklist above — that would be a CLI dependency hiding
      inside a Go-library consumer, not a library dependency, and would need
      the same flag-spelling fixes as any other CLI caller.
