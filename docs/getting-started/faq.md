---
title: FAQ
description: Short answers to the questions people ask before their first workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-13_

Quick answers with links to the full story. If your question isn't here, check
[Troubleshooting](/troubleshooting/) for symptom-indexed fixes, or the
[glossary](/concepts/glossary/) for a term you don't recognize.

## Does it run on Intel Macs?

No. macOS support requires Apple silicon and Apple Virtualization.framework;
there is no Intel Mac backend. See
[Host requirements](/concepts/backends/#macos).

## Can I run it in CI or a cloud VM?

Only if the runner exposes KVM. microagent's Linux backend needs `/dev/kvm`,
so a CI runner or cloud instance needs hardware virtualization, either bare
metal or nested virtualization on a hypervisor that supports it. Run
`microagent doctor` on the runner first - if it reports KVM unavailable,
you're on a host without that access, not hitting a microagent bug. Some
sandboxed shells also hide `/dev/kvm` from the process even when the host has
it; run `microagent` directly on the host, outside the sandbox wrapper. See
[Troubleshooting: KVM unavailable](/troubleshooting/#microagent-doctor-reports-kvm-unavailable-on-linux).

## How much overhead is there compared to a container?

More than a container, because it's a real VM: a kernel boot plus a memory
floor, not a shared-kernel process. Don't guess - measure it on your own host
and image:

```bash
microagent perf boot --iterations 5              # boot time, min/avg/max
microagent perf footprint <workspace-name>        # host RSS for a running workspace
```

See [`microagent perf`](/cli/perf/) for the full command set, or
[Reference measurements](/cli/perf/#reference-measurements) for iteration
counts, a one-command snapshot script, and why the numbers aren't printed
here.

## Do I need Docker installed?

No. microagent pulls OCI images directly and converts them into a bootable
rootfs - it doesn't shell out to Docker or require a Docker daemon. For
private registries, credentials come from `$REGISTRY_AUTH_FILE` or
`microagent registry login`; microagent never reads Docker's
`~/.docker/config.json` and never runs Docker's credential helpers. Public
images always pull anonymously. See [`microagent registry`](/cli/registry/).

## Does it work on WSL?

Yes, as a compatibility lane through the Linux backend, not a separate
product mode. WSL needs to expose the same Linux virtualization features a
native Linux host does - run `microagent doctor` inside WSL and it must
report those prerequisites as available. microagent does not fall back from
another host backend. See [Host requirements: WSL](/concepts/backends/#wsl).

## Can an agent see my API keys?

[Credential swap](/concepts/egress-mediation/#credential-swap) keeps the key
out of guest request state: an allowlisted, intercepted request leaves the
guest with a placeholder, and the mediator injects the real value host-side.
An upstream can still return or transform what it receives. Use a
[semantic broker grant](/guides/broker-grants/) when a bounded response must
also reject the exact injected value. Credential swap needs `--egress mitm`
plus an allowlisted host; see `--cred-swap` for the built-in provider shorthand.

## How do I get a secret into a workspace?

When the workload must read a credential itself, use
[`microagent secret`](/cli/secret/). The value is materialized inside the
guest as a file at `/run/secrets/<NAME>` (mode 0400, on tmpfs - never
written to the rootfs or any disk). microagent is a conduit, not a store: it
holds the value only in host process memory during delivery and does not
persist it anywhere. See [Deliver secrets](/guides/secrets/).

## Can two VMs share a volume?

No. A [named volume](/concepts/storage/#named-volumes) is single-attach: at
most one running workspace holds it at a time. Hand data between workspaces
by writing to the volume in one and attaching it to the next. The reasons
are in [limitations](/concepts/limitations/).

## What's the difference between `run` and `create`?

`run` is one-shot: it boots a microVM, runs your command, and tears down the
scratch state afterward (pass `--keep` to keep it). `create` (then `start`)
makes a persistent workspace - disk, identity, and event history stick
around between starts, so you `halt` it and `start` it again later instead of
recreating it. See [Quickstart](/getting-started/quickstart/) for the
one-shot path and [Persistent workspaces](/guides/persistent-workspaces/) for
the create/start/halt loop.

## Does my data survive a restart?

Yes, within a workspace's lifetime. Writes inside the guest persist in its
rootfs image across `halt`/`start`; `delete` discards them (a one-shot `run`
discards its scratch state by default unless you pass `--keep`). See
[Storage: the rootfs](/concepts/storage/#the-rootfs).

## What can a workspace reach on the network by default?

The public internet, but not your LAN or the host. The default `broker`
egress mode allows outbound connections to public destinations, denies
anything resolving to an internal address (RFC1918, link-local/metadata,
loopback, and the like), and records every decision. Nothing is intercepted
under the default mode - TLS interception is opt-in via `--egress mitm`. See
[Egress mediation](/concepts/egress-mediation/#the-egress-modes) and
`microagent egress <name>` to see what a workspace actually tried to reach.

## See also

- [Troubleshooting](/troubleshooting/) - fixes indexed by the error you're seeing
- [Limitations](/concepts/limitations/) - the deliberate refusals and where to go instead
- [Coming from Docker](/getting-started/coming-from-docker/) - command-by-command mapping
