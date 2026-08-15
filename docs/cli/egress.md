---
title: microagent egress
description: Show or stream the egress mediator's audit decisions for a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent egress <name> [--follow] [--state-dir <dir>]
```

`egress` answers one question about a workspace: what did it try to reach on
the network, and what happened to each attempt?

```bash
microagent egress research
```

```text
2026-07-10T14:02:11Z  egress_allow  api.github.com  140.82.121.6:443
2026-07-10T14:02:12Z  broker_request_allow  api.anthropic.com
2026-07-10T14:02:14Z  egress_deny  evil.example  not allowlisted
2026-07-10T14:02:15Z  egress_dns_deny  -  blocked
```

Each line is one decision, oldest first, written by the egress mediator — the
host-side process that rules on the guest's network traffic. Egress mediation
is on by default (mode `broker`; the other modes are `mitm` and `off`), so
every workspace whose mediator has made a decision has this record. When the
workspace has an
[egress broker](/concepts/egress-mediation/#the-broker-decision-stream)
configured, the broker's per-request decision records are merged into the same
time-ordered view.

Follow mode may show one delayed connection indicator on stderr. It stops
before recorded or new decisions are written and never mixes spinner frames
into the audit stream.

The record types you'll see most:

- `egress_allow` / `egress_deny` — a connection was allowed or denied
- `egress_dns_allow` / `egress_dns_deny` — a name-resolution ruling
- `broker_request_allow` / `broker_request_deny` — one brokered request:
  verdict plus minimized metadata (method, host, status, byte counts, timing,
  and the names — never the values — of the credential references used)
- `egress_mitm_*` — TLS-interception records (`mitm` mode only)
- UDP, listen, cap, swap, and loop-guard records the mediator emits as it runs

The vocabulary is intentionally open-ended — `egress` prints whatever was
recorded, including record types and fields added after this page was written;
see [egress mediation](/concepts/egress-mediation/) for the full taxonomy. An
absent audit log is not an error: it means no decision has been recorded
yet (or mediation is `off`), and `egress` reports an empty list.

The audit log is a separate stream from lifecycle [`events`](/cli/events/).
`events` shows how the workspace got to its current state; `egress` shows what
it tried to reach on the network and how each attempt was ruled on.

By default `egress` prints the recorded decisions once. With `--follow` (`-f`)
it prints them and then streams new decisions as the workspace makes them,
returning when the workspace reaches a terminal lifecycle state (`halted`,
`stopped`, or `failed`) or you interrupt with Ctrl-C. With the global `--json`
flag the decisions are returned once as an array under `egress`; `--follow` is
not supported with JSON output.

## Examples

Get the decisions as JSON:

```bash
microagent --json egress research
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

See [global flags](/cli/#global-flags) for `--output`/`--json`.

## Exit status

`egress` exits `0` when the workspace record is found and read — including when
the audit log is absent (an empty list). It exits nonzero when the workspace
name is invalid or `--follow` is combined with JSON output.

## Related

- [Egress mediation](/concepts/egress-mediation/) - the concepts: modes, the MITM CA, UDP/DNS, allow vs passthrough
- [Allowlist and passthrough how-to](/guides/egress-allowlist/) - the flags and the policy file
- [`events`](/cli/events/) - the lifecycle event history
- [`status`](/cli/status/) - the current state and readiness
- [`logs`](/cli/logs/) - serial console output
