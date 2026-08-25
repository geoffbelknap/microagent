# Changelog

Use this file for release notes and the rolling list of changes that have not
been cut into a release yet.

## Unreleased

## v0.10.0 - 2026-08-25

The bounded-and-mediated release: every workspace now carries default limits
on idle lifetime, egress volume, and host concurrency; broker endpoints
require an explicit assurance mode, with `semantic` validating each request
against a typed operation grant before it reaches the upstream service;
`quarantine` freezes and severs a guest's authority before it captures
evidence; and egress dropped at the datapath is recorded instead of
vanishing. Alongside those, linux-kvm cold boot through a working guest
command drops from roughly 1,200 ms to 460 ms, snapshot restore stops
panicking the guest and stops hanging on a liveness gate it could never
pass, built images keep the ownership and special mode bits their source
image declared, and every long-running command reports progress on one
surface.

### Breaking changes

- Broker endpoint declarations must name an assurance mode, `semantic` or
  `trusted-upstream`. A `semantic` endpoint also requires a grant file.
- Newly created workspaces are bounded by default: a 7-day idle TTL, 50 GiB
  cumulative and 256 concurrent egress caps under `broker` or `mitm`
  mediation, and a host-wide ceiling on running workspaces. `--ttl 0`,
  `--egress-max-total-bytes 0`, and `--egress-max-conns 0` still mean
  unlimited when set explicitly, and `MICROAGENT_MAX_WORKSPACES` sets the
  host ceiling. Workspaces created before this release keep their existing
  behavior.
- A rejected flag value now exits `1` as a permanent error instead of `75`
  (`EX_TEMPFAIL`) with `retryable: true`. A scripted retry loop keyed on the
  old classification no longer re-runs an invocation that cannot succeed.

### Every workspace is bounded by default, not just event retention

ASK tenet 8 (`operations-bounded`) requires every operational dimension to
have a limit that holds by default. Only event retention did; a persistent
workspace's idle TTL defaulted to permanent, egress byte/rate/concurrency
caps existed in the mediator but had no operator-facing surface to set them
at all, and nothing capped how many workspaces a host could run at once.

- **Idle TTL** now defaults to 7 days for a workspace created without
  `--ttl` (`create`/`create --from-snapshot`), instead of permanent.
  `--ttl 0` still means permanent — the default only fills in when the
  operator pinned nothing, including a genuine `0`.
- **Egress caps** (`--egress-max-total-bytes`, new; `--egress-max-conns`,
  new) now default to 50 GiB cumulative / 256 concurrent connections under
  `broker` or `mitm` mediation, instead of unlimited. `0` still means
  unlimited when set explicitly. A cap resolved at create time is fixed for
  that workspace's lifetime and round-trips through every later `start`
  unchanged.
- **Workspace count** is now capped host-wide: `create`, `create
  --from-snapshot`, and `start` fail closed once the number of
  running/starting/paused workspaces reaches the ceiling, computed from
  detected host memory (clamped to 4-100) or set explicitly with
  `MICROAGENT_MAX_WORKSPACES=<n>`.
- `microagent inspect`/`microagent status` report every bound actually in
  force under a new `boundedOperations` field, so an operator can see what's
  applied without reading defaults out of the source.

A workspace created before this change keeps its existing (unbounded)
behavior when restarted — these defaults only apply to newly created
workspaces, never retroactively.

### Interactive consoles detach and resize under full-screen applications

Full-screen terminal applications could make `microagent connect` appear to
ignore `Ctrl-]`. Those applications enable the extended terminal keyboard
protocol, which encodes the chord as a `CSI u` sequence instead of the legacy
single byte that `connect` recognized. The sequence reached the guest while
the host stayed attached.

Interactive shells also started on a fixed 80-by-24 PTY. Resizing the host
terminal did not reach the guest, so a TUI continued drawing with stale
dimensions and could corrupt its display.

`connect` now recognizes detach chords in both keyboard encodings. Guestinit
advertises a versioned console capability, `connect` sends the initial terminal
dimensions, and subsequent host resize events update the guest PTY. Capability
negotiation keeps older workspaces usable as byte-stream consoles; recreate an
old workspace to add live resize support.

### Fix a guest kernel panic on every linux-kvm snapshot restore

A guest that booted with the `XSAVES` CPU feature available could fault
repeatedly in `restore_fpregs_from_fpstate` (`#GP` on the `XRSTORS`
instruction) after a Firecracker snapshot restore, until the recursive fault
handling overran the task's kernel stack guard page and the guest panicked.
Reliably reproducible on this host (AMD Ryzen 9 5900X, Firecracker 1.15.1):
every restore of a guest booted before this change crashed the same way.

The guest kernel now boots with `clearcpuid=xsaves`, forcing it onto the
compacted-but-user-only `XSAVEC` save/restore path this bug does not reach
(`xsave`/`xsaveopt`/`xsavec` remain available; only `xsaves` drops out of
`/proc/cpuinfo`). This choice is baked in at the guest's original boot, not
at restore time, so a snapshot captured before this change still crashes on
restore — only workspaces (re)created after upgrading are fixed.

### Guest egress dropped at the datapath is no longer silent

Guest traffic whose protocol carries no allowlistable destination identity —
IPv4 ICMP and any other non-TCP/UDP L4 — is dropped at the firewall before it
reaches the egress mediator. That drop was invisible: `ping` from inside a
mediated workspace reported `100% packet loss` with nothing recorded anywhere,
indistinguishable from a dead network or an unresponsive host. The rule
emitted NFLOG group 5, but nothing in microagent subscribes to it, so the
detail went nowhere unless an operator happened to attach their own reader.

The drop rule now also carries a counter, and the mediator samples it while it
serves, reporting increases into the same audit log every other egress
decision lands in. A blocked ping now shows up in `microagent egress` with the
reason it was blocked, carrying the new `unmediatable-protocol` signal and the
packet count.

The policy is unchanged — these protocols are still dropped, deliberately, and
for the same recorded reason. Only the silence is fixed.

### Accelerate linux-kvm cold readiness without weakening startup checks

Firecracker guests now limit the kernel console to notices and more severe
messages. Routine kernel driver inventory no longer serializes cold boot on
the emulated UART, while kernel notices, warnings, errors, panics, Firecracker
diagnostics, and guest-init milestones remain in the serial log.

A fresh detached start can also finish its 500 ms early-exit observation
window after the guest successfully runs a structured no-op over direct vsock.
If the guest cannot prove that stronger liveness signal, the complete process
observation window remains in force. Direct-vsock connection handshakes honor
the readiness probe deadline.

On an AMD Ryzen 9 5900X with Firecracker 1.15.1, the tiny profile, and the
pinned NATS readiness image, cold boot through a successful structured command
improved from 745/754 ms to 457/459 ms in isolated mode. User networking
improved from 809/812 ms to 545/549 ms. These are p50/p95 results from 10
iterations with an uncontrolled host page cache. Snapshot restore remained at
101/107 ms isolated and 207/208 ms with user networking; paused resume remained
9/10 ms.

### Shorten linux-kvm cold boot by skipping an unused keyboard probe

Firecracker guests now skip the PS/2 keyboard-port probe. Microagent exposes
serial and vsock guest interfaces rather than a PS/2 keyboard, and the x86
reset path used for clean Firecracker shutdown does not require the input
driver. Guest-init serial logs also include microseconds so subsecond startup
milestones remain measurable.

The measurement host used an AMD Ryzen 9 5900X, Firecracker 1.15.1, the tiny
profile, and the pinned NATS readiness image. Cold boot through a successful
structured command improved from 1,198/1,202 ms to 745/754 ms in isolated mode.
User networking improved from 1,264/1,267 ms to 809/812 ms. These are p50/p95
results from 10 iterations with an uncontrolled host page cache. Snapshot
restore and paused resume timing stayed within the previous ranges.

### Containment freezes and severs authority before forensic capture

`quarantine` now creates a durable execution fence, freezes guest vCPUs,
severs network, broker, published-port, and serial authority, captures memory
and disk while the guest remains frozen, and only then stops the VM into
custody. The structured result reports freeze, severance, capture, stop, and
custody separately. Capture failure leaves the guest frozen and severed for a
safe retry or an explicit `--no-capture` evidence-loss retry. The marker blocks
ordinary start, resume, restore, mutation, workspace deletion, and deletion of
the custody snapshot after interruption or restart. Linux KVM and Apple
Virtualization.framework implement the same phase contract.

### A model runner restart no longer silently breaks paired workspaces (Linux/KVM)

Restarting a model runner (`microagent model stop` + `model serve`, including
any config change that forces a restart) used to leave every workspace
already paired to it silently broken: the runner came back on a new port,
the guest's forward still targeted the old one, and the agent inside saw a
bare `ECONNRESET` with nothing in any log naming the cause. The workspace
kept reporting `running` and `model runners` kept reporting the runner
healthy — nothing connected the two facts. The only fix was `microagent halt
<ws> && microagent start <ws>`.

On Linux/KVM, the guest-facing model forward now resolves the current runner
for its paired model on every connection instead of dialing the host:port
captured once at workspace start, so a runner restart no longer breaks
already-running workspaces at all. `microagent model stop` also now prints
the names of any workspaces still paired with the stopped runner, so an
operator who wants the fail-loud path can see who is affected before
restarting.

This is a Linux/KVM-only fix for now — Apple VF workspaces paired to a model
runner still need a manual restart after a runner restart, tracked as a
known backend gap.

### Prove the oauth2-cc credential-swap strategy end to end

`oauth2-cc` had acquisition unit-tested and injection proven only for the
`static` strategy through the real mediator — nothing established that a
guest request could traverse the complete oauth2-cc path (token exchange,
injection, caching) through a running mediator, or that it failed closed
correctly.

`internal/egress` now proves the full data path in-process against a
hermetic token endpoint: acquisition, header injection, cache reuse across a
second request, and three fail-closed cases (an unreachable token endpoint,
a token response missing `access_token`, and a token minted already within
the cache's expiry skew window, which must never be served twice). A new
`cred-swap-oauth2` Linux/KVM E2E scenario proves the CLI/lifecycle/mediator
wiring for a hand-authored oauth2-cc entry (there is no `--cred-swap`
provider shorthand for this strategy) boots correctly under `mitm` egress.

### Keep perf boot teardown outside the readiness timer

`perf boot` now stops timing after the guest command result and removes its
disposable workspace afterward. Reports expose the timer edges, teardown
exclusion, and uncontrolled host page-cache condition as structured fields, so
the command and its documented measurement contract agree.

The one-command performance snapshot now records exact component paths and
SHA-256 hashes. A checkout-built run refuses to summarize results if the CLI
resolved a guest init or supervisor other than the matched source build.

### The built rootfs now keeps the image's file ownership and special mode bits

`microagent rootfs build` (and every command that builds one, `run` and
`create` included) populated the ext4 image with `mke2fs -d` from a staged
directory on the host disk. `mke2fs -d` only ever encodes what `stat()`
reports for those files. Every path came back owned by whichever host user
ran the build instead of the uid/gid the image declared, and setuid, setgid,
and sticky bits were dropped along the way.

A guest with no root-owned files silently breaks anything that drops
privileges or relies on a shared sticky directory. The error rarely points
back to ownership. On a `golang:1.26-bookworm` image, `apt-key` (running as
`_apt`) failed to create a temp file in a `/tmp` that had lost its sticky
bit, and the failure surfaced as a signing error instead.

The builder now records each staged entry's uid/gid and mode (including the
special bits) alongside the existing stage metadata, then corrects the built
ext4 image in place with a `debugfs -w` batch script before publishing it.
`debugfs` edits inode fields directly on the unmounted image, so the
correction needs no host privilege at all. A new `--debugfs <path>` flag
resolves that binary the same way the existing `--mke2fs <path>` flag does.

### `--secret` now warns loudly that the guest holds the real value

`--secret`, `--secret-on-demand`, and `--secrets-env-file` deliver the real
credential value into the guest tmpfs. `--broker-endpoint`/`--cred-swap`
offer a fundamentally different, safer guarantee instead: the guest only
ever holds an `@secret:NAME` reference, and the real value never leaves the
host. The two take a similarly-shaped `NAME=<scheme>:<ref>` argument, which
made them easy to reach for interchangeably.

`create`/`run`/`dispatch`/`start` now print a warning to stderr, once per
invocation, whenever any of these flags is used, naming the broker as the
alternative when the guest doesn't need to hold the credential itself. The
`--help` text and docs for both mechanisms now cross-reference each other
with the same distinction.

### Broker endpoints can enforce semantic grants

Broker endpoints now require an explicit assurance mode. The new `semantic`
mode validates each request against a typed operation grant before contacting
the upstream service, reauthorizes permitted redirects, and buffers each
response until its status, content type, size, JSON shape, and exact credential
non-disclosure checks pass. Requests and responses that fall outside the grant
fail closed, and audit events identify the approved operation and effect.

The existing generic relay remains available as the explicitly lower-assurance
`trusted-upstream` mode. Existing endpoint declarations must be updated with an
assurance mode; semantic endpoints also require a grant file.

### Successful setup now leaves a valid workspace verification baseline

`create --setup` measured the rootfs before booting the one-time setup commands
and updated only the generated config-disk measurement after they succeeded.
Any setup that changed the rootfs therefore left the new stopped workspace in
verification failure even though setup exited successfully.

Successful setup now records the resulting rootfs, final boot config, and
setup-complete state as one manifest checkpoint. If either artifact cannot be
measured or the checkpoint cannot be recorded, setup stays incomplete and the
operation fails closed instead of trusting a partial result. Kernel and guest
init measurements remain anchored to their pre-boot records.

### Snapshot restore's liveness gate can now actually pass

`start`/`create --from-snapshot` held the workspace at not-running until the
resumed guest's exec service answered a probe dialed through the host TCP
port forward — but that forwarder is a detached companion process started
only after the gate passes, so the probe was dialing a port nothing was
listening on yet. Every snapshot restore with an exec port configured failed
closed after the liveness window elapsed, no matter how long the guest
actually took to come back.

The probe now dials the guest directly over the Firecracker vsock UDS, the
same path already used to rehydrate secrets immediately after a restore. The
vsock device is realized synchronously by `PUT /snapshot/load`, so it is
reachable before the host forwarder — or the guest itself — has done
anything else.

### Snapshot resume's clock sync now uses the same vsock path as the liveness gate

`syncGuestClockAfterResume` polled the guest's structured-exec service
through the host TCP port forward for up to 30s after `create
--from-snapshot`. That is the same forward `waitForRestoreLiveness` was
fixed to stop polling in #592, because it is bound by a detached companion
process not yet started when this runs. The clock sync itself was never
touched by that fix, so the 30s wait was still there, unchanged, on every
linux-kvm snapshot resume.

On linux-kvm this now dials the guest directly over the Firecracker vsock
UDS first, the same way #592's liveness probe does. It falls back to the
pre-existing TCP-forward poll only if the vsock socket does not exist at
all. Measured on real hardware: `create --from-snapshot` at 1.34s total,
guest and host clocks matching exactly, versus a 30s stall before. apple-vf
has no equivalent local vsock convention and is unaffected — it keeps
today's TCP-forward poll.

### A rejected flag value is permanent, not transient

`microagent exec dev-agent --timeout 5min` exited `75` (`EX_TEMPFAIL`) with
`kind: transient`, `retryable: true`, `retry_after_ms: 1000`, and a
remediation telling the caller to wait for a host resource. A typo in a flag
instructed a scripted retry loop to keep re-running an invocation that could
never succeed.

The classifier reached that verdict by reading the message text. A flag error
carried no type, so it fell through to the substring table, where the flag's
own name — `timeout` — matched the transient rule. Every flag whose name or
rejected value happened to contain `unreachable`, `temporar`, `no space` or
any other pattern in that table had the same exposure.

Rejected command lines now carry a validation type, which classifies ahead of
the substring table and cannot be undone by rewording a message. They report
`kind: permanent`, `retryable: false`, no `retry_after_ms`, and exit `1`. The
same typing covers the range checks each command runs after parsing, which
were misreported the same way: `wait --timeout`, `connect --ready-timeout`,
`perf boot --timeout`, and `create --result-port`.

A malformed duration also says what a usable value looks like. The `flag`
package discards `time.ParseDuration`'s error and reports a bare `parse
error`, so the old message named neither the accepted unit suffixes nor a
working example:

```
invalid value "5min" for flag -timeout: expected a duration with a unit
suffix, such as 250ms, 30s, 5m, or 1h
```

### Egress mediator liveness is observed from a lease, not a process ID

A healthy workspace could report an enforcement failure naming a mediator
process it never spawned — a single-digit process ID, kernel-thread territory
on the host. The mediator's process ID is recorded by the supervisor that
spawned it, which under `user` networking runs inside the nested PID namespace
pasta creates. Status resolved that number against its own `/proc`, where it
names an unrelated process or none at all, so the ownership check reported a
running mediator as gone. The same lookup could pass as easily as it failed:
any process whose command line happened to name the workspace read as proof
that enforcement was alive.

The mediator now holds an advisory lock on the workspace's mediation lease for
its whole life, inherited at spawn the way the deadman inherits the runtime
lease. Locks are visible across namespaces and the lease is per workspace by
path, so a held lease means this workspace's mediator is running and an unheld
one means it is gone — neither answer can be borrowed from another process.
Workspaces started before this change record no lease; their status reports no
`live` field rather than a verdict it cannot support.

### `perf boot` measures the boot a repeat `run` performs

`perf boot` never wired the rootfs baseline hooks the rest of the CLI wires,
so every measured iteration took the full-build branch: pull the image, make
a filesystem, populate it. A repeat `run` of the same image clones a recorded
baseline instead and skips all of it, which made the benchmark report a
first-boot time under the name "boot time" — substantially above what the
command it measures actually costs. It could not seed a baseline either, so
even the first real `run` afterward paid for a build the benchmark had
already done.

Measured boots now carry the same reuse and seed hooks `create` and `run`
carry. Because a build and a clone are genuinely different numbers, each
iteration also reports which one it measured in `rootfs` (`baseline` or
`build`), counted in `summary.baselines` and `summary.builds`. Compare
reports with the same mix, or with `summary.builds` at `0`.

### mitm is staying — as the opt-in it always should have been

The `mitm` egress mode is no longer described as sunsetting. With broker
endpoints covering credential injection on both backends with no
interception, `mitm`'s remaining purpose is TLS content inspection of
non-brokered traffic — a real need for some operators. It stays supported,
warned, and deliberately never the default.

### Snapshot restore works again on macOS

Restoring any apple-vf snapshot failed closed: the supervisor persisted the
*effective* guest addressing into its runtime state, snapshot capture stored
that state, and restore replayed it as a request — where the
mediated-addressing validation (correctly) rejects declared
ip/gateway/subnet, killing the restored workspace within seconds while its
state briefly read running. Runtime state now persists the DECLARED network
(responses still report the effective one), which also restores the
declared-vs-defaulted DNS distinction the datapath resolver allowlist
depends on. Snapshots captured with the affected build are tolerated on
restore: addressing that exactly matches what the datapath assigns is
recognized as the supervisor's own echo, while any other declared
addressing still fails closed.

### Agent examples use broker endpoints

The Agentfile examples (`examples/agents/*`) now run against broker
endpoints instead of mitm credential swap: the guest carries a
`@secret:<name>` reference and a base-URL env var, the key is injected
host-side with no TLS interception and no CA in the guest, and every
brokered request lands in the decision stream. The locked-allowlist recipe
gets simpler too — brokered provider traffic rides vsock, so the NIC
allowlist can be locked with nothing on it and the agent still reaches its
provider. The warned, opt-in `mitm`/`cred-swap` path remains documented for
TLS content inspection.

### Orphaned one-shot runs die at their timeout on macOS

`--timeout` was enforced only by the attending host process, so on apple-vf
a one-shot run whose CLI died kept running until lease reaping. The request
now carries an explicit supervisor-enforced run bound (`runBoundSeconds`),
set only for one-shot shapes — `run`, `dispatch`, and the create-time setup
boot — and the apple-vf supervisor kills the VM when the bound expires,
recording "run bound exceeded" in the workspace state. Persistent
workspaces never carry a bound: their lifetime remains governed by leases
and operator commands, and `timeoutSeconds` is now documented as the host
dispatch timeout it always was, never a VM bound. The Firecracker
supervisor prefers the new field with its historical behavior as the
fallback for older callers.

### Broker endpoints on macOS

Broker endpoints — host-side credential injection with no TLS interception —
now run on apple-vf, closing the last backend gap on the feature: the
supervisor spawns one `--broker-serve` companion per endpoint (before
confinement, terminated with the VM, self-reaping if orphaned) and splices
guest vsock connections to its owner-only unix socket. Both backends run the
same portable endpoint server, extracted from the Firecracker companion, so
credential handling, decision records, and CONNECT-tunnel gating are
identical; the brokered requests land in the same `microagent egress` view.
The guest still only ever holds a `@secret:<name>` reference and a base-URL
env var.

### The host→guest contract is now registry-guarded

Two registries extend the egress-fields parity pattern to the rest of the
supervisor contract. `vmkit.GuestBootParams` is the canonical list of
`microagent_*` kernel command-line keys: parity tests scan guest init and
both backends' cmdline builders, so a key added to one side without an
explicit per-backend decision (and a reason for any asymmetry) fails CI —
the class that silently disabled mitm CA delivery on macOS.
`vmkit.AppleVFUndecodedConfigFields` documents every `vmkit.Config` field
the apple-vf supervisor intentionally does not decode; a new field that is
neither decoded nor registered fails CI instead of being silently dropped
at boot and erased from persisted state.

### Firecracker vsock parity hardening

Two Linux-side fixes mirroring behavior the apple-vf supervisor already
had: an enabled mediation config with no vsock listener on its port now
synthesizes one (a direct library caller setting only `Config.Mediation`
would previously boot a guest dialing a port nothing served — with
fail-closed mediation, silent total egress loss), and the guest-facing
vsock accept loops are bounded at 128 concurrent connections per listener,
refusing excess connections instead of letting a looping guest exhaust
host file descriptors.

### Raw vsock requests respect backend broker gaps

The low-level request surface (`--vsock`, `--request-json`) accepted a
`broker://serve` listener on any backend, bypassing the capability gate the
workspace layer applies — on macOS that surfaced as a raw supervisor
protocol error at boot instead of the declared backend gap. The same
structured gap error now applies at request build time on every surface.

### Doctor on macOS now probes what apple-vf boots actually need

Three apple-vf doctor checks reported ready without verifying their real
prerequisites. The egress-mediation check only required the supervisor,
but every mediated-egress boot also execs a microagent binary in
`--egress-datapath` mode; doctor now resolves that binary the way the boot
path does (`MICROAGENT_EGRESS_DATAPATH_BIN`, else the current executable)
and reports not-ready — naming the variable and what it must point at —
when it does not resolve to an executable file. The offline file copy
check claimed ready unconditionally, though copy, commit, and artifact
extraction shell out to e2fsprogs (`e2fsck`, `debugfs`, `mke2fs`), which
Homebrew installs keg-only; doctor now resolves each tool through the same
PATH-plus-keg lookup the copy paths use (both Apple Silicon and Intel
prefixes) and names each missing one with the `brew install e2fsprogs`
remediation.

Pause/resume was also gated on save/restore support (macOS 14+), though
pausing a VM only needs the framework's pause support (macOS 13+): on
macOS 13 the capability row read not-ready while the legacy
`pauseResumeAvailable` boolean stayed true — the payload contradicted
itself. The pause/resume check now keys on the supervisor's pause fact,
snapshot checks stay on save/restore, and the legacy availability
booleans re-derive from the capability rows on apple-vf the way they
already did on linux-kvm, so the payload can no longer disagree with its
own capability rows.

### macOS supervisor hardening

Three small apple-vf supervisor fixes: the workspace result file is written
owner-only (0600, born that way) instead of world-readable, matching Linux;
the guest cmdline announces the shell/exec services keyed on the resolved
guest port like the Linux builder, so a config carrying only the guest-port
override cannot silently lose its shell and exec channels; and the vsock
socket device is attached whenever the CA-delivery or secrets-control ports
are configured, so those services can never dial a device that was never
attached.

### stats stops inventing numbers on macOS

On macOS, `stats` reported `cpuPercent` as a process-lifetime average (the
contract says a short-interval measurement, which is what Linux gets) and
emitted `ioReadBytes`/`ioWriteBytes` as zeros even though the host exposes
no per-process I/O accounting. CPU is now measured across the same
two-sample interval as Linux, and the I/O counters are absent — from the
JSON and the text line — instead of zero-valued lookalikes.

### Declared DNS works on mediated macOS workspaces

On apple-vf, the mediated user-mode datapath pinned the guest's resolv.conf
to a fixed default while using the workspace's declared `network.dns` as its
resolver allowlist — so declaring nameservers made every guest DNS query
refusable, silently, while the same spec worked on Linux. The guest now
receives the declared resolvers (or the default when none are declared), a
static user-mode guest with no declared nameservers gets the same injected
default as Linux instead of no resolution, and declared
`network.ip`/`gateway`/`subnet` under the mediated datapath — whose subnet
is fixed — now fail closed at start with a clear error instead of being
silently ignored (recorded as a backend gap in the library contract; static
addressing works with `--egress off`). Workspace state and `microagent
network` on macOS now report the addressing the guest actually received —
real IP, subnet, gateway, DNS, and route — instead of echoing the declared
spec.

### mitm CA delivery now works on macOS

On apple-vf, the supervisor never told the guest which vsock port serves the
per-workspace egress CA, so `mitm` workspaces booted without the CA in their
trust store and TLS inside the guest could not verify intercepted
connections — credential swap (`--cred-swap`, Agentfile `cred-swap:`) failed
with certificate errors on macOS while working on Linux. The guest kernel
command line now carries `microagent_ca_cert_port`, matching the
Firecracker path, and a supervisor test pins every future port against
being silently dropped the same way.

### Images that ship systemd now build

Rootfs builds rejected any OCI layer path or link target containing a
backslash, which broke real images: systemd escapes `-` as `\x2d` in unit
file names, so images with systemd installed (for example
`homebridge/homebridge`) failed extraction with `unsafe OCI layer path`.
The extraction path is POSIX-only, where a backslash is an ordinary name
character, so such paths now extract as literal names — a hostile name
like `..\..\evil` stays one file inside the stage root, covered by a
containment test.

### Doctor verifies TPROXY support by doing, not by listing

The egress-mediation check now installs a real TPROXY steering rule in a
scratch user+network namespace (the supervisor's `--tproxy-selfcheck`,
launched the way a mediated boot's rule install runs) and reports the
kernel's own verdict. Module listings misread hosts in both directions —
the kernel autoloads `nft_tproxy` on first use, and a built-in module
without parameters never appears under `/sys/module` — so doctor now asks
the kernel directly and reports ready even when the module list looks
empty. The module heuristic remains as the fallback when the probe cannot
run (older supervisor without the self-check, missing `unshare`) and as
the remediation detail naming exactly what to load when the kernel
refuses. The structured output gains `host.egressTProxyProbeError`,
set only when the probe ran and was refused.

### Doctor no longer reports working egress mediation as degraded

The TPROXY prerequisite list required the iptables-era `socket` match
modules (`xt_socket`, `nf_socket_ipv4`) that the boot path's nftables
steering never uses — mediated UDP/DNS runs end to end without them — so
doctor told working hosts their egress mediation was missing kernel
modules, and the rollup read `degraded` on a host where every mediated
boot succeeds. The requirement is now exactly what the steering rule
uses (`nft_tproxy` and its `nf_tproxy_ipv4` dependency), and the docs no
longer ask operators to load modules the mediator does not need. The
boot path remains the authoritative fail-closed check when TPROXY
genuinely cannot be set up.

### Doctor: one glyphed check per line, and a verdict that covers the whole contract

The doctor page is redesigned around the question it answers. An identity
header names the backend, architecture, and VMM version; every check —
including passing ones — renders as its own aligned line with one glyph
(`✓` ready, `⚠` degraded but usable, `✗` not usable), so a failing check is
always a visible line rather than a phrase missing from a summary; and the
page closes with a verdict sentence that states what will work on this host
and what will not. Remediation prints directly under the check it fixes.
Paths no longer appear on healthy lines; a failing check names where the
missing piece was expected.

Egress mediation is now a declared backend capability with its own
prerequisite check (on Linux, the TPROXY kernel modules UDP mediation
needs), replacing the standalone `Egress TPROXY modules` line. Every
declared capability now carries a `tier` in the structured output — `core`
(no workspace can boot or be observed without it), `safety` (the
enforcement plane; requests that need it fail closed), or `feature`
(the operation fails closed at use) — so clients can gate on severity
instead of guessing.

The rollup now speaks for the full advertised contract: a host with a
declared capability unavailable reports `degraded` even when every probe
passed, instead of `ok` with a warning buried below it. The structured
response carries the same rollup as a new `verdict` field (`ok` /
`degraded` / `failed`) alongside the unchanged `ok` boolean, so scripts and
agents branch on exactly what the text page prints. Exit codes are
unchanged: `degraded` still exits `0`.

## v0.9.0 - 2026-07-28

The CLI-contract release: classified errors with retry-aware exit codes, a
uniform help surface, dry-run guarantees, doctor verdicts anchored to the
real boot path, and a capability matrix split into operation facets — plus
the removal of the experimental Windows Hyper-V backend and of several
long-deprecated flag spellings.

### Experimental Windows Hyper-V backend removed (breaking)

The retired `windows-hyperv` backend, the Windows host runtime paths, and
the Windows E2E lanes are removed. Supported platforms are Linux
(`linux-kvm` / Firecracker) and macOS (`apple-vf` / Apple
Virtualization.framework); WSL remains a compatibility lane through the
Linux backend.

### Doctor: root-cause verdicts it can stand behind

`doctor` now resolves Firecracker exactly the way the boot path does, so its
verdict matches what `run` and `start` will actually use, and split layouts
(CLI on PATH, supervisor elsewhere) no longer produce a false "failed".
Output leads with the root cause, and hosts that can boot but with reduced
function report `degraded` instead of `failed`. Doctor also detects an
SELinux-confined user-networking binary by probing the real start path, and
`run` names the blocking policy in its error instead of timing out. When
`MICROAGENT_FIRECRACKER` does not resolve, the error says what to set and
where the lookup searched.

### Classified errors on the CLI, with retry-aware exit codes

Every CLI failure now carries the same classification MCP responses always
had: an error kind, a remediation line, and a retryability bit. Text mode
appends the remediation to the message; explicit JSON mode (`--json` /
`MICROAGENT_OUTPUT=json`) emits the full one-line error object on stderr.
Retryable failures exit 75 (sysexits `EX_TEMPFAIL`), other failures exit 1,
usage errors stay 2, and guest exit codes still pass through. Scripts can
finally branch on transient-vs-permanent without parsing prose.

### Help that answers instead of acting

The help surface is now uniform across the whole command registry, pinned by
test: `--help` never performs the operation, from any position — including
beside a mistyped subverb. Every command carries a Usage block, options are
listed once, and each command's help leads with the same summary its docs
page uses. A command group invoked with no subcommand explains itself and
exits 0. A mistyped command suggests the near miss, and the removed
`--text`/`--human` flags point at their `--output text` replacement. The
`kernel` synopsis was corrected rather than bent to fit the parser.

### Dry-run means no side effects, and returns the plan

`run` honors `--dry-run` (it previously executed), the snapshot-fork path
honors it, and `dispatch --dry-run` returns the planned operation instead of
discarding it. A malformed image reference is rejected during dry-run, and
before any side effects in a real run. `run` also rejects conflicting guest
command sources instead of silently picking one, and no longer silently
ignores `--entrypoint`.

### Run failures honor the discard contract

A failed `run` now applies the same `--rm`/keep semantics as a successful
one, instead of leaving discard-mode workspaces behind on failure.

### Results distinguish "never ran" from "failed"

The structured result now reports whether the workload ever started,
separate from how it exited, and a start error is carried into the runtime
result where `microagent result` reads it — a boot failure is no longer
indistinguishable from a guest that ran and failed. The serial log excerpt
inlined into the structured result is bounded, so a chatty guest cannot
bloat result payloads.

### Capability matrix reports operation facets

The host capability matrix (doctor, contract, and the library surface)
reports finer-grained facets: `Console` split from structured exec,
`OfflineFileCopy` and `LiveFileCopy` modeled separately, `NetworkPublish`
split from `LiveNetworkApply`, and the coarse `Snapshot` capability split
into `PauseResume`, `SnapshotCreate`, `SnapshotRestore`, and `SnapshotFork`.
Each facet carries its own L1 prerequisite diagnostics.

### Persisted state classified into durability tiers

The contract now assigns every microagent-owned file family a durability
tier — `recoverable`, `operational`, `audit`, or `evidence` — with explicit
failure and cleanup behavior per tier. `microagent contract` reports each
family's writer, retention, recovery behavior, and secret exposure. Cleanup
and cache pruning must not touch operational state, audit streams, named
volumes, or forensic evidence.

### Operation policy centralized behind one inventory

CLI and MCP adapters now consult the same canonical operation inventories
for lifecycle, file, snapshot, workspace, resource, and host operations,
and the MCP adapter's permissive fallbacks are removed: an operation absent
from the inventory is rejected identically on every surface.

### Smaller fixes

- A missing workspace name is reported as a usage error (exit 2) instead of
  a runtime failure.
- Terminal detection uses `term.IsTerminal`, so a non-TTY character device
  no longer selects interactive output.
- Base-stage rootfs cache entries publish atomically; a concurrent build can
  no longer observe a partially written cache entry.

### CLI output profiles removed

The deprecated `--mode ux|ax` flag and `MICROAGENT_MODE` environment variable
are removed. The CLI now has one text interaction model plus `--json`
serialization. Agent clients should use `microagent serve mcp`.

### Apple VF supervisor probes the mediation target for `mediationReady`

The Apple VF supervisor's protocol responses previously reported
`mediationReady` as ready whenever the workspace was running, without checking
the declared mediation target. The supervisor now performs the same bounded
TCP reachability probe the documented contract requires ("live reachable"),
matching the Linux supervisor and the `microagent status` path: a dead
mediation listener reports `ready: false` with a named unreachable error.
Direct supervisor consumers (shell, Python, Rust, Node) see the corrected
signal; `microagent status` output was already probe-backed and is unchanged.

### Output format flags consolidated to `--output` (breaking)

The global output-format flags are unified: `--text`/`--human` are removed
(use `--output text`), `--output human` is removed (use `--output text`),
`MICROAGENT_OUTPUT` only recognizes `json`/`text` (`human` dropped), and
`--mode`/`MICROAGENT_MODE` only recognize `ux`/`ax` (the `human`, `agent`,
`text`, `json` synonyms are dropped). AX no longer unconditionally forces
JSON: precedence is now explicit format flag (`--output`/`--json`) >
`MICROAGENT_OUTPUT` > (`--mode ax` defaults to JSON) > TTY detection, so
`--mode ax --output text` now renders text instead of being forced to JSON.
See MIGRATION.md.

### Request-JSON alias retired; `--request-json` only (breaking)

The `-json`/`--json <path|->` compat alias for request input on
`create`/`start` and the lifecycle verbs (`status`, `halt`, `stop`, `kill`,
`pause`, `resume`, `quarantine`, `delete`, `result`) is removed; a following
`--json` is now always the global output-format flag, on every command, with
no per-command exception. `--request-json <path|->` is the only spelling. A
deterministic tripwire catches the two unambiguous old-alias shapes — a
`--json` followed by a token ending in `.json`, or by the bare stdin marker
`-` — and fails them loudly as an unknown flag rather than silently treating
the path as a workspace name. A suffix-less token remains genuinely
ambiguous with the legitimate `status --json <workspace>` form; see
MIGRATION.md for the residual hazard and an audit grep for scripts carrying
the old alias.

### AX responses are one `{ok, result|error}` envelope on stdout (breaking)

Every `--mode ax` response (and every MCP-captured CLI call) is now exactly
one JSON document on stdout: `{"ok": true, "result": {...}}` on success,
`{"ok": false, "error": {...}}` on failure. AX errors move from stderr to
stdout — parse stdout only, and read the exit code for whether microagent
itself worked. Commands that previously printed a partial result before an
error under AX (`run`, `dispatch`, `create`, `start`, `rootfs build`) now
suppress that result so failure is always a single document, and
`start --wait` under AX emits only the final wait-outcome envelope instead
of a boot envelope followed by a wait envelope. Plain `--json` (UX) output
is unchanged and stays bare. See MIGRATION.md.

### MCP tool responses use the unified `{ok, result, meta}` envelope (breaking)

MCP tool payloads now match the CLI-AX envelope: every response carries an
`ok` discriminator, and transport concerns (`timing_ms`, `principal_context`,
`idempotency_replay`, and the exec retry fields) move from beside `result`
into a sibling `meta` block — the old nested exec `metadata` sub-object is
gone. JSON-RPC error responses gain the same `meta` block as a sibling of
`error.data`'s existing `structuredError` fields (`kind`, `message`,
`remediation`, `retryable`, `correlation_id`, unchanged), replacing the old
custom `mcpStructuredError` shape with the plain `structuredError` shape.
The `microagent.describe` manifest's `correlation_id_key` moves accordingly,
from `error.correlation_id` to `error.data.correlation_id`; gateways must
read the key from the manifest rather than hardcoding a path. See
MIGRATION.md.

### Typed-first AX error classification

`mapStructuredError` now checks `errors.Is`/`errors.As` against real error
types (`workspace.WaitTimeoutError`, `workspace.ExecRetryExhaustedError`,
`execclient.UnreachableError`, `vmkit.UnsupportedFeatureError`) and stdlib
sentinels (`os.ErrNotExist`, `os.ErrPermission`, `context.DeadlineExceeded`,
`context.Canceled`, `net.Error` timeouts) before falling through to the
existing substring-matching tail, so a reworded upstream error message can
no longer silently change its reported `kind`/`retryable`. A bare
`context.DeadlineExceeded` now classifies as transient/retryable (previously
permanent, undisclosed) — deadline expiry is retryable, and this was the one
classification change bundled with the refactor.

### Friendlier flag-error pointers on run/create/start/dispatch

An unknown flag on `run`/`dispatch` (high-level path) or
`create`/`start`/the lifecycle verbs (low-level request-JSON path) now gets
the same one-line error plus "Run 'microagent \<cmd\> --help' for usage"
pointer every other command already had, instead of a bare `flag` package
error.

### Fixed: lifecycle verbs' `--request-json` no longer misroutes to the high-level path

`status`/`halt`/`stop`/`kill`/`pause`/`resume`/`quarantine`/`delete
--request-json <path|->` (and `start --request-json <path|->`) now reach the
low-level request-file loader instead of the high-level workspace-state
path. The routing check that picks between the two paths did a naive arg
scan that didn't know `--request-json` takes a value, so for the
space-separated form it walked into the value (a bare file path) and
misread it as a workspace name or ID — landing on the high-level path, which
doesn't define `--request-json` and failed with an unknown-flag error before
the request file was ever opened. The `--request-json=<path>` form was
unaffected. Presence of `--request-json` (either dash spelling, `=`-joined
or space-separated) now always routes to the low-level path.

### CLI `stop` merged into `halt`; a clean exit now records `halted` (breaking)

The CLI has one graceful-shutdown verb: `halt`. `stop` is retained only as a
pure alias of `halt` and behaves identically — same SIGTERM + fixed backend
graceful window (~5s), returning an error without escalating if the guest
does not exit. The one caller-visible change: a clean `stop <name>` now
records the `halted` state instead of `stopped`. `kill` and `delete` are
unchanged and still record `stopped`. There is no library/contract change:
`stop` resolves to `halt` at the command registry, so `Control("stop")` and
the low-level `--request-json` path never see a raw `stop` from the CLI and
still record `stopped` when called directly. See MIGRATION.md.

### MCP `workspace.stop` removed; call `workspace.halt` (breaking)

The `workspace.stop` tool is removed from the MCP tool surface (`tools/list`
and the manifest). Call `workspace.halt` instead — identical semantics (same
graceful shutdown; a clean exit records `halted`). Calling `workspace.stop`
now returns a JSON-RPC tool-call error (`kind: "unsupported"`) instead of
running the alias. See MIGRATION.md.

### `image delete`/`image prune` flag `--delete` renamed to `--purge` (breaking)

The `--delete` flag on both `image delete` and `image prune` is renamed to
`--purge`, removing the self-shadowing name (`--delete` on the `delete`
command was confusing). The old spelling fails with the subcommand's usage
error naming `--purge`; the MCP tools `images.delete`/`images.prune` are
unaffected and keep their `delete_files` argument — only the CLI flag
spelling changed. See MIGRATION.md.

### Human-readable list tables: state-word color, adaptive widths, short digests

Human output from `workspace list`, `image list`, `volume list`, and `snapshot
list` (text mode, TTY-only) now adds state-word coloring (a redundant visual
channel independent of text), width-adaptive tables that measure the actual
terminal and fit themselves to available space with … truncation for
over-long values, and shortened digests in the DIGEST column (12-hex `docker`
style instead of the full `sha256:...` form). The `--no-color` flag (and
`NO_COLOR` environment variable) disable coloring across all commands. Piped
output (non-TTY) keeps the fixed column widths of the original byte-stable
format, so `awk`/`cut` scripts extract the same byte positions; only the
digest text itself changes (full → 12 hex). `model list` keeps its
tab-separated shape with the same shortened digest field. `--json`, AX, and
MCP outputs are unaffected: they always
carry the full digest and no truncation. **Note:** human table output is not
a parsing contract — use `--json` for machine consumption.

### Fixed: confined VMs no longer falsely reaped on tmpfs or btrfs state dirs

The gc and per-VM deadman identify a confined (pivot_root'd) Firecracker by
looking for the workspace jail path in the process's mountinfo. On a state
dir under tmpfs (a common `/tmp`) or a btrfs subvolume, the kernel records
the bind source relative to that filesystem's own root, so the host-absolute
path never matched: a healthy detached workspace was declared "reaped by gc:
firecracker process gone" within about a second of `start`, its record
rewritten to stopped, and the live VM leaked with no state tracking it. The
identity check now also compares `/proc/<pid>/root` against the jail
directory by device and inode, which holds on every filesystem.

### Fixed: delete is idempotent again

Deleting an absent (or already-deleted) workspace briefly returned a
`not_found` error after CLI and MCP deletion moved onto the shared
`workspace.Delete` contract. Both surfaces report the stopped response with
exit 0 again, matching the released behavior and the public-surface E2E pin.
The confirmation prompt is skipped for an absent workspace — there is
nothing to lose.

## v0.8.7 - 2026-07-16

The egress broker release. A workspace can now route credentialed egress
through a per-workspace forward proxy that swaps secret references for live
values on the host — no CA and no credential ever in the guest — with a
minimized decision stream, opt-in governed request capture, non-cooperation
signals, multiple endpoints per workspace, and a governed CONNECT tunnel. The
egress mode vocabulary is rebuilt around it: `broker` (the new no-CA default),
`mitm`, and `off` — a hard breaking change that retires `guarded` and
`strict`.

Alongside: `microagent wait` and `start --wait`, `run`/`dispatch` output that
behaves like running the command locally, the `helper:` secret scheme for
operator-owned credential helpers, readable one-shot workspace names, and a
batch of first-run fixes — fresh hosts install the default kernel again, the
rootfs disk grows to fit the image, and `aarch64`/`x86_64` arch spellings are
accepted.

### Fixed: `e2fsck` resolves through the Homebrew keg on macOS

Workspace copy, artifact extraction, and cached-rootfs refresh reconcile the
ext4 journal with `e2fsck` before touching an image. That lookup only searched
PATH, and Homebrew installs e2fsprogs keg-only — so on a standard macOS setup
these operations failed with `exec: "e2fsck": executable file not found` even
though `mke2fs` and `debugfs` already fell back to
`/opt/homebrew/opt/e2fsprogs/sbin`. `e2fsck` now resolves the same way, and
the public-surface E2E preflight puts the keg's tools on PATH instead of
skipping the scenario.

### `run` and `dispatch` act like running the command locally

On a terminal, `run` and `dispatch` now show live progress on stderr while the
image pulls, the rootfs builds, and the microVM boots — the same
spinner/progress-bar reporting `create` and `rootfs build` already had —
instead of sitting silent until the result. The workspace-metadata block
(Workspace/State/Rootfs/Profile/Restart/Network/Hostname/Resources/Kernel) is
gone from the human output: guest stdout and stderr land on the matching host
streams, and the guest exit code becomes the CLI exit code, matching `exec`
and `docker run` semantics. `run --keep` prints the kept workspace name to
stderr, and a failed run still prints the workspace name and console log path
so you can debug it. Dispatch's egress receipt moves to stderr (with hosts
sorted for stable output), so stdout carries only the task output and stays
pipeable. `--json`, AX, and MCP output shapes are unchanged.

### `make install` warns when another install shadows the new one

An install can succeed and still not be what `microagent` runs: a Homebrew
install earlier on PATH answers instead, so `microagent -v` quietly reports
the old version. The installer now checks what `microagent` resolves to after
installing and says so, with both versions:

```text
warning: `microagent` on PATH is /opt/homebrew/bin/microagent (0.8.6),
  which shadows the copy just installed at /Users/you/.local/bin/microagent (0.8.6+100.33358df.20260713).
  Put /Users/you/.local/bin earlier on PATH, or remove the other install (e.g. brew uninstall microagent).
```

When the prefix isn't on PATH at all, it prints the `export PATH=...` line to
add instead.

### Source builds check their build tools and report what's missing

`make build`, `make dev`, and `make install` now preflight the build tools
before running anything. A missing `go` used to surface as a raw shell error
partway through a build script; in an interactive shell with Homebrew
available the preflight now offers to run `brew install go` (or
`brew upgrade go` when the installed Go is older than the `go.mod`
requirement) and continues on acceptance. Without a terminal or without
brew, it fails immediately with the tool name and an install command, so CI
and scripts never hang on a prompt. A missing `git` only warns - it is
needed for version stamping, not for the build - and the result reports
`0.0.0+local`. Builds from a
source archive (a release tarball or ZIP, no git metadata) work now: they
stamp `0.0.0+local` and skip Go's VCS stamping, which used to fail the build
when a stray `.git` entry existed in a parent directory.

### Source-build versions say how old they are

Checkout-local builds used to stamp `<release>-<sha>` (for example
`0.8.6-56f0bdd`), which doesn't tell you whether the build is current - a
short sha carries no ordering. Dev builds now stamp one build-metadata block
of dot-separated fields:
`<release>+<commits-since-release>.<sha>.<commit-date>[.dirty]`, for example
`0.8.6+15.9c7ad3d.20260712`. The commit count orders any two builds and the
date says whether one is stale, with no extra punctuation to parse. A clean
checkout exactly on a release tag still reports the plain release version.

### Fixed: first boot on a fresh host installs the default kernel again

`run`, `dispatch`, `create`, `start`, and snapshot forks once again fetch,
verify, and install the default kernel from the signed manifest when no kernel
is installed - behavior that was lost in the capabilities-to-packages refactor,
leaving fresh hosts (typically Linux installs from source, which ship no
packaged kernel) to fail with `record workspace verification: open .../Image:
no such file or directory`. The ensure step is library-owned:
`workspace.EnsureKernel` runs inside `Create`/`Run`/`Start`/`CreateFromSnapshot`,
so the CLI, MCP, and embedding programs all get it. `pkg/kernel` registers the
installer when linked; an explicit kernel path is always used as-is.

### Fixed: `aarch64` and `x86_64` accepted as architecture spellings

`--arch aarch64` - the spelling `uname -m` prints on a Raspberry Pi - used to
fail with `no linux-kvm/aarch64/lts kernel in the signed manifest` because
kernels, image platforms, and paths are keyed on the Go/OCI names. Arch values
are now normalized everywhere they enter (`workspace.NormalizeArch`):
`aarch64` means `arm64`, `x86_64`/`x64` mean `amd64`, across run/create/
dispatch, `kernel install`/`verify`, `rootfs build`, and `image pull`.

### Fixed: the rootfs disk grows to fit the image

Building a rootfs from an image bigger than the workspace disk (for example
`python:3.12`, about 1 GiB unpacked, against the default 1024 MiB `small`
profile) used to fail with raw `mke2fs` output (`Could not allocate block in
ext2 filesystem`). The builder now measures the staged tree before formatting.
When no size was pinned - no `--size-mib`, no spec `sizeMiB` - the disk grows
to the smallest GiB multiple that holds the image plus at least 512 MiB of
writable space, and the workspace manifest records the size the disk actually
has. A pinned size stays authoritative and fails closed, now with an
actionable error: `rootfs contents need about 1183 MiB but the rootfs disk
size is 1024 MiB; give the workspace a larger disk, for example --size-mib
2048, or drop the pinned size to let the disk grow to fit`. Tar bundle disks
(`-v bundle.tar:/mnt`) and `image pull` baselines grow the same way. README
and docs examples switch `python:3.12` to `python:3.12-slim` for a faster
first pull; the full image now works too.

### `microagent wait`: block until a workspace's run finishes

`microagent wait <name> [--timeout <dur>]` blocks until the workspace reaches
a terminal state - `stopped`, `halted`, `failed`, `quarantined`, or a
never-started `prepared` - and exits `0` for a clean finish (`stopped`,
`halted`, `prepared`) or `1` for `failed`/`quarantined`, so scripts no longer
poll `microagent --json status` in a shell loop after a detached `start`.
`start --wait` (with optional `--wait-timeout`) boots and waits in one
command. While the recorded state is live, each check reconciles against the
backend supervisor like `status` does, so a dead VM resolves to its real
terminal state instead of blocking on a stale `running` record.

The capability is library-first and shared across surfaces: `workspace.Wait`
(with `WaitOptions`, `WaitResult`, and `WaitTimeoutError`) in `pkg/workspace`,
the `wait` CLI verb, and a `workspace.wait` MCP tool that returns
`{workspace, state, ok}` and maps an elapsed `timeout` to a retryable
`transient` error.

### Fixed: the broker CONNECT tunnel is governed — default-off, inside-deny, anti-rebind

The egress broker's HTTP CONNECT tunnel previously dialed any guest-chosen
target with no policy, bypassing the datapath's inside-deny. CONNECT is now
served only by an endpoint that declares `proxy` (a terminate-only endpoint
answers `405`), and where enabled the tunnel resolves the target, denies
fail-closed if any resolved IP is an inside/infrastructure address, and dials
the exact classified IP — never re-resolving — so a DNS rebind cannot swap an
allowed answer for an inside one. A per-endpoint CONNECT allowlist can lock
the tunnel to named hosts. Both refusal paths emit the `denied` signal on the
`broker_request_deny` record.

### Human-readable names for one-shot workspaces

`run` and `dispatch` (CLI and `workspace.Run`/`workspace.RunDispatch`) now mint
readable auto-names like `run-brave-otter-4f9c` instead of
`run-<19-digit-nanosecond-timestamp>` when no `--name` is given. The short
random suffix keeps names collision-safe while making `microagent delete
run-brave-otter-4f9c` typable by hand. The generator is exported as
`workspace.RandomName(prefix)`.

### `rootfs build --arch` defaults to the host architecture

The flag was hard-coded to `arm64` regardless of host; an amd64 host now
builds an amd64 rootfs by default. Pass `--arch` explicitly to cross-build.

### `create` gains `--request-json` for request input

The request-input flag was spelled `-json <path|->`, colliding with the global
`--json` output flag. `--request-json` is now the preferred spelling on every
command that accepts request JSON; `-json` after the subcommand remains a
compat alias (passing both with different paths is an error).

### `perf boot` exits nonzero when iterations fail

`perf boot` recorded failed iterations in the report but still exited `0` —
contradicting its documented exit status and making it useless as a CI gate.
It now prints the full report and then exits nonzero if any iteration failed;
the summary gains a `failures` count alongside `count`.

### Multiple egress broker endpoints per workspace

A workspace can now declare more than one egress broker endpoint instead of
just one: repeatable `--broker-endpoint "upstream=<url>;secret=NAME=<scheme>:
<ref>;base-url-env=KEY;ca=<path>;proxy;capture"` (Agentfile: an `agent.brokers`
list instead of the single `agent.broker` block; MCP: a `brokers` array of the
same spec strings) declares each endpoint fully on its own. This is for a
workload that reaches several credentialed upstreams from one workspace, each
with its own injected credential — the guest holds only that endpoint's
`@secret:NAME` reference, never the others'.

Each endpoint gets its own guest-local listen address and host vsock port,
assigned automatically so endpoints never collide, and its own optional
upstream CA (`ca=<path>`, or the single-endpoint `--broker-ca`) for an
upstream with a private certificate. All endpoints in a set share the one
per-workspace `broker-access.jsonl` decision trail, distinguished by upstream
host, and only one endpoint may claim the guest-wide `HTTPS_PROXY`/
`HTTP_PROXY` slot — declaring `proxy` on more than one is rejected. A
`--broker-endpoint` spec cannot be combined with the single-endpoint
`--broker-upstream`/`--broker-secret`/`--broker-env`/`--broker-proxy`/
`--broker-capture`/`--broker-ca` flags. The full set persists in the workspace
manifest as `brokers`, so restart/wake re-arms every endpoint identically.

### Fixed: snapshot of a `broker`-mode workspace no longer demands an egress CA

Snapshot manifest capture still required the persisted per-workspace egress CA
for every mediated workspace, but `broker` mode (the default) deliberately
mints no CA — so snapshotting any broker workspace failed with "requires its
persisted egress CA". The requirement is now gated on certificate-forging
modes only (`mitm`), matching the start/restore paths; a broker snapshot
carries an empty CA fingerprint and the restore path already treats that as
"no CA to reuse".

### Egress mode vocabulary — `broker` / `mitm` / `off` (breaking)

The `guarded` and `strict` egress modes are **retired**. The vocabulary is now:

- **`broker`** (default): allow-broad, opaque forward-proxy splice, no CA in the
  guest — the same reach the old `guarded` default had, without TLS interception.
- **`mitm`**: allow-broad, forge per-SNI certificates (the old `guarded`/`strict`
  datapath). A sunsetting, warning-gated compatibility mode: enabling it prints a
  load-time warning and logs an `egress_mitm_enabled` audit record.
- **`off`**: unmediated.
- **`--egress-lock-allowlist`**: an orthogonal parameter that makes either
  mediating mode allowlist-only — the retired `strict` reach control.

This is a hard breaking change with no aliases. `--egress guarded`, `--egress
strict`, a manifest or snapshot naming either, and any unrecognized value are
rejected with an error naming the successor — never silently reinterpreted. An
unspecified mode resolves to `broker`, so the common case keeps working and gets
the no-CA default; only callers who *typed* a retired name or restore an old
manifest are affected. Credential swap and `--egress-policy`/`--egress-swap-config`
now require `mitm` (swap needs the plaintext). `--egress open`/`disabled` are no
longer accepted aliases for `off`.

### Non-cooperation signals

The egress mediator now tags the audit records it writes with a `signal` field
from a closed vocabulary when it detects an attempt to route around it —
`denied`, `direct-ip-no-sni`, `quic-udp443` (QUIC/UDP:443 is default-denied so
clients fall back to TCP), `foreign-resolver`, and the broker's
`unresolved-secret-ref`. The mediator only detects and emits; the response is a
consumer's policy.

### Broker decision stream + governed request capture

The egress broker's per-workspace trail (`broker-access.jsonl`) is now a
**decision stream**: one minimized record per brokered request — verdict,
rule, method, host, upstream status, byte counts both ways, timing, and the
*names* of the credential references swapped, never values. By schema the
record carries no path, headers, or bodies, and everything the broker records
is captured **before** the reference-for-secret swap, so the injected
credential cannot appear in any trail by construction. `microagent egress`
merges the mediator's connection-level log and the broker's request-level
records into one time-ordered view (snapshot and `--follow`), and the per-host
allow/deny rollups fold broker verdicts in.

A new `--broker-capture` flag (Agentfile: `agent.broker.capture`, MCP:
`broker_capture`) opts in to raw capture of pre-swap requests — path, headers
with references verbatim, and a bounded body prefix (1 MiB, truncation
flagged) — to a separate owner-only `broker-capture.jsonl`. Capture is
request-only (responses have no swap point and are never captured), off by
default, and declared in the workspace manifest.

The broker also gained an in-process policy seam: a hook that judges each
pre-swap request before any bytes go upstream and returns only a verdict
(allow/deny, rule, classification labels). It is fail-closed — a policy error
or panic denies — and unconfigured by default.

### `broker` egress mode — mediation without a CA in the guest

A new `--egress broker` mode terminates guest egress at a transparent forward
proxy instead of forging per-SNI certificates. Allowed TLS flows are spliced
opaquely — the guest sees the real upstream certificate — so **no
per-workspace CA is minted or delivered to the guest**. Like `guarded`, broker
mode is allow-broad: it permits public destinations and denies only the
inside/infrastructure (RFC1918, link-local, loopback, CGNAT, cloud metadata),
classified on the resolved IP. As the guest's sole resolver, the mediator also
strips HTTPS/SVCB records (and thus any ECH config) from DNS answers, keeping
the TLS SNI visible so enforcement is not defeated by Encrypted Client Hello.

`--egress-lock-allowlist` tightens broker mode to allowlisted destinations only
(dropping the allow-broad default) while keeping the opaque-splice, no-CA
behavior — the destination-restriction posture without TLS interception. The
mode and toggle persist with the workspace and across snapshot restore.

### Egress broker — credential isolation without MITM

Workspaces can now route egress through a per-workspace broker: a cooperative
forward proxy served on a host vsock listener that swaps credential
references (`@secret:<name>`) for the live secret just before originating its
own upstream TLS — no forged certificates, no CA injected into the guest. The
guest never holds the credential: not in its environment, its filesystem, or
anything it can read; the live value exists only in host process memory.
Because the channel is vsock, it works even for `--network isolated` guests,
composing containment with credential isolation.

`microagent create/run --broker-upstream <url> --broker-secret NAME=<ref>`
(plus optional `--broker-env KEY[=VALUE]` and `--broker-proxy`) wire
everything with zero manual steps: the guest env (bridge listener, base URLs,
proxy vars) is baked at create, the broker config persists in the workspace
manifest — reference only, never a value — and the supervisor serves the
broker at every start, resolving the secret fresh (fail-closed: an
unresolvable reference or a pasted literal aborts instead of booting an
unbrokered workspace). Every brokered request is recorded pre-swap in the
per-workspace `broker-access.jsonl`, so the trail carries the workload's own
reference and can never contain the live secret — absent by construction,
not redaction.

### New `helper:` secret scheme — credential-helper binaries

Secret references gain a `helper:` scheme that resolves by executing an
operator-owned binary (named by `MICROAGENT_SECRET_HELPER` in the resolving
process's environment) with the reference remainder as its argument; the
secret is the helper's stdout. This is the git/docker credential-helper
pattern applied to the conduit: embedding platforms plug in their cloud's
secret manager — resolved via instance identity, no tokens at rest — without
cloud SDKs entering microagent. Fail-closed throughout: unconfigured host,
nonzero exit, or empty output all fail the resolve, and stderr (never the
secret) is surfaced in the error.

## v0.8.6 - 2026-07-07

Snapshot-chain correctness release: three related fixes for workspaces that
are snapshotted and restored repeatedly (the pattern behind microplane's
hibernate/wake), all found and field-validated by real hibernation cycles.

### Resuming a fork in place no longer loses its baked identity

`start --from-snapshot` on a workspace that was itself created from a
snapshot (a fork) launched the VM without the baked identity adoption that
`create --from-snapshot` performs: the loaded guest listens on its ancestor's
service ports and references the ancestor's vsock path, but the host bridged
name-derived ports and recorded the fork's own path — shell/exec came up dead
(connection reset) while the workspace looked running. Start now adopts the
baked guest ports and vsock path from the snapshot manifest (explicit caller
values still win); for an original workspace the adopted values equal its own,
so behavior there is unchanged.

### Snapshot forks no longer drop the caller's port forwards

`CreateFromSnapshot` adopted the snapshot's baked network addressing by
replacing the whole network config, silently discarding port forwards the
caller requested for the fork. Forwards are realized host-side by the fork's
own pasta/forwarder and are invisible to the resumed guest, so adopting the
source's addressing now preserves them — a hibernate/wake cycle keeps the
workload's exposed services reachable.

### Snapshot of a restored workspace no longer loses its baked identity

Snapshotting a workspace that was itself started from a snapshot (a chain of
forks — for example repeated hibernate/resume cycles) recorded the wrong restore
identity in the manifest: the fork's own vsock UDS path instead of the
ancestor path baked into the loaded VM state, and the fork's host-side bridge
ports instead of the guest service ports the resumed guest actually listens
on. The NEXT restore in the chain then bind-mounted over the wrong directory
and bridged to ports nobody listened on, leaving shell/exec dead (connection
reset) while the workspace looked running. Capture now carries the baked
identity forward: `CreateFromSnapshot` threads the source manifest's vsock
path through the runtime config (new `bakedVsockUDSPath`), and both the
Firecracker and Apple VF capture paths prefer the baked guest ports. The fork
mount-exec path also creates the bind mountpoint when the ancestor's
directory does not exist on this host, so chained restores work on a fresh
node (bundle-restore scenarios), not just where the ancestor once ran.

## v0.8.5 - 2026-07-07

### Guarded-egress DNS no longer breaks on hosts with a local UDP :53 service

On hosts where a service holds a UDP port-53 socket in the init netns (for example
systemd-resolved on GCE Ubuntu 24.04), every guest DNS query under guarded or
strict egress timed out (`EAI_AGAIN`) even though the audit logged
`egress_dns_allow`. pasta mirrors host-bound UDP ports into the workspace
netns as wildcard `SO_REUSEADDR` listeners, and the mediator's spoofed-source
reply socket — which must bind the resolver's address on that same port 53 —
lacked `SO_REUSEADDR`, so its bind failed `EADDRINUSE` and the answer was
dropped before it reached the guest. The reply socket now sets `SO_REUSEADDR`
(the canonical transparent-proxy reply-socket setup), which also removes the
bind race between a guest's parallel A/AAAA answers toward the same resolver.
The same fix covers non-DNS UDP flows whose destination port collides with a
pasta-mirrored host port (for example NTP :123/:323).

A reply-delivery failure is also no longer silent: the DNS path now audits
`egress_dns_reply_error` (mirroring `egress_udp_reply_error`) instead of
discarding the error — the gap that made this failure look like a healthy
mediator with a dead guest resolver.

### Honest user-namespace detection on Ubuntu 24.04 (AppArmor userns restriction)

On hosts with `kernel.apparmor_restrict_unprivileged_userns=1` (the stock
Ubuntu 23.10+/24.04 default), user namespace *creation* succeeds but AppArmor
denies the confined process's own uid-map write — so `microagent doctor`
reported the host green while every rootless workspace boot died in the
supervisor jail with `unshare: write failed /proc/self/uid_map: Operation not
permitted`. Doctor's user-namespace probe now performs the same
`unshare --map-root-user` self-map setup the jail and `pasta` use, so these
hosts are reported as `userNamespacesAvailable: false` with the concrete
AppArmor remediation (the sysctl, or a targeted AppArmor profile — see the
troubleshooting guide).

Confinement `auto` now resolves against the same live self-map probe: a host
that blocks the rootless jail falls back to unconfined launches (per `auto`'s
documented semantics) instead of failing every boot, while a targeted AppArmor
profile that re-enables the jail is honored. Explicit
`MICROAGENT_CONFINEMENT=rootless` still fails closed. The pasta start-failure
hint now names the actual tripped gate (including the AppArmor restriction)
with its matching fix, and no longer points at the removed `--network nat`
mode.

## v0.8.3 - 2026-06-27

### Apple VF snapshots, restore, and fork are now on the shared contract path

Apple Virtualization.framework now has the same product-level snapshot shape as
Firecracker: pause/resume control, save-state configuration probes, supervisor
runtime-control capability fixes, snapshot creation, restore, fork, and the
library-owned backend capability contract. Snapshot artifacts are described in
the backend-neutral contract, and secret restore/purge behavior is enforced
through shared workspace semantics instead of adapter-only checks.

### Backend-aware egress capture reporting

The workspace layer now negotiates the egress capture provider by backend and
fails closed when an invalid provider is requested. Workspace status and inspect
output surface the active capture-provider report, and the Apple VF host-fd
egress datapath is wired into the backend-neutral feature contract.

### Contracts, docs, and E2E release coverage

The CLI/MCP feature contracts, backend-neutral coverage policy, and Apple VF
snapshot support docs were brought in line with the current behavior. Linux
Firecracker E2E regressions were fixed, experimental Windows Hyper-V release-tag
gating was removed, the E2E CI cutover is complete, and the E2E preflight now
detects macOS hosts that cannot run Apple Virtualization.framework instead of
misclassifying them as live VM-capable. A live Apple VF PR/tag workflow is ready
for self-hosted macOS/ARM64 runners with Virtualization.framework access, and
Apple VF force-stop control and live E2E assertions were hardened so stuck
helpers and early console closure produce structured state instead of false
release-lane failures.

## v0.8.2 - 2026-06-25

### VMM-process confinement is on by default (behavior change)

The Firecracker backend now confines the VMM process by default (`auto` mode) —
the per-VM jailer (Linux) / Seatbelt (macOS) confinement that was opt-in is now
the default for every workspace. Each microVM's VMM runs under a tightened
filesystem, process, and capability boundary with hardened signing, so a guest
escape is contained to the jail rather than the host. Confinement can still be
disabled per workspace for backends or environments that don't support it.

This closes the default-on confinement work (#11). It is paired with
event-driven per-VM reaping and supervisor fixes so confined VMs are tracked and
torn down reliably.

### Drop bridged/named/nat networking — user + isolated only

Removed the three host-netns network modes (`bridged`, `named`, `nat`) and the
entire privileged-networking subsystem they depended on: host TAP/bridge
creation, host nftables NAT, host-netns TPROXY provisioning, and the
`CAP_NET_ADMIN`/`setcap` requirement. Along with them go the
`microagent host setup-networking` command, the `network create/list/delete`
subcommands, the `network.create`/`network.list`/`network.delete` and
`host.networking.setup` MCP tools, and the `--unsupported`,
`--network-interface`, `--network-name`, and `--peer` flags.

What remains: `user` networking (pasta per-VM user namespace on Linux, VZNAT on
macOS — both unprivileged) and `isolated`. Egress mediation is unchanged in
behavior but now runs only inside the per-VM user-mode netns, so it no longer
needs any privileged host setup.

### `dispatch` — one-shot delegated work with an egress audit receipt

New `microagent dispatch <image> [command]` command: boot a throwaway microVM
under the egress guardrails you choose, run one command, and get back its result
**and** a mediator-written summary of everything it reached on the network — then
the workspace is torn down. The audit is written outside the guest's control, so
a prompt-injected or rogue task can neither forge nor suppress it. It is the
one-call "delegate this to an isolated machine and tell me what it did"
primitive. Use `run` for the same disposable boot without the receipt, or
`create` for a named workspace that survives.

### `--cred-swap <provider>` — one-word credential swap for built-in providers

New repeatable `--cred-swap PROVIDER[=ref]` flag on `create`/`run`/`dispatch`
(and a `cred_swap` param on the MCP `workspace.create`/`workspace.dispatch`
tools). The guest can *use* a provider API key it can never read: the real secret
is injected host-side at the mediator, the provider host is unioned into the
egress allowlist, and the generated swap config is written to a durable
per-workspace path so restart/restore/fork re-arm it. Requires `--egress guarded`
or `strict`. Unknown providers and pasted literal secrets are rejected up front.
Cred-swap protects the *task* credentials a guest uses, not the agent's own auth.

### Agentfile — an `agent:` block on the workspace spec (build-free agents)

A workspace spec can now carry an optional `agent:` block (entry, egress,
cred-swap), and `--file` now drives `run`/`dispatch`, not just `create`. So
`microagent dispatch --file agent.yaml` pulls a thin base, installs the SDK at
boot, drops the agent script, and runs it under the egress envelope with
cred-swap — no image build, no BuildKit. CLI flags override the spec; allow and
cred-swap sets union. Turnkey example Agentfiles for the OpenAI Agents SDK and
the Claude Agent SDK ship under `examples/agents/`. Turning egress fully `off`
now emits a one-line stderr warning so disabling mediation is never silent.

## v0.8.1 - 2026-06-22

### guarded egress mode is now the default (behavior change)

**Migration note:** the default egress mode changed from `mediated` to
`guarded`. Workspaces that omit `--egress` now deny internal destinations
(link-local/metadata 169.254/16, RFC1918, IPv6 ULA, CGNAT 100.64/10, loopback,
and east-west peers on named networks) while still allowing the public internet
freely with no allowlist required. DNS continues to resolve freely — the
protection is applied at connect time on the resolved IP, which also defeats DNS
rebinding attacks.

To restore the previous allow-all behavior: `--egress mediated`.
To permit a specific internal host: `--egress-allow <host-or-ip>`.

New audit events:
- `egress_internal_deny` — TCP connection denied to an inside address under
  guarded mode; includes `dst` and `internal: true` fields.
- `egress_udp_internal_deny` — UDP datagram denied to an inside address under
  guarded mode; includes `dst` and `internal: true` fields.

### windows-hyperv snapshot is unsupported (documented limitation)

- `snapshot create`, `start --from-snapshot`, and `create --from-snapshot` now
  fail closed with a clear message on backends that do not support snapshots,
  gated by a new `Snapshot` capability (true only for Firecracker). Windows
  Hyper-V cannot support snapshots: its HCS-direct (`LinuxKernelDirect`)
  compute systems have no guest-memory save-state — `HcsSaveComputeSystem`
  captures only device state (verified on a real host: a paused 512 MiB guest
  saves a 24 KB device-only file and the worker aborts), and the Hyper-V
  mechanisms that do save memory (`Save-VM`, checkpoints) belong to VMMS, which
  this backend deliberately does not use. Apple VF snapshot support remains
  planned (VZ `saveMachineStateTo`, macOS 14+). Use `commit` or `clone` on
  Windows Hyper-V instead.

### windows-hyperv runtime.json read tolerates a concurrent atomic rewrite

- Reading `runtime.json` now retries briefly on a transient read error. The
  supervisor rewrites the file atomically (temp file + rename); on Windows a
  concurrent reader — most often the freshly started runtime listener helper
  reading its config while `apply` rewrites the file — could hit "the process
  cannot access the file because it is being used by another process" during
  the rename window, which surfaced as an `apply` exec-bridge failure on loaded
  CI runners. A genuinely missing file still returns immediately, so the
  not-exist callers stay fast.

### windows-hyperv managed NAT subnet no longer collides with the Default Switch

- The managed `microagent-nat` HNS network is created on a subnet chosen to
  avoid overlapping any existing HNS network, instead of a hardcoded
  `192.168.127.0/24`. The Windows Default Switch (ICS) takes a dynamically
  assigned `/20` that can span `192.168.127.x`; when it did, HNS rejected the
  NAT create with a misleading "duplicate name exists on the network" (`0x34`)
  error, failing any `user`/`nat` workspace boot. This was the root cause of
  the windows-hyperv NAT failures that reproduced reliably on some hosts (and
  intermittently on hosted CI runners, depending on each runner's Default
  Switch subnet). The preferred `192.168.127.0/24` is still used when it is
  clear; otherwise the first non-overlapping candidate is chosen and the
  gateway is derived from it. Fails closed if every candidate conflicts.

### windows-hyperv pause/resume (P7)

- `microagent pause` and `microagent resume` now work on the Windows Hyper-V
  backend. `pause` freezes a running workspace's vCPUs in place via
  `HcsPauseComputeSystem` (state → `paused`); guest memory, the workspace disk,
  the compute system registration, the HNS endpoint, and the runtime listener
  helper are all preserved, so `resume` (`HcsResumeComputeSystem`) thaws the
  workspace back to `running` with its exec/shell bridges intact. Unlike the
  teardown controls, pause/resume never touch the runtime listener helper — it
  holds the compute system open and brokers exec, so it must survive a pause.
- Both HCS operations are asynchronous (they signal `SystemPauseCompleted` /
  `SystemResumeCompleted`), handled through the same callback-wait path as
  start/create. The wait is bound to a timeout so a standalone `pause`/`resume`
  CLI process — whose only goroutine is blocked on the HCS notification an
  OS-owned thread delivers — does not trip the Go runtime's deadlock detector,
  and so a transition that never signals fails closed.
- The `lifecycle-deep` windows arm now exercises pause → exec-rejected → resume,
  asserting the workspace freezes, exec is refused while paused, and the same
  exec channel answers again after resume with guest state preserved. The
  `snapshot/pause/resume` coverage row records windows-hyperv for vCPU
  pause/resume; memory snapshot remains Firecracker-only.

### windows-hyperv bridged networking DHCP

- Bridged mode now configures the guest NIC when the named HNS network or
  Hyper-V switch does not statically allocate an endpoint address. Previously
  the guest attached an interface but was left down with no IP (the cmdline
  carried a static address only, which such networks never provide), so
  bridged mode never actually reached the network. The supervisor now emits
  `ip=dhcp` for a bridged endpoint without a static address, and the guest's
  existing in-init `udhcpc` path brings the interface up from the bridged
  network's DHCP. Live-verified against the built-in ICS `Default Switch`: the
  guest DHCPs an address, installs a default route, and reaches the gateway.
  Networks that do allocate a static endpoint address keep the static path
  unchanged.
- The `networking-deep` windows arm gains a bridged segment (gated on a
  `Default Switch` being present) that asserts the guest NIC comes up addressed
  with a default route.

### windows-hyperv teardown diagnostics

- When a compute system survives teardown, the failure now reports what HCS
  says about it — its `State`, or, when the follow-up `Describe` call itself
  fails, that error — instead of a bare "still registered after teardown".
  Previously a failed `Describe` was swallowed, leaving the most diagnostic
  teardown failures with no evidence of the cause.
- The post-terminate unregistration wait grew from 60s to 120s. Hosted CI
  runners in a degraded-pool window have been observed to take over a minute
  to unregister a NAT-attached compute system after Terminate, because the
  HNS endpoint release that normally unblocks deregistration is itself async
  and slow under that load. The wait still fails closed.

### windows-hyperv docs and coverage-matrix closure (P6)

- Documented windows-hyperv as a supported backend rather than experimental.
  Dropped the "experimental" qualifier across `docs/` (backends, protocol,
  architecture, boundaries, glossary, networking, runtime-contract, contract,
  doctor, connect, persistent-workspaces, index) and the
  "supported experimentally" lifecycle-table cells on
  `docs/protocol/windows-hyperv.md`.
- Rewrote the windows-hyperv current-limitations section to the real
  remainder: bridged mode exists in the backend but is not yet live-verified
  (needs an external Hyper-V vSwitch); HNS user/nat segments need an elevated
  host; pause/resume and snapshots are planned, not yet implemented; named
  networks are planned (named-network attachment is currently Linux-only);
  survive-reboot registers a Scheduled Task when elevated and surfaces the
  manual `schtasks` command otherwise. The no-WSL/no-QEMU statements stay, and
  direct supervisor `console` is restated as a deliberate cross-backend
  non-goal (use `connect`).
- Reframed "Firecracker-only" pause/resume and snapshot phrasings in the CLI
  docs (`pause`, `resume`, `snapshot`, `start`, `create`,
  snapshots-and-forking) as current state ("currently implemented only for the
  Firecracker backend"), since those are planned on the other backends rather
  than permanent platform facts.
- Removed the stale `Windows Hyper-V|not-yet-practical|...` E2E_MATRIX row;
  windows-hyperv parity is now expressed by its presence in the per-feature
  backend-neutral rows. Dropped the now-unused `not-yet-practical` matrix class
  and the `Windows Hyper-V` required-feature key from the coverage-matrix
  validator. `coverage-matrix` passes.

### windows-hyperv mcp-lifecycle and perf parity (P6)

- `perf footprint` and `perf steady` work against windows-hyperv workspaces.
  The backend has no host guest PID (HCS owns the VM worker process), so the
  memory sample comes from the HCS statistics properties that already back
  `stats`, converted to the report's KiB unit; the report's `pid` stays 0.
- `perf boot` gains `--network <mode>` (`perf.BootOptions.NetworkMode`) so
  measured boots can run isolated; user/nat HNS setup needs elevation on
  Windows hosts and a boot benchmark should not. Empty keeps the backend
  default, so existing invocations are unchanged.
- The MCP `workspace.create` tool gains an optional `network` argument
  (mapped to `create --network`), letting MCP agents create isolated
  workspaces on hosts without HNS elevation.
- The `mcp-lifecycle` E2E scenario gains a windows-hyperv arm and joins the
  live workflow: the full create/start/exec/halt/delete lifecycle driven
  through MCP tool calls against a real VHD microVM with CLI parity
  assertions. The windows arm passes host-native path forms into the python
  driver (it spawns the CLI directly, outside Git Bash arg conversion) and
  pins `network: isolated`.
- The windows-hyperv public-surface scenario now exercises the perf
  surfaces: footprint/steady against a running workspace (HCS statistics)
  and `perf boot` validation failures plus two real isolated measured boots.

### windows-hyperv supervision parity: failed-state classification (P6)

- A windows-hyperv guest that exits on its own with a non-zero result is now
  reconciled to `failed` instead of `stopped`, so `supervise` with the
  `on-failure` policy restarts it — mirroring the firecracker inspect
  reconcile. The classification reads the guest's delivered result (with the
  same bounded 2s wait firecracker uses for a still-flushing result file);
  a missing result or exit code 0 remains a clean `stopped`, preserving
  poweroff/always semantics.
- `readRuntimeResult` now parses the on-disk result file with the guest's
  snake_case schema (the runtime listener writes the raw guest bytes). It
  previously unmarshaled the camelCase vmkit form, silently reporting
  `exitCode: 0` and empty stdout in inspect/status responses regardless of
  the real guest exit.
- The `supervision-deep` E2E scenario gains a windows-hyperv arm
  (`microagent-e2e-supervision-windows.sh`) and joins the live workflow:
  `never` is not restarted; `always` restarts a guest poweroff to the
  restart cap and ends `stopped`; killing only the supervise loop leaves the
  workspace running (HCS owns the VM, not the loop) and a manual stop is not
  policy-restarted; an `on-failure` guest exiting 42 is restarted to the cap,
  ends `failed`, and the result carries the guest exit code and stdout. The
  host-PID cancel assertion from the POSIX arms is expressed as
  workspace-state truth because windows-hyperv has no host runtime PID.

### windows-hyperv survive-reboot E2E (P6)

- The `survive-reboot` E2E scenario gains a windows-hyperv arm and joins the
  live workflow. It proves `supervise --install`/`--uninstall` generate and
  remove the boot unit. On Windows the boot unit is a Scheduled Task XML and
  registration via `schtasks /Create` requires an elevated token. The install
  step reads structured `--json` output and asserts both honest outcomes: an
  elevated host (hosted CI runners run elevated) registers the task for real
  (`enabled=true`, with the `/Delete` round-trip proven by the uninstall),
  while an unelevated host (the dev shell) gets "Access is denied" and must
  surface the manual `schtasks /Create /TN <label> /XML <file> /F` command
  alongside the written unit file (`enabled=false` fail-open contract).

### windows-hyperv exec-stream and health E2E (P6)

- The `exec-stream` and `health` E2E scenarios gain a windows-hyperv arm and
  join the live workflow. Both boot a real isolated VHD microVM: `exec-stream`
  proves `exec --stream` line delivery, non-zero exit propagation, and
  streamed/buffered parity; `health` proves health-spec validation, the
  declared exec probe succeeding in the booted guest, and the host-side
  `supervise` restart loop firing once on an unhealthy probe and exiting
  `failed`. The supervise loop, process model, and signal handling needed no
  Windows-specific changes — the existing host-side Go path works as-is.
- The health probe's leading-slash guest command (`/bin/true`) is guarded
  with `MSYS2_ARG_CONV_EXCL` so Git Bash does not rewrite it into a Windows
  path before it reaches the CLI; the guard is inert off Windows.

### windows-hyperv VHD-wrapped named volumes (P6)

- `microagent volume create/ls/inspect/rm` and `--volume name:/mountpoint`
  attach work on windows-hyperv. The host has no `mke2fs` for the VHD disk
  format, so volume creation builds the backing image in-process: an empty
  ext4 filesystem wrapped in a VHD footer (`volumes/<name>.vhd`), mirroring
  the VHD rootfs builder. The read-only feature flag tar2ext4 stamps in is
  cleared and a zero-filled reserved-space file pads the filesystem toward
  the requested size so the guest gets writable capacity.
- `microagent-guestinit` now deletes the reserved-space file at each rw
  disk mountpoint on first mount (previously only the rootfs root), so
  VHD-wrapped named volumes expose their full capacity to the guest. The
  removal is tolerated-absent, so ext4-lane backends and subsequent boots
  are unaffected.
- New exported `rootfs.BuildEmptyVolume(ctx, outputPath, sizeBytes)` builds
  the empty VHD-wrapped ext4 image; `volume.Create`, `volume.Path`, and
  `volume.DiskPath` now take a backend so the backing-file shape follows the
  backend's capabilities (bare `.ext4` on firecracker/apple-vf, `.vhd` on
  windows-hyperv).
- The volumes E2E scenario gains a windows-hyperv arm and joins the live
  workflow; it boots isolated microVMs to prove attach-by-name persistence
  across separate runs and single-attach enforcement.

### windows-hyperv guest-mediated cp/artifacts/commit (P6)

- `cp`, `artifacts get`, and `commit` work on windows-hyperv. The host has
  no ext4 tooling for VHD rootfs images, so file operations ride the
  guest's structured exec channel instead (Open Decision #1 resolved as
  guest-mediated copy): a stopped workspace gets a transient maintenance
  boot — guest init serves only the shell and exec channels, runs no
  service command, materializes no secrets — the operation streams file
  content in exec-sized chunks, and the workspace halts again. `commit`
  tars the filesystem inside the guest (kernel-managed trees excluded)
  and assembles the OCI layer from that stream directly, so symlinks and
  hard links survive without staging through a host filesystem that
  cannot represent them unprivileged.
- `cp` endpoint parsing treats Windows drive-absolute paths (`C:\dir`,
  `C:/dir`) as local paths instead of a workspace named after the drive
  letter — `microagent cp ws:/file C:/out` now works from any Windows
  shell.
- The lifecycle-deep lane covers guest-mediated cp out/in (the injected
  file shows up in a clone), artifact extraction, and stopped-state
  restoration after maintenance boots; the secrets lane uses the shared
  cp step on every backend; the commit-images scenario gains a
  windows-hyperv arm and joins the live workflow.
- windows-hyperv teardown no longer races registrations pinned by
  attachments: hosted CI runners began keeping NAT-attached compute
  systems registered after Terminate, failing halt/stop/kill against a
  registration that could never clear. Terminal controls now release the
  HNS endpoint as soon as terminate is issued (before the unregistration
  wait), wait for the runtime listener helper to actually exit first (it
  holds an open HCS handle that also pins registration), and the
  post-terminate wait grew from 30s to 60s. When a system still survives
  teardown, the failure now reports the live HCS state so the mechanism
  is visible in CI logs. Teardown still fails closed if the compute
  system never unregisters.

### windows-hyperv guest networking (hv_netvsc kernel)

- The packaged windows-hyperv kernel is now `kernels-6.12.22-r2`, which
  adds `CONFIG_HYPERV_NET` (hv_netvsc): guests finally get a netdev for
  their HNS endpoint. The kernel cmdline carries the endpoint's static
  configuration (`microagent_net_*`, same as the Firecracker boot args),
  so user/nat/bridged guests boot with an addressed `eth0`, a default
  route, and DNS applied. The networking-deep elevated segment asserts
  the in-guest NIC and route.
- Lifecycle controls on workspaces created before the JSON-array event
  history migrate the legacy JSON-lines `events.json` on first touch
  instead of failing the control and leaving the workspace
  uncontrollable.
- `kernel install` names the likely cause when it cannot replace an
  existing kernel on Windows (a running VM holding the file open)
  instead of surfacing a bare rename error.
- The windows-hyperv model bridge smoke's teardown ran on the test's
  already-canceled context, silently no-opping every cleanup command and
  leaking running compute systems on failure; it now tears down on its
  own context.

### windows-hyperv networking-deep + HNS live apply (P5)

- `apply` live-reloads host bind changes for existing port forwards on a
  running windows-hyperv workspace: the runtime listener helper restarts
  with the updated network config (in-flight forwarded connections and
  exec sessions drop, exactly like the Firecracker port-forwarder
  restart), the exec bridge is confirmed back before the apply reports
  success, and the replacement helper waits for the old one to release
  its binds so the rebind cannot race into address-in-use.
  `LiveNetworkApply` is flipped for the backend, and the non-support
  error for other backends names the backend instead of hard-coding the
  supported list.
- windows-hyperv `network inspect` reports the HNS endpoint address in
  CIDR form (prefix length from the endpoint, network subnet fallback),
  matching the Firecracker report shape. Known limitation surfaced by the
  lane and documented in code: the packaged windows-hyperv kernel lacks
  `CONFIG_HYPERV_NET` (hv_netvsc), so user/nat guests see no NIC for
  their HNS endpoint — publish and the model/secrets/exec bridges are
  unaffected (they ride hv_sock), and guest-side IP configuration is
  wired up the moment the kernel artifact ships the driver.
- The `networking-deep` scenario runs on the windows-hyperv lane: network
  mode and publish validation, isolated-mode semantics (no NIC, working
  loopback), the network inspect surface, and the live-apply guard rails
  run everywhere; the HNS segments — user-mode boot with published
  ports, the live host-bind apply round trip, and published-listener
  teardown on halt — need an elevated shell (HNS NAT creation) and run
  on the elevated CI runner, logged as deferred on non-elevated hosts.

### windows-hyperv secrets + model serving over hv_sock (P4)

- Secret delivery works on windows-hyperv: the runtime listener helper
  serves the resolved secrets bundle (and the on-demand secret API) on an
  hv_sock listener the guest dials at boot, with the same fail-closed
  resolution and audit records as Firecracker. The shared server moved to
  `pkg/secretxfer` (`ResolveBundle`/`Server`); the Firecracker supervisor
  delegates to it unchanged. The `secrets` E2E scenario runs on the
  windows-hyperv lane (the guest probe is baked in at build time instead of
  debugfs-copied) and in the live workflow.
- Model serving works on windows-hyperv: `create`/`start`/`run --model`
  pair the workspace through the same hv_sock bridge as published ports,
  and `llama-server.exe` resolves next to the binary on Windows installs.
  The `model-serving` E2E scenario gains a windows-hyperv arm (env-gated by
  `MICROAGENT_LLAMA_SERVER`, exactly like Linux), and a new
  `windows-hyperv-model-host` probe live-verifies the full pairing path —
  runner spawn, holder registry, guest `MICROAGENT_MODEL_URL` round trip —
  with a stand-in engine, so CI covers everything but llama.cpp itself.
- The windows-hyperv kernel cmdline now carries the secrets and model
  parameters (`microagent_secrets_port`, `microagent_secrets_api`,
  `microagent_model_fwd`) the guest init reads; they were silently dropped
  before, so the guest never fetched secrets or started the model
  forwarder. The snapshot-only secrets control port stays Firecracker-only.
- Model runner liveness checks work on Windows: `Signal(0)` always errors
  there, so every live runner self-healed out of the registry; the probe
  now asks the kernel for the process exit code.
- The guest brings up loopback even with no NIC (isolated network):
  guest-local services — the model forward helper, workload servers probed
  over exec — need 127.0.0.1 regardless of host networking.

### windows-hyperv writable rootfs (for real this time)

The guest can now write to its root filesystem. Three compounding causes,
all in the VHD rootfs build on hcsshim's tar2ext4:

- tar2ext4 stamps the ext4 `RO_COMPAT_READONLY` feature flag into every
  filesystem it writes (its images back shared read-only container
  layers), so the kernel forced a read-only root mount even though the
  HCS attach is writable and the cmdline says `root=/dev/sda rw`. The
  builder now clears the flag after conversion.
- tar2ext4 sizes the filesystem to its content with zero free blocks —
  `--size-mib` only capped metadata reservations — so even a writable
  mount hit ENOSPC on the first write. Rootfs builds now append a
  zero-filled reserved-space file padding the filesystem to the requested
  size; the guest init deletes it on first boot, releasing the space.
  Content that cannot fit the requested size now fails the build with a
  clear message instead of tar2ext4's internal error.
- Hard links exploded into full copies in the stage tar (the NTFS stage
  preserves them, but the tar writer didn't), so a ~4 MiB busybox image
  produced ~400 MiB of rootfs. The stage tar now emits tar hard links;
  this is also why 256 MiB rootfs builds "overflowed".

Setup commands, exec writes, and clone state inheritance on windows-hyperv
all silently hit `Read-only file system` (or ENOSPC) before this — the
windows-hyperv lifecycle-deep lane now live-verifies write → halt →
reboot → read → clone → read.

### windows-hyperv terminal controls actually stop the guest

- `stop`/`halt` performed an HCS shutdown call and recorded the terminal
  state immediately — but the call only initiates a guest shutdown the
  guest was ignoring (see below), so the workspace reported `halted` while
  the VM kept running, and a restart collided with the live registration
  ("a virtual machine or container with the specified identifier already
  exists"). Terminal controls now wait (bounded) for the compute system to
  unregister; graceful controls give the guest a 15s window and then
  escalate to terminate, container-runtime style. A system that survives
  terminate fails the control instead of recording a state the host does
  not match.
- `microagent-guestinit` now honors the init power-signal protocol
  (SIGUSR1/SIGUSR2/SIGTERM → sync + power off). The kernel's
  orderly-poweroff helper signals PID 1 rather than calling reboot(2), so
  host-initiated graceful shutdown was silently ignored on every backend
  image whose `/sbin/poweroff` defers to init.

### status of a missing workspace

- `status`/`inspect` on a workspace that does not exist now report
  `workspace <name> not found` on every backend instead of the raw
  state-file open error. Corrupt state files still surface as-is.

### windows-hyperv lifecycle-deep E2E lane

- The `lifecycle-deep` scenario runs on the windows-hyperv backend in the
  unified E2E runner: create (+dry-run and validation failures), start with
  channel-true exec/shell readiness, status/inspect/ps, connect `--send`,
  structured exec, logs, the JSON-array events history, HCS-backed stats on
  a busy guest, halt + restart, stop/kill idempotency, clone of a stopped
  workspace booted and exec'd, quarantine semantics, artifacts list, images
  list, prune, and delete cleanup. `cp`/`commit`/artifact extraction stay
  deferred pending the guest-mediated VHD copy decision; mke2fs segments
  stay on the ext4 lanes. The live-windows-hyperv workflow runs the lane.

### windows-hyperv events/stats

- `stats` works for windows-hyperv workspaces: there is no host guest PID
  (HCS owns the VM worker process), so the sample reads HCS properties —
  CPU percent from two `Statistics.Processor.TotalRuntime100ns` samples over
  a short interval, memory from the VM `Memory` property's page counts
  (LinuxKernelDirect VMs report zeros in `Statistics.Memory`), storage
  counters as reported.
- `events` works for windows-hyperv workspaces: the supervisor maintained
  the history as JSON lines while the events CLI reads the backend-neutral
  JSON-array shape; events.json is now a capped, atomically rewritten array
  with the same semantics as the Firecracker supervisor.

### windows-hyperv lifecycle: honest liveness, supervise, clone, boot units

- `inspect`/`status` now reconcile a vanished compute system: a guest that
  exits on its own marks the workspace `stopped` (listener helper reaped,
  network endpoint cleaned) instead of reporting a stale `running` state.
  This makes the `supervise` restart loop work on windows-hyperv: restart
  policies observe real terminal states (live-verified: 3 supervised boots,
  3 clean stop transitions, exit at max-restarts).
- `stop`/`halt`/`kill` are idempotent when the compute system is already
  gone: the control's goal is achieved, so the transition records normally
  instead of a spurious `failed` event.
- `clone` works for stopped windows-hyperv workspaces: the VHD copies with
  the manifest rewrite, and the kernel cmdline now tells the guest its
  runtime shell/exec vsock ports (guestinit's kernel-config override wins
  over the source's baked ports), so a cloned workspace boots, connects,
  and execs on its own ports.
- `supervise --install` writes a Windows Scheduled Task (logon trigger,
  restart-on-failure) where Linux/macOS emit systemd/launchd units, with the
  same graceful degradation when registration needs an elevated shell; the
  survive-reboot E2E scenario runs on the windows-hyperv lane.

### Windows lane in the unified E2E runner

- `scripts/dev/microagent-e2e.sh` now runs under Git Bash on a Windows host
  with the windows-hyperv backend: Windows host detection and HCS preflight
  in `e2e-lib.sh`, `MINGW`/`MSYS`/`CYGWIN` hosts map to the windows-hyperv
  lane in every backend-neutral scenario (un-ported scenarios self-skip with
  the lane named), and the portable scenarios — coverage-matrix, contract,
  help-usage, mcp-stdio, registry-auth, text-output, init — all pass on
  Windows.
- New `windows-hyperv-*-host` probe scenarios (lifecycle, connect, exec,
  transport) wrap the gated Go smokes per the host-probe convention, and the
  E2E feature matrix records windows-hyperv coverage for run/create/start,
  connect, and exec. The live-windows-hyperv workflow runs the lane.
- `public-surface` and `transport-deep` join the windows-hyperv lane:
  public-surface dispatches to a VHD-native Windows arm (CLI surface checks
  plus a real boot/run/result cycle over hv_sock; ext4/debugfs segments stay
  deferred until VHD volumes and guest-mediated copy land), and
  transport-deep rides the mediation smoke as its Windows transport
  contract.
- `perf footprint`/`perf steady` RSS sampling was POSIX-only (`ps -o rss=`);
  Windows now reads the process working set via `K32GetProcessMemoryInfo`.
- `run -v` parses volume specs from the right, so a Windows drive-letter
  source (`C:\data:/workspace:rw`) is rejected with the intended host-bind
  message instead of a spec parse error.
- The remaining Windows-hostile unit tests pass on a Windows host: debugfs
  fake-shim log normalization handles cmd quoting, the missing-rootfs status
  check accepts the Windows error text, and the registry-auth rootfs E2E
  builds the host-native format (VHD needs no mke2fs).

### windows-hyperv structured exec over Hyper-V sockets

- `microagent exec` (buffered and `--stream`) now works against running
  `windows-hyperv` workspaces: the supervisor bridges the host
  `127.0.0.1:<execPort>` TCP listener to the guest's structured exec service
  over Hyper-V sockets, the same mechanic as `connect` and published ports.
  `Capabilities.StructuredExec` is now true for `windows-hyperv`, which also
  enables `health.exec` probes.
- windows-hyperv readiness now reports channel truth instead of "HCS compute
  system started": `shellReady` comes from a bounded Hyper-V socket dial of
  the guest shell service (`Capabilities.ShellReadinessProbe` flipped true),
  `execReady` from a structured exec round-trip, and `guestReady` from the
  recorded runtime state, matching Firecracker semantics.
- The supervisor moves the host exec bind off unbindable ports (the default
  exec range overlaps the Windows dynamic TCP range, so ephemeral outbound
  connections can transiently hold one) onto a free port while preserving the
  guest's Hyper-V socket service port, and detached `start` fails closed when
  the listener helper's exec bridge never accepts instead of reporting a
  running workspace with a silently dead exec channel.
- New gated live smoke `TestWindowsHyperVExecSmoke` (buffered exec, `--stream`
  ordering, non-zero exit propagation, channel-signaled readiness) wired into
  the `live-windows-hyperv` workflow.

## v0.8.0 - 2026-06-12

This release moves microagent from the 0.1.x line to 0.8.x. The jump reflects
where the project actually is: 0.8.x is the mature pre-1.0 development line,
and 0.9.x is reserved for stabilization and 1.0 readiness. The version jump
itself changes no behavior. Minor releases may still include breaking changes
before 1.0; they are called out explicitly below.

### Workspace model pairing

- Workspaces can now be paired with a local model at creation: `create
  --model` (or `model:` in a workspace spec) resolves the model, pulls it if
  missing, ensures a host-side runner, and bakes
  `MICROAGENT_MODEL_URL`/`OPENAI_BASE_URL` into the guest env so the pairing
  survives across boots. The canonical model ref is persisted in the spec,
  manifest, and workspace options.
- `start` re-pairs from the manifest on every boot, auto-pulling a missing
  blob the way `run` does. `halt`/`stop`/`kill`/`delete` release the
  workspace's runner holder on success.
- `supervise` re-pairs the manifest model before every supervised boot,
  including policy restarts — previously a policy-driven restart of a paired
  workspace came back without a model runner and a dead
  `MICROAGENT_MODEL_URL`.
- Model pairing is exposed on the MCP `workspace.create` tool.

### Fixes

- Companion processes (vsock listener, port forwarder) no longer leak when a
  detached user-network workspace's guest exits on its own. The foreground
  exit path reaps recorded companion PIDs before the final state write,
  companions bound their own lifetime to the workspace state, and `delete` is
  refused while recorded companions are still alive even when the VM process
  entry is a dead PID. Previously the companions and the published port
  binding leaked forever.
- Snapshotting a running workspace (and pause/resume) no longer drops runtime
  config fields from `runtime.json`. State rewrites previously copied a
  hand-picked field list that predated shell/exec ports, secrets wiring, and
  model pairing, so exec and shell failed after a snapshot until the next
  halt/start. The request config is now rebuilt from the recorded state
  wholesale.
- `create --setup` no longer loses the OCI image env on later boots. The
  setup boot's guest-config reset previously carried operator env only, so
  setup-created workspaces lost the image `PATH` and exec failed with
  exit 127. The reset is now composed in the rootfs builder with the same
  merged image + request env as the initial guest config.
- `exec` no longer scans the guest command's argv after the `--` separator
  for help flags: `exec ws -- psql -h x` runs `psql` in the guest instead of
  printing the microagent exec usage. Only CLI-side arguments trigger help.
- The guest now gets the standard `/dev/fd` -> `/proc/self/fd` symlink plus
  `/dev/stdin`, `/dev/stdout`, and `/dev/stderr`. Stock entrypoints using
  bash process substitution worked incorrectly before — the official postgres
  image died in `initdb` with "could not open file /dev/fd/63".
- `create <name> --secret/--secrets-env-file/--secret-on-demand <value>`
  argument reordering: the flag reorder tables now know these flags take
  values (and that `--secrets-audit` is boolean), so flag-after-name
  invocations no longer fail with "unexpected create argument".

### CLI and install

- `serve mcp` launched from an interactive terminal now prints MCP client
  setup guidance instead of waiting silently for stdio frames.
  **Breaking:** `serve mcp` is deliberately no longer listed in CLI help —
  it is launched by MCP clients, not typed interactively. The command itself
  is unchanged.
- Local builds made with `scripts/dev/build-local.sh` now report the latest
  stable version plus the source SHA, for example `0.8.0-8780315-dirty`, so
  they are easy to distinguish from stable Homebrew builds.
- Source installs are friendlier: the Makefile and install docs cover the
  full from-source flow, and `microagent version` distinguishes dev builds.

### Library

- **Breaking:** `workspace.ResetGuestConfigCommand` is removed; rootfs
  `BuildRequest` gains `ResetFinalConfig`/`FinalCommand`/`FinalMode` and the
  workspace layer reports the final command and mode via the new
  `FinalCommandAndMode` helper.
- `SuperviseOptions.BeforeStart` is invoked before every supervised boot
  (initial and each policy restart); a hook error is treated as a failed
  start and follows the restart policy.

### Docs and examples

- The docs site was rewritten end to end: new quickstart and "coming from
  Docker" getting-started pages, decision-first concept pages, a CLI
  reference covering every command, six new task guides, and a first-agent
  tutorial that follows the real start/status/result flow.
  **Breaking:** the `recipes/` docs section moved to `guides/`, so old
  recipe URLs no longer resolve.
- **Breaking:** the examples were renamed from "body" to "agent" terminology:
  `examples/minimal-body*` -> `examples/minimal-agent*`, `body.py` ->
  `agent.py`. The OpenAI example now reads `OPENAI_MODEL` and relies on the
  SDK's base-url env.

## v0.1.46 - 2026-06-10

- Retired the `microagent-rc` Homebrew formula. Only stable releases ship to
  the tap; release candidates remain git tags validated by local builds and
  the tag-gated live CI suites. The tap-update workflow skips `-rc` tags.
- Live Windows Hyper-V smokes now run in CI on GitHub-hosted runners
  (nightly, on release tags, and on demand): hosted windows-latest runners
  ship with the Hyper-V role active, so the parity smokes no longer depend on
  a self-hosted runner. The workflow installs the default guest kernel via
  `microagent kernel install`; its previous failures were a workflow-file
  validation error, now caught statically by a new actionlint step in CI.
- `doctor` now verifies that unprivileged user namespace creation actually
  works by running a live `CLONE_NEWUSER` probe instead of trusting the
  classic userns sysctls alone. On hosts where AppArmor blocks the clone
  (stock Ubuntu 24.04 sets `kernel.apparmor_restrict_unprivileged_userns=1`),
  `userNamespacesAvailable` and `userNetworkReady` are now reported `false`
  with a remediation hint; previously doctor reported user networking ready
  while pasta failed at runtime. `userNetworkReady` also now requires user
  namespaces in addition to pasta.
- Fixed Firecracker `user` networking on hosts with older pasta releases
  (including the stock Ubuntu 24.04 package): pasta was invoked without a
  `--` option terminator, so getopt-permuting versions tried to parse the
  supervisor's `--request-json` flag as their own and aborted.
- The live Linux parity workflow now runs on GitHub-hosted KVM runners —
  nightly against main, on every release tag, and on demand — instead of
  targeting a self-hosted runner label that was never registered (the suite
  had never executed in CI).
- Centralized backend differences in a declarative `vmkit.BackendCapabilities`
  table (structured exec, live network apply, rootfs format, runtime-state
  ownership, detached-start style, shell transport and probing, block-device
  naming). The workspace layer now consults capabilities instead of branching
  on backend constants; unknown backends get zero capabilities (fail closed).
  No behavior change for existing backends.
- Workspace dispatch errors now preserve the underlying error chain, so Go
  library callers can use `errors.Is`/`errors.As` (for example to reach the
  supervisor's `*exec.ExitError`). Error message text is unchanged.
- CI now collects unit-test coverage on the Linux job (summary in the log, full
  profile uploaded as an artifact) and caches the Apple VF supervisor Swift
  build on the macOS job.
- README links to the releases page instead of hardcoding the latest version.
- **Security:** `model pull` now verifies downloads against the upstream
  digest. The expected LFS sha256 for the file is fetched from the Hugging
  Face paths-info API before the download, and the pull fails closed — on a
  digest mismatch, on a file that is not LFS-tracked, or when the upstream
  digest cannot be resolved — without writing a blob or an index record.
- **Security:** debugfs requests (`cp`, `artifacts get`) are no longer built
  by raw string concatenation. Every `-R` argument is validated (no quotes,
  control characters, or option-like leading dashes) and double-quoted, and
  remote workspace paths are validated in the copy layer itself so
  manifest-derived artifact paths get the same checks as CLI endpoints. As a
  side effect, local target directories containing spaces now work with
  `cp`/`artifacts get`.
- **Security:** OCI layer extraction now rejects entry names, hardlink
  targets, and symlink targets containing backslashes, which the
  slash-oriented traversal checks previously treated as plain name characters
  but Windows filesystem APIs treat as path separators. The Windows symlink
  marker fallback is also written through the `os.Root` sandbox instead of a
  host-path join outside it.
- **Breaking (Go library):** `workspace.ExecWithMetadata` now returns
  `(ExecResult, ExecRetryMetadata, error)` with the error last, matching Go
  convention. CLI and MCP behavior are unchanged.
- Tightened host state-file permissions. Files written under the state dir
  (workspace manifests, runtime/process state, events, network/volume/image/
  model indexes, snapshot manifests, serial and supervisor logs, volume disks)
  are now created `0600`, and new state directories `0700`, so workspace
  topology and runtime configuration are no longer readable by other users on
  the host. Existing files keep their modes; user-requested outputs (`cp`,
  artifact exports, `init` scaffolds, kernel downloads) are unchanged.
- Secret-access audit appends and Hyper-V event-log appends now report file
  close errors instead of silently dropping a possibly unflushed record.
- Adopted golangci-lint (`.golangci.yaml`, enforced in CI alongside the
  existing race tests; `go vet` is included in the lint run). Added package
  documentation for the exported Go packages and removed dead code left over
  from the CLI-to-library refactor. New `make fmt`, `make lint`, and
  `make test-race` targets.
- Unified the pinned Go toolchain across all CI workflows (1.26.4).
- Completed user-defined named networks: workspaces join a network with
  `create`/`run` `--network-name <name>`. Each member gets a stable IP from the
  network subnet (persisted in the registry, surviving stop/start), members
  share a per-network managed Linux bridge so they reach each other directly,
  and `/etc/hosts` name resolution is injected at boot via the kernel-cmdline →
  guest-init seam (parallel to DNS). Deleting a workspace frees its address; the
  shared bridge is reaped once the last member stops. Firecracker/Linux only
  (requires `net.ipv4.ip_forward=1` and CAP_NET_ADMIN, as with `nat` mode);
  Apple Virtualization.framework NAT cannot share a subnet. `/etc/hosts` is a
  boot-time snapshot — restart a member to pick up peers that joined later.
  Builds on the named-network registry (`pkg/network`) added earlier.
- Brought Apple Virtualization.framework validation up to the backend-neutral
  E2E surface. The unified `scripts/dev/microagent-e2e.sh` runner now selects
  the Darwin/Apple VF scenarios on macOS, covering public CLI surface,
  lifecycle, networking, transport/mediation, supervision, volumes,
  commit-images, secrets, health, streaming exec, and Apple VF host probes.
  The latest full macOS arm64 run on `main` passed 23 scenarios with no skips
  or failures.
- Fixed Apple VF networking and transport parity issues found by the expanded
  suite: guest DHCP/DNS setup for Apple VF boots, publish/workspace-connect
  smoke races, external-DNS-independent networking probes, optional mediation
  readiness semantics, and TCP-vsock bridge connection handling.
- Added host networking readiness/setup visibility for Linux privileged network
  modes. `doctor` and host diagnostics now report IP-forwarding and supervisor
  `CAP_NET_ADMIN` readiness, and `host setup-networking` can persist
  `ip_forward` plus set the supervisor capability on Linux while stubbing
  unsupported hosts explicitly.
- Added managed named volumes: `microagent volume create/ls/inspect/rm` and
  attach-by-name with `--volume <name>:/mount`. A named volume is a
  platform-managed ext4 disk with a lifecycle independent of any one workspace
  (new `pkg/volume` registry at `<state-dir>/volumes/index.json` plus a backing
  `<name>.ext4`), the in-boundary analog of a container volume. Volumes are
  single-attach: at most one running workspace holds a volume at a time, a stale
  holder (a stopped or crashed workspace) is reclaimed automatically, and
  deleting a workspace releases the volumes it held. This is deliberately not the
  Docker volume model — no daemon, no drivers, no concurrent sharing.
- Added `microagent commit <workspace> <image-ref>` and `microagent images push`
  to snapshot a stopped workspace's rootfs back into an OCI image and push it,
  closing the previously one-way OCI→rootfs loop. commit extracts the rootfs
  unprivileged via `debugfs`, assembles a single-layer OCI image (new
  `pkg/ociimage`), and writes it to a local OCI image layout under
  `<state-dir>/images/oci`; `images push` (or `commit --push`) copies it to the
  registry with the standard Docker pull credentials. Unprivileged extraction
  does not preserve file ownership (content, modes, and symlinks are preserved).
- Added `supervise --install` / `--uninstall` to survive host reboot. `--install`
  writes and registers an OS init unit (systemd user unit on Linux, launchd agent
  on macOS) that runs `supervise <name>` at boot, so a long-running workspace
  survives a reboot without microagent adding a persistent daemon. The unit file
  is always written; if automatic registration can't run, the manual enable
  command is reported. Backed by the new `pkg/superviseunit`.
- Added user-defined named networks: `microagent network create/ls/rm`. A named
  network is a VM-independent record (new `pkg/network` registry at
  `<state-dir>/networks/index.json`) with an auto-allocated `/24` from
  `10.44.0.0/16` (or an explicit `--subnet`) and a gateway. `rm` fails closed
  while members exist unless `--force`. This is the registry foundation for
  multi-workspace networking; joining workspaces and cross-VM connectivity +
  name resolution are realized by the backend supervisor (follow-up).
- Implemented streaming structured exec (`exec --stream` / `workspace.ExecStream`).
  The guest now delivers stdout/stderr as incremental chunk frames followed by a
  terminal result frame, so long-running commands stream output live instead of
  buffering until completion. AX mode keeps emitting a single structured
  envelope. Previously `stream` mode was reserved but unimplemented.
- Added a `health:` block to the workspace spec and restart-on-unhealthy to
  `supervise`. An exec probe (guest command, Firecracker) or httpGet probe
  (host-side GET to a published port) runs while the workspace is running; after
  `retries` consecutive failures the wedged VM is force-killed and the restart
  policy (`on-failure`/`always`) restarts it. Closes the gap where supervise
  only restarted on exit, not on alive-but-wedged.
- Added `microagent init <name>` to scaffold a starter agent body project — a
  `microagent.yaml` spec, a provider-specific `body.py` (Anthropic, OpenAI, or
  Gemini via `--provider`), the shared `protocol.py`, and a runnable demo
  request. Fails closed on existing files unless `--force`. Backed by the new
  `pkg/scaffold` package.
- Refreshed README, install, architecture, boundaries, library, and MCP docs for
  the stable `v0.1.45` release, `microagent-rc` Homebrew formula, and current
  AX/MCP substrate boundary.

## v0.1.45 - 2026-06-01

- Added AX output mode for agent-facing structured CLI responses and errors.
- Added the `microagent serve mcp` stdio endpoint with workspace lifecycle,
  status, inspect, exec, cost-estimate, mutation-preview, idempotency, and
  capability-manifest tools.
- Added the structured exec protocol, guest service, host client, CLI command,
  and MCP wiring.
- Added runtime readiness signals for guest, shell, structured exec, result,
  and mediation state.
- Added mediation target readiness probing for running workspaces, with
  fail-closed errors for required mediation and non-error not-ready status for
  optional mediation.
- Added bounded retry handling for transient MCP structured-exec connection
  failures, including retryable error metadata and retry-exhaustion details.
- Added fast status/inspect readiness behavior for non-live workspace states.
- Expanded Linux/Firecracker E2E coverage for lifecycle, networking,
  mediation/transport, supervision, public CLI surface, and runtime contracts.
- Renamed the project, Go module, Homebrew formula references, and docs from
  `microagent-kit` to `microagent`; the CLI name and `~/.microagent` state
  layout are unchanged.
- Hardened workspace/rootfs security behavior from the May 2026 findings pass.
- Added Apple VF end-to-end mediation validation for guest-to-host vsock,
  host replies, and structured guest results.
- Fixed Apple VF mediation listener setup and transient socket copy handling.
- Added Linux Firecracker validation fixes for NAT and host firewall behavior.
- Project governance, contribution, conduct, and security guidance.
- CI coverage for Linux Go checks, documentation links, shell scripts, and dependency vulnerability scanning.
