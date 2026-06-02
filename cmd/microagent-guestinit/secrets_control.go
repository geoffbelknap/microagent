//go:build linux

package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"golang.org/x/sys/unix"
)

// handleControlConn reads one ControlRequest, runs purge or rehydrate, and
// writes the ack. Pure (io.ReadWriter), so it is unit-testable.
func handleControlConn(rw io.ReadWriter, purge func() error, rehydrate func() error) {
	var req secretxfer.ControlRequest
	if err := secretxfer.DecodeMessage(rw, &req); err != nil {
		return
	}
	resp := secretxfer.ControlResponse{ProtocolVersion: secretxfer.ProtocolVersion, OK: true}
	var err error
	switch req.Op {
	case secretxfer.OpPurge:
		err = purge()
	case secretxfer.OpRehydrate:
		err = rehydrate()
	default:
		err = fmt.Errorf("unknown control op %q", req.Op)
	}
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
	}
	_ = secretxfer.EncodeMessage(rw, resp)
}

// serveSecretsControl listens on the guest control vsock port and serves
// purge/rehydrate requests from the host. purge scrubs /run/secrets; rehydrate
// re-fetches the bundle from hostSecretsPort and rewrites the files.
func serveSecretsControl(ctlPort uint16, hostSecretsPort uint16) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("open vsock control listener: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: uint32(ctlPort)}); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("bind vsock control port %d: %w", ctlPort, err)
	}
	if err := unix.Listen(fd, 4); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("listen vsock control port %d: %w", ctlPort, err)
	}
	purge := func() error { return secretxfer.PurgeSecrets(secretsDir) }
	rehydrate := func() error { return dialAndWriteSecrets(hostSecretsPort, secretsDir) }
	go func() {
		for {
			connFd, _, err := unix.Accept(fd)
			if err != nil {
				continue
			}
			conn := os.NewFile(uintptr(connFd), "secrets-ctl")
			handleControlConn(conn, purge, rehydrate)
			_ = conn.Close()
		}
	}()
	log.Printf("microagent-init: secrets control listening on vsock port %d", ctlPort)
	return nil
}
