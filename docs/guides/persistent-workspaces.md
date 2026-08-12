---
title: Keep a persistent workspace
description: Create a named workspace and walk its create, start, halt, connect, delete lifecycle.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-12_

Use a persistent workspace when disk and state should survive between starts:
the environment you set up today is still there tomorrow. A workspace is a
named, persistent microVM record. Unlike [`microagent run`](/guides/one-shot-runs/),
nothing is thrown away until you say so.

## 1. Create it

```bash
microagent create research \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium
```

```text
Workspace: research
State: prepared
Rootfs: /home/you/.microagent/workspaces/research/rootfs.ext4
Profile: medium
Resources: memory=2048MiB cpus=2 disk=8192MiB used=812MiB(10%) host=931MiB
```

The name can also go in `--name`. `--profile` picks a named resource size and
is the recommended way to size a workspace; override single values with
`--memory`, `--cpus`, or `--size-mib` when you need to. `create` builds the
rootfs once and records the workspace in the state directory; if the default
kernel is missing, it installs that first.

`--setup` runs shell commands once before first start - useful for installing
packages into the rootfs:

```bash
microagent create research \
  --image docker.io/library/ubuntu:24.04 \
  --setup "apt-get update && apt-get install -y ripgrep"
```

This `--setup` example (like the storage variant in step 5) is an alternative
`create` form, not a step to stack on the first. A second `create research`
fails while the workspace exists, so `delete` it or pick a new name first.

## 2. Start it and do some work

```bash
microagent start research
```

Run commands with structured `exec`:

```bash
microagent exec research -- sh -c "echo 'notes from run 1' > /root/notes.txt"
microagent exec research -- /bin/cat /root/notes.txt
```

```text
notes from run 1
```

Or open the serial console with `connect` - interactive, or scripted with
`--send`:

```bash
microagent connect research --send "uname -r"
```

```text
# 6.1.155
```

Use [`logs`](/cli/logs/) when you want captured serial output instead of an
interactive console.

## 3. Inspect it

```bash
microagent list                # saved workspaces; ls is an alias
microagent ps                  # running workspaces
microagent status research   # one workspace
microagent logs research     # boot/serial output
```

```text
NAME                     STATE        BACKEND      PROFILE      NETWORK    RESTART
research                 running      linux-kvm    medium       user       never
```

`microagent --json status research` adds the structured readiness signals
(`guestReady`, `execReady`, and related fields) for scripts that need to
sequence work.

## 4. Halt it, start it again

`halt` is the clean disk-preserving shutdown. The microVM exits; the disk
stays.

```bash
microagent halt research
microagent start research
microagent exec research -- /bin/cat /root/notes.txt
```

```text
notes from run 1
```

Everything you wrote, installed, or configured is still there. Set a
workspace up once, then halt and start it as needed — a restart takes
seconds.

`stop` is a pure alias of `halt` - same graceful window, same `halted`
outcome on a clean exit - so existing `stop` habits keep working. The other
lifecycle words are not synonyms for halt, though. `pause`/`resume` suspend
and thaw a running workspace's memory in place; `kill` hard-terminates
instead of asking nicely; `quarantine` cuts a workspace off from the
host without shutting it down. If the guest doesn't exit within `halt`'s
fixed graceful window (about 15 seconds), the workspace is recorded
`failed` and `halt` returns an error. It does not fall back to `kill` on its
own; run [`kill`](/cli/kill/) yourself when a guest is stuck.
See the [glossary](/concepts/glossary/) for the full halt / pause / kill /
quarantine vocabulary.

## 5. Attach extra storage

The rootfs is the workspace's own disk. Attach more at create time - a named
volume, an existing ext4 image, or a one-shot disk built from a tar bundle:

```bash
microagent create research \
  --image docker.io/library/ubuntu:24.04 \
  --volume data:/work \
  --bundle config=/tmp/config.tar:/config:ro
```

[Volumes and data](/guides/volumes-and-data/) walks through all three forms
and when to use which.

## 6. Delete it

```bash
microagent halt research
microagent delete research --yes
```

`delete` removes the workspace record and its disk. If the workspace is still
running, the confirmation prompt becomes "Stop and delete it?", and answering
yes (or passing `--yes`) stops it first; `--force` kills instead of stopping.
Leave off `--yes` to get a confirmation prompt either way.

## Related

- [Run your first agent](/getting-started/cli/first-agent/) — put an agent in a persistent workspace.
- [Run a service](/guides/run-a-service/) — keep a long-lived server running in one.
- [`microagent.yaml`](/cli/spec/) — describe the whole workspace in one file.
- [Library overview](/library/) — drive workspaces from Go instead of the CLI.
