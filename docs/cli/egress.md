---
title: microagent egress
description: Show or stream the egress mediator's audit decisions for a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-17_

```text
microagent egress <name> [--follow] [--state-dir <dir>]
```

`egress` surfaces the egress mediator's audit log for a workspace, oldest first.
Egress mediation is **on by default** (mode `mediated`; the other modes are
`strict` and `off`), so every workspace whose mediator has made a decision has
this record. Each line is one decision the mediator made: `egress_allow` /
`egress_deny` for connections, the `egress_mitm_*` records for TLS
interception, `egress_dns_allow` / `egress_dns_deny` for name resolution, and
the UDP, listen, cap, swap, and loop-guard records the mediator emits as it
runs. The audit log is a separate stream from lifecycle
[`events`](/cli/events/): `events` shows how the workspace got to its current
state, `egress` shows what it tried to reach on the network and how the mediator
ruled on each attempt.

The vocabulary of event types is intentionally open-ended — `egress` prints
whatever the mediator recorded, including event types and fields added after
this page was written. An absent audit log is not an error: it simply means the
mediator has not recorded a decision yet (or mediation is `off`), and `egress`
reports an empty list.

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
