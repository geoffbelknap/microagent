package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
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

// lifecycleRunWithHelp is lifecycleRun for the lifecycle verbs that carry a
// hand-written when-to-use help page. It prints that page on help/--help/-h
// before any flag parsing, then dispatches through the same workspace-state /
// low-level split as lifecycleRun.
func lifecycleRunWithHelp(command string, help func(*os.File)) func(context.Context, []string, *os.File) error {
	return func(ctx context.Context, args []string, stdout *os.File) error {
		if wantsHelp(args) {
			help(stdout)
			return nil
		}
		if hasWorkspaceStateTarget(args) {
			return runWorkspaceStateCommand(ctx, command, args, stdout)
		}
		return runLowLevelRequest(ctx, command, args, stdout)
	}
}

var commandRegistry []commandSpec

// init populates commandRegistry in an ordinary function body rather than a
// package-level var initializer. Some registry entries store Run closures
// that (transitively, through the MCP tool-call path) invoke run(), which
// looks up commandRegistry via lookupCommand. Go's initialization-order
// analysis treats a var's initializer expression as depending on every
// function reachable from it, so assigning the literal here — where normal
// function-body rules apply instead of initializer-dependency analysis —
// avoids a false "initialization cycle for commandRegistry" compile error.
func init() {
	commandRegistry = []commandSpec{
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
		{Name: "halt", Aliases: []string{"stop"}, Group: "Lifecycle", Summary: "Shut down cleanly and keep disk state", Curated: true, Run: lifecycleRunWithHelp("halt", printHaltHelp)},
		{Name: "kill", Group: "Lifecycle", Summary: "Force stop a workspace", Run: lifecycleRunWithHelp("kill", printKillHelp)},
		{Name: "pause", Group: "Lifecycle", Summary: "Freeze vCPUs, keep memory and disk", Run: lifecycleRunWithHelp("pause", printPauseHelp)},
		{Name: "resume", Group: "Lifecycle", Summary: "Resume a paused workspace", Run: lifecycleRunWithHelp("resume", printResumeHelp)},
		{Name: "quarantine", Group: "Lifecycle", Summary: "Freeze, sever, capture, and stop a workspace", Run: lifecycleRunWithHelp("quarantine", printQuarantineHelp)},
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
		{Name: "artifact", Group: "Data", Summary: "List or retrieve declared workspace artifacts", Curated: true, Run: runArtifact},
		{Name: "snapshot", Group: "Data", Summary: "Create, list, or remove workspace snapshots", Run: runSnapshot},
		{Name: "clone", Group: "Data", Summary: "Clone a stopped workspace",
			Run: func(ctx context.Context, a []string, w *os.File) error { return runClone(a, w) }},
		{Name: "commit", Group: "Data", Summary: "Snapshot a stopped workspace rootfs into an OCI image", Run: runCommit},
		{Name: "resize", Group: "Data", Summary: "Grow or shrink a stopped workspace's rootfs disk", Run: runResize},

		{Name: "image", Group: "Resources", Summary: "Manage reusable rootfs baselines", Curated: true,
			Run: func(ctx context.Context, a []string, w *os.File) error { return runImage(a, w) }},
		{Name: "volume", Group: "Resources", Summary: "Manage named ext4 volumes", Curated: true, Run: runVolume},
		{Name: "network", Group: "Resources", Summary: "Show workspace networking or manage named networks", Curated: true,
			Run: func(ctx context.Context, a []string, w *os.File) error { return runNetwork(a, w) }},
		{Name: "model", Group: "Resources", Summary: "Manage local GGUF models and runners", Curated: true,
			Run: func(ctx context.Context, a []string, w *os.File) error { return runModel(a, w) }},
		{Name: "secret", Group: "Resources", Summary: "Validate secret references", Curated: true, Run: runSecret},
		{Name: "registry", Group: "Resources", Summary: "Store credentials for private OCI registries", Curated: true,
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
}

var helpGroupOrder = []string{"Getting started", "Run", "Lifecycle", "Observe", "Data", "Resources", "Agents", "Host", "Maintenance"}

// subverbAliases is the single alias vocabulary for resource subtrees.
var subverbAliases = map[string]string{"ls": "list", "rm": "delete", "log": "logs", "inspect": "status"}

func canonicalSubverb(v string) string {
	if c, ok := subverbAliases[v]; ok {
		return c
	}
	return v
}

func printCommandTable(w io.Writer, curatedOnly bool) {
	for _, group := range helpGroupOrder {
		var lines []string
		for _, spec := range commandRegistry {
			if spec.Group != group || spec.Hidden || (curatedOnly && !spec.Curated) {
				continue
			}
			name := spec.Name
			if len(spec.Aliases) > 0 {
				name += ", " + strings.Join(spec.Aliases, ", ")
			}
			lines = append(lines, fmt.Sprintf("  %-20s %s", name, spec.Summary))
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s:\n%s\n\n", group, strings.Join(lines, "\n"))
	}
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

// runLowLevelRequest builds and dispatches a raw vmkit.Request for the given
// command word and its arguments. It is the low-level supervisor-request path
// that backs every lifecycle verb via lifecycleRun (status, halt, stop, kill,
// pause, resume, quarantine, delete) as well as the create/start lifecycle
// commands' low-level forms, once their high-level shortcuts (help,
// from-snapshot, positional name, workspace-state target, etc.) have been
// ruled out by the caller.
func runLowLevelRequest(ctx context.Context, command string, args []string, stdout *os.File) error {
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := newCommandFlagSet(command)
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	req, err := requestForCommand(command, fs, stdout, reorderFlagArgs(args))
	if err != nil {
		return err
	}
	// Flags parsed cleanly, so an unrecognized flag has already been reported
	// above and the only remaining reason for no identity is that no workspace
	// was named. Say so, rather than letting the request reach the supervisor
	// and surfacing its contract violation — a fabricated workspace called
	// "unknown", the absolute supervisor path, and the internal field name
	// identity.runtimeID, framed as a failed operation.
	if req.Identity == nil || strings.TrimSpace(req.Identity.RuntimeID) == "" {
		return fmt.Errorf("usage: microagent %s <name> [--state-dir <dir>] (or --request-json <path|->)", command)
	}
	if !supervisorExplicit && req.Identity != nil {
		supervisorPath = defaultSupervisorPath(req.Identity.Backend)
	}
	opts, err := workspaceOptionsFromRequest(req, supervisorPath)
	if err != nil {
		return err
	}
	resp, err := dispatchWorkspaceRequest(ctx, opts, req)
	if err != nil {
		if resp.Error == "" {
			return err
		}
	}
	if encodeErr := writeResponse(stdout, resp); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		// The response above already reported this failure — Error: in text
		// output, ok/error in the JSON envelope. Returning err as well printed
		// the same sentence a second time on stderr. Keep the exit code, drop
		// the duplicate.
		return cliExitError{Code: 1, Silent: true}
	}
	return nil
}

// nearestCommandName suggests the closest command for an unknown input,
// computed from the same registry that rejected it — canonical names and
// aliases both. Distance ≤ 2 or a unique 3+ character prefix qualifies;
// "statu" was a one-edit miss on a 45-command surface and the old message
// asked the user to scan the full list to find the character they dropped.
func nearestCommandName(input string) string {
	best, bestDist := "", 3
	consider := func(name string) {
		if d := commandEditDistance(input, name); d < bestDist {
			best, bestDist = name, d
		}
		if len(input) >= 3 && strings.HasPrefix(name, input) && bestDist > 1 {
			best, bestDist = name, 1
		}
	}
	for i := range commandRegistry {
		consider(commandRegistry[i].Name)
		for _, alias := range commandRegistry[i].Aliases {
			consider(alias)
		}
	}
	return best
}

func commandEditDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev = cur
	}
	return prev[len(b)]
}
