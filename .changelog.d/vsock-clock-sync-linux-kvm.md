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
