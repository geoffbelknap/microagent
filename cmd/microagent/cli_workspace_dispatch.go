package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

type workspaceOptions = workspace.Options
type workspaceSpec = workspace.Spec
type networkSpec = workspace.NetworkSpec
type workspaceDisk = workspace.Disk
type workspaceOutput = workspace.Output
type workspaceManifest = workspace.Manifest

type workspaceResult = workspace.Result

type waitResult = workspace.WaitResult

type applyResult = workspace.ApplyResult

type copyResult = workspace.CopyResult

type artifactsResult struct {
	Workspace string                 `json:"workspace"`
	Artifacts vmkit.RuntimeArtifacts `json:"artifacts"`
}

type workspaceNetworkResult = workspace.NetworkStatus

type guestResult = workspace.GuestResult

type workspaceListEntry = workspace.ListEntry

type resourceConfig = workspace.Resources
type resourceProfile = workspace.Profile

type imageRecord = imagecache.Record
type imagePruneResult = imagecache.PruneResult

var resourceProfiles = workspace.Profiles

func runWorkspace(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRunHelp(stdout)
		return nil
	}
	// Pre-scan --model-token before flag parsing: the token drives pull-time
	// auth only and must never land in Options or any persisted state.
	modelToken, _ := flagValue(args, "model-token")

	opts, err := parseWorkspaceOptions("run", stdout, args)
	if err != nil {
		return err
	}
	warnEgressOff(opts.EgressMode)
	if strings.TrimSpace(opts.ExecCommand) == "" && !opts.UseImageCommand {
		return fmt.Errorf("run requires IMAGE [COMMAND...] or --exec")
	}
	if opts.Name == "" {
		opts.Name = workspace.RandomName("run")
	}
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	// Model orchestration: resolve, pull if needed, start runner, wire into opts.
	progress, finishProgress := rootfsProgress(stdout, "run")
	opts.Progress = progress
	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, modelToken)
	if err != nil {
		finishProgress(err)
		return err
	}
	defer releaseModel()

	wireRootfsBaseline(&opts)
	result, err := workspace.Run(ctx, opts)
	commandErr := err
	if commandErr == nil {
		commandErr = guestExitError(result.Result)
	}
	finishProgress(commandErr)
	if encodeErr := writeRunResult(stdout, os.Stderr, result, opts.Keep, err); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		return err
	}
	return commandErr
}

// guestExitError maps a nonzero guest exit code onto the CLI process exit code.
func guestExitError(result *guestResult) error {
	if result == nil || result.ExitCode == 0 {
		return nil
	}
	return cliExitError{Code: result.ExitCode, Silent: true}
}

// runDispatch is `run` for delegated, single-use work: it boots a throwaway
// workspace under the chosen egress guardrails, runs the command, and returns
// the guest result together with a summary of what the workspace reached on the
// network (the mediator-written audit). The workspace is torn down before it
// returns. Mirrors runWorkspace's option parsing; the difference is the
// audit-bearing result and the one-shot teardown in workspace.RunDispatch.
func runDispatch(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printDispatchHelp(stdout)
		return nil
	}
	modelToken, _ := flagValue(args, "model-token")

	opts, err := parseWorkspaceOptions("dispatch", stdout, args)
	if err != nil {
		return err
	}
	warnEgressOff(opts.EgressMode)
	if strings.TrimSpace(opts.ExecCommand) == "" && !opts.UseImageCommand {
		return fmt.Errorf("dispatch requires IMAGE [COMMAND...] or --exec")
	}
	if opts.Name == "" {
		opts.Name = workspace.RandomName("dispatch")
	}
	if err := validateWorkspaceName(opts.Name); err != nil {
		return err
	}
	progress, finishProgress := rootfsProgress(stdout, "dispatch")
	opts.Progress = progress
	releaseModel, err := ensureModelPairing(ctx, &opts, opts.Model, modelToken)
	if err != nil {
		finishProgress(err)
		return err
	}
	defer releaseModel()

	result, err := workspace.RunDispatch(ctx, opts)
	commandErr := err
	if commandErr == nil {
		commandErr = guestExitError(result.Result)
	}
	finishProgress(commandErr)
	if encodeErr := writeDispatchResult(stdout, os.Stderr, result); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		return err
	}
	return commandErr
}

func printDispatchHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent dispatch — run one task in a fresh, isolated, single-use workspace

Usage:
  microagent dispatch IMAGE [COMMAND...] [flags]

Boots a throwaway microVM under the egress guardrails you choose, runs the
command, and returns its result AND a summary of what it reached on the network
— the mediator-written audit, so you can see whether it stayed on-intent — then
tears the workspace down. One-shot: nothing persists.

Core:
  --exec <command>             command to run (alternative to positional COMMAND)

Egress & broker:
  --egress <mode>              broker (default; allow-broad, no CA) | mitm (forge per-SNI, sunsetting) | off
  --egress-allow <host>        allowlisted destination (repeatable)
  --egress-swap-config <path>  inject into the request host-side; upstream responses remain service trust
  --cred-swap PROVIDER[=ref]   inject a built-in provider API key host-side (e.g. anthropic); reference only
  --secret NAME=<ref>          deliver a secret to the guest tmpfs (repeatable); guest holds the
                                real value, unlike --egress-swap-config/--cred-swap above

Output:
  --json                       machine-readable result + audit
  --dry-run                    validate and return the plan without booting

Example:
  microagent dispatch docker.io/library/python:3.12-slim python -c 'print(2+2)'
`)
}
