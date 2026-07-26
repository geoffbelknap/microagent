package main

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
)

func parseVsock(raw string) (vmkit.VsockListener, error) {
	left, right, ok := strings.Cut(raw, "=")
	if !ok {
		return vmkit.VsockListener{}, fmt.Errorf("vsock mapping must be port=host:port")
	}
	port, err := strconv.ParseUint(left, 10, 32)
	if err != nil || port == 0 {
		return vmkit.VsockListener{}, fmt.Errorf("vsock port must be a positive uint32")
	}
	if strings.TrimSpace(right) == "" {
		return vmkit.VsockListener{}, fmt.Errorf("vsock target is required")
	}
	return vmkit.VsockListener{Port: uint32(port), Target: right}, nil
}

func parseVsockMappings(raw []string) ([]vmkit.VsockListener, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	listeners := make([]vmkit.VsockListener, 0, len(raw))
	for _, entry := range raw {
		listener, err := parseVsock(entry)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func parseMediationMapping(raw string, optional bool) (vmkit.MediationConfig, error) {
	listener, err := parseVsock(raw)
	if err != nil {
		return vmkit.MediationConfig{}, fmt.Errorf("mediation %w", err)
	}
	mediation := vmkit.MediationConfig{
		Enabled:    true,
		Required:   !optional,
		Port:       listener.Port,
		Target:     listener.Target,
		FailClosed: !optional,
	}
	if err := vmkit.ValidateMediationConfig(mediation); err != nil {
		return vmkit.MediationConfig{}, err
	}
	return mediation, nil
}

func parsePortForward(raw string) (vmkit.PortForward, error) {
	protocol := "tcp"
	if before, after, ok := strings.Cut(raw, "/"); ok {
		raw = before
		protocol = strings.TrimSpace(after)
	}
	parts := strings.Split(raw, ":")
	var host string
	var hostPortText string
	var guestPortText string
	switch len(parts) {
	case 2:
		hostPortText = parts[0]
		guestPortText = parts[1]
	case 3:
		host = parts[0]
		hostPortText = parts[1]
		guestPortText = parts[2]
	default:
		return vmkit.PortForward{}, fmt.Errorf("publish mapping must be [host:]hostPort:guestPort[/tcp]")
	}
	hostPort, err := strconv.ParseUint(strings.TrimSpace(hostPortText), 10, 16)
	if err != nil || hostPort == 0 {
		return vmkit.PortForward{}, fmt.Errorf("publish host port must be a positive uint16")
	}
	guestPort, err := strconv.ParseUint(strings.TrimSpace(guestPortText), 10, 16)
	if err != nil || guestPort == 0 {
		return vmkit.PortForward{}, fmt.Errorf("publish guest port must be a positive uint16")
	}
	forward := vmkit.PortForward{
		Protocol:  protocol,
		Host:      strings.TrimSpace(host),
		HostPort:  uint16(hostPort),
		GuestPort: uint16(guestPort),
	}
	if err := vmkit.ValidateNetworkConfig(vmkit.NetworkConfig{Mode: defaultNetworkMode, PortForwards: []vmkit.PortForward{forward}}); err != nil {
		return vmkit.PortForward{}, err
	}
	return normalizeNetworkConfig(vmkit.NetworkConfig{PortForwards: []vmkit.PortForward{forward}}).PortForwards[0], nil
}

func parsePortForwardMappings(raw []string) ([]vmkit.PortForward, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	forwards := make([]vmkit.PortForward, 0, len(raw))
	seen := map[string]bool{}
	for _, entry := range raw {
		forward, err := parsePortForward(entry)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%s/%d", forward.Protocol, forward.Host, forward.HostPort)
		if seen[key] {
			return nil, fmt.Errorf("duplicate published host port %s", entry)
		}
		seen[key] = true
		forwards = append(forwards, forward)
	}
	return forwards, nil
}

func parseWorkspaceDisks(values []string, bundle bool) ([]workspaceDisk, error) {
	if len(values) == 0 {
		return nil, nil
	}
	disks := make([]workspaceDisk, 0, len(values))
	for _, raw := range values {
		disk, err := parseWorkspaceDisk(raw, bundle)
		if err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func parseWorkspaceDisk(raw string, bundle bool) (workspaceDisk, error) {
	name, rest, ok := strings.Cut(raw, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return workspaceDisk{}, fmt.Errorf("disk must be name=path:/mount:ro|rw")
	}
	if name == "rootfs" {
		return workspaceDisk{}, fmt.Errorf("disk name rootfs is reserved")
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 3 {
		return workspaceDisk{}, fmt.Errorf("disk %q must be path:/mount:ro|rw", name)
	}
	mode := strings.TrimSpace(parts[len(parts)-1])
	mountpoint := strings.TrimSpace(parts[len(parts)-2])
	sourcePath := strings.TrimSpace(strings.Join(parts[:len(parts)-2], ":"))
	if sourcePath == "" {
		return workspaceDisk{}, fmt.Errorf("disk %q path is required", name)
	}
	if mountpoint == "" || !strings.HasPrefix(mountpoint, "/") {
		return workspaceDisk{}, fmt.Errorf("disk %q mountpoint must be absolute", name)
	}
	if mode != "ro" && mode != "rw" {
		return workspaceDisk{}, fmt.Errorf("disk %q mode must be ro or rw", name)
	}
	return workspaceDisk{
		Name:       name,
		SourcePath: sourcePath,
		Path:       sourcePath,
		Mountpoint: mountpoint,
		Mode:       mode,
		Bundle:     bundle,
	}, nil
}

func parseWorkspaceVolumes(values []string) ([]workspaceDisk, error) {
	if len(values) == 0 {
		return nil, nil
	}
	disks := make([]workspaceDisk, 0, len(values))
	for _, raw := range values {
		disk, err := parseWorkspaceVolume(raw)
		if err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func parseWorkspaceVolume(raw string) (workspaceDisk, error) {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 {
		return workspaceDisk{}, fmt.Errorf("volume must be SRC:DST[:ro|rw]")
	}
	// Parse from the right: an optional ro|rw mode, then the guest mountpoint.
	// The source may contain its own colon (a Windows drive-letter path such
	// as C:\data), so everything left of the destination is the source.
	mode := "rw"
	last := strings.TrimSpace(parts[len(parts)-1])
	switch {
	case last == "ro" || last == "rw":
		mode = last
		parts = parts[:len(parts)-1]
	case len(parts) >= 3 && !strings.HasPrefix(last, "/") && strings.HasPrefix(strings.TrimSpace(parts[len(parts)-2]), "/"):
		// SRC:/dst:<something> where <something> is not a guest path.
		return workspaceDisk{}, fmt.Errorf("volume mode must be ro or rw")
	}
	if len(parts) < 2 {
		return workspaceDisk{}, fmt.Errorf("volume must be SRC:DST[:ro|rw]")
	}
	mountpoint := strings.TrimSpace(parts[len(parts)-1])
	sourcePath := strings.TrimSpace(strings.Join(parts[:len(parts)-1], ":"))
	if sourcePath == "" {
		return workspaceDisk{}, fmt.Errorf("volume source path is required")
	}
	if mountpoint == "" || !strings.HasPrefix(mountpoint, "/") {
		return workspaceDisk{}, fmt.Errorf("volume destination must be an absolute guest path")
	}
	if mode != "ro" && mode != "rw" {
		return workspaceDisk{}, fmt.Errorf("volume mode must be ro or rw")
	}
	// A bare name (no path separator or extension, per volume.ValidName) refers
	// to a managed named volume, resolved to its backing ext4 disk at prepare
	// time. This is the in-boundary analog of a docker volume.
	if volume.ValidName(sourcePath) {
		return workspaceDisk{
			Name:          sourcePath,
			Mountpoint:    mountpoint,
			Mode:          mode,
			ManagedVolume: true,
		}, nil
	}
	if info, err := os.Stat(sourcePath); err == nil && info.IsDir() {
		return workspaceDisk{}, fmt.Errorf("MicroAgent does not expose host bind mounts yet; use --bundle with a tar archive, --disk with an ext4 image, or copy files with microagent cp")
	}
	name, err := volumeDiskName(mountpoint)
	if err != nil {
		return workspaceDisk{}, err
	}
	lower := strings.ToLower(sourcePath)
	switch {
	case strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return workspaceDisk{
			Name:       name,
			SourcePath: sourcePath,
			Path:       sourcePath,
			Mountpoint: mountpoint,
			Mode:       mode,
			Bundle:     true,
		}, nil
	case strings.HasSuffix(lower, ".ext4") || strings.HasSuffix(lower, ".img"):
		return workspaceDisk{
			Name:       name,
			SourcePath: sourcePath,
			Path:       sourcePath,
			Mountpoint: mountpoint,
			Mode:       mode,
			Bundle:     false,
		}, nil
	default:
		return workspaceDisk{}, fmt.Errorf("unsupported volume source %q; MicroAgent accepts a managed volume name, a tar archive bundle, or an ext4 disk image, not host bind mounts", sourcePath)
	}
}

func volumeDiskName(mountpoint string) (string, error) {
	name := path.Base(path.Clean(mountpoint))
	if name == "." || name == "/" {
		return "", fmt.Errorf("volume destination must include a mount directory name")
	}
	if name == "rootfs" {
		return "", fmt.Errorf("disk name rootfs is reserved")
	}
	if err := validateSafeBasename("volume-derived disk name", name); err != nil {
		return "", err
	}
	return name, nil
}

func rejectUnsupportedContainerCompatibilityFlags(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := splitFlagArg(arg)
		switch name {
		case "--privileged", "-privileged":
			return fmt.Errorf("--privileged is not supported; MicroAgent runs workloads inside a microVM boundary and does not expose privileged host/container mode")
		case "--pod", "-pod", "--pod-id-file", "-pod-id-file":
			return fmt.Errorf("%s is not supported; MicroAgent does not implement pods, so run one workspace per microVM and keep orchestration outside microagent", name)
		case "--mount", "-mount":
			if !hasValue && i+1 < len(args) {
				value = args[i+1]
			}
			if strings.Contains(value, "type=bind") || strings.Contains(value, "bind") {
				return fmt.Errorf("--mount type=bind is not supported; MicroAgent does not expose host bind mounts, so use -v with a tar archive or ext4 image, --bundle, --disk, microagent cp, or declared --output paths")
			}
			return fmt.Errorf("--mount is not supported; use -v SRC:DST[:ro|rw] with a tar archive or ext4 image, --bundle, or --disk")
		case "--cap-add", "-cap-add", "--cap-drop", "-cap-drop", "--security-opt", "-security-opt", "--device", "-device", "--pid", "-pid", "--ipc", "-ipc", "--userns", "-userns":
			return fmt.Errorf("%s is not supported; MicroAgent exposes a microVM boundary rather than namespace, capability, device, or security-opt controls", name)
		}
	}
	return nil
}

func splitFlagArg(arg string) (name, value string, hasValue bool) {
	if before, after, ok := strings.Cut(arg, "="); ok {
		return before, after, true
	}
	return arg, "", false
}

func newRequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func parseEnvFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("env must be KEY=VALUE: %s", raw)
		}
		if !validEnvName(key) {
			return nil, fmt.Errorf("env key is invalid: %s", key)
		}
		env[key] = value
	}
	return env, nil
}

func parseEgressMode(v string) (string, error) {
	return vmkit.ValidateEgressMode(v)
}

func parseSecretFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	secrets := make(map[string]string, len(values))
	for _, raw := range values {
		name, ref, ok := strings.Cut(raw, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("secret must be NAME=<scheme>:<ref>: %s", raw)
		}
		if !secretxfer.ValidName(name) {
			return nil, fmt.Errorf("secret name is invalid: %s", name)
		}
		if strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("secret reference is empty for %s", name)
		}
		if _, dup := secrets[name]; dup {
			return nil, fmt.Errorf("duplicate secret name: %s", name)
		}
		secrets[name] = ref
	}
	return secrets, nil
}

func validEnvName(key string) bool {
	for i, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z' && i > 0:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return key != ""
}
