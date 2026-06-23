package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runNetwork(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printNetworkHelp(stdout)
		return nil
	}
	if len(args) > 0 && args[0] == "status" {
		return runNetworkInspect(args[1:], stdout)
	}
	return runNetworkInspect(args, stdout)
}

func runNetworkInspect(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := flag.NewFlagSet("network", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent network <workspace> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	result, err := workspace.Network(opts.StateDir, name)
	if err != nil {
		return err
	}
	return writeNetworkResult(stdout, result)
}

func printNetworkHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent network

Inspect a workspace's network.

Usage:
  microagent network <workspace>              Show a workspace's network
  microagent network status <workspace>       Show a workspace's network

Options:
  --state-dir <dir>     State directory
`)
}
