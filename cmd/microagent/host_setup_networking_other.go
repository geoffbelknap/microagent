//go:build !linux

package main

import "fmt"

func applyHostNetworking(supervisorPath string) error {
	return fmt.Errorf("host setup-networking is only supported on Linux")
}

func revertHostNetworking(supervisorPath string) error {
	return fmt.Errorf("host setup-networking is only supported on Linux")
}
