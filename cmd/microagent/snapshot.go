package main

import (
	"context"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runSnapshot(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || wantsHelp(args) {
		fmt.Fprint(stdout, `microagent snapshot — create, list, or remove workspace snapshots

  microagent snapshot create <name> [--tag <tag>] [--forensic] [--state-dir <dir>]
  microagent snapshot list <name> [--state-dir <dir>]
  microagent snapshot delete <name> <tag> [--state-dir <dir>]
`)
		return nil
	}
	switch canonicalSubverb(args[0]) {
	case "create":
		return runSnapshotCreate(ctx, args[1:], stdout)
	case "list":
		return runSnapshotList(args[1:], stdout)
	case "delete":
		return runSnapshotRemove(args[1:], stdout)
	default:
		return fmt.Errorf("unknown snapshot subcommand %q; use create, list, or delete", args[0])
	}
}

func runSnapshotCreate(ctx context.Context, args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	name := ""
	tag := ""
	forensic := false
	fs := newCommandFlagSet("snapshot create")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&backend, "backend", backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	fs.StringVar(&tag, "tag", "", "Snapshot tag (defaults to a timestamp)")
	fs.BoolVar(&forensic, "forensic", false, "Capture for investigation: retain guest secrets, not restorable")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if !supervisorExplicit {
		supervisorPath = defaultSupervisorPath(backend)
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: microagent snapshot create <name> [--tag <tag>] [--forensic] [--state-dir <dir>]")
	}
	if fs.NArg() == 1 {
		if name != "" {
			return fmt.Errorf("workspace name specified twice")
		}
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: microagent snapshot create <name> [--tag <tag>] [--forensic] [--state-dir <dir>]")
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	opts := workspaceOptions{StateDir: stateDir, Name: name, Backend: backend, SupervisorPath: supervisorPath}
	snapshot := workspace.Snapshot
	if forensic {
		snapshot = workspace.SnapshotForensic
	}
	manifest, err := snapshot(ctx, opts, tag)
	if err != nil {
		return err
	}
	return writeSnapshotManifestResult(stdout, manifest)
}

func runSnapshotList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	name := ""
	fs := newCommandFlagSet("snapshot list")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: microagent snapshot list <name> [--state-dir <dir>]")
	}
	if fs.NArg() == 1 {
		if name != "" {
			return fmt.Errorf("workspace name specified twice")
		}
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: microagent snapshot list <name> [--state-dir <dir>]")
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	infos, err := workspace.SnapshotList(workspaceOptions{StateDir: stateDir, Name: name})
	if err != nil {
		return err
	}
	return writeSnapshotListResult(stdout, name, infos)
}

func runSnapshotRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	name := ""
	fs := newCommandFlagSet("snapshot rm")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&name, "name", "", "Workspace name")
	fs.StringVar(&name, "id", "", "Workspace ID")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	rest := fs.Args()
	tag := ""
	if name == "" {
		if len(rest) != 2 {
			return fmt.Errorf("usage: microagent snapshot delete <name> <tag> [--state-dir <dir>]")
		}
		name = rest[0]
		tag = rest[1]
	} else {
		if len(rest) != 1 {
			return fmt.Errorf("usage: microagent snapshot delete <name> <tag> [--state-dir <dir>]")
		}
		tag = rest[0]
	}
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if err := workspace.SnapshotRemove(workspaceOptions{StateDir: stateDir, Name: name}, tag); err != nil {
		return err
	}
	return writeSnapshotRemoveResult(stdout, name, tag)
}

// snapshotSecretsLabel describes a snapshot's secret disposition in one word.
// "retained" is the one that matters: that snapshot holds guest credential
// material and will not restore.
func snapshotSecretsLabel(manifest vmkit.SnapshotManifest) string {
	switch {
	case !manifest.SecretsMaterialized:
		return "none"
	case manifest.SecretsPurged:
		return "purged"
	default:
		return "retained"
	}
}

func writeSnapshotManifestResult(stdout *os.File, manifest vmkit.SnapshotManifest) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, manifest)
	}
	fmt.Fprintf(stdout, "snapshot %s created (%d MiB RAM, %d vCPU) at %s\n", manifest.Tag, manifest.MemoryMiB, manifest.VCPUCount, manifest.CreatedAt)
	// A capture that kept the guest's secrets must say so on the way out: it is
	// a secret-bearing artifact whose custody is the caller's problem from here,
	// and it will not restore. Silence would let it be filed like any snapshot.
	if manifest.SecretsMaterialized && !manifest.SecretsPurged {
		fmt.Fprintf(stdout, "  forensic capture: guest secrets RETAINED, not restorable — store it accordingly\n")
	}
	return nil
}

func writeSnapshotListResult(stdout *os.File, name string, infos []vmkit.SnapshotInfo) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspace": name, "snapshots": infos})
	}
	if len(infos) == 0 {
		fmt.Fprintf(stdout, "no snapshots for %s\n", name)
		return nil
	}
	// SECRETS marks captures that kept the guest's secrets. Without it a
	// secret-bearing, un-restorable forensic capture is indistinguishable from an
	// ordinary snapshot in the listing — and quarantine now produces one by
	// default, so operators see them without having asked for one by name.
	cols := []tableColumn{
		{Header: "TAG", Legacy: 24, Min: 10, Max: 32, Flex: true},
		{Header: "SIZE", Legacy: 12, Min: 6, Max: 12},
		{Header: "CREATED", Legacy: 21, Min: 19, Max: 25},
		{Header: "SECRETS", Legacy: 10, Min: 8, Max: 10},
		{Header: "IMAGE", Legacy: 0, Min: 10},
	}
	rows := make([][]tableCell, len(infos))
	for i, info := range infos {
		rows[i] = []tableCell{
			cell(info.Tag),
			cell(formatBytes(info.SizeBytes)),
			cell(info.CreatedAt),
			cell(snapshotSecretsLabel(info.SnapshotManifest)),
			cell(info.ImageRef),
		}
	}
	renderTable(stdout, cols, rows)
	return nil
}

func writeSnapshotRemoveResult(stdout *os.File, name, tag string) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspace": name, "removed": tag})
	}
	fmt.Fprintf(stdout, "removed snapshot %s of %s\n", tag, name)
	return nil
}
