### Apple VF run-bound enforcement is main-actor isolated

The Apple VF supervisor now makes the run-bound timer's main-actor isolation
explicit. This preserves Virtualization.framework queue confinement and removes
the Swift 6 data-race diagnostic emitted by release builds.
