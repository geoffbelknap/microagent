# microagent — ASK conformance scope

**Framework version: ASK 2026.07.** Assessed against `edc3219c7b3b3f71c5a4a1c746fd23db3ceef388`, 2026-08-02.
**Mode:** Mixed. Nineteen invariants were tested live against a running instance built from this
commit on an authorized local Linux/KVM (Firecracker) host; the other nineteen are source-read. See
the Mode column per invariant.

[ASK](https://askframework.org) states 38 invariants and 14 principles that must hold for an agent
deployment to be governed. This document states, for each invariant, where microagent stands — a
scope declaration, not a certification.

microagent is a substrate, not an agent deployment. It owns the Workspace element outright
(Firecracker/Apple-VF microVMs, boot-artifact hashing, lifecycle control) and the mechanism half of
the Mediation Layer, Audit Log, and Human Override — egress interception, host-only append-only
logs, and halt/kill/pause/resume/quarantine as commands the guest cannot reach. It owns none of the
Model, Context, or Runtime layers, and has no principal, trust, or authority system of its own.
Closing a Delegated verdict requires the caller or the in-guest agent framework to build that
missing layer on hooks microagent exposes but never interprets: an identity field, a signal
vocabulary on denied egress, an MCP `principal_scope` label, and a `Principal` model that
`microagent init` scaffolds for new callers. Delegated is not a pass.

**Provenance:** Drafted by Claude (Fable 5) from a live session against a workspace built and run
from this commit, plus a source read of every invariant with no live analog. Nineteen invariants
carry live evidence: the guest was booted, probed from inside, tampered with from the host, halted
mid-task, killed mid-action, and quarantined, with the resulting audit files read directly. Every
live finding that produced a Gap was re-run at least once before being recorded, and the four most
consequential (`mediation-complete`'s allowlist bypass, `runtime-known`'s unverified rootfs,
`enforcement-fails-closed`'s silent component death, `halts-auditable`'s lost writes) were each
confirmed against the source path that produces the behavior, cited below. Not verified live: the
Apple VF backend, which was unavailable in this session — every verdict here describes Linux/KVM.

## Verdict summary

Four verdicts:

- **Satisfied** — a named mechanism holds the property at microagent's own layer.
- **Delegated** — the property lands on a layer microagent doesn't own; the entry states what
  closing it requires of the other party.
- **Not applicable** — microagent has no surface the invariant governs.
- **Gap** — microagent's own layer owns this property and doesn't hold it. No partial credit: a
  half-fixed property is still Gap, with which half is fixed stated in the one-line reason.

| Verdict | Count |
|---|---|
| Satisfied | 8 |
| Delegated | 6 |
| Not applicable | 14 |
| Gap | 10 |

Gaps cluster around four root causes:

- **Enforcement decisions are keyed on what the guest says, and enforcement liveness is never
  reported.** The egress allowlist admits a flow based on the guest-supplied SNI or Host header,
  never checking it against the destination actually dialed; and when the mediator process dies,
  nothing records it while `status` keeps reporting complete coverage. Drives `mediation-complete`
  and `enforcement-fails-closed`.
- **The rootfs is the one boot artifact never compared.** Kernel, init, and config disk are hashed
  and re-checked; the rootfs hash is echoed back from the record without reading the file. Drives
  `runtime-known`.
- **The audit streams share no join key.** `events.json`, the egress log, the secret-access log,
  and the broker log are separate files correlated only by directory and timestamp. Drives
  `trajectory-recorded` and `incident-record-complete`.
- **Command dispatch doesn't compose, bound, or step up.** Capability grants are validated one
  field at a time, several operational dimensions have no default bound, destructive verbs execute
  instantly for whoever can invoke them, and lifecycle records carry no initiator or reason. Drives
  `capability-composition-governed`, `operations-bounded`, `verification-proportional`, and
  `halts-auditable`. `constraint-history-immutable` sits alongside it: config is overwritten in
  place, never versioned.

### Full table

| Slug | Verdict | Mode | Why (one line) |
|---|---|---|---|
| `constraints-external` | Satisfied | Live | Guest sees two block devices; host state, audit, and policy paths do not exist for it |
| `mediation-complete` | Gap | Live | Choke point complete, but allowlist admits on guest-asserted name; DoH unblocked by default |
| `model-output-mediated` | Not applicable | Static | No agent loop or model-output dispatch in microagent's own code |
| `enforcement-fails-closed` | Gap | Live | Mediator death denies all egress (no bypass) but is never recorded; status still reports complete |
| `runtime-known` | Gap | Live | Kernel/init/config tamper detected; rootfs never compared, so a tampered rootfs boots clean |
| `containment-matches-context` | Not applicable | Static | No declared-context concept; `Profile` is resource sizing only |
| `constraints-atomic` | Not applicable | Live | No mid-session constraint delivery exists; live apply refuses anything but port-bind changes |
| `constraints-survive-compaction` | Not applicable | Static | No Context or compaction concept exists |
| `actions-traced` | Satisfied | Live | Host-only writers, no guest path, records survived SIGKILL mid-action |
| `trajectory-recorded` | Gap | Live | Egress records carry no request or workspace identity; nothing joins the four streams |
| `provenance-mediated` | Not applicable | Static | "Provenance" here is OCI image lineage; no output-marking mechanism |
| `authority-logged` | Delegated | Live | Same fidelity as ordinary events; every actor is recorded as `workload` |
| `incident-record-complete` | Gap | Live | Quarantine record carries capture tag only; no what-was-reached, what-data, or objective |
| `constraint-history-immutable` | Gap | Static | Manifest and config disk overwrite one fixed path; no prior-state archive |
| `identity-mutations-recoverable` | Not applicable | Static | `Identity` is uninterpreted transport metadata, not agent memory |
| `knowledge-durable` | Not applicable | Static | No knowledge store; volumes are opaque disk-attach primitives |
| `capability-declared` | Satisfied | Live | Fixed listener set; only the result port is guest-reachable and it grants nothing |
| `capability-composition-governed` | Gap | Static | Every validator checks one field alone; no cross-capability composition check exists |
| `operations-bounded` | Gap | Live | Event retention bounded by default; lease, byte, rate, and workspace-count bounds are not |
| `delegation-bounded` | Not applicable | Static | No agent-to-agent permission delegation exists anywhere |
| `labeled-delivery-enforced` | Not applicable | Static | No authorization-scope label on any output or knowledge item |
| `knowledge-access-bounded` | Not applicable | Static | `pkg/volume` is an attachment registry with no query or traversal semantics |
| `authority-derived-from-principal` | Delegated | Live | `identity.role` is shape-validated only; MCP `principal_scope` is published, never checked |
| `verification-proportional` | Gap | Live | Halt, kill, and quarantine execute instantly with no gate; delete's prompt takes `--yes` |
| `trust-declared` | Delegated | Static | No trust concept; a scaffolded `Principal` model and an unenforced MCP field are the hooks |
| `unverified-zero-trust` | Satisfied | Live | Unresolvable secret refs hard-fail; plaintext schemes are warned, never silently accepted |
| `instruction-channel-distinct` | Delegated | Static | Mediation channel is a raw guest-initiated byte pipe; typing is the host listener's job |
| `external-agents-cannot-instruct` | Satisfied | Static | "Peer workspace" is reachability classification for audit, never an instruction path |
| `trust-not-self-elevated` | Not applicable | Static | No trust tier or elevation mechanism exists to self-elevate |
| `reasoning-not-emitted` | Satisfied | Static | Model responses stream through as opaque bytes; only counts and scalars are inspected |
| `halts-auditable` | Gap | Live | Halt works and reverses, but records no initiator or reason, and unflushed guest writes are lost |
| `boundary-violation-halts` | Delegated | Live | Denied flow was logged with a signal and the workspace kept running, by stated design |
| `halt-authority-asymmetric` | Delegated | Live | No caller-authority check gates any verb; asymmetry needs a principal system microagent lacks |
| `quarantine-complete` | Satisfied | Live | Severed in 0.8s with forensic capture, no guest notice, and no path out from inside |
| `hierarchy-inviolable` | Satisfied | Live | Only the result port is guest-reachable; a forged lifecycle payload on it was inert |
| `authority-never-orphaned` | Not applicable | Static | No coverage-principal concept exists |
| `lifecycles-independent` | Not applicable | Static | The workspace is the only stateful entity; no separate principal lifecycle exists |
| `oversight-capacity-enforced` | Not applicable | Static | No approval queue or oversight-demand concept exists |

---

## Evidence

### Enforcement sits outside the agent

**`constraints-external`.** From inside a running guest, `/proc/mounts` showed only the rootfs,
devtmpfs, proc, sysfs, and devpts — no host path and no enforcement artifact. Reading the host's
`egress-access.jsonl`, `events.json`, `runtime.json`, and the rootfs image each returned "No such
file or directory," and a write into the host state directory failed with "Directory nonexistent."
The guest's two block devices are its rootfs and a config disk carrying only `run.json` (command
and environment); a byte scan of that disk for `example.com`, `allowlist`, `egress`, and `secret`
found none of them, so the egress policy in force is not merely unwritable from the guest, it is
not present there at all.

**`mediation-complete`.** The choke point itself is complete: every "direct" connection tried from
the guest was intercepted and logged, including bare-IP TCP, DNS to an external resolver (stamped
`foreign-resolver`), and a connection to the host gateway, which was refused with
`egress_internal_deny`. What fails is the policy decision on top of it. With
`--egress-allow example.com --egress-lock-allowlist`, a guest connecting to `1.1.1.1:80` — not
allowlisted — while sending `Host: example.com` was allowed and received 300 bytes of real upstream
response; the same connection with an honest `Host` header was denied. `Policy.AllowHost`
(`internal/egress/policy.go:54-67`) evaluates the sniffed SNI or Host string alone, and the audit
record it wrote proves the mismatch it never checks: `{"dst":"1.1.1.1:80","event":"egress_allow",
"host":"example.com"}`. The bypass reproduced identically in `mitm` mode, and separately, DoH to
Cloudflare returned `HTTP/1.1 200 OK` under the default `broker` mode, which ASK's test requires be
blocked. See the deep dive.

**`model-output-mediated`.** Not applicable. `pkg/modelrunner` launches `llama-server`/vLLM as an
OpenAI-compatible subprocess (`pkg/modelrunner/process.go:30`) and nothing reads a completion and
dispatches it; a search for `tool_call|function_call|ParseCompletion` outside tests and scaffold
templates hits only the CLI's own MCP protocol plumbing. There is no Runtime here to mediate — the
loop that would turn model output into execution belongs to whatever workload the guest runs.

**`enforcement-fails-closed`.** Killing the egress mediator with SIGKILL cost the guest its network
outright rather than opening a bypass: TCP to `1.1.1.1:443` returned connection refused, name
resolution failed, and UDP DNS timed out. That is the invariant's stated property, and it holds. It
is the surrounding bullets that do not: nothing was written when the component died — `events.json`
gained no entry — and `status` afterwards still reported `state: running`, `guestReady: true`, and
`egressCapture: {coverageStatus: "complete", bypassResistance: "host-enforced"}` for a workspace
whose mediator was gone. An operator reading that output cannot tell that a safety-tier component
is dead.

**`runtime-known`.** Tampering with the config disk of a running workspace was caught exactly as the
invariant requires: `verification.ok` went false with a divergence naming the artifact, field,
expected hash, and actual. The rootfs is the exception. `shouldCompareRootfs`
(`pkg/workspace/lifecycle_status.go:308-310`) returns true only for an empty or unknown state, and
for a halted workspace `rootfsArtifactForStatus` (`:327-344`) copies the recorded hash into the
current-hash field without reading the file. Live, a halted workspace whose rootfs had been
modified on the host reported `verification.ok: true`, `divergence: null`, and identical
recorded/current hashes while the file on disk hashed to something else entirely — then started
without complaint. See the deep dive.

**`containment-matches-context`.** Not applicable. No declared-security-context concept exists;
`Spec.Profile` (`pkg/workspace/spec.go:53-55`) selects CPU, memory, and disk size, and nothing
cross-checks a risk classification against a containment floor before boot.

**`constraints-atomic`.** Not applicable. There is no mid-session constraint delivery to make
atomic: `WriteConfigDisk` is called only from `Create`, `Run`, and `Start`
(`pkg/workspace/lifecycle_create.go:87,140,309,470`), and `apply` against a running workspace
refused a network-mode change live with "live network apply only supports host bind changes for
existing port forwards; stop and start ask-locked to apply this change." Constraints are fixed at
boot and changed only by a restart.

**`constraints-survive-compaction`.** Not applicable. A search for `compaction|context window|
context compression` across the tree returns zero non-test hits; microagent holds no LLM context of
its own to transform.

### Everything is on the record

**`actions-traced`.** All four log writers — `eventhistory.Append`, `secretxfer.AppendAccessRecord`,
the broker's `appendEndpointJSONL`, and the egress mediator's logger — are host-side only, and a
search of `cmd/microagent-guestinit` for any of them returns zero hits. Live, the guest could not
read or write any log path, and after a SIGKILL landed mid-action the egress log still held all 44
records written up to that instant. Two caveats belong in the same breath: `microagent delete`
removes the whole per-workspace directory, so append-only holds per writer rather than against
wholesale deletion, and `result.json` is guest-authored by design — a guest wrote arbitrary JSON
into it, including a forged `role: enforcer`, which the host neither adopted nor treated as an audit
record.

**`trajectory-recorded`.** The streams do not join. A live egress record carried exactly
`event, id, qname, qtype, signal, ts` — no request ID, no workspace name — while the matching
`events.json` entry carried `identity.requestID` and `identity.runtimeID`. `MergeEgressEvents`
(`pkg/workspace/egress_audit.go:85-94`) interleaves the egress and broker streams by timestamp
string alone and never touches `events.json` or the secret-access log. Reconstructing what a run
did means correlating four files by directory and timestamp window, and no single source carries
objective through action to effect.

**`provenance-mediated`.** Not applicable. Provenance in this codebase means OCI image build lineage
(`pkg/rootfs/types.go:143`); a case-insensitive search for `watermark|machine-generated|c2pa`
returns zero non-test hits, and `result` transports guest stdout and stderr verbatim with no
annotation.

**`authority-logged`.** Halt, kill, pause, resume, quarantine, stop, and delete all write through the
same `Control` → `eventhistory.Append` path as ordinary transitions
(`pkg/workspace/lifecycle_control.go:96-131`), so a governance action gets no weaker record than a
routine one — that parity is real. But every live event across a quarantine and its release carried
`role: workload`, because the CLI always sets it there (`cli_workspace_state.go:81`) and
`vmkit.Identity.Role` admits only `workload` or `enforcer` as component labels. Attribution needs an
identity layer above microagent, which must then populate a field microagent will faithfully record
and never overwrite.

**`incident-record-complete`.** `Quarantine` captures forensics before containment and returned
`{"captured": true, "captureTag": "forensic-20260802-172725"}` live — a real capture, reachable by
the operator under `snapshots/`. But that is the whole incident record: no field names what the
workspace reached, what data was involved, or to what end. Those facts live in the separately
correlated egress, broker, and secret logs, and a search for `objective` returns zero hits, so the
agent's purpose is not a thing microagent can record.

**`constraint-history-immutable`.** `WriteManifest` (`pkg/workspace/lifecycle_state.go:19-25`) and
`WriteConfigDisk` (`pkg/workspace/configdisk.go:274-323`) each write one fixed path via
temp-file-then-rename, and no per-write history file exists. `status` compares current against
currently-recorded only, so "what configuration was in force at time T" is unanswerable for any T
before the last write.

**`identity-mutations-recoverable`.** Not applicable. `vmkit.Identity` (`pkg/vmkit/types.go:65-71`)
is `RequestID`, `RuntimeID`, `Role`, `Backend`, `HomeHash` — routing metadata whose only use is
keying file-path lookups. No agent-memory or identity-mutation concept exists to make recoverable.

**`knowledge-durable`.** Not applicable. `pkg/volume` states its own scope: single-attach,
VM-independent ext4 disks. Its `Record`/`Index` types form an attachment registry, and the volume's
contents are an opaque block device, not structured agent-contributed knowledge.

### Capability is granted, never taken

**`capability-declared`.** Network mode, mounts, secrets, and egress mode are fixed in the manifest
at `Create` before any VM boots, and the guest-facing vsock listener set is built once
(`pkg/workspace/workspace.go:1195-1237`) with no grant verb anywhere. Live, of six vsock ports
probed from the guest only the result port accepted a connection; the rest reset. `Apply` is the
one post-create mutation path and it is host-side and narrow — restart policy and network only
(`pkg/workspace/apply.go:36-56`) — and it refused a live network change outright. The guest has no
way to ask for more of itself.

**`capability-composition-governed`.** Each validator in `pkg/workspace` — `ValidateName`,
`ValidateDisk`, `ValidateResources`, `validateBrokerEndpointSet` — checks a single field in
isolation, and a search for `composition|combined grant|cross-capability` outside tests returns
nothing. There is no capability-category vocabulary and no cross-reference between mounts, secrets,
and egress mode, so a workspace holding all three at once meets no rule that considers the
combination.

**`operations-bounded`.** Bounded by default: event retention, at `DefaultMaxEvents = 1024`
(`internal/eventhistory/eventhistory.go:15`), applied unconditionally. Not bounded by default:
`LeaseSeconds` is zero for a persistent workspace, which the field's own comment defines as "no
bound — the VM is permanent and is never reaped for age" (`pkg/vmkit/types.go:192-195`), and the
live workspace confirmed `leaseSeconds: None`; egress byte and rate caps are opt-in with zero
meaning unlimited; and no workspace-count ceiling exists anywhere, configurable or otherwise. The
codebase tags this invariant by name in its own comments (`internal/egress/mediator.go:35`), so the
bounds were built deliberately — as opt-in caps rather than safe defaults.

**`delegation-bounded`.** Not applicable. Twelve non-test matches for "delegat" across the tree are
all incidental English in doc comments (`pkg/sandbox/sandbox.go:4`, `pkg/workspace/dispatch.go:29`)
plus one free-text MCP field copied into response metadata and never checked. No agent-to-agent
permission delegation exists to bound.

**`labeled-delivery-enforced`.** Not applicable. The four non-test hits for
`authorization_scope|clearance|labeled` are SELinux process labels and a network alias comment.
`Artifacts` and `Output` (`pkg/workspace/workspace.go:346-413`) carry only a name and a path, so
there is no label on any deliverable to refuse.

**`knowledge-access-bounded`.** Not applicable. `pkg/volume` exposes `Create`, `List`, `Get`, and
`Attach`, gated only on whether a workspace already holds the disk (`pkg/volume/volume.go:340-362`).
There is no query or traversal API and no authorization-scope field to bound access by.

**`authority-derived-from-principal`.** `identity.Role` is validated for shape and nothing more —
`if identity.Role != RoleWorkload && identity.Role != RoleEnforcer` (`pkg/vmkit/types.go:641-642`)
— and is never compared elsewhere to permit or deny. The MCP surface goes one step further and then
stops: `mcpToolPrincipalScope` (`cmd/microagent/mcp_catalog.go:439-451`) labels every tool with the
scope it would need, but its only use is emitting `principal_scope` into the catalog at `:234`;
nothing checks a caller against it. Live, a forged `role: enforcer` sent by the guest changed
nothing about what the host permitted.

**`verification-proportional`.** Halt returned in 0.114s and quarantine in 0.836s, each with no
confirmation of any kind (`cmd/microagent/cli_workspace_state.go:108-123`). Delete does prompt for a
running workspace — "Workspace ask-mitm is running. Stop and delete it? pass --yes to confirm" — but
the prompt is same-session and `--yes` skips it. The MCP `preview`/`confirm_token` flow
(`mcp_confirmation.go:57`) is opt-in, excludes halt, kill, and quarantine entirely, and where it
does apply is a self-computable hash proving argument stability rather than a second approver.

### Trust is explicit, never assumed

**`trust-declared`.** No trust-relationship concept exists in microagent's own code; a search for
"trust" outside tests hits TLS trust stores and design prose. Two hooks gesture at one: an
unenforced MCP `principal` object whose keys (`workload_identity`, `delegated_authority`, `purpose`,
`correlation_id`) pass straight into audit metadata (`cmd/microagent/mcp_idempotency.go:204-219`),
and a `Principal` model in the Python template `microagent init` scaffolds, whose docstring states
"An agent that receives a request with `verified=False` must refuse it"
(`pkg/scaffold/templates/protocol.py:55-61`). Both are vocabulary handed to the caller, enforced by
nobody in this binary.

**`unverified-zero-trust`.** No trust tiering exists, but every unresolvable path refuses rather than
degrades. Live, `secret check TOK=env:NO_SUCH_VAR_ASK` returned `"secret \"env:NO_SUCH_VAR_ASK\"
resolved to an empty value"` as an error rather than an empty secret, and a resolvable plaintext ref
returned `ok: true` with a warning naming the scheme as unfit for production and a byte count in
place of the value. Refusing outright is at least as strict as assigning a lowest tier; no ambiguous
case resolves toward more access.

**`instruction-channel-distinct`.** The mediation channel is structurally distinct from ordinary
egress (`pkg/vmkit/types.go:275-281`), and the supervisor wires it as its own vsock listener
(`pkg/supervisors/firecracker/forwarding_linux.go:432`). But the transport is an undifferentiated
byte pipe from a guest-initiated vsock connection to a configured TCP target, with no
instruction-versus-data typing applied by microagent. `docs/concepts/boundaries.md` says so
directly: your listener decides what each call may do.

**`external-agents-cannot-instruct`.** The one place the codebase uses "peer" is east-west
reachability classification for audit: `peerName` reverse-resolves a destination IP to a named
workspace and `addPeerFields` stamps `peer`/`peer_ip` onto the record
(`internal/egress/mediator.go:313-334`), which also decides whether TLS interception applies. No
code path carries anything instruction-shaped from one workspace to another; each is opaque compute
to the other.

**`trust-not-self-elevated`.** Not applicable. A search for `trust.?tier|trustLevel|elevat` returns
zero non-test hits — there is no trust level to elevate and no mechanism that could raise one.

**`reasoning-not-emitted`.** The model proxy streams upstream responses through a 32KB buffer with
no JSON decode at any point (`internal/hostworker/mediator.go:496-548`), and request-side inspection
extracts only `Model`, `Stream`, `MaxTokens`, and byte counts, never message text. A
`reasoning_content` field in an upstream response passes through as opaque bytes — never parsed,
logged, or surfaced to a third party. This holds by not looking rather than by policy, which is
enough for the invariant but is not a redaction mechanism.

### Humans can always stop it

**`halts-auditable`.** Halt stopped a mid-task workspace from outside its process, left zero
firecracker processes, and restarted cleanly, so the reversibility half works. The record does not:
the event carries `identity`, `state: halted`, a serial-log pointer, and a timestamp — no initiator
beyond the constant `workload` role, no reason, no in-flight work, and no notified party, since no
notification mechanism exists in the tree. State preservation is also narrower than it reads. A file
written three seconds before halt was gone after restart, reproduced deliberately with a second
file, while an explicitly `sync`ed file survived: `stopWorkspace`
(`pkg/supervisors/firecracker/process_linux.go:489-545`) signals the firecracker process group
without a guest-side flush, and the durability contract's `halt` row does declare memory discarded
(`pkg/vmkit/durability.go:68`) — but unflushed page cache is exactly where an agent's last actions
live.

**`boundary-violation-halts`.** Live, a denied destination produced `egress_dns_deny` and
`egress_deny` records with a closed-vocabulary `signal` field and reason "not allowlisted" — and the
workspace stayed `running`. That is the stated design: `internal/egress/signals.go` says the
mediator only detects and emits, never decides the response, and no file under `internal/egress`
imports `pkg/workspace`'s control functions. Every crossing is logged and continued, which is
precisely what ASK's test calls a violation unless the consumer above supplies the halt.

**`halt-authority-asymmetric`.** Halt, kill, pause, resume, and quarantine all route through the same
`runWorkspaceStateCommand` (`cmd/microagent/cli_workspace_state.go:20-141`) into `workspace.Control`,
gated only on workspace existence and state-machine legality. Live, the same caller halted, killed,
quarantined, and released without presenting any credential, because there is none to present.
Distinguishing who may resume from who may halt requires the principal system named under
`authority-derived-from-principal`.

**`quarantine-complete`.** Quarantine severed a running workspace in 0.836s: forensic snapshot
captured first, then every companion process — firecracker, egress mediator, pasta — gone, exec
refused with "workspace ask-audit is not running; structured exec is unavailable in state
quarantined," and the state directory intact and operator-readable with the capture under
`snapshots/`. The guest received nothing resembling a warning; the serial log's last lines are the
snapshot's own resume. From inside there is no path out because nothing inside survives, and
`EnsureCanStart` rejects starting from `StateQuarantined`
(`pkg/workspace/lifecycle_state.go:365-369`). An operator can release it by kill-then-start, which
the refusal message states plainly and which `events.json` recorded as quarantined → stopped →
running.

**`hierarchy-inviolable`.** The guest-facing exec protocol defines only `ExecRequest`
(`pkg/workspace/exec/protocol/protocol.go:83-140`) — a command to run inside the guest — with no
lifecycle message type, and the host listener set never exposes a lifecycle verb to a guest-initiated
connection. Live, the result port was the only one of six the guest could reach, and a
lifecycle-shaped payload sent to it (`{"command":"halt","identity":{"role":"enforcer"}}`) was written
verbatim into `result.json` and had no effect: the workspace stayed running and `status` still
reported `role: workload`.

**`authority-never-orphaned`.** Not applicable. A search for a coverage-principal concept returns
zero non-test hits; no authority-transfer or fail-closed-on-suspension pattern exists because no
principal exists to suspend.

**`lifecycles-independent`.** Not applicable. The workspace is the only entity with persisted
lifecycle state. `RoleEnforcer` is defined and validated but never assigned in non-test code, so
there is no second stateful entity to hold an independent lifecycle.

**`oversight-capacity-enforced`.** Not applicable. The only "approval" in the tree describes an
external MCP client's own tool-call consent UI (`docs/guides/mcp-server.md:162-170`). No
approval queue, capacity threshold, or autonomy-reduction mechanism exists.

---

## Deep dives

### `mediation-complete` — a complete choke point with a forgeable key

Everything about the interception layer works. The guest cannot route around it: bare-IP TCP, DNS,
and DoH all landed in the audit log, a connection aimed at the host gateway was refused as an
internal destination, and the mediator's own bind address is loop-guarded. The comment at
`internal/egress/mediator.go:479-487` explains the design and is accurate about what it defends —
the inside-deny classifies the *resolved destination IP*, "which also defeats DNS rebinding, since
the IP — not a guest-supplied name — is classified."

The allowlist does not get that treatment. `Policy.AllowHost` takes a host string, and the host
string comes from sniffing the guest's SNI or HTTP `Host` header. Nothing compares it to the address
the connection is actually dialing. So with a locked allowlist containing only `example.com`, a
guest reached `1.1.1.1` — a destination it was never granted — by writing `Host: example.com` into
its own request, and got a real response back. It reproduced over TLS with a forged SNI, and again
in `mitm` mode, which forges certificates but still keys the decision on the same string.

Two things keep this from being a purely theoretical concern. ASK's premise is that the agent is
compromisable, and this is a bypass available to exactly that agent — no host access, no privilege,
just a string it controls. And the codebase demonstrates it knows this class of confusion: the
credential-swap path deliberately selects on the outer SNI rather than "the guest-controlled inner
Host header," because matching on Host would "let a guest send `Host: <another-swap-host>` ... and
have THAT host's credential injected" (`internal/egress/swap.go:128-133`). That defense reasons
about which guest-supplied name to trust for a secret; the allowlist decision accepts either name
and checks neither against the destination. The audit record does carry the true
`dst` alongside the asserted `host`, so a reviewer cross-checking resolution could detect it after
the fact — evidence exists even though enforcement doesn't. Closing it means binding an allowed name
to the addresses the mediator itself resolved for it, and refusing a flow whose destination isn't in
that set. The default `broker` mode's unblocked DoH is the same shape of problem one layer up: the
mediator sees the TLS flow but not the DNS question inside it.

### `runtime-known` — the artifact that is reported but not read

Three of four boot artifacts behave exactly as the invariant asks. Tampering with the config disk of
a running workspace produced `verification.ok: false` and a divergence record naming the artifact,
the field, the expected hash, and the actual one — attestation failing and reporting the divergence,
which is the test verbatim.

The rootfs takes a different path, and the reason is defensible: a running VM writes to its own
rootfs constantly, so comparing a live rootfs against its boot-time hash would report divergence on
every healthy workspace. `shouldCompareRootfs` therefore suppresses the comparison for any real
lifecycle state. The problem is what happens in the halted case, where no such excuse applies and
the file is quiescent. Rather than recompute and compare, `rootfsArtifactForStatus` assigns
`artifact.SHA256 = recorded.SHA256` — it reports the recorded value back as though it were a fresh
measurement.

The consequence is visible only live, because reading the code it looks like agreement. A rootfs
modified on the host while the workspace was halted produced a `status` output showing matching
recorded and current hashes with `verification.ok: true` and `divergence: null`, at a moment when
the file on disk hashed to something else entirely. Starting that workspace raised nothing either.
An operator asking microagent whether the workspace's disk is what it recorded receives a confident
yes derived from the record alone. Closing it means recomputing the rootfs hash in the halted and
stopped states — where drift is genuinely anomalous — and leaving the running-state suppression in
place, or recording a post-boot baseline to compare a halted workspace against.

---

## Principles

**Egress and capability scope** (`indirect-egress-declared`, `least-privilege`,
`bounds-calibrated`). `indirect-egress-declared` is implemented concretely: selecting the fully
unmediated path prints an unsuppressable warning naming exactly what is lost — "no mediation, no
audit, no cred-swap (yolo mode)" (`cmd/microagent/cli_workspace_options.go:423-427`) — so the
residual risk is declared rather than absorbed. `least-privilege` is asymmetric: mounts, secrets,
and ports are opt-in per workspace, but the default egress posture is allow-broad and the default
network reaches the public internet. `bounds-calibrated` inherits `operations-bounded`'s gap — a
bound that is off by default offers nothing to calibrate.

**Trust and authority judgment** (`trust-legible`, `trust-earned`, `authority-anomalies-reviewed`,
`implicit-capability-inferred`). None of these have a surface here: no trust relationship, trust
level, or task-to-capability inference exists anywhere in the codebase. That is the boundary the
repo states for itself in `docs/concepts/boundaries.md` — identity, policy, and authority judgment
belong to the caller's control plane. All four land entirely on that other party.

**Composition and impact judgment** (`synthesis-reviewed`, `impact-classified`,
`unknown-conflicts-yield`). `synthesis-reviewed` has no mechanism, tracking
`capability-composition-governed`: nothing flags a workspace whose combined grants exceed the risk
of any one. `impact-classified` exists only informally — the durability contract does distinguish
what each verb preserves and discards, which is the closest thing to an impact taxonomy, but
`verification-proportional` reads nothing from it. `unknown-conflicts-yield` describes an agent's
own behavior under ambiguity, which is the hosted workload's decision loop, not microagent's.

**Recorded-trajectory review** (`trajectory-reviewed`). Reviewing cumulative effect presupposes a
joined trajectory to review, and `trajectory-recorded`'s Gap means a reviewer must first join four
log streams by hand. The forged-`Host` finding is a concrete illustration: the evidence sat in the
audit log as a `host`/`dst` mismatch, detectable by anyone who thought to look, and nothing surfaced
it.

**Probing and reasoning exposure** (`probing-informs-trust`, `content-is-data`).
`probing-informs-trust` has no surface — there is no trust system to inform, and the signal
vocabulary that would feed one (`direct-ip-no-sni`, `foreign-resolver`, `denied`) is emitted for a
consumer that does not exist here. `content-is-data` holds trivially and narrowly: microagent never
parses the payload crossing the mediation channel, so it cannot promote content to instruction — but
only because it never interprets content at all. The real distinction is the host listener's to
enforce.

**Oversight capacity** (`oversight-calibrated`). No approval queue or oversight-demand concept
exists to calibrate, tracking `oversight-capacity-enforced`.

---

## What would change these verdicts

- **Bind an allowed name to the addresses the mediator resolved for it.** Refusing a flow whose
  actual destination isn't in that set, and treating a `host`/`dst` mismatch as a signal rather than
  a silent allow, closes the largest hole in `mediation-complete`.
- **Recompute the rootfs hash in the halted and stopped states.** Reporting a recorded value as a
  current measurement is what makes `runtime-known`'s gap invisible; comparing where drift is
  genuinely anomalous closes it without touching the running-state suppression that exists for good
  reason.
- **Record enforcement-component liveness as state.** An event when the mediator dies, and an
  `egressCapture` block that reflects the live process rather than the configured mode, would move
  `enforcement-fails-closed` from a silent failure to a visible one.
- **Give the four audit streams a shared join key.** Adding `RequestID` and `RuntimeID` to every
  egress, broker, and secret-access record would move `trajectory-recorded` and
  `incident-record-complete` toward Satisfied.
- **Give lifecycle commands a caller identity, and record it with a reason.** Populating an identity
  field microagent itself verifies, gating verbs on it, and carrying an operator-supplied reason
  would move `authority-logged`, `authority-derived-from-principal`, and `halt-authority-asymmetric`
  off Delegated and close most of `halts-auditable`. This one sits outside the scope boundary the
  repo states for itself, so it is a statement about what the layer above must build.
- **Default the bounds that are currently opt-in**, add a composition check across declared
  mounts/secrets/egress at create time, and put a real gate before `kill` and `quarantine` — closing
  `operations-bounded`, `capability-composition-governed`, and `verification-proportional`.
