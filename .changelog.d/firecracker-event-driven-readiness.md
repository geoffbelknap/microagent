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
