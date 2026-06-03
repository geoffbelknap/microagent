//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
)

const sysctlDropIn = "/etc/sysctl.d/99-microagent.conf"

func applyHostNetworking(supervisorPath string) error {
	if err := os.WriteFile(sysctlDropIn, []byte("net.ipv4.ip_forward=1\n"), 0o644); err != nil {
		return fmt.Errorf("persist ip_forward: %w", err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("enable ip_forward: %w", err)
	}
	if _, err := exec.LookPath("setcap"); err != nil {
		return fmt.Errorf("setcap not found (install libcap2-bin / libcap): %w", err)
	}
	if out, err := exec.Command("setcap", "cap_net_admin+eip", supervisorPath).CombinedOutput(); err != nil {
		return fmt.Errorf("grant CAP_NET_ADMIN to %s: %w: %s", supervisorPath, err, out)
	}
	return nil
}

func revertHostNetworking(supervisorPath string) error {
	if err := os.Remove(sysctlDropIn); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", sysctlDropIn, err)
	}
	if _, err := exec.LookPath("setcap"); err == nil {
		_ = exec.Command("setcap", "-r", supervisorPath).Run() // best-effort
	}
	return nil
}
