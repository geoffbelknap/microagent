package main

// commandUsage holds the invocation shapes shown under "Usage:" in command
// help.
//
// Generated help printed no usage line at all, so most commands announced
// their options without ever saying what they take: `delete --help` listed
// --force and --yes but never mentioned it needs a workspace name, and `model
// --help` was a single line covering eight subcommands.
//
// These are the synopses from docs/cli/<command>.md, which is where they were
// already written down and kept current. TestUsageMatchesTheDocsSynopsis pins
// the two together so help and docs cannot drift; the docs page stays the
// place to edit a shape.
//
// Shape, continuation, and description are separate fields rather than one
// padded string, so each renderer owns its own column alignment.
type usageLine struct {
	Shape string
	Cont  []string // wrapped remainder of a shape too long for one line
	Desc  string
}

var commandUsage = map[string][]usageLine{
	"apply": {
		{Shape: "microagent apply --file <path> [--state-dir <dir>]"},
	},
	"artifact": {
		{Shape: "microagent artifact <name> [--state-dir <dir>]", Desc: "List declared artifacts"},
		{Shape: "microagent artifact get <name> <artifact> <target> [--state-dir <dir>] [--debugfs <path>]", Desc: "Retrieve one output artifact"},
	},
	"clone": {
		{Shape: "microagent clone <source> <target> [--state-dir <dir>]"},
	},
	"commit": {
		{Shape: "microagent commit <workspace> <image-ref> [options]"},
	},
	"connect": {
		{Shape: "microagent connect <name> [--send \"<line>\"] [--state-dir <dir>] [--timeout <seconds>] [--ready-timeout <seconds>]"},
	},
	"contract": {
		{Shape: "microagent [--json] contract"},
	},
	"cp": {
		{Shape: "microagent cp <source> <target> [--state-dir <dir>] [--debugfs <path>]"},
	},
	"create": {
		{Shape: "microagent create [--name <name>] [--image <ref>] [flags]"},
		{Shape: "microagent create <name> [--image <ref>] [flags]"},
		{Shape: "microagent create <name> --from-snapshot <workspace>:<tag> [flags]"},
	},
	"delete": {
		{Shape: "microagent delete <name> [<name>...] [--reason <text>] [--yes] [--force] [--state-dir <dir>]"},
	},
	"dispatch": {
		{Shape: "microagent dispatch <image> [command arg...] [flags]"},
		{Shape: "microagent dispatch --image <ref> --exec \"<command>\" [flags]"},
		{Shape: "microagent dispatch --file <agent.yaml> [flags]"},
	},
	"doctor": {
		{Shape: "microagent doctor [--arch <arch>] [--supervisor <path>] [--state-dir <dir>]"},
	},
	"egress": {
		{Shape: "microagent egress <name> [--follow] [--state-dir <dir>]"},
	},
	"events": {
		{Shape: "microagent events <name> [--follow] [--state-dir <dir>]"},
	},
	"exec": {
		{Shape: "microagent exec <workspace> [flags] -- <argv...>"},
	},
	"gc": {
		{Shape: "microagent gc [--state-dir <dir>]"},
	},
	"halt": {
		{Shape: "microagent halt <name> [--reason <text>] [--state-dir <dir>]"},
	},
	"host": {
		{Shape: "microagent host [--arch <arch>] [--supervisor <path>]", Desc: "Report host backend capabilities"},
	},
	"image": {
		{Shape: "microagent image pull <image> [--state-dir <dir>]", Desc: "Pull and record an image"},
		{Shape: "microagent image list [--state-dir <dir>]", Desc: "List local image records"},
		{Shape: "microagent image push <image> [--state-dir <dir>]", Desc: "Push a committed image"},
		{Shape: "microagent image tag <source> <target> [--state-dir <dir>]", Desc: "Tag an image record"},
		{Shape: "microagent image delete <image> [--purge] [--yes] [--state-dir <dir>]", Desc: "Remove an image record"},
		{Shape: "microagent image prune [--purge] [--yes] [--state-dir <dir>]", Desc: "Prune stale image records"},
	},
	"init": {
		{Shape: "microagent init <name> [options]"},
	},
	"kernel": {
		{Shape: "microagent kernel list [--all] [--backend <name>] [--arch <arch>]", Desc: "List available kernels"},
		{Shape: "microagent kernel check [--backend <name>] [--arch <arch>]", Desc: "Check the installed kernel"},
		{Shape: "microagent kernel install [--channel <ch>] [--version <ver>] [--url <url>]", Cont: []string{"[--from <path>] [--sha256 <sum>] [--out <path>]"}, Desc: "Install a kernel"},
		{Shape: "microagent kernel verify [--path <path>] [--sha256 <sum>]", Desc: "Verify a kernel checksum"},
	},
	"kill": {
		{Shape: "microagent kill <name> --reason <text> [--yes] [--state-dir <dir>]"},
	},
	"list": {
		{Shape: "microagent list [--state-dir <dir>]"},
		{Shape: "microagent ls [--state-dir <dir>]"},
	},
	"logs": {
		{Shape: "microagent logs <name> [--follow] [--state-dir <dir>]"},
	},
	"model": {
		{Shape: "microagent model pull <hf-ref> [--token <t>] [--state-dir <dir>]", Desc: "Download a GGUF model"},
		{Shape: "microagent model list [--state-dir <dir>]", Desc: "List stored models"},
		{Shape: "microagent model delete <ref> [--keep-files] [--state-dir <dir>]", Desc: "Remove a model and its blob"},
		{Shape: "microagent model prune [--delete-files] [--state-dir <dir>]", Desc: "Drop records for missing blobs"},
		{Shape: "microagent model serve <hf-ref> [--dedicated] [--runner <llamacpp|vllm|custom>]", Cont: []string{"[--runner-gpu <off|on|auto>] [--runner-model <id>]", "[--runner-served-model <name>] [--runner-command <template>]", "[--runner-name <name>] [--runner-health-path <path>]", "[--runner-arg <arg>] [--runner-env KEY=VALUE]", "[--token <t>] [--state-dir <dir>]"}, Desc: "Serve a model on the host"},
		{Shape: "microagent model stop <hf-ref> [--state-dir <dir>]", Desc: "Stop a model's runners"},
		{Shape: "microagent model runners [--state-dir <dir>]", Desc: "List running model servers"},
		{Shape: "microagent model policy validate <policy.json>", Desc: "Validate a mediation policy file"},
		{Shape: "microagent model policy evaluate <policy.json> [options]", Desc: "Dry-run a policy file against structured request metadata"},
	},
	"network": {
		{Shape: "microagent network <workspace> [--state-dir <dir>]", Desc: "Inspect a workspace's network"},
		{Shape: "microagent network status <workspace> [--state-dir <dir>]", Desc: "Same, spelled out"},
	},
	"pause": {
		{Shape: "microagent pause <name> [--reason <text>] [--state-dir <dir>]"},
	},
	"perf": {
		{Shape: "microagent perf boot [flags]", Desc: "Measure boot time over iterations"},
		{Shape: "microagent perf footprint <name> [flags]", Desc: "Report backend process memory"},
		{Shape: "microagent perf steady <name> [flags]", Desc: "Sample steady-state memory over time"},
	},
	"profiles": {
		{Shape: "microagent profiles"},
	},
	"ps": {
		{Shape: "microagent ps [--state-dir <dir>]"},
	},
	"quarantine": {
		{Shape: "microagent quarantine <name> --reason <text> [--yes] [--no-capture] [--state-dir <dir>]"},
	},
	"registry": {
		{Shape: "microagent registry login <registry> -u <user> [--password-stdin]", Desc: "Store registry credentials"},
		{Shape: "microagent registry logout <registry>", Desc: "Remove stored credentials"},
		{Shape: "microagent registry list", Desc: "List registries with stored credentials"},
	},
	"resize": {
		{Shape: "microagent resize <workspace> --size-mib <n> [options]"},
	},
	"result": {
		{Shape: "microagent result <name> [--state-dir <dir>]"},
	},
	"resume": {
		{Shape: "microagent resume <name> [--reason <text>] [--state-dir <dir>]"},
	},
	"rootfs": {
		{Shape: "microagent rootfs build --image <ref> --out <path> [flags]", Desc: "Build an ext4 rootfs from an OCI image"},
	},
	"run": {
		{Shape: "microagent run --image <ref> --exec \"<command>\" [flags]"},
		{Shape: "microagent run [flags] <image> [command arg...]"},
	},
	"secret": {
		{Shape: "microagent secret check NAME=<scheme>:<ref> [NAME=<scheme>:<ref> ...]", Desc: "Validate secret references"},
		{Shape: "microagent secret audit <workspace> [--state-dir <dir>]", Desc: "Read a workspace's secret-access log"},
	},
	"serve": {
		{Shape: "microagent serve mcp [--state-dir <dir>] [--supervisor <path>]", Desc: "Stdio MCP transport for agent clients"},
	},
	"snapshot": {
		{Shape: "microagent snapshot create <name> [--tag <tag>] [--forensic] [--state-dir <dir>]", Desc: "Checkpoint a running workspace"},
		{Shape: "microagent snapshot list <name> [--state-dir <dir>]", Desc: "List a workspace's snapshots"},
		{Shape: "microagent snapshot delete <name> <tag> [--state-dir <dir>]", Desc: "Remove one snapshot"},
	},
	"start": {
		{Shape: "microagent start <name> [--state-dir <dir>]"},
		{Shape: "microagent start <name> --wait [--wait-timeout <dur>]"},
		{Shape: "microagent start <name> --from-snapshot <tag> [--state-dir <dir>]"},
	},
	"stats": {
		{Shape: "microagent stats <name> [--follow] [--state-dir <dir>]"},
	},
	"status": {
		{Shape: "microagent [--json] status <name> [--state-dir <dir>]"},
		{Shape: "microagent [--json] status --name <name> [--state-dir <dir>]"},
	},
	"supervise": {
		{Shape: "microagent supervise <name> [--state-dir <dir>] [--max-restarts <n>]"},
	},
	"volume": {
		{Shape: "microagent volume create <name> [--size-mib <n>]", Desc: "Create a named volume"},
		{Shape: "microagent volume list", Desc: "List named volumes"},
		{Shape: "microagent volume status <name>", Desc: "Show one volume"},
		{Shape: "microagent volume resize <name> --size-mib <n>", Desc: "Resize a named volume"},
		{Shape: "microagent volume delete <name> [--force]", Desc: "Remove a named volume"},
	},
	"wait": {
		{Shape: "microagent wait <name> [--timeout <dur>] [--state-dir <dir>]"},
	},
}
