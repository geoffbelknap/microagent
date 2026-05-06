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
	StateUnknown  VMState = "unknown"
	StatePrepared VMState = "prepared"
	StateStarting VMState = "starting"
	StateRunning  VMState = "running"
	StateStopping VMState = "stopping"
	StateStopped  VMState = "stopped"
	StateFailed   VMState = "failed"
)

type Identity struct {
	RequestID string        `json:"requestID"`
	RuntimeID string        `json:"runtimeID"`
	Role      ComponentRole `json:"role"`
	Backend   string        `json:"backend"`
	HomeHash  string        `json:"homeHash,omitempty"`
}

type Config struct {
	KernelPath     string          `json:"kernelPath"`
	RootfsPath     string          `json:"rootfsPath"`
	StateDir       string          `json:"stateDir"`
	MemoryMiB      int             `json:"memoryMiB,omitempty"`
	CPUCount       int             `json:"cpuCount,omitempty"`
	Disks          []Disk          `json:"disks,omitempty"`
	VsockListeners []VsockListener `json:"vsockListeners,omitempty"`
	SerialInput    bool            `json:"serialInput,omitempty"`
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
	BinaryPath              string `json:"binaryPath,omitempty"`
	BinaryVersion           string `json:"binaryVersion,omitempty"`
	KVMAvailable            bool   `json:"kvmAvailable,omitempty"`
	VsockAvailable          bool   `json:"vsockAvailable,omitempty"`
	ConsoleAvailable        bool   `json:"consoleAvailable"`
	ConsoleMode             string `json:"consoleMode,omitempty"`
}

type KernelSupport struct {
	Backend      string `json:"backend"`
	Architecture string `json:"architecture"`
	Path         string `json:"path,omitempty"`
	Status       string `json:"status"`
	SHA256       string `json:"sha256,omitempty"`
	Error        string `json:"error,omitempty"`
}

type Response struct {
	OK      bool           `json:"ok"`
	Backend string         `json:"backend,omitempty"`
	Event   *Event         `json:"event,omitempty"`
	Host    *HostSupport   `json:"host,omitempty"`
	Kernel  *KernelSupport `json:"kernel,omitempty"`
	Error   string         `json:"error,omitempty"`
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
	case "inspect", "stop", "kill", "delete":
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
	if identity.Role != RoleWorkload && identity.Role != RoleEnforcer {
		return fmt.Errorf("identity.role must be %q or %q", RoleWorkload, RoleEnforcer)
	}
	if strings.TrimSpace(identity.Backend) == "" {
		return errors.New("identity.backend is required")
	}
	return nil
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
	return nil
}
