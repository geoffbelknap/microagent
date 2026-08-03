package workspace

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"gopkg.in/yaml.v3"
)

const (
	DefaultWorkspaceImageArm64 = "docker.io/library/python@sha256:fe7dfda0f28395abfacca893065882031469eef7269a1bb017e5e59c130edd92"
	DefaultWorkspaceImageAMD64 = "docker.io/library/python@sha256:0ee2df98db454606ca92bb7a79d47ff7dc9cc0c8d5901e32eb71e6b5203377b2"
	DefaultWorkspaceImageOther = "docker.io/library/python:3.13-slim"
	DefaultWorkspaceMemoryMiB  = 512
	DefaultWorkspaceCPUCount   = 2
	DefaultWorkspaceProfile    = "small"
	DefaultRestartPolicy       = "never"
	DefaultNetworkMode         = "user"
	DefaultResultPort          = 1024
	DefaultSecretsPort         = 1026
	DefaultSecretsControlPort  = 1028
	// DefaultCACertPort is the host vsock port the guest connects to at boot to
	// fetch the per-workspace egress CA certificate. Allocated only when the
	// negotiated capture provider mediates AND the mode forges certificates
	// (mitm) — broker splices opaquely and delivers no CA. See the CACertPort
	// gating where the request is built.
	DefaultCACertPort    = 1030
	DefaultShellPortBase = 22000
	DefaultShellPortSpan = 20000
	DefaultExecPortBase  = 42000
	DefaultExecPortSpan  = 20000
	DefaultTimeout       = 5 * time.Minute
)

// Model pairing transport defaults. The guest forwarder listens on
// 127.0.0.1:DefaultModelGuestPort and tunnels to host vsock DefaultModelVsockPort.
const (
	DefaultModelGuestPort uint16 = 11434
	DefaultModelVsockPort uint32 = 62100
)

// Egress broker transport defaults: the broker is served on host vsock port
// DefaultBrokerPort and reached in-guest via a bridge on
// DefaultBrokerGuestListen.
const (
	DefaultBrokerPort        uint32 = 1032
	DefaultBrokerGuestListen        = "127.0.0.1:18888"
)

// secretsListenerTarget marks the vsock listener that serves resolved secrets.
// Must equal the firecracker supervisor's sentinel.
const secretsListenerTarget = "secrets://serve"

type Options struct {
	Name                  string
	Purpose               string                  // opaque caller context; recorded verbatim
	CorrelationID         string                  // opaque caller correlation key; recorded verbatim
	Caller                vmkit.CallerAttribution // provenance-labeled caller context for lifecycle audit
	LifecycleEvidenceRef  string                  // host evidence linked from the next lifecycle event
	ImageRef              string
	ExecCommand           string
	ServiceCommand        string
	Entrypoint            string
	ConsoleShell          string
	Hostname              string
	SetupCommands         []string
	Env                   map[string]string
	Secrets               map[string]string // name -> scheme-prefixed reference
	SecretEnvFiles        []string          // dotenv file paths (plaintext, re-read each start)
	OnDemandSecrets       map[string]string // name -> reference (lazy, never materialized)
	SecretsAudit          bool              // append every access to the audit log
	EgressMode            string            // "broker" (default; allow-broad, no CA), "mitm" (forge per-SNI), or "off" (empty = broker)
	EgressAllow           []string          // allowlisted egress destination hosts
	EgressPassthrough     []string          // allowed hosts that are NOT TLS-intercepted
	EgressAllowlistLocked bool              // broker/mitm: restrict egress to allowlisted destinations only
	EgressSwapConfigPath  string            // path to the operator credential-swap config (mediator injects host-side; secret never enters the guest)
	// CredSwapProviders are parsed `--cred-swap PROVIDER[=ref]` specs. They are a
	// convenience surface over EgressSwapConfigPath: at workspace prep they are
	// resolved against the built-in provider registry, their hosts are unioned
	// into EgressAllow, and the resulting entries are written to a generated
	// per-workspace cred-swap config which becomes EgressSwapConfigPath. They
	// protect the TASK credentials a guest uses (provider API keys), never the
	// host's own auth; the guest never holds the key.
	CredSwapProviders []CredSwapProvider
	Files             []File
	Profile           string
	RestartPolicy     string
	Backend           string
	KernelPath        string
	StateDir          string
	SupervisorPath    string
	GuestInitPath     string
	Mke2fsPath        string
	Architecture      string
	MemoryMiB         int
	CPUCount          int
	SizeMiB           int64
	Network           vmkit.NetworkConfig
	Mediation         *vmkit.MediationConfig
	// Broker configures the egress broker: a host-side forward proxy on a vsock
	// listener that injects the workspace credential upstream so the guest only
	// ever holds a reference. Defaults (vsock port, guest listen address) are
	// filled by Request; see vmkit.BrokerConfig.
	Broker *vmkit.BrokerConfig
	// Brokers configures multiple egress broker endpoints; see
	// vmkit.Config.Brokers. Setting both Broker and Brokers is an operator
	// error, rejected both at the declaring surface (CLI/Agentfile/MCP) and by
	// normalizeEffectiveBrokers. Persisted in Manifest.Brokers.
	Brokers        []*vmkit.BrokerConfig
	Health         Health
	Timeout        time.Duration
	LeaseSeconds   int
	ResultPort     uint32
	ShellPort      uint16
	ExecPort       uint16
	GuestShellPort uint16
	GuestExecPort  uint16
	// BakedVsockUDSPath carries the source snapshot's baked vsock path when
	// starting from a snapshot; see vmkit.Config.BakedVsockUDSPath.
	BakedVsockUDSPath string
	Disks             []Disk
	Outputs           []Output
	VsockListeners    []vmkit.VsockListener
	// ModelTarget, when non-empty, is the host TCP address (host:port) of a paired
	// model server. It is realized as a guest→host vsock channel and a guest
	// forwarder. Orchestration (starting the runner) happens in the CLI layer.
	ModelTarget string
	// Model is the canonical model ref this workspace is paired with. It is
	// persisted in the manifest so every start re-pairs; the pull-time token is
	// never persisted.
	Model           string
	ModelRunner     ModelRunnerSpec
	ModelMediation  ModelMediationSpec
	ProfileExplicit bool
	KernelExplicit  bool
	// HeadroomMiB is the writable space a derived or auto-grown rootfs
	// guarantees the guest beyond the image content. Zero means the
	// builder default (512 MiB). Only consulted at build time.
	HeadroomMiB int64
	// SizeDerived records that SizeMiB came from image content rather than
	// an explicit size or profile; persisted so status can explain why the
	// disk is the size it is.
	SizeDerived bool
	// SizeExplicit marks a disk size the caller pinned (--size or an API
	// value). Without it, the rootfs build grows the disk to fit the image.
	SizeExplicit bool
	// FromSnapshot, when set, restores the workspace in place from this snapshot
	// tag instead of booting fresh (start --from-snapshot).
	FromSnapshot string
	SpecMemory   bool
	SpecCPU      bool
	SpecSize     bool
	Keep         bool
	DryRun       bool
	// SerialLogMaxBytes bounds the console log inlined into Result.SerialLog:
	// 0 means DefaultSerialLogMaxBytes, negative means the full log. The full
	// log always remains on disk at Result.SerialPath while the workspace is
	// kept; this only shapes the structured response.
	SerialLogMaxBytes int
	PrepareForStart   bool
	SerialInput       bool
	// MaintenanceBoot boots the guest with only the shell and exec
	// channels (no service command, no secrets) for guest-mediated file
	// operations against an otherwise-stopped workspace.
	MaintenanceBoot bool
	Verification    *vmkit.RuntimeVerification
	Progress        rootfs.ProgressFunc
	UseImageCommand bool
	// ImageEnv/ImageEntrypoint/ImageCmd carry the OCI image config captured
	// at build time (see the matching Manifest fields) so boot-time config
	// assembly can merge image env and honor --image-command.
	ImageEnv        []string
	ImageEntrypoint []string
	ImageCmd        []string
	// SetupComplete records that a setup boot exited successfully; until it
	// flips, every start re-runs the setup config.
	SetupComplete bool

	// RootfsBaseline, when set and the workspace is "plain" (nothing that would
	// change the rootfs — see CanReuseRootfsBaseline), lets BuildRootfs reuse a
	// previously pulled/tagged baseline rootfs (clone it) instead of pulling and
	// rebuilding. It receives the target rootfs path and returns the baseline path
	// to clone plus its provenance, or ok=false to fall through to a normal build.
	// Injected by the CLI (which owns the image cache) so pkg/workspace need not
	// depend on pkg/imagecache.
	RootfsBaseline func(rootfsPath string) (baseline string, prov rootfs.Provenance, ok bool)

	// RootfsBaselineSave, when set, is called after a full build whose
	// output is a plain baseline (CanReuseRootfsBaseline), so the first
	// build of an image can seed the baseline store no `image pull`
	// required. Failures are the callback's to swallow or log — seeding is
	// an optimization and must never fail the build that produced a
	// perfectly good rootfs.
	RootfsBaselineSave func(rootfsPath string, prov rootfs.Provenance)
}

// CredSwapProvider is one parsed `--cred-swap PROVIDER[=ref]` spec: a built-in
// provider name and an optional credential reference (env:/file:/vault:, never a
// literal). It is resolved into a static swap entry at workspace prep time; see
// Options.CredSwapProviders and materializeCredSwapConfig.
type CredSwapProvider struct {
	Provider string // built-in provider name (e.g. "anthropic", "openai")
	Ref      string // optional key reference; empty falls back to the provider default
}

// ParseCredSwapProvider parses one `PROVIDER[=ref]` spec into a CredSwapProvider,
// failing fast on an unknown provider or a literal (non-reference) key so a
// pasted secret never gets processed. It is the single parser shared by the CLI
// flag and the Agentfile `agent.cred-swap` block. An empty spec returns ok=false
// with no error so callers can skip blank entries.
func ParseCredSwapProvider(spec string) (CredSwapProvider, bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return CredSwapProvider{}, false, nil
	}
	provider, ref, _ := strings.Cut(spec, "=")
	provider = strings.TrimSpace(provider)
	ref = strings.TrimSpace(ref)
	// Validate the provider name and key reference up front (rejects a literal);
	// the resolved entry is rebuilt at workspace prep by materializeCredSwapConfig.
	if _, _, err := egress.ProviderSwapEntry(provider, ref); err != nil {
		return CredSwapProvider{}, false, err
	}
	return CredSwapProvider{Provider: provider, Ref: ref}, true, nil
}

type Spec struct {
	Name           string                `yaml:"name"`
	ImageRef       string                `yaml:"image"`
	Profile        string                `yaml:"profile"`
	Restart        string                `yaml:"restart"`
	Entrypoint     string                `yaml:"entrypoint"`
	Service        string                `json:"service_command,omitempty" yaml:"service"`
	Shell          string                `yaml:"shell"`
	Hostname       string                `yaml:"hostname"`
	Model          string                `yaml:"model"`
	ModelRunner    ModelRunnerSpec       `yaml:"modelRunner"`
	ModelMediation ModelMediationSpec    `yaml:"modelMediation"`
	Setup          SetupSteps            `yaml:"setup"`
	SetupFiles     []string              `yaml:"setupFiles"`
	Env            map[string]string     `yaml:"env"`
	Resources      Resources             `yaml:"resources"`
	Network        NetworkSpec           `yaml:"network"`
	Mediation      vmkit.MediationConfig `yaml:"mediation"`
	Health         Health                `yaml:"health"`
	Disks          []Disk                `yaml:"disks"`
	Bundles        []Disk                `yaml:"bundles"`
	Outputs        []Output              `yaml:"outputs"`
	Files          []File                `yaml:"files"`
	Agent          AgentSpec             `yaml:"agent"`
}

// AgentSpec is the optional `agent:` block of a workspace Spec — the "Agentfile"
// surface. It carries only the agent-defining knobs the rest of the Spec cannot
// express today; everything else (base image, dependency install, files, env)
// reuses existing top-level Spec fields. There is no image build: a Spec with an
// agent block is realized into a live workspace (pull thin base → run setup →
// drop files → exec entry under the egress envelope), not into an image.
type AgentSpec struct {
	// Entry is the agent's one-shot run command (maps to Options.ExecCommand).
	Entry string `yaml:"entry"`
	// Egress is the egress mediation mode: broker | mitm | off.
	Egress string `yaml:"egress"`
	// Allow lists extra egress hosts to allowlist, unioned with any from flags.
	Allow []string `yaml:"allow"`
	// CredSwap lists built-in providers to inject host-side, each PROVIDER[=ref]
	// (reference only, never a literal secret). See Options.CredSwapProviders.
	CredSwap []string `yaml:"cred-swap"`
	// Broker configures the egress broker: the guest reaches the upstream
	// through a host-side proxy that injects the credential, so the guest only
	// ever holds a reference. See Options.Broker and AgentBrokerSpec.
	Broker *AgentBrokerSpec `yaml:"broker"`
	// Brokers configures multiple egress broker endpoints; see Options.Brokers.
	// Setting both Broker and Brokers is rejected at spec-apply time.
	Brokers []AgentBrokerSpec `yaml:"brokers"`
}

// AgentBrokerSpec is the Agentfile `agent.broker` block (and each element of
// `agent.brokers`). Its fields mirror the --broker-* CLI flags and route
// through the same ParseBrokerConfig, so every surface builds an identical
// broker.
type AgentBrokerSpec struct {
	// Upstream is the terminate-mode upstream base URL.
	Upstream string `yaml:"upstream"`
	// Secret is the host-side-only credential reference NAME=<scheme>:<ref>.
	Secret string `yaml:"secret"`
	// Env lists base-URL env keys pointed at the broker, each KEY[=VALUE].
	Env []string `yaml:"env"`
	// Proxy sets HTTPS_PROXY/HTTP_PROXY in the guest to the broker.
	Proxy bool `yaml:"proxy"`
	// Capture opts in to governed raw capture of pre-swap requests. Off by
	// default; the default emission is the minimized decision stream.
	Capture bool `yaml:"capture"`
	// CA is an optional PEM bundle path this endpoint's upstream TLS client
	// trusts (maps to vmkit.BrokerConfig.UpstreamCAFile); empty means system
	// roots.
	CA string `yaml:"ca"`
}

// Declared reports whether the agent block carries any field, so an empty block
// is a no-op rather than forcing egress/exec defaults.
func (a AgentSpec) Declared() bool {
	return strings.TrimSpace(a.Entry) != "" ||
		strings.TrimSpace(a.Egress) != "" ||
		len(a.Allow) != 0 ||
		len(a.CredSwap) != 0 ||
		a.Broker != nil ||
		len(a.Brokers) != 0
}

type NetworkSpec struct {
	Mode         string              `json:"mode,omitempty" yaml:"mode,omitempty"`
	PortForwards []vmkit.PortForward `json:"port_forwards,omitempty" yaml:"forwards,omitempty"`
	DNS          []string            `json:"dns,omitempty" yaml:"dns,omitempty"`
	Routes       []string            `json:"routes,omitempty" yaml:"routes,omitempty"`
	IP           string              `json:"ip,omitempty" yaml:"ip,omitempty"`
	Subnet       string              `json:"subnet,omitempty" yaml:"subnet,omitempty"`
	Gateway      string              `json:"gateway,omitempty" yaml:"gateway,omitempty"`
}

type Disk struct {
	Name       string `json:"name" yaml:"name"`
	SourcePath string `json:"source_path,omitempty" yaml:"sourcePath,omitempty"`
	Path       string `json:"path" yaml:"path"`
	Mountpoint string `json:"mountpoint" yaml:"mountpoint"`
	Mode       string `json:"mode" yaml:"mode"`
	Bundle     bool   `json:"bundle,omitempty" yaml:"bundle,omitempty"`
	// ManagedVolume marks a disk that refers to a named managed volume by name.
	// It is resolved to a backing ext4 path before the disk is persisted, so it
	// is transient and never serialized.
	ManagedVolume bool `json:"-" yaml:"-"`
}

type Output struct {
	Name string `json:"name" yaml:"name"`
	Path string `json:"path" yaml:"path"`
}

type File struct {
	SourcePath string `json:"source_path" yaml:"src"`
	Path       string `json:"path" yaml:"dst"`
	Mode       string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

type SetupStep struct {
	Run  string `json:"run,omitempty" yaml:"run,omitempty"`
	File string `json:"file,omitempty" yaml:"file,omitempty"`
}

type SetupSteps []SetupStep

func (steps *SetupSteps) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var raw string
		if err := value.Decode(&raw); err != nil {
			return err
		}
		*steps = SetupSteps{{Run: raw}}
		return nil
	case yaml.SequenceNode:
		out := make([]SetupStep, 0, len(value.Content))
		for _, item := range value.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				var raw string
				if err := item.Decode(&raw); err != nil {
					return err
				}
				out = append(out, SetupStep{Run: raw})
			case yaml.MappingNode:
				var decoded struct {
					Run     string `yaml:"run"`
					Command string `yaml:"command"`
					File    string `yaml:"file"`
				}
				if err := item.Decode(&decoded); err != nil {
					return err
				}
				step := SetupStep{Run: decoded.Run, File: decoded.File}
				if strings.TrimSpace(step.Run) == "" {
					step.Run = decoded.Command
				}
				out = append(out, step)
			default:
				return fmt.Errorf("setup entries must be strings or maps")
			}
		}
		*steps = out
		return nil
	case 0:
		return nil
	default:
		return fmt.Errorf("setup must be a string or list")
	}
}

type Artifacts struct {
	Ingress []Disk   `json:"ingress,omitempty"`
	Egress  []Output `json:"egress,omitempty"`
}

type Manifest struct {
	Name           string                 `json:"name"`
	Purpose        string                 `json:"purpose,omitempty"`
	CorrelationID  string                 `json:"correlation_id,omitempty"`
	Profile        string                 `json:"profile,omitempty"`
	Restart        string                 `json:"restart"`
	Resources      Resources              `json:"resources"`
	Network        NetworkSpec            `json:"network,omitempty"`
	Service        string                 `json:"service_command,omitempty"`
	ConsoleShell   string                 `json:"shell,omitempty"`
	Hostname       string                 `json:"hostname,omitempty"`
	Model          string                 `json:"model,omitempty"`
	ModelRunner    *ModelRunnerSpec       `json:"model_runner,omitempty"`
	ModelMediation *ModelMediationSpec    `json:"model_mediation,omitempty"`
	Mediation      *vmkit.MediationConfig `json:"mediation,omitempty"`
	Health         *Health                `json:"health,omitempty"`
	// SizeDerived records that Resources.SizeMiB was derived from image
	// content rather than pinned by a size or profile.
	SizeDerived           bool                       `json:"size_derived,omitempty"`
	Disks                 []Disk                     `json:"disks,omitempty"`
	Artifacts             Artifacts                  `json:"artifacts,omitempty"`
	Verification          *vmkit.RuntimeVerification `json:"verification,omitempty"`
	Secrets               []vmkit.SecretRef          `json:"secrets,omitempty"`
	SecretEnvFiles        []string                   `json:"secret_env_files,omitempty"`
	OnDemandSecrets       []vmkit.SecretRef          `json:"on_demand_secrets,omitempty"`
	SecretsAudit          bool                       `json:"secrets_audit,omitempty"`
	EgressMode            string                     `json:"egress_mode,omitempty"`
	EgressAllow           []string                   `json:"egress_allow,omitempty"`
	EgressPassthrough     []string                   `json:"egress_passthrough,omitempty"`
	EgressAllowlistLocked bool                       `json:"egress_allowlist_locked,omitempty"`
	EgressSwapConfigPath  string                     `json:"egress_swap_config_path,omitempty"`
	Broker                *vmkit.BrokerConfig        `json:"broker,omitempty"`
	// Brokers persists the multi-endpoint broker set (see Options.Brokers), so
	// restart/wake preserves every endpoint, not just a single legacy Broker.
	Brokers []*vmkit.BrokerConfig `json:"brokers,omitempty"`
	// Boot configuration. Nothing is baked into the rootfs; every Start
	// assembles the guest config disk host-side from these fields, so they
	// must round-trip through the manifest.
	Entrypoint      string            `json:"entrypoint,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	UseImageCommand bool              `json:"use_image_command,omitempty"`
	// ImageEnv/ImageEntrypoint/ImageCmd are the OCI image config captured
	// at build time — the only point the image config is available — so
	// boot-time assembly can merge image env and honor --image-command
	// without rebuilding.
	ImageEnv        []string `json:"image_env,omitempty"`
	ImageEntrypoint []string `json:"image_entrypoint,omitempty"`
	ImageCmd        []string `json:"image_cmd,omitempty"`
	// Files records the declared file injections for introspection; the
	// contents authoritative for boots are captured at create time into
	// files.tar beside this manifest.
	Files []File `json:"files,omitempty"`
	// SetupCommands/ExecCommand are persisted so a start after a failed
	// setup boot can regenerate the setup config and retry.
	SetupCommands []string `json:"setup_commands,omitempty"`
	ExecCommand   string   `json:"exec_command,omitempty"`
	// SetupComplete flips when a setup boot exits successfully. Until
	// then every start re-runs the setup config — the same
	// retry-on-failure semantics the in-rootfs config rewrite had, without
	// the failure mode where a dead setup script poisons later boots.
	SetupComplete bool `json:"setup_complete,omitempty"`
}

type ModelRunnerSpec struct {
	Backend      string   `json:"backend,omitempty" yaml:"backend,omitempty"`
	GPU          string   `json:"gpu,omitempty" yaml:"gpu,omitempty"`
	BackendModel string   `json:"backend_model,omitempty" yaml:"backendModel,omitempty"`
	ServedModel  string   `json:"served_model,omitempty" yaml:"servedModel,omitempty"`
	Command      []string `json:"command,omitempty" yaml:"command,omitempty"`
	Name         string   `json:"name,omitempty" yaml:"name,omitempty"`
	HealthPath   string   `json:"health_path,omitempty" yaml:"healthPath,omitempty"`
	Args         []string `json:"args,omitempty" yaml:"args,omitempty"`
	Env          []string `json:"-" yaml:"-"`
}

type ModelMediationSpec struct {
	Mode          string `json:"mode,omitempty" yaml:"mode,omitempty"`
	PolicyURL     string `json:"policy_url,omitempty" yaml:"policyURL,omitempty"`
	PolicyFile    string `json:"policy_file,omitempty" yaml:"policyFile,omitempty"`
	PolicyTimeout string `json:"policy_timeout,omitempty" yaml:"policyTimeout,omitempty"`
}

type Resources struct {
	MemoryMiB int   `json:"memory_mib" yaml:"memoryMiB"`
	CPUCount  int   `json:"cpu_count" yaml:"cpuCount"`
	SizeMiB   int64 `json:"size_mib,omitempty" yaml:"sizeMiB,omitempty"`
	// HeadroomMiB is spec-only input: writable space guaranteed beyond the
	// image content when the disk size is derived or auto-grown.
	HeadroomMiB int64 `json:"headroom_mib,omitempty" yaml:"headroomMiB,omitempty"`
}

type Profile struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Resources   Resources `json:"resources"`
}

var Profiles = []Profile{
	{Name: "tiny", Description: "smoke tests and very small shells", Resources: Resources{MemoryMiB: 256, CPUCount: 1, SizeMiB: 512}},
	{Name: "small", Description: "default lightweight workspace", Resources: Resources{MemoryMiB: DefaultWorkspaceMemoryMiB, CPUCount: DefaultWorkspaceCPUCount, SizeMiB: rootfs.DefaultSizeMiB}},
	{Name: "medium", Description: "package installs and normal agent work", Resources: Resources{MemoryMiB: 2048, CPUCount: 2, SizeMiB: 8192}},
	{Name: "large", Description: "heavier builds and larger workspaces", Resources: Resources{MemoryMiB: 4096, CPUCount: 4, SizeMiB: 16384}},
}

func DefaultOptions() Options {
	opts := Options{
		Backend:       HostBackend(),
		Architecture:  GuestArch(),
		Profile:       DefaultWorkspaceProfile,
		RestartPolicy: DefaultRestartPolicy,
		Network:       vmkit.NetworkConfig{Mode: DefaultNetworkMode},
		Timeout:       DefaultTimeout,
		ResultPort:    DefaultResultPort,
		StateDir:      StateDir(),
		Mke2fsPath:    Mke2fsPath(),
	}
	opts.KernelPath = KernelPath(opts.Backend, opts.Architecture)
	opts.GuestInitPath = GuestInitPath(opts.Architecture)
	if opts.Backend == vmkit.BackendAppleVF {
		opts.SupervisorPath = AppleVFSupervisorPath()
	}
	_ = ApplyProfile(&opts, false, false, false)
	return opts
}

func HostBackend() string {
	switch runtime.GOOS {
	case "darwin":
		return vmkit.BackendAppleVF
	case "linux":
		return vmkit.BackendLinuxKVM
	default:
		return ""
	}
}

func ValidateHostBackend(backend string) error {
	backend = strings.TrimSpace(backend)
	hostBackend := HostBackend()
	if hostBackend == "" {
		return fmt.Errorf("microagent does not support a backend on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if backend == "" {
		return fmt.Errorf("backend is required; this %s/%s install supports %s", runtime.GOOS, runtime.GOARCH, hostBackend)
	}
	if backend != hostBackend {
		return fmt.Errorf("backend %q is not available in this %s/%s install; supported backend is %q", backend, runtime.GOOS, runtime.GOARCH, hostBackend)
	}
	return nil
}

func GuestArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

// NormalizeArch maps the architecture spellings people actually type - uname
// output like "aarch64" and "x86_64" - onto the Go/OCI names the manifests
// and registries use. Unknown values pass through for the resolver to reject
// with their own error.
func NormalizeArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "aarch64", "arm64":
		return "arm64"
	case "x86_64", "x64", "amd64":
		return "amd64"
	default:
		return strings.TrimSpace(arch)
	}
}

// ValidateArch rejects guest architectures this build cannot boot. Keep this
// check separate from NormalizeArch so OCI-only callers may still normalize
// other platform names without turning them into host filesystem components.
func ValidateArch(arch string) error {
	normalized := NormalizeArch(arch)
	if supportedGuestArch(normalized) {
		return nil
	}
	return operation.New(operation.ErrorValidation, "unsupported guest architecture %q: choose arm64 (aarch64) or amd64 (x86_64)", strings.TrimSpace(arch))
}

func supportedGuestArch(arch string) bool {
	arch = NormalizeArch(arch)
	return arch == "arm64" || arch == "amd64"
}

func DefaultImage(arch string) string {
	switch strings.TrimSpace(arch) {
	case "arm64", "aarch64":
		return DefaultWorkspaceImageArm64
	case "amd64", "x86_64":
		return DefaultWorkspaceImageAMD64
	default:
		return DefaultWorkspaceImageOther
	}
}

func StateDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), "microagent")
	}
	return filepath.Join(home, ".microagent")
}

func KernelPath(backend, arch string) string {
	arch = NormalizeArch(arch)
	if !vmkit.SafeIdentifier(backend) || !supportedGuestArch(arch) {
		return ""
	}
	writable := WritableKernelPath(backend, arch)
	if writable == "" {
		return PackagedKernelPath(backend, arch)
	}
	if _, err := os.Stat(writable); err == nil {
		return writable
	}
	legacy := LegacyKernelPath(backend)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	packaged := PackagedKernelPath(backend, arch)
	if packaged != "" {
		if _, err := os.Stat(packaged); err == nil {
			return packaged
		}
	}
	return writable
}

func WritableKernelPath(backend, arch string) string {
	arch = NormalizeArch(arch)
	if !vmkit.SafeIdentifier(backend) || !supportedGuestArch(arch) {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".microagent", "kernels", backend, arch, "Image")
}

func LegacyKernelPath(backend string) string {
	if !vmkit.SafeIdentifier(backend) {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".microagent", "kernels", backend, "Image")
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// installBases lists the executables to resolve install-relative runtime paths
// against: the running binary first (correct when microagent's own CLI/supervisor
// is running), then the installed `microagent` on PATH. The second base is what
// makes resolution correct for LIBRARY CONSUMERS — a process embedding microagent
// (e.g. microagency) has its own os.Executable(), not microagent's install prefix,
// so without this the guest-init/kernel/supervisor are looked for in the wrong dir.
func installBases() []string {
	var bases []string
	if exe, err := os.Executable(); err == nil && exe != "" {
		bases = append(bases, exe)
	}
	if p, err := exec.LookPath("microagent"); err == nil && p != "" {
		if len(bases) == 0 || p != bases[0] {
			bases = append(bases, p)
		}
	}
	return bases
}

// resolveInstallPath returns the first install base whose resolved path exists,
// falling back to the first base's resolution (preserving prior behavior when
// nothing is found).
func resolveInstallPath(fromExecutable func(string) string) string {
	var fallback string
	for i, base := range installBases() {
		cand := fromExecutable(base)
		if fileExists(cand) {
			return cand
		}
		if i == 0 {
			fallback = cand
		}
	}
	return fallback
}

func PackagedKernelPath(backend, arch string) string {
	return resolveInstallPath(func(exe string) string {
		return PackagedKernelPathFromExecutable(exe, backend, arch)
	})
}

func PackagedKernelPathFromExecutable(executable, backend, arch string) string {
	arch = NormalizeArch(arch)
	if !vmkit.SafeIdentifier(backend) || !supportedGuestArch(arch) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	dir := filepath.Dir(executable)
	candidates := []string{
		filepath.Join(filepath.Clean(filepath.Join(dir, "..", "libexec")), "kernels", backend, arch, "Image"),
		filepath.Join(dir, "kernels", backend, arch, "Image"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

// e2fsprogsFallbackDirs are the keg-only Homebrew install locations for
// e2fsprogs on macOS (Apple Silicon and Intel prefixes). Homebrew never links
// keg-only formulae into PATH, so tool resolution falls back here after
// LookPath. A package-level var so tests can point resolution at scratch
// directories without a real brew install.
var e2fsprogsFallbackDirs = []string{
	"/opt/homebrew/opt/e2fsprogs/sbin",
	"/usr/local/opt/e2fsprogs/sbin",
}

// LookupE2fsprogsTool resolves an e2fsprogs binary (mke2fs, e2fsck, debugfs)
// the way the copy/commit/build paths do: PATH first, then the keg-only
// Homebrew locations. found reports whether a real binary was located; when
// false, path is the bare name so callers keep the legacy exec-and-fail
// behavior.
func LookupE2fsprogsTool(name string) (path string, found bool) {
	if path, err := exec.LookPath(name); err == nil {
		return path, true
	}
	for _, dir := range e2fsprogsFallbackDirs {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return name, false
}

func Mke2fsPath() string {
	path, _ := LookupE2fsprogsTool("mke2fs")
	return path
}

func Resize2fsPath() string {
	path, _ := LookupE2fsprogsTool("resize2fs")
	return path
}

func AppleVFSupervisorPath() string {
	if path := strings.TrimSpace(os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR")); path != "" {
		return path
	}
	if p := resolveInstallPath(AppleVFSupervisorPathFromExecutable); p != "" {
		return p
	}
	return "microagent-applevf-supervisor"
}

func AppleVFSupervisorPathFromExecutable(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	dir := filepath.Dir(executable)
	candidates := []string{
		filepath.Join(dir, "microagent-applevf-supervisor"),
		filepath.Join(filepath.Clean(filepath.Join(dir, "..", "..")), "supervisors", "applevf", ".build", "release", "microagent-applevf-supervisor"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "microagent-applevf-supervisor"
}

func GuestInitPath(arch string) string {
	arch = NormalizeArch(arch)
	if !supportedGuestArch(arch) {
		return ""
	}
	if p := resolveInstallPath(func(exe string) string {
		return GuestInitPathFromExecutable(exe, arch)
	}); p != "" {
		return p
	}
	return "microagent-guestinit"
}

func GuestInitPathFromExecutable(executable, arch string) string {
	arch = NormalizeArch(arch)
	if !supportedGuestArch(arch) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	dir := filepath.Dir(executable)
	libexecDir := filepath.Clean(filepath.Join(dir, "..", "libexec"))
	candidates := []string{
		filepath.Join(libexecDir, "microagent-guestinit-"+arch),
		filepath.Join(libexecDir, "microagent-guestinit"),
		filepath.Join(dir, "microagent-guestinit-"+arch),
		filepath.Join(dir, "microagent-guestinit"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

func BackendSupportsConsoleInput(backend string) bool {
	operation, ok := vmkit.OperationContractByID(vmkit.OperationWorkspaceConsole)
	if !ok {
		return false
	}
	ready, _ := vmkit.BackendSupportsOperation(backend, operation)
	return ready
}

func LookupProfile(name string) (Profile, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, profile := range Profiles {
		if profile.Name == name {
			return profile, true
		}
	}
	return Profile{}, false
}

func ProfileNames() []string {
	names := make([]string, 0, len(Profiles))
	for _, profile := range Profiles {
		names = append(names, profile.Name)
	}
	return names
}

func ApplyProfile(opts *Options, memoryExplicit, cpusExplicit, sizeExplicit bool) error {
	profile, ok := LookupProfile(opts.Profile)
	if !ok {
		return operation.New(operation.ErrorValidation, "unknown resource profile %q; choose one of: %s", opts.Profile, strings.Join(ProfileNames(), ", "))
	}
	opts.Profile = profile.Name
	if !memoryExplicit {
		opts.MemoryMiB = profile.Resources.MemoryMiB
	}
	if !cpusExplicit {
		opts.CPUCount = profile.Resources.CPUCount
	}
	if !sizeExplicit {
		opts.SizeMiB = profile.Resources.SizeMiB
	}
	return nil
}

func ValidateResources(resources Resources, requireDisk bool) error {
	if resources.MemoryMiB <= 0 {
		return operation.New(operation.ErrorValidation, "memory must be positive")
	}
	if resources.CPUCount <= 0 {
		return operation.New(operation.ErrorValidation, "cpus must be positive")
	}
	if requireDisk && resources.SizeMiB <= 0 {
		return operation.New(operation.ErrorValidation, "size-mib must be positive")
	}
	if resources.SizeMiB < 0 {
		return operation.New(operation.ErrorValidation, "size-mib must not be negative")
	}
	return nil
}

func ValidateRestartPolicy(policy string) error {
	switch NormalizeRestartPolicy(policy) {
	case "never", "on-failure", "always":
		return nil
	default:
		return operation.New(operation.ErrorValidation, "restart policy must be never, on-failure, or always")
	}
}

func NormalizeRestartPolicy(policy string) string {
	if strings.TrimSpace(policy) == "" {
		return DefaultRestartPolicy
	}
	return strings.TrimSpace(policy)
}

func NormalizeNetworkConfig(network vmkit.NetworkConfig) vmkit.NetworkConfig {
	network.Mode = strings.TrimSpace(network.Mode)
	if network.Mode == "" {
		network.Mode = DefaultNetworkMode
	}
	network.IP = strings.TrimSpace(network.IP)
	network.Subnet = strings.TrimSpace(network.Subnet)
	network.Gateway = strings.TrimSpace(network.Gateway)
	for i := range network.PortForwards {
		network.PortForwards[i].Protocol = strings.TrimSpace(network.PortForwards[i].Protocol)
		if network.PortForwards[i].Protocol == "" {
			network.PortForwards[i].Protocol = "tcp"
		}
		network.PortForwards[i].Host = strings.TrimSpace(network.PortForwards[i].Host)
	}
	return network
}

func NetworkSpecFromConfig(network vmkit.NetworkConfig) NetworkSpec {
	network = NormalizeNetworkConfig(network)
	return NetworkSpec{
		Mode:         network.Mode,
		PortForwards: append([]vmkit.PortForward{}, network.PortForwards...),
		DNS:          append([]string{}, network.DNS...),
		Routes:       append([]string{}, network.Routes...),
		IP:           network.IP,
		Subnet:       network.Subnet,
		Gateway:      network.Gateway,
	}
}

func NetworkConfigFromSpec(spec NetworkSpec) vmkit.NetworkConfig {
	return NormalizeNetworkConfig(vmkit.NetworkConfig{
		Mode:         spec.Mode,
		PortForwards: append([]vmkit.PortForward{}, spec.PortForwards...),
		DNS:          append([]string{}, spec.DNS...),
		Routes:       append([]string{}, spec.Routes...),
		IP:           spec.IP,
		Subnet:       spec.Subnet,
		Gateway:      spec.Gateway,
	})
}

func NetworkConfigPtr(network vmkit.NetworkConfig) *vmkit.NetworkConfig {
	normalized := NormalizeNetworkConfig(network)
	return &normalized
}

func ResourcesFromOptions(opts Options) Resources {
	return Resources{
		MemoryMiB:   opts.MemoryMiB,
		CPUCount:    opts.CPUCount,
		SizeMiB:     opts.SizeMiB,
		HeadroomMiB: opts.HeadroomMiB,
	}
}

func ArtifactsFromOptions(opts Options) Artifacts {
	artifacts := Artifacts{Egress: append([]Output{}, opts.Outputs...)}
	for _, disk := range opts.Disks {
		if disk.Bundle {
			artifacts.Ingress = append(artifacts.Ingress, disk)
		}
	}
	return artifacts
}

func RuntimeArtifacts(artifacts Artifacts) vmkit.RuntimeArtifacts {
	result := vmkit.RuntimeArtifacts{}
	for _, ingress := range artifacts.Ingress {
		kind := "disk"
		if ingress.Bundle {
			kind = "bundle"
		}
		result.Ingress = append(result.Ingress, vmkit.ArtifactRef{
			Name:       ingress.Name,
			Path:       firstNonEmpty(ingress.SourcePath, ingress.Path),
			Mountpoint: ingress.Mountpoint,
			Mode:       ingress.Mode,
			Kind:       kind,
		})
	}
	for _, egress := range artifacts.Egress {
		result.Egress = append(result.Egress, vmkit.ArtifactRef{
			Name: egress.Name,
			Path: egress.Path,
			Kind: "output",
		})
	}
	return result
}

func Mounts(disks []Disk) []rootfs.Mount {
	return MountsForBackend("", disks)
}

func MountsForBackend(backend string, disks []Disk) []rootfs.Mount {
	if len(disks) == 0 {
		return nil
	}
	mounts := make([]rootfs.Mount, 0, len(disks))
	for idx, disk := range disks {
		mounts = append(mounts, rootfs.Mount{
			Device:     BlockDeviceForBackend(backend, idx+1),
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	return mounts
}

func BlockDeviceForBackend(backend string, index int) string {
	return VirtioBlockDevice(index)
}

func RootfsFiles(files []File) []rootfs.File {
	if len(files) == 0 {
		return nil
	}
	out := make([]rootfs.File, 0, len(files))
	for _, file := range files {
		out = append(out, rootfs.File{
			SourcePath: file.SourcePath,
			Path:       file.Path,
			Mode:       file.Mode,
		})
	}
	return out
}

func VirtioBlockDevice(index int) string {
	return vmkit.VirtioBlockDevice(index)
}

func SCSIBlockDevice(index int) string {
	if index < 0 {
		index = 0
	}
	name := ""
	for {
		name = string(rune('a'+(index%26))) + name
		index = index/26 - 1
		if index < 0 {
			break
		}
	}
	return "/dev/sd" + name
}

func RootfsPortForwards(forwards []vmkit.PortForward) []rootfs.PortForward {
	if len(forwards) == 0 {
		return nil
	}
	out := make([]rootfs.PortForward, 0, len(forwards))
	for _, forward := range forwards {
		out = append(out, rootfs.PortForward{
			Protocol:  "tcp",
			Host:      forward.Host,
			HostPort:  forward.HostPort,
			GuestPort: forward.GuestPort,
		})
	}
	return out
}

func ShellPortForName(name string) uint16 {
	return hashedPortForName(name, DefaultShellPortBase, DefaultShellPortSpan)
}

func ExecPortForName(name string) uint16 {
	return hashedPortForName(name, DefaultExecPortBase, DefaultExecPortSpan)
}

func hashedPortForName(name string, base, span int) uint16 {
	var hash uint32 = 2166136261
	for _, b := range []byte(strings.TrimSpace(name)) {
		hash ^= uint32(b)
		hash *= 16777619
	}
	return uint16(base + int(hash%uint32(span)))
}

func ShellPort(opts Options) uint16 {
	if opts.ShellPort != 0 {
		return opts.ShellPort
	}
	return ShellPortForName(opts.Name)
}

func ExecPort(opts Options) uint16 {
	if opts.ExecPort != 0 {
		return opts.ExecPort
	}
	return ExecPortForName(opts.Name)
}

// SecretsPort returns the host vsock port used to serve secrets, or 0 when no
// secrets are declared.
func SecretsPort(opts Options) uint32 {
	if len(opts.Secrets) == 0 && len(opts.SecretEnvFiles) == 0 && len(opts.OnDemandSecrets) == 0 {
		return 0
	}
	return DefaultSecretsPort
}

// SecretsControlPort returns the guest control vsock port when any secrets are
// declared (used for snapshot purge/rehydrate), or 0 otherwise.
func SecretsControlPort(opts Options) uint32 {
	if SecretsPort(opts) == 0 {
		return 0
	}
	return DefaultSecretsControlPort
}

// onDemandRefsFromOptions converts the on-demand name->ref map into a stable slice.
func onDemandRefsFromOptions(opts Options) []vmkit.SecretRef {
	if len(opts.OnDemandSecrets) == 0 {
		return nil
	}
	names := make([]string, 0, len(opts.OnDemandSecrets))
	for name := range opts.OnDemandSecrets {
		names = append(names, name)
	}
	sort.Strings(names)
	refs := make([]vmkit.SecretRef, 0, len(names))
	for _, name := range names {
		refs = append(refs, vmkit.SecretRef{Name: name, Ref: opts.OnDemandSecrets[name]})
	}
	return refs
}

// secretRefsFromOptions converts the name->ref map into a stable slice.
func secretRefsFromOptions(opts Options) []vmkit.SecretRef {
	if len(opts.Secrets) == 0 {
		return nil
	}
	names := make([]string, 0, len(opts.Secrets))
	for name := range opts.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	refs := make([]vmkit.SecretRef, 0, len(names))
	for _, name := range names {
		refs = append(refs, vmkit.SecretRef{Name: name, Ref: opts.Secrets[name]})
	}
	return refs
}

// EgressPolicyFromOptions maps the egress-related fields of Options into an
// EgressPolicy bundle. Fields with no corresponding Options source (SwapConfigPath,
// Caps, DNS) are left at their zero values.
func EgressPolicyFromOptions(opts Options) vmkit.EgressPolicy {
	return vmkit.EgressPolicy{
		Mode:            opts.EgressMode,
		Allow:           opts.EgressAllow,
		Passthrough:     opts.EgressPassthrough,
		AllowlistLocked: opts.EgressAllowlistLocked,
		SwapConfigPath:  opts.EgressSwapConfigPath,
		// Caps and DNS have no source on Options; callers that need them must
		// build the EgressPolicy directly.
	}
}

func Request(opts Options, command, rootfsPath string, requestID string) (vmkit.Request, error) {
	// Build, normalize, and validate the egress policy at this single
	// chokepoint before any other egress-dependent work (CA-cert listener
	// allocation, etc.) so an invalid policy fails the start early.
	pol := vmkit.NormalizeEgressPolicy(EgressPolicyFromOptions(opts))
	if err := pol.Validate(); err != nil {
		return vmkit.Request{}, fmt.Errorf("egress policy: %w", err)
	}
	if err := pol.ValidateForNetworkMode(opts.Network.Mode); err != nil {
		return vmkit.Request{}, fmt.Errorf("egress policy: %w", err)
	}
	opts.EgressMode = pol.Mode
	var listeners []vmkit.VsockListener
	if opts.ResultPort != 0 {
		listeners = []vmkit.VsockListener{{Port: opts.ResultPort, Target: ResultPath(opts.StateDir, opts.Name)}}
	}
	listeners = append(listeners, opts.VsockListeners...)
	if opts.Mediation != nil && opts.Mediation.Enabled {
		listeners = append(listeners, vmkit.VsockListener{Port: opts.Mediation.Port, Target: opts.Mediation.Target})
	}
	var modelGuestPort uint16
	var modelVsockPort uint32
	if strings.TrimSpace(opts.ModelTarget) != "" {
		modelGuestPort = DefaultModelGuestPort
		modelVsockPort = DefaultModelVsockPort
		listeners = append(listeners, vmkit.VsockListener{Port: DefaultModelVsockPort, Target: opts.ModelTarget})
	}
	secretRefs := secretRefsFromOptions(opts)
	secretsPort := SecretsPort(opts)
	if secretsPort != 0 {
		listeners = append(listeners, vmkit.VsockListener{Port: secretsPort, Target: secretsListenerTarget})
	}
	brokers, err := normalizeEffectiveBrokers(opts)
	if err != nil {
		return vmkit.Request{}, err
	}
	for _, bc := range brokers {
		listeners = append(listeners, vmkit.VsockListener{Port: bc.VsockPort, Target: broker.ListenerTarget})
	}
	// CACertPort is allocated only when the negotiated capture provider actually
	// mediates a protocol class AND the mode forges certificates (mitm)
	// — i.e. a real mediator will exist AND it needs the guest to trust the
	// per-workspace CA it forges leaves from. Gating on the provider (not on
	// EgressMediationOn + NetworkModeMediates) means a backend/mode combination
	// with no capture provider never gets a CA-cert listener its supervisor
	// cannot serve — an unserved listener is what broke the default apple-vf
	// boot before it grew the host-fd provider. "off" and isolated provide no
	// mediator; broker mediates but splices opaquely and forges nothing, so
	// none of them deliver a CA.
	captureReport := vmkit.NegotiateEgressCapture(opts.Backend, opts.Network.Mode, opts.EgressMode)
	var caCertPort uint32
	if captureReport.MediatesAnyClass() && vmkit.EgressModeForgesCerts(opts.EgressMode) {
		caCertPort = DefaultCACertPort
		listeners = append(listeners, vmkit.VsockListener{Port: caCertPort, Target: secretxfer.CACertTarget})
	}
	disks := make([]vmkit.Disk, 0, len(opts.Disks))
	for _, disk := range opts.Disks {
		disks = append(disks, vmkit.Disk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	identity := &vmkit.Identity{
		RequestID:     requestID,
		RuntimeID:     opts.Name,
		Purpose:       opts.Purpose,
		CorrelationID: opts.CorrelationID,
		Role:          vmkit.RoleWorkload,
		Backend:       opts.Backend,
	}
	if command == "run" || command == "start" {
		identity.SessionID = NewSessionID()
	}
	return vmkit.Request{
		Command:  command,
		Identity: identity,
		Config: &vmkit.Config{
			KernelPath:               opts.KernelPath,
			RootfsPath:               rootfsPath,
			StateDir:                 opts.StateDir,
			MemoryMiB:                opts.MemoryMiB,
			CPUCount:                 opts.CPUCount,
			Disks:                    disks,
			VsockListeners:           listeners,
			Mediation:                opts.Mediation,
			Network:                  NetworkConfigPtr(opts.Network),
			ShellPort:                ShellPort(opts),
			ExecPort:                 ExecPort(opts),
			Hostname:                 strings.TrimSpace(opts.Hostname),
			ConfigDiskPath:           ConfigDiskFile(opts.StateDir, opts.Name),
			SecretsPort:              secretsPort,
			CACertPort:               caCertPort,
			Secrets:                  secretRefs,
			SecretEnvFiles:           opts.SecretEnvFiles,
			OnDemandSecrets:          onDemandRefsFromOptions(opts),
			SecretsAudit:             opts.SecretsAudit,
			EgressMode:               pol.Mode,
			EgressAllow:              pol.Allow,
			EgressPassthrough:        pol.Passthrough,
			EgressAllowlistLocked:    pol.AllowlistLocked,
			EgressSwapConfigPath:     pol.SwapConfigPath,
			EgressMaxBytesPerSec:     pol.Caps.MaxBytesPerSec,
			EgressMaxTotalBytes:      pol.Caps.MaxTotalBytes,
			EgressMaxConcurrentConns: pol.Caps.MaxConcurrentConns,
			EgressAuditMaxBytes:      pol.Caps.AuditMaxBytes,
			EgressAuditMaxBackups:    pol.Caps.AuditMaxBackups,
			SecretsControlPort:       SecretsControlPort(opts),
			GuestShellPort:           opts.GuestShellPort,
			GuestExecPort:            opts.GuestExecPort,
			BakedVsockUDSPath:        opts.BakedVsockUDSPath,
			SerialInput:              opts.SerialInput,
			MaintenanceBoot:          opts.MaintenanceBoot,
			TimeoutSeconds:           int(opts.Timeout.Seconds()),
			LeaseSeconds:             opts.LeaseSeconds,
			ModelGuestPort:           modelGuestPort,
			ModelVsockPort:           modelVsockPort,
			Brokers:                  brokers,
		},
	}, nil
}

// effectiveBrokers returns the broker endpoints Options declares: the
// explicit multi-endpoint set when present, else the single legacy Broker
// folded into a one-element set, else nil. It only resolves precedence — the
// "both set" operator error is normalizeEffectiveBrokers' job, so it exists in
// exactly one place.
func effectiveBrokers(opts Options) []*vmkit.BrokerConfig {
	if len(opts.Brokers) > 0 {
		return opts.Brokers
	}
	if opts.Broker != nil {
		return []*vmkit.BrokerConfig{opts.Broker}
	}
	return nil
}

// normalizeEffectiveBrokers is the single chokepoint Request and rootfsRequest
// both call to derive the broker endpoints to run: it rejects the operator
// error of setting both Options.Broker and Options.Brokers, enforces the
// backend's BrokerEndpoints capability, then normalizes whichever one is set
// via normalizeBrokers. The capability gate lives here — before any listener
// is composed — so a broker workspace on a backend whose supervisor cannot
// serve the broker vsock target fails closed with the declared contract gap
// instead of a supervisor protocol error at start.
func normalizeEffectiveBrokers(opts Options) ([]*vmkit.BrokerConfig, error) {
	if len(opts.Brokers) > 0 && opts.Broker != nil {
		return nil, fmt.Errorf("broker: set either a single broker or a broker set, not both")
	}
	brokers := effectiveBrokers(opts)
	if len(brokers) > 0 && !vmkit.BackendCapabilities(opts.Backend).BrokerEndpoints {
		feature, _ := vmkit.FeatureForCLICommand("create --broker-upstream")
		return nil, vmkit.NewUnsupportedFeatureError(opts.Backend, feature, "broker endpoints")
	}
	return normalizeBrokers(brokers)
}

// normalizeBrokerConfig validates the operator's broker config and fills
// transport defaults, returning a copy so the caller's Options are not
// mutated. The secret must be a scheme-prefixed reference — a pasted literal
// is rejected before it is ever processed (same posture as --cred-swap) — and
// its name must satisfy the shared secret-name rules so the guest's
// @secret:<name> reference always parses.
func normalizeBrokerConfig(cfg *vmkit.BrokerConfig) (*vmkit.BrokerConfig, error) {
	if cfg == nil {
		return nil, nil
	}
	out := *cfg
	if strings.TrimSpace(out.Upstream) == "" {
		return nil, fmt.Errorf("broker: upstream URL is required")
	}
	if !secretxfer.ValidName(out.Secret.Name) {
		return nil, fmt.Errorf("broker: secret name %q must be a valid secret name (letters, digits, underscore; not starting with a digit)", out.Secret.Name)
	}
	if !secret.DefaultRegistry(nil, nil).ValidRef(out.Secret.Ref) {
		return nil, fmt.Errorf("broker: secret reference %q must be <scheme>:<ref> (env:/file:/dotenv:/vault:/helper:), never a literal secret", out.Secret.Ref)
	}
	if strings.TrimSpace(out.GuestListen) == "" {
		out.GuestListen = DefaultBrokerGuestListen
	}
	if out.VsockPort == 0 {
		out.VsockPort = DefaultBrokerPort
	}
	return &out, nil
}

// normalizeBrokers validates a set of broker endpoints and fills each one's
// transport defaults so they do not collide: any endpoint that left VsockPort
// or GuestListen zero is assigned the next free slot (ports from
// DefaultBrokerPort upward; guest-listen from DefaultBrokerGuestListen's port
// upward). It returns nil for an empty/nil set (back-compat: no brokers). It
// fails closed on: a duplicate explicit VsockPort or GuestListen, and more
// than one endpoint with Proxy=true (there is a single HTTPS_PROXY slot).
// Each endpoint is otherwise validated exactly as normalizeBrokerConfig does.
func normalizeBrokers(brokers []*vmkit.BrokerConfig) ([]*vmkit.BrokerConfig, error) {
	if len(brokers) == 0 {
		return nil, nil
	}

	out := make([]*vmkit.BrokerConfig, len(brokers))
	usedPorts := make(map[uint32]bool, len(brokers))
	usedListens := make(map[string]bool, len(brokers))
	proxyCount := 0

	// First pass: run the shared per-endpoint validation and record the
	// transport slots endpoints claimed explicitly, so auto-assignment below
	// never picks an already-taken slot.
	for i, cfg := range brokers {
		norm, err := normalizeBrokerConfig(cfg)
		if err != nil {
			return nil, err
		}
		if cfg.VsockPort != 0 {
			usedPorts[norm.VsockPort] = true
		}
		if strings.TrimSpace(cfg.GuestListen) != "" {
			usedListens[norm.GuestListen] = true
		}
		if norm.Proxy {
			proxyCount++
		}
		out[i] = norm
	}
	if proxyCount > 1 {
		return nil, fmt.Errorf("broker: only one endpoint may set proxy=true (got %d)", proxyCount)
	}

	// Second pass: assign transport defaults to endpoints that left them zero,
	// walking forward from the base port/listen and skipping slots already
	// claimed (explicitly or by an earlier auto-assignment in this pass). The
	// vsock-port and guest-listen sequences advance independently: a collision
	// in one namespace must not push the other namespace's next candidate
	// past an offset it could otherwise still use.
	guestHost, guestBasePort, err := net.SplitHostPort(DefaultBrokerGuestListen)
	if err != nil {
		return nil, fmt.Errorf("broker: invalid DefaultBrokerGuestListen %q: %w", DefaultBrokerGuestListen, err)
	}
	basePort, err := strconv.Atoi(guestBasePort)
	if err != nil {
		return nil, fmt.Errorf("broker: invalid DefaultBrokerGuestListen port %q: %w", guestBasePort, err)
	}
	portOffset, listenOffset := 0, 0
	for i, cfg := range brokers {
		norm := out[i]
		if cfg.VsockPort == 0 {
			for {
				candidate := DefaultBrokerPort + uint32(portOffset)
				if !usedPorts[candidate] {
					norm.VsockPort = candidate
					usedPorts[candidate] = true
					break
				}
				portOffset++
			}
		}
		if strings.TrimSpace(cfg.GuestListen) == "" {
			for {
				candidate := net.JoinHostPort(guestHost, strconv.Itoa(basePort+listenOffset))
				if !usedListens[candidate] {
					norm.GuestListen = candidate
					usedListens[candidate] = true
					break
				}
				listenOffset++
			}
		}
	}

	// Final collision check: explicit values (already recorded above) plus
	// assigned values must all be pairwise distinct.
	seenPorts := make(map[uint32]int, len(out))
	seenListens := make(map[string]int, len(out))
	seenBaseURLEnvKeys := make(map[string]int, len(out))
	for i, norm := range out {
		if prev, ok := seenPorts[norm.VsockPort]; ok {
			return nil, fmt.Errorf("broker: endpoints %d and %d both use VsockPort %d", prev, i, norm.VsockPort)
		}
		seenPorts[norm.VsockPort] = i
		if prev, ok := seenListens[norm.GuestListen]; ok {
			return nil, fmt.Errorf("broker: endpoints %d and %d both use GuestListen %q", prev, i, norm.GuestListen)
		}
		seenListens[norm.GuestListen] = i
		for key := range norm.BaseURLEnv {
			if prev, ok := seenBaseURLEnvKeys[key]; ok {
				return nil, fmt.Errorf("broker: endpoints %d and %d both use BaseURLEnv key %q", prev, i, key)
			}
			seenBaseURLEnvKeys[key] = i
		}
	}

	return out, nil
}

func OptionsFromRequest(req vmkit.Request, supervisorPath string) (Options, error) {
	if req.Identity == nil {
		return Options{}, fmt.Errorf("identity is required")
	}
	if req.Config == nil {
		return Options{}, fmt.Errorf("config is required")
	}
	if err := ValidateHostBackend(req.Identity.Backend); err != nil {
		return Options{}, err
	}
	network := vmkit.NetworkConfig{Mode: DefaultNetworkMode}
	if req.Config.Network != nil {
		network = NormalizeNetworkConfig(*req.Config.Network)
	}
	return Options{
		Name:           req.Identity.RuntimeID,
		Backend:        req.Identity.Backend,
		KernelPath:     req.Config.KernelPath,
		StateDir:       req.Config.StateDir,
		SupervisorPath: supervisorPath,
		RestartPolicy:  DefaultRestartPolicy,
		MemoryMiB:      req.Config.MemoryMiB,
		CPUCount:       req.Config.CPUCount,
		Network:        network,
		Mediation:      req.Config.Mediation,
		ShellPort:      req.Config.ShellPort,
		ExecPort:       req.Config.ExecPort,
		Hostname:       req.Config.Hostname,
		Disks:          ConfigDisks(req.Config.Disks),
	}, nil
}

func ConfigDisks(disks []vmkit.Disk) []Disk {
	if len(disks) == 0 {
		return nil
	}
	out := make([]Disk, 0, len(disks))
	for _, disk := range disks {
		out = append(out, Disk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	return out
}

func Supervisor(opts Options) (vmkit.Supervisor, error) {
	if err := ValidateHostBackend(opts.Backend); err != nil {
		return nil, err
	}
	switch opts.Backend {
	case vmkit.BackendLinuxKVM:
		return vmkit.ExecutableSupervisor{Path: FirecrackerSupervisorPath(opts)}, nil
	case vmkit.BackendAppleVF:
		return vmkit.ExecutableSupervisor{Path: opts.SupervisorPath}, nil
	default:
		return nil, fmt.Errorf("unsupported backend: %s", opts.Backend)
	}
}

func FirecrackerSupervisorPath(opts Options) string {
	if opts.SupervisorPath != "" {
		return opts.SupervisorPath
	}
	if path := strings.TrimSpace(os.Getenv("MICROAGENT_FIRECRACKER_SUPERVISOR")); path != "" {
		return path
	}
	if p := resolveInstallPath(FirecrackerSupervisorPathFromExecutable); fileExists(p) {
		return p
	}
	return "microagent-firecracker-supervisor"
}

func FirecrackerSupervisorPathFromExecutable(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	dir := filepath.Dir(executable)
	candidates := []string{
		filepath.Join(dir, "microagent-firecracker-supervisor"),
		filepath.Join(filepath.Clean(filepath.Join(dir, "..", "libexec")), "microagent-firecracker-supervisor"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

// launchCommands are the supervisor commands that actually boot a guest, as
// opposed to inspect/snapshot/control commands that operate on an existing or
// recorded workspace. Egress capture-provider fail-closed validation applies
// only to a launch: a stopped mediated workspace can still be inspected, but it
// cannot boot mediated on a backend with no capture provider.
func isLaunchCommand(command string) bool {
	switch strings.TrimSpace(command) {
	case "run", "prepare", "start":
		return true
	default:
		return false
	}
}

func Dispatch(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	// Fail closed before booting when mediated egress (broker/mitm) is
	// requested but this backend has no capture provider that can cover it
	// (e.g. apple-vf native NAT today). Only launches are gated — inspect,
	// snapshot, and control commands on an existing workspace are not.
	if isLaunchCommand(req.Command) && req.Config != nil {
		networkMode := ""
		if req.Config.Network != nil {
			networkMode = req.Config.Network.Mode
		}
		pol := vmkit.EgressPolicy{Mode: req.Config.EgressMode}
		if err := pol.ValidateForCaptureProvider(opts.Backend, networkMode); err != nil {
			err = contextualDispatchError(opts, req, err)
			return vmkit.Response{Backend: opts.Backend, Error: err.Error()}, err
		}
	}
	supervisor, err := Supervisor(opts)
	if err != nil {
		err = contextualDispatchError(opts, req, err)
		return vmkit.Response{Backend: opts.Backend, Error: err.Error()}, err
	}
	resp, err := supervisor.Do(ctx, req)
	if err == nil {
		return resp, nil
	}
	cause := err
	if detail := strings.TrimSpace(resp.Error); detail != "" && detail != err.Error() {
		cause = supervisorError{detail: detail, cause: err}
	}
	err = contextualDispatchError(opts, req, cause)
	if resp.Backend == "" {
		resp.Backend = opts.Backend
	}
	resp.Error = err.Error()
	return resp, err
}

// supervisorError reports the supervisor's structured error detail as the
// message while keeping the dispatch error reachable via errors.Is/As.
type supervisorError struct {
	detail string
	cause  error
}

func (e supervisorError) Error() string { return e.detail }
func (e supervisorError) Unwrap() error { return e.cause }

func contextualDispatchError(opts Options, req vmkit.Request, cause error) error {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		command = "request"
	}
	name := strings.TrimSpace(opts.Name)
	backend := strings.TrimSpace(opts.Backend)
	stateDir := strings.TrimSpace(opts.StateDir)
	if req.Identity != nil {
		if runtimeID := strings.TrimSpace(req.Identity.RuntimeID); runtimeID != "" {
			name = runtimeID
		}
		if identityBackend := strings.TrimSpace(req.Identity.Backend); identityBackend != "" {
			backend = identityBackend
		}
	}
	if req.Config != nil && stateDir == "" {
		stateDir = strings.TrimSpace(req.Config.StateDir)
	}
	if name == "" {
		name = "unknown"
	}
	if backend == "" {
		backend = "unknown"
	}
	supervisorPath := strings.TrimSpace(opts.SupervisorPath)
	if opts.Backend == vmkit.BackendLinuxKVM {
		supervisorPath = FirecrackerSupervisorPath(opts)
	}
	fields := []string{
		fmt.Sprintf("backend=%s", backend),
	}
	if stateDir != "" {
		fields = append(fields, fmt.Sprintf("state-dir=%s", stateDir))
	}
	if supervisorPath != "" {
		fields = append(fields, fmt.Sprintf("supervisor=%s", supervisorPath))
	}
	return fmt.Errorf("%s workspace %q failed (%s): %w", command, name, strings.Join(fields, " "), cause)
}

func ResultPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "result.json")
}

func ShellCommand(command string) []string {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return []string{"/bin/sh", "-lc", command}
}

func Command(opts Options) string {
	var lines []string
	for _, command := range opts.SetupCommands {
		command = strings.TrimSpace(command)
		if command != "" {
			lines = append(lines, command)
		}
	}
	execCommand := strings.TrimSpace(opts.ExecCommand)
	if execCommand != "" {
		lines = append(lines, execCommand)
	}
	if len(lines) == 0 {
		return ""
	}
	return "set -eu\n" + strings.Join(lines, "\n")
}

func HasGuestCommand(opts Options) bool {
	if strings.TrimSpace(opts.ServiceCommand) != "" {
		return true
	}
	if strings.TrimSpace(opts.ExecCommand) != "" {
		return true
	}
	return HasSetupCommand(opts)
}

func HasSetupCommand(opts Options) bool {
	for _, command := range opts.SetupCommands {
		if strings.TrimSpace(command) != "" {
			return true
		}
	}
	return false
}

func ShellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
