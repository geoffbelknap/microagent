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

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/google/nftables/userdata"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
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
	case "apply":
		return applyWorkspaceConfig(opts, req)
	case "inspect":
		return inspectWorkspace(opts)
	case "halt":
		return stopWorkspace(ctx, opts, req, syscall.SIGTERM, vmkit.StateHalted)
	case "quarantine":
		return quarantineWorkspace(opts, req)
	case "pause":
		return pauseWorkspace(ctx, opts, req)
	case "resume":
		return resumeWorkspace(ctx, opts, req)
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
	case "check", "prepare", "start", "run", "console", "apply":
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
		mode = "user"
	}
	switch mode {
	case "user", "nat", "isolated":
		return nil
	case "bridged":
		if strings.TrimSpace(config.Network.Interface) == "" {
			return fmt.Errorf("firecracker network.interface is required for bridged mode")
		}
		return validateLinuxBridge(strings.TrimSpace(config.Network.Interface))
	default:
		return fmt.Errorf("firecracker network.mode %q is unsupported; use user, nat, isolated, or bridged", mode)
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
	if req.Command == "run" && os.Getenv(userNetworkDisableRunTimeoutEnv) == "1" {
		opts.Timeout = -1
	}
	if opts.Timeout == 0 && req.Config != nil && req.Config.TimeoutSeconds > 0 {
		opts.Timeout = time.Duration(req.Config.TimeoutSeconds) * time.Second
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
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
	PID       int    `json:"pid,omitempty"`
}

type transientFirewallRule struct {
	Family  string   `json:"family,omitempty"`
	Table   string   `json:"table"`
	Chain   string   `json:"chain"`
	Comment string   `json:"comment,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type runtimeState struct {
	Event            eventFile                `json:"event"`
	Config           vmkit.Config             `json:"config"`
	PID              int                      `json:"pid,omitempty"`
	PortForwardPID   int                      `json:"portForwardPid,omitempty"`
	VsockListenerPID int                      `json:"vsockListenerPid,omitempty"`
	NetworkDevices   []transientNetworkDevice `json:"networkDevices,omitempty"`
	FirewallRules    []transientFirewallRule  `json:"firewallRules,omitempty"`
	SerialLogPath    string                   `json:"serialLogPath"`
	SerialInputPath  string                   `json:"serialInputPath,omitempty"`
	StartedAt        string                   `json:"startedAt,omitempty"`
	UpdatedAt        string                   `json:"updatedAt"`
	Readiness        vmkit.RuntimeReadiness   `json:"readiness,omitempty"`
	Error            string                   `json:"error,omitempty"`
}

type guestResult struct {
	StartedAt string `json:"started_at"`
	ExitedAt  string `json:"exited_at"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
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
	if networkMode(req.Config) == "user" && !insideUserNetworkNamespace() {
		return startUserNetworkProcess(ctx, opts, req, detached)
	}
	path := opts.FirecrackerPath
	if path == "" {
		resolved, err := opts.ResolveFirecracker()
		if err != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		path = resolved
	}
	_, needsNetworkCapabilities := firecrackerNetworkInterface(opts, req.Config)
	needsAmbientNetworkCapabilities := needsNetworkCapabilities && networkMode(req.Config) != "user"
	if needsAmbientNetworkCapabilities {
		if err := ensureNetAdminInheritable(); err != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
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
	var vsockListeners *vsockListenerSet
	if !detached && !insideUserNetworkNamespace() {
		vsockListeners, err = startVsockListeners(opts, runtimeReq.Config)
		if err != nil {
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		defer vsockListeners.Close()
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
	// Firecracker refuses to start if the API socket already exists.
	if err := os.Remove(apiSocketPath(opts)); err != nil && !os.IsNotExist(err) {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		_ = serialLog.Close()
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	// Boot from the config file and additionally expose the API socket so
	// pause/resume and snapshot can control the running VM. Only --no-api would
	// disable the API.
	cmd := exec.CommandContext(ctx, path, "--api-sock", apiSocketPath(opts), "--config-file", configPath(opts))
	cmd.Stdout = serialLog
	cmd.Stderr = serialLog
	if serialInput != nil {
		cmd.Stdin = serialInput
	}
	cmd.SysProcAttr = firecrackerSysProcAttr(detached, needsAmbientNetworkCapabilities)
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
	vsockListenerPID := 0
	if detached && hasVsockListeners(req.Config) {
		pid, err := startVsockListenerProcess(opts)
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
		vsockListenerPID = pid
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, vsockListenerPID, networkDevices, firewallRules, ""); err != nil {
			_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
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
	if detached && needsPortForwarder(req.Config) {
		pid, err := startPortForwarderProcess(opts)
		if err != nil {
			if vsockListenerPID != 0 {
				_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
			}
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
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, vsockListenerPID, networkDevices, firewallRules, ""); err != nil {
			_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
			if vsockListenerPID != 0 {
				_ = signalProcessGroup(vsockListenerPID, syscall.SIGTERM)
			}
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
		if err := detachedStartExitError(cmd, 500*time.Millisecond); err != nil {
			if portForwardPID != 0 {
				_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
			}
			if vsockListenerPID != 0 {
				terminateAuxProcess(vsockListenerPID)
			}
			cleanupTransientFirewallRules(firewallRules)
			cleanupTransientNetworkDevices(networkDevices)
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			errorText := fmt.Sprintf("%s; serial log: %s", err.Error(), serialLogPath(opts))
			_ = writeProcessState(opts, runtimeReq, vmkit.StateFailed, 0, errorText)
			return failedResponse(req, errorText), fmt.Errorf("%s", errorText)
		}
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
		if state.VsockListenerPID != 0 {
			terminateAuxProcess(state.VsockListenerPID)
		}
		cleanupTransientFirewallRules(state.FirewallRules)
		cleanupTransientNetworkDevices(state.NetworkDevices)
		cleanupUserNetworkProcess(opts)
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
	if state.VsockListenerPID != 0 {
		terminateAuxProcess(state.VsockListenerPID)
	}
	cleanupTransientFirewallRules(state.FirewallRules)
	cleanupTransientNetworkDevices(state.NetworkDevices)
	cleanupUserNetworkProcess(opts)
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
	if state.VsockListenerPID != 0 {
		terminateAuxProcess(state.VsockListenerPID)
	}
	cleanupTransientFirewallRules(state.FirewallRules)
	cleanupTransientNetworkDevices(state.NetworkDevices)
	cleanupUserNetworkProcess(opts)
	_ = os.Remove(vsockSocketPath(opts))
	_ = os.Remove(serialInputPath(opts))
	if err := writeProcessStateWithForwarderAndNetwork(opts, runtimeStateRequest(req, state), vmkit.StateQuarantined, state.PID, 0, nil, nil, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, vmkit.StateQuarantined, ""), nil
}

// vmStateController issues runtime state transitions over a running VM's API
// unix socket. It is satisfied by *apiClient and is a package variable so unit
// tests can substitute a fake without a live Firecracker process.
type vmStateController interface {
	patchVMState(ctx context.Context, state string) error
}

var newVMStateController = func(socketPath string) vmStateController {
	return newAPIClient(socketPath)
}

func pauseWorkspace(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	return transitionVMState(ctx, opts, req, "Paused", vmkit.StateRunning, vmkit.StatePaused)
}

func resumeWorkspace(ctx context.Context, opts Options, req vmkit.Request) (vmkit.Response, error) {
	return transitionVMState(ctx, opts, req, "Resumed", vmkit.StatePaused, vmkit.StateRunning)
}

// transitionVMState pauses or resumes the running VM over its API socket. It
// requires the workspace to be in fromState, issues the PATCH /vm transition,
// and persists toState while preserving the host-side aux processes (port
// forwarder, vsock listener, transient network) so resume keeps working. The
// VM process is untouched; only its vCPUs are frozen or thawed.
func transitionVMState(ctx context.Context, opts Options, req vmkit.Request, apiState string, fromState, toState vmkit.VMState) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.Event.State != fromState {
		err := fmt.Errorf("firecracker workspace %s is %s; %s requires state %s", opts.Name, state.Event.State, req.Command, fromState)
		return failedResponse(req, err.Error()), err
	}
	if state.PID == 0 {
		err := fmt.Errorf("firecracker workspace %s has no running VM process to %s", opts.Name, req.Command)
		return failedResponse(req, err.Error()), err
	}
	active, err := processActive(state.PID)
	if err != nil {
		return vmkit.Response{}, err
	}
	if !active {
		err := fmt.Errorf("firecracker workspace %s VM process %d is not running", opts.Name, state.PID)
		return failedResponse(req, err.Error()), err
	}
	if err := newVMStateController(apiSocketPath(opts)).patchVMState(ctx, apiState); err != nil {
		// The PATCH is atomic; on failure the VM stays in fromState, so leave the
		// persisted state untouched rather than recording a spurious failure.
		return failedResponse(req, err.Error()), err
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeStateRequest(req, state), toState, state.PID, state.PortForwardPID, state.VsockListenerPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, toState, ""), nil
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
		if state.Event.State == vmkit.StateStarting || state.Event.State == vmkit.StateRunning {
			return fmt.Errorf("firecracker workspace %s is running; stop or kill it before delete", opts.Name)
		}
		if state.PortForwardPID != 0 {
			active, err := processActive(state.PortForwardPID)
			if err != nil {
				return err
			}
			if active {
				return fmt.Errorf("firecracker workspace %s port forwarder is running; stop or kill it before delete", opts.Name)
			}
		}
		if state.VsockListenerPID != 0 {
			active, err := processActive(state.VsockListenerPID)
			if err != nil {
				return err
			}
			if active {
				return fmt.Errorf("firecracker workspace %s vsock listener is running; stop or kill it before delete", opts.Name)
			}
		}
		if active, err := userNetworkProcessActive(opts); err != nil {
			return err
		} else if active {
			return fmt.Errorf("firecracker workspace %s user network process is running; stop or kill it before delete", opts.Name)
		}
		return nil
	}
	active, err := processActive(state.PID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("firecracker workspace %s is running; stop or kill it before delete", opts.Name)
	}
	if active, err := userNetworkProcessActive(opts); err != nil {
		return err
	} else if active {
		return fmt.Errorf("firecracker workspace %s user network process is running; stop or kill it before delete", opts.Name)
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
		if err := os.Remove(vsockSocketPath(opts)); err != nil && !os.IsNotExist(err) {
			return err
		}
		cfg.Vsock = &vsockConfig{
			VsockID:  "vsock0",
			GuestCID: firecrackerGuestCID(opts),
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
	if config != nil && config.ShellPort != 0 {
		args = append(args, fmt.Sprintf("microagent_shell_port=%d", config.ShellPort))
	}
	if config != nil && config.ExecPort != 0 {
		args = append(args, fmt.Sprintf("microagent_exec_port=%d", config.ExecPort))
	}
	if (networkMode(config) == "nat" || networkMode(config) == "user") && config != nil && config.Network != nil && config.Network.IP != "" && config.Network.Gateway != "" {
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
	if config.ExecPort != 0 {
		return true
	}
	return config.Network != nil && len(config.Network.PortForwards) != 0
}

func hasPortForwards(config *vmkit.Config) bool {
	return config != nil && config.Network != nil && len(config.Network.PortForwards) != 0
}

func needsPortForwarder(config *vmkit.Config) bool {
	return hasPortForwards(config) || (config != nil && (config.ShellPort != 0 || config.ExecPort != 0))
}

func applyWorkspaceConfig(opts Options, req vmkit.Request) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
	if state.Event.State != vmkit.StateRunning {
		err := fmt.Errorf("firecracker apply only live-reloads running workspaces")
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
	if !sameGuestPortForwardShape(networkPortForwards(state.Config.Network), networkPortForwards(req.Config.Network)) {
		err := fmt.Errorf("firecracker apply can only live-reload host bind changes for existing port forwards; stop and start the workspace for port, guest port, protocol, or network mode changes")
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
	if state.PortForwardPID != 0 {
		_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
		state.PortForwardPID = 0
	}
	state.Config.Network = req.Config.Network
	runtimeReq := runtimeStateRequest(req, state)
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
	if needsPortForwarder(req.Config) {
		pid, err := startPortForwarderProcess(opts)
		if err != nil {
			_ = writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.NetworkDevices, state.FirewallRules, err.Error())
			return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
		}
		state.PortForwardPID = pid
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, pid, state.VsockListenerPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
			_ = signalProcessGroup(pid, syscall.SIGTERM)
			return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
		}
	}
	return responseFromRuntimeState(opts, state), nil
}

func networkPortForwards(network *vmkit.NetworkConfig) []vmkit.PortForward {
	if network == nil {
		return nil
	}
	return network.PortForwards
}

func sameGuestPortForwardShape(oldForwards, newForwards []vmkit.PortForward) bool {
	if len(oldForwards) != len(newForwards) {
		return false
	}
	for i := range oldForwards {
		oldForward := oldForwards[i]
		newForward := newForwards[i]
		oldProtocol := strings.TrimSpace(oldForward.Protocol)
		if oldProtocol == "" {
			oldProtocol = "tcp"
		}
		newProtocol := strings.TrimSpace(newForward.Protocol)
		if newProtocol == "" {
			newProtocol = "tcp"
		}
		if oldProtocol != newProtocol || oldForward.HostPort != newForward.HostPort || oldForward.GuestPort != newForward.GuestPort {
			return false
		}
	}
	return true
}

func hasVsockListeners(config *vmkit.Config) bool {
	return config != nil && len(config.VsockListeners) != 0
}

func firecrackerNetworkInterface(opts Options, config *vmkit.Config) (networkInterface, bool) {
	mode := networkMode(config)
	if mode != "bridged" && mode != "nat" && mode != "user" {
		return networkInterface{}, false
	}
	return networkInterface{
		IfaceID:     "eth0",
		GuestMAC:    firecrackerGuestMAC(opts.Name),
		HostDevName: tapName(opts),
	}, true
}

func firecrackerSysProcAttr(detached, needsNetworkCapabilities bool) *syscall.SysProcAttr {
	if !detached && !needsNetworkCapabilities {
		return nil
	}
	attr := &syscall.SysProcAttr{Setpgid: detached}
	if needsNetworkCapabilities {
		attr.AmbientCaps = []uintptr{uintptr(unix.CAP_NET_ADMIN)}
	}
	return attr
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
		return "user"
	}
	return strings.TrimSpace(config.Network.Mode)
}

func prepareNetworkForStart(opts Options, config *vmkit.Config) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, error) {
	switch networkMode(config) {
	case "isolated":
		return nil, nil, nil, nil
	case "user":
		return prepareUserNetworkForStart(opts, config)
	case "nat":
		return prepareNATForStart(opts, config)
	case "bridged":
	default:
		return nil, nil, nil, fmt.Errorf("firecracker network.mode %q is unsupported; use user, nat, isolated, or bridged", networkMode(config))
	}
	bridge := strings.TrimSpace(config.Network.Interface)
	if err := validateLinuxBridge(bridge); err != nil {
		return nil, nil, nil, err
	}
	device := transientNetworkDevice{Name: tapName(opts), Mode: "tap", Interface: bridge, Created: true}
	if err := createBridgeTap(device.Name, bridge); err != nil {
		return nil, nil, nil, err
	}
	return []transientNetworkDevice{device}, nil, nil, nil
}

func prepareNATForStart(opts Options, config *vmkit.Config) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, error) {
	if err := requireIPv4Forwarding(); err != nil {
		return nil, nil, nil, err
	}
	return prepareTAPNATForStart(opts, config, "nat")
}

func prepareUserNetworkForStart(opts Options, config *vmkit.Config) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, error) {
	if !insideUserNetworkNamespace() {
		return nil, nil, nil, fmt.Errorf("firecracker user networking must run inside a pasta user network namespace")
	}
	if err := enableNamespaceIPv4Forwarding(); err != nil {
		return nil, nil, nil, err
	}
	devices, rules, network, err := prepareTAPNATForStart(opts, config, "user")
	if err != nil {
		return nil, nil, nil, err
	}
	return attachUserNetworkPID(devices), rules, network, nil
}

func prepareTAPNATForStart(opts Options, config *vmkit.Config, mode string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, error) {
	plan, err := tapNATAddressPlan(opts, config)
	if err != nil {
		return nil, nil, nil, err
	}
	tap := tapName(opts)
	device := transientNetworkDevice{Name: tap, Mode: "tap", Interface: plan.Subnet, Created: true}
	if err := createTap(tap); err != nil {
		return nil, nil, nil, networkPrivilegeError("create firecracker nat tap "+tap, err)
	}
	cleanupDevices := []transientNetworkDevice{device}
	link, err := netlink.LinkByName(tap)
	if err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, networkPrivilegeError("inspect firecracker nat tap "+tap, err)
	}
	addr, err := netlink.ParseAddr(plan.HostCIDR)
	if err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, fmt.Errorf("parse firecracker nat tap address %s: %w", plan.HostCIDR, err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil && !alreadyExistsError(err) {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, networkPrivilegeError("assign firecracker nat tap address "+plan.HostCIDR, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, networkPrivilegeError("bring firecracker nat tap up", err)
	}
	rules, err := installNATFirewallRules(tap, plan.Subnet)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, err
	}
	network := runtimeNetworkConfig(config, plan.Subnet, plan.GuestCIDR, plan.Gateway)
	network.Mode = mode
	return cleanupDevices, rules, &network, nil
}

type tapNATAddress struct {
	Subnet    string
	GuestCIDR string
	Gateway   string
	HostCIDR  string
}

func tapNATAddressPlan(opts Options, config *vmkit.Config) (tapNATAddress, error) {
	if config != nil && config.Network != nil && (config.Network.IP != "" || config.Network.Gateway != "" || config.Network.Subnet != "") {
		return staticTAPNATAddressPlan(*config.Network)
	}
	subnetOctet, err := allocateNATSubnetOctet(opts)
	if err != nil {
		return tapNATAddress{}, err
	}
	subnet := fmt.Sprintf("10.43.%d.0/29", subnetOctet)
	hostIP := fmt.Sprintf("10.43.%d.1", subnetOctet)
	guestIP := fmt.Sprintf("10.43.%d.2", subnetOctet)
	return tapNATAddress{
		Subnet:    subnet,
		GuestCIDR: guestIP + "/29",
		Gateway:   hostIP,
		HostCIDR:  hostIP + "/29",
	}, nil
}

func staticTAPNATAddressPlan(network vmkit.NetworkConfig) (tapNATAddress, error) {
	if strings.TrimSpace(network.IP) == "" || strings.TrimSpace(network.Gateway) == "" {
		return tapNATAddress{}, fmt.Errorf("firecracker static nat/user networking requires network.ip and network.gateway")
	}
	guestIP, guestNet, err := net.ParseCIDR(strings.TrimSpace(network.IP))
	if err != nil {
		return tapNATAddress{}, fmt.Errorf("parse firecracker static network.ip %q: %w", network.IP, err)
	}
	if guestIP.To4() == nil {
		return tapNATAddress{}, fmt.Errorf("firecracker static network.ip %q must be IPv4 CIDR", network.IP)
	}
	gateway := net.ParseIP(strings.TrimSpace(network.Gateway)).To4()
	if gateway == nil {
		return tapNATAddress{}, fmt.Errorf("firecracker static network.gateway %q must be IPv4", network.Gateway)
	}
	subnet := strings.TrimSpace(network.Subnet)
	if subnet == "" {
		subnet = guestNet.String()
	} else {
		_, declaredSubnet, err := net.ParseCIDR(subnet)
		if err != nil {
			return tapNATAddress{}, fmt.Errorf("parse firecracker static network.subnet %q: %w", network.Subnet, err)
		}
		if declaredSubnet.IP.To4() == nil {
			return tapNATAddress{}, fmt.Errorf("firecracker static network.subnet %q must be IPv4 CIDR", network.Subnet)
		}
		if !declaredSubnet.Contains(guestIP) || !declaredSubnet.Contains(gateway) {
			return tapNATAddress{}, fmt.Errorf("firecracker static network.subnet %q must contain network.ip and network.gateway", network.Subnet)
		}
	}
	_, hostNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return tapNATAddress{}, fmt.Errorf("parse firecracker static network.subnet %q: %w", subnet, err)
	}
	prefix, _ := hostNet.Mask.Size()
	return tapNATAddress{
		Subnet:    subnet,
		GuestCIDR: guestIP.String() + "/" + strconv.Itoa(prefix),
		Gateway:   gateway.String(),
		HostCIDR:  gateway.String() + "/" + strconv.Itoa(prefix),
	}, nil
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
	if err := createTap(tap); err != nil {
		return networkPrivilegeError("create firecracker tap "+tap, err)
	}
	tapLink, err := netlink.LinkByName(tap)
	if err != nil {
		_ = deleteTap(tap)
		return networkPrivilegeError("inspect firecracker tap "+tap, err)
	}
	bridgeLink, err := netlink.LinkByName(bridge)
	if err != nil {
		_ = deleteTap(tap)
		return fmt.Errorf("inspect firecracker bridge %q: %w", bridge, err)
	}
	if err := netlink.LinkSetMaster(tapLink, bridgeLink); err != nil {
		_ = deleteTap(tap)
		return networkPrivilegeError(fmt.Sprintf("attach firecracker tap %q to bridge %q", tap, bridge), err)
	}
	if err := netlink.LinkSetUp(tapLink); err != nil {
		_ = deleteTap(tap)
		return networkPrivilegeError("bring firecracker tap "+tap+" up", err)
	}
	return nil
}

func createTap(name string) error {
	if _, err := netlink.LinkByName(name); err == nil {
		return fmt.Errorf("network link %q already exists", name)
	} else if !linkNotFoundError(err) {
		return err
	}
	return netlink.LinkAdd(&netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Flags:     netlink.TUNTAP_DEFAULTS | netlink.TUNTAP_NO_PI,
	})
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

func enableNamespaceIPv4Forwarding() error {
	path := "/proc/sys/net/ipv4/ip_forward"
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("inspect net.ipv4.ip_forward for firecracker user networking: %w", err)
	}
	if strings.TrimSpace(string(data)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
		return networkPrivilegeError("enable net.ipv4.ip_forward in firecracker user network namespace", err)
	}
	return nil
}

func allocateNATSubnetOctet(opts Options) (int, error) {
	used := map[int]bool{}
	links, err := netlink.LinkList()
	if err != nil {
		return 0, fmt.Errorf("list host network interfaces for nat subnet allocation: %w", err)
	}
	for _, link := range links {
		name := link.Attrs().Name
		if !strings.HasPrefix(name, "magtap") {
			continue
		}
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			v4 := addr.IP.To4()
			if len(v4) == net.IPv4len && v4[0] == 10 && v4[1] == 43 {
				used[int(v4[2])] = true
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

const (
	nftMicroagentTable      = "microagent"
	nftNATPostroutingChain  = "MICROAGENT-NAT-POSTROUTING"
	nftForwardChain         = "MICROAGENT-FORWARD"
	nftUFWFilterTable       = "filter"
	nftUFWUserForwardChain  = "ufw-user-forward"
	nftRuleCommentPrefix    = "microagent:"
	nftRuleCommentSeparator = ":"
	nftForwardPriority      = -1
)

type nftFirewallRule struct {
	transientFirewallRule
	Exprs []expr.Any
}

func installNATFirewallRules(tap, subnet string) ([]transientFirewallRule, error) {
	nftRules, err := buildNATFirewallRules(tap, subnet)
	if err != nil {
		return nil, err
	}
	conn := &nftables.Conn{}
	if err := ensureNATFirewallChains(conn); err != nil {
		return nil, err
	}
	transientRules := make([]transientFirewallRule, 0, len(nftRules))
	for i, rule := range nftRules {
		table := nftRuleTable(rule.transientFirewallRule)
		chain := &nftables.Chain{Name: rule.Chain, Table: table}
		exists, err := nftRuleExists(conn, table, chain, rule.Comment)
		if err != nil {
			cleanupTransientFirewallRules(transientRules)
			return transientRules, networkPrivilegeError("inspect firecracker nat firewall", err)
		}
		if !exists {
			conn.AddRule(&nftables.Rule{
				Table:    table,
				Chain:    chain,
				Exprs:    rule.Exprs,
				UserData: nftRuleUserData(rule.Comment),
			})
			if err := conn.Flush(); err != nil {
				cleanupTransientFirewallRules(transientRules)
				return transientRules, networkPrivilegeError("configure firecracker nat firewall", err)
			}
		}
		transientRules = append(transientRules, nftRules[i].transientFirewallRule)
	}
	ufwRules, err := buildUFWForwardRules(conn, tap, subnet)
	if err != nil {
		cleanupTransientFirewallRules(transientRules)
		return transientRules, err
	}
	for _, rule := range ufwRules {
		table := nftRuleTable(rule.transientFirewallRule)
		chain := &nftables.Chain{Name: rule.Chain, Table: table}
		exists, err := nftRuleExists(conn, table, chain, rule.Comment)
		if err != nil {
			cleanupTransientFirewallRules(transientRules)
			return transientRules, networkPrivilegeError("inspect firecracker ufw forward firewall", err)
		}
		if !exists {
			conn.AddRule(&nftables.Rule{
				Table:    table,
				Chain:    chain,
				Exprs:    rule.Exprs,
				UserData: nftRuleUserData(rule.Comment),
			})
			if err := conn.Flush(); err != nil {
				cleanupTransientFirewallRules(transientRules)
				return transientRules, networkPrivilegeError("configure firecracker ufw forward firewall", err)
			}
		}
		transientRules = append(transientRules, rule.transientFirewallRule)
	}
	return transientRules, nil
}

func buildNATFirewallRules(tap, subnet string) ([]nftFirewallRule, error) {
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("parse firecracker nat subnet %q: %w", subnet, err)
	}
	if network.IP.To4() == nil {
		return nil, fmt.Errorf("firecracker nat subnet %q is not IPv4", subnet)
	}
	return []nftFirewallRule{
		{
			transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftNATPostroutingChain, Comment: nftRuleComment(tap, "masquerade")},
			Exprs: append(append(ipv4SubnetMatchExprs(12, network),
				&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: nftIfName(tap)},
			), &expr.Masq{}),
		},
		{
			transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftForwardChain, Comment: nftRuleComment(tap, "forward-out")},
			Exprs:                 append(append(ifNameMatchExprs(expr.MetaKeyIIFNAME, tap), ipv4SubnetMatchExprs(12, network)...), &expr.Verdict{Kind: expr.VerdictAccept}),
		},
		{
			transientFirewallRule: transientFirewallRule{Table: nftMicroagentTable, Chain: nftForwardChain, Comment: nftRuleComment(tap, "forward-established")},
			Exprs:                 append(append(append(ifNameMatchExprs(expr.MetaKeyOIFNAME, tap), ipv4SubnetMatchExprs(16, network)...), establishedRelatedExprs()...), &expr.Verdict{Kind: expr.VerdictAccept}),
		},
	}, nil
}

func buildUFWForwardRules(conn *nftables.Conn, tap, subnet string) ([]nftFirewallRule, error) {
	table := &nftables.Table{Name: nftUFWFilterTable, Family: nftables.TableFamilyIPv4}
	if _, err := conn.ListChain(table, nftUFWUserForwardChain); err != nil {
		return nil, nil
	}
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("parse firecracker nat subnet %q: %w", subnet, err)
	}
	if network.IP.To4() == nil {
		return nil, fmt.Errorf("firecracker nat subnet %q is not IPv4", subnet)
	}
	return []nftFirewallRule{
		{
			transientFirewallRule: transientFirewallRule{Family: "ip", Table: nftUFWFilterTable, Chain: nftUFWUserForwardChain, Comment: nftRuleComment(tap, "ufw-forward-out")},
			Exprs:                 append(append(ifNameMatchExprs(expr.MetaKeyIIFNAME, tap), ipv4SubnetMatchExprs(12, network)...), &expr.Verdict{Kind: expr.VerdictAccept}),
		},
	}, nil
}

func ensureNATFirewallChains(conn *nftables.Conn) error {
	table := microagentNFTTable()
	conn.AddTable(table)
	if err := conn.Flush(); err != nil {
		return networkPrivilegeError("create firecracker nftables table "+nftMicroagentTable, err)
	}
	chains := []*nftables.Chain{
		{
			Name:     nftNATPostroutingChain,
			Table:    table,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookPostrouting,
			Priority: nftables.ChainPriorityNATSource,
		},
		{
			Name:     nftForwardChain,
			Table:    table,
			Type:     nftables.ChainTypeFilter,
			Hooknum:  nftables.ChainHookForward,
			Priority: nftables.ChainPriorityRef(nftForwardPriority),
		},
	}
	for _, chain := range chains {
		if _, err := conn.ListChain(table, chain.Name); err == nil {
			continue
		}
		conn.AddChain(chain)
		if err := conn.Flush(); err != nil {
			return networkPrivilegeError("create firecracker nftables chain "+chain.Name, err)
		}
	}
	return nil
}

func alreadyExistsError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "chain already exists") || strings.Contains(text, "file exists") || strings.Contains(text, "object already exists")
}

func cleanupTransientNetworkDevices(devices []transientNetworkDevice) {
	for _, device := range devices {
		if device.PID > 0 {
			_ = syscall.Kill(device.PID, syscall.SIGTERM)
		}
		if device.Created && device.Name != "" && device.Mode == "tap" {
			if !validMicroagentTapName(device.Name) {
				continue
			}
			_ = deleteTap(device.Name)
		}
	}
}

func cleanupTransientFirewallRules(rules []transientFirewallRule) {
	for i := len(rules) - 1; i >= 0; i-- {
		rule := rules[i]
		if !validMicroagentFirewallRule(rule) {
			continue
		}
		_ = deleteNFTFirewallRule(rule)
	}
}

func validMicroagentTapName(name string) bool {
	if !strings.HasPrefix(name, "magtap") || len(name) != len("magtap")+8 {
		return false
	}
	for _, r := range strings.TrimPrefix(name, "magtap") {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validMicroagentFirewallRule(rule transientFirewallRule) bool {
	if rule.Comment == "" {
		return false
	}
	if rule.Table == nftMicroagentTable {
		if rule.Chain != nftNATPostroutingChain && rule.Chain != nftForwardChain {
			return false
		}
	} else if rule.Family == "ip" && rule.Table == nftUFWFilterTable && rule.Chain == nftUFWUserForwardChain {
		// microagent may add a tagged allow rule to UFW's user-forward chain
		// so UFW does not drop packets accepted by microagent's own base chain.
	} else {
		return false
	}
	parts := strings.Split(rule.Comment, nftRuleCommentSeparator)
	return len(parts) == 3 && parts[0] == strings.TrimSuffix(nftRuleCommentPrefix, nftRuleCommentSeparator) && validMicroagentTapName(parts[1])
}

func deleteTap(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if linkNotFoundError(err) {
			return nil
		}
		return err
	}
	return netlink.LinkDel(link)
}

func microagentNFTTable() *nftables.Table {
	return &nftables.Table{Name: nftMicroagentTable, Family: nftables.TableFamilyINet}
}

func nftRuleTable(rule transientFirewallRule) *nftables.Table {
	family := nftables.TableFamilyINet
	if rule.Family == "ip" {
		family = nftables.TableFamilyIPv4
	}
	return &nftables.Table{Name: rule.Table, Family: family}
}

func nftRuleExists(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, comment string) (bool, error) {
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return false, err
	}
	for _, rule := range rules {
		if nftRuleCommentFromUserData(rule.UserData) == comment {
			return true, nil
		}
	}
	return false, nil
}

func deleteNFTFirewallRule(rule transientFirewallRule) error {
	conn := &nftables.Conn{}
	table := nftRuleTable(rule)
	chain := &nftables.Chain{Name: rule.Chain, Table: table}
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return err
	}
	deleted := false
	for _, candidate := range rules {
		if nftRuleCommentFromUserData(candidate.UserData) != rule.Comment {
			continue
		}
		if err := conn.DelRule(candidate); err != nil {
			return err
		}
		deleted = true
	}
	if !deleted {
		return nil
	}
	return conn.Flush()
}

func nftRuleComment(tap, kind string) string {
	return nftRuleCommentPrefix + tap + nftRuleCommentSeparator + kind
}

func nftRuleUserData(comment string) []byte {
	return userdata.AppendString(nil, userdata.TypeComment, comment)
}

func nftRuleCommentFromUserData(data []byte) string {
	comment, ok := userdata.GetString(data, userdata.TypeComment)
	if !ok {
		return ""
	}
	return comment
}

func nftIfName(name string) []byte {
	data := make([]byte, 16)
	copy(data, []byte(name+"\x00"))
	return data
}

func ifNameMatchExprs(key expr.MetaKey, name string) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: nftIfName(name)},
	}
}

func ipv4SubnetMatchExprs(offset uint32, network *net.IPNet) []expr.Any {
	mask := []byte(network.Mask)
	if len(mask) != net.IPv4len {
		mask = []byte{255, 255, 255, 255}
	}
	networkIP := network.IP.To4()
	masked := make([]byte, net.IPv4len)
	for i := 0; i < net.IPv4len; i++ {
		masked[i] = networkIP[i] & mask[i]
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: net.IPv4len},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: net.IPv4len, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: masked},
	}
}

func establishedRelatedExprs() []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:            binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
	}
}

func linkNotFoundError(err error) bool {
	var notFound netlink.LinkNotFoundError
	return errors.As(err, &notFound)
}

type processCapabilities struct {
	Effective   uint64
	Permitted   uint64
	Inheritable uint64
}

var getProcessCapabilities = currentProcessCapabilities
var addInheritableCapability = currentAddInheritableCapability
var getEffectiveUID = os.Geteuid

func currentProcessCapabilities() (processCapabilities, error) {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return processCapabilities{}, err
	}
	return processCapabilities{
		Effective:   capabilityWords(data[0].Effective, data[1].Effective),
		Permitted:   capabilityWords(data[0].Permitted, data[1].Permitted),
		Inheritable: capabilityWords(data[0].Inheritable, data[1].Inheritable),
	}, nil
}

func capabilityWords(low, high uint32) uint64 {
	return uint64(low) | uint64(high)<<32
}

func currentAddInheritableCapability(capability int) error {
	if capability < 0 || capability >= 64 {
		return fmt.Errorf("invalid Linux capability %d", capability)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return err
	}
	index := capability / 32
	bit := uint32(1) << uint(capability%32)
	data[index].Inheritable |= bit
	return unix.Capset(&header, &data[0])
}

func ensureNetAdminInheritable() error {
	if getEffectiveUID() == 0 {
		return nil
	}
	caps, err := getProcessCapabilities()
	if err != nil {
		return fmt.Errorf("inspect firecracker supervisor capabilities: %w", err)
	}
	if hasCapability(caps.Effective, unix.CAP_NET_ADMIN) &&
		hasCapability(caps.Permitted, unix.CAP_NET_ADMIN) &&
		hasCapability(caps.Inheritable, unix.CAP_NET_ADMIN) {
		return nil
	}
	if hasCapability(caps.Effective, unix.CAP_NET_ADMIN) &&
		hasCapability(caps.Permitted, unix.CAP_NET_ADMIN) &&
		hasCapability(caps.Effective, unix.CAP_SETPCAP) &&
		hasCapability(caps.Permitted, unix.CAP_SETPCAP) {
		if err := addInheritableCapability(unix.CAP_NET_ADMIN); err != nil {
			return fmt.Errorf("add CAP_NET_ADMIN to firecracker supervisor inheritable capability set: %w", err)
		}
		caps, err = getProcessCapabilities()
		if err != nil {
			return fmt.Errorf("inspect firecracker supervisor capabilities after inheritable update: %w", err)
		}
		if hasCapability(caps.Effective, unix.CAP_NET_ADMIN) &&
			hasCapability(caps.Permitted, unix.CAP_NET_ADMIN) &&
			hasCapability(caps.Inheritable, unix.CAP_NET_ADMIN) {
			return nil
		}
	}
	return fmt.Errorf("firecracker nat and bridged networking require CAP_NET_ADMIN in the supervisor effective, permitted, and inheritable capability sets so Firecracker can inherit it; run as root, launch microagent with CAP_NET_ADMIN in effective/permitted/inheritable sets, use --network user for unprivileged outbound networking, or use --network isolated if outbound network is not needed")
}

func hasCapability(caps uint64, capability int) bool {
	if capability < 0 || capability >= 64 {
		return false
	}
	return caps&(uint64(1)<<uint(capability)) != 0
}

func networkPrivilegeError(action string, err error) error {
	text := strings.ToLower(err.Error())
	if errors.Is(err, syscall.EPERM) || strings.Contains(text, "operation not permitted") || strings.Contains(text, "permission denied") {
		return fmt.Errorf("%s: firecracker nat and bridged networking require CAP_NET_ADMIN to create TAP devices, configure NAT, and let Firecracker attach the TAP; run as root, launch microagent with CAP_NET_ADMIN in effective/permitted/inheritable sets, use --network user for unprivileged outbound networking, or use --network isolated if outbound network is not needed: %w", action, err)
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

func firecrackerGuestCID(opts Options) uint32 {
	seed := opts.Name + "\x00" + opts.StateDir
	sum := sha1.Sum([]byte(seed))
	value := uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
	return 3 + value%((1<<31)-3)
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

func startVsockListenerProcess(opts Options) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := filepath.Join(opts.StateDir, opts.Name, "vsock-listener.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(executable, "--vsock-listener", "--state-dir", opts.StateDir, "--name", opts.Name)
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

func RunVsockListener(ctx context.Context, opts Options) error {
	state, err := readRuntimeState(opts)
	if err != nil {
		return err
	}
	set, err := startVsockListeners(opts, &state.Config)
	if err != nil {
		return err
	}
	defer set.Close()
	<-ctx.Done()
	return ctx.Err()
}

func RunPortForwarder(ctx context.Context, opts Options) error {
	state, err := readRuntimeState(opts)
	if err != nil {
		return err
	}
	if !needsPortForwarder(&state.Config) {
		return nil
	}
	forwards := portForwarderForwards(state.Config)
	listeners := make([]net.Listener, 0, len(forwards))
	for _, forward := range forwards {
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

func portForwarderForwards(config vmkit.Config) []vmkit.PortForward {
	forwards := []vmkit.PortForward{}
	if config.Network != nil {
		forwards = append(forwards, config.Network.PortForwards...)
	}
	if config.ShellPort != 0 {
		forwards = append(forwards, vmkit.PortForward{
			Protocol:  "tcp",
			Host:      "127.0.0.1",
			HostPort:  config.ShellPort,
			GuestPort: config.ShellPort,
		})
	}
	if config.ExecPort != 0 {
		forwards = append(forwards, vmkit.PortForward{
			Protocol:  "tcp",
			Host:      "127.0.0.1",
			HostPort:  config.ExecPort,
			GuestPort: config.ExecPort,
		})
	}
	return forwards
}

type vsockListenerSet struct {
	listeners []net.Listener
}

func startVsockListeners(opts Options, config *vmkit.Config) (*vsockListenerSet, error) {
	if config == nil || len(config.VsockListeners) == 0 {
		return &vsockListenerSet{}, nil
	}
	set := &vsockListenerSet{}
	for _, listener := range config.VsockListeners {
		if !isAllowedVsockTarget(opts, listener.Target) {
			set.Close()
			return nil, fmt.Errorf("firecracker vsock listener %d target must be host:port or the workspace result path", listener.Port)
		}
		path := firecrackerGuestVsockPath(opts, listener.Port)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			set.Close()
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			set.Close()
			return nil, err
		}
		unixListener, err := net.Listen("unix", path)
		if err != nil {
			set.Close()
			return nil, fmt.Errorf("listen firecracker guest vsock port %d: %w", listener.Port, err)
		}
		set.listeners = append(set.listeners, unixListener)
		go serveVsockListener(unixListener, listener)
	}
	return set, nil
}

func (s *vsockListenerSet) Close() {
	if s == nil {
		return
	}
	for _, listener := range s.listeners {
		_ = listener.Close()
		if unixListener, ok := listener.(*net.UnixListener); ok {
			_ = os.Remove(unixListener.Addr().String())
		}
	}
}

func serveVsockListener(listener net.Listener, config vmkit.VsockListener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleGuestVsockConnection(conn, config.Target)
	}
}

func handleGuestVsockConnection(conn net.Conn, target string) {
	const maxResultBytes int64 = 16 * 1024 * 1024
	defer conn.Close()
	if tcpTarget, ok := parseTCPAddr(target); ok {
		remote, err := net.Dial("tcp", tcpTarget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect vsock target %s: %v\n", tcpTarget, err)
			return
		}
		defer remote.Close()
		go func() {
			_, _ = io.Copy(remote, conn)
			closeWriteConn(remote)
		}()
		_, _ = io.Copy(conn, remote)
		closeWriteConn(conn)
		return
	}
	data, err := io.ReadAll(io.LimitReader(conn, maxResultBytes+1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read guest vsock result for %s: %v\n", target, err)
		return
	}
	if int64(len(data)) > maxResultBytes {
		fmt.Fprintf(os.Stderr, "guest vsock result for %s exceeded %d bytes\n", target, maxResultBytes)
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create result directory for %s: %v\n", target, err)
		return
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write result %s: %v\n", target, err)
	}
}

func isAllowedVsockTarget(opts Options, target string) bool {
	if _, ok := parseTCPAddr(target); ok {
		return true
	}
	return target == filepath.Join(opts.StateDir, opts.Name, "result.json")
}

func parseTCPAddr(target string) (string, bool) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || port == "" {
		return "", false
	}
	return net.JoinHostPort(host, port), true
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
	_ = conn.Close()
	_ = vsock.Close()
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
	if state.Event.State == vmkit.StateRunning && GuestHalted(serialLogPath(opts)) {
		resultWait := time.Duration(0)
		if runtimeHasResultListener(opts, state) {
			resultWait = 2 * time.Second
		}
		finalState, errorText := guestHaltedState(opts, resultWait)
		if state.PID != 0 {
			_ = signalProcessGroup(state.PID, syscall.SIGTERM)
			_ = waitForProcessExit(context.Background(), state.PID, 5*time.Second)
		}
		if state.PortForwardPID != 0 {
			_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
		}
		if state.VsockListenerPID != 0 {
			terminateAuxProcess(state.VsockListenerPID)
		}
		cleanupTransientFirewallRules(state.FirewallRules)
		cleanupTransientNetworkDevices(state.NetworkDevices)
		cleanupUserNetworkProcess(opts)
		req := runtimeStateRequest(vmkit.Request{}, state)
		if err := writeProcessStateWithProcessesAndNetwork(opts, req, finalState, 0, 0, 0, nil, nil, errorText); err != nil {
			return vmkit.Response{}, err
		}
		state, err = readRuntimeState(opts)
		if err != nil {
			return vmkit.Response{}, err
		}
		return responseFromRuntimeState(opts, state), nil
	}
	return responseFromRuntimeState(opts, state), nil
}

func runtimeHasResultListener(opts Options, state runtimeState) bool {
	resultPath := resultPathFromState(opts, state)
	for _, listener := range state.Config.VsockListeners {
		if listener.Target == resultPath {
			return true
		}
	}
	return false
}

func guestHaltedState(opts Options, waitForResult time.Duration) (vmkit.VMState, string) {
	resultPath := filepath.Join(opts.StateDir, opts.Name, "result.json")
	deadline := time.Now().Add(waitForResult)
	var data []byte
	var err error
	for {
		data, err = os.ReadFile(resultPath)
		if err != nil && (waitForResult <= 0 || !os.IsNotExist(err) || time.Now().After(deadline)) {
			return vmkit.StateStopped, ""
		}
		if err == nil {
			var result struct {
				ExitCode int    `json:"exit_code"`
				Error    string `json:"error"`
			}
			if err := json.Unmarshal(data, &result); err == nil {
				if result.ExitCode == 0 {
					return vmkit.StateStopped, ""
				}
				if result.Error != "" {
					return vmkit.StateFailed, result.Error
				}
				return vmkit.StateFailed, fmt.Sprintf("guest exited with code %d", result.ExitCode)
			} else if waitForResult <= 0 || time.Now().After(deadline) {
				return vmkit.StateFailed, err.Error()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
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

func responseFromRuntimeState(opts Options, state runtimeState) vmkit.Response {
	resp := responseFromEvent(state.Event, state.Error)
	readiness := readinessFromRuntimeState(state)
	resp.Readiness = &readiness
	if state.Config.Network != nil {
		network := *state.Config.Network
		network.Runtime = nil
		resp.Network = &network
	}
	resp.Mediation = state.Config.Mediation
	if result, err := runtimeResultFromState(opts, state); err == nil {
		resp.Result = &result
	}
	return resp
}

func runtimeResultFromState(opts Options, state runtimeState) (vmkit.RuntimeResult, error) {
	path := resultPathFromState(opts, state)
	data, err := os.ReadFile(path)
	if err != nil {
		return vmkit.RuntimeResult{}, err
	}
	var guest guestResult
	if err := json.Unmarshal(data, &guest); err != nil {
		return vmkit.RuntimeResult{}, err
	}
	return vmkit.RuntimeResult{
		Identity:    state.Event.Identity,
		Backend:     vmkit.BackendFirecracker,
		ResultPath:  path,
		StartedAt:   guest.StartedAt,
		CompletedAt: guest.ExitedAt,
		ExitCode:    guest.ExitCode,
		Stdout:      guest.Stdout,
		Stderr:      guest.Stderr,
		Error:       guest.Error,
	}, nil
}

func writeProcessState(opts Options, req vmkit.Request, state vmkit.VMState, pid int, errorText string) error {
	return writeProcessStateWithForwarder(opts, req, state, pid, 0, errorText)
}

func writeProcessStateWithForwarder(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID int, errorText string) error {
	return writeProcessStateWithForwarderAndNetwork(opts, req, state, pid, portForwardPID, nil, nil, errorText)
}

func writeProcessStateWithForwarderAndNetwork(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID int, networkDevices []transientNetworkDevice, firewallRules []transientFirewallRule, errorText string) error {
	return writeProcessStateWithProcessesAndNetwork(opts, req, state, pid, portForwardPID, 0, networkDevices, firewallRules, errorText)
}

func writeProcessStateWithProcessesAndNetwork(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID, vsockListenerPID int, networkDevices []transientNetworkDevice, firewallRules []transientFirewallRule, errorText string) error {
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
		Event:            fileEvent,
		Config:           *req.Config,
		PID:              pid,
		PortForwardPID:   portForwardPID,
		VsockListenerPID: vsockListenerPID,
		NetworkDevices:   append([]transientNetworkDevice{}, networkDevices...),
		FirewallRules:    append([]transientFirewallRule{}, firewallRules...),
		SerialLogPath:    serialLogPath(opts),
		SerialInputPath:  serialInputPath(opts),
		UpdatedAt:        now.Format(time.RFC3339),
		Error:            errorText,
	}
	if state == vmkit.StateStarting || state == vmkit.StateRunning {
		runtime.StartedAt = now.Format(time.RFC3339)
	}
	runtime.Readiness = readinessFromRuntimeState(runtime)
	return writeJSONFile(filepath.Join(dir, "runtime.json"), runtime)
}

func readinessFromRuntimeState(state runtimeState) vmkit.RuntimeReadiness {
	readiness := vmkit.RuntimeReadiness{}
	if state.StartedAt != "" || state.Event.State == vmkit.StateRunning || state.Event.State == vmkit.StateHalted || state.Event.State == vmkit.StateStopped || state.Event.State == vmkit.StateQuarantined {
		readiness.GuestReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: firstEventTime(state.StartedAt, state.Event.ObservedAt),
			Detail:     "workspace reached runtime state " + string(state.Event.State),
		}
	}
	if state.Event.State == vmkit.StateRunning && state.SerialInputPath != "" {
		if signal, ok := shellReadinessFromRuntimeState(state); ok {
			readiness.ShellReady = signal
		}
	}
	if signal, ok := execReadinessFromRuntimeState(state); ok {
		readiness.ExecReady = signal
	}
	resultPath := resultPathFromState(Options{}, state)
	if _, err := os.Stat(resultPath); err == nil {
		readiness.ResultReady = vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: fileModTime(resultPath),
			Detail:     "guest result is available",
		}
	} else if !os.IsNotExist(err) {
		readiness.ResultReady = vmkit.ReadinessSignal{Error: err.Error()}
	}
	if state.Config.Mediation != nil && state.Config.Mediation.Enabled {
		readiness.MediationReady = vmkit.MediationReadinessSignal(context.Background(), *state.Config.Mediation, state.Event.State, firstEventTime(state.StartedAt, state.Event.ObservedAt), 150*time.Millisecond)
	}
	return readiness
}

func execReadinessFromRuntimeState(state runtimeState) (vmkit.ReadinessSignal, bool) {
	if state.Event.State != vmkit.StateRunning {
		return vmkit.ReadinessSignal{}, false
	}
	observedAt := time.Now().UTC()
	if state.Config.ExecPort == 0 {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     "structured exec port is not configured",
		}, true
	}
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(state.Config.ExecPort)))
	req := execprotocol.NewExecRequest([]string{"true"})
	req.TimeoutMS = 2000
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	start := time.Now()
	result, err := execclient.New(target).Exec(ctx, req)
	cancel()
	elapsed := time.Since(start)
	if err != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service unreachable at %s after %s: %v", target, elapsed.Round(time.Millisecond), err),
		}, true
	}
	if result.Error != nil {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec service returned %s: %s", result.Error.Code, result.Error.Message),
			Error:      result.Error.Error(),
		}, true
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		exit := "nil"
		if result.ExitCode != nil {
			exit = strconv.Itoa(*result.ExitCode)
		}
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("exec probe command failed unexpectedly: status=%s exit_code=%s", result.Status, exit),
		}, true
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: &observedAt,
		Detail:     fmt.Sprintf("exec service round-trip ready at %s in %s", target, elapsed.Round(time.Millisecond)),
	}, true
}

func shellReadinessFromRuntimeState(state runtimeState) (vmkit.ReadinessSignal, bool) {
	if _, err := os.Stat(state.SerialInputPath); err != nil {
		if !os.IsNotExist(err) {
			return vmkit.ReadinessSignal{Error: err.Error()}, true
		}
		return vmkit.ReadinessSignal{}, false
	}
	if state.Config.ShellPort != 0 {
		target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(state.Config.ShellPort)))
		observedAt := time.Now().UTC()
		start := time.Now()
		conn, err := net.DialTimeout("tcp", target, 150*time.Millisecond)
		elapsed := time.Since(start)
		if err != nil {
			return vmkit.ReadinessSignal{
				Ready:      false,
				ObservedAt: &observedAt,
				Detail:     fmt.Sprintf("shell target unreachable at %s after %s: %v", target, elapsed.Round(time.Millisecond), err),
			}, true
		}
		_ = conn.Close()
		return vmkit.ReadinessSignal{
			Ready:      true,
			ObservedAt: &observedAt,
			Detail:     fmt.Sprintf("shell target reachable at %s in %s", target, elapsed.Round(time.Millisecond)),
		}, true
	}
	return vmkit.ReadinessSignal{
		Ready:      true,
		ObservedAt: fileModTime(state.SerialInputPath),
		Detail:     "console input is available",
	}, true
}

func resultPathFromState(opts Options, state runtimeState) string {
	stateDir := state.Config.StateDir
	if stateDir == "" {
		stateDir = opts.StateDir
	}
	name := state.Event.Identity.RuntimeID
	if name == "" {
		name = opts.Name
	}
	return filepath.Join(stateDir, name, "result.json")
}

func fileModTime(path string) *time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	modified := info.ModTime().UTC()
	return &modified
}

func firstEventTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func appendEvent(path string, event eventFile) error {
	const maxEvents = 1024
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
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
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
		req.Config.TimeoutSeconds = state.Config.TimeoutSeconds
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

func detachedStartExitError(cmd *exec.Cmd, delay time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	var status syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
	if err != nil {
		if err == syscall.ECHILD {
			return nil
		}
		return fmt.Errorf("check detached firecracker process %d: %w", cmd.Process.Pid, err)
	}
	if pid == 0 {
		return nil
	}
	if status.Exited() {
		return fmt.Errorf("firecracker exited during detached startup: exit status %d", status.ExitStatus())
	}
	if status.Signaled() {
		return fmt.Errorf("firecracker exited during detached startup: signal %s", status.Signal())
	}
	return fmt.Errorf("firecracker exited during detached startup: wait status %d", status)
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

func terminateAuxProcess(pid int) {
	if pid <= 0 {
		return
	}
	_ = signalProcessGroup(pid, syscall.SIGTERM)
	if err := waitForProcessExit(context.Background(), pid, 2*time.Second); err == nil {
		return
	}
	_ = signalProcessGroup(pid, syscall.SIGKILL)
	_ = waitForProcessExit(context.Background(), pid, time.Second)
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

// apiSocketPath is the Firecracker API unix socket. It is deterministic from
// the workspace identity, so pause/resume/snapshot reach it without recording
// it in runtime state. The VM boots from the config file; the API socket is
// additionally exposed (only --no-api would disable it) for runtime control.
func apiSocketPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "firecracker-api.sock")
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

func firecrackerGuestVsockPath(opts Options, port uint32) string {
	return fmt.Sprintf("%s_%d", vsockSocketPath(opts), port)
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
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
