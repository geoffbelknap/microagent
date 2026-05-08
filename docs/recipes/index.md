---
title: Recipes
description: End-to-end walkthroughs that combine microagent-kit primitives into something useful.
---

Recipes are tutorials. They take you from "I have microagent-kit installed" to "I have a working thing", showing the moving parts as you assemble them.

If you're after the reference docs — what every flag does, what every command returns — see the [CLI reference](../cli/index.md) or the [Go library](../library/go.md) instead.

## Recipes

- [Build a simple agent](simple-agent.md) — a one-shot agent body that takes a request, calls Claude under operator-supplied constraints, and writes a result. Prompt caching on by default. Halt and resume between requests.
- [Wire up the mediation channel](mediation-channel.md) — pattern guide for moving the body from one-request-per-restart to a stream of requests over a guest-to-host vsock contract.
- [Pin everything for production](pin-everything.md) — image digest, kernel SHA, rootfs hash, verified at every start.
