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
