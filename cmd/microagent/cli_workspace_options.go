package main

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

type stateCommandOptions struct {
	StateDir string
}

// splitCommaHosts flattens comma-separated host entries into distinct hosts.
// --egress-allow / --egress-passthrough are repeatable, but operators reasonably
// also write a single comma-separated list; without this, "a,b" is stored as one
// literal host that matches nothing and silently denies both. Hostnames never
// contain commas, so splitting is safe; empty fields are dropped.
func splitCommaHosts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, entry := range in {
		for _, h := range strings.Split(entry, ",") {
			if h = strings.TrimSpace(h); h != "" {
				out = append(out, h)
			}
		}
	}
	return out
}

func parseWorkspaceOptions(command string, stdout *os.File, args []string) (workspaceOptions, error) {
	kernelExplicit := hasFlagValue(args, "kernel")
	memoryExplicit := hasFlagValue(args, "memory")
	cpusExplicit := hasFlagValue(args, "cpus")
	sizeExplicit := hasFlagValue(args, "size-mib")
	specExplicit := hasFlagValue(args, "file")
	supervisorExplicit := hasFlagValue(args, "supervisor")
	opts := workspaceOptions{
		Backend:       hostBackend(),
		Architecture:  defaultGuestArch(),
		Profile:       defaultWorkspaceProfile,
		RestartPolicy: defaultRestartPolicy,
		Network:       vmkit.NetworkConfig{Mode: defaultNetworkMode},
		Timeout:       2 * time.Minute,
		ResultPort:    1024,
	}
	if err := applyResourceProfile(&opts, false, false, false); err != nil {
		return workspaceOptions{}, err
	}
	opts.StateDir = defaultStateDir()
	opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	opts.Mke2fsPath = defaultMke2fsPath()
	opts.GuestInitPath = defaultGuestInitPath(opts.Architecture)
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	specPath := workspaceSpecPath(command, args)
	if specPath != "" {
		if err := applyWorkspaceSpecFile(&opts, specPath, memoryExplicit, cpusExplicit, sizeExplicit); err != nil {
			return workspaceOptions{}, err
		}
	}
	fs := newCommandFlagSet(command)
	fs.StringVar(&specPath, "file", specPath, "Workspace spec file")
	fs.StringVar(&opts.Name, "name", opts.Name, "Workspace name")
	fs.StringVar(&opts.Name, "id", opts.Name, "Workspace ID")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "OCI image reference")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Shell command to run as guest init")
	fs.StringVar(&opts.ServiceCommand, "service-command", opts.ServiceCommand, "Long-running shell command to run as the VM service")
	fs.StringVar(&opts.Entrypoint, "entrypoint", opts.Entrypoint, "Shell command to run when the workspace starts")
	fs.BoolVar(&opts.UseImageCommand, "image-command", opts.UseImageCommand, "Run the image Entrypoint/Cmd when creating a prepared workspace")
	fs.StringVar(&opts.ConsoleShell, "shell", opts.ConsoleShell, "Interactive console shell path")
	fs.StringVar(&opts.Hostname, "hostname", opts.Hostname, "Guest hostname")
	setupCommands := multiFlag(append([]string{}, opts.SetupCommands...))
	fs.Var(&setupCommands, "setup", "Shell command to run before --exec")
	var setupFiles multiFlag
	fs.Var(&setupFiles, "setup-file", "Shell script file to run before --exec")
	var envVars multiFlag
	fs.Var(&envVars, "env", "Guest environment variable KEY=VALUE")
	fs.Var(&envVars, "e", "Guest environment variable KEY=VALUE")
	var secretFlags multiFlag
	fs.Var(&secretFlags, "secret", "Declare a secret NAME=<scheme>:<ref> (repeatable)")
	var secretsEnvFile string
	fs.StringVar(&secretsEnvFile, "secrets-env-file", "", "Load secrets from a dotenv file (plaintext, re-read each start)")
	var secretOnDemandFlags multiFlag
	fs.Var(&secretOnDemandFlags, "secret-on-demand", "Declare an on-demand secret NAME=<scheme>:<ref> (fetched at runtime, never written to tmpfs; repeatable)")
	var secretsAudit bool
	fs.BoolVar(&secretsAudit, "secrets-audit", false, "Append every secret access to the workspace audit log")
	var egressMode string
	fs.StringVar(&egressMode, "egress", "", "Egress mediation: broker (default; allow-broad, no CA in the guest), mitm (allow-broad, forge per-SNI — sunsetting), off")
	var egressAllow multiFlag
	fs.Var(&egressAllow, "egress-allow", "Allowlisted egress destination host (repeatable)")
	var egressPassthrough multiFlag
	fs.Var(&egressPassthrough, "egress-passthrough", "Allowed egress host that is not TLS-intercepted (repeatable)")
	var egressLockAllowlist bool
	fs.BoolVar(&egressLockAllowlist, "egress-lock-allowlist", false, "Restrict egress to allowlisted destinations only (drop the allow-broad default) in --egress broker or mitm")
	var egressPolicy string
	fs.StringVar(&egressPolicy, "egress-policy", "", "Path to an egress policy file (.yaml/.yml/.json) declaring allow[]/passthrough[]; unioned with --egress-allow/--egress-passthrough (requires --egress broker or mitm)")
	var egressSwapConfig string
	fs.StringVar(&egressSwapConfig, "egress-swap-config", "", "Path to a credential-swap config (YAML); the mediator injects the real credential host-side so the guest never holds it (requires --egress mitm)")
	var credSwap multiFlag
	fs.Var(&credSwap, "cred-swap", "Inject a provider API key host-side for a built-in provider: PROVIDER[=env:NAME|file:PATH|vault:PATH] (e.g. anthropic, openai). The guest never holds the key; reference only, never a literal. Repeatable; requires --egress mitm")
	var brokerUpstream, brokerSecret string
	fs.StringVar(&brokerUpstream, "broker-upstream", "", "Egress broker upstream base URL; requests reach it through the broker with the credential injected host-side")
	fs.StringVar(&brokerSecret, "broker-secret", "", "Egress broker credential NAME=<scheme>:<ref>; held host-side only, the guest sends @secret:NAME references (never the value)")
	var brokerEnv multiFlag
	fs.Var(&brokerEnv, "broker-env", "Guest env var pointed at the broker, KEY[=VALUE] (empty VALUE = broker URL; repeatable)")
	var brokerProxy bool
	fs.BoolVar(&brokerProxy, "broker-proxy", false, "Also set HTTPS_PROXY/HTTP_PROXY in the guest to the broker (CONNECT tunneling)")
	var brokerCapture bool
	fs.BoolVar(&brokerCapture, "broker-capture", false, "Opt in to raw capture of pre-swap broker requests (path, headers with references, bounded body) to an owner-only file; default is the minimized decision stream")
	var brokerCA string
	fs.StringVar(&brokerCA, "broker-ca", "", "PEM bundle path the broker upstream TLS client trusts; empty = system roots")
	var brokerEndpoints multiFlag
	fs.Var(&brokerEndpoints, "broker-endpoint", "Declare one broker endpoint as ;-separated key=value pairs: upstream=<url>;secret=NAME=<scheme>:<ref>;base-url-env=KEY[=VALUE];ca=<path>;proxy;capture (repeatable; cannot combine with --broker-upstream/-secret/-env/-proxy/-capture/-ca)")
	var diskFlags multiFlag
	fs.Var(&diskFlags, "disk", "Attach disk name=path:/mount:ro|rw")
	var bundleFlags multiFlag
	fs.Var(&bundleFlags, "bundle", "Build and attach bundle name=tar:/mount:ro|rw")
	var volumeFlags multiFlag
	fs.Var(&volumeFlags, "volume", "Attach a volume SRC:DST[:ro|rw] (managed volume name, tar bundle, or ext4 image)")
	fs.Var(&volumeFlags, "v", "Attach a volume SRC:DST[:ro|rw] (managed volume name, tar bundle, or ext4 image)")
	var outputFlags multiFlag
	fs.Var(&outputFlags, "output", "Declare output artifact name=/guest/path")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "Backend identity (internal; must match this install)")
	fs.StringVar(&opts.KernelPath, "kernel", opts.KernelPath, "Linux kernel path")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "supervisor path")
	fs.StringVar(&opts.GuestInitPath, "guest-init", opts.GuestInitPath, "Guest init path")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.Architecture, "arch", opts.Architecture, "Guest architecture")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.RestartPolicy, "restart", opts.RestartPolicy, "Restart policy: never, on-failure, or always")
	fs.StringVar(&opts.Network.Mode, "network", opts.Network.Mode, networkModeFlagHelp)
	mediationMapping := ""
	fs.StringVar(&mediationMapping, "mediation", "", "Required mediation vsock mapping port=host:port")
	mediationOptional := false
	fs.BoolVar(&mediationOptional, "mediation-optional", false, "Allow workspace to run if mediation is unavailable")
	var publishFlags multiFlag
	fs.Var(&publishFlags, "publish", "Forward host[:hostPort]:guestPort[/tcp]")
	fs.Var(&publishFlags, "p", "Forward host[:hostPort]:guestPort[/tcp]")
	fs.IntVar(&opts.MemoryMiB, "memory", opts.MemoryMiB, "Memory in MiB")
	fs.IntVar(&opts.CPUCount, "cpus", opts.CPUCount, "CPU count")
	fs.Int64Var(&opts.SizeMiB, "size-mib", opts.SizeMiB, "Rootfs disk size in MiB; without the flag the disk grows to fit the image")
	resultPort := uint(opts.ResultPort)
	fs.UintVar(&resultPort, "result-port", resultPort, "Vsock result port")
	var timeoutSeconds int
	fs.IntVar(&timeoutSeconds, "timeout", int(opts.Timeout.Seconds()), "Run timeout in seconds")
	fs.IntVar(&opts.LeaseSeconds, "ttl", opts.LeaseSeconds, "Idle TTL in seconds; the VM is reaped after this long with no exec/connect (activity renews). 0 = permanent")
	fs.BoolVar(&opts.Keep, "keep", false, "Keep workspace state after run (run discards by default)")
	rm := false
	fs.BoolVar(&rm, "rm", false, "Discard workspace state after run (explicit; this is the default for run)")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "Validate without writing state")
	// --model-token is consumed by callers via flagValue pre-scan (it must never
	// land in Options); register it so the flagset accepts it and shows it in
	// --help output.
	var absorbedModelToken string
	fs.StringVar(&opts.Model, "model", opts.Model, "Pair this workspace with a locally-served model (HuggingFace GGUF ref); injects MICROAGENT_MODEL_URL/OPENAI_BASE_URL")
	fs.StringVar(&absorbedModelToken, "model-token", "", "HuggingFace token for model auto-pull (else HF_TOKEN/HUGGING_FACE_HUB_TOKEN)")
	modelRunnerCommand := ""
	modelRunnerArgs := multiFlag(append([]string{}, opts.ModelRunner.Args...))
	var modelRunnerEnv multiFlag
	fs.StringVar(&opts.ModelRunner.Backend, "model-runner", opts.ModelRunner.Backend, "Model runner backend: llamacpp, vllm, or custom")
	fs.StringVar(&opts.ModelRunner.GPU, "model-gpu", opts.ModelRunner.GPU, "Model runner GPU intent: off, on, or auto")
	fs.StringVar(&opts.ModelRunner.BackendModel, "model-runner-model", opts.ModelRunner.BackendModel, "Backend model id for runners such as vLLM")
	fs.StringVar(&opts.ModelRunner.ServedModel, "model-runner-served-model", opts.ModelRunner.ServedModel, "OpenAI-compatible served model name for runners such as vLLM")
	fs.StringVar(&modelRunnerCommand, "model-runner-command", "", "Custom host model runner command template")
	fs.StringVar(&opts.ModelRunner.Name, "model-runner-name", opts.ModelRunner.Name, "Custom host model runner name for state output")
	fs.StringVar(&opts.ModelRunner.HealthPath, "model-runner-health-path", opts.ModelRunner.HealthPath, "Custom host model runner health probe path")
	fs.Var(&modelRunnerArgs, "model-runner-arg", "Extra model runner argument (repeatable)")
	fs.Var(&modelRunnerEnv, "model-runner-env", "Extra model runner environment KEY=VALUE for this invocation (repeatable; not persisted)")
	fs.StringVar(&opts.ModelMediation.Mode, "model-mediation", opts.ModelMediation.Mode, "Model mediation mode: off, local-allow, or policy")
	fs.StringVar(&opts.ModelMediation.PolicyURL, "model-policy-url", opts.ModelMediation.PolicyURL, "Model mediation external policy endpoint URL")
	fs.StringVar(&opts.ModelMediation.PolicyFile, "model-policy-file", opts.ModelMediation.PolicyFile, "Model mediation policy JSON file path")
	fs.StringVar(&opts.ModelMediation.PolicyTimeout, "model-policy-timeout", opts.ModelMediation.PolicyTimeout, "Model mediation policy timeout")
	if err := rejectUnsupportedContainerCompatibilityFlags(args); err != nil {
		return workspaceOptions{}, err
	}
	// run/dispatch take a verbatim guest command after the IMAGE positional, so
	// their flags must be hoisted only up to that positional — never lifted out
	// of the guest command (e.g. `run alpine grep -e foo`). Other commands keep
	// the whole-args reorder.
	reordered := reorderFlagArgs(args)
	if command == "run" || command == "dispatch" {
		reordered = reorderFlagArgsForRunDispatch(args)
	}
	if err := parseCommandFlags(fs, stdout, reordered); err != nil {
		return workspaceOptions{}, err
	}
	if fs.NArg() != 0 {
		if command == "create" && fs.NArg() == 1 && opts.Name == "" {
			opts.Name = fs.Arg(0)
		} else if command == "run" || command == "dispatch" {
			if err := applyContainerRunArgs(&opts, fs.Args()); err != nil {
				return workspaceOptions{}, err
			}
		} else {
			return workspaceOptions{}, fmt.Errorf("unexpected %s argument: %s", command, fs.Arg(0))
		}
	}
	if err := applyModelRunnerOptionFlags(&opts, modelRunnerCommand, modelRunnerArgs, modelRunnerEnv); err != nil {
		return workspaceOptions{}, err
	}
	if err := applySetupEnvSecretOptionFlags(&opts, setupCommands, setupFiles, envVars, secretFlags, secretsEnvFile, secretOnDemandFlags, secretsAudit); err != nil {
		return workspaceOptions{}, err
	}
	opts.EgressAllowlistLocked = egressLockAllowlist
	if err := applyEgressOptionFlags(&opts, egressMode, egressAllow, egressPassthrough, egressPolicy, egressSwapConfig, credSwap); err != nil {
		return workspaceOptions{}, err
	}
	if err := applyBrokerOptionFlags(&opts, brokerUpstream, brokerSecret, brokerEnv, brokerProxy, brokerCapture, brokerCA, brokerEndpoints); err != nil {
		return workspaceOptions{}, err
	}
	if err := applyStorageOptionFlags(&opts, volumeFlags, diskFlags, bundleFlags, outputFlags); err != nil {
		return workspaceOptions{}, err
	}
	if err := applyNetworkMediationOptionFlags(&opts, publishFlags, mediationMapping, mediationOptional, command); err != nil {
		return workspaceOptions{}, err
	}
	explicit := workspaceOptionExplicitFlags{
		Kernel:     kernelExplicit,
		Memory:     memoryExplicit,
		CPUs:       cpusExplicit,
		Size:       sizeExplicit,
		Spec:       specExplicit,
		Supervisor: supervisorExplicit,
	}
	if err := finalizeWorkspaceOptions(command, &opts, explicit, rm, specPath, resultPort, timeoutSeconds); err != nil {
		return workspaceOptions{}, err
	}
	return opts, nil
}

type workspaceOptionExplicitFlags struct {
	Kernel     bool
	Memory     bool
	CPUs       bool
	Size       bool
	Spec       bool
	Supervisor bool
}

func applyModelRunnerOptionFlags(opts *workspaceOptions, modelRunnerCommand string, modelRunnerArgs, modelRunnerEnv multiFlag) error {
	if strings.TrimSpace(modelRunnerCommand) != "" {
		command, err := modelrunner.ParseRunnerCommand(modelRunnerCommand)
		if err != nil {
			return fmt.Errorf("model runner command: %w", err)
		}
		opts.ModelRunner.Command = command
	}
	opts.ModelRunner.Args = append([]string{}, modelRunnerArgs...)
	opts.ModelRunner.Env = append([]string{}, modelRunnerEnv...)
	return nil
}

func applySetupEnvSecretOptionFlags(opts *workspaceOptions, setupCommands, setupFiles, envVars, secretFlags multiFlag, secretsEnvFile string, secretOnDemandFlags multiFlag, secretsAudit bool) error {
	opts.SetupCommands = append([]string{}, setupCommands...)
	setupFileCommands, err := setupCommandsFromFiles(setupFiles, ".")
	if err != nil {
		return err
	}
	opts.SetupCommands = append(opts.SetupCommands, setupFileCommands...)
	env, err := parseEnvFlags(envVars)
	if err != nil {
		return err
	}
	opts.Env = mergeEnv(opts.Env, env)
	secrets, err := parseSecretFlags(secretFlags)
	if err != nil {
		return err
	}
	opts.Secrets = secrets
	if strings.TrimSpace(secretsEnvFile) != "" {
		opts.SecretEnvFiles = []string{secretsEnvFile}
	}
	onDemand, err := parseSecretFlags(secretOnDemandFlags)
	if err != nil {
		return err
	}
	opts.OnDemandSecrets = onDemand
	opts.SecretsAudit = secretsAudit
	return nil
}

// applyBrokerOptionFlags parses the --broker-* flags into Options.Broker (or,
// with --broker-endpoint, Options.Brokers) via the shared
// workspace.ParseBrokerConfig / ParseBrokerEndpoints, so the CLI and the
// Agentfile agent.broker(s) block validate and build a broker identically. A
// partial declaration fails loudly and a literal secret is rejected at parse
// time, before any state is written (matching --cred-swap). Combining
// --broker-endpoint with any single-broker flag is rejected here for a clear
// CLI message, ahead of the same both-set guard in normalizeEffectiveBrokers.
func applyBrokerOptionFlags(opts *workspaceOptions, brokerUpstream, brokerSecret string, brokerEnv multiFlag, brokerProxy, brokerCapture bool, brokerCA string, brokerEndpoints multiFlag) error {
	if len(brokerEndpoints) > 0 {
		if strings.TrimSpace(brokerUpstream) != "" || strings.TrimSpace(brokerSecret) != "" || len(brokerEnv) != 0 || brokerProxy || brokerCapture || strings.TrimSpace(brokerCA) != "" {
			return fmt.Errorf("--broker-endpoint cannot be combined with --broker-upstream/--broker-secret/--broker-env/--broker-proxy/--broker-capture/--broker-ca; declare each endpoint fully within its --broker-endpoint spec")
		}
		brokers, err := workspace.ParseBrokerEndpoints([]string(brokerEndpoints))
		if err != nil {
			return err
		}
		// A CLI --broker-endpoint declaration wins outright over anything an
		// Agentfile already set (applied earlier, before flags): clear both
		// fields so the two surfaces can never leave a split single/multi
		// state for Request/normalizeEffectiveBrokers or WriteManifest to see.
		opts.Broker = nil
		opts.Brokers = brokers
		return nil
	}
	broker, err := workspace.ParseBrokerConfig(brokerUpstream, brokerSecret, []string(brokerEnv), brokerProxy, brokerCapture, brokerCA)
	if err != nil {
		return err
	}
	if broker != nil {
		// A CLI --broker-upstream/--broker-secret declaration wins outright
		// over any agent.brokers an Agentfile already set; clear it so at
		// most one of Broker/Brokers is ever set after this function.
		opts.Brokers = nil
		opts.Broker = broker
	}
	return nil
}

func applyEgressOptionFlags(opts *workspaceOptions, egressMode string, egressAllow, egressPassthrough multiFlag, egressPolicy, egressSwapConfig string, credSwap multiFlag) error {
	// Mode precedence: an explicit --egress flag wins; otherwise keep any value a
	// workspace spec (Agentfile `agent.egress`) already applied; otherwise default
	//
	if strings.TrimSpace(egressMode) != "" {
		mode, err := parseEgressMode(egressMode)
		if err != nil {
			return err
		}
		opts.EgressMode = mode
	} else if strings.TrimSpace(opts.EgressMode) == "" {
		opts.EgressMode = vmkit.EgressModeBroker
	}
	mode := opts.EgressMode
	// Allow/passthrough are additive: default-deny means flags, a spec, a policy
	// file, and the manifest can only ADD reachability, never remove it, so they
	// combine by union. Seed with the flag hosts unioned with whatever the spec
	// already applied to Options.
	allowHosts := append(splitCommaHosts([]string(egressAllow)), opts.EgressAllow...)
	passthroughHosts := append(splitCommaHosts([]string(egressPassthrough)), opts.EgressPassthrough...)
	if strings.TrimSpace(egressPolicy) != "" {
		// A policy file only enforces against a running mediator; with mediation
		// off there is nothing to apply it to, so reject rather than silently
		// ignore (which would mislead the operator into believing it took effect).
		if mode == vmkit.EgressModeOff {
			return fmt.Errorf("--egress-policy: an egress policy file requires --egress broker or mitm")
		}
		pf, err := egress.LoadPolicyFile(egressPolicy)
		if err != nil {
			return err
		}
		allowHosts = append(allowHosts, pf.Allow...)
		passthroughHosts = append(passthroughHosts, pf.Passthrough...)
	}
	opts.EgressAllow = egress.DedupeHosts(allowHosts)
	opts.EgressPassthrough = egress.DedupeHosts(passthroughHosts)
	if trimmed := strings.TrimSpace(egressSwapConfig); trimmed != "" {
		// Credential swap injects a real secret host-side only on the mitm
		// datapath (broker splices TLS opaquely and never consults the swap
		// table). In any other mode — including the default broker — the swap
		// would silently do nothing, so reject rather than mislead the operator
		// (mirroring --egress-policy).
		if mode != vmkit.EgressModeMITM {
			return fmt.Errorf("--egress-swap-config: credential swap requires --egress mitm")
		}
		opts.EgressSwapConfigPath = trimmed
	}
	// cred-swap from flags unions with any a spec already declared.
	providers, err := parseCredSwapFlags(credSwap)
	if err != nil {
		return err
	}
	opts.CredSwapProviders = append(opts.CredSwapProviders, providers...)
	if len(opts.CredSwapProviders) > 0 && mode != vmkit.EgressModeMITM {
		// cred-swap injection is performed only by the mitm datapath; broker
		// splices TLS opaquely and off runs no mediator, so in any non-mitm mode
		// — including the default broker — the swap would silently do nothing.
		// Fail loud. Checked against the merged set so a spec-sourced cred-swap is
		// caught too.
		return fmt.Errorf("--cred-swap: credential swap requires --egress mitm")
	}
	return nil
}

// parseCredSwapFlags parses repeatable `--cred-swap PROVIDER[=ref]` specs into
// resolved CredSwapProvider entries via the shared workspace parser (same one the
// Agentfile `agent.cred-swap` block uses). It fails fast: an unknown provider or
// a literal (non-reference) key is rejected here, before any file is written or
// audit entry made, so a secret pasted on the command line never gets processed.
func parseCredSwapFlags(credSwap multiFlag) ([]workspace.CredSwapProvider, error) {
	if len(credSwap) == 0 {
		return nil, nil
	}
	providers := make([]workspace.CredSwapProvider, 0, len(credSwap))
	for _, raw := range credSwap {
		provider, ok, err := workspace.ParseCredSwapProvider(raw)
		if err != nil {
			return nil, err
		}
		if ok {
			providers = append(providers, provider)
		}
	}
	return providers, nil
}

// egressOffWarning returns a one-line operator notice when egress mediation is
// off — unrestricted network, no allowlist, no audit, no cred-swap. It is empty
// for broker/mitm. Printed to stderr at launch so turning mediation off is
// never silent (it's effectively yolo mode).
func egressOffWarning(mode string) string {
	if mode != vmkit.EgressModeOff {
		return ""
	}
	return "⚠ egress off: this workspace has unrestricted network access — no mediation, no audit, no cred-swap (yolo mode)."
}

// warnEgressOff prints the egress-off notice to stderr if applicable. Stderr, so
// it never pollutes structured stdout results.
func warnEgressOff(mode string) {
	if w := egressOffWarning(mode); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
}

func applyStorageOptionFlags(opts *workspaceOptions, volumeFlags, diskFlags, bundleFlags, outputFlags multiFlag) error {
	volumes, err := parseWorkspaceVolumes(volumeFlags)
	if err != nil {
		return err
	}
	disks, err := parseWorkspaceDisks(diskFlags, false)
	if err != nil {
		return err
	}
	bundles, err := parseWorkspaceDisks(bundleFlags, true)
	if err != nil {
		return err
	}
	opts.Disks = append(opts.Disks, volumes...)
	opts.Disks = append(opts.Disks, disks...)
	opts.Disks = append(opts.Disks, bundles...)
	outputs, err := parseWorkspaceOutputs(outputFlags)
	if err != nil {
		return err
	}
	opts.Outputs = append(opts.Outputs, outputs...)
	return nil
}

func applyNetworkMediationOptionFlags(opts *workspaceOptions, publishFlags multiFlag, mediationMapping string, mediationOptional bool, command string) error {
	published, err := parsePortForwardMappings(publishFlags)
	if err != nil {
		return err
	}
	opts.Network.PortForwards = append(opts.Network.PortForwards, published...)
	if strings.TrimSpace(mediationMapping) != "" {
		mediation, err := parseMediationMapping(mediationMapping, mediationOptional)
		if err != nil {
			return err
		}
		opts.Mediation = &mediation
	} else if mediationOptional {
		return fmt.Errorf("%s requires --mediation with --mediation-optional", command)
	}
	return nil
}

func finalizeWorkspaceOptions(command string, opts *workspaceOptions, explicit workspaceOptionExplicitFlags, rm bool, specPath string, resultPort uint, timeoutSeconds int) error {
	// Normalize before anything derives paths or image platforms from it, so
	// `--arch aarch64` (uname spelling) means arm64 everywhere.
	opts.Architecture = workspace.NormalizeArch(opts.Architecture)
	opts.ImageRef = strings.TrimSpace(opts.ImageRef)
	if opts.ImageRef == "" {
		if command == "create" {
			opts.ImageRef = defaultWorkspaceImage(opts.Architecture)
		} else {
			return fmt.Errorf("%s requires --image", command)
		}
	}
	if (command == "run" || command == "dispatch") && strings.TrimSpace(opts.ExecCommand) == "" {
		opts.UseImageCommand = true
	}
	if err := validateConsoleShell(opts.ConsoleShell); err != nil {
		return err
	}
	if strings.TrimSpace(opts.Hostname) == "" && strings.TrimSpace(opts.Name) != "" {
		opts.Hostname = workspace.DefaultHostname(opts.Name)
	}
	if err := validateHostname(opts.Hostname); err != nil {
		return err
	}
	if !explicit.Kernel {
		opts.KernelPath = defaultKernelPath(opts.Backend, opts.Architecture)
	}
	if !explicit.Supervisor {
		opts.SupervisorPath = defaultSupervisorPath(opts.Backend)
	}
	opts.KernelExplicit = explicit.Kernel
	opts.SizeExplicit = explicit.Size
	if err := validateRestartPolicy(opts.RestartPolicy); err != nil {
		return err
	}
	opts.RestartPolicy = normalizeRestartPolicy(opts.RestartPolicy)
	opts.Network = normalizeNetworkConfig(opts.Network)
	if err := vmkit.ValidateNetworkConfig(opts.Network); err != nil {
		return err
	}
	if command != "create" && strings.TrimSpace(opts.ServiceCommand) != "" {
		return fmt.Errorf("%s does not support --service-command", command)
	}
	if opts.UseImageCommand && strings.TrimSpace(opts.ServiceCommand) != "" {
		return fmt.Errorf("%s cannot use both --image-command and --service-command", command)
	}
	if (command == "run" || command == "dispatch") && rm && opts.Keep {
		return fmt.Errorf("%s cannot use both --rm and --keep", command)
	}
	if command != "run" && command != "dispatch" && rm {
		return fmt.Errorf("%s does not support --rm", command)
	}
	if rm {
		opts.Keep = false // --rm forces discard, authoritative over any prior Keep setting
	}
	opts.SerialInput = backendSupportsConsoleInput(opts.Backend)
	if explicit.Spec && specPath == "" {
		return fmt.Errorf("%s requires --file path", command)
	}
	if err := applyResourceProfile(opts, explicit.Memory || opts.SpecMemory, explicit.CPUs || opts.SpecCPU, explicit.Size || opts.SpecSize); err != nil {
		return err
	}
	if err := validateResourceConfig(workspaceResources(*opts), true); err != nil {
		return err
	}
	if timeoutSeconds <= 0 {
		return fmt.Errorf("%s timeout must be positive", command)
	}
	if resultPort > uint(^uint32(0)) {
		return fmt.Errorf("%s result port is too large", command)
	}
	opts.ResultPort = uint32(resultPort)
	opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	return nil
}

func applyContainerRunArgs(opts *workspaceOptions, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if strings.TrimSpace(opts.ExecCommand) != "" {
		return fmt.Errorf("run cannot use both --exec and positional command arguments")
	}
	if opts.UseImageCommand {
		return fmt.Errorf("run cannot use both --image-command and positional command arguments")
	}
	commandArgs := args
	if strings.TrimSpace(opts.ImageRef) == "" {
		opts.ImageRef = strings.TrimSpace(args[0])
		commandArgs = args[1:]
	}
	if strings.TrimSpace(opts.ImageRef) == "" {
		return fmt.Errorf("run requires IMAGE [COMMAND...] or --image")
	}
	if len(commandArgs) == 0 {
		opts.UseImageCommand = true
		return nil
	}
	opts.ExecCommand = shellCommandFromArgs(commandArgs)
	return nil
}

func shellCommandFromArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellSingleQuote(arg))
	}
	return "exec " + strings.Join(quoted, " ")
}

func workspaceSpecPath(command string, args []string) string {
	switch command {
	case "create", "run", "dispatch":
	default:
		return ""
	}
	if value, ok := flagValue(args, "file"); ok {
		return value
	}
	// Auto-discover a workspace spec only for create — the durable, declarative
	// path. run/dispatch require an explicit --file so a stray microagent.yaml in
	// the working directory never silently alters a one-shot invocation.
	if command == "create" {
		if _, err := os.Stat("microagent.yaml"); err == nil {
			return "microagent.yaml"
		}
		if _, err := os.Stat("microagent.yml"); err == nil {
			return "microagent.yml"
		}
	}
	return ""
}

func applyWorkspaceSpecFile(opts *workspaceOptions, path string, memoryExplicit, cpusExplicit, sizeExplicit bool) error {
	return workspace.ApplySpecFile(opts, path, workspace.SpecApplyOptions{
		MemoryExplicit: memoryExplicit,
		CPUExplicit:    cpusExplicit,
		SizeExplicit:   sizeExplicit,
	})
}

func readWorkspaceSpec(path string) (workspaceSpec, error) {
	return workspace.ReadSpec(path)
}

func setupCommandsFromFiles(files []string, baseDir string) ([]string, error) {
	return workspace.SetupCommandsFromFiles(files, baseDir)
}

func parseWorkspaceOutputs(values []string) ([]workspaceOutput, error) {
	outputs := make([]workspaceOutput, 0, len(values))
	for _, raw := range values {
		left, path, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("output must be name=/guest/path")
		}
		output := workspaceOutput{Name: strings.TrimSpace(left), Path: strings.TrimSpace(path)}
		if err := validateWorkspaceOutput(output); err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return validateWorkspaceOutputs(outputs)
}

func validateWorkspaceOutputs(outputs []workspaceOutput) ([]workspaceOutput, error) {
	return workspace.ValidateOutputs(outputs)
}

func validateWorkspaceOutput(output workspaceOutput) error {
	if strings.TrimSpace(output.Name) == "" {
		return fmt.Errorf("output name is required")
	}
	if strings.TrimSpace(output.Path) == "" {
		return fmt.Errorf("output %q path is required", output.Name)
	}
	if !strings.HasPrefix(output.Path, "/") {
		return fmt.Errorf("output %q path must be absolute", output.Name)
	}
	return nil
}

func mergeEnv(base, overrides map[string]string) map[string]string {
	return workspace.MergeEnv(base, overrides)
}

func applyResourceProfile(opts *workspaceOptions, memoryExplicit, cpusExplicit, sizeExplicit bool) error {
	return workspace.ApplyProfile(opts, memoryExplicit, cpusExplicit, sizeExplicit)
}

func lookupResourceProfile(name string) (resourceProfile, bool) {
	return workspace.LookupProfile(name)
}

func workspaceResources(opts workspaceOptions) resourceConfig {
	return workspace.ResourcesFromOptions(opts)
}

func validateResourceConfig(resources resourceConfig, requireDisk bool) error {
	return workspace.ValidateResources(resources, requireDisk)
}

func validateRestartPolicy(policy string) error {
	return workspace.ValidateRestartPolicy(policy)
}

func validateConsoleShell(shellPath string) error {
	shellPath = strings.TrimSpace(shellPath)
	if shellPath == "" {
		return nil
	}
	if !path.IsAbs(shellPath) {
		return fmt.Errorf("shell must be an absolute guest path")
	}
	if path.Clean(shellPath) != shellPath {
		return fmt.Errorf("shell must be a clean absolute guest path")
	}
	return nil
}

func validateHostname(hostname string) error {
	if strings.TrimSpace(hostname) == "" {
		return nil
	}
	return workspace.ValidateHostname(strings.TrimSpace(hostname))
}

func normalizeRestartPolicy(policy string) string {
	return workspace.NormalizeRestartPolicy(policy)
}

func canUseImageBaseline(opts workspaceOptions) bool {
	return opts.PrepareForStart &&
		!workspaceHasGuestCommand(opts) &&
		strings.TrimSpace(opts.ConsoleShell) == "" &&
		strings.TrimSpace(opts.Hostname) == "" &&
		len(opts.Files) == 0 &&
		len(opts.Disks) == 0 &&
		len(opts.Env) == 0 &&
		len(opts.Network.PortForwards) == 0
}

func normalizeNetworkConfig(network vmkit.NetworkConfig) vmkit.NetworkConfig {
	return workspace.NormalizeNetworkConfig(network)
}

func networkSpecFromConfig(network vmkit.NetworkConfig) networkSpec {
	return workspace.NetworkSpecFromConfig(network)
}

func workspaceArtifactsFromOptions(opts workspaceOptions) workspaceArtifacts {
	return workspace.ArtifactsFromOptions(opts)
}
