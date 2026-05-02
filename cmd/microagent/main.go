package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

var version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	if args[0] == "version" {
		fmt.Fprintf(stdout, "microagent %s\n", version)
		return nil
	}
	helperPath := os.Getenv("MICROAGENT_APPLEVF_HELPER")
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&helperPath, "helper", helperPath, "Apple VF helper path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	req, err := requestForCommand(args[0], fs.Args())
	if err != nil {
		return err
	}
	resp, err := vmkit.HelperClient{Path: helperPath}.Do(ctx, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encodeErr := enc.Encode(resp); encodeErr != nil {
		return encodeErr
	}
	return err
}

func requestForCommand(command string, args []string) (vmkit.Request, error) {
	switch command {
	case "host":
		if len(args) != 0 {
			return vmkit.Request{}, fmt.Errorf("usage: microagent host")
		}
		return vmkit.Request{Command: "host"}, nil
	case "check", "prepare", "start", "inspect", "stop", "kill", "delete":
		if len(args) != 1 {
			return vmkit.Request{}, fmt.Errorf("usage: microagent %s <request.json>", command)
		}
		req, err := readRequest(args[0])
		if err != nil {
			return vmkit.Request{}, err
		}
		req.Command = command
		if command == "check" {
			vmkit.NormalizeConfig(req.Config)
			if err := vmkit.ValidateRequest(req); err != nil {
				return vmkit.Request{}, err
			}
			return req, nil
		}
		return req, nil
	default:
		return vmkit.Request{}, fmt.Errorf("unknown command: %s", command)
	}
}

func readRequest(path string) (vmkit.Request, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return vmkit.Request{}, err
	}
	var req vmkit.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return vmkit.Request{}, err
	}
	return req, nil
}

func printHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent

Commands:
  host                 Print Apple VF host support
  check <request>      Validate a lifecycle request
  prepare <request>    Validate and persist prepared state
  start <request>      Validate and attempt VM start
  inspect <request>    Read persisted runtime state
  stop <request>       Mark a runtime stopped
  kill <request>       Mark a runtime forcibly stopped
  delete <request>     Delete persisted runtime state
  version              Print version information
  help                 Show this help

Options:
  -helper <path>        Override the Apple VF helper path
`)
}
