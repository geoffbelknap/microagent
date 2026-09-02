---
title: Confine egress to an allowlist
description: Lock a workspace down to a known set of destinations, and let cert-pinned endpoints through with passthrough.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

Use an egress allowlist when a workspace should reach only known destinations.

By default (`--egress broker`) a workspace can reach the public internet
freely, internal addresses are denied, and every connection is recorded. That
answers "what did the agent reach?" - this guide is for the stronger question:
"how do I make sure it can reach *only* what I approve?"

For the ideas behind it - the modes, the trust model, UDP/DNS mediation - see
[Egress mediation](../concepts/egress-mediation.md).

## Confine a workspace with `--egress-lock-allowlist`

`--egress-lock-allowlist` flips the default from allow-broad to
deny-everything-not-listed. The mediator also becomes the workspace's only
DNS resolver, so a name you didn't allowlist never even resolves:

```bash
microagent create research \
  --image docker.io/library/python:3.12-slim \
  --egress-lock-allowlist \
  --egress-allow api.openai.com \
  --egress-allow .pypi.org
```

- `--egress-allow` is repeatable - one host per flag.
- A plain host (`api.openai.com`) is an **exact** match.
- A leading-dot entry (`.pypi.org`) is a **suffix** match: it matches the apex
  `pypi.org` and any subdomain (`files.pypi.org`). Use it for a service
  spread across subdomains.

Matching is case-insensitive and a trailing dot (FQDN form) is normalized away,
so `API.Example.com` and `api.example.com.` are the same entry. Anything you do
not list is denied fail-closed, and its name is REFUSED at the resolver.

The same flags exist on [`microagent run`](../cli/run.md) and
[`microagent dispatch`](../cli/dispatch.md) for one-shot workloads:

```bash
microagent run --egress-lock-allowlist --egress-allow .anthropic.com \
  docker.io/library/python:3.12-slim python agent.py
```

Egress settings are persisted with the workspace, so a later
[`start`](../cli/start.md) re-applies the same mode and lists. You can also declare
them in the Agentfile's `agent:` block (`egress:` and `allow:`) - see
[`microagent dispatch`](../cli/dispatch.md).

### Allowing one internal host

The lock isn't required to allow a single internal host: under plain
`broker`, `--egress-allow 10.0.0.5` permits exactly that internal
destination while the rest of the internal address space stays denied.

## allow vs passthrough

This distinction matters only under `--egress mitm`, the opt-in mode where
microagent intercepts TLS to read request content. There:

- An **allow** host's TLS is intercepted, so microagent can audit the
  plaintext.
- A **passthrough** host is allowed but never intercepted - forwarded as an
  opaque byte stream, with the original server certificate reaching the guest.

Use passthrough for endpoints that break under interception (certificate
pinning, mutual TLS, or a client with its own root store):

```bash
microagent create research \
  --egress mitm \
  --egress-lock-allowlist \
  --egress-allow api.openai.com \
  --egress-passthrough pinned.example.com
```

`--egress-passthrough` is repeatable and takes the same exact / `.suffix` forms
as `--egress-allow`. Under the default `broker` mode nothing is intercepted in
the first place, so you rarely need passthrough there.

If an allowed host's TLS is failing under `mitm`, passthrough is usually the
fix - see
[Troubleshooting](../troubleshooting.md#an-allowed-hosts-tls-connection-fails-under-mitm).

## Reusable lists: the policy file

Repeating flags gets unwieldy for large lists. Declare them once in a policy
file and point `--egress-policy` at it:

```yaml
# egress.yaml
allow:
  - api.openai.com
  - .anthropic.com
  - .pypi.org
passthrough:
  - pinned.example.com
  - mtls.internal.example
```

```bash
microagent create research --egress-lock-allowlist --egress-policy egress.yaml
```

- The file may be `.yaml`, `.yml`, or `.json` (same `allow:` / `passthrough:`
  shape).
- Its entries are unioned with any `--egress-allow` / `--egress-passthrough`
  flags you also pass - the file does not replace the flags.
- It is decoded strictly: an unknown top-level key (a typo like `allowed:`) or an
  empty list entry is an error, so a misconfiguration fails closed rather than
  silently leaving a host unreachable.
- A policy file requires a mediating mode (`broker`, the default, or `mitm`);
  passing one with `--egress off` is rejected (mediation is off, so there is
  nothing to allow).

Because the locked allowlist is default-deny, a policy file can only ever
add reachability. It never widens access beyond the hosts it names, and it
grants nothing when mediation is off.

## Confirm what the agent reached

Whichever form you used, check the decisions the mediator made:

```bash
microagent egress research            # recorded allow / deny / DNS decisions
microagent egress research --follow   # stream them live
```

A denied destination shows up as `egress_deny` (or `egress_dns_deny` for a
refused name); an allowed one as `egress_allow`. That is how you verify your
allowlist is neither too tight (legitimate traffic denied) nor too loose. See
[`microagent egress`](../cli/egress.md) for the full record vocabulary.

## Related

- [Egress mediation](../concepts/egress-mediation.md) - the concepts: modes, the mitm CA, UDP/DNS, fail-closed
- [`microagent egress`](../cli/egress.md) - view the audit decisions
- [`microagent create`](../cli/create.md) / [`microagent run`](../cli/run.md) - where the egress flags live
- [Troubleshooting](../troubleshooting.md#an-allowed-hosts-tls-connection-fails-under-mitm) - when an allowed host's TLS fails
