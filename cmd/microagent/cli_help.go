package main

import (
	"fmt"
	"os"
)

func printHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent

Usage:
  microagent run IMAGE [COMMAND ARG...]
  microagent create NAME --image IMAGE
  microagent start NAME
  microagent exec NAME -- CMD

`)
	printCommandTable(stdout, true)
	fmt.Fprint(stdout, `More:
  microagent <command> --help
  microagent help all

Global options:
  --output <json|text>  Select output format
  --json               Alias for --output json
  --progress <mode>    Progress display: auto, plain, or off
  --no-color           Disable state-word color in text output
`)
}

func printFullHelp(stdout *os.File) {
	fmt.Fprint(stdout, "microagent\n\n")
	printCommandTable(stdout, false)
	fmt.Fprint(stdout, `More:
  version              Print the version
  help                 Show help; 'help all' lists every command

`)
	fmt.Fprint(stdout, `Options:
  --output <json|text>  Select output format
  --json                Alias for --output json
  --progress <mode>     Progress display: auto, plain, or off
  --no-color            Disable state-word color in text output
  -supervisor <path>    Override the supervisor path
  -request-json <path|->
                         Read request JSON from a file or stdin
  -image <ref>          OCI image
  -image-command        Run the image Entrypoint/Cmd instead of opening a shell
  -service-command <cmd> Long-running command to run as the VM service
  -name <name>          Workspace name
  -id <id>              Workspace ID
  -entrypoint <command> Command to run on start
  -shell <path>         Interactive console shell path
  -hostname <name>      Guest hostname
  -env KEY=VALUE        Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -debugfs <path>       debugfs binary path
  -file <path>          Workspace spec file
  -kernel <path>        Custom kernel path
  -rootfs <path>        Rootfs image path
  -state-dir <dir>      State directory
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
  -network <mode>       Network mode:
                         user (rootless, unprivileged user namespace; default)
                         or isolated (no network)
  -memory <MiB>         Memory in MiB; defaults to 512 for workspaces
  -cpus <n>             CPU count
  -vsock p=host:port    Add a vsock mapping
`)
}

func printRunHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent run

Run a command from an image.

Usage:
  microagent run IMAGE [COMMAND ARG...]
  microagent run --image IMAGE --exec <command>

What runs (one rule, no precedence to memorize):
  Pick ONE way to say what executes — a positional COMMAND, --exec, or
  --image-command (the image's own Entrypoint/Cmd; also the default when
  you pass nothing). Conflicting picks are rejected, not resolved.
  --setup/--setup-file COMPOSE with any of them: setup runs first, in the
  same boot. --shell only sets the console shell for connect; it never
  changes what runs. --entrypoint belongs to create (the command later
  starts boot); run has no later starts and rejects it.

Core:
  -image <ref>          OCI image
  -exec <command>       Shell command to run
  -setup <command>      Shell command to run before --exec
  -setup-file <path>    Shell script file to run before --exec
  -image-command        Run the image Entrypoint/Cmd
  -shell <path>         Interactive console shell path
  -hostname <name>      Guest hostname
  -env KEY=VALUE        Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -file <path>          Workspace spec file
  -name <name>          Workspace name; a readable name is generated when omitted
  -backend <name>       Backend identity override
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -guest-init <path>    Guest init path
  -arch <arch>          Guest architecture
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
  -network <mode>       Network mode:
                         user (rootless, unprivileged user namespace; default)
                         or isolated (no network)
  -mediation p=host:port Required mediation vsock mapping
  -mediation-optional
                        Allow workspace to run if mediation is unavailable
  -memory <MiB>         Memory in MiB; defaults to 512
  -cpus <n>             CPU count
  -size-mib <MiB>       Disk size
  -headroom-mib <MiB>   Writable headroom beyond image content when size is derived
  -result-port <port>   Vsock result port
  -timeout <seconds>    Timeout
  -keep                 Keep state
  -rm                   Explicitly remove state after run
  -dry-run              Validate the config and report the resolved command
                         without building or booting anything
  -mke2fs <path>        mke2fs binary path
  -debugfs <path>       debugfs binary path
  -supervisor <path>    Override the supervisor path

Container-style aliases:
  -e KEY=VALUE          Guest environment variable
  -p host:guest[/tcp]   Publish a TCP port
  -v, -volume SRC:DST[:ro|rw]
                        Attach a safe tar/ext4 volume

Egress & broker:
  -egress <mode>        Egress mediation: broker (default, allow-broad, opaque
                         splice, no CA in guest), mitm (forge per-SNI, sunsetting),
                         or off
  -egress-allow <host>  Allowlisted egress host (repeatable; .suffix matches subdomains)
  -egress-passthrough <host>
                         Allowed egress host that is not TLS-intercepted (repeatable)
  -egress-lock-allowlist
                        In broker/mitm mode, restrict egress to allowlisted destinations only
  -egress-policy <path>  Egress allow/passthrough policy file (.yaml/.yml/.json)
  -egress-swap-config <path>
                         Credential-swap config; request injection is host-side (upstream response remains service trust)
  -cred-swap PROVIDER[=ref]
                         Inject a built-in provider API key host-side (e.g. anthropic, openai); request-side reference only (repeatable)
  -broker-upstream <url>
                         Egress broker upstream; request credentials are injected host-side
  -broker-secret NAME=<scheme>:<ref>
                         Broker credential reference; the guest sends @secret:NAME
  -broker-env KEY[=VALUE]
                         Guest env var pointed at the broker (repeatable)
  -broker-proxy         Set HTTPS_PROXY/HTTP_PROXY in the guest to the broker
  -broker-ca <path>     PEM bundle the broker upstream TLS client trusts; empty = system roots
  -broker-assurance <mode>
                         Required trust contract: semantic or trusted-upstream
  -broker-grant <path>  YAML/JSON semantic grant (required in semantic mode)
  -broker-endpoint <spec>
                         Declare one broker endpoint as ;-separated key=value pairs:
                         upstream=<url>;secret=NAME=<scheme>:<ref>;assurance=<mode>;grant=<path>;...
                         (repeatable; cannot combine with the other -broker-* flags)
  -secret NAME=<scheme>:<ref>
                         Deliver a secret to tmpfs /run/secrets (repeatable); the guest holds the
                         real value — NOT the same protection as -broker-* / -cred-swap above
  -secret-on-demand NAME=<scheme>:<ref>
                         On-demand secret, fetched at runtime, never written to tmpfs; the guest
                         still receives the real value when it fetches it
  -secrets-env-file <path>
                         Deliver every key in a dotenv file as a secret; same guest-holds-it risk as -secret
  -secrets-audit        Append every secret access to the workspace audit log

Model runner:
  -model <ref>          Pair with a locally-served model (HuggingFace GGUF ref);
                         injects MICROAGENT_MODEL_URL and OPENAI_BASE_URL
  -model-token <token>  HuggingFace token for model auto-pull
                         (defaults to HF_TOKEN or HUGGING_FACE_HUB_TOKEN)
  -model-runner <name>  Model runner backend: llamacpp, vllm, or custom
  -model-gpu <mode>     Model runner GPU intent: off, on, or auto
  -model-mediation <mode> Model mediation: off, local-allow, or policy
  -model-policy-file <path> Model mediation policy file

Output:
  -output n=/guest/path Declare an output artifact

Container-style examples:
  microagent run alpine echo hello
  microagent run -e FOO=bar -p 8080:80 alpine
  microagent run -v /tmp/config.tar:/config:ro alpine ls /config
  microagent run -v data:/work alpine ls /work   (attach a managed named volume)

Not implemented:
  container-engine APIs, compose projects, pods, privileged mode, namespace flags, devices, and
  host directory bind mounts are not exposed.
`)
}

func printCreateHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "create")
	printUsageBlock(stdout, "create", "create")
	fmt.Fprint(stdout, `
Create a workspace from an image.

Options:
  -image <ref>          OCI image; defaults to Python 3.13 slim
  -name <name>          Workspace name
  -setup <command>      Shell command to run before first start
  -setup-file <path>    Shell script file to run before first start
  -service-command <cmd> Long-running command to run as the VM service
  -image-command        Run the image Entrypoint/Cmd when creating a prepared workspace
  -entrypoint <command> Command to run on start
  -shell <path>         Interactive console shell path
  -hostname <name>      Guest hostname
  -env KEY=VALUE        Guest environment variable
  -e KEY=VALUE          Guest environment variable
  -disk n=p:/m:ro|rw    Attach an ext4 disk
  -bundle n=p:/m:ro|rw  Build a disk from a tar bundle
  -v, -volume SRC:DST[:ro|rw]
                        Attach a safe tar/ext4 volume
  -output n=/guest/path Declare an output artifact
  -file <path>          Workspace spec file
  -backend <name>       Backend identity override
  -kernel <path>        Custom kernel path
  -state-dir <dir>      State directory
  -guest-init <path>    Guest init path
  -arch <arch>          Guest architecture
  -profile <name>       Resource profile: tiny, small, medium, or large
  -restart <policy>     Restart policy: never, on-failure, or always
  -network <mode>       Network mode:
                         user (rootless, unprivileged user namespace; default)
                         or isolated (no network)
  -p host:guest[/tcp]   Publish a TCP port
  -publish host:guest[/tcp]
                         Publish a TCP port
  -mediation p=host:port Required mediation vsock mapping
  -mediation-optional
                        Allow workspace to run if mediation is unavailable
  -egress <mode>        Egress mediation: broker (default, allow-broad, opaque
                         splice, no CA in guest), mitm (forge per-SNI, sunsetting),
                         or off
  -egress-allow <host>  Allowlisted egress host (repeatable; .suffix matches subdomains)
  -egress-passthrough <host>
                         Allowed egress host that is not TLS-intercepted (repeatable)
  -egress-lock-allowlist
                        In broker/mitm mode, restrict egress to allowlisted destinations only
  -egress-policy <path>  Egress allow/passthrough policy file (.yaml/.yml/.json)
  -egress-swap-config <path>
                         Credential-swap config; request injection is host-side (upstream response remains service trust)
  -cred-swap PROVIDER[=ref]
                         Inject a built-in provider API key host-side (e.g. anthropic, openai); request-side reference only (repeatable)
  -broker-upstream <url>
                         Egress broker upstream; request credentials are injected host-side
  -broker-secret NAME=<scheme>:<ref>
                         Broker credential reference; the guest sends @secret:NAME
  -broker-env KEY[=VALUE]
                         Guest env var pointed at the broker (repeatable)
  -broker-proxy         Set HTTPS_PROXY/HTTP_PROXY in the guest to the broker
  -broker-ca <path>     PEM bundle the broker upstream TLS client trusts; empty = system roots
  -broker-assurance <mode>
                         Required trust contract: semantic or trusted-upstream
  -broker-grant <path>  YAML/JSON semantic grant (required in semantic mode)
  -broker-endpoint <spec>
                         Declare one broker endpoint as ;-separated key=value pairs:
                         upstream=<url>;secret=NAME=<scheme>:<ref>;assurance=<mode>;grant=<path>;...
                         (repeatable; cannot combine with the other -broker-* flags)
  -secret NAME=<scheme>:<ref>
                         Deliver a secret to tmpfs /run/secrets (repeatable); the guest holds the
                         real value — NOT the same protection as -broker-* / -cred-swap above
  -secret-on-demand NAME=<scheme>:<ref>
                         On-demand secret, fetched at runtime, never written to tmpfs; the guest
                         still receives the real value when it fetches it
  -secrets-env-file <path>
                         Deliver every key in a dotenv file as a secret; same guest-holds-it risk as -secret
  -secrets-audit        Append every secret access to the workspace audit log
  -memory <MiB>         Memory in MiB; defaults to 512
  -cpus <n>             CPU count
  -size-mib <MiB>       Disk size
  -headroom-mib <MiB>   Writable headroom beyond image content when size is derived
  -result-port <port>   Vsock result port
  -mke2fs <path>        mke2fs binary path
  -debugfs <path>       debugfs binary path
  -supervisor <path>    Override the supervisor path
  -model <ref>          Pair with a locally-served model (HuggingFace GGUF ref);
                         persisted so every start re-pairs; injects
                         MICROAGENT_MODEL_URL and OPENAI_BASE_URL
  -model-token <token>  HuggingFace token for model auto-pull
                         (defaults to HF_TOKEN or HUGGING_FACE_HUB_TOKEN)
  -model-runner <name>  Model runner backend: llamacpp, vllm, or custom
  -model-gpu <mode>     Model runner GPU intent: off, on, or auto
  -model-mediation <mode> Model mediation: off, local-allow, or policy
  -model-policy-file <path> Model mediation policy file
  -dry-run              Validate without writing state
  -request-json <path|->
                         Read request JSON from a file or stdin
`)
}

func printHaltHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "halt")
	printUsageBlock(stdout, "halt", "halt")
	fmt.Fprint(stdout, `
Park a workspace with a clean, disk-preserving shutdown. halt requests a
graceful exit and records the terminal state as halted: the VM process exits
but the rootfs, attached disks, identity, and event timeline are preserved, so
a later 'microagent start <name>' boots the same disk state. The guest is given
a fixed graceful window (about 15 seconds) to exit; if it does not exit in time,
the workspace is recorded as failed and halt returns an error without escalating -
follow up with 'microagent kill <name>' for a hard termination. 'stop' is an alias of halt and
behaves identically. This is not memory pause/resume; for that see
'microagent pause'.

Options:
  -name <name>          Workspace name; positional name is also accepted
  -id <id>              Workspace ID alias for -name
  -state-dir <dir>      State directory holding the workspace record
  -backend <name>       Backend identity override
  -supervisor <path>    Override the installed host backend supervisor path
`)
}

func printKillHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "kill")
	printUsageBlock(stdout, "kill", "kill")
	fmt.Fprint(stdout, `
Force-terminate a workspace. kill is the hard variant of halt: reach for it
when a graceful 'microagent halt' does not return within its graceful window,
since halt never escalates on its own. The disk state survives kill, but
nothing inside the guest gets a chance to flush or exit cleanly. For a clean
shutdown of a healthy workspace you intend to start again, use halt instead.

Options:
  -name <name>          Workspace name; positional name is also accepted
  -id <id>              Workspace ID alias for -name
  -state-dir <dir>      State directory holding the workspace record
  -backend <name>       Backend identity override
  -supervisor <path>    Override the installed host backend supervisor path
`)
}

func printPauseHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "pause")
	printUsageBlock(stdout, "pause", "pause")
	fmt.Fprint(stdout, `
Freeze a running workspace in place and record its state as paused. The VM's
vCPUs stop executing, but guest memory, the rootfs, attached disks, identity,
and events are all preserved, and the host-side network, port forwarding, and
vsock paths stay in place, so the workspace can be thawed with
'microagent resume'. This is memory pause, not a disk-preserving shutdown:
unlike halt, a paused workspace keeps its live memory state and resumes exactly
where it left off rather than booting again. pause requires the workspace to be
running.

Options:
  -name <name>          Workspace name; positional name is also accepted
  -id <id>              Workspace ID alias for -name
  -state-dir <dir>      State directory holding the workspace record
  -backend <name>       Backend identity override
  -supervisor <path>    Override the installed host backend supervisor path
`)
}

func printResumeHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "resume")
	printUsageBlock(stdout, "resume", "resume")
	fmt.Fprint(stdout, `
Thaw a paused workspace back to running, exactly where it was. The VM's vCPUs
continue executing from the point they were frozen, with guest memory, disk
state, and the host-side network, port forwarding, and vsock paths intact.
resume requires the workspace to be paused; to boot a halted workspace from
disk instead, use 'microagent start'.

Options:
  -name <name>          Workspace name; positional name is also accepted
  -id <id>              Workspace ID alias for -name
  -state-dir <dir>      State directory holding the workspace record
  -backend <name>       Backend identity override
  -supervisor <path>    Override the installed host backend supervisor path
`)
}

func printQuarantineHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "quarantine")
	printUsageBlock(stdout, "quarantine", "quarantine")
	fmt.Fprint(stdout, `
Contain a workspace in one ordered operation: create a durable deny marker,
freeze guest execution, sever network, brokers, published ports, serial input,
and other host authority, capture evidence while frozen, then stop the VM into
custody. The marker blocks ordinary start, resume, restore, mutation, and
deletion even if a phase or state write fails.

The forensic capture retains guest secrets and is not restorable - keep it
somewhere the workloads it came from cannot read. A capture failure never
restores execution or authority: the VM stays frozen and severed so rerunning
quarantine can retry against the same volatile state. Pass --no-capture to omit
the snapshot, including on a retry after capture failure, and explicitly accept
losing volatile evidence before final custody.

Options:
  -name <name>          Workspace name; positional name is also accepted
  -id <id>              Workspace ID alias for -name
  -no-capture           Freeze and sever without saving a forensic snapshot
  -state-dir <dir>      State directory holding the workspace record
  -backend <name>       Backend identity override
  -supervisor <path>    Override the installed host backend supervisor path
`)
}
