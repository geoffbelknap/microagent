package workspace

import (
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// dryRunResult reports what a real call would prepare, without creating
// anything. Callers reach it by setting Options.DryRun and calling Create or
// Run as usual.
//
// This lives on the library path rather than in an adapter because DryRun is an
// Options field three adapters can set: the CLI's create and run, and MCP. Each
// adapter used to implement the flag itself, and run never got a copy — so
// `run --dry-run` parsed the flag, dropped it, and booted a real VM. One
// implementation here cannot diverge that way, and a fourth caller inherits it.
//
// The kernel path is resolved the same pure way EnsureKernel would, but never
// installed: a dry run reports the path a real call would use without fetching
// anything.
func dryRunResult(opts Options) Result {
	kernelPath := strings.TrimSpace(opts.KernelPath)
	if kernelPath == "" {
		kernelPath = KernelPath(opts.Backend, opts.Architecture)
	}
	return Result{
		Workspace:    opts.Name,
		StateDir:     opts.StateDir,
		Profile:      opts.Profile,
		Restart:      opts.RestartPolicy,
		Resources:    ResourcesFromOptions(opts),
		Network:      NetworkSpecFromConfig(opts.Network),
		Service:      opts.ServiceCommand,
		ConsoleShell: opts.ConsoleShell,
		Hostname:     opts.Hostname,
		Disks:        opts.Disks,
		Artifacts:    ArtifactsFromOptions(opts),
		KernelPath:   kernelPath,
		// Report the command a real call would run. Validating a configuration
		// without showing what it will execute leaves the most important field
		// unchecked, which matters most when several inputs can set it.
		GuestCommand:          dryRunGuestCommand(opts),
		CapabilityComposition: EvaluateCapabilityComposition(opts),
		Response: vmkit.Response{
			OK:      true,
			Backend: opts.Backend,
			Event: &vmkit.Event{
				Identity: vmkit.Identity{
					RequestID: NewRequestID(),
					RuntimeID: opts.Name,
					Role:      vmkit.RoleWorkload,
					Backend:   opts.Backend,
				},
				State:      vmkit.StatePrepared,
				Detail:     "dry run validated workspace config",
				ObservedAt: time.Now().UTC(),
			},
		},
	}
}

// dryRunGuestCommand renders the command a workspace would run, for reporting.
// UseImageCommand has no text of its own — the command comes from the image's
// Entrypoint/Cmd, which is only known after the image is pulled — so it is
// named rather than resolved.
func dryRunGuestCommand(opts Options) string {
	if opts.UseImageCommand {
		return "(image entrypoint/cmd)"
	}
	if command := strings.TrimSpace(Command(opts)); command != "" {
		return command
	}
	return strings.TrimSpace(opts.Entrypoint)
}

// validateGuestCommandInputs rejects two ways of saying what a workspace runs
// that cannot both be honored.
//
// UseImageCommand means "run what the image declares". Combined with an exec or
// service command the rootfs build receives conflicting instructions — service
// mode from the image command, plus a Command derived from the other input —
// and which one wins is not a documented contract. Fail closed instead of
// resolving it silently.
//
// Deliberately narrow. A service command composes with setup and exec commands
// on purpose (a setup/exec boot, then the managed service), and setup commands
// compose with an exec command, so neither pair is a conflict.
func validateGuestCommandInputs(opts Options) error {
	if !opts.UseImageCommand {
		return nil
	}
	if strings.TrimSpace(opts.ServiceCommand) != "" {
		return operation.New(operation.ErrorConflict,
			"cannot combine the image command with a service command: choose one way to say what the workspace runs")
	}
	if strings.TrimSpace(opts.ExecCommand) != "" {
		return operation.New(operation.ErrorConflict,
			"cannot combine the image command with an exec command: choose one way to say what the workspace runs")
	}
	return nil
}
