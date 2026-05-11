//go:build !windows

package main

import "syscall"

func platformProcessStillActive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return err != syscall.ESRCH
	}
	return true
}
