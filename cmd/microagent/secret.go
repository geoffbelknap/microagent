package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/secret"
)

func runSecret(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printSecretHelp(stdout)
		return nil
	}
	switch args[0] {
	case "check":
		return runSecretCheck(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown secret command: %s", args[0])
	}
}

func runSecretCheck(ctx context.Context, args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printSecretHelp(stdout)
		return nil
	}
	fs := flag.NewFlagSet("secret check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	entries := fs.Args()
	if len(entries) == 0 {
		return fmt.Errorf("usage: microagent secret check NAME=<scheme>:<ref> [NAME=<ref> ...]")
	}
	registry := secret.DefaultRegistry(os.Getenv, func(msg string) {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	})
	results := make([]secret.CheckResult, 0, len(entries))
	allOK := true
	for _, entry := range entries {
		res := registry.Check(ctx, entry)
		results = append(results, res)
		if !res.OK {
			allOK = false
		}
	}
	if outputJSON(stdout) {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		for _, res := range results {
			writeSecretCheckLine(stdout, res)
		}
	}
	if !allOK {
		return cliExitError{Code: 1, Silent: true}
	}
	return nil
}

func writeSecretCheckLine(stdout *os.File, res secret.CheckResult) {
	if !res.OK {
		fmt.Fprintf(stdout, "%s\tFAILED\t%s\n", res.Name, res.Error)
		return
	}
	line := fmt.Sprintf("%s\tok\tsource=%s\tbytes=%d", res.Name, res.Source, res.Bytes)
	if res.Warning != "" {
		line += "\twarning: " + res.Warning
	}
	fmt.Fprintln(stdout, line)
}

func printSecretHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent secret

Resolve and validate secret references. microagent is a secret conduit, not a
store: plaintext schemes are warned and never written to disk; the secure path
resolves from an external manager.

Commands:
  check NAME=<scheme>:<ref> [...]   Validate that references resolve

Schemes:
  env:VAR                   Value from the CLI's environment (plaintext, warned)
  file:PATH                 File contents (plaintext, warned)
  dotenv:PATH#KEY           KEY from a dotenv file (plaintext, warned)
  vault:<mount>/data/<path>#<field>
                            HashiCorp Vault KV v2 (VAULT_ADDR / VAULT_TOKEN)

check reports ok, source, and byte length and never prints the secret value.
Use the global JSON flag before the subcommand for machine output:
microagent --json secret check NAME=<scheme>:<ref>.
`)
}
