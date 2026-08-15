package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

type doctorOptions struct {
	Backend        string
	Arch           string
	SupervisorPath string
	StateDir       string
	Progress       operation.ProgressFunc
}

func runContract(args []string, stdout *os.File) error {
	fs := newCommandFlagSet("contract")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent contract")
	}
	return writeRuntimeContract(stdout, vmkit.NewRuntimeContract())
}

func runDoctor(ctx context.Context, args []string, stdout *os.File) error {
	opts := doctorOptions{
		Backend: hostBackend(),
		Arch:    defaultGuestArch(),
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	opts.StateDir = defaultStateDir()
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := newCommandFlagSet("doctor")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Arch, "arch", opts.Arch, "Guest architecture")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected doctor argument: %s", fs.Arg(0))
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	progress, finishProgress := commandProgressFor(stdout, "doctor", "Check host")
	opts.Progress = progress
	resp, err := doctorResponse(ctx, opts)
	finishProgress(err)
	if encodeErr := writeDoctorResponse(stdout, resp); encodeErr != nil {
		return encodeErr
	}
	if err == nil {
		return nil
	}
	// The rendered page already carries the diagnosis; returning the raw
	// error text printed the same line twice. Exit reflects the verdict:
	// degraded means runs work today, and exiting nonzero for it teaches
	// scripts that a usable host is a broken one.
	verdict := resp.Verdict
	if verdict == "" {
		verdict = diagnostics.DeriveVerdict(&resp)
	}
	if verdict == vmkit.VerdictDegraded {
		return nil
	}
	return cliExitError{Code: 1, Silent: true}
}

func runHost(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("unknown host command: %s", args[0])
	}
	opts := doctorOptions{
		Backend: hostBackend(),
		Arch:    defaultGuestArch(),
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := newCommandFlagSet("host")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Arch, "arch", opts.Arch, "Guest architecture")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected host argument: %s", fs.Arg(0))
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	if err := workspace.ValidateHostBackend(opts.Backend); err != nil {
		return err
	}
	resp, _ := doctorResponse(ctx, opts)
	return writeDoctorResponse(stdout, resp)
}

func doctorResponse(ctx context.Context, opts doctorOptions) (vmkit.Response, error) {
	return diagnostics.Check(ctx, diagnostics.Options{Backend: opts.Backend, Arch: opts.Arch, SupervisorPath: opts.SupervisorPath, StateDir: opts.StateDir, Progress: opts.Progress})
}

func augmentHostSupport(resp *vmkit.Response, opts doctorOptions) {
	diagnostics.AugmentHostSupport(resp, diagnostics.Options{Backend: opts.Backend, Arch: opts.Arch, SupervisorPath: opts.SupervisorPath})
}

func backendSupportsConsoleInput(backend string) bool {
	return workspace.BackendSupportsConsoleInput(backend)
}

func firecrackerDoctorResponse(backend, arch string, resolveBinary func() (string, error), resolveSupervisor func(diagnostics.Options) (string, error), resolveGuestInit func(diagnostics.Options) (string, error), stat func(string) (os.FileInfo, error), binaryVersion func(string) string, lookPath func(string) (string, error), readFile func(string) ([]byte, error), probeUserNamespaces func() error) (vmkit.Response, error) {
	return diagnostics.CheckFirecracker(
		diagnostics.Options{Backend: backend, Arch: arch},
		diagnostics.FirecrackerProbe{ResolveBinary: resolveBinary, ResolveSupervisor: resolveSupervisor, ResolveGuestInit: resolveGuestInit, Stat: stat, BinaryVersion: binaryVersion, LookPath: lookPath, ReadFile: readFile, ProbeUserNamespaces: probeUserNamespaces},
	)
}

func resolveFirecrackerPath() (string, error) {
	return diagnostics.ResolveFirecrackerPath()
}

func defaultFirecrackerPathFromExecutable(executable string) string {
	return diagnostics.DefaultFirecrackerPathFromExecutable(executable)
}

func firstOutputLine(output string) string {
	return diagnostics.FirstOutputLine(output)
}
