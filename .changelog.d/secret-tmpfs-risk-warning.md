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
