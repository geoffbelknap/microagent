---
title: Allowlist and passthrough egress
description: Confine a workspace to a known set of destinations with strict mode, and let cert-pinned endpoints through with passthrough.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-17_

This guide shows how to set a workspace's egress allowlist and passthrough set.
For the ideas behind it - the three modes, the MITM trust model, UDP/DNS
mediation - read [Egress mediation](/concepts/egress-mediation/) first.

By default (`--egress guarded`) a workspace can reach the public internet
freely while internal destinations are denied, and every connection is captured
and audited. To **confine** it to a known set of destinations, switch to
`strict` and declare what it may reach.

## Confine a workspace with `strict`

`strict` mode denies anything not on the allowlist, and the mediator becomes the
only DNS resolver, so non-allowlisted names never even resolve:

```bash
microagent create research \
  --image docker.io/library/python:3.12 \
  --egress strict \
  --egress-allow api.openai.com \
  --egress-allow .pypi.org
```

- `--egress-allow` is repeatable - one host per flag.
- A plain host (`api.openai.com`) is an **exact** match.
- A leading-dot entry (`.pypi.org`) is a **suffix** match: it matches the apex
  `pypi.org` **and** any subdomain (`files.pypi.org`, `pypi.org`). Use it for a
  service spread across subdomains.

Matching is case-insensitive and a trailing dot (FQDN form) is normalized away,
so `API.Example.com` and `api.example.com.` are the same entry. Anything you do
not list is denied fail-closed, and its name is REFUSED at the resolver.

The same flags exist on [`microagent run`](/cli/run/) for one-shot workloads:

```bash
microagent run --egress strict --egress-allow .anthropic.com \
  docker.io/library/python:3.12 python agent.py
```

Egress settings are persisted with the workspace, so a later
[`start`](/cli/start/) re-applies the same mode and lists. They are configured
through these flags (and the policy file below), not in the `microagent.yaml`
spec file.

## allow vs passthrough

A passthrough host is **allowed but not TLS-intercepted** - forwarded as an
opaque byte stream, with the original server certificate reaching the guest. Use
it for endpoints that break under interception (certificate pinning, mutual TLS,
or a client with its own root store):

```bash
microagent create research \
  --egress strict \
  --egress-allow api.openai.com \
  --egress-passthrough pinned.example.com
```

`--egress-passthrough` is repeatable and takes the same exact / `.suffix` forms
as `--egress-allow`.

- An **allow** host's TLS is MITM'd, so microagent can audit the plaintext.
- A **passthrough** host's TLS is left intact, so microagent records *that* the
  connection happened but **cannot see the payload.**

Passthrough is also meaningful under the default `guarded` mode: public
destinations are already reachable there, but marking a host passthrough stops
microagent from MITM'ing it. If an allowed host's TLS is failing, passthrough is usually the fix
- see [Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails).

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
microagent create research --egress strict --egress-policy egress.yaml
```

- The file may be `.yaml`, `.yml`, or `.json` (same `allow:` / `passthrough:`
  shape).
- Its entries are **unioned** with any `--egress-allow` / `--egress-passthrough`
  flags you also pass - the file does not replace the flags.
- It is decoded strictly: an unknown top-level key (a typo like `allowed:`) or an
  empty list entry is an error, so a misconfiguration fails closed rather than
  silently leaving a host unreachable.
- A policy file requires `--egress guarded` or `--egress strict`; passing one
  with `--egress off` is rejected (mediation is off, so there is nothing to
  allow).

Because the policy is default-deny, a policy file can only ever **add
reachability** - it can never widen access beyond the hosts it names, and it
grants nothing when mediation is off.

## Confirm what the agent reached

Whichever form you used, watch the decisions the mediator actually made:

```bash
microagent egress research            # recorded allow / deny / DNS decisions
microagent egress research --follow   # stream them live
```

A denied destination shows up as `egress_deny` (or `egress_dns_deny` for a
refused name); an allowed one as `egress_allow`. That is how you verify your
allowlist is neither too tight (legitimate traffic denied) nor too loose. See
[`microagent egress`](/cli/egress/) for the full record vocabulary.

## See also

- [Egress mediation](/concepts/egress-mediation/) - the concepts: modes, MITM CA, UDP/DNS, fail-closed
- [`microagent egress`](/cli/egress/) - view the audit decisions
- [`microagent create`](/cli/create/) / [`microagent run`](/cli/run/) - where the egress flags live
- [Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails) - when an allowed host's TLS fails
