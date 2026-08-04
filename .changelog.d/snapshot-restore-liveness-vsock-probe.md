### Snapshot restore's liveness gate can now actually pass

`start`/`create --from-snapshot` held the workspace at not-running until the
resumed guest's exec service answered a probe dialed through the host TCP
port forward — but that forwarder is a detached companion process started
only after the gate passes, so the probe was dialing a port nothing was
listening on yet. Every snapshot restore with an exec port configured failed
closed after the liveness window elapsed, no matter how long the guest
actually took to come back.

The probe now dials the guest directly over the Firecracker vsock UDS, the
same path already used to rehydrate secrets immediately after a restore. The
vsock device is realized synchronously by `PUT /snapshot/load`, so it is
reachable before the host forwarder — or the guest itself — has done
anything else.
