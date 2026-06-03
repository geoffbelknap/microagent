//go:build windows

package modelrunner

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

// processIsZombie always returns false on Windows; zombie processes don't exist
// in the Windows process model.
func processIsZombie(_ int) bool {
	return false
}
