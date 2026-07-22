package main

import (
	"context"
	"fmt"
	"os"
)

type commandSpec struct {
	Name         string
	Aliases      []string
	Group        string
	Summary      string
	Curated      bool // shown in default `microagent help`
	TrailingArgs bool // guest payload follows first positional; stop global-flag extraction there
	Hidden       bool
	HiddenReason string
	NoDocs       bool // exempt from docs-parity (help, version)
	Run          func(ctx context.Context, args []string, stdout *os.File) error
}

func lifecycleRun(command string) func(context.Context, []string, *os.File) error {
	return func(ctx context.Context, args []string, stdout *os.File) error {
		if wantsHelp(args) || hasWorkspaceStateTarget(args) {
			return runWorkspaceStateCommand(ctx, command, args, stdout)
		}
		return runLowLevelRequest(ctx, command, args, stdout)
	}
}

var commandRegistry = []commandSpec{
	{Name: "init", Group: "Getting started", Summary: "Scaffold a starter agent project", Curated: true,
		Run: func(ctx context.Context, a []string, w *os.File) error { return runInit(a, w) }},
	{Name: "doctor", Group: "Getting started", Summary: "Check whether this host can run microVMs", Curated: true, Run: runDoctor},

	{Name: "run", Group: "Run", Summary: "Run something once and discard state", Curated: true, TrailingArgs: true, Run: runWorkspace},
	{Name: "dispatch", Group: "Run", Summary: "Run one task in an isolated workspace; return result + egress audit", Curated: true, TrailingArgs: true, Run: runDispatch},
	{Name: "create", Group: "Run", Summary: "Create a persistent workspace", Curated: true,
		Run: func(ctx context.Context, a []string, w *os.File) error {
			if wantsHelp(a) {
				printCreateHelp(w)
				return nil
			}
			if hasFlagValue(a, "from-snapshot") {
				return runCreateFromSnapshot(ctx, a, w)
			}
			if shouldUseHighLevelCreate(a) {
				return runHighLevelCreate(ctx, a, w)
			}
			return runLowLevelRequest(ctx, "create", a, w)
		}},
	{Name: "start", Group: "Run", Summary: "Boot a workspace", Curated: true,
		Run: func(ctx context.Context, a []string, w *os.File) error {
			if wantsHelp(a) || hasPositionalWorkspaceName(a) {
				return runStartWorkspace(ctx, a, w)
			}
			return runLowLevelRequest(ctx, "start", a, w)
		}},
	{Name: "exec", Group: "Run", Summary: "Run a structured command in a workspace", Curated: true, TrailingArgs: true,
		Run: func(ctx context.Context, a []string, w *os.File) error {
			return runStructuredExec(ctx, a, w, os.Stderr)
		}},
	{Name: "connect", Group: "Run", Summary: "Open the workspace console", Curated: true, Run: runConnect},
	{Name: "apply", Group: "Run", Summary: "Apply supported workspace spec changes", Run: runApply},
	{Name: "supervise", Group: "Run", Summary: "Run host restart supervision for a workspace", Run: runSupervise},
	{Name: "compose", Group: "Run", Hidden: true, HiddenReason: "teaching stub: rejects with guidance", NoDocs: true,
		Summary: "Not supported; explains why",
		Run: func(ctx context.Context, a []string, w *os.File) error {
			return fmt.Errorf("compose-style multi-workspace projects are not supported; run one MicroAgent workspace at a time and keep orchestration outside microagent")
		}},

	{Name: "status", Aliases: []string{"inspect"}, Group: "Lifecycle", Summary: "Show one workspace", Curated: true, Run: lifecycleRun("status")},
	{Name: "wait", Group: "Lifecycle", Summary: "Block until a workspace's run finishes", Curated: true, Run: runWaitWorkspace},
	{Name: "halt", Group: "Lifecycle", Summary: "Shut down cleanly and keep disk state", Curated: true, Run: lifecycleRun("halt")},
	{Name: "stop", Group: "Lifecycle", Summary: "Ask a workspace to shut down gracefully", Run: lifecycleRun("stop")},
	{Name: "kill", Group: "Lifecycle", Summary: "Force stop a workspace", Run: lifecycleRun("kill")},
	{Name: "pause", Group: "Lifecycle", Summary: "Freeze vCPUs, keep memory and disk", Run: lifecycleRun("pause")},
	{Name: "resume", Group: "Lifecycle", Summary: "Resume a paused workspace", Run: lifecycleRun("resume")},
	{Name: "quarantine", Group: "Lifecycle", Summary: "Sever host-side network and mediation", Run: lifecycleRun("quarantine")},
	{Name: "delete", Aliases: []string{"rm"}, Group: "Lifecycle", Summary: "Delete a workspace", Curated: true, Run: lifecycleRun("delete")},

	{Name: "list", Aliases: []string{"ls"}, Group: "Observe", Summary: "List saved workspaces", Curated: true, Run: runList},
	{Name: "ps", Group: "Observe", Summary: "List running workspaces", Curated: true, Run: runPS},
	{Name: "logs", Aliases: []string{"log"}, Group: "Observe", Summary: "Show workspace logs", Curated: true, Run: runLogs},
	{Name: "events", Group: "Observe", Summary: "Show or stream the lifecycle event history", Run: runEvents},
	{Name: "egress", Group: "Observe", Summary: "Show or stream the egress mediator's audit decisions", Run: runEgress},
	{Name: "stats", Group: "Observe", Summary: "Show or stream workspace resource usage", Run: runStats},
	{Name: "result", Group: "Observe", Summary: "Show structured workspace result",
		Run: func(ctx context.Context, a []string, w *os.File) error {
			return runWorkspaceStateCommand(ctx, "result", a, w)
		}},

	{Name: "cp", Group: "Data", Summary: "Copy files into or out of a stopped workspace", Run: runCP},
	{Name: "artifact", Group: "Data", Summary: "List or retrieve declared workspace artifacts", Run: runArtifact},
	{Name: "snapshot", Group: "Data", Summary: "Create, list, or remove workspace snapshots", Run: runSnapshot},
	{Name: "clone", Group: "Data", Summary: "Clone a stopped workspace",
		Run: func(ctx context.Context, a []string, w *os.File) error { return runClone(a, w) }},
	{Name: "commit", Group: "Data", Summary: "Snapshot a stopped workspace rootfs into an OCI image", Run: runCommit},

	{Name: "image", Group: "Resources", Summary: "Manage reusable rootfs baselines",
		Run: func(ctx context.Context, a []string, w *os.File) error { return runImage(a, w) }},
	{Name: "volume", Group: "Resources", Summary: "Manage named ext4 volumes", Run: runVolume},
	{Name: "network", Group: "Resources", Summary: "Show workspace networking or manage named networks",
		Run: func(ctx context.Context, a []string, w *os.File) error { return runNetwork(a, w) }},
	{Name: "model", Group: "Resources", Summary: "Manage local GGUF models and runners",
		Run: func(ctx context.Context, a []string, w *os.File) error { return runModel(a, w) }},
	{Name: "secret", Group: "Resources", Summary: "Validate secret references", Run: runSecret},
	{Name: "registry", Group: "Resources", Summary: "Store credentials for private OCI registries",
		Run: func(ctx context.Context, a []string, w *os.File) error { return runRegistry(a, w) }},
	{Name: "rootfs", Group: "Resources", Summary: "Build a rootfs from an OCI image", Run: runRootFS},
	{Name: "kernel", Group: "Resources", Summary: "Install or verify guest kernels", Run: runKernel},
	{Name: "profiles", Group: "Resources", Summary: "List resource profiles",
		Run: func(ctx context.Context, a []string, w *os.File) error { return runProfiles(a, w) }},

	{Name: "serve", Group: "Agents", Summary: "Expose microagent tools to AI clients over MCP", Curated: true, Run: runServe},

	{Name: "host", Group: "Host", Summary: "Report host capabilities", Run: runHost},
	{Name: "contract", Group: "Host", Summary: "Show backend-neutral runtime contract",
		Run: func(ctx context.Context, a []string, w *os.File) error { return runContract(a, w) }},
	{Name: "perf", Group: "Host", Summary: "Measure workspace performance", Run: runPerf},

	{Name: "gc", Group: "Maintenance", Summary: "Reap dead VM processes and stale state", Run: runGC},
}

func lookupCommand(name string) (*commandSpec, bool) {
	for i := range commandRegistry {
		spec := &commandRegistry[i]
		if spec.Name == name {
			return spec, true
		}
		for _, alias := range spec.Aliases {
			if alias == name {
				return spec, true
			}
		}
	}
	return nil, false
}

// runLowLevelRequest is a temporary stub so the package compiles before Task 2
// extracts the real tail of run() into this function. Task 2 replaces this
// stub with the real extraction.
func runLowLevelRequest(ctx context.Context, command string, args []string, stdout *os.File) error {
	return fmt.Errorf("runLowLevelRequest: wired in Task 2")
}
