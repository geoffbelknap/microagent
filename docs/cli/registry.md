---
title: microagent registry
description: Store credentials for private OCI registries without any Docker dependency.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-25_

```text
microagent registry login <registry> -u <user> [--password-stdin]   Store registry credentials
microagent registry logout <registry>                               Remove stored credentials
microagent registry list                                            List registries with stored credentials
```

microagent pulls and pushes OCI images by talking to registries directly — it
does **not** depend on Docker, Docker Desktop, or any `docker-credential-*`
helper. Public images always pull anonymously. For private registries,
credentials are resolved from static credential files only; credential helpers
are never executed.

## Credential sources

Resolved in order, first match wins — all Docker-free:

1. **`$REGISTRY_AUTH_FILE`** — the vendor-neutral file convention shared with
   Podman, Skopeo, and Buildah (`{"auths":{...}}` JSON).
2. **`~/.microagent/auth.json`** — microagent's own credential file, written by
   `microagent registry login` (mode `0600`).
3. **anonymous** — no credentials (public images).

Docker's `~/.docker/config.json` is **never** read, and credential helpers
(`credsStore`/`credHelpers`) are never executed — microagent has no dependency
on Docker or Docker Desktop.

## Examples

Log in to GitHub Container Registry with a token piped on stdin (the password is
never passed as an argument, so it cannot leak into the process table or shell
history):

```bash
echo "$GHCR_TOKEN" | microagent registry login ghcr.io -u USERNAME --password-stdin
```

Log in interactively (the prompt reads the password without echo):

```bash
microagent registry login registry.example.com -u alice
```

List and remove stored credentials:

```bash
microagent registry list
microagent registry logout ghcr.io
```

Use a shared auth file instead of microagent's own store:

```bash
export REGISTRY_AUTH_FILE=~/auth.json
microagent run registry.example.com/team/app
```

## Flags

| Flag | Command | Description |
|---|---|---|
| `-u`, `--username` | `login` | Registry username (required). |
| `--password-stdin` | `login` | Read the password from stdin instead of prompting. |

## Notes

- `microagent registry login` stores `base64(username:password)` — the standard
  encoding, the same one Docker/OCI config files use. This is encoding, not
  encryption; the file is written `0600`. microagent is otherwise a secret
  conduit, not a store (see [secret](/cli/secret/)).
- microagent never writes Docker's `~/.docker/config.json`.
