package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runHostSetupNetworking(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("host setup-networking", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	check := fs.Bool("check", false, "report readiness without changing the host")
	revert := fs.Bool("revert", false, "undo a previous setup-networking")
	assumeYes := fs.Bool("yes", false, "skip the confirmation prompt before re-running under sudo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *check && *revert {
		return fmt.Errorf("--check and --revert are mutually exclusive")
	}

	opts := doctorOptions{Backend: hostBackend(), Arch: defaultGuestArch()}
	opts.SupervisorPath = setupNetworkingSupervisorPath(opts.Backend)

	if *check {
		resp, _ := doctorResponse(context.Background(), opts)
		printNetworkingSection(stdout, resp.Host)
		printTProxyNATCheck(stdout)
		if resp.Host == nil || !resp.Host.PrivilegedNetworkReady {
			return fmt.Errorf("privileged networking is not ready")
		}
		return nil
	}

	if os.Geteuid() != 0 {
		return maybeSelfElevate(*revert, *assumeYes, stdout)
	}

	if *revert {
		if err := revertHostNetworking(opts.SupervisorPath); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "reverted microagent host networking setup")
		return nil
	}

	if err := applyHostNetworking(opts.SupervisorPath); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "host networking enabled: ip_forward persisted and CAP_NET_ADMIN granted to the supervisor")
	fmt.Fprintln(stdout, "egress TPROXY ready: kernel modules loaded and nat-mode routing provisioned")
	fmt.Fprintln(stdout, "run `microagent doctor` to confirm")
	return nil
}

// setupNetworkingSupervisorPath resolves the supervisor binary that the apply and
// revert paths grant CAP_NET_ADMIN to via setcap. defaultSupervisorPath returns an
// empty string for the firecracker backend (its supervisor is normally resolved at
// spawn time, relative to the executable), so host setup-networking previously ran
// setcap against an empty path and failed to set capabilities on a missing file.
// Resolve a concrete firecracker supervisor path here so setcap operates on the
// real binary.
func setupNetworkingSupervisorPath(backend string) string {
	if path := defaultSupervisorPath(backend); path != "" {
		return path
	}
	return workspace.FirecrackerSupervisorPath(workspace.Options{Backend: backend})
}
