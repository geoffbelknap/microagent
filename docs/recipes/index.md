---
title: Recipes
description: End-to-end walkthroughs that combine microagent primitives into something useful.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

Recipes are tutorials. They take you from "I have microagent installed" to "I have a working thing", showing the moving parts as you assemble them.

If you're after the reference docs - what every flag does, what every command returns - see the [CLI reference](/cli/) or the [Go library](/library/go/) instead.

## Recipes

- [Build a simple agent](/recipes/simple-agent/) - a one-shot agent body that takes a request, calls Claude under operator-supplied constraints, and writes a result. Prompt caching on by default. Halt and resume between requests.
- [Wire up the mediation channel](/recipes/mediation-channel/) - pattern guide for moving the body from one-request-per-restart to a stream of requests over a guest-to-host vsock contract. Pattern guide, not copy-paste - the listener shape depends on your control plane.
