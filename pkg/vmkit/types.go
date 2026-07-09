package vmkit

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	BackendAppleVF       = "apple-vf"
	BackendLinuxKVM      = "linux-kvm"
	BackendWindowsHyperV = "windows-hyperv"
)

type ComponentRole string

const (
	RoleWorkload ComponentRole = "workload"
	RoleEnforcer ComponentRole = "enforcer"
)

type VMState string

const (
	StateUnknown     VMState = "unknown"
	StatePrepared    VMState = "prepared"
	StateStarting    VMState = "starting"
	StateRunning     VMState = "running"
	StatePaused      VMState = "paused"
	StateStopping    VMState = "stopping"
	StateStopped     VMState = "stopped"
	StateHalted      VMState = "halted"
	StateQuarantined VMState = "quarantined"
	StateFailed      VMState = "failed"
)

type Identity struct {
	RequestID string        `json:"requestID"`
	RuntimeID string        `json:"runtimeID"`
	Role      ComponentRole `json:"role"`
	Backend   string        `json:"backend"`
	HomeHash  string        `json:"homeHash,omitempty"`
}

// SecretRef declares a secret by name and a scheme-prefixed reference (e.g.
// "vault:secret/data/app#api_key"). References name sources, never values, and
// are safe to persist; the host resolves them at start and never stores values.
type SecretRef struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

type Config struct {
	KernelPath               string           `json:"kernelPath"`
	RootfsPath               string           `json:"rootfsPath"`
	StateDir                 string           `json:"stateDir"`
	AppleVFMachineIdentifier string           `json:"appleVFMachineIdentifier,omitempty"`
	AppleVFNetworkMACAddress string           `json:"appleVFNetworkMACAddress,omitempty"`
	MemoryMiB                int              `json:"memoryMiB,omitempty"`
	CPUCount                 int              `json:"cpuCount,omitempty"`
	Disks                    []Disk           `json:"disks,omitempty"`
	VsockListeners           []VsockListener  `json:"vsockListeners,omitempty"`
	Mediation                *MediationConfig `json:"mediation,omitempty"`
	Network                  *NetworkConfig   `json:"network,omitempty"`
	ShellPort                uint16           `json:"shellPort,omitempty"`
	ExecPort                 uint16           `json:"execPort,omitempty"`
	// SecretsPort is the host vsock port the guest connects to at boot to fetch
	// resolved secrets. Zero means no secrets are delivered.
	SecretsPort uint32 `json:"secretsPort,omitempty"`
	// CACertPort is the host vsock port the guest connects to at boot to fetch
	// the per-workspace egress CA certificate (PEM). Zero means no CA cert is
	// delivered. The CA cert is public; no tmpfs or audit trail is required.
	CACertPort uint32 `json:"caCertPort,omitempty"`
	// Secrets are scheme-prefixed references resolved by the host at start.
	Secrets []SecretRef `json:"secrets,omitempty"`
	// SecretEnvFiles are dotenv file paths whose KEY=VALUE pairs are loaded by
	// the host at start (plaintext, warned). Only paths are persisted.
	SecretEnvFiles []string `json:"secretEnvFiles,omitempty"`
	// OnDemandSecrets are references the host resolves lazily, per fetch, and
	// never materializes to the guest tmpfs.
	OnDemandSecrets []SecretRef `json:"onDemandSecrets,omitempty"`
	// SecretsAudit enables the per-workspace secret-access audit log.
	SecretsAudit bool `json:"secretsAudit,omitempty"`
	// EgressMode controls transparent egress mediation: "strict" forces guest
	// TCP through the mediator with an allowlist; "open" (or empty) leaves
	// networking unmediated. EgressAllow is the destination allowlist.
	EgressMode        string   `json:"egressMode,omitempty"`
	EgressAllow       []string `json:"egressAllow,omitempty"`
	EgressPassthrough []string `json:"egressPassthrough,omitempty"`
	// EgressSwapConfigPath points at the operator credential-swap config the
	// mediator loads (--swap-config). The real secret is injected host-side and
	// never enters the guest; empty disables swap.
	EgressSwapConfigPath string `json:"egressSwapConfigPath,omitempty"`
	// Bounded-operations caps for the egress mediator (ASK tenet 8). All are
	// per-mediator-process (= per-workspace) and reset on restart; a zero value
	// means unlimited (the current, uncapped behavior).
	//   EgressMaxBytesPerSec     rate-limits the upstream-bound copy of each flow.
	//   EgressMaxTotalBytes      caps cumulative egress bytes across tcp+udp; once
	//                            exceeded, the breaching flow is torn down (the
	//                            mediator keeps serving).
	//   EgressMaxConcurrentConns caps concurrently mediated TCP connections
	//                            (refused fail-closed beyond the cap).
	//   EgressAuditMaxBytes      rotates the audit log at this size per active file.
	//   EgressAuditMaxBackups    number of rotated audit-log backups to retain.
	EgressMaxBytesPerSec     int64 `json:"egressMaxBytesPerSec,omitempty"`
	EgressMaxTotalBytes      int64 `json:"egressMaxTotalBytes,omitempty"`
	EgressMaxConcurrentConns int32 `json:"egressMaxConcurrentConns,omitempty"`
	EgressAuditMaxBytes      int64 `json:"egressAuditMaxBytes,omitempty"`
	EgressAuditMaxBackups    int   `json:"egressAuditMaxBackups,omitempty"`
	// SecretsControlPort is the guest vsock port the host connects to (via the
	// firecracker CONNECT protocol) to signal purge/rehydrate around snapshots.
	SecretsControlPort uint32 `json:"secretsControlPort,omitempty"`
	// GuestShellPort/GuestExecPort are the in-guest vsock ports for the shell
	// and structured-exec services when they differ from the host-side ports
	// (ShellPort/ExecPort). A fork resumes a guest that listens on the source's
	// ports while taking its own unique host ports, so the forwarder bridges
	// host ShellPort/ExecPort to these guest ports. Zero means the guest port
	// equals the host port (the normal case).
	GuestShellPort uint16 `json:"guestShellPort,omitempty"`
	GuestExecPort  uint16 `json:"guestExecPort,omitempty"`
	// BakedVsockUDSPath is the vsock UDS path recorded in the snapshot this
	// workspace was started from. Firecracker cannot remap the path on load,
	// so a fork's running VM still references its ancestor's path (resolved
	// through the fork's bind mount). Snapshot capture must carry this exact
	// path forward — recording the fork's own path would point the NEXT
	// fork's bind mount at the wrong directory. Empty means the workspace
	// booted fresh and its vsock path is its own.
	BakedVsockUDSPath string `json:"bakedVsockUDSPath,omitempty"`
	SerialInput       bool   `json:"serialInput,omitempty"`
	TimeoutSeconds    int    `json:"timeoutSeconds,omitempty"`
	// LeaseSeconds bounds the VM's lifetime when set (>0): the gc sweep reaps a
	// workspace still recorded running past StartedAt+LeaseSeconds. Zero means no
	// bound — the VM is permanent and is never reaped for age.
	LeaseSeconds int `json:"leaseSeconds,omitempty"`
	// ModelGuestPort/ModelVsockPort wire a paired host model server to the guest:
	// the guest forwarder listens on TCP 127.0.0.1:ModelGuestPort and tunnels to
	// host vsock ModelVsockPort, which the supervisor proxies to the model server.
	// Both zero means no model is paired.
	ModelGuestPort uint16 `json:"modelGuestPort,omitempty"`
	ModelVsockPort uint32 `json:"modelVsockPort,omitempty"`
	// MaintenanceBoot asks the guest init to serve only the shell and exec
	// channels — no service command, no secrets — so the host can perform
	// guest-mediated file operations against an otherwise-stopped
	// workspace and halt it again.
	MaintenanceBoot bool `json:"maintenanceBoot,omitempty"`
	// Broker configures the egress broker served on a host vsock listener; nil
	// means no broker. See BrokerConfig.
	Broker *BrokerConfig `json:"broker,omitempty"`
}

// BrokerConfig configures the egress broker: a host-side forward proxy served
// on a per-workspace vsock listener that swaps credential references
// (@secret:<name>) for the live secret just before originating its own
// upstream TLS. The guest holds only the reference; the live credential exists
// only in broker process memory and is absent from guest state by
// construction.
type BrokerConfig struct {
	// Upstream is the terminate-mode upstream base URL requests are forwarded
	// to with the credential injected.
	Upstream string `json:"upstream"`
	// Secret is the credential the broker resolves at listener start and holds
	// host-side only. It is deliberately separate from Config.Secrets: broker
	// secrets are never delivered into the guest.
	Secret SecretRef `json:"secret"`
	// GuestListen is the in-guest TCP address the vsock bridge listens on and
	// workloads are pointed at (e.g. "127.0.0.1:18888").
	GuestListen string `json:"guestListen,omitempty"`
	// VsockPort is the host vsock port the broker listener is served on.
	VsockPort uint32 `json:"vsockPort,omitempty"`
	// Proxy sets HTTPS_PROXY/HTTP_PROXY in the guest to the broker so
	// proxy-honoring clients tunnel their egress through it (CONNECT).
	Proxy bool `json:"proxy,omitempty"`
	// BaseURLEnv points per-SDK base-URL env vars at the broker for
	// terminate-mode credential injection; an empty value is filled with the
	// guest listen URL.
	BaseURLEnv map[string]string `json:"baseURLEnv,omitempty"`
}

type Disk struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Mountpoint string `json:"mountpoint"`
	Mode       string `json:"mode"`
}

type VsockListener struct {
	Port   uint32 `json:"port"`
	Target string `json:"target"`
}

type MediationConfig struct {
	Enabled    bool   `json:"enabled" yaml:"enabled"`
	Required   bool   `json:"required" yaml:"required"`
	Port       uint32 `json:"port,omitempty" yaml:"port,omitempty"`
	Target     string `json:"target,omitempty" yaml:"target,omitempty"`
	FailClosed bool   `json:"failClosed" yaml:"failClosed"`
}

type NetworkConfig struct {
	Mode         string         `json:"mode" yaml:"mode"`
	PortForwards []PortForward  `json:"portForwards,omitempty" yaml:"forwards,omitempty"`
	DNS          []string       `json:"dns,omitempty" yaml:"dns,omitempty"`
	Routes       []string       `json:"routes,omitempty" yaml:"routes,omitempty"`
	IP           string         `json:"ip,omitempty" yaml:"ip,omitempty"`
	Subnet       string         `json:"subnet,omitempty" yaml:"subnet,omitempty"`
	Gateway      string         `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	Runtime      *NetworkConfig `json:"runtime,omitempty" yaml:"-"`
}

type PortForward struct {
	Protocol  string `json:"protocol" yaml:"protocol"`
	Host      string `json:"host,omitempty" yaml:"host,omitempty"`
	HostPort  uint16 `json:"hostPort" yaml:"hostPort"`
	GuestPort uint16 `json:"guestPort" yaml:"guestPort"`
}

type Request struct {
	Command  string    `json:"command,omitempty"`
	Identity *Identity `json:"identity,omitempty"`
	Config   *Config   `json:"config,omitempty"`
	// Tag names the snapshot a snapshot/load request operates on.
	Tag string `json:"tag,omitempty"`
}

type Event struct {
	Identity   Identity  `json:"identity"`
	State      VMState   `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
}

type HostSupport struct {
	Backend                 string `json:"backend"`
	Architecture            string `json:"architecture"`
	FrameworkAvailable      bool   `json:"frameworkAvailable"`
	VirtualizationSupported bool   `json:"virtualizationSupported"`
	SupervisorPath          string `json:"supervisorPath,omitempty"`
	SupervisorAvailable     bool   `json:"supervisorAvailable,omitempty"`
	GuestInitPath           string `json:"guestInitPath,omitempty"`
	GuestInitAvailable      bool   `json:"guestInitAvailable,omitempty"`
	BinaryPath              string `json:"binaryPath,omitempty"`
	BinaryVersion           string `json:"binaryVersion,omitempty"`
	KVMAvailable            bool   `json:"kvmAvailable,omitempty"`
	VsockAvailable          bool   `json:"vsockAvailable,omitempty"`
	PauseResumeAvailable    bool   `json:"pauseResumeAvailable,omitempty"`
	SnapshotCreateAvailable bool   `json:"snapshotCreateAvailable,omitempty"`
	SnapshotAvailable       bool   `json:"snapshotAvailable,omitempty"`
	ConsoleAvailable        bool   `json:"consoleAvailable"`
	ConsoleMode             string `json:"consoleMode,omitempty"`
	UserNetworkingAvailable bool   `json:"userNetworkingAvailable,omitempty"`
	UserNetworkingBinary    string `json:"userNetworkingBinary,omitempty"`
	UserNamespacesAvailable bool   `json:"userNamespacesAvailable,omitempty"`
	TunAvailable            bool   `json:"tunAvailable,omitempty"`

	IsolatedNetworkReady bool `json:"isolatedNetworkReady,omitempty"`
	UserNetworkReady     bool `json:"userNetworkReady,omitempty"`

	// EgressTProxyReady reports whether the kernel modules UDP egress mediation
	// (TPROXY) needs are loaded or built-in. Needed for user-mode egress. When
	// false, EgressTProxyMissingModules lists what is absent.
	EgressTProxyReady          bool     `json:"egressTProxyReady,omitempty"`
	EgressTProxyMissingModules []string `json:"egressTProxyMissingModules,omitempty"`

	// ConfinementMode is the host VMM-process confinement posture ("off" until a
	// backend implements it). ConfinementActive is true only when confinement is
	// actually enforced for this host's backend.
	ConfinementMode   string `json:"confinementMode,omitempty"`
	ConfinementActive bool   `json:"confinementActive,omitempty"`
}

type KernelSupport struct {
	Backend      string `json:"backend"`
	Architecture string `json:"architecture"`
	Path         string `json:"path,omitempty"`
	Status       string `json:"status"`
	SHA256       string `json:"sha256,omitempty"`
	Error        string `json:"error,omitempty"`
}

type VerifiedArtifact struct {
	Path           string `json:"path,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	RecordedSHA256 string `json:"recordedSHA256,omitempty"`
	Error          string `json:"error,omitempty"`
}

type VerificationDivergence struct {
	Artifact string `json:"artifact"`
	Field    string `json:"field,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Error    string `json:"error,omitempty"`
}

type RuntimeVerification struct {
	OK          bool                     `json:"ok"`
	ImageRef    string                   `json:"imageRef,omitempty"`
	ResolvedRef string                   `json:"resolvedRef,omitempty"`
	ImageDigest string                   `json:"imageDigest,omitempty"`
	Kernel      *VerifiedArtifact        `json:"kernel,omitempty"`
	Rootfs      *VerifiedArtifact        `json:"rootfs,omitempty"`
	Init        *VerifiedArtifact        `json:"init,omitempty"`
	Divergence  []VerificationDivergence `json:"divergence,omitempty"`
}

type ReadinessSignal struct {
	Ready      bool       `json:"ready"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type RuntimeReadiness struct {
	GuestReady     ReadinessSignal `json:"guestReady"`
	ShellReady     ReadinessSignal `json:"shellReady"`
	ExecReady      ReadinessSignal `json:"execReady"`
	ResultReady    ReadinessSignal `json:"resultReady"`
	MediationReady ReadinessSignal `json:"mediationReady"`
}

type RuntimeResult struct {
	Identity    Identity `json:"identity"`
	Backend     string   `json:"backend,omitempty"`
	ResultPath  string   `json:"resultPath,omitempty"`
	StartedAt   string   `json:"startedAt,omitempty"`
	CompletedAt string   `json:"completedAt,omitempty"`
	ExitCode    int      `json:"exitCode"`
	Stdout      string   `json:"stdout,omitempty"`
	Stderr      string   `json:"stderr,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type ArtifactRef struct {
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

type RuntimeArtifacts struct {
	Ingress []ArtifactRef `json:"ingress,omitempty"`
	Egress  []ArtifactRef `json:"egress,omitempty"`
}

type Response struct {
	OK            bool                 `json:"ok"`
	Backend       string               `json:"backend,omitempty"`
	Event         *Event               `json:"event,omitempty"`
	Host          *HostSupport         `json:"host,omitempty"`
	Kernel        *KernelSupport       `json:"kernel,omitempty"`
	Verification  *RuntimeVerification `json:"verification,omitempty"`
	Readiness     *RuntimeReadiness    `json:"readiness,omitempty"`
	Result        *RuntimeResult       `json:"result,omitempty"`
	Artifacts     *RuntimeArtifacts    `json:"artifacts,omitempty"`
	Mediation     *MediationConfig     `json:"mediation,omitempty"`
	RestartPolicy string               `json:"restartPolicy,omitempty"`
	Network       *NetworkConfig       `json:"network,omitempty"`
	EgressCapture *EgressCaptureReport `json:"egressCapture,omitempty"`
	Error         string               `json:"error,omitempty"`
}

func NormalizeConfig(config *Config) {
	if config == nil {
		return
	}
	if config.MemoryMiB == 0 {
		config.MemoryMiB = 512
	}
	if config.CPUCount == 0 {
		config.CPUCount = 2
	}
}

func ValidateRequest(req Request) error {
	if strings.TrimSpace(req.Command) == "" {
		return errors.New("command is required")
	}
	switch req.Command {
	case "host":
		return nil
	case "check", "prepare", "start", "run", "console", "apply":
		if err := ValidateIdentity(req.Identity); err != nil {
			return err
		}
		if err := ValidateConfig(req.Config); err != nil {
			return err
		}
	case "inspect", "gc", "halt", "quarantine", "pause", "resume", "stop", "kill", "delete":
		if err := ValidateIdentity(req.Identity); err != nil {
			return err
		}
		if req.Config == nil || strings.TrimSpace(req.Config.StateDir) == "" {
			return errors.New("config.stateDir is required")
		}
	case "snapshot":
		if err := ValidateIdentity(req.Identity); err != nil {
			return err
		}
		if req.Config == nil || strings.TrimSpace(req.Config.StateDir) == "" {
			return errors.New("config.stateDir is required")
		}
		if strings.TrimSpace(req.Tag) == "" {
			return errors.New("snapshot tag is required")
		}
		if !SafeIdentifier(req.Tag) {
			return fmt.Errorf("snapshot tag must be a safe basename: %s", req.Tag)
		}
	default:
		return fmt.Errorf("unknown command %q", req.Command)
	}
	return nil
}

func ValidateIdentity(identity *Identity) error {
	if identity == nil {
		return errors.New("identity is required")
	}
	if strings.TrimSpace(identity.RequestID) == "" {
		return errors.New("identity.requestID is required")
	}
	if strings.TrimSpace(identity.RuntimeID) == "" {
		return errors.New("identity.runtimeID is required")
	}
	if !SafeIdentifier(identity.RuntimeID) {
		return fmt.Errorf("identity.runtimeID must be a safe basename: %s", identity.RuntimeID)
	}
	if identity.Role != RoleWorkload && identity.Role != RoleEnforcer {
		return fmt.Errorf("identity.role must be %q or %q", RoleWorkload, RoleEnforcer)
	}
	if strings.TrimSpace(identity.Backend) == "" {
		return errors.New("identity.backend is required")
	}
	return nil
}

func SafeIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, `/\`) && !strings.ContainsRune(value, 0)
}

func ValidateConfig(config *Config) error {
	if config == nil {
		return errors.New("config is required")
	}
	NormalizeConfig(config)
	if strings.TrimSpace(config.KernelPath) == "" {
		return errors.New("config.kernelPath is required")
	}
	if strings.TrimSpace(config.RootfsPath) == "" {
		return errors.New("config.rootfsPath is required")
	}
	if strings.TrimSpace(config.StateDir) == "" {
		return errors.New("config.stateDir is required")
	}
	if config.MemoryMiB <= 0 {
		return errors.New("config.memoryMiB must be positive")
	}
	if config.CPUCount <= 0 {
		return errors.New("config.cpuCount must be positive")
	}
	diskNames := map[string]bool{}
	diskMountpoints := map[string]bool{}
	for _, disk := range config.Disks {
		name := strings.TrimSpace(disk.Name)
		if name == "" {
			return errors.New("disk name is required")
		}
		if name == "rootfs" {
			return errors.New("disk name rootfs is reserved")
		}
		if diskNames[name] {
			return fmt.Errorf("duplicate disk name %q", name)
		}
		diskNames[name] = true
		if strings.TrimSpace(disk.Path) == "" {
			return fmt.Errorf("disk %q path is required", name)
		}
		mountpoint := strings.TrimSpace(disk.Mountpoint)
		if mountpoint == "" {
			return fmt.Errorf("disk %q mountpoint is required", name)
		}
		if !strings.HasPrefix(mountpoint, "/") {
			return fmt.Errorf("disk %q mountpoint must be absolute", name)
		}
		if diskMountpoints[mountpoint] {
			return fmt.Errorf("duplicate disk mountpoint %q", mountpoint)
		}
		diskMountpoints[mountpoint] = true
		if disk.Mode != "ro" && disk.Mode != "rw" {
			return fmt.Errorf("disk %q mode must be ro or rw", name)
		}
	}
	ports := map[uint32]bool{}
	for _, listener := range config.VsockListeners {
		if listener.Port == 0 {
			return errors.New("vsock listener port must be positive")
		}
		if ports[listener.Port] {
			return fmt.Errorf("duplicate vsock listener port %d", listener.Port)
		}
		ports[listener.Port] = true
		if strings.TrimSpace(listener.Target) == "" {
			return fmt.Errorf("vsock listener %d target is required", listener.Port)
		}
	}
	if config.Network != nil {
		if err := ValidateNetworkConfig(*config.Network); err != nil {
			return err
		}
	}
	if config.Mediation != nil {
		if err := ValidateMediationConfig(*config.Mediation); err != nil {
			return err
		}
	}
	return nil
}

// Egress mode constants. An empty/unspecified mode is the secure default and
// resolves to EgressModeGuarded.
const (
	EgressModeGuarded = "guarded"
	EgressModeStrict  = "strict"
	EgressModeOff     = "off"
	// EgressModeBroker terminates guest egress at a forward proxy instead of
	// forging per-SNI certificates: the mediator splices allowed flows opaquely
	// and delivers no CA to the guest. Credential injection happens on the
	// cooperative base-URL vsock channel, not this transparent path. See the
	// P3.5 S2 design.
	EgressModeBroker = "broker"
)

// NormalizeEgressMode collapses an egress mode string to one of the canonical
// values: "guarded", "strict", or "off". Empty/whitespace (and any unrecognized
// value) resolves to "guarded" — the secure default. This is the single
// normalization chokepoint: once applied, downstream code only ever sees the
// three canonical values.
func NormalizeEgressMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case EgressModeStrict:
		return EgressModeStrict
	case EgressModeBroker:
		return EgressModeBroker
	case EgressModeOff:
		return EgressModeOff
	default: // empty, "guarded", or unrecognized -> safe default
		return EgressModeGuarded
	}
}

// EgressMediationOn reports whether the given egress mode provisions the
// mediator (mint CA, spawn mediator, install REDIRECT, allocate the CA-cert
// vsock listener). "guarded" and "strict" are both ON. An
// empty/unspecified mode is OFF here on purpose: "default" is set by
// NormalizeEgressMode at the high-level workspace chokepoints, while
// EgressMediationOn decides whether to *provision*. The low-level raw
// create/start primitive leaves EgressMode empty and must NOT be force-mediated
// (it allocates no CA-cert listener, so mediating it would MITM the guest's TLS
// with a CA the guest never receives).
func EgressMediationOn(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == EgressModeGuarded || m == EgressModeStrict || m == EgressModeBroker
}

// EgressModeForgesCerts reports whether the mode terminates guest TLS by forging
// per-SNI certificates from the per-workspace CA (guarded, strict) — which
// requires delivering that CA to the guest so it trusts the forged leaves.
// broker splices allowed flows opaquely and off runs no mediator, so neither
// delivers a CA. Callers gate CA minting + the CA-cert vsock listener on this.
func EgressModeForgesCerts(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == EgressModeGuarded || m == EgressModeStrict
}

// NetworkModeMediates reports whether the given network mode actually runs the
// egress mediator. Only "user" routes guest egress through the mediator
// (provisionEgressMediation runs for it). "isolated" (no egress at all) never
// starts a mediator, so even with EgressMode=mediated/strict there is no mediator
// to install a CA for. An empty mode resolves to the "user" default — which
// mediates. Used to avoid telling the guest to trust a CA for a mediator that
// will never exist (dead state).
func NetworkModeMediates(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "user":
		return true
	default:
		return false
	}
}

func ValidateMediationConfig(mediation MediationConfig) error {
	if !mediation.Enabled && !mediation.Required && mediation.Port == 0 && strings.TrimSpace(mediation.Target) == "" && !mediation.FailClosed {
		return nil
	}
	if !mediation.Enabled {
		return errors.New("mediation.enabled must be true when mediation is configured")
	}
	if mediation.Port == 0 {
		return errors.New("mediation.port must be positive")
	}
	if strings.TrimSpace(mediation.Target) == "" {
		return errors.New("mediation.target is required")
	}
	if mediation.Required && !mediation.FailClosed {
		return errors.New("required mediation must fail closed")
	}
	return nil
}

func ValidateNetworkConfig(network NetworkConfig) error {
	mode := strings.TrimSpace(network.Mode)
	if mode == "" {
		mode = "user"
	}
	switch mode {
	case "user", "isolated":
	default:
		return fmt.Errorf("network.mode must be user or isolated")
	}
	if mode == "isolated" && len(network.PortForwards) != 0 {
		return fmt.Errorf("network.portForwards require user mode")
	}
	hostPorts := map[string]bool{}
	for i, forward := range network.PortForwards {
		protocol := strings.TrimSpace(forward.Protocol)
		if protocol == "" {
			protocol = "tcp"
		}
		if protocol != "tcp" {
			return fmt.Errorf("network port forward %d protocol must be tcp", i)
		}
		if forward.HostPort == 0 {
			return fmt.Errorf("network port forward %d hostPort must be positive", i)
		}
		if forward.GuestPort == 0 {
			return fmt.Errorf("network port forward %d guestPort must be positive", i)
		}
		key := fmt.Sprintf("%s/%s/%d", protocol, strings.TrimSpace(forward.Host), forward.HostPort)
		if hostPorts[key] {
			return fmt.Errorf("duplicate network host port %d", forward.HostPort)
		}
		hostPorts[key] = true
	}
	return nil
}
