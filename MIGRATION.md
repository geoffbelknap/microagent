# Migration notes

Breaking changes by release. Written for downstream consumers
(microagency, microplane, scripts driving the `microagent` CLI).

## Unreleased

### Output format flags consolidated to `--output`

- `microagent --text <cmd>` / `--human` → `microagent --output text <cmd>`.
  The `--text`/`--human` global flags are removed; unknown-flag errors point
  at `--help`.
- `--json` remains as the alias for `--output json`.
- `MICROAGENT_OUTPUT` accepts `json` or `text` only (`human` removed).
- `MICROAGENT_MODE` accepts `ux` or `ax` only (`human`, `agent`, `text`,
  `json` synonyms removed).
- `--output human` removed; use `--output text`.
- Precedence is now: explicit format flag > `MICROAGENT_OUTPUT` >
  (`--mode ax` defaults to json) > TTY detection. In particular
  `--mode ax --output text` now renders text; previously AX forced JSON.

### Request-JSON alias removed

- `microagent <cmd> --json <path|- >` and `-json <path|- >` (the
  `--request-json` compat alias on create/start and the lifecycle verbs) are
  removed. Use `--request-json <path|- >`.
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
