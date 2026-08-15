package main

import (
	"context"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/scaffold"
)

func runInit(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printInitHelp(stdout)
		return nil
	}
	fs := newCommandFlagSet("init")
	provider := fs.String("provider", string(scaffold.DefaultProvider), "Body provider: anthropic, openai, or gemini")
	dir := fs.String("dir", "", "Target directory (defaults to ./<name>)")
	force := fs.Bool("force", false, "Overwrite existing files")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: microagent init <name> [--provider anthropic|openai|gemini] [--dir <path>] [--force]")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("unexpected init argument: %s", fs.Arg(1))
	}
	result, err := scaffold.Generate(scaffold.Options{
		Name:     fs.Arg(0),
		Dir:      *dir,
		Provider: scaffold.Provider(*provider),
		Force:    *force,
	})
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Scaffolded %s agent %q in %s\n", result.Provider, result.Name, result.Dir)
	for _, f := range result.Files {
		fmt.Fprintf(stdout, "  %s\n", f)
	}
	fmt.Fprintf(stdout, "\nNext:\n")
	fmt.Fprintf(stdout, "  cd %s\n", result.Dir)
	fmt.Fprintf(stdout, "  microagent create --file microagent.yaml --env %s=$%s\n", result.APIKey, result.APIKey)
	fmt.Fprintf(stdout, "  microagent cp demo/input-001.json %s:/workspace/input.json\n", result.Name)
	fmt.Fprintf(stdout, "  microagent start %s\n", result.Name)
	return nil
}

func runCommit(ctx context.Context, args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printCommitHelp(stdout)
		return nil
	}
	stateDir := defaultStateDir()
	backend := hostBackend()
	debugfsPath := defaultDebugFSPath()
	arch := defaultGuestArch()
	fs := newCommandFlagSet("commit")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&backend, "backend", backend, "Backend identity override")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	fs.StringVar(&arch, "arch", arch, "OCI image architecture")
	push := fs.Bool("push", false, "Push the committed image to its registry after committing")
	allowRegistryShadow := fs.Bool("allow-registry-shadow", false, "Allow the local commit target to shadow a registry image reference")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: microagent commit <workspace> <image-ref> [--push] [--allow-registry-shadow] [--arch <arch>] [--debugfs <path>] [--state-dir <dir>]")
	}
	if err := validateWorkspaceName(fs.Arg(0)); err != nil {
		return err
	}
	result, err := commit.Commit(ctx, commit.Options{
		StateDir:            stateDir,
		DebugFSPath:         debugfsPath,
		Workspace:           fs.Arg(0),
		Backend:             backend,
		Reference:           fs.Arg(1),
		AllowRegistryShadow: *allowRegistryShadow,
		Architecture:        arch,
	})
	if err != nil {
		return err
	}
	pushed := false
	if *push {
		progress, finishProgress := commandProgressFor(stdout, "commit-push", "Push committed image")
		err := commit.PushWithOptions(ctx, commit.PushOptions{StateDir: stateDir, Reference: result.Reference, Progress: progress})
		finishProgress(err)
		if err != nil {
			return err
		}
		pushed = true
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{
			"reference": result.Reference, "digest": result.Digest,
			"size_bytes": result.SizeBytes, "layout_path": result.LayoutPath, "pushed": pushed,
		})
	}
	fmt.Fprintf(stdout, "Committed %s\n  digest: %s\n  layer:  %d bytes\n  layout: %s\n", result.Reference, result.Digest, result.SizeBytes, result.LayoutPath)
	if pushed {
		fmt.Fprintf(stdout, "Pushed %s\n", result.Reference)
	} else {
		fmt.Fprintf(stdout, "Push it with: microagent image push %s\n", result.Reference)
	}
	return nil
}

func printCommitHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent commit

Snapshot a stopped workspace's rootfs into an OCI image, stored in the local
image layout. Closes the OCI->rootfs loop; push it with `+"`microagent image push`"+`.

Usage:
  microagent commit <workspace> <image-ref> [options]

Options:
  --push                Push to the registry immediately after committing
  --allow-registry-shadow  Allow a commit target with registry identity
  --arch <arch>         OCI image architecture (defaults to the guest arch)
  --debugfs <path>      debugfs binary path used to extract the rootfs
  --backend <name>      Backend identity override
  --state-dir <dir>     State directory
`)
}

func printInitHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent init

Scaffold a starter agent: a microagent.yaml spec, a provider-specific
agent, the shared agent protocol, and a runnable demo request. The generated
project is consumed by the normal create/cp/start flow.

Usage:
  microagent init <name> [options]

Options:
  --provider <name>     Model provider: anthropic (default), openai, or gemini
  --dir <path>          Target directory (defaults to ./<name>)
  --force               Overwrite existing files
`)
}
