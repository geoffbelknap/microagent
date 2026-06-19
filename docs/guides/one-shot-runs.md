---
title: Run one-shot commands
description: Boot a microVM, run a command, and tear it down - with setup, env vars, and artifacts.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-19_

By the end of this guide you can run any command in a disposable microVM:
image plus command, setup steps before it, environment variables into it, and
files back out of it. `microagent run` builds the rootfs, boots the microVM,
runs the command, and removes the scratch state when it's done.

## 1. Run an image and a command

The positional form mirrors `docker run`: image first, command after.

```bash
microagent run docker.io/library/alpine:3.20 cat /etc/alpine-release
```

```text
Workspace: run-1781167799699801876
State: stopped
Profile: small
Network: user
Resources: memory=512MiB cpus=2 disk=1024MiB
Exit code: 0

3.20.10
```

Leave the command off and microagent runs the image's Entrypoint/Cmd instead.
Later output blocks on this page trim the workspace summary above and show
only the command output.

Use `--exec` when you want one shell command string rather than argv words:

```bash
microagent run --image docker.io/library/alpine:3.20 --exec 'echo hello from $(hostname)'
```

The guest command's exit code is reported as `Exit code:` (or
`result.exit_code` with `--json`), not as the CLI's own exit status. The CLI
exits nonzero only when the run itself fails to build, boot, or complete. Use
[`exec`](/cli/exec/) against a running workspace when you need the guest exit
code to drive your shell.

## 2. Add setup steps

`--setup` runs before `--exec` in the same boot. Repeat it for multiple steps.

```bash
microagent run \
  --image docker.io/library/alpine:3.20 \
  --setup "apk add --no-cache jq" \
  --exec "jq --version"
```

```text
(1/2) Installing oniguruma (6.9.9-r0)
(2/2) Installing jq (1.7.1-r0)
jq-1.7.1
```

`--setup-file` does the same with a script file from the host.

## 3. Pass environment variables

`-e`/`--env` sets variables in the guest, repeatable:

```bash
microagent run -e GREETING=hello -e TARGET=microvm \
  docker.io/library/alpine:3.20 printenv GREETING TARGET
```

```text
hello
microvm
```

Environment variables are fine for configuration. For credentials, use
`--secret` instead - see [Deliver secrets](/guides/secrets/).

## 4. Get files back out

A one-shot run removes its scratch state, so declare what you want to keep.
`--output` names a guest path as an artifact; `--keep` preserves the workspace
state so you can fetch it afterwards:

```bash
microagent run --keep --name report-run \
  --image docker.io/library/alpine:3.20 \
  --output report=/workspace/report.txt \
  --exec "mkdir -p /workspace && echo 'artifact content' > /workspace/report.txt"
```

Then list and retrieve the artifact from the stopped workspace:

```bash
microagent artifact report-run
microagent artifact get report-run report ./report.txt
cat ./report.txt
```

```text
artifact content
```

A kept run is a regular workspace until you delete it:

```bash
microagent delete report-run --yes
```

`--delete` spells out the default disposable behavior; it exists so container-style
muscle memory works. You only need a flag when you want the opposite, `--keep`.

## 5. Bound the run with a timeout

`--timeout` caps wall-clock seconds before the microVM is killed:

```bash
microagent run --timeout 5 docker.io/library/alpine:3.20 sleep 60
```

```text
Error: run workspace "run-1781167843059951259" failed (backend=linux-kvm ...): signal: killed
```

The CLI exits nonzero and the workspace record is left behind in state
`failed` so you can read its [logs](/cli/logs/). Delete it once you've looked:

```bash
microagent list
microagent delete run-1781167843059951259 --yes
```

## Clean up

Disposable runs clean up after themselves. Anything you ran with `--keep` (or
that timed out) shows up in `microagent list` - delete those when you're done,
and confirm the list is empty:

```bash
microagent list
```

```text
No workspaces.
```

## What's next

- **Keep state between runs** - [persistent workspaces](/guides/persistent-workspaces/) cover the create, start, halt lifecycle.
- **Mount data instead of baking it in** - [volumes and data](/guides/volumes-and-data/).
- **Every `run` flag** - the [`run`](/cli/run/) reference.
