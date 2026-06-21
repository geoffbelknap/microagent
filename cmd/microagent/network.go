package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/network"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runNetwork(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printNetworkHelp(stdout)
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "create":
			return runNetworkCreate(args[1:], stdout)
		case "list":
			return runNetworkList(args[1:], stdout)
		case "delete":
			return runNetworkRemove(args[1:], stdout)
		case "status":
			return runNetworkInspect(args[1:], stdout)
		}
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

func runNetworkCreate(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("network create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	subnet := fs.String("subnet", "", "Subnet CIDR (auto-allocated from 10.44.0.0/16 when omitted)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent network create <name> [--subnet <cidr>] [--state-dir <dir>]")
	}
	record, err := network.Create(stateDir, fs.Arg(0), *subnet)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Created network %q (%s, gateway %s)\n", record.Name, record.Subnet, record.Gateway)
	return nil
}

func runNetworkList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("network list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent network list [--state-dir <dir>]")
	}
	records, err := network.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"networks": records})
	}
	fmt.Fprintf(stdout, "%-20s %-18s %-15s %s\n", "NAME", "SUBNET", "GATEWAY", "MEMBERS")
	for _, r := range records {
		fmt.Fprintf(stdout, "%-20s %-18s %-15s %d\n", r.Name, r.Subnet, r.Gateway, len(r.Members))
	}
	return nil
}

func runNetworkRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	force := false
	fs := flag.NewFlagSet("network delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&force, "force", false, "Remove even if the network still has members")
	fs.BoolVar(&force, "f", false, "Remove even if the network still has members")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent network delete <name> [--force] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := network.Remove(stateDir, name, force); err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"removed": name})
	}
	fmt.Fprintf(stdout, "Removed network %q\n", name)
	return nil
}

func printNetworkHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent network

Inspect a workspace's network, or manage user-defined named networks.

Usage:
  microagent network <workspace>              Show a workspace's network
  microagent network status <workspace>       Show a workspace's network
  microagent network create <name> [options]  Create a named network
  microagent network list                      List named networks
  microagent network delete <name> [options]       Remove a named network

Options:
  --subnet <cidr>       Subnet for create; auto-allocated from 10.44.0.0/16 when omitted
  --force               Remove a network even if it still has members
  --state-dir <dir>     State directory

Join a workspace to a named network with create/run --network-name <name>:
members get a stable IP from the subnet, share a managed bridge (HNS network on
windows-hyperv), and resolve each other by name. Workspace attachment is
implemented by Firecracker on Linux (privileged) and windows-hyperv (elevated,
as a private HNS network); Apple VF does not currently implement
network.mode=named.
`)
}
