//go:build windows

package windows_hyperv

import "syscall"

func detachedListenerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
