package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func runHostSetupNetworking(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("host setup-networking", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	check := fs.Bool("check", false, "report readiness without changing the host")
	revert := fs.Bool("revert", false, "undo a previous setup-networking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *check && *revert {
		return fmt.Errorf("--check and --revert are mutually exclusive")
	}

	opts := doctorOptions{Backend: hostBackend(), Arch: defaultGuestArch()}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)

	if *check {
		resp, _ := doctorResponse(context.Background(), opts)
		printNetworkingSection(stdout, resp.Host)
		if resp.Host == nil || !resp.Host.PrivilegedNetworkReady {
			return fmt.Errorf("privileged networking is not ready")
		}
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("setup requires root: run `sudo microagent host setup-networking`")
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
	fmt.Fprintln(stdout, "run `microagent doctor` to confirm")
	return nil
}
