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
	"path/filepath"
	"strings"
	"time"

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
	fs := newCommandFlagSet(command)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&backend, "backend", backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	if command == "quarantine" {
		fs.BoolVar(&noCapture, "no-capture", false, "Contain without first capturing evidence (volatile state is lost)")
	}
	if command == "delete" {
		fs.BoolVar(&yes, "yes", false, "Confirm workspace deletion without prompting")
		fs.BoolVar(&yes, "y", false, "Confirm workspace deletion without prompting")
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
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: microagent %s <name> [--state-dir <dir>]", command)
	}
	if fs.NArg() == 1 {
		if name != "" {
			return fmt.Errorf("workspace name specified twice")
		}
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: microagent %s <name> [--state-dir <dir>]", command)
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	req := vmkit.Request{
		Command: mapCLICommand(command),
		Identity: &vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   backend,
		},
		Config: &vmkit.Config{StateDir: opts.StateDir},
	}
	workspaceOpts := workspaceOptions{StateDir: opts.StateDir, Name: name, Backend: backend, SupervisorPath: supervisorPath}
	if command == "status" {
		resp, err := workspace.Status(workspaceOpts)
		if err != nil {
			return err
		}
		// Reconcile a possibly-dead VM (reap if its firecracker is gone) so status
		// reports reality, not a stale "running". Only worth a supervisor round-trip
		// when the recorded state still claims to be live; best-effort otherwise.
		if resp.Event != nil && isLiveRecordedState(resp.Event.State) {
			if _, ierr := workspace.Inspect(ctx, workspaceOpts); ierr == nil {
				if reread, rerr := workspace.Status(workspaceOpts); rerr == nil {
					resp = reread
				}
			}
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
	// Capture the paired model ref before the verb runs (delete removes the
	// manifest); release the workspace's holder only after the verb succeeds.
	var releaseModel func()
	switch command {
	case "halt", "kill", "delete":
		releaseModel = pendingModelRelease(opts.StateDir, name, backend)
	default:
		releaseModel = func() {}
	}
	if command == "delete" {
		resp, err := runDeleteWorkspace(ctx, workspaceOpts, yes, force)
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
	if command == "quarantine" {
		result, qerr := workspace.Quarantine(ctx, workspaceOpts, workspace.QuarantineOptions{SkipCapture: noCapture})
		if qerr != nil && result.Response.Error == "" {
			return qerr
		}
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

func runDeleteWorkspace(ctx context.Context, opts workspaceOptions, yes, force bool) (vmkit.Response, error) {
	state, _, err := workspace.LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return vmkit.Response{}, err
	}
	// Delete is idempotent: an absent workspace deletes to the same stopped
	// response as a present one. Skip the confirmation prompt in that case —
	// there is nothing to lose — but still run the delete so every caller
	// gets the one contract.
	absent := false
	if _, statusErr := workspace.Status(opts); statusErr != nil {
		var notFound workspace.WorkspaceNotFoundError
		if errors.As(statusErr, &notFound) {
			rootDir := filepath.Dir(workspace.WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend))
			if _, statErr := os.Stat(rootDir); os.IsNotExist(statErr) {
				absent = true
			}
		}
	}
	if !yes && !force && !absent {
		prompt := fmt.Sprintf("Delete workspace %s and its disk/state?", opts.Name)
		if state == vmkit.StateRunning || state == vmkit.StateStarting {
			prompt = fmt.Sprintf("Workspace %s is %s. Stop and delete it?", opts.Name, state)
		}
		ok, err := confirmAction(prompt)
		if err != nil {
			return vmkit.Response{}, err
		}
		if !ok {
			return vmkit.Response{}, fmt.Errorf("delete cancelled")
		}
	}
	return workspace.Delete(ctx, opts, workspace.DeleteOptions{Force: force})
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
		return fmt.Errorf("connect ready-timeout must not be negative")
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
