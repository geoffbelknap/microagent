//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "microagent-guestinit is a Linux guest binary")
	os.Exit(1)
}
