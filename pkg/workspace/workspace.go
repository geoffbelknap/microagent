package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/rootfs"
	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

const (
	DefaultWorkspaceImageArm64 = "docker.io/library/busybox@sha256:bd44eb136a95dcc8dc58995e43abc40a413f2e8e3d4a2aae6bccbe94686acb05"
	DefaultWorkspaceImageAMD64 = "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
	DefaultWorkspaceImageOther = "docker.io/library/busybox:1.36.1"
	DefaultWorkspaceMemoryMiB  = 512
	DefaultWorkspaceCPUCount   = 2
	DefaultWorkspaceProfile    = "small"
	DefaultRestartPolicy       = "never"
	DefaultNetworkMode         = "nat"
	DefaultResultPort          = 1024
	DefaultTimeout             = 2 * time.Minute
)

type Options struct {
	Name            string
	ImageRef        string
	ExecCommand     string
	Entrypoint      string
	SetupCommands   []string
	Env             map[string]string
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
	Timeout         time.Duration
	ResultPort      uint32
	Disks           []Disk
	Outputs         []Output
	VsockListeners  []vmkit.VsockListener
	ProfileExplicit bool
	KernelExplicit  bool
	SpecMemory      bool
	SpecCPU         bool
	SpecSize        bool
	Keep            bool
	PrepareForStart bool
	SerialInput     bool
	Verification    *vmkit.RuntimeVerification
}

type Spec struct {
	Name       string                `yaml:"name"`
	ImageRef   string                `yaml:"image"`
	Profile    string                `yaml:"profile"`
	Restart    string                `yaml:"restart"`
	Entrypoint string                `yaml:"entrypoint"`
	Setup      []string              `yaml:"setup"`
	Env        map[string]string     `yaml:"env"`
	Resources  Resources             `yaml:"resources"`
	Network    NetworkSpec           `yaml:"network"`
	Mediation  vmkit.MediationConfig `yaml:"mediation"`
	Disks      []Disk                `yaml:"disks"`
	Bundles    []Disk                `yaml:"bundles"`
	Outputs    []Output              `yaml:"outputs"`
}

type NetworkSpec struct {
	Mode         string              `json:"mode,omitempty" yaml:"mode,omitempty"`
	Interface    string              `json:"interface,omitempty" yaml:"interface,omitempty"`
	PortForwards []vmkit.PortForward `json:"port_forwards,omitempty" yaml:"forwards,omitempty"`
	DNS          []string            `json:"dns,omitempty" yaml:"dns,omitempty"`
	Routes       []string            `json:"routes,omitempty" yaml:"routes,omitempty"`
	IP           string              `json:"ip,omitempty" yaml:"ip,omitempty"`
}

type Disk struct {
	Name       string `json:"name" yaml:"name"`
	SourcePath string `json:"source_path,omitempty" yaml:"sourcePath,omitempty"`
	Path       string `json:"path" yaml:"path"`
	Mountpoint string `json:"mountpoint" yaml:"mountpoint"`
	Mode       string `json:"mode" yaml:"mode"`
	Bundle     bool   `json:"bundle,omitempty" yaml:"bundle,omitempty"`
}

type Output struct {
	Name string `json:"name" yaml:"name"`
	Path string `json:"path" yaml:"path"`
}

type Artifacts struct {
	Ingress []Disk   `json:"ingress,omitempty"`
	Egress  []Output `json:"egress,omitempty"`
}

type Manifest struct {
	Name         string                     `json:"name"`
	Profile      string                     `json:"profile,omitempty"`
	Restart      string                     `json:"restart"`
	Resources    Resources                  `json:"resources"`
	Network      NetworkSpec                `json:"network,omitempty"`
	Mediation    *vmkit.MediationConfig     `json:"mediation,omitempty"`
	Disks        []Disk                     `json:"disks,omitempty"`
	Artifacts    Artifacts                  `json:"artifacts,omitempty"`
	Verification *vmkit.RuntimeVerification `json:"verification,omitempty"`
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
	opts.SupervisorPath = os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR")
	_ = ApplyProfile(&opts, false, false, false)
	return opts
}

func HostBackend() string {
	if runtime.GOOS == "darwin" {
		return vmkit.BackendAppleVF
	}
	return vmkit.BackendFirecracker
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
	return backend == vmkit.BackendAppleVF || backend == vmkit.BackendFirecracker
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
		PortForwards: append([]vmkit.PortForward{}, network.PortForwards...),
		DNS:          append([]string{}, network.DNS...),
		Routes:       append([]string{}, network.Routes...),
		IP:           network.IP,
	}
}

func NetworkConfigFromSpec(spec NetworkSpec) vmkit.NetworkConfig {
	return NormalizeNetworkConfig(vmkit.NetworkConfig{
		Mode:         spec.Mode,
		Interface:    spec.Interface,
		PortForwards: append([]vmkit.PortForward{}, spec.PortForwards...),
		DNS:          append([]string{}, spec.DNS...),
		Routes:       append([]string{}, spec.Routes...),
		IP:           spec.IP,
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
	if len(disks) == 0 {
		return nil
	}
	mounts := make([]rootfs.Mount, 0, len(disks))
	for idx, disk := range disks {
		mounts = append(mounts, rootfs.Mount{
			Device:     VirtioBlockDevice(idx + 1),
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	return mounts
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

func Request(opts Options, command, rootfsPath string, requestID string) vmkit.Request {
	var listeners []vmkit.VsockListener
	if opts.ResultPort != 0 {
		listeners = []vmkit.VsockListener{{Port: opts.ResultPort, Target: ResultPath(opts.StateDir, opts.Name)}}
	}
	listeners = append(listeners, opts.VsockListeners...)
	if opts.Mediation != nil && opts.Mediation.Enabled {
		listeners = append(listeners, vmkit.VsockListener{Port: opts.Mediation.Port, Target: opts.Mediation.Target})
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
			KernelPath:     opts.KernelPath,
			RootfsPath:     rootfsPath,
			StateDir:       opts.StateDir,
			MemoryMiB:      opts.MemoryMiB,
			CPUCount:       opts.CPUCount,
			Disks:          disks,
			VsockListeners: listeners,
			Mediation:      opts.Mediation,
			Network:        NetworkConfigPtr(opts.Network),
			SerialInput:    opts.SerialInput,
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
	switch opts.Backend {
	case vmkit.BackendFirecracker:
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
	return "microagent-firecracker-supervisor"
}

func Dispatch(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	supervisor, err := Supervisor(opts)
	if err != nil {
		return vmkit.Response{Backend: opts.Backend, Error: err.Error()}, err
	}
	return supervisor.Do(ctx, req)
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
	if opts.PrepareForStart {
		lines = append(lines, ResetGuestConfigCommand(ShellCommand(opts.Entrypoint), opts.Env, 0, Mounts(opts.Disks), RootfsPortForwards(opts.Network.PortForwards)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "set -eu\n" + strings.Join(lines, "\n")
}

func BuildCommandAndPort(opts Options) ([]string, uint32) {
	if opts.PrepareForStart && !HasGuestCommand(opts) {
		return ShellCommand(opts.Entrypoint), 0
	}
	return ShellCommand(Command(opts)), opts.ResultPort
}

func ResetGuestConfigCommand(command []string, env map[string]string, port uint32, mounts []rootfs.Mount, forwards []rootfs.PortForward) string {
	if command == nil {
		command = []string{}
	}
	data, err := json.Marshal(struct {
		Command      []string             `json:"command"`
		Env          []string             `json:"env,omitempty"`
		Port         uint32               `json:"port"`
		Mounts       []rootfs.Mount       `json:"mounts,omitempty"`
		HostForwards []rootfs.PortForward `json:"hostForwards,omitempty"`
	}{
		Command:      command,
		Env:          envList(env),
		Port:         port,
		Mounts:       mounts,
		HostForwards: forwards,
	})
	if err != nil {
		panic(err)
	}
	return "printf '%s\\n' " + ShellSingleQuote(string(data)) + " > /etc/microagent/run.json"
}

func HasGuestCommand(opts Options) bool {
	if strings.TrimSpace(opts.ExecCommand) != "" {
		return true
	}
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

func envList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		if validEnvName(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
