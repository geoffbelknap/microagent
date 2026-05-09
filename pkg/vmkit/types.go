package vmkit

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	BackendAppleVF     = "apple-vf"
	BackendFirecracker = "firecracker"
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

type Config struct {
	KernelPath     string           `json:"kernelPath"`
	RootfsPath     string           `json:"rootfsPath"`
	StateDir       string           `json:"stateDir"`
	MemoryMiB      int              `json:"memoryMiB,omitempty"`
	CPUCount       int              `json:"cpuCount,omitempty"`
	Disks          []Disk           `json:"disks,omitempty"`
	VsockListeners []VsockListener  `json:"vsockListeners,omitempty"`
	Mediation      *MediationConfig `json:"mediation,omitempty"`
	Network        *NetworkConfig   `json:"network,omitempty"`
	SerialInput    bool             `json:"serialInput,omitempty"`
	TimeoutSeconds int              `json:"timeoutSeconds,omitempty"`
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
	Interface    string         `json:"interface,omitempty" yaml:"interface,omitempty"`
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
	ConsoleAvailable        bool   `json:"consoleAvailable"`
	ConsoleMode             string `json:"consoleMode,omitempty"`
	UserNetworkingAvailable bool   `json:"userNetworkingAvailable,omitempty"`
	UserNetworkingBinary    string `json:"userNetworkingBinary,omitempty"`
	UserNamespacesAvailable bool   `json:"userNamespacesAvailable,omitempty"`
	TunAvailable            bool   `json:"tunAvailable,omitempty"`
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
	case "check", "prepare", "start", "run", "console":
		if err := ValidateIdentity(req.Identity); err != nil {
			return err
		}
		if err := ValidateConfig(req.Config); err != nil {
			return err
		}
	case "inspect", "halt", "quarantine", "stop", "kill", "delete":
		if err := ValidateIdentity(req.Identity); err != nil {
			return err
		}
		if req.Config == nil || strings.TrimSpace(req.Config.StateDir) == "" {
			return errors.New("config.stateDir is required")
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
	case "user", "nat", "isolated", "bridged":
	default:
		return fmt.Errorf("network.mode must be user, nat, isolated, or bridged")
	}
	if mode == "isolated" && len(network.PortForwards) != 0 {
		return fmt.Errorf("network.portForwards require user, nat, or bridged mode")
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
