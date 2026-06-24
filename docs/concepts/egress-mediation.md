---
title: Egress mediation
description: How microagent captures, audits, and controls everything a workspace sends to the network - the four modes, the per-workspace MITM CA, UDP/DNS mediation, and allow vs passthrough.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-24_

Egress mediation is microagent's transparent control point for **everything a
workspace sends to the network**. The host captures the guest's outbound
traffic, decides what to do with each connection, records the decision, and
forwards or denies it - the guest cannot reach the network except through this
path. It is how microagent answers "what did this agent actually try to talk
to?" and, when you want it, "what is it allowed to talk to?".

> **Not the same thing as the mediation channel.** Egress mediation (this page)
> governs the guest's *ordinary network egress* - the TCP, UDP, and DNS it sends
> out of its network device. The [mediation channel](/guides/agents-and-mediation/)
> is a separate guest-to-host **vsock** contract for the agent's calls into your
> host control plane. Different mechanism, different purpose; they share only the
> word "mediation". See [networking](/concepts/networking/#mediation-channel) for
> the channel.

Egress mediation is implemented by the Firecracker backend on Linux. It runs in
the [`user` network mode](/concepts/networking/)'s per-VM user namespace, the
only mode that carries network egress.

> **Migration note (behavior change):** the default egress mode is `guarded`.
> Workspaces that omit `--egress` deny internal destinations
> (link-local/metadata 169.254/16, RFC1918, IPv6 ULA, CGNAT 100.64/10,
> loopback, and east-west peers) while still allowing the public internet
> freely. To permit a specific internal host use `--egress-allow <host-or-ip>`.

## The three modes

A workspace's egress posture is set with `--egress` on
[`create`](/cli/create/) or [`run`](/cli/run/):

| Mode | What happens | Default |
|---|---|---|
| `guarded` | Denies "the inside" (link-local/metadata 169.254/16, RFC1918, IPv6 ULA, CGNAT 100.64/10, loopback, east-west peers) on the **resolved destination IP**; allows the public internet with **no allowlist required**. DNS resolves freely — the inside protection is enforced at connect time, which also defeats DNS rebinding. | **Yes** |
| `strict` | **Deny anything not on the allowlist**, and the mediator is the **only DNS resolver** — non-allowlisted names get REFUSED before any connection is attempted. | No |
| `off` | No mediation. The guest's network device is wired straight to the chosen [network mode](/concepts/networking/). | No |

`guarded` is the default: omit `--egress` and the workspace can reach the
public internet freely, but any attempt to connect to an internal address is
denied and audited. `--egress open` and `--egress disabled` are accepted aliases
for `off`; an empty value resolves to `guarded`.

Reach for `guarded` (the default) when you want the public internet available
without any allowlist but need the host and internal infrastructure protected;
to grant access to a specific internal host, add it with `--egress-allow`.
Reach for `strict` when you want the agent confined to an explicit set of
destinations.

### Allowlist exception under guarded

An operator can permit a specific internal host or IP while keeping the guarded
default by using `--egress-allow <host-or-ip>`. An explicitly allowlisted
destination overrides the inside-deny: `d.Allow` wins. This lets you grant
access to exactly one internal service (e.g. a sidecar on `10.0.0.5`) without
opening the entire internal address space.

Every decision in every mode is recorded. See
[Where decisions are recorded](#where-decisions-are-recorded).

## Mediation means microagent reads your TLS

Be clear-eyed about what `guarded` (the default) and `strict` do for
intercepted connections: the mediator performs a
**man-in-the-middle (MITM)** on the guest's outbound TLS. For an allowed,
intercepted connection the mediator terminates the guest's TLS, opens its own
verified TLS connection upstream, and relays the plaintext between the two - so
microagent can audit the request. The guest sees a valid certificate because of
the trust model below; the operator sees the cleartext of what the agent sent
and received.

This is intentional. The point of mediation is to make an agent's network
activity legible and governable. But it means the host can read the contents of
intercepted TLS. If a destination must not be read - or cannot
tolerate interception (certificate pinning, mutual TLS) - mark it
**[passthrough](#allow-vs-passthrough)** so it is forwarded opaquely instead of
intercepted.

### The per-workspace CA trust model

Interception works because each workspace gets its own certificate authority:

- On start, microagent mints a **fresh ECDSA P-256 CA** scoped to that one
  workspace.
- The CA's **public certificate** is delivered to the guest over a vsock channel
  at boot and installed into the guest's trust store (copied into the system CA
  bundle, `update-ca-certificates` is run, and `SSL_CERT_FILE` / `CURL_CA_BUNDLE`
  point at a combined bundle). So tools inside the guest trust the leaf
  certificates the mediator signs per-SNI.
- The CA's **private key never leaves the host.** The guest holds only the public
  cert; it can verify the mediator's leaves but cannot sign anything.

The CA is scoped to a single workspace and dies with it. There is no shared root,
no host-wide trust grant, and nothing the guest can use to forge a certificate.
A snapshot/restore re-arms the *same* CA the guest's baked trust store was built
against - microagent refuses to restore a mediated workspace whose persisted CA
fingerprint does not match, rather than silently breaking the guest's trust.

## UDP and DNS mediation

Mediation is not TCP-only. Under `guarded` and `strict`:

- **UDP** is captured transparently (via Linux TPROXY) and forwarded, with each
  datagram flow audited. In `guarded` mode, UDP datagrams to inside addresses
  are denied and recorded as `egress_udp_internal_deny`.
- **DNS** is mediated by making the mediator the guest's resolver. In `guarded`
  mode every query is forwarded to the real resolver and the
  answers are recorded (the name-to-IP mappings are also used to police later
  flows by hostname). In `guarded` mode DNS resolves freely — even for names
  that point at internal IPs — but the resulting TCP/UDP connection is denied
  at connect time on the resolved IP, which also defeats DNS rebinding attacks.
  In `strict` mode the mediator only resolves allowlisted names; a query for a
  non-allowlisted name is answered **REFUSED** without ever being forwarded. The
  guest learns no IP, so **DNS tunneling and DNS-based exfiltration are defeated
  before any connection is even attempted.**

Destinations are policed by **hostname**, not just IP: the SNI of a TLS
connection, the HTTP `Host` header, or a name the guest resolved through the
mediator. Guest IPv4 traffic that is neither TCP nor UDP (ICMP and
the like) carries no allowlistable destination and is dropped and audited rather
than forwarded. Guest IPv6 egress is dropped fail-closed while v4-only mediation
ships, so nothing slips past the v4 capture.

### Host requirement: TPROXY (and fail-closed)

UDP and DNS mediation run inside the workspace's own user namespace and depend
on the host's TPROXY netfilter modules (`nft_tproxy`, `nf_tproxy_ipv4`,
`xt_socket`, `nf_socket_ipv4`). A rootless workspace cannot load these itself,
so:

- [`microagent doctor`](/cli/doctor/) reports whether the TPROXY modules are
  loaded or built in.
- Load the modules once, as root, with
  `sudo modprobe nft_tproxy nf_tproxy_ipv4 xt_socket nf_socket_ipv4`. With the
  modules present, the workspace's netns installs its own TPROXY rules.

If a `guarded` or `strict` workspace lands on a host where TPROXY
is not available, the workspace **fails closed - it refuses to start** rather
than running with an unmediated UDP/DNS channel. The error names the fix:

```text
egress: UDP mediation (TPROXY) unavailable for workspace research — load the TPROXY kernel modules or use --egress off
```

Load the TPROXY kernel modules, or drop to `--egress off` if you genuinely want
no mediation. This fail-closed behavior is the point: an enforcement failure can
never silently widen what the agent can do.

## Allow vs passthrough

Two ways to permit a destination, and they are not the same:

- **allow** (`--egress-allow <host>`) - the connection is permitted, **and** if it
  is TLS it is **intercepted** (MITM'd) so microagent can read and audit the
  plaintext. This is the normal allowlist entry.
- **passthrough** (`--egress-passthrough <host>`) - the connection is permitted
  **but NOT intercepted.** It is forwarded as an opaque L4 byte stream; the
  original server certificate reaches the guest untouched, and microagent records
  *that* the connection happened (and how much data moved) but **cannot see the
  payload.**

Passthrough is the escape hatch for endpoints that break under MITM:
certificate-pinned clients, mutual-TLS endpoints, or any client carrying its own
root store that would reject the injected per-workspace CA. You trade payload
visibility for compatibility - the connection is still allowed and still audited
as a connection, you just can't inspect what crossed it. See
[Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails) for
the symptom that tells you to use it.

In `strict` mode, both allow and passthrough entries are reachable; everything
else is denied. In `guarded` mode public destinations are already
reachable (the allowlist is not required), but marking a host passthrough still
matters - it stops microagent from MITM'ing that host's TLS. An `--egress-allow`
entry in `guarded` mode additionally overrides the inside-deny for that specific
host (see [Allowlist exception under guarded](#allowlist-exception-under-guarded)).

For the flags, the `.suffix` matching form, and the policy file, see the
[allowlist and passthrough how-to](/guides/egress-allowlist/).

## Credential swap

A capability built on top of interception: for an allowlisted, intercepted host,
microagent can inject a **real credential host-side** so the guest never holds
the secret. The agent makes an unauthenticated (or placeholder) request to the
allowed host; the mediator parses the request and injects the actual credential -
acquired by a `static`, `oauth2-cc`, or `jwt-bearer` strategy - before forwarding
it upstream. The secret stays on the host, out of the guest's filesystem and
memory. This is related to, but distinct from, [delivering secrets into the
guest](/guides/secrets/); reach for credential swap when you want the agent to
use a credential it should never be able to read.

Enable it with `--egress-swap-config <path>` on [`run`](/cli/run/) or
[`create`](/cli/create/) — it requires `--egress guarded` or
`strict`, and the target host must be allowlisted. The file declares named swap entries:

```yaml
# swaps.yaml
swaps:
  openai:
    type: static                 # static | oauth2-cc | jwt-bearer
    domains: [api.openai.com]     # exact host, or .suffix for subdomains
    header: Authorization
    format: "Bearer {key}"        # {key} is replaced by the acquired credential
    key_ref: env:OPENAI_API_KEY   # resolved on the host; never enters the guest
```

## Bounded operations

The mediator can enforce per-workspace caps so a mediated workspace's egress is
bounded, not unlimited by default: a maximum upstream byte rate, a cumulative
total-bytes cap across TCP and UDP, and a concurrent-connection cap. A flow that
breaches a cap is torn down and audited; the mediator keeps serving. The caps are
off (unlimited) unless you set them, and the audit log records cap trips as
`egress_cap_exceeded`.

## Where decisions are recorded

Every decision the mediator makes is written to a per-workspace, append-only
audit log - by the host, not the agent. View it with
[`microagent egress <name>`](/cli/egress/):

```bash
microagent egress research            # the recorded decisions, oldest first
microagent egress research --follow   # stream new decisions live
microagent --json egress research     # the decisions as a JSON array
```

Each line is one decision. The vocabulary is open-ended, but the common records
are:

| Record | Meaning |
|---|---|
| `egress_allow` / `egress_close` | A permitted TCP connection opened / closed |
| `egress_deny` | A TCP connection denied fail-closed (`strict`, non-allowlisted) |
| `egress_internal_deny` | A TCP connection denied because the resolved destination IP is an inside address (`guarded` mode); includes `internal: true` and `dst` fields |
| `egress_mitm_handshake_error` / `egress_mitm_upstream_error` | A TLS interception problem (see [Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails)) |
| `egress_dns_allow` / `egress_dns_deny` | A name resolved / REFUSED |
| `egress_udp_allow` / `egress_udp_deny` / `egress_udp_close` | A UDP flow permitted / denied / closed |
| `egress_udp_internal_deny` | A UDP datagram denied because the destination IP is an inside address (`guarded` mode); includes `internal: true` and `dst` fields |
| `egress_cap_exceeded` | A bounded-operations cap tripped |
| `egress_loop_guard` | The mediator's own forwarding leg, dropped to avoid a self-loop |

An `unlisted: true` field marks a destination permitted only because of
`guarded` mode's public grant (it is on no allowlist), so the audit distinguishes
the looser grant from an explicitly allowlisted one. This audit log is a separate stream
from lifecycle [`events`](/cli/events/): `events` is how the workspace got to its
state, `egress` is what it tried to reach and how the mediator ruled.

## See also

- [Allowlist and passthrough how-to](/guides/egress-allowlist/) - the flags, the `.suffix` form, and the policy file
- [`microagent egress`](/cli/egress/) - view the audit decisions
- [Networking](/concepts/networking/) - network modes and the (separate) mediation channel
- [Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails) - what to do when an allowed host's TLS fails
- [Deliver secrets](/guides/secrets/) - the related credential-delivery path
