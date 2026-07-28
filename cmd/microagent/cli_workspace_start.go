package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/superviseunit"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runStartWorkspace(ctx context.Context, args []string, stdout *os.File) error {
	profileExplicit := hasFlagValue(args, "profile")
	memoryExplicit := hasFlagValue(args, "memory")
	cpusExplicit := hasFlagValue(args, "cpus")
	supervisorExplicit := hasFlagValue(args, "supervisor")
	backend := hostBackend()
	opts := workspaceOptions{
		Backend:        backend,
		Architecture:   defaultGuestArch(),
		Profile:        defaultWorkspaceProfile,
		Network:        vmkit.NetworkConfig{Mode: defaultNetworkMode},
		StateDir:       defaultStateDir(),
		SupervisorPath: defaultSupervisorPath(backend),
		ResultPort:     workspace.DefaultResultPort,
		SerialInput:    backendSupportsConsoleInput(backend),
	}
	if err := applyResourceProfile(&opts, false, false, false); err != nil {
		return err
	}
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	kernelExplicit := hasFlagValue(args, "kernel")
	fs := newCommandFlagSet("start")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	var vsocks multiFlag
	fs.Var(&vsocks, "vsock", "Vsock mapping port=host:port")
	fs.StringVar(&opts.FromSnapshot, "from-snapshot", "", "Restore the workspace in place from this snapshot tag")
	fs.IntVar(&opts.LeaseSeconds, "ttl", opts.LeaseSeconds, "Idle TTL in seconds; the VM is reaped after this long with no exec/connect (activity renews). 0 = permanent (preserves a create-time lease)")
	waitForFinish := fs.Bool("wait", false, "After boot, block until the workspace reaches a terminal state (stopped, halted, failed)")
	waitTimeout := fs.Duration("wait-timeout", 0, "Give up waiting after this long (e.g. 5m); 0 waits forever; implies --wait")
	var startModelRunner workspace.ModelRunnerSpec
	var startModelMediation workspace.ModelMediationSpec
	modelRunnerCommand := ""
	var modelRunnerArgs multiFlag
	var modelRunnerEnv multiFlag
	fs.StringVar(&startModelRunner.Backend, "model-runner", "", "Model runner backend override: llamacpp, vllm, or custom")
	fs.StringVar(&startModelRunner.GPU, "model-gpu", "", "Model runner GPU intent override: off, on, or auto")
	fs.StringVar(&startModelRunner.BackendModel, "model-runner-model", "", "Backend model id override for runners such as vLLM")
	fs.StringVar(&startModelRunner.ServedModel, "model-runner-served-model", "", "OpenAI-compatible served model name override for runners such as vLLM")
	fs.StringVar(&modelRunnerCommand, "model-runner-command", "", "Custom host model runner command template override")
	fs.StringVar(&startModelRunner.Name, "model-runner-name", "", "Custom host model runner name override")
	fs.StringVar(&startModelRunner.HealthPath, "model-runner-health-path", "", "Custom host model runner health probe path override")
	fs.Var(&modelRunnerArgs, "model-runner-arg", "Extra model runner argument override (repeatable)")
	fs.Var(&modelRunnerEnv, "model-runner-env", "Extra model runner environment KEY=VALUE for this invocation (repeatable; not persisted)")
	fs.StringVar(&startModelMediation.Mode, "model-mediation", "", "Model mediation mode override: off, local-allow, or policy")
	fs.StringVar(&startModelMediation.PolicyURL, "model-policy-url", "", "Model mediation external policy endpoint URL override")
	fs.StringVar(&startModelMediation.PolicyFile, "model-policy-file", "", "Model mediation policy JSON file path override")
	fs.StringVar(&startModelMediation.PolicyTimeout, "model-policy-timeout", "", "Model mediation policy timeout override")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	opts.SerialInput = backendSupportsConsoleInput(opts.Backend)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent start <name> [--state-dir <dir>]")
	}
	opts.Name = fs.Arg(0)
	if strings.TrimSpace(modelRunnerCommand) != "" {
		command, err := modelrunner.ParseRunnerCommand(modelRunnerCommand)
		if err != nil {
			return fmt.Errorf("model runner command: %w", err)
		}
		startModelRunner.Command = command
	}
	startModelRunner.Args = append([]string{}, modelRunnerArgs...)
	startModelRunner.Env = append([]string{}, modelRunnerEnv...)
	opts.KernelExplicit = kernelExplicit
	opts.ProfileExplicit = profileExplicit
	opts.SpecMemory = memoryExplicit
	opts.SpecCPU = cpusExplicit
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	listeners, err := parseVsockMappings(vsocks)
	if err != nil {
		return err
	}
	opts.VsockListeners = listeners
	// Re-pair with the manifest's model for this boot (auto-pulls a missing
	// blob, like run). Start is detached, so the release func is intentionally
	// ignored: the holder is dropped by the next lifecycle verb
	// (halt/stop/kill/delete). A manifest read error is tolerated;
	// workspace.Start surfaces it properly.
	if manifest, err := workspace.ReadManifest(opts.StateDir, opts.Name); err == nil {
		var manifestRunner workspace.ModelRunnerSpec
		if manifest.ModelRunner != nil {
			manifestRunner = *manifest.ModelRunner
		}
		var manifestMediation workspace.ModelMediationSpec
		if manifest.ModelMediation != nil {
			manifestMediation = *manifest.ModelMediation
		}
		opts.ModelRunner = mergeModelRunnerSpec(manifestRunner, startModelRunner)
		opts.ModelMediation = mergeModelMediationSpec(manifestMediation, startModelMediation)
		if strings.TrimSpace(manifest.Model) != "" {
			release, err := ensureModelPairing(ctx, &opts, manifest.Model, "")
			if err != nil {
				return err
			}
			_ = release
		}
	}
	result, err := workspace.Start(ctx, opts)
	if err != nil && result.Workspace == "" {
		return err
	}
	waiting := err == nil && (*waitForFinish || *waitTimeout > 0)
	if encodeErr := writeStartResult(stdout, result, err); encodeErr != nil {
		return encodeErr
	}
	if waiting {
		return waitAndReport(ctx, stdout, opts, workspace.WaitOptions{Timeout: *waitTimeout})
	}
	return err
}

// waitAndReport blocks until the workspace reaches a terminal state, writes
// the wait result, and converts an unclean final state (failed, quarantined)
// into a silent nonzero exit so scripts can branch on the exit code alone.
func waitAndReport(ctx context.Context, stdout *os.File, opts workspaceOptions, waitOpts workspace.WaitOptions) error {
	result, err := workspace.Wait(ctx, opts, waitOpts)
	if err != nil {
		return err
	}
	if encodeErr := writeWaitResult(stdout, result); encodeErr != nil {
		return encodeErr
	}
	if !result.OK {
		return cliExitError{Code: 1, Silent: true}
	}
	return nil
}

func runWaitWorkspace(ctx context.Context, args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printWaitHelp(stdout)
		return nil
	}
	backend := hostBackend()
	supervisorExplicit := hasFlagValue(args, "supervisor")
	opts := workspaceOptions{
		StateDir:       defaultStateDir(),
		Backend:        backend,
		SupervisorPath: defaultSupervisorPath(backend),
	}
	fs := newCommandFlagSet("wait")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	timeout := fs.Duration("timeout", 0, "Give up after this long (e.g. 30s, 5m); 0 waits forever")
	interval := fs.Duration("interval", time.Second, "Delay between state checks")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent wait <name> [--timeout <dur>] [--state-dir <dir>]")
	}
	opts.Name = fs.Arg(0)
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	if *timeout < 0 {
		return fmt.Errorf("wait timeout must not be negative")
	}
	if *interval <= 0 {
		return fmt.Errorf("wait interval must be positive")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return waitAndReport(ctx, stdout, opts, workspace.WaitOptions{Timeout: *timeout, Interval: *interval})
}

func printWaitHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent wait

Block until a workspace's run finishes.

Returns when the workspace reaches a terminal state - stopped, halted,
failed, quarantined, or prepared (created but never started) - and reports
it. Exits 0 for a clean finish (stopped, halted, prepared) and 1 for failed
or quarantined, so scripts can follow "microagent start" without polling
"microagent status" in a loop.

Usage:
  microagent wait <name> [--timeout <dur>] [--state-dir <dir>]

Options:
  -timeout <dur>        Give up after this long (e.g. 30s, 5m); 0 waits forever
  -interval <dur>       Delay between state checks (default 1s)
  -state-dir <dir>      State directory
  -backend <name>       Backend identity override
  -supervisor <path>    Override the installed host backend supervisor path
`)
}

func ensureWorkspaceCanStart(stateDir, name string) error {
	state, pid, err := latestWorkspaceStartState(stateDir, name)
	if err != nil {
		return err
	}
	switch state {
	case "", vmkit.StateUnknown, vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed:
		return nil
	case vmkit.StateQuarantined:
		if pid > 0 {
			return fmt.Errorf("workspace %s is quarantined with preserved pid %d; halt, stop, or kill it before start", name, pid)
		}
		return fmt.Errorf("workspace %s is quarantined; halt, stop, or kill it before start", name)
	case vmkit.StateStarting, vmkit.StateRunning:
		return fmt.Errorf("workspace %s is already %s", name, state)
	default:
		return fmt.Errorf("workspace %s cannot start from state %s", name, state)
	}
}

func latestWorkspaceStartState(stateDir, name string) (vmkit.VMState, int, error) {
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: stateDir, Name: name})
	if err == nil {
		return state.Event.State, state.PID, nil
	}
	if !os.IsNotExist(err) {
		return "", 0, err
	}
	event, eventErr := readWorkspaceEvent(workspaceOptions{StateDir: stateDir, Name: name})
	if eventErr == nil {
		return event.State, 0, nil
	}
	if os.IsNotExist(eventErr) {
		return "", 0, nil
	}
	return "", 0, eventErr
}

type superviseOptions = workspace.SuperviseOptions
type superviseResult = workspace.SuperviseResult

func runSupervise(ctx context.Context, args []string, stdout *os.File) error {
	opts := superviseOptions{
		StateDir:     defaultStateDir(),
		Backend:      hostBackend(),
		Architecture: defaultGuestArch(),
		Interval:     time.Second,
	}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.KernelExplicit = hasFlagValue(args, "kernel")
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := newCommandFlagSet("supervise")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	intervalSeconds := fs.Int("interval", int(opts.Interval.Seconds()), "Seconds between state checks")
	fs.IntVar(&opts.MaxRestarts, "max-restarts", 0, "Maximum restarts; 0 means unlimited")
	install := fs.Bool("install", false, "Install a boot unit that supervises the workspace, then exit")
	uninstall := fs.Bool("uninstall", false, "Remove the installed boot unit, then exit")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if !supervisorExplicit {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent supervise <name> [--state-dir <dir>]")
	}
	if *install && *uninstall {
		return fmt.Errorf("supervise: --install and --uninstall are mutually exclusive")
	}
	if *install || *uninstall {
		return runSuperviseUnit(fs.Arg(0), opts.StateDir, *uninstall, stdout)
	}
	if *intervalSeconds <= 0 {
		return fmt.Errorf("supervise interval must be positive")
	}
	if opts.MaxRestarts < 0 {
		return fmt.Errorf("supervise max-restarts must not be negative")
	}
	opts.Interval = time.Duration(*intervalSeconds) * time.Second
	opts.Name = fs.Arg(0)
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	// Re-pair the manifest's model before every supervised boot, like the
	// start handler does for a single boot: a policy-driven restart must come
	// back with a live runner and MICROAGENT_MODEL_URL working, not silently
	// unpaired. The release func is ignored for the same reason as start —
	// the holder is dropped by the next lifecycle verb.
	opts.BeforeStart = func(ctx context.Context, wsOpts *workspaceOptions) error {
		manifest, err := workspace.ReadManifest(opts.StateDir, opts.Name)
		if err != nil || strings.TrimSpace(manifest.Model) == "" {
			return nil
		}
		release, err := ensureModelPairing(ctx, wsOpts, manifest.Model, "")
		if err != nil {
			return err
		}
		_ = release
		return nil
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := workspace.Supervise(ctx, opts)
	if result.Workspace != "" {
		if encodeErr := writeSuperviseResult(stdout, result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

func runSuperviseUnit(name, stateDir string, uninstall bool, stdout *os.File) error {
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if !uninstall {
		if _, err := workspace.ReadManifest(stateDir, name); err != nil {
			return fmt.Errorf("workspace %q not found; create it before installing a boot unit: %w", name, err)
		}
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve microagent executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	unit, err := superviseunit.Build(superviseunit.Options{
		Name:     name,
		ExecPath: execPath,
		StateDir: stateDir,
		Home:     home,
		GOOS:     runtime.GOOS,
	})
	if err != nil {
		return err
	}
	if uninstall {
		if err := superviseunit.Uninstall(unit); err != nil {
			return err
		}
		if outputJSON(stdout) {
			return writeJSON(stdout, map[string]any{"uninstalled": unit.Label, "path": unit.Path})
		}
		fmt.Fprintf(stdout, "Removed boot unit %s (%s)\n", unit.Label, unit.Path)
		return nil
	}
	enableErr, err := superviseunit.Install(unit)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		out := map[string]any{"installed": unit.Label, "path": unit.Path, "enabled": enableErr == nil}
		if enableErr != nil {
			out["enable_error"] = enableErr.Error()
			out["enable_command"] = strings.Join(unit.EnableArgs, " ")
		}
		return writeJSON(stdout, out)
	}
	fmt.Fprintf(stdout, "Installed boot unit %s (%s)\n", unit.Label, unit.Path)
	if enableErr != nil {
		fmt.Fprintf(stdout, "Could not register it automatically: %v\nEnable it manually with:\n  %s\n", enableErr, strings.Join(unit.EnableArgs, " "))
	} else {
		fmt.Fprintf(stdout, "Registered to start %q at boot.\n", name)
	}
	return nil
}

func superviseWorkspace(ctx context.Context, opts superviseOptions) (superviseResult, error) {
	return workspace.Supervise(ctx, opts)
}

func shouldRestartWorkspace(policy string, state vmkit.VMState) bool {
	return workspace.ShouldRestart(policy, state)
}
