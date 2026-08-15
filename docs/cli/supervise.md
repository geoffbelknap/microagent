---
title: microagent supervise
description: Start and restart a workspace according to its restart policy.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent supervise <name> [--state-dir <dir>] [--max-restarts <n>]
```

`supervise` is a foreground host supervisor. It starts a workspace and keeps
watching it while the command is running. When the workspace reaches a terminal
state, the persisted restart policy decides whether `supervise` starts it
again.

Human output acknowledges the initial supervised workspace readiness, then
removes the progress indicator and lets the long-lived supervisor wait without
a perpetual spinner. Startup failure and retry phases remain distinguishable.
JSON and MCP results contain no terminal presentation.

## Examples

Create a workspace with a restart policy, then supervise it:

```bash
microagent create research --restart always
microagent supervise research
```

Stop the foreground `supervise` process to stop restart supervision.

## Restart policies

| Policy | Behavior |
|---|---|
| `never` | Do not start under `supervise` |
| `on-failure` | Restart only after a `failed` state |
| `always` | Restart after `stopped` or `failed` |

The policy comes from `microagent create --restart ...` or `restart:` in
`microagent.yaml`.

## Health checks

By default `supervise` only restarts a workspace that exits - it cannot tell an
alive-but-wedged guest from a healthy one. Declare a [`health`](/cli/spec/)
probe in `microagent.yaml` to close that gap:

```yaml
restart: on-failure
health:
  exec: ["python3", "-c", "import socket; socket.create_connection(('127.0.0.1', 8080), 1)"]
  intervalSeconds: 15
  retries: 3
  startPeriodSeconds: 10
```

While the workspace runs, `supervise` probes it every `intervalSeconds` (after a
`startPeriodSeconds` grace). After `retries` consecutive failures the workspace
is force-killed and the restart policy restarts it - so health-based restart
requires `on-failure` or `always`. Probe forms:

- `exec` - a command run in the guest via structured exec when the selected
  backend exposes `execReady`; healthy on exit 0.
- `httpGet` + `port` - a host-side GET against a published guest port; healthy
  on a non-error status.

An unhealthy probe returns a `failed` state in the supervise result, so the
restart accounting (and `--max-restarts`) applies the same as an exit failure.

## Survive host reboot

`supervise` runs in the foreground and does not survive a host reboot. To keep a
long-running workspace alive across reboots without microagent adding a
persistent daemon, install an OS init unit that runs `supervise` at boot:

```bash
microagent supervise research --install     # write + register a boot unit
microagent supervise research --uninstall   # remove it
```

`--install` writes a **systemd user unit** (`~/.config/systemd/user/microagent-supervise-<name>.service`)
on Linux or a **launchd agent** (`~/Library/LaunchAgents/com.microagent.supervise.<name>.plist`)
on macOS, then registers it for boot. The OS init - not microagent - supervises
the workspace, so the no-daemon model is preserved. The workspace must already
exist. If automatic registration can't run (for example, no active user init
session), the unit file is still written and the CLI prints the manual enable
command.

## Flags

Common flags:

- `--max-restarts <n>` - cap a crash-looping workspace instead of restarting it
  forever (the default, `0`, is unlimited)
- `--interval <seconds>` - trade exit-detection latency against polling overhead
- `--install` - hand supervision to the OS init so it survives a host reboot
- `--uninstall` - remove that boot unit when you retire the workspace
- `--state-dir <dir>` - supervise a workspace recorded under a non-default
  state directory

The complete set:

| Flag | Description |
|---|---|
| `--install` | Write and register a boot unit that supervises the workspace, then exit |
| `--uninstall` | Remove the installed boot unit, then exit |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--backend <name>` | Backend identity override |
| `--arch <arch>` | Guest architecture |
| `--kernel <path>` | Kernel path |
| `--interval <seconds>` | Seconds between state checks |
| `--max-restarts <n>` | Maximum restarts; `0` means unlimited |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`supervise` exits `0` when supervision ends cleanly - the workspace reaches a
terminal state its policy does not restart from, or `--max-restarts` is
reached; nonzero when the workspace cannot be found or a start fails. With
`--install`/`--uninstall` it exits `0` when the unit is written or removed.

## Related

- [`create`](/cli/create/) - set the restart policy
- [`start`](/cli/start/) - a single start without supervision
- [Workspace spec](/cli/spec/) - declare `restart` and `health`
- [`status`](/cli/status/) - check the supervised workspace
