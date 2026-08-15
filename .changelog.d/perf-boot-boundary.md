### Keep perf boot teardown outside the readiness timer

`perf boot` now stops timing after the guest command result and removes its
disposable workspace afterward. Reports expose the timer edges, teardown
exclusion, and uncontrolled host page-cache condition as structured fields, so
the command and its documented measurement contract agree.

The one-command performance snapshot now records exact component paths and
SHA-256 hashes. A checkout-built run refuses to summarize results if the CLI
resolved a guest init or supervisor other than the matched source build.
