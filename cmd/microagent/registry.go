package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/registryauth"
	"golang.org/x/term"
)

func runRegistry(args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRegistryHelp(stdout)
		return nil
	}
	switch args[0] {
	case "login":
		return runRegistryLogin(args[1:], stdout)
	case "logout":
		return runRegistryLogout(args[1:], stdout)
	case "list", "ls":
		return runRegistryList(args[1:], stdout)
	default:
		return fmt.Errorf("unknown registry command: %s", args[0])
	}
}

func runRegistryLogin(args []string, stdout *os.File) error {
	var username string
	var passwordStdin bool
	fs := flag.NewFlagSet("registry login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&username, "username", "", "Registry username")
	fs.StringVar(&username, "u", "", "Registry username (shorthand)")
	fs.BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin instead of prompting")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent registry login <registry> --username <user> [--password-stdin]")
	}
	registry := fs.Arg(0)
	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("registry login requires --username")
	}
	password, err := readRegistryPassword(passwordStdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("password must not be empty")
	}
	if err := registryauth.Login(registry, username, password); err != nil {
		return err
	}
	// Never echo the credential; report only where it was stored.
	fmt.Fprintf(stdout, "Stored credentials for %s in %s\n", registry, registryauth.AuthFilePath())
	return nil
}

func runRegistryLogout(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("registry logout", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent registry logout <registry>")
	}
	if err := registryauth.Logout(fs.Arg(0)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed credentials for %s\n", fs.Arg(0))
	return nil
}

func runRegistryList(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("registry list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	registries, err := registryauth.List()
	if err != nil {
		return err
	}
	if len(registries) == 0 {
		fmt.Fprintln(stdout, "No registry credentials stored.")
		return nil
	}
	for _, r := range registries {
		fmt.Fprintln(stdout, r)
	}
	return nil
}

// readRegistryPassword reads the password from stdin (when --password-stdin is
// set or stdin is piped) or prompts without echo on an interactive terminal.
// The password is never accepted as a CLI argument, so it cannot leak into the
// process table or shell history.
func readRegistryPassword(fromStdin bool) (string, error) {
	if fromStdin || !stdinIsTerminal() {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

func printRegistryHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent registry

Store credentials for private OCI registries. microagent does not depend on
Docker: credentials are read only from REGISTRY_AUTH_FILE and microagent's own
auth file (~/.microagent/auth.json), and credential helpers are never executed.
Public images always pull anonymously.

Commands:
  login <registry>     Store a username/password for a registry
  logout <registry>    Remove stored credentials for a registry
  list                 List registries with stored credentials

Login options:
  -u, --username <user>   Registry username (required)
  --password-stdin        Read the password from stdin (else prompt, no echo)

Examples:
  echo "$TOKEN" | microagent registry login ghcr.io -u USERNAME --password-stdin
  microagent registry list
  microagent registry logout ghcr.io

Credential sources, in order (all Docker-free):
  1. $REGISTRY_AUTH_FILE        (shared with Podman/Skopeo/Buildah)
  2. ~/.microagent/auth.json    (written by registry login)
  3. anonymous                  (public images)

Docker's ~/.docker/config.json is never read.
`)
}
