### The built rootfs now keeps the image's file ownership and special mode bits

`microagent rootfs build` (and every command that builds one, `run` and
`create` included) populated the ext4 image with `mke2fs -d` from a staged
directory on the host disk. `mke2fs -d` only ever encodes what `stat()`
reports for those files. Every path came back owned by whichever host user
ran the build instead of the uid/gid the image declared, and setuid, setgid,
and sticky bits were dropped along the way.

A guest with no root-owned files silently breaks anything that drops
privileges or relies on a shared sticky directory. The error rarely points
back to ownership. On a `golang:1.26-bookworm` image, `apt-key` (running as
`_apt`) failed to create a temp file in a `/tmp` that had lost its sticky
bit, and the failure surfaced as a signing error instead.

The builder now records each staged entry's uid/gid and mode (including the
special bits) alongside the existing stage metadata, then corrects the built
ext4 image in place with a `debugfs -w` batch script before publishing it.
`debugfs` edits inode fields directly on the unmounted image, so the
correction needs no host privilege at all. A new `--debugfs <path>` flag
resolves that binary the same way the existing `--mke2fs <path>` flag does.
