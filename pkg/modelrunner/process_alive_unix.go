//go:build !windows

package modelrunner

import (
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if p.Signal(syscall.Signal(0)) != nil {
		return false
	}
	// A detached process that has been signalled becomes a zombie on Linux
	// (kill -0 still succeeds on zombies). Treat zombies as dead.
	return !processIsZombie(pid)
}

func stopProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
