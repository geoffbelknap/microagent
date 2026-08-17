### Interactive consoles detach and resize under full-screen applications

Full-screen terminal applications could make `microagent connect` appear to
ignore `Ctrl-]`. Those applications enable the extended terminal keyboard
protocol, which encodes the chord as a `CSI u` sequence instead of the legacy
single byte that `connect` recognized. The sequence reached the guest while
the host stayed attached.

Interactive shells also started on a fixed 80-by-24 PTY. Resizing the host
terminal did not reach the guest, so a TUI continued drawing with stale
dimensions and could corrupt its display.

`connect` now recognizes detach chords in both keyboard encodings. Guestinit
advertises a versioned console capability, `connect` sends the initial terminal
dimensions, and subsequent host resize events update the guest PTY. Capability
negotiation keeps older workspaces usable as byte-stream consoles; recreate an
old workspace to add live resize support.
