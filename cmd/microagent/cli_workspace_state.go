package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runWorkspaceStateCommand(ctx context.Context, command string, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	name := ""
	yes := false
	force := false
	noCapture := false
	reason := ""
	fs := newCommandFlagSet(command)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&backend, "backend", backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	switch command {
	case "halt", "kill", "quarantine", "pause", "resume", "delete":
		fs.StringVar(&reason, "reason", "", "Opaque reason recorded in the lifecycle audit event")
	}
	if command == "quarantine" {
		fs.BoolVar(&noCapture, "no-capture", false, "Freeze and sever without saving a forensic snapshot (volatile evidence is lost)")
	}
	if command == "delete" || command == "kill" || command == "quarantine" {
		description := "Confirm the high-impact lifecycle action without prompting"
		if command == "delete" {
			description = "Confirm workspace deletion without prompting"
		}
		fs.BoolVar(&yes, "yes", false, description)
		fs.BoolVar(&yes, "y", false, description)
	}
	if command == "delete" {
		fs.BoolVar(&force, "force", false, "Kill a running workspace before deleting")
		fs.BoolVar(&force, "f", false, "Kill a running workspace before deleting")
	}
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		supervisorPath = defaultSupervisorPath(backend)
	}
	// delete takes any number of names; every other state verb takes one.
	if fs.NArg() > 1 && command != "delete" {
		return fmt.Errorf("usage: microagent %s <name> [--state-dir <dir>]", command)
	}
	names := fs.Args()
	if len(names) > 0 && name != "" {
		return fmt.Errorf("workspace name specified twice")
	}
	if len(names) == 0 && name != "" {
		names = []string{name}
	}
	if len(names) == 0 {
		if command == "delete" {
			return fmt.Errorf("usage: microagent %s <name> [<name>...] [--state-dir <dir>]", command)
		}
		return fmt.Errorf("usage: microagent %s <name> [--state-dir <dir>]", command)
	}
	for _, n := range names {
		if err := validateWorkspaceName(n); err != nil {
			return err
		}
	}
	name = names[0]
	if command == "kill" || command == "quarantine" {
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("%s requires --reason <text>", command)
		}
		if err := confirmHighImpactLifecycle(opts.StateDir, name, command, noCapture, yes); err != nil {
			return err
		}
	}
	req := vmkit.Request{
		Command: mapCLICommand(command),
		Identity: &vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: name,
			Purpose:   reason,
			Role:      vmkit.RoleWorkload,
			Backend:   backend,
		},
		Config: &vmkit.Config{StateDir: opts.StateDir},
	}
	workspaceOpts := workspaceOptions{StateDir: opts.StateDir, Name: name, Backend: backend, SupervisorPath: supervisorPath, Purpose: reason, Caller: vmkit.CallerAttribution{Channel: "cli", Assurance: "unavailable"}}
	if command == "status" {
		resp, err := workspace.Status(workspaceOpts)
		if err != nil {
			return err
		}
		return writeResponse(stdout, resp)
	}
	if command == "result" {
		resp, err := workspace.ResultStatus(workspaceOpts)
		if err != nil {
			return err
		}
		return writeResultResponse(stdout, resp)
	}
	if command == "delete" {
		return runDeleteWorkspaces(ctx, opts.StateDir, backend, supervisorPath, names, yes, force, reason, stdout)
	}
	// Capture the paired model ref before the verb runs (halt/kill can drop
	// the runner); release the workspace's holder only after the verb
	// succeeds. runDeleteWorkspaces does its own per-name capture.
	var releaseModel func()
	switch command {
	case "halt", "kill":
		releaseModel = pendingModelRelease(opts.StateDir, name, backend)
	default:
		releaseModel = func() {}
	}
	if command == "quarantine" {
		result, qerr := workspace.Quarantine(ctx, workspaceOpts, workspace.QuarantineOptions{SkipCapture: noCapture})
		if encodeErr := writeQuarantineResult(stdout, result); encodeErr != nil {
			return encodeErr
		}
		return qerr
	}
	resp, err := workspace.Control(ctx, workspaceOpts, req.Command)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	if err == nil && resp.OK {
		releaseModel()
	}
	if encodeErr := writeResponse(stdout, resp); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		// Already reported by the response above. See runLowLevelRequest.
		return cliExitError{Code: 1, Silent: true}
	}
	return nil
}

func confirmHighImpactLifecycle(stateDir, name, command string, noCapture, yes bool) error {
	if yes {
		return nil
	}
	state, _, err := workspace.LatestStartState(stateDir, name)
	if err != nil {
		return err
	}
	if state != vmkit.StateRunning && state != vmkit.StateStarting {
		return nil
	}
	prompt := fmt.Sprintf("Force-stop workspace %s and discard its volatile runtime state?", name)
	if command == "quarantine" {
		prompt = fmt.Sprintf("Freeze workspace %s, sever its authority, capture evidence, and stop it into custody?", name)
		if noCapture {
			prompt = fmt.Sprintf("Freeze and sever workspace %s without capturing volatile evidence, permanently accept that evidence loss, then stop it into durable quarantined state and custody?", name)
		}
	}
	ok, err := confirmAction(prompt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s cancelled", command)
	}
	return nil
}

func runDeleteWorkspace(ctx context.Context, opts workspaceOptions, yes, force bool) (workspace.DeleteResult, error) {
	if !yes && !force && !workspace.Absent(opts) {
		state, _, err := workspace.LatestStartState(opts.StateDir, opts.Name)
		if err != nil {
			return workspace.DeleteResult{}, err
		}
		prompt := fmt.Sprintf("Delete workspace %s and its disk/state?", opts.Name)
		if state == vmkit.StateRunning || state == vmkit.StateStarting {
			prompt = fmt.Sprintf("Workspace %s is %s. Stop and delete it?", opts.Name, state)
		}
		ok, err := confirmAction(prompt)
		if err != nil {
			return workspace.DeleteResult{}, err
		}
		if !ok {
			return workspace.DeleteResult{}, fmt.Errorf("delete cancelled")
		}
	}
	return workspace.Delete(ctx, opts, workspace.DeleteOptions{Force: force})
}

// deleteOutcome pairs one delete target with what happened to it, so
// multi-name output can attribute every result. The embedded DeleteResult
// flattens into JSON alongside the name.
type deleteOutcome struct {
	Workspace string `json:"workspace"`
	workspace.DeleteResult
	failed bool
}

// runDeleteWorkspaces deletes each name after one aggregate confirmation.
// A failure on one workspace does not stop the others; the exit code reports
// whether any failed.
func runDeleteWorkspaces(ctx context.Context, stateDir, backend, supervisorPath string, names []string, yes, force bool, reason string, stdout *os.File) error {
	if len(names) == 1 {
		opts := workspaceOptions{StateDir: stateDir, Name: names[0], Backend: backend, SupervisorPath: supervisorPath, Purpose: reason, Caller: vmkit.CallerAttribution{Channel: "cli", Assurance: "unavailable"}}
		releaseModel := pendingModelRelease(stateDir, names[0], backend)
		result, err := runDeleteWorkspace(ctx, opts, yes, force)
		if err != nil && result.Error == "" {
			return err
		}
		if err == nil && result.OK {
			releaseModel()
		}
		if encodeErr := writeDeleteOutcomes(stdout, []deleteOutcome{{Workspace: names[0], DeleteResult: result}}); encodeErr != nil {
			return encodeErr
		}
		if err != nil {
			// Already reported by the response above. See runLowLevelRequest.
			return cliExitError{Code: 1, Silent: true}
		}
		return nil
	}
	if err := confirmDeleteWorkspaces(stateDir, backend, supervisorPath, names, yes, force); err != nil {
		return err
	}
	outcomes := make([]deleteOutcome, 0, len(names))
	failed := false
	for _, n := range names {
		opts := workspaceOptions{StateDir: stateDir, Name: n, Backend: backend, SupervisorPath: supervisorPath, Purpose: reason, Caller: vmkit.CallerAttribution{Channel: "cli", Assurance: "unavailable"}}
		releaseModel := pendingModelRelease(stateDir, n, backend)
		result, err := workspace.Delete(ctx, opts, workspace.DeleteOptions{Force: force})
		if err == nil && result.OK {
			releaseModel()
		}
		if err != nil {
			failed = true
			if result.Error == "" {
				result.Error = err.Error()
			}
		}
		outcomes = append(outcomes, deleteOutcome{Workspace: n, DeleteResult: result, failed: err != nil})
	}
	if encodeErr := writeDeleteOutcomes(stdout, outcomes); encodeErr != nil {
		return encodeErr
	}
	if failed {
		return cliExitError{Code: 1, Silent: true}
	}
	return nil
}

// confirmDeleteWorkspaces asks once for the whole batch, naming the
// consequence: how many workspaces, which ones, and which are running and
// will be stopped first. Absent names need no confirmation — there is
// nothing to lose.
func confirmDeleteWorkspaces(stateDir, backend, supervisorPath string, names []string, yes, force bool) error {
	if yes || force {
		return nil
	}
	existing := []string{}
	live := []string{}
	for _, n := range names {
		opts := workspaceOptions{StateDir: stateDir, Name: n, Backend: backend, SupervisorPath: supervisorPath}
		if workspace.Absent(opts) {
			continue
		}
		existing = append(existing, n)
		state, _, err := workspace.LatestStartState(stateDir, n)
		if err != nil {
			return err
		}
		if state == vmkit.StateRunning || state == vmkit.StateStarting {
			live = append(live, n)
		}
	}
	if len(existing) == 0 {
		return nil
	}
	prompt := fmt.Sprintf("Delete %d workspaces (%s) and their disk/state?", len(existing), strings.Join(existing, ", "))
	switch {
	case len(live) == 1:
		prompt = fmt.Sprintf("Workspace %s is running. Stop it and delete %d workspaces (%s) and their disk/state?", live[0], len(existing), strings.Join(existing, ", "))
	case len(live) > 1:
		prompt = fmt.Sprintf("Workspaces %s are running. Stop them and delete %d workspaces (%s) and their disk/state?", strings.Join(live, ", "), len(existing), strings.Join(existing, ", "))
	}
	ok, err := confirmAction(prompt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("delete cancelled")
	}
	return nil
}

func confirmAction(prompt string) (bool, error) {
	if !stdinIsTerminal() {
		return false, fmt.Errorf("%s pass --yes to confirm", prompt)
	}
	return readConfirmation(prompt)
}

func defaultStdinIsTerminal() bool {
	return fileIsTerminal(os.Stdin)
}

func defaultReadConfirmation(prompt string) (bool, error) {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func runConnect(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := newCommandFlagSet("connect")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	send := fs.String("send", "", "Write text to the console and exit")
	timeoutSeconds := fs.Int("timeout", 5, "Seconds to wait for output after --send")
	readyTimeoutSeconds := fs.Int("ready-timeout", 10, "Seconds to wait for a shell prompt before --send; 0 disables")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent connect <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	workspace.MarkActivity(workspace.Options{StateDir: opts.StateDir, Name: name})
	if *readyTimeoutSeconds < 0 {
		return operation.New(operation.ErrorValidation, "connect ready-timeout must not be negative")
	}
	consoleOpts := workspace.ConsoleOptions{
		StateDir:            opts.StateDir,
		Name:                name,
		ReadyTimeout:        time.Duration(*readyTimeoutSeconds) * time.Second,
		SendTimeout:         time.Duration(*timeoutSeconds) * time.Second,
		RequireCommandReady: strings.TrimSpace(*send) != "",
	}
	if strings.TrimSpace(*send) != "" {
		if outputStructured() {
			var buf bytes.Buffer
			if err := workspace.SendConsoleCommand(ctx, consoleOpts, *send, &buf); err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{
				"workspace": name,
				"output":    buf.String(),
			})
		}
		return workspace.SendConsoleCommand(ctx, consoleOpts, *send, stdout)
	}
	if outputStructured() {
		return fmt.Errorf("microagent connect interactive sessions are not supported with structured JSON output; use --output text for an interactive console, or connect --send for structured output")
	}
	conn, err := workspace.DialConsole(ctx, consoleOpts)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if stdinIsTerminal() {
		restoreTerminal, err := makeRawTerminal(os.Stdin)
		if err != nil {
			return fmt.Errorf("enable raw terminal mode: %w", err)
		}
		defer restoreTerminal()
	}
	type connectResult struct {
		err error
	}
	results := make(chan connectResult, 2)
	go func() {
		_, err := io.Copy(stdout, conn)
		results <- connectResult{err: err}
	}()
	go func() {
		_, err := copyShellInput(conn, os.Stdin)
		results <- connectResult{err: err}
	}()
	result := <-results
	_ = conn.Close()
	if result.err != nil && !errors.Is(result.err, net.ErrClosed) {
		return result.err
	}
	return nil
}

func runCreateFromSnapshot(ctx context.Context, args []string, stdout *os.File) error {
	backend := hostBackend()
	supervisorExplicit := hasFlagValue(args, "supervisor")
	opts := workspaceOptions{
		Backend:        backend,
		Architecture:   defaultGuestArch(),
		StateDir:       defaultStateDir(),
		SupervisorPath: defaultSupervisorPath(backend),
		ResultPort:     workspace.DefaultResultPort,
		SerialInput:    backendSupportsConsoleInput(backend),
	}
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	kernelExplicit := hasFlagValue(args, "kernel")
	fromSnapshot := ""
	fs := newCommandFlagSet("create")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&fromSnapshot, "from-snapshot", "", "Fork from <workspace>:<tag>")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "Validate without writing state")
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
	opts.KernelExplicit = kernelExplicit
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent create <name> --from-snapshot <workspace>:<tag>")
	}
	opts.Name = fs.Arg(0)
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	source, tag, err := parseForkSnapshotRef(fromSnapshot)
	if err != nil {
		return err
	}
	result, err := workspace.CreateFromSnapshot(ctx, opts, source, tag)
	if err != nil && result.Workspace == "" {
		return err
	}
	if encodeErr := writeCreateResult(stdout, result, err); encodeErr != nil {
		return encodeErr
	}
	return err
}

// parseForkSnapshotRef splits a create --from-snapshot value of the form
// <workspace>:<tag> into its parts.
func parseForkSnapshotRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	source, tag, ok := strings.Cut(ref, ":")
	source = strings.TrimSpace(source)
	tag = strings.TrimSpace(tag)
	if !ok || source == "" || tag == "" {
		return "", "", fmt.Errorf("create --from-snapshot requires <workspace>:<tag>, got %q", ref)
	}
	return source, tag, nil
}
