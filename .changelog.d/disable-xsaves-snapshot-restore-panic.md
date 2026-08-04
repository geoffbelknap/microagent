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
