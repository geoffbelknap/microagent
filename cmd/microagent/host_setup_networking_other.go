//go:build !linux

package main

import (
	"fmt"
	"os"
)

func applyHostNetworking(supervisorPath string) error {
	return fmt.Errorf("host setup-networking is only supported on Linux")
}

func revertHostNetworking(supervisorPath string) error {
	return fmt.Errorf("host setup-networking is only supported on Linux")
}

func maybeSelfElevate(revert, assumeYes bool, stdout *os.File) error {
	return fmt.Errorf("host setup-networking is only supported on Linux")
}

// printTProxyNATCheck is a no-op off Linux: the TPROXY nat prerequisites only
// exist on the Linux/Firecracker backend.
func printTProxyNATCheck(stdout *os.File) {}
