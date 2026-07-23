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
