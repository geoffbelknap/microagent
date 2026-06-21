# Changelog

Use this file for release notes and the rolling list of changes that have not
been cut into a release yet.

## Unreleased

### windows-hyperv egress mediation for user and nat

Egress mediation now works on Windows Hyper-V for `user` and `nat` network
modes. Both modes use the same managed HNS NAT network, which is placed on a
no-uplink HNS topology at mediated start — the only path off the host for a
mediated workspace is through the host-side mediator.

- **Transparent TCP capture** runs inside the guest via nftables redirect
  rules; outbound connections are forwarded to the host mediator over a Hyper-V
  socket.
- **Per-workspace egress CA**: a fresh ECDSA P-256 CA is minted at workspace
  start. Its public certificate is delivered to the guest over Hyper-V socket
  and installed into the guest trust store, so per-SNI TLS interception works
  transparently without manual CA configuration.
- **TLS interception** (per-SNI MITM), **DNS resolution through the mediator**,
  and the full audit log (`microagent egress`) are all functional.
- **Host-enforced topology**: the no-uplink HNS network means a workspace cannot
  reach the internet except through the mediator. A compromised guest can break
  its own egress (fail-closed) but cannot bypass policy enforcement.
- **`nat` no longer requires `--unsupported`** on Windows Hyper-V: it shares
  the managed network with `user` and is reliably mediated. `bridged` remains
  unmediated and still requires `--unsupported`.
- Non-TCP/UDP traffic (ICMP and the like) is dropped fail-closed. IPv6 egress
  is dropped fail-closed while mediation ships v4-only.

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
