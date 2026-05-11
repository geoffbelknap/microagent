//go:build !linux

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func main() {
	resp := vmkit.Response{
		OK:      false,
		Backend: vmkit.BackendFirecracker,
		Error:   "firecracker supervisor is only supported on linux",
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
	fmt.Fprintln(os.Stderr, resp.Error)
	os.Exit(1)
}
