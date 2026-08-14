### Successful setup now leaves a valid workspace verification baseline

`create --setup` measured the rootfs before booting the one-time setup commands
and updated only the generated config-disk measurement after they succeeded.
Any setup that changed the rootfs therefore left the new stopped workspace in
verification failure even though setup exited successfully.

Successful setup now records the resulting rootfs, final boot config, and
setup-complete state as one manifest checkpoint. If either artifact cannot be
measured or the checkpoint cannot be recorded, setup stays incomplete and the
operation fails closed instead of trusting a partial result. Kernel and guest
init measurements remain anchored to their pre-boot records.
