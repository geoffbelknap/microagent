---
title: Egress mediation
description: Control and audit what a workspace sends to the network.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-04_

By default, a workspace can reach the public internet, it cannot reach your
LAN or the host, and every connection it attempts is recorded. Two commands
cover most needs: `--egress-allow <host>` permits something specific, and
`microagent egress <name>` shows what the workspace tried to reach. The rest
of this page is the machinery behind those two commands.

Egress mediation is microagent's transparent control point for workspace
network traffic. When mediation is active, the host captures the guest's
outbound traffic, decides what to do with each connection, records the
decision, and forwards or denies it. It is how microagent answers "what did
this agent try to reach?" and, when you want it, "what is it allowed to
reach?".

> **Not the same thing as the mediation channel.** Egress mediation (this page)
> governs the guest's *ordinary network egress* - the TCP, UDP, and DNS it sends
> out of its network device. The [mediation channel](/guides/agents-and-mediation/)
> is a separate guest-to-host vsock contract for the agent's calls into your
> host control plane. See [networking](/concepts/networking/#mediation-channel)
> for the channel.

Egress mediation only applies to [`user` network mode](/concepts/networking/),
the mode that carries outbound network traffic. If the current host cannot
provide mediation, microagent reports that as structured command output instead
of asking you to infer it from logs.

> **Migration note (breaking change):** the mode vocabulary is now
> `broker` / `mitm` / `off`. The former `guarded` and `strict` modes are
> retired — `--egress guarded`, `--egress strict`, and a manifest or
> snapshot naming either are hard errors (never silently reinterpreted), and
> the default is now `broker`. `broker` keeps the same allow-broad reach the old
> `guarded` default had (deny the inside, allow the public internet) without
> installing a CA in the guest. Choose `mitm` for the old cert-forging
> interception, and `--egress-lock-allowlist` for the old `strict` allowlist-only
> reach on either mode.

## The egress modes

A workspace's egress posture is set with `--egress` on
[`create`](/cli/create/) or [`run`](/cli/run/):

| Mode | What happens | Default |
|---|---|---|
| `broker` | Public internet allowed, "the inside" denied, every decision audited. Allowed TLS is spliced opaquely - no forged certificate, no CA in the guest, which sees the real upstream certificate. | **Yes** |
| `mitm` | Same allow-broad / deny-the-inside decision as `broker`, but allowed TLS is intercepted with a per-workspace CA so the mediator sees plaintext (content inspection, header-rewrite credential swap). Opt-in and warned; never the default. | No |
| `off` | No mediation. The guest's network device is wired straight to the chosen [network mode](/concepts/networking/). | No |

Microagent also evaluates the complete capability set before creating or
starting a workspace. A routable workspace cannot combine guest-delivered
secrets, injected files or disks, and `--egress off` unless the operator records
a reason with `--acknowledge-capability-risk`. The structured create result and
workspace manifest report the derived capability categories and whether this
composition was acknowledged. Isolated networking and host-side broker or
credential-swap secrets do not create that finding because they do not give the
guest unmediated outbound access or possession of the real credential.

"The inside" is classified on the resolved destination IP and covers
link-local/metadata (`169.254/16`), RFC1918 private ranges, IPv6 ULA, CGNAT
(`100.64/10`), loopback, and east-west peer workspaces.

`broker` is the default: omit `--egress` and the workspace can reach the public
internet freely. Any attempt to connect to an internal address is denied and
audited, and no CA is installed in the guest. An empty value resolves to
`broker`; the retired `guarded`/`strict` names and any unrecognized value are
rejected with an error naming the successor.

Add `--egress-lock-allowlist` on either mediating mode to deny anything not on
the allowlist (the old `strict` reach control). The mediator becomes the only
DNS resolver and answers `REFUSED` for non-allowlisted names before any
connection is attempted. It composes with the mode: `broker --egress-lock-allowlist` is
allowlist-only without interception; `mitm --egress-lock-allowlist` is
allowlist-only with interception.

Reach for `mitm` only when you need microagent to read the guest's TLS —
content inspection of non-brokered traffic requires it. It is not the
default. Prefer `broker`, which keeps cert-pinning
clients working and installs no CA to reason about. For credential injection,
use [broker endpoints](#the-broker-decision-stream) — they inject host-side
with no interception at all. `mitm` remains supported for operators who need
it. As the guest's sole resolver, both mediating modes strip HTTPS/SVCB
records — and any Encrypted Client Hello (ECH) config — from DNS answers. The
TLS SNI stays visible, so enforcement is not blinded by ECH.

### Allowlist exception under broker

An operator can permit a specific internal host or IP while keeping the broker
default by using `--egress-allow <host-or-ip>`: an explicitly allowlisted
destination overrides the inside-deny. This lets you grant access to exactly
one internal service (for example a sidecar on `10.0.0.5`) without opening the entire
internal address space.

Every decision in every mode is recorded. See
[Where decisions are recorded](#where-decisions-are-recorded).

## The mitm mode reads your TLS

`mitm` does what the name says: the mediator performs a man-in-the-middle
(MITM) on the guest's outbound TLS. For an allowed
connection the mediator terminates the guest's TLS, opens its own verified TLS
connection upstream, and relays the plaintext between the two - so microagent
can audit the request. The guest sees a valid certificate because of the trust
model below; the operator sees the cleartext of what the agent sent and
received.

`broker` (the default) does not do this — it splices allowed TLS opaquely
and delivers no CA, so the guest keeps its end-to-end TLS to the upstream and
the operator sees only the destination. Reach for `mitm` only when you need the
plaintext (credential swap, content inspection). Even under `mitm`, a
destination that must not be read — or cannot tolerate interception (certificate
pinning, mutual TLS) — should be marked
[passthrough](#allow-vs-passthrough) so it is forwarded opaquely.

This distinction also applies to encrypted DNS. In `mitm`, HTTP/1 requests
using the `/dns-query` path or `application/dns-message` media type are denied
before reaching upstream and audited with `signal: dns-over-https`. In
`broker`, arbitrary DNS-over-HTTPS is not observable inside opaque TLS. A
locked allowlist still limits its possible destinations, but does not inspect
or classify the encrypted request. Status reports this distinction in
`egressCapture.encryptedDNS`; microagent does not maintain a resolver
blocklist.

### The per-workspace CA trust model

Interception works because each workspace gets its own certificate authority:

- On start, microagent mints a fresh ECDSA P-256 CA scoped to that one
  workspace.
- The CA's public certificate is delivered to the guest over a vsock channel
  at boot and installed into the guest's trust store (copied into the system CA
  bundle, `update-ca-certificates` is run, and `SSL_CERT_FILE` / `CURL_CA_BUNDLE`
  point at a combined bundle). So tools inside the guest trust the leaf
  certificates the mediator signs per-SNI.
- The CA's **private key never leaves the host.** The guest holds only the public
  cert; it can verify the mediator's leaves but cannot sign anything.

The CA is scoped to a single workspace and dies with it. There is no shared root,
no host-wide trust grant, and nothing the guest can use to forge a certificate.
A snapshot/restore re-arms the *same* CA the guest's baked trust store was built
against. microagent refuses to restore a mediated workspace whose persisted CA
fingerprint does not match, rather than silently breaking the guest's trust.

## UDP and DNS mediation

Mediation is not TCP-only. Under `broker` and `mitm`:

- **UDP** is captured transparently (via Linux TPROXY) and forwarded, with each
  datagram flow audited. In `broker` mode, UDP datagrams to inside addresses
  are denied and recorded as `egress_udp_internal_deny`. Allowed UDP flows
  retain the guest socket's source port on the upstream leg so protocols that
  negotiate return endpoints, such as RTP/RTCP, keep working through mediation.
  Stateful replies may return from another port on an active, allowed peer IP;
  replies from every other IP are dropped and recorded as
  `egress_udp_reply_deny`. If the guest source port cannot be retained, the flow
  fails closed and records `egress_udp_dial_error`; it never silently falls
  back to a different port.
- **DNS** is mediated by making the mediator the guest's resolver. Every query
  is forwarded to the real resolver and the answers are recorded (the
  name-to-IP mappings are also used to police later flows by hostname). In
  `broker` mode DNS resolves freely — even for names
  that point at internal IPs — but the resulting TCP/UDP connection is denied
  at connect time on the resolved IP, which also defeats DNS rebinding attacks.
  With a locked allowlist the mediator only resolves allowlisted names; a query for a
  non-allowlisted name is answered `REFUSED` without ever being forwarded. The
  guest learns no IP, which blocks DNS tunneling and DNS-based exfiltration
  before any connection is attempted.

Destinations are policed by hostname, not just IP: the SNI of a TLS
connection, the HTTP `Host` header, or a name the guest resolved through the
mediator. Guest IPv4 traffic that is neither TCP nor UDP (ICMP and
the like) carries no allowlistable destination and is dropped and audited rather
than forwarded. Guest IPv6 egress is dropped fail-closed while v4-only mediation
ships, so nothing slips past the v4 capture.

### Host requirement: TPROXY (and fail-closed)

UDP and DNS mediation depend on the kernel's TPROXY support — the
`nft_tproxy` module and the `nf_tproxy_ipv4` helper it pulls in. On most
hosts nothing needs doing: the kernel autoloads them the first time a
mediated workspace's steering rule is installed. When that first boot cannot
trigger the autoload:

- [`microagent doctor`](/cli/doctor/) verifies TPROXY support by installing
  a probe steering rule in a scratch network namespace — the same operation a
  mediated boot performs. Its verdict covers autoloaded and built-in modules,
  not just what a module listing shows.
- If doctor reports it unavailable, load the module once, as root, with
  `sudo modprobe nft_tproxy` (its dependency loads with it).

If a mediated (`broker` or `mitm`) workspace lands on a host where TPROXY
cannot be set up — a kernel built without it, or a policy blocking the rule
install — the workspace fails closed. The boot aborts before the guest runs
rather than running with an unmediated UDP/DNS channel. The error
names the fix:

```text
egress: UDP mediation (TPROXY) unavailable for workspace research — ensure the host kernel provides TPROXY support (e.g. the nft_tproxy/xt_TPROXY module) or use --egress off
```

Load the module as shown above, or use `--egress off` if you want no
mediation at all.

## Allow vs passthrough

Two ways to permit a destination, and they are not the same:

- **allow** (`--egress-allow <host>`) - the connection is permitted; under
  `mitm`, its TLS is also intercepted so microagent can read and audit the
  plaintext. This is the normal allowlist entry.
- **passthrough** (`--egress-passthrough <host>`) - the connection is permitted
  but not intercepted. It is forwarded as an opaque L4 byte stream. The
  original server certificate reaches the guest untouched, and microagent
  records that the connection happened (and how much data moved) but cannot
  see the payload.

Passthrough is the escape hatch for endpoints that break under interception:
certificate-pinned clients, mutual-TLS endpoints, or any client carrying its own
root store that would reject the injected per-workspace CA. You trade payload
visibility for compatibility - the connection is still allowed and still audited
as a connection, you just can't inspect what crossed it. See
[Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails-under-mitm)
for the symptom that tells you to use it. Under the default `broker` mode
nothing is intercepted in the first place, so passthrough mostly matters when
you've opted into `mitm`.

With a locked allowlist, both allow and passthrough entries are reachable;
everything else is denied. Without the lock, public destinations are already
reachable (the allowlist is not required); an `--egress-allow`
entry additionally overrides the inside-deny for that specific host (see
[Allowlist exception under broker](#allowlist-exception-under-broker)).

For the flags, the `.suffix` matching form, and the policy file, see the
[allowlist and passthrough how-to](/guides/egress-allowlist/).

## Credential swap

A capability built on top of interception: for an allowlisted, intercepted host,
microagent can inject a real credential host-side so the guest never holds
the secret. The agent makes an unauthenticated (or placeholder) request to the
allowed host. The mediator parses the request and injects the real credential -
acquired by a `static`, `oauth2-cc`, or `jwt-bearer` strategy - before forwarding
it upstream. The secret stays on the host, out of the guest's filesystem and
memory. This is related to, but distinct from, [delivering secrets into the
guest](/guides/secrets/); reach for credential swap when you want the agent to
use a credential it should never be able to read.

Enable it with `--egress-swap-config <path>` on [`run`](/cli/run/) or
[`create`](/cli/create/) — it requires `--egress mitm` (credential swap needs
TLS interception), and the target host must be allowlisted. The file declares named swap entries:

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

Every strategy's acquire-and-inject data path — including the fail-closed
cases (an unreachable token endpoint, an invalid token response, a
near-expiry token that must be re-acquired rather than reused) — is proven
end to end against a real in-process mediator, not just at the unit level.
`static` additionally has a live Linux/KVM E2E scenario (`--cred-swap`, a
built-in provider) proving the CLI-to-mediator wiring boots a real guest
under it; `oauth2-cc` has a live scenario proving the same wiring for a
hand-authored entry (no built-in provider shorthand exists for it yet), but
without a built-in provider that live scenario stays hermetic — it does not
carry a live guest request through a real token exchange the way the
in-process proof does. `jwt-bearer` is proven at the acquisition level
(signing a valid assertion) but has neither a full-mediator nor a live E2E
proof yet.

### Provider shorthand: `--cred-swap`

For the common case — a built-in LLM/API provider — `--cred-swap PROVIDER[=ref]`
generates the entry above for you. `--cred-swap openai` allowlists `api.openai.com`,
injects `Authorization: Bearer {key}`, and resolves the key from `env:OPENAI_API_KEY`;
add `=ref` to point at a different reference (`env:NAME`, `file:PATH`, or
`vault:PATH`). The reference is never a literal secret — a literal is rejected up
front so it can't land in shell history. Built-in providers: `anthropic`, `openai`,
`gemini`, `groq`, `openrouter`, `deepseek`. The flag is repeatable and composes with
`--egress-swap-config` (entries are merged; a name collision is an error).

```bash
microagent dispatch --egress mitm --cred-swap anthropic \
  some-image  node agent.js     # agent calls api.anthropic.com with a key it never sees
```

This protects the task credentials a guest uses, not the agent's own auth.
The guest never holds the swapped key, so a prompt-injected agent cannot
exfiltrate it. Other data in the workspace is a separate concern; you still
choose the egress envelope around it.

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
| `egress_deny` | A TCP connection denied fail-closed (off-allowlist under a locked allowlist); carries `signal: denied` |
| `egress_internal_deny` | A TCP connection denied because the resolved destination IP is an inside address; includes `internal: true` and `dst` fields, and `signal: denied` |
| `egress_mitm_handshake_error` / `egress_mitm_upstream_error` | A TLS interception problem (see [Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails-under-mitm)) |
| `egress_dns_allow` / `egress_dns_deny` | A name resolved / REFUSED |
| `egress_dns_reply_error` | A resolved answer could not be delivered back to the guest (the guest sees a timeout even though the name was allowed and resolved) |
| `egress_udp_allow` / `egress_udp_deny` / `egress_udp_close` | A UDP flow permitted / denied / closed; allow records include the guest `src` and actual `upstream_src` endpoints |
| `egress_udp_dial_error` | An allowed UDP flow could not open its upstream socket while retaining the guest source port; no datagram was forwarded |
| `egress_udp_reply_port_change` | The first stateful reply on an association that arrived from a different port on the active allowed peer IP |
| `egress_udp_reply_deny` | A datagram reached an active guest UDP association from an IP with no active allowed flow and was dropped |
| `egress_udp_internal_deny` | A UDP datagram denied because the destination IP is an inside address; includes `internal: true` and `dst` fields |
| `egress_cap_exceeded` | A bounded-operations cap tripped |
| `egress_loop_guard` | The mediator's own forwarding leg, dropped to avoid a self-loop |

An `unlisted: true` field marks a destination permitted only because of
an allow-broad mode's public grant (it is on no allowlist), so the audit distinguishes
the looser grant from an explicitly allowlisted one. This audit log is a separate stream
from lifecycle [`events`](/cli/events/): `events` is how the workspace got to its
state, `egress` is what it tried to reach and how the mediator ruled.

### Non-cooperation signals

A well-behaved workload never tries to route around the mediator, so any
attempt to do so is treated as an anomaly. When the mediator
detects one it stamps a `signal` field (from a small closed vocabulary) on
the audit record it already writes. The mediator only detects and emits; the
response is left to the consumer (a platform above microagent can map a
signal to alert, halt, or quarantine):

| `signal` | Meaning |
|---|---|
| `denied` | Any fail-closed drop — an inside/metadata destination, or an off-allowlist destination under `--egress-lock-allowlist` |
| `direct-ip-no-sni` | An allowed connection to a bare public IP with no SNI: permitted under allow-broad, but unusual — a cooperative client resolves names first |
| `quic-udp443` | A non-STUN UDP:443 attempt, normally QUIC / HTTP-3. QUIC and unknown traffic are denied so clients fall back to governed TCP/TLS; strictly framed STUN continues through normal destination policy and audited UDP mediation |
| `foreign-resolver` | A DNS query aimed at a public resolver address — an attempt to use a resolver other than the mediator. The guest cannot reach it, but the attempt is recorded |
| `dns-over-https` | A DNS-over-HTTPS request identified and denied from HTTP semantics in `mitm` mode |
| `unresolved-secret-ref` | A [broker](#the-broker-decision-stream) request carrying a credential reference that could not be resolved (a fail-closed workload error) |

## The broker decision stream

A workspace with an [egress broker](/cli/create/) configured
(`--broker-upstream` / `--broker-secret`) records a second, request-level
stream alongside the mediator's connection-level log: one record per brokered
request, written by the host companion, never by the guest. Broker endpoints
run on both supported backends — Linux serves them in the supervisor's
vsock-listener companion, macOS in a dedicated host companion the supervisor
spawns and terminates with the VM. Both run the same endpoint server, so
credential handling, decision records, and CONNECT gating are identical.
[`microagent egress`](/cli/egress/) merges both into one time-ordered view.

### Multiple broker endpoints

A single workspace can declare more than one broker endpoint — for a workload
that must reach several credentialed upstreams (say, two different
first-party APIs) with each credential injected independently and never
mixed. Repeat [`--broker-endpoint`](/cli/create/) instead of the single
`--broker-upstream`/`--broker-secret` pair, once per endpoint:

```bash
microagent run --network isolated \
  --broker-endpoint "upstream=https://a.example.com;secret=apiA=env:API_A_KEY;base-url-env=API_A_URL" \
  --broker-endpoint "upstream=https://b.example.com;secret=apiB=env:API_B_KEY;base-url-env=API_B_URL" \
  some-image  node agent.js
```

Each endpoint is fully self-contained: its own upstream, its own credential
reference, its own guest base-URL env, and (optionally) its own `ca=<path>`
upstream trust bundle. The transport details — the vsock port and the guest's
local listen address — are assigned automatically so endpoints never collide;
the guest only ever needs the base-URL env each endpoint pointed at it. A
`--broker-endpoint` spec cannot be combined with the single-endpoint
`--broker-upstream`/`--broker-secret`/`--broker-env`/`--broker-proxy`/
`--broker-capture`/`--broker-ca` flags — declare each endpoint fully within
its own spec. The equivalent Agentfile form is an `agent.brokers` list (instead
of the single `agent.broker` block); the MCP `workspace.create` and
`workspace.dispatch` tools take the same specs in a `brokers` array. All endpoints in a set share the single
`broker-access.jsonl` decision trail below, distinguished by upstream host.
Only one endpoint in the set may claim the guest-wide `HTTPS_PROXY`/
`HTTP_PROXY` slot (`proxy` on more than one endpoint is rejected).

| Record | Meaning |
|---|---|
| `broker_request_allow` | A brokered request completed; carries `mode` (`terminate` or `connect`), `method`, `host`, upstream `status` (terminate only), `bytes_out` / `bytes_in`, `duration_ms`, and `secret_refs` — the names of the credential references the broker swapped, never the values |
| `broker_request_deny` | A brokered request refused, with the `rule` that decided it (`unresolved-secret-ref`, `upstream-error`, `denied` for a refused CONNECT tunnel, or a policy rule) |

### The CONNECT tunnel is governed

A broker endpoint can optionally serve the guest-wide `HTTPS_PROXY`/`HTTP_PROXY`
slot (`proxy` in the endpoint spec). That HTTP CONNECT tunnel is off by
default - a terminate-only/base-URL endpoint answers CONNECT with `405` and
can never tunnel. Where enabled, the tunnel is governed like the rest of
egress: the broker resolves the target and denies fail-closed if any resolved
IP is an inside/infrastructure address (the same address space the mediator
denies). It then dials the exact IP it just classified, never re-resolving,
so a DNS rebind cannot swap an allowed answer for an inside one. An endpoint
can also lock the tunnel to named hosts with a per-endpoint CONNECT allowlist.
Both refusal paths stamp the `denied` signal on the decision record.

The default record is metadata only: no request path, no headers, no bodies.
Content cannot appear in it by schema, so it is safe to tail, persist, and
export. The live credential cannot appear in it either, because everything
the broker records is captured before the reference is swapped for the
secret.

### Governed raw capture

`--broker-capture` (or `agent.broker.capture` in a spec) opts in to capturing
the full pre-swap request — path, headers with the `@secret:` references
verbatim, and a bounded body prefix — to a separate owner-only
`broker-capture.jsonl` in the workspace state. Capture is request-only:
requests are recorded pre-swap, so the injected credential is absent by
construction. Responses have no swap point (an upstream could echo the
injected credential back), so they are never captured. What capture records is
the workload's own request data — an operator observing their own workload.
So it is a declared opt-in (persisted in the workspace manifest), never a
silent default, and retention/access of the capture file is the operator's
responsibility.

## See also

- [Allowlist and passthrough how-to](/guides/egress-allowlist/) - the flags, the `.suffix` form, and the policy file
- [`microagent egress`](/cli/egress/) - view the audit decisions
- [Networking](/concepts/networking/) - network modes and the (separate) mediation channel
- [Troubleshooting](/troubleshooting/#an-allowed-hosts-tls-connection-fails-under-mitm) - what to do when an allowed host's TLS fails
- [Deliver secrets](/guides/secrets/) - the related credential-delivery path
