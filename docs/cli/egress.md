---
title: microagent egress
description: Show or stream the egress mediator's audit decisions for a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-09_

```text
microagent egress <name> [--follow] [--state-dir <dir>]
```

`egress` shows a workspace's egress decisions, oldest first — the mediator's
connection-level audit log and, when the workspace has an
[egress broker](/concepts/egress-mediation/#the-broker-decision-stream)
configured, the broker's per-request decision records, merged into one
time-ordered view. Egress mediation is **on by default** (mode `guarded`; the
other modes are `broker`, `strict`, and `off`), so every workspace whose
mediator has made a decision has this record. Each line is one decision:
`egress_allow` / `egress_deny` for connections, the `egress_mitm_*` records
for TLS interception, `egress_dns_allow` / `egress_dns_deny` for name
resolution, the UDP, listen, cap, swap, and loop-guard records the mediator
emits as it runs, and `broker_request_allow` / `broker_request_deny` for
brokered requests (verdict plus minimized metadata — method, host, status,
byte counts, timing, and the names of the credential references used). The
audit log is a separate stream from lifecycle [`events`](/cli/events/):
`events` shows how the workspace got to its current state, `egress` shows what
it tried to reach on the network and how each attempt was ruled on.

The vocabulary of event types is intentionally open-ended — `egress` prints
whatever was recorded, including event types and fields added after this page
was written. An absent audit log is not an error: it simply means no decision
has been recorded yet (or mediation is `off`), and `egress` reports an empty
list.

By default `egress` prints the recorded decisions once. With `--follow` (`-f`)
it prints them and then streams new decisions as the workspace makes them,
returning when the workspace reaches a terminal lifecycle state (`halted`,
`stopped`, or `failed`) or you interrupt with Ctrl-C. With the global `--json`
flag the decisions are returned once as an array under `egress`; `--follow` is
not supported with JSON/AX output.

## Examples

Show the recorded decisions:

```bash
microagent egress research
microagent --json egress research
```

```text
2026-06-16T00:00:01Z  egress_allow  api.github.com  140.82.0.1:443
2026-06-16T00:00:02Z  egress_deny  evil.example  not allowlisted
2026-06-16T00:00:03Z  egress_dns_deny  -  blocked
```

Follow a workspace's egress decisions live:

```bash
microagent egress research --follow
```

## Flags

| Flag | Description |
|---|---|
| `--follow`, `-f` | Stream new decisions until the workspace reaches a terminal state or you interrupt |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`egress` exits `0` when the workspace record is found and read — including when
the audit log is absent (an empty list) — and nonzero when the workspace name is
invalid or `--follow` is combined with JSON/AX output. In AX mode a failure is
written as a structured error envelope.

## Related

- [Egress mediation](/concepts/egress-mediation/) - the concepts: modes, the MITM CA, UDP/DNS, allow vs passthrough
- [Allowlist and passthrough how-to](/guides/egress-allowlist/) - the flags and the policy file
- [`events`](/cli/events/) - the lifecycle event history
- [`status`](/cli/status/) - the current state and readiness
- [`logs`](/cli/logs/) - serial console output
