package firecracker

// BinaryNotFoundError is the message reported when the Firecracker VMM cannot
// be resolved, from either the boot path (ResolveBinary) or the host probe
// `doctor` runs. It carries its own remediation: this failure blocks every
// workflow, so a bare "not found" leaves the operator with nothing to act on,
// while the neighbouring guest-init and MICROAGENT_FIRECRACKER failures both
// already name a next step.
//
// The wording matches what scripts/dev/*-smoke.sh already print when they skip
// for a missing VMM, so the CLI, the probe, and the dev scripts agree.
//
// The leading "firecracker binary not found" is load-bearing: downstream
// consumers surface this string (microagency's doctor renders it as a probe
// error) and tests match on that prefix, so remediation is appended rather than
// replacing it.
//
// Declared in a file without a _linux suffix so cross-platform callers — the
// diagnostics probe among them — can reference it on any host.
const BinaryNotFoundError = "firecracker binary not found; install it with `brew install geoffbelknap/tap/microagent` (or `make install` from a source checkout), or set MICROAGENT_FIRECRACKER to its path"
