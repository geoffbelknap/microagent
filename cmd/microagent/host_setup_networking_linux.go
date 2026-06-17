//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const sysctlDropIn = "/etc/sysctl.d/99-microagent.conf"

// elevateWithSudo re-runs microagent under sudo. It is a package var so tests
// can intercept the re-exec instead of replacing the process image.
var elevateWithSudo = defaultElevateWithSudo

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
	// Egress (UDP mediation) prerequisites: load the TPROXY kernel modules and
	// provision the nat-mode host-global routing (ip rule + local route +
	// sysctls) that the supervisor verifies fail-closed before starting a
	// nat-mode mediated workspace.
	if err := applyTProxyPrereqs(); err != nil {
		return err
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
	// Remove the TPROXY ip rule + local route and restore the sysctls. The
	// kernel modules are intentionally left loaded (other things may use them).
	if err := revertTProxyPrereqs(); err != nil {
		return err
	}
	return nil
}

// maybeSelfElevate runs when `host setup-networking` is invoked without root.
// In an interactive terminal it explains the privileged change, asks
// permission, then re-runs microagent under sudo using its own absolute path
// (which sidesteps sudo's PATH reset). Agent (AX) and non-interactive callers
// never get a surprise sudo.
func maybeSelfElevate(revert, assumeYes bool, stdout *os.File) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own path to re-run under sudo: %w", err)
	}
	childArgs := []string{"host", "setup-networking"}
	if revert {
		childArgs = append(childArgs, "--revert")
	}
	manualCmd := "sudo " + self + " " + strings.Join(childArgs, " ")

	// Agent/automation callers must never be prompted or silently elevated.
	if currentOutputMode() == outputModeAX {
		return fmt.Errorf("setup-networking requires root; re-run as root: %s", manualCmd)
	}

	if !assumeYes {
		if !stdinIsTerminal() {
			return fmt.Errorf("setup-networking requires root; re-run: %s (or pass --yes)", manualCmd)
		}
		if revert {
			fmt.Fprintf(stdout, "Reverting host networking setup needs root to:\n")
			fmt.Fprintf(stdout, "  - remove %s\n", sysctlDropIn)
			fmt.Fprintf(stdout, "  - drop CAP_NET_ADMIN from the supervisor (setcap -r)\n")
			fmt.Fprintf(stdout, "  - remove the egress TPROXY ip rule + local route and restore its sysctls\n\n")
		} else {
			fmt.Fprintf(stdout, "Enabling nat/bridged/named networking needs root to:\n")
			fmt.Fprintf(stdout, "  - set net.ipv4.ip_forward=1 (persisted in %s)\n", sysctlDropIn)
			fmt.Fprintf(stdout, "  - grant CAP_NET_ADMIN to the supervisor (setcap)\n")
			fmt.Fprintf(stdout, "  - load the egress TPROXY kernel modules and provision its nat-mode routing\n\n")
		}
		fmt.Fprintf(stdout, "About to run:\n  %s\n\n", manualCmd)
		ok, err := readConfirmation("Proceed?")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("cancelled; run it yourself with: %s", manualCmd)
		}
	}
	return elevateWithSudo(self, childArgs)
}

func defaultElevateWithSudo(self string, childArgs []string) error {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("sudo not found; run as root: sudo %s %s", self, strings.Join(childArgs, " "))
	}
	argv := append([]string{"sudo", self}, childArgs...)
	// Replace this process with sudo so its password prompt owns the TTY and the
	// exit code propagates directly. The re-exec'd copy runs as root and skips
	// the elevation branch via the euid check.
	return syscall.Exec(sudo, argv, os.Environ())
}
