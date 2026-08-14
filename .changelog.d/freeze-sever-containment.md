### Containment freezes and severs authority before forensic capture

`quarantine` now creates a durable execution fence, freezes guest vCPUs,
severs network, broker, published-port, and serial authority, captures memory
and disk while the guest remains frozen, and only then stops the VM into
custody. The structured result reports freeze, severance, capture, stop, and
custody separately. Capture failure leaves the guest frozen and severed for a
safe retry or an explicit `--no-capture` evidence-loss retry. The marker blocks
ordinary start, resume, restore, mutation, workspace deletion, and deletion of
the custody snapshot after interruption or restart. Linux KVM and Apple
Virtualization.framework implement the same phase contract.
