### Guest egress dropped at the datapath is no longer silent

Guest traffic whose protocol carries no allowlistable destination identity —
IPv4 ICMP and any other non-TCP/UDP L4 — is dropped at the firewall before it
reaches the egress mediator. That drop was invisible: `ping` from inside a
mediated workspace reported `100% packet loss` with nothing recorded anywhere,
indistinguishable from a dead network or an unresponsive host. The rule
emitted NFLOG group 5, but nothing in microagent subscribes to it, so the
detail went nowhere unless an operator happened to attach their own reader.

The drop rule now also carries a counter, and the mediator samples it while it
serves, reporting increases into the same audit log every other egress
decision lands in. A blocked ping now shows up in `microagent egress` with the
reason it was blocked, carrying the new `unmediatable-protocol` signal and the
packet count.

The policy is unchanged — these protocols are still dropped, deliberately, and
for the same recorded reason. Only the silence is fixed.
