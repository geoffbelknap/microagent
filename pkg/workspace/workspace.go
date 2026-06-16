package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	windowshyperv "github.com/geoffbelknap/microagent/pkg/supervisors/windows_hyperv"
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
	DefaultShellPortBase       = 22000
	DefaultShellPortSpan       = 20000
	DefaultExecPortBase        = 42000
	DefaultExecPortSpan        = 20000
	DefaultTimeout             = 5 * time.Minute
)

// Model pairing transport defaults. The guest forwarder listens on
// 127.0.0.1:DefaultModelGuestPort and tunnels to host vsock DefaultModelVsockPort.
const (
	DefaultModelGuestPort uint16 = 11434
	DefaultModelVsockPort uint32 = 62100
)

// secretsListenerTarget marks the vsock listener that serves resolved secrets.
// Must equal the firecracker supervisor's sentinel.
const secretsListenerTarget = "secrets://serve"

type Options struct {
	Name            string
	ImageRef        string
	ExecCommand     string
	ServiceCommand  string
	Entrypoint      string
	ConsoleShell    string
	Hostname        string
	SetupCommands   []string
	Env             map[string]string
	Secrets         map[string]string // name -> scheme-prefixed reference
	SecretEnvFiles  []string          // dotenv file paths (plaintext, re-read each start)
	OnDemandSecrets map[string]string // name -> reference (lazy, never materialized)
	SecretsAudit    bool              // append every access to the audit log
	Files           []File
	Profile         string
	RestartPolicy   string
	Backend         string
	KernelPath      string
	StateDir        string
	SupervisorPath  string
	GuestInitPath   string
	Mke2fsPath      string
	Architecture    string
	MemoryMiB       int
	CPUCount        int
	SizeMiB         int64
	Network         vmkit.NetworkConfig
	Mediation       *vmkit.MediationConfig
	Health          Health
	Timeout         time.Duration
	ResultPort      uint32
	ShellPort       uint16
	ExecPort        uint16
	GuestShellPort  uint16
	GuestExecPort   uint16
	Disks           []Disk
	Outputs         []Output
	VsockListeners  []vmkit.VsockListener
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
	// FromSnapshot, when set, restores the workspace in place from this snapshot
	// tag instead of booting fresh (start --from-snapshot).
	FromSnapshot    string
	SpecMemory      bool
	SpecCPU         bool
	SpecSize        bool
	Keep            bool
	DryRun          bool
	PrepareForStart bool
	SerialInput     bool
	// MaintenanceBoot boots the guest with only the shell and exec
	// channels (no service command, no secrets) for guest-mediated file
	// operations against an otherwise-stopped workspace.
	MaintenanceBoot bool
	Verification    *vmkit.RuntimeVerification
	Progress        rootfs.ProgressFunc
	UseImageCommand bool
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
}

type NetworkSpec struct {
	Mode         string              `json:"mode,omitempty" yaml:"mode,omitempty"`
	Interface    string              `json:"interface,omitempty" yaml:"interface,omitempty"`
	Name         string              `json:"name,omitempty" yaml:"name,omitempty"`
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
	Name            string                     `json:"name"`
	Profile         string                     `json:"profile,omitempty"`
	Restart         string                     `json:"restart"`
	Resources       Resources                  `json:"resources"`
	Network         NetworkSpec                `json:"network,omitempty"`
	Service         string                     `json:"service_command,omitempty"`
	ConsoleShell    string                     `json:"shell,omitempty"`
	Hostname        string                     `json:"hostname,omitempty"`
	Model           string                     `json:"model,omitempty"`
	ModelRunner     *ModelRunnerSpec           `json:"model_runner,omitempty"`
	ModelMediation  *ModelMediationSpec        `json:"model_mediation,omitempty"`
	Mediation       *vmkit.MediationConfig     `json:"mediation,omitempty"`
	Health          *Health                    `json:"health,omitempty"`
	Disks           []Disk                     `json:"disks,omitempty"`
	Artifacts       Artifacts                  `json:"artifacts,omitempty"`
	Verification    *vmkit.RuntimeVerification `json:"verification,omitempty"`
	Secrets         []vmkit.SecretRef          `json:"secrets,omitempty"`
	SecretEnvFiles  []string                   `json:"secret_env_files,omitempty"`
	OnDemandSecrets []vmkit.SecretRef          `json:"on_demand_secrets,omitempty"`
	SecretsAudit    bool                       `json:"secrets_audit,omitempty"`
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
		return vmkit.BackendFirecracker
	case "windows":
		return vmkit.BackendWindowsHyperV
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
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".microagent", "kernels", backend, arch, "Image")
}

func LegacyKernelPath(backend string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".microagent", "kernels", backend, "Image")
}

func PackagedKernelPath(backend, arch string) string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return PackagedKernelPathFromExecutable(executable, backend, arch)
}

func PackagedKernelPathFromExecutable(executable, backend, arch string) string {
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

func Mke2fsPath() string {
	if path, err := exec.LookPath("mke2fs"); err == nil {
		return path
	}
	if _, err := os.Stat("/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"); err == nil {
		return "/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
	}
	return "mke2fs"
}

func AppleVFSupervisorPath() string {
	if path := strings.TrimSpace(os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR")); path != "" {
		return path
	}
	executable, err := os.Executable()
	if err != nil {
		return "microagent-applevf-supervisor"
	}
	return AppleVFSupervisorPathFromExecutable(executable)
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
	executable, err := os.Executable()
	if err != nil {
		return "microagent-guestinit"
	}
	return GuestInitPathFromExecutable(executable, arch)
}

func GuestInitPathFromExecutable(executable, arch string) string {
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
	return backend == vmkit.BackendAppleVF || backend == vmkit.BackendFirecracker || backend == vmkit.BackendWindowsHyperV
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
		return fmt.Errorf("unknown resource profile %q; choose one of: %s", opts.Profile, strings.Join(ProfileNames(), ", "))
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
		return fmt.Errorf("memory must be positive")
	}
	if resources.CPUCount <= 0 {
		return fmt.Errorf("cpus must be positive")
	}
	if requireDisk && resources.SizeMiB <= 0 {
		return fmt.Errorf("size-mib must be positive")
	}
	if resources.SizeMiB < 0 {
		return fmt.Errorf("size-mib must not be negative")
	}
	return nil
}

func ValidateRestartPolicy(policy string) error {
	switch NormalizeRestartPolicy(policy) {
	case "never", "on-failure", "always":
		return nil
	default:
		return fmt.Errorf("restart policy must be never, on-failure, or always")
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
	network.Interface = strings.TrimSpace(network.Interface)
	network.Name = strings.TrimSpace(network.Name)
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
		Interface:    network.Interface,
		Name:         network.Name,
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
		Interface:    spec.Interface,
		Name:         spec.Name,
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
		MemoryMiB: opts.MemoryMiB,
		CPUCount:  opts.CPUCount,
		SizeMiB:   opts.SizeMiB,
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
	if vmkit.BackendCapabilities(backend).SCSIBlockDevices {
		return SCSIBlockDevice(index)
	}
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
	return "/dev/vd" + name
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

func Request(opts Options, command, rootfsPath string, requestID string) vmkit.Request {
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
	disks := make([]vmkit.Disk, 0, len(opts.Disks))
	for _, disk := range opts.Disks {
		disks = append(disks, vmkit.Disk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	return vmkit.Request{
		Command: command,
		Identity: &vmkit.Identity{
			RequestID: requestID,
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   opts.Backend,
		},
		Config: &vmkit.Config{
			KernelPath:         opts.KernelPath,
			RootfsPath:         rootfsPath,
			StateDir:           opts.StateDir,
			MemoryMiB:          opts.MemoryMiB,
			CPUCount:           opts.CPUCount,
			Disks:              disks,
			VsockListeners:     listeners,
			Mediation:          opts.Mediation,
			Network:            NetworkConfigPtr(opts.Network),
			ShellPort:          ShellPort(opts),
			ExecPort:           ExecPort(opts),
			SecretsPort:        secretsPort,
			Secrets:            secretRefs,
			SecretEnvFiles:     opts.SecretEnvFiles,
			OnDemandSecrets:    onDemandRefsFromOptions(opts),
			SecretsAudit:       opts.SecretsAudit,
			SecretsControlPort: SecretsControlPort(opts),
			GuestShellPort:     opts.GuestShellPort,
			GuestExecPort:      opts.GuestExecPort,
			SerialInput:        opts.SerialInput,
			MaintenanceBoot:    opts.MaintenanceBoot,
			TimeoutSeconds:     int(opts.Timeout.Seconds()),
			ModelGuestPort:     modelGuestPort,
			ModelVsockPort:     modelVsockPort,
		},
	}
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
	case vmkit.BackendFirecracker:
		return vmkit.ExecutableSupervisor{Path: FirecrackerSupervisorPath(opts)}, nil
	case vmkit.BackendAppleVF:
		return vmkit.ExecutableSupervisor{Path: opts.SupervisorPath}, nil
	case vmkit.BackendWindowsHyperV:
		return windowshyperv.Supervisor{Options: windowshyperv.Options{Name: opts.Name, StateDir: opts.StateDir}}, nil
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
	if executable, err := os.Executable(); err == nil {
		path := FirecrackerSupervisorPathFromExecutable(executable)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
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

func Dispatch(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
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
	if opts.Backend == vmkit.BackendFirecracker {
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

// FinalCommandAndMode reports the boot command and mode later starts should
// use after a setup/exec boot, and whether the rootfs build must append a
// guest-config reset for them. Only the combined setup/exec script path of
// BuildCommandAndPort needs the reset; the rootfs builder composes it so the
// rewritten guest env keeps the image env merge.
func FinalCommandAndMode(opts Options) ([]string, string, bool) {
	if !opts.PrepareForStart || !HasGuestCommand(opts) {
		return nil, "", false
	}
	if strings.TrimSpace(opts.ServiceCommand) != "" {
		if !HasSetupCommand(opts) && strings.TrimSpace(opts.ExecCommand) == "" {
			return nil, "", false
		}
		return ShellCommand(opts.ServiceCommand), "managed-service", true
	}
	return ShellCommand(opts.Entrypoint), "", true
}

func BuildCommandAndPort(opts Options) ([]string, uint32) {
	if strings.TrimSpace(opts.ServiceCommand) != "" && !HasSetupCommand(opts) && strings.TrimSpace(opts.ExecCommand) == "" {
		return ShellCommand(opts.ServiceCommand), 0
	}
	if opts.PrepareForStart && !HasGuestCommand(opts) {
		command := ShellCommand(opts.Entrypoint)
		if len(command) == 0 {
			return nil, 0
		}
		return command, opts.ResultPort
	}
	return ShellCommand(Command(opts)), opts.ResultPort
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
