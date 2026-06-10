//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"golang.org/x/sys/unix"
)

const secretsDir = "/run/secrets"
const secretsConnectTimeout = 30 * time.Second

// writeFetchedSecrets fetches the bundle from rw and materializes it under root.
// Pure orchestration: no vsock dial or mount, so it is unit-testable.
func writeFetchedSecrets(rw io.ReadWriter, root string) error {
	bundle, err := secretxfer.FetchBundle(rw)
	if err != nil {
		return fmt.Errorf("fetch secrets: %w", err)
	}
	return secretxfer.WriteSecrets(root, bundle)
}

// dialAndWriteSecrets dials the host secrets port, fetches the bundle, and
// writes it into root (no mount). Used for boot delivery and for rehydrate.
func dialAndWriteSecrets(port uint16, root string) error {
	fd, err := dialHostVsock(uint32(port), secretsConnectTimeout)
	if err != nil {
		return fmt.Errorf("connect secrets vsock port %d: %w", port, err)
	}
	conn := os.NewFile(uintptr(fd), "secrets-vsock")
	defer func() { _ = conn.Close() }()
	return writeFetchedSecrets(conn, root)
}

// fetchAndWriteSecrets mounts a tmpfs at /run/secrets and writes the bundle.
// Any failure is fatal to boot: a workload must never run without its declared
// secrets.
func fetchAndWriteSecrets(port uint16) error {
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", secretsDir, err)
	}
	if err := unix.Mount("tmpfs", secretsDir, "tmpfs", 0, "mode=0700"); err != nil {
		return fmt.Errorf("mount tmpfs at %s: %w", secretsDir, err)
	}
	return dialAndWriteSecrets(port, secretsDir)
}
