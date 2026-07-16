//go:build windows

package main

import (
	"context"
	"fmt"
)

// runEgressDatapath is the apple-vf host-fd datapath subprocess; apple-vf is
// darwin-only, so on Windows the subcommand only reports that it is
// unsupported.
func runEgressDatapath(ctx context.Context, args []string) error {
	return fmt.Errorf("egress-datapath: not supported on windows")
}
