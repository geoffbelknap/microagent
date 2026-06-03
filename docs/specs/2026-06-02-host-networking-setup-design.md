# Host networking setup + capability visibility — design

- Status: approved (design)
- Date: 2026-06-02
- Scope: Linux / Firecracker only (Apple VF networking is a different model, out of scope)

## Problem

microagent's `nat`, `bridged`, and `named` network modes silently require host
prerequisites that a fresh install does not have:

1. `net.ipv4.ip_forward=1` on the host.
2. The Firecracker supervisor holding `CAP_NET_ADMIN` in its effective,
   permitted, and inheritable sets, so it can create TAP devices, program NAT,
   create per-network bridges, and let Firecracker inherit the capability.

When these are missing, a user who selects one of those modes hits a runtime
error deep in workspace start. Nothing at install time or in `microagent
doctor` tells them the requirement exists or how to satisfy it. The only setup
helper that exists today (`scripts/dev/microagent-e2e-linux-network-setup.sh`)
is dev-only: it builds the supervisor from source and `setcap`s a copy under
`.cache/`, so it does not apply to an installed (Homebrew) microagent.

`isolated` and `user` modes need none of this. `user` mode uses `passt`
(already a Homebrew runtime dependency) for unprivileged outbound networking, so
it is the default that "just works." `nat`/`bridged`/`named` are an **explicit
privileged opt-in**; this work makes that requirement visible and one command to
satisfy.

## Goals

- A shippable, idempotent setup command that prepares an *installed* microagent
  for privileged networking.
- `microagent doctor` reports privileged-networking readiness and names exactly
  which modes are gated, with a one-line remediation.
- Homebrew caveats tell users about the one-time step and the post-upgrade
  re-run.
- The e2e runner surfaces the same remediation for the scenarios it skips.

## Non-goals (this round)

- **Persisting `CAP_NET_ADMIN` across `brew upgrade`.** File capabilities are a
  property of the binary's inode; `brew upgrade` installs a fresh binary and the
  capability is lost. Homebrew cannot re-apply it — formula `post_install` runs
  as the user, not root. We accept this and make `doctor` detect the missing
  capability and prompt a re-run (see "Living with non-persistence"). A
  Docker-style persistent privileged component is deferred (see Future work).
- macOS / Apple VF networking.
- Pre-creating bridges. Named networks create their per-network bridge at
  runtime (`prepareNamedNetworkForStart`) once the supervisor holds the
  capability, so setup does not need to provision one.

## Privilege model (decided)

`setcap` by default, with a documented `sudo` fallback.

- **Default:** `setcap cap_net_admin+eip` on the installed libexec supervisor.
  This matches the supervisor's existing ambient-capability design
  (`ensureNetAdminInheritable`): the binary must hold `CAP_NET_ADMIN` in
  effective+permitted+inheritable so Firecracker can inherit it. Once set,
  nat/bridged/named work with no per-run `sudo`.
- **Fallback:** for hosts that forbid file capabilities (hardened hosts, some
  CI), document running the supervisor under `sudo` / a capability-granting
  launcher. No file caps involved; survives upgrades trivially.

## Components

### 1. `microagent host setup-networking`

A new subcommand under the existing `host` noun.

Modes:

- `--check` — report current prerequisite status; **no host mutation**. Exit
  non-zero if not ready.
- (default) apply — make the host ready. **Requires root.** If not root, print
  the exact `sudo microagent host setup-networking` line and exit non-zero
  without mutating anything.
- `--revert` — undo what apply did (remove the sysctl drop-in, drop the file
  capability).

Apply performs exactly the two things the runtime needs:

1. Enable and persist IPv4 forwarding: write
   `/etc/sysctl.d/99-microagent.conf` with `net.ipv4.ip_forward=1` (survives
   reboot) and apply it live.
2. `setcap cap_net_admin+eip` on the resolved installed supervisor binary (same
   resolution `doctor` uses).

Idempotent: re-running is a no-op when already satisfied. Bottle-safe: operates
on the installed libexec binary, so it is the correct thing to re-run after a
`brew upgrade`.

Output names what changed (or what is already satisfied) and ends by pointing at
`microagent doctor` to confirm.

### 2. `microagent doctor` — networking section

Add a networking capability block to the doctor/host report. It reports the raw
facts and the derived per-mode readiness:

Facts:
- `net.ipv4.ip_forward` (on/off)
- supervisor `CAP_NET_ADMIN` present (via file caps on the resolved binary, or
  running as root)
- `passt` present on PATH

Derived per-mode readiness:
- `isolated`: ready always
- `user`: ready when `passt` is present
- `nat` / `bridged` / `named`: ready iff `ip_forward` AND supervisor
  `CAP_NET_ADMIN`

When nat/bridged/named are gated, emit a single remediation line:
`nat/bridged/named networking unavailable — run: sudo microagent host
setup-networking`. When `ip_forward` is set but the capability is absent (the
typical post-`brew upgrade` state), say so explicitly: the capability was likely
reset by an upgrade; re-run the setup command.

This makes the privilege state explicit and auditable, consistent with ASK
tenet 6 (all trust explicit and auditable).

### 3. Homebrew caveats (`homebrew-tap/microagent.rb`)

Append to the existing `caveats` block:
- `isolated` and `user` networking work out of the box.
- `nat`/`bridged`/`named` need a one-time `sudo microagent host
  setup-networking`.
- The capability is reset by `brew upgrade`; re-run the command after upgrading.
- Run `microagent doctor` to check networking readiness.

Homebrew re-prints caveats on `upgrade`, which reinforces the re-run reminder.

### 4. e2e runner messaging

- Add an actionable block to the final `microagent-e2e.sh` summary: for each
  skipped scenario, print how to unlock it (the network setup command / the
  privileged re-run), so the remediation is visible at the end of a run rather
  than only mid-stream.
- Fix `named-network`'s terse skip line so it points at the remediation like the
  networking scenarios already do.

## Living with non-persistence

Because file caps do not survive `brew upgrade`, the experience is:

1. Install → `user`/`isolated` work; `doctor` shows nat/bridged/named gated.
2. `sudo microagent host setup-networking` → all modes ready.
3. `brew upgrade` → capability wiped; nat/bridged/named gated again; the
   re-printed caveats and `doctor` both say to re-run; `sudo microagent host
   setup-networking` restores it.

This friction only affects users who opt into privileged networking; the default
(`user`/`passt`) is unaffected by upgrades.

## Testing

- **Unit (no privilege, no host mutation):**
  - `host setup-networking` argument/flag parsing and the not-root guard (prints
    the sudo line, mutates nothing).
  - doctor per-mode derivation: pure function from `(ip_forward, capability,
    passt)` booleans to readiness + remediation strings. This is the core logic
    and is fully unit-testable.
- **Integration (privilege-gated, skip-with-reason otherwise):** on a
  capability-capable host, run `host setup-networking` then a real `nat`/`named`
  e2e scenario and assert it passes. This is the genuine end-to-end
  verification of the privileged lane.
- Unit tests never mutate host state (CLAUDE.md rule 3); the apply path is
  exercised only by the privilege-gated integration lane.

## Cross-repo work

- `microagent` (this repo): the `host setup-networking` command, the doctor
  networking section, the e2e runner messaging.
- `homebrew-tap`: the caveats addition (separate PR).

## Future work (deferred, own design + ASK review)

A Docker-style persistent privileged component would remove both the per-upgrade
re-run and the standing file capability:

- Docker's privilege persistence comes from a root process launched by the
  system (systemd `docker.service`), not from file caps — upgrades replace the
  binary but it is still launched as root, so nothing is wiped.
- For microagent (daemonless, per-VM supervisor), the analogous options are an
  on-demand privileged netlink helper installed outside the bottle to a stable
  path, or a small privileged service. Both add a standing privileged surface
  that must be designed against ASK least-privilege and "the runtime is a known
  quantity," so they are out of scope here and tracked as a separate effort.
