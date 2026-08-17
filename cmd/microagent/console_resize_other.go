//go:build !linux && !darwin

package main

import (
	"net"
	"os"
)

func startConsoleResize(_ *os.File, _ net.Conn) (func(), error) {
	return func() {}, nil
}
