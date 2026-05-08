---
title: Pin everything for production
description: Image digest, kernel SHA, rootfs hash, verified at every start. What "reproducible" actually means for a microagent-kit workspace.
---

The simple-agent recipe uses `python:3.13-slim` — a mutable tag. That's fine for getting started, dangerous for production. The same tag tomorrow can resolve to different bytes, and you won't notice until something behaves differently and you spend an hour debugging.

This recipe locks every layer of a workspace's identity to a specific value, then has microagent verify it on every start. The aim isn't paranoia — it's to make divergence detectable instead of invisible.

## What can drift

Three things define what your workspace boots:

| Layer | Where it can change | What pinning prevents |
|---|---|---|
| OCI image | Mutable tag (`python:3.13-slim`) resolves differently | Quietly swapping userspace under your body |
| Kernel | Default download changes between microagent versions | Quietly changing kernel features or security posture |
| Rootfs | Built-from-source ext4 — can be tampered with on disk | A modified rootfs going unnoticed |

microagent records SHA-256 hashes for all three when the workspace is created, and `microagent --json status` recomputes them on demand. That's the foundation; pinning gives it something to compare against.

## Pin the image

Use a digest reference, not a tag.

**Resolve the digest with microagent itself:**

```bash
microagent images pull python:3.13-slim
microagent --json images list | jq -r '.[] | select(.imageRef == "python:3.13-slim") | .resolvedRef'
# docker.io/library/python@sha256:1a2b3c...
```

`images pull` records the source ref, resolved ref (with digest), platform, and rootfs path in microagent's local image index. `images list` reads it back.

**Use the resolved ref everywhere:**

```yaml
# microagent.yaml
image: docker.io/library/python@sha256:1a2b3c...
```

`microagent rootfs build` rejects mutable tags by default — it'll fail closed unless you pass `--allow-mutable`. `microagent create` and `microagent run` are looser (they accept tags and record the resolved digest), but the right discipline is to pin upstream and let the looser commands enforce it implicitly.

## Pin the kernel

The default `microagent kernel install` pulls the kernel pinned to your microagent-kit version. That's stable per-release, but it changes when microagent-kit ships a new release.

**For reproducibility across releases, install explicitly:**

```bash
microagent kernel install \
  --url https://example.com/kernels/Image-6.6.15-firecracker-amd64 \
  --sha256 4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

The `--sha256` flag tells microagent what the file should hash to after download. If it doesn't match, install fails. The hash is then recorded in the workspace manifest at create time.

**Pick the SHA from a release record.** The [release notes](../releases/index.md) name the kernel SHA each microagent-kit release was validated against. Use that one — don't pick a kernel that no microagent release has signed off on.

## Verify on every start

```bash
microagent --json status <name> | jq '.verification'
```

```json
{
  "ok": true,
  "imageRef": "docker.io/library/python@sha256:1a2b3c...",
  "resolvedRef": "docker.io/library/python@sha256:1a2b3c...",
  "imageDigest": "sha256:1a2b3c...",
  "kernel":  { "path": "...", "sha256": "..." },
  "rootfs":  { "path": "...", "sha256": "...", "recordedSHA256": "..." }
}
```

If `verification.ok` is `false`, the response includes `verification.divergence` with one entry per mismatched artifact:

```json
{
  "verification": {
    "ok": false,
    "divergence": [
      { "artifact": "kernel", "field": "sha256", "expected": "4bbe...", "actual": "9f8e..." }
    ]
  }
}
```

A divergence means: the workspace was created against `expected`, and `actual` is what's on disk now. Investigate before you `start`. Either the recorded value is wrong (someone reinstalled the kernel), or the file was modified.

**Make this part of your start path** — wrap `microagent start` in a script that fails closed on divergence:

```bash
status=$(microagent --json status "$name")
if [ "$(echo "$status" | jq -r '.verification.ok')" != "true" ]; then
  echo "verification failed:" >&2
  echo "$status" | jq '.verification.divergence' >&2
  exit 1
fi
microagent start "$name"
```

## What pinning doesn't cover

- **Body source code.** Pinning the image and kernel doesn't tell you which version of `body.py` is in the workspace. If you need that, hash it yourself before `microagent cp`, or rebuild the spec'd workspace from a tagged commit.
- **Operator-supplied files.** `constraints.json`, `system_prompt.md`, etc., are content the operator places. Treat their hashes as part of your audit trail; microagent doesn't track them.
- **Runtime behavior.** Pinning makes drift detectable; it doesn't prevent the model itself from behaving differently call to call. Caching helps with consistency, but model determinism is a separate concern.

## Related

- [Security](../security.md) — trust boundary discussion.
- [`microagent kernel`](../cli/kernel.md) — install and verify.
- [Stability](../stability.md) — what microagent itself promises across releases.
