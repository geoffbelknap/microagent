# microagent E2E Feature Matrix

This matrix is the implementation guide for the full live E2E suite. Scenarios
are feature-specific and backend-agnostic. Backends are execution lanes that
provide setup, binaries, kernels, and capability notes.

## Backend Lanes

| Lane | Host | Backend | Required host capabilities |
| --- | --- | --- | --- |
| `firecracker` | Linux amd64 | Firecracker | KVM, Firecracker binary, supervisor, guest init, kernel, rootfs tools |
| `applevf` | macOS arm64 | Apple Virtualization.framework | signed Apple VF supervisor, arm64 kernel, guest init, rootfs tools |
| `hyperv` | Windows amd64 or arm64 | Hyper-V | future lane; not a release gate until implemented |

## Feature Scenarios

| Feature scenario | Shared contract | Current Linux coverage | Target backend lanes |
| --- | --- | --- | --- |
| `public-surface` | CLI/API output shape, request JSON, identity, host/doctor, rootfs/image cache, dry-run, invalid input, confirmations | `scripts/dev/microagent-e2e-public-surface.sh` | `firecracker`, `applevf` |
| `lifecycle` | create/start/status/ps/connect/logs/halt/resume/cp/clone/artifacts/quarantine/delete/force-delete | `scripts/dev/microagent-e2e-lifecycle-matrix.sh` | `firecracker`, `applevf` |
| `networking` | user/nat/isolated/bridged where supported, publish, outbound DNS/TCP, apply, invalid publish failures, runtime network fields | `scripts/dev/microagent-e2e-networking.sh` | `firecracker`, `applevf` |
| `transport` | required mediation, optional mediation, fail-closed mediation, raw vsock listeners, large responses, quarantine transport severing | `scripts/dev/microagent-e2e-mediation.sh` | `firecracker`, `applevf` |
| `supervision` | restart never/on-failure/always, max restarts, cancellation, signal handling, guest failure result capture, helper cleanup | `scripts/dev/microagent-e2e-supervision.sh` | `firecracker`, `applevf` |

## Capability Exceptions

Capability exceptions are backend-specific facts, not separate contracts.
Feature assertions should check the shared behavior and record a skipped
assertion only when the capability is genuinely unavailable.

| Capability | Firecracker lane | Apple VF lane | Contract handling |
| --- | --- | --- | --- |
| Host acceleration | KVM required | Virtualization.framework required | Host lane preflight, not feature behavior |
| User networking internals | Firecracker user-mode networking, host helper process | Native Virtualization.framework attachment and host forwarders | Assert declared/runtime network behavior, not implementation details |
| NAT configuration detail | Linux can expose deterministic TAP/bridge/static network data | Apple VF NAT is backend-managed and does not expose deterministic guest lease details | Assert mode, readiness, outbound DNS/TCP, and available runtime fields |
| Bridged setup | Linux bridge and host networking tools | Requires Apple restricted networking entitlement | Treat entitlement failure as fail-closed; run bridged positive check only where entitled |
| Raw vsock plumbing | `/dev/vhost-vsock` and Firecracker vsock listener helpers | virtio-vsock support in Apple VF supervisor | Assert guest-to-host behavior and readiness fields |
| Kernel install | `microagent kernel install --backend firecracker --arch amd64` | Apple VF kernel is supplied by lane setup | Keep kernel acquisition in lane setup |
| Supervisor build | Go Firecracker supervisor | Swift Apple VF supervisor with signing/entitlement | Keep supervisor build in lane setup |
| Guest architecture | amd64 | arm64 | Scenario data must select image/kernel/guest-init by lane |

## Implementation Rules

- Scenario names should describe features, not backends.
- Backend selection should be an option or environment input to the feature
  scenario.
- Backend-specific setup should live in small lane helpers that export paths and
  capabilities.
- Assertions should target public CLI/API output, runtime state, guest-visible
  behavior, or host-visible service behavior.
- Do not assert Firecracker-only TAP, nftables, KVM, PID, or Linux bridge
  details in feature scenarios.
- Do not assert Apple VF supervisor implementation details unless the feature is
  the supervisor protocol itself.
- When a backend cannot support a capability, record the skipped capability with
  a concrete reason and keep the shared contract unchanged.
- Every backend gap discovered by a feature scenario should produce either a
  focused implementation fix or a documented capability exception.

## Initial Work Order

1. Convert lifecycle coverage first; it exercises the broadest shared surface.
2. Convert networking next; it has the most capability-specific setup.
3. Convert transport after networking; it depends on reliable guest/host
   connectivity and quarantine semantics.
4. Convert supervision once lifecycle stop/delete behavior is stable across
   lanes.
5. Expand public-surface coverage after the feature lanes expose the reusable
   backend setup shape.
