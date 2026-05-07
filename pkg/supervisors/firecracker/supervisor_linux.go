//go:build linux

package firecracker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

type Options struct {
	Name               string
	StateDir           string
	Timeout            time.Duration
	FirecrackerPath    string
	ResolveFirecracker func() (string, error)
}

type Supervisor struct {
	Options Options
}

func (s Supervisor) Do(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	if err := vmkit.ValidateRequest(req); err != nil {
		return vmkit.Response{}, err
	}
	if err := validateFirecrackerRequest(req); err != nil {
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
	opts := s.normalizedOptions(req)
	switch req.Command {
	case "host":
		return hostResponse(opts)
	case "check":
		return vmkit.Response{OK: true, Backend: vmkit.BackendFirecracker}, nil
	case "prepare":
		if err := prepareWorkspace(opts, req); err != nil {
			return failedResponse(req, err.Error()), err
		}
		return eventResponse(req, vmkit.StatePrepared, ""), nil
	case "run":
		return startProcess(ctx, opts, req, false)
	case "start":
		return startProcess(context.Background(), opts, req, true)
	case "inspect":
		return inspectWorkspace(opts)
	case "halt":
		return stopWorkspace(ctx, opts, req, syscall.SIGTERM, vmkit.StateHalted)
	case "quarantine":
		return quarantineWorkspace(opts, req)
	case "stop":
		return stopWorkspace(ctx, opts, req, syscall.SIGTERM, vmkit.StateStopped)
	case "kill":
		return stopWorkspace(ctx, opts, req, syscall.SIGKILL, vmkit.StateStopped)
	case "delete":
		if err := ensureCanDelete(opts); err != nil {
			return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
		}
		cleanupWorkspaceState(opts)
		return eventResponse(req, vmkit.StateStopped, ""), nil
	case "console":
		err := fmt.Errorf("firecracker supervisor console command is unsupported; use serial input FIFO")
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	default:
		err := fmt.Errorf("unknown firecracker command %q", req.Command)
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
}

func validateFirecrackerRequest(req vmkit.Request) error {
	switch req.Command {
	case "check", "prepare", "start", "run", "console":
		return validateFirecrackerConfig(req.Config)
	default:
		return nil
	}
}

func validateFirecrackerConfig(config *vmkit.Config) error {
	if config == nil || config.Network == nil {
		return nil
	}
	mode := strings.TrimSpace(config.Network.Mode)
	if mode == "" {
		mode = "nat"
	}
	switch mode {
	case "nat", "isolated":
		return nil
	case "bridged":
		if strings.TrimSpace(config.Network.Interface) == "" {
			return fmt.Errorf("firecracker network.interface is required for bridged mode")
		}
		return validateLinuxBridge(strings.TrimSpace(config.Network.Interface))
	default:
		return fmt.Errorf("firecracker network.mode %q is unsupported; use nat, isolated, or bridged", mode)
	}
}

func hostResponse(opts Options) (vmkit.Response, error) {
	path := opts.FirecrackerPath
	var resolveErr error
	if path == "" {
		path, resolveErr = opts.ResolveFirecracker()
	}
	resp := vmkit.Response{
		OK:      resolveErr == nil,
		Backend: vmkit.BackendFirecracker,
		Host: &vmkit.HostSupport{
			Backend:                 vmkit.BackendFirecracker,
			Architecture:            runtime.GOARCH,
			BinaryPath:              path,
			BinaryVersion:           binaryVersion(path),
			VirtualizationSupported: fileExists("/dev/kvm"),
			KVMAvailable:            fileExists("/dev/kvm"),
			VsockAvailable:          fileExists("/dev/vhost-vsock"),
			ConsoleAvailable:        true,
			ConsoleMode:             "interactive",
		},
		Kernel: &vmkit.KernelSupport{
			Backend:      vmkit.BackendFirecracker,
			Architecture: runtime.GOARCH,
			Status:       "unknown",
		},
	}
	if resolveErr != nil {
		resp.Error = resolveErr.Error()
		return resp, resolveErr
	}
	return resp, nil
}

func binaryVersion(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (s Supervisor) normalizedOptions(req vmkit.Request) Options {
	opts := s.Options
	if opts.Name == "" && req.Identity != nil {
		opts.Name = req.Identity.RuntimeID
	}
	if opts.StateDir == "" && req.Config != nil {
		opts.StateDir = req.Config.StateDir
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.ResolveFirecracker == nil {
		opts.ResolveFirecracker = ResolveBinary
	}
	return opts
}

func ResolveBinary() (string, error) {
	if path := strings.TrimSpace(os.Getenv("MICROAGENT_FIRECRACKER")); path != "" {
		return path, nil
	}
	if path, err := exec.LookPath("firecracker"); err == nil {
		return path, nil
	}
	if packaged := packagedFirecrackerPath(); packaged != "" {
		if _, err := os.Stat(packaged); err == nil {
			return packaged, nil
		}
	}
	return "", fmt.Errorf("firecracker binary not found")
}

func packagedFirecrackerPath() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "libexec", "firecracker"))
}

type config struct {
	BootSource        bootSource         `json:"boot-source"`
	Drives            []drive            `json:"drives"`
	Machine           machineConfig      `json:"machine-config"`
	Vsock             *vsockConfig       `json:"vsock,omitempty"`
	NetworkInterfaces []networkInterface `json:"network-interfaces,omitempty"`
}

type bootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type machineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	SMT        bool `json:"smt"`
}

type vsockConfig struct {
	VsockID  string `json:"vsock_id"`
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type networkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac"`
	HostDevName string `json:"host_dev_name"`
}

type eventFile struct {
	Identity   vmkit.Identity `json:"identity"`
	State      vmkit.VMState  `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ObservedAt string         `json:"observedAt"`
}

type transientNetworkDevice struct {
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Interface string `json:"interface,omitempty"`
	Created   bool   `json:"created"`
}

type transientFirewallRule struct {
	Table string   `json:"table"`
	Chain string   `json:"chain"`
	Args  []string `json:"args"`
}

type runtimeState struct {
	Event           eventFile                `json:"event"`
	Config          vmkit.Config             `json:"config"`
	PID             int                      `json:"pid,omitempty"`
	PortForwardPID  int                      `json:"portForwardPid,omitempty"`
	NetworkDevices  []transientNetworkDevice `json:"networkDevices,omitempty"`
	FirewallRules   []transientFirewallRule  `json:"firewallRules,omitempty"`
	SerialLogPath   string                   `json:"serialLogPath"`
	SerialInputPath string                   `json:"serialInputPath,omitempty"`
	StartedAt       string                   `json:"startedAt,omitempty"`
	UpdatedAt       string                   `json:"updatedAt"`
	Error           string                   `json:"error,omitempty"`
}

func prepareWorkspace(opts Options, req vmkit.Request) error {
	if err := writeConfig(opts, req); err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return err
	}
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o755); err != nil {
		return err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := serialLog.Close(); err != nil {
		return err
	}
	return writeProcessState(opts, req, vmkit.StatePrepared, 0, "")
}

func startProcess(ctx context.Context, opts Options, req vmkit.Request, detached bool) (vmkit.Response, error) {
	path := opts.FirecrackerPath
	if path == "" {
		resolved, err := opts.ResolveFirecracker()
		if err != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		path = resolved
	}
	networkDevices, firewallRules, runtimeNetwork, err := prepareNetworkForStart(opts, req.Config)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	runtimeReq := requestWithRuntimeNetwork(req, runtimeNetwork)
	if err := writeConfig(opts, runtimeReq); err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o755); err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		return vmkit.Response{}, err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		return vmkit.Response{}, err
	}
	var serialInput *os.File
	if req.Config.SerialInput {
		input, inputErr := openSerialInputFIFO(opts)
		if inputErr != nil {
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, inputErr.Error())
			return failedResponse(req, inputErr.Error()), inputErr
		}
		serialInput = input
	}
	cmd := exec.CommandContext(ctx, path, "--no-api", "--config-file", configPath(opts))
	cmd.Stdout = serialLog
	cmd.Stderr = serialLog
	if serialInput != nil {
		cmd.Stdin = serialInput
	}
	if detached {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	if err := writeProcessStateWithForwarderAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, 0, networkDevices, firewallRules, ""); err != nil {
		_ = cmd.Process.Kill()
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		return vmkit.Response{}, err
	}
	portForwardPID := 0
	if detached && hasPortForwards(req.Config) {
		pid, err := startPortForwarderProcess(opts)
		if err != nil {
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		portForwardPID = pid
		if err := writeProcessStateWithForwarderAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, networkDevices, firewallRules, ""); err != nil {
			_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
			_ = cmd.Process.Kill()
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			return vmkit.Response{}, err
		}
	}
	if detached {
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		_ = cmd.Process.Release()
		return eventResponse(req, vmkit.StateRunning, ""), nil
	}
	waitErr := waitForeground(ctx, cmd, serialLogPath(opts), opts.Timeout)
	inputCloseErr := error(nil)
	if serialInput != nil {
		inputCloseErr = serialInput.Close()
	}
	closeErr := serialLog.Close()
	state := vmkit.StateStopped
	errorText := ""
	if waitErr != nil {
		state = vmkit.StateFailed
		errorText = waitErr.Error()
	}
	cleanupTransientFirewallRules(firewallRules)
	cleanupTransientNetworkDevices(networkDevices)
	if closeErr != nil && errorText == "" {
		state = vmkit.StateFailed
		errorText = closeErr.Error()
	}
	if inputCloseErr != nil && errorText == "" {
		state = vmkit.StateFailed
		errorText = inputCloseErr.Error()
	}
	if err := writeProcessState(opts, runtimeReq, state, 0, errorText); err != nil && waitErr == nil && closeErr == nil && inputCloseErr == nil {
		return vmkit.Response{}, err
	}
	if errorText != "" {
		return failedResponse(req, errorText), fmt.Errorf("%s", errorText)
	}
	return eventResponse(req, vmkit.StateStopped, ""), nil
}

func openSerialInputFIFO(opts Options) (*os.File, error) {
	path := serialInputPath(opts)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create firecracker serial input fifo: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return nil, fmt.Errorf("firecracker serial input path is not a fifo: %s", path)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open firecracker serial input fifo: %w", err)
	}
	return file, nil
}

func stopWorkspace(ctx context.Context, opts Options, req vmkit.Request, signal syscall.Signal, finalState vmkit.VMState) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.PID == 0 {
		if state.PortForwardPID != 0 {
			_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
		}
		cleanupTransientFirewallRules(state.FirewallRules)
		cleanupTransientNetworkDevices(state.NetworkDevices)
		if err := writeProcessState(opts, runtimeStateRequest(req, state), finalState, 0, ""); err != nil {
			return vmkit.Response{}, err
		}
		return eventResponse(req, finalState, ""), nil
	}
	active, err := processActive(state.PID)
	if err != nil {
		return vmkit.Response{}, err
	}
	if active {
		if err := signalProcessGroup(state.PID, signal); err != nil && err != syscall.ESRCH {
			errorText := err.Error()
			_ = writeProcessState(opts, runtimeStateRequest(req, state), vmkit.StateFailed, state.PID, errorText)
			return failedResponse(req, errorText), err
		}
		if err := waitForProcessExit(ctx, state.PID, 5*time.Second); err != nil {
			errorText := err.Error()
			_ = writeProcessState(opts, runtimeStateRequest(req, state), vmkit.StateFailed, state.PID, errorText)
			return failedResponse(req, errorText), err
		}
	}
	if state.PortForwardPID != 0 {
		_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
	}
	cleanupTransientFirewallRules(state.FirewallRules)
	cleanupTransientNetworkDevices(state.NetworkDevices)
	if err := writeProcessState(opts, runtimeStateRequest(req, state), finalState, 0, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, finalState, ""), nil
}

func quarantineWorkspace(opts Options, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.PortForwardPID != 0 {
		_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
	}
	cleanupTransientFirewallRules(state.FirewallRules)
	cleanupTransientNetworkDevices(state.NetworkDevices)
	_ = os.Remove(vsockSocketPath(opts))
	if err := writeProcessStateWithForwarderAndNetwork(opts, runtimeStateRequest(req, state), vmkit.StateQuarantined, state.PID, 0, nil, nil, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, vmkit.StateQuarantined, ""), nil
}

func ensureCanDelete(opts Options) error {
	state, err := readRuntimeState(opts)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if state.PID == 0 {
		return nil
	}
	active, err := processActive(state.PID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("firecracker workspace %s is running; stop or kill it before delete", opts.Name)
	}
	return nil
}

func writeConfig(opts Options, req vmkit.Request) error {
	cfg := config{
		BootSource: bootSource{
			KernelImagePath: req.Config.KernelPath,
			BootArgs:        firecrackerBootArgs(req.Config),
		},
		Drives: []drive{{
			DriveID:      "rootfs",
			PathOnHost:   req.Config.RootfsPath,
			IsRootDevice: true,
			IsReadOnly:   false,
		}},
		Machine: machineConfig{VCPUCount: req.Config.CPUCount, MemSizeMiB: req.Config.MemoryMiB},
	}
	if needsVsock(req.Config) {
		cfg.Vsock = &vsockConfig{
			VsockID:  "vsock0",
			GuestCID: 3,
			UDSPath:  vsockSocketPath(opts),
		}
	}
	if iface, ok := firecrackerNetworkInterface(opts, req.Config); ok {
		cfg.NetworkInterfaces = append(cfg.NetworkInterfaces, iface)
	}
	for _, disk := range req.Config.Disks {
		cfg.Drives = append(cfg.Drives, drive{
			DriveID:      disk.Name,
			PathOnHost:   disk.Path,
			IsRootDevice: false,
			IsReadOnly:   disk.Mode == "ro",
		})
	}
	if err := os.MkdirAll(filepath.Dir(configPath(opts)), 0o755); err != nil {
		return err
	}
	return writeJSONFile(configPath(opts), cfg)
}

func firecrackerBootArgs(config *vmkit.Config) string {
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "pci=off", "root=/dev/vda", "rw", "init=/sbin/microagent-init"}
	if networkMode(config) == "nat" && config != nil && config.Network != nil && config.Network.IP != "" && config.Network.Gateway != "" {
		args = append(args,
			"microagent_net_if=eth0",
			"microagent_net_ip="+config.Network.IP,
			"microagent_net_gw="+config.Network.Gateway,
		)
		if len(config.Network.DNS) != 0 {
			args = append(args, "microagent_net_dns="+strings.Join(config.Network.DNS, ","))
		}
	}
	return strings.Join(args, " ")
}

func needsVsock(config *vmkit.Config) bool {
	if config == nil {
		return false
	}
	if len(config.VsockListeners) != 0 {
		return true
	}
	if config.Mediation != nil && config.Mediation.Enabled {
		return true
	}
	return config.Network != nil && len(config.Network.PortForwards) != 0
}

func hasPortForwards(config *vmkit.Config) bool {
	return config != nil && config.Network != nil && len(config.Network.PortForwards) != 0
}

func firecrackerNetworkInterface(opts Options, config *vmkit.Config) (networkInterface, bool) {
	mode := networkMode(config)
	if mode != "bridged" && mode != "nat" {
		return networkInterface{}, false
	}
	return networkInterface{
		IfaceID:     "eth0",
		GuestMAC:    firecrackerGuestMAC(opts.Name),
		HostDevName: tapName(opts),
	}, true
}

func requestWithRuntimeNetwork(req vmkit.Request, runtimeNetwork *vmkit.NetworkConfig) vmkit.Request {
	if runtimeNetwork == nil {
		return req
	}
	config := *req.Config
	network := *runtimeNetwork
	config.Network = &network
	req.Config = &config
	return req
}

func networkMode(config *vmkit.Config) string {
	if config == nil || config.Network == nil || strings.TrimSpace(config.Network.Mode) == "" {
		return "nat"
	}
	return strings.TrimSpace(config.Network.Mode)
}

func prepareNetworkForStart(opts Options, config *vmkit.Config) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, error) {
	switch networkMode(config) {
	case "isolated":
		return nil, nil, nil, nil
	case "nat":
		return prepareNATForStart(opts, config)
	case "bridged":
	default:
		return nil, nil, nil, fmt.Errorf("firecracker network.mode %q is unsupported; use nat, isolated, or bridged", networkMode(config))
	}
	bridge := strings.TrimSpace(config.Network.Interface)
	if err := validateLinuxBridge(bridge); err != nil {
		return nil, nil, nil, err
	}
	if _, err := exec.LookPath("ip"); err != nil {
		return nil, nil, nil, fmt.Errorf("firecracker bridged networking requires iproute2 'ip': %w", err)
	}
	device := transientNetworkDevice{Name: tapName(opts), Mode: "tap", Interface: bridge, Created: true}
	if err := createBridgeTap(device.Name, bridge); err != nil {
		return nil, nil, nil, err
	}
	return []transientNetworkDevice{device}, nil, nil, nil
}

func prepareNATForStart(opts Options, config *vmkit.Config) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, error) {
	if _, err := exec.LookPath("ip"); err != nil {
		return nil, nil, nil, fmt.Errorf("firecracker nat networking requires iproute2 'ip': %w", err)
	}
	if _, err := exec.LookPath("iptables"); err != nil {
		return nil, nil, nil, fmt.Errorf("firecracker nat networking requires iptables: %w", err)
	}
	if err := requireIPv4Forwarding(); err != nil {
		return nil, nil, nil, err
	}
	subnetOctet, err := allocateNATSubnetOctet(opts)
	if err != nil {
		return nil, nil, nil, err
	}
	tap := tapName(opts)
	subnet := fmt.Sprintf("10.43.%d.0/29", subnetOctet)
	hostIP := fmt.Sprintf("10.43.%d.1", subnetOctet)
	guestIP := fmt.Sprintf("10.43.%d.2", subnetOctet)
	device := transientNetworkDevice{Name: tap, Mode: "tap", Interface: subnet, Created: true}
	if err := runIP("tuntap", "add", "dev", tap, "mode", "tap"); err != nil {
		return nil, nil, nil, networkPrivilegeError("create firecracker nat tap "+tap, err)
	}
	cleanupDevices := []transientNetworkDevice{device}
	if err := runIP("addr", "add", hostIP+"/29", "dev", tap); err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, networkPrivilegeError("assign firecracker nat tap address "+hostIP, err)
	}
	if err := runIP("link", "set", tap, "up"); err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, networkPrivilegeError("bring firecracker nat tap up", err)
	}
	rules, err := installNATFirewallRules(tap, subnet)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, err
	}
	network := runtimeNetworkConfig(config, subnet, guestIP+"/29", hostIP)
	return cleanupDevices, rules, &network, nil
}

func runtimeNetworkConfig(config *vmkit.Config, subnet, ip, gateway string) vmkit.NetworkConfig {
	network := vmkit.NetworkConfig{Mode: "nat"}
	if config != nil && config.Network != nil {
		network = *config.Network
	}
	network.Mode = "nat"
	network.IP = ip
	network.Subnet = subnet
	network.Gateway = gateway
	if len(network.DNS) == 0 {
		network.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}
	network.Routes = []string{"0.0.0.0/0 via " + gateway}
	return network
}

func validateLinuxBridge(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("firecracker network.interface is required for bridged mode")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\x00") || len(name) > 15 {
		return fmt.Errorf("firecracker bridged network.interface %q is not a valid Linux interface name", name)
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("firecracker bridged network.interface %q does not exist", name)
		}
		return fmt.Errorf("inspect firecracker bridged network.interface %q: %w", name, err)
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", name, "bridge")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("firecracker bridged network.interface %q must be a Linux bridge", name)
		}
		return fmt.Errorf("inspect firecracker bridged network.interface %q bridge state: %w", name, err)
	}
	return nil
}

func createBridgeTap(tap, bridge string) error {
	if err := runIP("tuntap", "add", "dev", tap, "mode", "tap"); err != nil {
		return networkPrivilegeError("create firecracker tap "+tap, err)
	}
	if err := runIP("link", "set", tap, "master", bridge); err != nil {
		_ = deleteTap(tap)
		return networkPrivilegeError(fmt.Sprintf("attach firecracker tap %q to bridge %q", tap, bridge), err)
	}
	if err := runIP("link", "set", tap, "up"); err != nil {
		_ = deleteTap(tap)
		return networkPrivilegeError("bring firecracker tap "+tap+" up", err)
	}
	return nil
}

func requireIPv4Forwarding() error {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return fmt.Errorf("inspect net.ipv4.ip_forward for firecracker nat networking: %w", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		return fmt.Errorf("firecracker nat networking requires net.ipv4.ip_forward=1 on the host")
	}
	return nil
}

func allocateNATSubnetOctet(opts Options) (int, error) {
	used := map[int]bool{}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return 0, fmt.Errorf("list host network interfaces for nat subnet allocation: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "magtap") {
			continue
		}
		out, err := exec.Command("ip", "-o", "-4", "addr", "show", "dev", name).Output()
		if err != nil {
			continue
		}
		fields := strings.Fields(string(out))
		for i, field := range fields {
			if field != "inet" || i+1 >= len(fields) {
				continue
			}
			ip, _, err := net.ParseCIDR(fields[i+1])
			if err == nil && ip != nil {
				v4 := ip.To4()
				if len(v4) == net.IPv4len && v4[0] == 10 && v4[1] == 43 {
					used[int(v4[2])] = true
				}
			}
		}
	}
	sum := sha1.Sum([]byte(opts.Name + opts.StateDir))
	start := int(sum[0])%254 + 1
	for offset := 0; offset < 254; offset++ {
		octet := ((start - 1 + offset) % 254) + 1
		if !used[octet] {
			return octet, nil
		}
	}
	return 0, fmt.Errorf("no free firecracker nat subnet in 10.43.1.0/29 through 10.43.254.0/29")
}

func installNATFirewallRules(tap, subnet string) ([]transientFirewallRule, error) {
	if err := ensureFirewallChain("nat", "MICROAGENT-NAT", "POSTROUTING"); err != nil {
		return nil, err
	}
	if err := ensureFirewallChain("filter", "MICROAGENT-FWD", "FORWARD"); err != nil {
		return nil, err
	}
	rules := []transientFirewallRule{
		{Table: "nat", Chain: "MICROAGENT-NAT", Args: []string{"-s", subnet, "!", "-o", tap, "-j", "MASQUERADE"}},
		{Table: "filter", Chain: "MICROAGENT-FWD", Args: []string{"-i", tap, "-s", subnet, "-j", "ACCEPT"}},
		{Table: "filter", Chain: "MICROAGENT-FWD", Args: []string{"-o", tap, "-d", subnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}},
	}
	for i, rule := range rules {
		if err := runIPTables(append([]string{"-t", rule.Table, "-A", rule.Chain}, rule.Args...)...); err != nil {
			cleanupTransientFirewallRules(rules[:i])
			return rules[:i], networkPrivilegeError("configure firecracker nat firewall", err)
		}
	}
	return rules, nil
}

func ensureFirewallChain(table, chain, parent string) error {
	if err := runIPTables("-t", table, "-N", chain); err != nil && !alreadyExistsError(err) {
		return networkPrivilegeError("create firecracker firewall chain "+chain, err)
	}
	if err := runIPTables("-t", table, "-C", parent, "-j", chain); err == nil {
		return nil
	}
	if err := runIPTables("-t", table, "-I", parent, "1", "-j", chain); err != nil {
		return networkPrivilegeError("attach firecracker firewall chain "+chain, err)
	}
	return nil
}

func alreadyExistsError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "chain already exists") || strings.Contains(text, "file exists")
}

func cleanupTransientNetworkDevices(devices []transientNetworkDevice) {
	for _, device := range devices {
		if device.Created && device.Name != "" && device.Mode == "tap" {
			_ = deleteTap(device.Name)
		}
	}
}

func cleanupTransientFirewallRules(rules []transientFirewallRule) {
	for i := len(rules) - 1; i >= 0; i-- {
		rule := rules[i]
		if rule.Table == "" || rule.Chain == "" || len(rule.Args) == 0 {
			continue
		}
		_ = runIPTables(append([]string{"-t", rule.Table, "-D", rule.Chain}, rule.Args...)...)
	}
}

func deleteTap(name string) error {
	return runIP("link", "delete", name)
}

func runIP(args ...string) error {
	cmd := exec.Command("ip", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, text)
	}
	return nil
}

func runIPTables(args ...string) error {
	cmd := exec.Command("iptables", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, text)
	}
	return nil
}

func networkPrivilegeError(action string, err error) error {
	text := strings.ToLower(err.Error())
	if errors.Is(err, syscall.EPERM) || strings.Contains(text, "operation not permitted") || strings.Contains(text, "permission denied") {
		return fmt.Errorf("%s: firecracker nat and bridged networking require CAP_NET_ADMIN to create TAP devices and configure NAT; run with sufficient privileges, setcap cap_net_admin+ep on the supervisor binary, or use --network isolated if outbound network is not needed: %w", action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func tapName(opts Options) string {
	seed := opts.Name
	if seed == "" {
		seed = opts.StateDir
	}
	sum := sha1.Sum([]byte(seed))
	return "magtap" + hexPrefix(sum[:], 8)
}

func firecrackerGuestMAC(name string) string {
	sum := sha1.Sum([]byte(name))
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", sum[0], sum[1], sum[2], sum[3])
}

func hexPrefix(data []byte, n int) string {
	const digits = "0123456789abcdef"
	if n > len(data)*2 {
		n = len(data) * 2
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		b := data[i/2]
		if i%2 == 0 {
			out[i] = digits[b>>4]
		} else {
			out[i] = digits[b&0x0f]
		}
	}
	return string(out)
}

func startPortForwarderProcess(opts Options) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := filepath.Join(opts.StateDir, opts.Name, "port-forward.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(executable, "--port-forwarder", "--state-dir", opts.StateDir, "--name", opts.Name)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return pid, nil
}

func RunPortForwarder(ctx context.Context, opts Options) error {
	state, err := readRuntimeState(opts)
	if err != nil {
		return err
	}
	if state.Config.Network == nil || len(state.Config.Network.PortForwards) == 0 {
		return nil
	}
	listeners := make([]net.Listener, 0, len(state.Config.Network.PortForwards))
	for _, forward := range state.Config.Network.PortForwards {
		if forward.Protocol != "" && forward.Protocol != "tcp" {
			continue
		}
		host := strings.TrimSpace(forward.Host)
		if host == "" {
			host = "127.0.0.1"
		}
		addr := net.JoinHostPort(host, strconv.Itoa(int(forward.HostPort)))
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		fmt.Fprintf(os.Stderr, "forward tcp %s to guest port %d via vsock port %d\n", addr, forward.GuestPort, forward.HostPort)
		listeners = append(listeners, listener)
		go servePortForward(listener, vsockSocketPath(opts), uint32(forward.HostPort))
	}
	<-ctx.Done()
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return ctx.Err()
}

func servePortForward(listener net.Listener, udsPath string, guestPort uint32) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept published tcp connection: %v\n", err)
			return
		}
		go proxyTCPToGuestVsock(conn, udsPath, guestPort)
	}
}

func proxyTCPToGuestVsock(conn net.Conn, udsPath string, guestPort uint32) {
	defer conn.Close()
	vsock, reader, err := dialGuestVsock(udsPath, guestPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect guest vsock port %d: %v\n", guestPort, err)
		return
	}
	defer vsock.Close()
	done := make(chan struct{}, 2)
	go func() {
		if _, err := io.Copy(vsock, conn); err != nil {
			fmt.Fprintf(os.Stderr, "copy published tcp to guest vsock port %d: %v\n", guestPort, err)
		}
		closeWriteConn(vsock)
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(conn, reader); err != nil {
			fmt.Fprintf(os.Stderr, "copy guest vsock port %d to published tcp: %v\n", guestPort, err)
		}
		closeWriteConn(conn)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func dialGuestVsock(udsPath string, guestPort uint32) (net.Conn, *bufio.Reader, error) {
	conn, err := net.DialTimeout("unix", udsPath, 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", guestPort); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	ack, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if !strings.HasPrefix(ack, "OK ") {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("firecracker vsock connect failed: %s", strings.TrimSpace(ack))
	}
	return conn, reader, nil
}

func closeWriteConn(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if writer, ok := conn.(closeWriter); ok {
		_ = writer.CloseWrite()
	}
}

func inspectWorkspace(opts Options) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		event, eventErr := readEvent(opts)
		if eventErr != nil {
			return vmkit.Response{}, err
		}
		return responseFromEvent(event, ""), nil
	}
	return responseFromEvent(state.Event, state.Error), nil
}

func responseFromEvent(file eventFile, errorText string) vmkit.Response {
	event := vmkit.Event{Identity: file.Identity, State: file.State, Detail: file.Detail, ObservedAt: time.Now().UTC()}
	if parsed, err := time.Parse(time.RFC3339, file.ObservedAt); err == nil {
		event.ObservedAt = parsed
	}
	resp := vmkit.Response{OK: file.State != vmkit.StateFailed, Backend: vmkit.BackendFirecracker, Event: &event}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func writeProcessState(opts Options, req vmkit.Request, state vmkit.VMState, pid int, errorText string) error {
	return writeProcessStateWithForwarder(opts, req, state, pid, 0, errorText)
}

func writeProcessStateWithForwarder(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID int, errorText string) error {
	return writeProcessStateWithForwarderAndNetwork(opts, req, state, pid, portForwardPID, nil, nil, errorText)
}

func writeProcessStateWithForwarderAndNetwork(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID int, networkDevices []transientNetworkDevice, firewallRules []transientFirewallRule, errorText string) error {
	if req.Identity == nil || req.Config == nil {
		return fmt.Errorf("workspace request is missing identity or config")
	}
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	now := time.Now().UTC()
	fileEvent := eventFile{
		Identity:   *req.Identity,
		State:      state,
		Detail:     "serial=" + serialLogPath(opts),
		ObservedAt: now.Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "event.json"), fileEvent); err != nil {
		return err
	}
	if err := appendEvent(filepath.Join(dir, "events.json"), fileEvent); err != nil {
		return err
	}
	runtime := runtimeState{
		Event:           fileEvent,
		Config:          *req.Config,
		PID:             pid,
		PortForwardPID:  portForwardPID,
		NetworkDevices:  append([]transientNetworkDevice{}, networkDevices...),
		FirewallRules:   append([]transientFirewallRule{}, firewallRules...),
		SerialLogPath:   serialLogPath(opts),
		SerialInputPath: serialInputPath(opts),
		UpdatedAt:       now.Format(time.RFC3339),
		Error:           errorText,
	}
	if state == vmkit.StateStarting || state == vmkit.StateRunning {
		runtime.StartedAt = now.Format(time.RFC3339)
	}
	return writeJSONFile(filepath.Join(dir, "runtime.json"), runtime)
}

func appendEvent(path string, event eventFile) error {
	var events []eventFile
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && len(bytes.TrimSpace(data)) != 0 {
		if err := json.Unmarshal(data, &events); err != nil {
			return err
		}
	}
	events = append(events, event)
	return writeJSONFile(path, events)
}

func runtimeStateRequest(req vmkit.Request, state runtimeState) vmkit.Request {
	if req.Identity == nil {
		identity := state.Event.Identity
		req.Identity = &identity
	}
	if req.Config == nil {
		config := state.Config
		req.Config = &config
	} else {
		req.Config.KernelPath = state.Config.KernelPath
		req.Config.RootfsPath = state.Config.RootfsPath
		req.Config.MemoryMiB = state.Config.MemoryMiB
		req.Config.CPUCount = state.Config.CPUCount
		req.Config.Disks = state.Config.Disks
		req.Config.VsockListeners = state.Config.VsockListeners
		req.Config.Mediation = state.Config.Mediation
		req.Config.Network = state.Config.Network
		req.Config.SerialInput = state.Config.SerialInput
	}
	return req
}

func readRuntimeState(opts Options) (runtimeState, error) {
	var state runtimeState
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "runtime.json"))
	if err != nil {
		return state, err
	}
	return state, json.Unmarshal(data, &state)
}

func readEvent(opts Options) (eventFile, error) {
	var event eventFile
	data, err := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "event.json"))
	if err != nil {
		return event, err
	}
	return event, json.Unmarshal(data, &event)
}

func waitForeground(ctx context.Context, cmd *exec.Cmd, serialPath string, timeout time.Duration) error {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-waitCh:
			return err
		case <-ticker.C:
			if GuestHalted(serialPath) {
				if err := terminateProcess(cmd.Process, waitCh, 5*time.Second); err != nil {
					return err
				}
				return nil
			}
		case <-timer:
			_ = cmd.Process.Kill()
			<-waitCh
			return fmt.Errorf("firecracker process did not exit before timeout")
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-waitCh
			return ctx.Err()
		}
	}
}

func GuestHalted(serialPath string) bool {
	data, err := os.ReadFile(serialPath)
	if err != nil {
		return false
	}
	log := string(data)
	return strings.Contains(log, "reboot: System halted") ||
		strings.Contains(log, "reboot: Power down")
}

func terminateProcess(process *os.Process, waitCh <-chan error, timeout time.Duration) error {
	if process == nil {
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waitCh:
		return nil
	case <-timer.C:
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-waitCh
		return nil
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, signal); err == nil || err != syscall.ESRCH {
		return err
	}
	return syscall.Kill(pid, signal)
}

func processActive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			return false, nil
		}
		return false, err
	}
	if state, err := linuxProcessState(pid); err == nil && state == "Z" {
		return false, nil
	}
	return true, nil
}

func waitForProcessExit(ctx context.Context, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		active, err := processActive(pid)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process %d did not exit before timeout", pid)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func linuxProcessState(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return "", fmt.Errorf("invalid proc stat for pid %d", pid)
	}
	return fields[2], nil
}

func eventResponse(req vmkit.Request, state vmkit.VMState, errorText string) vmkit.Response {
	event := &vmkit.Event{State: state, ObservedAt: time.Now().UTC()}
	if req.Identity != nil {
		event.Identity = *req.Identity
	}
	if req.Config != nil && req.Identity != nil {
		event.Detail = "serial=" + filepath.Join(req.Config.StateDir, req.Identity.RuntimeID, "serial.log")
	}
	resp := vmkit.Response{OK: state != vmkit.StateFailed, Backend: vmkit.BackendFirecracker, Event: event}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func failedResponse(req vmkit.Request, errorText string) vmkit.Response {
	return eventResponse(req, vmkit.StateFailed, errorText)
}

func configPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "firecracker.json")
}

func serialLogPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "serial.log")
}

func serialInputPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "serial.in")
}

func vsockSocketPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "vsock.sock")
}

func cleanupWorkspaceState(opts Options) {
	if state, err := readRuntimeState(opts); err == nil {
		cleanupTransientFirewallRules(state.FirewallRules)
		cleanupTransientNetworkDevices(state.NetworkDevices)
	}
	_ = os.RemoveAll(filepath.Join(opts.StateDir, "workspaces", opts.Name))
	_ = os.RemoveAll(filepath.Join(opts.StateDir, opts.Name))
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
