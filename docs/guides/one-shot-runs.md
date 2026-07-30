---
title: Run one-shot commands
description: Boot a microVM, run a command, and tear it down - with setup, env vars, and artifacts.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

Use `microagent run` for disposable work: image plus command, setup steps before
it, environment variables into it, and files back out of it. microagent builds
the rootfs, boots the microVM, runs the command, and removes the scratch state
when it's done.

## 1. Run an image and a command

The positional form mirrors `docker run`: image first, command after.

```bash
microagent run docker.io/library/alpine:3.20 cat /etc/alpine-release
```

```text
3.20.10
```

Pull, rootfs build, and boot progress is shown live on stderr; stdout carries
only the command's output. Leave the command off and microagent runs the
image's Entrypoint/Cmd instead.

Use `--exec` when you want one shell command string rather than argv words:

```bash
microagent run --image docker.io/library/alpine:3.20 --exec 'echo hello from $(hostname)'
```

The guest command's exit code becomes the CLI's exit status, like `docker run`:
your shell's `$?` reflects the command itself. When the run fails to build,
boot, or complete, the CLI exits `1` with the error on stderr. With `--json`,
the exit code is also in `result.exit_code`.

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

`--rm` spells out the default disposable behavior; it exists so
container-style commands carry over. You only need a flag when you want the
opposite, `--keep`.

## 5. Bound the run with a timeout

`--timeout` caps wall-clock seconds before the microVM is killed:

```bash
microagent run --timeout 5 docker.io/library/alpine:3.20 sleep 60
```

```text
Error: run workspace "run-plucky-lynx-8t2m" failed (backend=linux-kvm ...): signal: killed
```

The CLI exits nonzero and the workspace record is left behind in state
`failed` so you can read its [logs](/cli/logs/). Delete it once you've looked:

```bash
microagent list
microagent delete <name-from-list> --yes
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

## Related

- [Persistent workspaces](/guides/persistent-workspaces/) — keep state between runs with the create, start, halt lifecycle.
- [Volumes and data](/guides/volumes-and-data/) — mount data instead of baking it in.
- [`run`](/cli/run/) — every flag on the one-shot path.
