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
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/network"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
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
	case "snapshot":
		return snapshotWorkspace(ctx, opts, req)
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
	case "named":
		if strings.TrimSpace(config.Network.Name) == "" {
			return fmt.Errorf("firecracker network.mode named requires a network name")
		}
		return nil
	case "bridged":
		if strings.TrimSpace(config.Network.Interface) == "" {
			return fmt.Errorf("firecracker network.interface is required for bridged mode")
		}
		return validateLinuxBridge(strings.TrimSpace(config.Network.Interface))
	default:
		return fmt.Errorf("firecracker network.mode %q is unsupported; use user, nat, isolated, bridged, or named", mode)
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
	Event             eventFile                `json:"event"`
	Config            vmkit.Config             `json:"config"`
	PID               int                      `json:"pid,omitempty"`
	PortForwardPID    int                      `json:"portForwardPid,omitempty"`
	VsockListenerPID  int                      `json:"vsockListenerPid,omitempty"`
	EgressMediatorPID int                      `json:"egressMediatorPid,omitempty"`
	NetworkDevices    []transientNetworkDevice `json:"networkDevices,omitempty"`
	FirewallRules     []transientFirewallRule  `json:"firewallRules,omitempty"`
	SerialLogPath     string                   `json:"serialLogPath"`
	SerialInputPath   string                   `json:"serialInputPath,omitempty"`
	StartedAt         string                   `json:"startedAt,omitempty"`
	UpdatedAt         string                   `json:"updatedAt"`
	Readiness         vmkit.RuntimeReadiness   `json:"readiness,omitempty"`
	Error             string                   `json:"error,omitempty"`
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
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o700); err != nil {
		return err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := serialLog.Close(); err != nil {
		return err
	}
	return writeProcessState(opts, req, vmkit.StatePrepared, 0, "")
}

func startProcess(ctx context.Context, opts Options, req vmkit.Request, detached bool) (vmkit.Response, error) {
	// Snapshot restore/fork rolls the rootfs back to the snapshot copy and
	// validates kernel/network compatibility once on the host, before any
	// user-network namespace re-exec carries the request inward.
	if req.Tag != "" && !insideUserNetworkNamespace() {
		if err := prepareSnapshotRestore(opts, req); err != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
	}
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
	// On a snapshot restore/fork (req.Tag != "") the egress mediator must be
	// re-armed with the SAME per-workspace CA the guest's baked trust store was
	// built against, NOT a freshly minted one. Read the recorded CA fingerprint
	// from the snapshot manifest so prepareNetworkForStart can reuse-and-verify
	// the persisted CA instead of re-minting. Fail closed if the manifest is
	// unreadable during a restore.
	restore := req.Tag != ""
	expectedCASHA := ""
	if restore {
		manifest, merr := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(opts.StateDir, opts.Name, req.Tag))
		if merr != nil {
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, merr.Error())
			return failedResponse(req, merr.Error()), merr
		}
		expectedCASHA = manifest.EgressCASHA256
		// Re-apply the persisted bounded-operations caps (ASK tenet 8) so a restored
		// workspace keeps the SAME bounds it was snapshotted under, just as the CA is
		// reused. Threaded onto req.Config here so provisionEgressMediation hands them
		// to the mediator flags via egressCapsFromConfig. Idempotent: if the restore
		// request already carries caps, the manifest reproduces the same values.
		applyManifestEgressCaps(req.Config, manifest)
	}
	networkDevices, firewallRules, runtimeNetwork, egressMediatorPID, err := prepareNetworkForStart(opts, req.Config, restore, expectedCASHA)
	if err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	// prepareNetworkForStart may have started the egress mediator — a host-side
	// companion already bound to the tap gateway. Every failure path below cleans
	// up the transient firewall rules and network devices, but the mediator is a
	// separate process and must be reaped too, or it is orphaned with the
	// workspace gone. Guard it with a deferred reaper that is disarmed only on the
	// detached-success path (where the mediator is intentionally left running and
	// recorded in runtime.json for stop/halt to reap later). terminateAuxProcess
	// is idempotent, so paths that also reap it explicitly stay correct.
	egressMediatorRunning := egressMediatorPID != 0
	defer func() {
		if egressMediatorRunning {
			terminateAuxProcess(egressMediatorPID)
		}
	}()
	runtimeReq := requestWithRuntimeNetwork(req, runtimeNetwork)
	// Move the shell/exec host binds off any unbindable port (e.g. a WSL2/Windows
	// reserved range) before the VM config (boot args) and runtime state are
	// written, so the guest, the forwarder, and readiness/connect all agree on
	// the final ports.
	ensureBindableManagementPorts(runtimeReq.Config)
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
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o700); err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		return vmkit.Response{}, err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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
	// disable the API. A snapshot restore/fork instead launches with just the
	// API socket and loads the snapshot (which carries its own machine config)
	// over it.
	loadMode := req.Tag != ""
	launchArgs := []string{"--api-sock", apiSocketPath(opts)}
	if !loadMode {
		launchArgs = append(launchArgs, "--config-file", configPath(opts))
	}
	cmd, err := firecrackerLaunchCommand(ctx, opts, req, path, launchArgs, loadMode)
	if err != nil {
		cleanupTransientFirewallRules(firewallRules)
		cleanupTransientNetworkDevices(networkDevices)
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
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
	if loadMode {
		if err := restoreFromSnapshot(ctx, opts, req.Tag, snapshotNetworkOverrides(opts, req.Config)); err != nil {
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
		// The restored/forked guest resumes with a zeroed /run/secrets (purged
		// before the source snapshot). Rehydrate it by re-fetching the bundle.
		// Best-effort: the guest is already running, so a transient failure is
		// retryable and should not kill a freshly restored VM.
		if materializedSecretsDeclared(runtimeReq.Config) && runtimeReq.Config.SecretsControlPort != 0 {
			if err := rehydrateGuestSecrets(opts, runtimeReq.Config.SecretsControlPort); err != nil {
				fmt.Fprintf(os.Stderr, "warning: rehydrate secrets after restore failed: %v\n", err)
			}
		}
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, 0, 0, egressMediatorPID, networkDevices, firewallRules, ""); err != nil {
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
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, vsockListenerPID, egressMediatorPID, networkDevices, firewallRules, ""); err != nil {
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
		pid, err := startReadyPortForwarderProcessWithManagementPortRetry(ctx, opts, runtimeReq.Config, func() error {
			return writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, 0, vsockListenerPID, egressMediatorPID, networkDevices, firewallRules, "")
		})
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
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, vsockListenerPID, egressMediatorPID, networkDevices, firewallRules, ""); err != nil {
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
			if egressMediatorPID != 0 {
				terminateAuxProcess(egressMediatorPID)
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
		// Detached start succeeded: the mediator stays up as a recorded companion
		// (reaped later by stop/halt/quarantine), so disarm the deferred reaper.
		egressMediatorRunning = false
		return eventResponse(req, vmkit.StateRunning, ""), nil
	}
	waitErr := waitForeground(ctx, cmd, serialLogPath(opts), opts.Timeout)
	// In detached user-network mode the outer start records companion PIDs
	// (port forwarder, vsock listener) in runtime.json after boot, and this
	// in-namespace foreground supervisor is the only process that observes the
	// VM exit. Reap those host-side companions before the final state write
	// below discards the recorded PIDs.
	terminateRecordedCompanions(opts)
	if egressMediatorPID != 0 {
		terminateAuxProcess(egressMediatorPID)
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
		if state.EgressMediatorPID != 0 {
			terminateAuxProcess(state.EgressMediatorPID)
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
	if state.EgressMediatorPID != 0 {
		terminateAuxProcess(state.EgressMediatorPID)
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
	if state.EgressMediatorPID != 0 {
		terminateAuxProcess(state.EgressMediatorPID)
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
	createSnapshot(ctx context.Context, snapshotPath, memFilePath string) error
	loadSnapshot(ctx context.Context, snapshotPath, memFilePath string, resume bool, networkOverrides []networkOverride) error
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
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeStateRequest(req, state), toState, state.PID, state.PortForwardPID, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
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
		return ensureWorkspaceProcessesStopped(opts, state)
	}
	active, err := processActive(state.PID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("firecracker workspace %s is running; stop or kill it before delete", opts.Name)
	}
	return ensureWorkspaceProcessesStopped(opts, state)
}

// ensureWorkspaceProcessesStopped rejects delete while any recorded companion
// or user-network process for the workspace is still running, regardless of
// whether the VM process itself is gone (a guest that exits on its own leaves
// the VM dead but can leave companions behind).
func ensureWorkspaceProcessesStopped(opts Options, state runtimeState) error {
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
	if state.EgressMediatorPID != 0 {
		active, err := processActive(state.EgressMediatorPID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("firecracker workspace %s egress mediator is running; stop or kill it before delete", opts.Name)
		}
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
	if err := os.MkdirAll(filepath.Dir(configPath(opts)), 0o700); err != nil {
		return err
	}
	return writeJSONFile(configPath(opts), cfg)
}

func firecrackerBootArgs(config *vmkit.Config) string {
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "pci=off", "root=/dev/vda", "rw", "init=/sbin/microagent-init"}
	// The guest listens on its own vsock ports, which differ from the host bind
	// ports when a fork or a host-port fallback (ensureBindableManagementPorts)
	// has moved the host side. Tell the guest its own ports, not the host ports.
	if config != nil && guestShellPort(*config) != 0 {
		args = append(args, fmt.Sprintf("microagent_shell_port=%d", guestShellPort(*config)))
	}
	if config != nil && guestExecPort(*config) != 0 {
		args = append(args, fmt.Sprintf("microagent_exec_port=%d", guestExecPort(*config)))
	}
	if config != nil && config.SecretsPort != 0 {
		args = append(args, fmt.Sprintf("microagent_secrets_port=%d", config.SecretsPort))
	}
	if config != nil && config.CACertPort != 0 {
		args = append(args, fmt.Sprintf("microagent_ca_cert_port=%d", config.CACertPort))
	}
	if config != nil && len(config.OnDemandSecrets) != 0 {
		args = append(args, "microagent_secrets_api=1")
	}
	if config != nil && config.SecretsControlPort != 0 {
		args = append(args, fmt.Sprintf("microagent_secrets_ctl_port=%d", config.SecretsControlPort))
	}
	if config != nil && config.ModelGuestPort != 0 && config.ModelVsockPort != 0 {
		args = append(args, fmt.Sprintf("microagent_model_fwd=%d:%d", config.ModelGuestPort, config.ModelVsockPort))
	}
	mode := networkMode(config)
	if (mode == "nat" || mode == "user" || mode == "named") && config != nil && config.Network != nil && config.Network.IP != "" && config.Network.Gateway != "" {
		args = append(args,
			"microagent_net_if=eth0",
			"microagent_net_ip="+config.Network.IP,
			"microagent_net_gw="+config.Network.Gateway,
		)
		if len(config.Network.DNS) != 0 {
			args = append(args, "microagent_net_dns="+strings.Join(config.Network.DNS, ","))
		}
		if len(config.Network.Hosts) != 0 {
			args = append(args, "microagent_net_hosts="+strings.Join(config.Network.Hosts, ","))
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

// bindableHostPort returns a host port that can actually be bound on 127.0.0.1.
// If the preferred port binds it is returned unchanged. Otherwise — most notably
// on WSL2, where Windows reserves dynamic TCP port ranges that are unbindable
// inside the distro even though no Linux process holds them (visible via
// `netsh.exe interface ipv4 show excludedportrange protocol=tcp`) — an
// OS-assigned free port is returned. The bool reports whether the port changed.
func bindableHostPort(preferred uint16) (uint16, bool) {
	if preferred == 0 {
		return 0, false
	}
	if l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(preferred)))); err == nil {
		_ = l.Close()
		return preferred, false
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// Could not secure any port; leave the preferred port in place and let
		// the forwarder surface the original bind error.
		return preferred, false
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return port, true
}

// ensureBindableManagementPorts moves the host bind for the shell and exec
// services off any unbindable port (e.g. a WSL2/Windows-reserved range) onto a
// free port, preserving the original as the guest vsock port so the host->guest
// bridge and the guest's own listeners still agree. The guest side is unaffected
// by host port reservations because it listens on vsock, not host TCP. User
// port-forwards are intentionally left untouched: those ports are operator
// intent and a conflict there should surface, not be silently reassigned.
func ensureBindableManagementPorts(config *vmkit.Config) {
	if config == nil {
		return
	}
	if config.ShellPort != 0 {
		if port, changed := bindableHostPort(config.ShellPort); changed {
			if config.GuestShellPort == 0 {
				config.GuestShellPort = config.ShellPort
			}
			config.ShellPort = port
		}
	}
	if config.ExecPort != 0 {
		if port, changed := bindableHostPort(config.ExecPort); changed {
			if config.GuestExecPort == 0 {
				config.GuestExecPort = config.ExecPort
			}
			config.ExecPort = port
		}
	}
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
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
	}
	if needsPortForwarder(req.Config) {
		pid, err := startReadyPortForwarderProcessWithManagementPortRetry(context.Background(), opts, runtimeReq.Config, func() error {
			return writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, "")
		})
		if err != nil {
			_ = writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, err.Error())
			return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
		}
		state.PortForwardPID = pid
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, pid, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
			_ = signalProcessGroup(pid, syscall.SIGTERM)
			return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
		}
		state.Config = *runtimeReq.Config
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
	if mode != "bridged" && mode != "nat" && mode != "user" && mode != "named" {
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

func prepareNetworkForStart(opts Options, config *vmkit.Config, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	switch networkMode(config) {
	case "isolated":
		return nil, nil, nil, 0, nil
	case "user":
		return prepareUserNetworkForStart(opts, config, restore, expectedCASHA)
	case "nat":
		return prepareNATForStart(opts, config, restore, expectedCASHA)
	case "named":
		return prepareNamedNetworkForStart(opts, config, restore, expectedCASHA)
	case "bridged":
	default:
		return nil, nil, nil, 0, fmt.Errorf("firecracker network.mode %q is unsupported; use user, nat, isolated, bridged, or named", networkMode(config))
	}
	bridge := strings.TrimSpace(config.Network.Interface)
	if err := validateLinuxBridge(bridge); err != nil {
		return nil, nil, nil, 0, err
	}
	device := transientNetworkDevice{Name: tapName(opts), Mode: "tap", Interface: bridge, Created: true}
	if err := createBridgeTap(device.Name, bridge); err != nil {
		return nil, nil, nil, 0, err
	}
	return []transientNetworkDevice{device}, nil, nil, 0, nil
}

func prepareNATForStart(opts Options, config *vmkit.Config, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	if err := requireIPv4Forwarding(); err != nil {
		return nil, nil, nil, 0, err
	}
	return prepareTAPNATForStart(opts, config, "nat", restore, expectedCASHA)
}

func prepareUserNetworkForStart(opts Options, config *vmkit.Config, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	if !insideUserNetworkNamespace() {
		return nil, nil, nil, 0, fmt.Errorf("firecracker user networking must run inside a pasta user network namespace")
	}
	if err := enableNamespaceIPv4Forwarding(); err != nil {
		return nil, nil, nil, 0, err
	}
	devices, rules, network, egressPID, err := prepareTAPNATForStart(opts, config, "user", restore, expectedCASHA)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return attachUserNetworkPID(devices), rules, network, egressPID, nil
}

func prepareTAPNATForStart(opts Options, config *vmkit.Config, mode string, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	plan, err := tapNATAddressPlan(opts, config)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	tap := tapName(opts)
	device := transientNetworkDevice{Name: tap, Mode: "tap", Interface: plan.Subnet, Created: true}
	if err := createTap(tap); err != nil {
		return nil, nil, nil, 0, networkPrivilegeError("create firecracker nat tap "+tap, err)
	}
	cleanupDevices := []transientNetworkDevice{device}
	link, err := netlink.LinkByName(tap)
	if err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, networkPrivilegeError("inspect firecracker nat tap "+tap, err)
	}
	addr, err := netlink.ParseAddr(plan.HostCIDR)
	if err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, fmt.Errorf("parse firecracker nat tap address %s: %w", plan.HostCIDR, err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil && !alreadyExistsError(err) {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, networkPrivilegeError("assign firecracker nat tap address "+plan.HostCIDR, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, networkPrivilegeError("bring firecracker nat tap up", err)
	}
	rules, err := installNATFirewallRules(tap, plan.Subnet)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, err
	}
	network := runtimeNetworkConfig(config, plan.Subnet, plan.GuestCIDR, plan.Gateway)
	network.Mode = mode
	egressPID, egressRules, err := provisionEgressMediation(opts, config, mode, tap, plan.Gateway, plan.Subnet, nil, restore, expectedCASHA)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, err
	}
	rules = append(rules, egressRules...)
	return cleanupDevices, rules, &network, egressPID, nil
}

// provisionEgressMediation provisions the egress mediator and its steering rules
// (per-workspace CA, mediator process, TCP REDIRECT, UDP TPROXY) for a guest
// reachable on the given tap, gateway, and subnet. It returns the mediator PID
// and the nft rules the caller must append to its transient-firewall slice (so
// the standard stop/quarantine/failed-start teardown removes them).
//
// When egress mediation is off (EgressMediationOn(config.EgressMode) is false)
// it is a no-op: it returns (0, nil, nil) and the guest's egress is unmediated.
//
// Fail-closed: on ANY failure it tears down everything IT started (CA files,
// mediator process, TPROXY ip rule/route in user mode) and returns the error.
// It does NOT touch the caller's tap or base NAT rules — the caller unwinds those
// with its own cleanupTransient* discipline, exactly as the inline path did.
//
// mode selects the TPROXY prerequisite ownership model and must be "user" or a
// host-netns mode ("nat", "named"):
//   - "user" (pasta) mode: the sysctls/ip-rule/local-route are netns-local and
//     reaped with the ephemeral netns; we provision them here.
//   - host-netns modes ("nat", "named"): those are host-global infra owned by
//     `host setup-networking`; we only VERIFY them (fail-closed if absent) and
//     install just the per-workspace nft tproxy rule. Anything other than "user"
//     takes this verify-only path.
func provisionEgressMediation(opts Options, config *vmkit.Config, mode, tap, gateway, subnet string, peers []string, restore bool, expectedCASHA string) (int, []transientFirewallRule, error) {
	if config == nil || !vmkit.EgressMediationOn(config.EgressMode) {
		return 0, nil, nil
	}
	var rules []transientFirewallRule
	// Acquire the per-workspace CA. On a fresh start we mint one and persist it;
	// on a snapshot restore/fork we REUSE the persisted CA the guest's baked trust
	// store was built against (re-minting would silently break every MITM
	// handshake of the restored guest). cleanupCA removes the CA files only when
	// we minted them this call — on reuse it is a no-op so a downstream failure
	// never deletes the workspace's persistent CA.
	caCertPath, caKeyPath, cleanupCA, caErr := acquireEgressCA(opts, restore, expectedCASHA)
	if caErr != nil {
		return 0, nil, caErr
	}
	pid, port, eerr := startEgressMediator(opts, gateway, config.EgressMode, config.EgressAllow, config.EgressPassthrough, peers, caCertPath, caKeyPath, egressCapsFromConfig(config))
	if eerr != nil {
		cleanupCA()
		return 0, nil, eerr
	}
	redirect, rerr := installEgressRedirectRule(tap, subnet, uint16(port))
	if rerr != nil {
		terminateAuxProcess(pid)
		cleanupCA()
		return 0, nil, rerr
	}
	rules = append(rules, redirect)

	// UDP mediation via TPROXY. The mediator already binds a transparent UDP
	// socket on gateway:port (same addr:port as its TCP listener); the supervisor
	// steers guest UDP there. This is fail-closed: any failure here (TPROXY
	// modules absent, missing host prerequisites, etc.) tears down EVERYTHING this
	// helper already provisioned for the workspace and returns the guiding error
	// so the start aborts rather than booting a guest whose UDP escapes the
	// mediator.
	//
	// netnsLocal distinguishes the two prerequisite ownership models:
	//   - user (pasta) mode: the sysctls/ip-rule/local-route are netns-local and
	//     reaped with the ephemeral netns; we provision them here.
	//   - host-netns modes (nat, named): those are host-global infra owned by
	//     `host setup-networking`; we only VERIFY them (fail-closed if absent) and
	//     install just the per-workspace nft tproxy rule.
	netnsLocal := mode == "user"
	undoRouting, perr := prepareEgressTProxyNetns(netnsLocal, egressTProxyMark, egressTProxyTable)
	if perr != nil {
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: UDP mediation (TPROXY) unavailable for workspace %s — run 'microagent host setup-networking' or use --egress off: %w", opts.Name, perr)
	}
	mediatorAddr := netip.AddrPortFrom(netip.MustParseAddr(gateway), uint16(port))
	tproxy, terr := installEgressTProxyRule(tap, subnet, egressTProxyMark, mediatorAddr)
	if terr != nil {
		undoRouting()
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: UDP mediation (TPROXY) unavailable for workspace %s — run 'microagent host setup-networking' or use --egress off: %w", opts.Name, terr)
	}
	// The nft tproxy rule joins the returned rules slice so the standard firewall
	// teardown (stop/quarantine/failed-start) removes it. The ip rule/local route
	// are NOT firewall rules: in user mode they vanish with the ephemeral pasta
	// netns (no per-stop teardown needed); in host-netns modes they are host infra
	// we did not create here (undoRouting is a no-op). undoRouting is therefore
	// only meaningful on the failure paths above.
	rules = append(rules, tproxy)

	// Fail-closed IPv6 drop. The REDIRECT/TPROXY steering above is IPv4-only
	// (nfproto ipv4) and the tap plan hands the guest an IPv4-only address, so v6
	// is not a live leak today. But a guest that ever acquired a v6 address while
	// mediated would have its v6 egress slip past the v4-only capture — an
	// unmediated channel. We drop ALL guest v6 egress at the firewall so the
	// "mediation is complete" invariant holds for the not-yet-mediated v6 path.
	// Same fail-closed discipline as the steering rules: on failure tear down
	// everything this helper provisioned and abort the start.
	v6drop, v6err := installEgressV6DropRule(tap)
	if v6err != nil {
		undoRouting()
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: IPv6 fail-closed drop unavailable for workspace %s: %w", opts.Name, v6err)
	}
	rules = append(rules, v6drop)

	// Tier 5: drop-and-audit guest IPv4 L4 traffic that is neither TCP
	// (REDIRECT-mediated above) nor UDP (TPROXY-mediated above) — ICMP and any
	// other protocol with no allowlistable destination identity. With TCP and UDP
	// already mediated, dropping the rest at the firewall completes IPv4 mediation
	// ("mediation is complete"): allowing ICMP echo etc. would be an unmediated
	// covert/exfil + liveness-leak channel. The three precedence rules (accept
	// tcp, accept udp, catch-all nflog+drop) share the filter chain with the v6
	// drop and audit drops via nflog, not the mediator JSONL. Same fail-closed
	// discipline: on failure tear down everything this helper provisioned and
	// abort the start.
	l4drops, l4err := installEgressL4DropRule(tap, subnet)
	if l4err != nil {
		undoRouting()
		terminateAuxProcess(pid)
		cleanupCA()
		cleanupTransientFirewallRules(rules)
		return 0, nil, fmt.Errorf("egress: non-TCP/UDP fail-closed drop unavailable for workspace %s: %w", opts.Name, l4err)
	}
	rules = append(rules, l4drops...)
	return pid, rules, nil
}

// acquireEgressCA returns the on-disk paths to the per-workspace egress CA cert
// and key for the mediator, plus a cleanup closure to invoke on a downstream
// failure. It has two clearly separated branches:
//
//   - Fresh start (restore=false): mint a new ECDSA CA, persist egress-ca.pem
//     (0644, public — delivered to the guest) and egress-ca-key.pem (0600, host
//     only), and return a cleanup that removes BOTH files (we created them).
//     This path is byte-identical to the pre-restore implementation.
//
//   - Restore/fork (restore=true): REUSE the persisted CA the guest's baked trust
//     store was built against. Read egress-ca.pem + egress-ca-key.pem, compute the
//     cert DER SHA-256, and fail closed if either file is missing or the
//     fingerprint differs from the snapshot manifest's expectedCASHA — a mismatch
//     means the on-disk CA is not the one the guest trusts, so minting/serving any
//     other CA would silently break MITM. No egress.NewCA call, no write. The
//     returned cleanup is a NO-OP so a downstream failure never deletes the
//     workspace's persistent CA.
func acquireEgressCA(opts Options, restore bool, expectedCASHA string) (caCertPath, caKeyPath string, cleanup func(), err error) {
	wsDir := filepath.Join(opts.StateDir, opts.Name)
	caCertPath = filepath.Join(wsDir, "egress-ca.pem")
	caKeyPath = filepath.Join(wsDir, "egress-ca-key.pem")
	noop := func() {}

	if restore {
		// Reuse the persisted CA. Fail closed on any divergence from the manifest.
		if expectedCASHA == "" {
			return "", "", noop, fmt.Errorf("egress: restore of mediated workspace %s has no recorded CA fingerprint; refusing to re-arm the mediator", opts.Name)
		}
		if _, statErr := os.Stat(caKeyPath); statErr != nil {
			return "", "", noop, fmt.Errorf("egress: restore of workspace %s cannot reuse CA key: %w", opts.Name, statErr)
		}
		gotSHA, shaErr := egressCACertSHA256(wsDir)
		if shaErr != nil {
			return "", "", noop, fmt.Errorf("egress: restore of workspace %s cannot reuse CA cert: %w", opts.Name, shaErr)
		}
		if gotSHA != expectedCASHA {
			return "", "", noop, fmt.Errorf("egress: restore of workspace %s refused — persisted CA fingerprint %s does not match snapshot fingerprint %s; the guest's baked trust store would reject the mediator", opts.Name, gotSHA, expectedCASHA)
		}
		return caCertPath, caKeyPath, noop, nil
	}

	// Fresh start: mint a per-workspace CA. The cert (public) is delivered to the
	// guest over the cacert vsock listener so guestinit installs it in the trust
	// store. The key stays on the host and is passed to the mediator for TLS MITM.
	ca, caErr := egress.NewCA(opts.Name, 720*time.Hour)
	if caErr != nil {
		return "", "", noop, fmt.Errorf("mint egress CA for %s: %w", opts.Name, caErr)
	}
	caKeyPEM, caErr := ca.KeyPEM()
	if caErr != nil {
		return "", "", noop, fmt.Errorf("encode egress CA key for %s: %w", opts.Name, caErr)
	}
	if caErr = os.MkdirAll(wsDir, 0o700); caErr != nil {
		return "", "", noop, fmt.Errorf("create workspace dir for egress CA: %w", caErr)
	}
	if caErr = os.WriteFile(caCertPath, ca.CertPEM(), 0o644); caErr != nil {
		return "", "", noop, fmt.Errorf("write egress CA cert: %w", caErr)
	}
	if caErr = os.WriteFile(caKeyPath, caKeyPEM, 0o600); caErr != nil {
		_ = os.Remove(caCertPath)
		return "", "", noop, fmt.Errorf("write egress CA key: %w", caErr)
	}
	cleanup = func() {
		_ = os.Remove(caCertPath)
		_ = os.Remove(caKeyPath)
	}
	return caCertPath, caKeyPath, cleanup, nil
}

// prepareNamedNetworkForStart joins a workspace to a user-defined named network:
// it allocates a stable address from the network's subnet, ensures the shared
// Linux bridge exists with the gateway address, attaches a TAP to the bridge,
// installs masquerade rules for outbound traffic, and returns a runtime network
// config carrying /etc/hosts entries for every member so they resolve by name.
func prepareNamedNetworkForStart(opts Options, config *vmkit.Config, restore bool, expectedCASHA string) ([]transientNetworkDevice, []transientFirewallRule, *vmkit.NetworkConfig, int, error) {
	if err := requireIPv4Forwarding(); err != nil {
		return nil, nil, nil, 0, err
	}
	name := strings.TrimSpace(config.Network.Name)
	record, err := network.Get(opts.StateDir, name)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("join named network: %w", err)
	}
	ip, err := network.Join(opts.StateDir, name, opts.Name)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("allocate address on network %q: %w", name, err)
	}
	// Refresh the record so the /etc/hosts entries include this member.
	record, err = network.Get(opts.StateDir, name)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	prefix, err := subnetPrefixLen(record.Subnet)
	if err != nil {
		return nil, nil, nil, 0, err
	}

	bridge := bridgeName(name)
	if err := ensureNetworkBridge(bridge, record.Gateway, prefix); err != nil {
		return nil, nil, nil, 0, err
	}
	tap := tapName(opts)
	device := transientNetworkDevice{Name: tap, Mode: "tap", Interface: bridge, Created: true}
	if err := createBridgeTap(tap, bridge); err != nil {
		return nil, nil, nil, 0, networkPrivilegeError("attach firecracker tap to network bridge "+bridge, err)
	}
	cleanupDevices := []transientNetworkDevice{device}
	rules, err := installNATFirewallRules(tap, record.Subnet)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, err
	}

	// Full egress mediation (CA + mediator + TCP REDIRECT + UDP TPROXY) so all
	// guest egress — including east-west VM↔VM traffic within the named bridge —
	// is captured by the mediator. A named network runs in the HOST netns (like
	// nat, not pasta), so the helper takes the host-global verify-only TPROXY path.
	// Fail-closed: the helper unwinds its own provisioning on error; we still tear
	// down the tap and base NAT rules we created above, mirroring the nat path.
	//
	// Hand the mediator the named-network peer roster (every OTHER member's
	// name↔IP) so it reverse-resolves a bare-IP east-west destination to the peer's
	// workspace name and polices it by name under the same default-deny allowlist.
	egressPID, egressRules, err := provisionEgressMediation(opts, config, "named", tap, record.Gateway, record.Subnet, namedNetworkPeers(record, opts.Name), restore, expectedCASHA)
	if err != nil {
		cleanupTransientFirewallRules(rules)
		cleanupTransientNetworkDevices(cleanupDevices)
		return nil, nil, nil, 0, err
	}
	rules = append(rules, egressRules...)

	runtime := vmkit.NetworkConfig{
		Mode:    "named",
		Name:    name,
		IP:      ip + "/" + strconv.Itoa(prefix),
		Subnet:  record.Subnet,
		Gateway: record.Gateway,
		Routes:  []string{"0.0.0.0/0 via " + record.Gateway},
		Hosts:   namedNetworkHosts(record),
	}
	if config.Network != nil && len(config.Network.DNS) != 0 {
		runtime.DNS = config.Network.DNS
	} else {
		runtime.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}
	return cleanupDevices, rules, &runtime, egressPID, nil
}

// namedNetworkHosts renders one "name:ip" entry per member for the guest
// /etc/hosts file.
func namedNetworkHosts(record network.Record) []string {
	hosts := make([]string, 0, len(record.Members))
	for _, m := range record.Members {
		hosts = append(hosts, m.Workspace+":"+m.IP)
	}
	return hosts
}

// namedNetworkPeers renders the egress mediator's peer roster as "name=ip" pairs,
// one per OTHER member of the network (this workspace's own entry is excluded —
// the mediator never reverse-resolves a flow to "self"). The mediator uses it to
// police east-west VM↔VM traffic by the peer's workspace name under the same
// default-deny allowlist as external hosts.
func namedNetworkPeers(record network.Record, self string) []string {
	peers := make([]string, 0, len(record.Members))
	for _, m := range record.Members {
		if m.Workspace == self {
			continue
		}
		peers = append(peers, m.Workspace+"="+m.IP)
	}
	return peers
}

// bridgeName derives a stable, valid (<=15 char) Linux bridge name for a named
// network. The "mbr" prefix marks it as microagent-managed so cleanup may reap
// it without touching operator-provided bridges used by bridged mode.
func bridgeName(networkName string) string {
	sum := sha1.Sum([]byte(networkName))
	return fmt.Sprintf("mbr%x", sum[:4])
}

func isManagedNetworkBridge(name string) bool {
	return strings.HasPrefix(name, "mbr") && len(name) == 11
}

func subnetPrefixLen(subnet string) (int, error) {
	_, parsed, err := net.ParseCIDR(subnet)
	if err != nil {
		return 0, fmt.Errorf("parse network subnet %q: %w", subnet, err)
	}
	ones, _ := parsed.Mask.Size()
	return ones, nil
}

// ensureNetworkBridge creates the shared bridge if absent, assigns the gateway
// address, and brings it up. It is idempotent so concurrent member starts and
// restarts converge on the same bridge.
func ensureNetworkBridge(bridge, gateway string, prefix int) error {
	link, err := netlink.LinkByName(bridge)
	if err != nil {
		if !linkNotFoundError(err) {
			return networkPrivilegeError("inspect network bridge "+bridge, err)
		}
		if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: bridge}}); err != nil && !alreadyExistsError(err) {
			return networkPrivilegeError("create network bridge "+bridge, err)
		}
		link, err = netlink.LinkByName(bridge)
		if err != nil {
			return networkPrivilegeError("inspect network bridge "+bridge, err)
		}
	}
	gwCIDR := gateway + "/" + strconv.Itoa(prefix)
	addr, err := netlink.ParseAddr(gwCIDR)
	if err != nil {
		return fmt.Errorf("parse network bridge address %s: %w", gwCIDR, err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil && !alreadyExistsError(err) {
		return networkPrivilegeError("assign network bridge address "+gwCIDR, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return networkPrivilegeError("bring network bridge "+bridge+" up", err)
	}
	return nil
}

// reapNetworkBridgeIfEmpty deletes a microagent-managed network bridge once it
// has no remaining enslaved interfaces, so a stopped last member leaves no
// orphan bridge. Best-effort: any error is non-fatal to teardown.
func reapNetworkBridgeIfEmpty(bridge string) {
	if !isManagedNetworkBridge(bridge) {
		return
	}
	entries, err := os.ReadDir(filepath.Join("/sys/class/net", bridge, "brif"))
	if err != nil || len(entries) > 0 {
		return
	}
	if link, err := netlink.LinkByName(bridge); err == nil {
		_ = netlink.LinkDel(link)
	}
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
			// For named networks the TAP is enslaved to a shared managed bridge;
			// reap that bridge once this was its last member.
			reapNetworkBridgeIfEmpty(device.Interface)
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
		if rule.Chain != nftNATPostroutingChain && rule.Chain != nftForwardChain && rule.Chain != nftNATPreroutingChain && rule.Chain != nftManglePreroutingChain && rule.Chain != nftFilterPreroutingChain {
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
	logPath := portForwarderLogPath(opts)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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

func portForwarderLogPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "port-forward.log")
}

// startEgressMediator allocates a free port on bindHost, spawns a detached
// `microagent-firecracker-supervisor --egress-mediator` in the CURRENT netns
// (host for nat, pasta for user mode — in user mode the spawning supervisor is
// the in-netns re-exec, so the child inherits the pasta netns), waits until it
// accepts, and returns (pid, port). Uses the same detached-spawn mechanism as
// the port-forwarder companion.
//
// bindHost must be the tap gateway IP (e.g. "10.43.29.1") because the nftables
// REDIRECT target rewrites the destination to the primary address of the
// incoming interface — i.e. the tap host-side IP — not 127.0.0.1.
//
// caCertPath and caKeyPath, when non-empty, enable TLS interception: the
// mediator loads the per-workspace CA and signs per-SNI leaf certs on the fly.
// passthrough lists hosts whose TLS is forwarded opaquely (not intercepted).
// egressMediatorArgs builds the argv for the detached
// `microagent-firecracker-supervisor --egress-mediator` child. Pure (no I/O) so
// it can be unit-tested. The mode ("mediated"/"strict") is threaded to the
// mediator via --mode; an empty mode is normalized to the secure default.
// egressCaps carries the bounded-operations caps (ASK tenet 8) from the workspace
// Config into egressMediatorArgs. All fields default to zero = unlimited (current
// behavior), so an unset config produces argv byte-identical to the pre-caps one.
type egressCaps struct {
	maxBytesPerSec  int64
	maxTotalBytes   int64
	maxConns        int32
	auditMaxBytes   int64
	auditMaxBackups int
}

// applyManifestEgressCaps re-applies the bounded-operations caps recorded in a
// snapshot manifest onto the restore request's Config (ASK tenet 8), so a
// restored/forked workspace keeps the SAME bounds it was snapshotted under —
// mirroring how the persisted CA is reused. A no-op when config is nil. Manifest
// values overwrite the config's so the snapshot is authoritative for the restored
// posture (the manifest reproduces what the workspace was actually running).
func applyManifestEgressCaps(config *vmkit.Config, manifest vmkit.SnapshotManifest) {
	if config == nil {
		return
	}
	config.EgressMaxBytesPerSec = manifest.EgressMaxBytesPerSec
	config.EgressMaxTotalBytes = manifest.EgressMaxTotalBytes
	config.EgressMaxConcurrentConns = manifest.EgressMaxConcurrentConns
	config.EgressAuditMaxBytes = manifest.EgressAuditMaxBytes
	config.EgressAuditMaxBackups = manifest.EgressAuditMaxBackups
}

// egressCapsFromConfig extracts the caps from a workspace Config. Nil config (or
// all-zero caps) yields a zero egressCaps (unlimited).
func egressCapsFromConfig(config *vmkit.Config) egressCaps {
	if config == nil {
		return egressCaps{}
	}
	return egressCaps{
		maxBytesPerSec:  config.EgressMaxBytesPerSec,
		maxTotalBytes:   config.EgressMaxTotalBytes,
		maxConns:        config.EgressMaxConcurrentConns,
		auditMaxBytes:   config.EgressAuditMaxBytes,
		auditMaxBackups: config.EgressAuditMaxBackups,
	}
}

func egressMediatorArgs(bindHost string, port int, auditPath, mode string, allow, passthrough, peers []string, caCertPath, caKeyPath string, caps egressCaps) []string {
	args := []string{"--egress-mediator", "--bind-host", bindHost, "--bind-port", strconv.Itoa(port), "--audit-log", auditPath, "--mode", vmkit.NormalizeEgressMode(mode)}
	for _, h := range allow {
		args = append(args, "--allow", h)
	}
	if caCertPath != "" && caKeyPath != "" {
		args = append(args, "--ca-cert", caCertPath, "--ca-key", caKeyPath)
	}
	for _, h := range passthrough {
		args = append(args, "--passthrough", h)
	}
	// Named-network peer roster (name=ip). Empty for nat/user (no roster). The
	// mediator reverse-resolves a bare-IP east-west destination to the peer's
	// workspace name and polices it by name under the same default-deny allowlist.
	for _, p := range peers {
		args = append(args, "--peer", p)
	}
	// Bounded-operations caps (ASK tenet 8). Each is emitted only when non-zero so
	// an uncapped workspace's argv is byte-identical to the pre-caps one.
	if caps.maxBytesPerSec > 0 {
		args = append(args, "--max-bps", strconv.FormatInt(caps.maxBytesPerSec, 10))
	}
	if caps.maxTotalBytes > 0 {
		args = append(args, "--max-bytes", strconv.FormatInt(caps.maxTotalBytes, 10))
	}
	if caps.maxConns > 0 {
		args = append(args, "--max-conns", strconv.Itoa(int(caps.maxConns)))
	}
	if caps.auditMaxBytes > 0 {
		args = append(args, "--audit-max-bytes", strconv.FormatInt(caps.auditMaxBytes, 10))
		if caps.auditMaxBackups > 0 {
			args = append(args, "--audit-max-backups", strconv.Itoa(caps.auditMaxBackups))
		}
	}
	return args
}

func startEgressMediator(opts Options, bindHost, mode string, allow, passthrough, peers []string, caCertPath, caKeyPath string, caps egressCaps) (int, int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		return 0, 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // port-allocation race is bounded: mediator readiness probe retries until it accepts
	exe, err := os.Executable()
	if err != nil {
		return 0, 0, err
	}
	auditPath := filepath.Join(opts.StateDir, opts.Name, "egress-access.jsonl")
	args := egressMediatorArgs(bindHost, port, auditPath, mode, allow, passthrough, peers, caCertPath, caKeyPath, caps)
	logPath := filepath.Join(opts.StateDir, opts.Name, "egress-mediator.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, err
	}
	// The logfile is opened O_APPEND and reused across mediator restarts, so a
	// stale readiness marker from a PRIOR run may already be present. Record the
	// current size and scan only bytes written AFTER this offset, so the marker
	// check observes this child's signal and never a stale one (which would be a
	// false-positive ready).
	var logStart int64
	if info, serr := logFile.Stat(); serr == nil {
		logStart = info.Size()
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	// Readiness requires BOTH the TCP listener to accept AND the mediator to have
	// emitted its post-UDP readiness marker to its logfile. The TCP listener
	// binds (in egress.Run) before the transparent UDP socket opens, so a
	// TCP-dial-only probe can pass during the window where UDP has not yet come
	// up — and if the UDP open then fails the mediator exits, leaving a confusing
	// half-provisioned start. Gating on the marker (written only after UDP is up)
	// closes that window. Fail-closed is preserved: a mediator that never signals
	// ready (never opens UDP) → the marker never appears → the deadline trips and
	// the start aborts after terminating the child.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, derr := net.DialTimeout("tcp", net.JoinHostPort(bindHost, strconv.Itoa(port)), 200*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			if egressMediatorLoggedReady(logPath, logStart) {
				return pid, port, nil
			}
		}
		if time.Now().After(deadline) {
			terminateAuxProcess(pid)
			return 0, 0, fmt.Errorf("egress mediator did not become ready on %s:%d", bindHost, port)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// egressMediatorLoggedReady reports whether the mediator's logfile contains the
// post-UDP readiness marker (egress.ReadyMarker) in the bytes written at or
// after startOffset. The offset scoping ignores any stale marker from a prior
// run that shares this append-mode logfile. A read error (e.g. the file not yet
// created) reports false so the caller keeps polling until the deadline.
func egressMediatorLoggedReady(logPath string, startOffset int64) bool {
	f, err := os.Open(logPath)
	if err != nil {
		return false
	}
	defer f.Close()
	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return false
		}
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), egress.ReadyMarker) {
			return true
		}
	}
	return false
}

func startReadyPortForwarderProcess(ctx context.Context, opts Options, config vmkit.Config) (int, error) {
	pid, err := startPortForwarderProcess(opts)
	if err != nil {
		return 0, err
	}
	if err := waitForPortForwarderReady(ctx, pid, config, 5*time.Second); err != nil {
		terminateAuxProcess(pid)
		return 0, fmt.Errorf("start port forwarder: %w; see %s", err, portForwarderLogPath(opts))
	}
	return pid, nil
}

func startReadyPortForwarderProcessWithManagementPortRetry(ctx context.Context, opts Options, config *vmkit.Config, persistRuntimeConfig func() error) (int, error) {
	if config == nil {
		return 0, fmt.Errorf("start port forwarder: missing runtime config")
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		pid, err := startReadyPortForwarderProcess(ctx, opts, *config)
		if err == nil {
			return pid, nil
		}
		lastErr = err
		if attempt == 2 || !moveManagementHostPorts(config) {
			break
		}
		if persistRuntimeConfig != nil {
			if err := persistRuntimeConfig(); err != nil {
				return 0, err
			}
		}
	}
	return 0, lastErr
}

func moveManagementHostPorts(config *vmkit.Config) bool {
	if config == nil {
		return false
	}
	excluded := map[uint16]bool{}
	if config.ShellPort != 0 {
		excluded[config.ShellPort] = true
	}
	if config.ExecPort != 0 {
		excluded[config.ExecPort] = true
	}
	changed := false
	if config.ShellPort != 0 {
		if port, ok := replacementHostPort(excluded); ok {
			if config.GuestShellPort == 0 {
				config.GuestShellPort = config.ShellPort
			}
			config.ShellPort = port
			excluded[port] = true
			changed = true
		}
	}
	if config.ExecPort != 0 {
		if port, ok := replacementHostPort(excluded); ok {
			if config.GuestExecPort == 0 {
				config.GuestExecPort = config.ExecPort
			}
			config.ExecPort = port
			excluded[port] = true
			changed = true
		}
	}
	return changed
}

func replacementHostPort(excluded map[uint16]bool) (uint16, bool) {
	for i := 0; i < 20; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, false
		}
		port := uint16(listener.Addr().(*net.TCPAddr).Port)
		_ = listener.Close()
		if port != 0 && !excluded[port] {
			return port, true
		}
	}
	return 0, false
}

func waitForPortForwarderReady(ctx context.Context, pid int, config vmkit.Config, timeout time.Duration) error {
	forwards := portForwarderForwards(config)
	if len(forwards) == 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		active, err := processActive(pid)
		if err != nil {
			return fmt.Errorf("inspect port forwarder process %d: %w", pid, err)
		}
		if !active {
			return fmt.Errorf("port forwarder process %d exited before listeners became ready", pid)
		}
		ready := true
		for _, forward := range forwards {
			if forward.Protocol != "" && forward.Protocol != "tcp" {
				continue
			}
			target := portForwardDialTarget(forward)
			conn, err := net.DialTimeout("tcp", target, 50*time.Millisecond)
			if err != nil {
				ready = false
				lastErr = fmt.Errorf("dial %s: %w", target, err)
				break
			}
			_ = conn.Close()
		}
		if ready {
			active, err := processActive(pid)
			if err != nil {
				return fmt.Errorf("inspect port forwarder process %d: %w", pid, err)
			}
			if !active {
				return fmt.Errorf("port forwarder process %d exited after listeners became reachable", pid)
			}
			return nil
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("listeners not ready after %s: %w", timeout, lastErr)
			}
			return fmt.Errorf("listeners not ready after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func portForwardDialTarget(forward vmkit.PortForward) string {
	host := strings.TrimSpace(forward.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(int(forward.HostPort)))
}

func startVsockListenerProcess(opts Options) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := filepath.Join(opts.StateDir, opts.Name, "vsock-listener.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
	watchWorkspaceRuntime(ctx, opts)
	return ctx.Err()
}

// watchWorkspaceRuntime blocks until the workspace runtime indicates the
// companion process should exit. Companions are daemonized and survive their
// parent, so this poll is what bounds their lifetime to the VM's: when the
// guest exits on its own, no lifecycle verb runs to reap them (ASK tenet 8:
// operations are bounded, not unlimited by default).
func watchWorkspaceRuntime(ctx context.Context, opts Options) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if companionShouldExit(opts) {
				return
			}
		}
	}
}

// companionShouldExit reports whether a companion process (port forwarder or
// vsock listener) has outlived its workspace: the runtime state is gone
// (deleted), the workspace reached a terminal state, or the recorded VM
// process is no longer running.
func companionShouldExit(opts Options) bool {
	state, err := readRuntimeState(opts)
	if err != nil {
		return true
	}
	switch state.Event.State {
	case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused:
	default:
		return true
	}
	if state.PID != 0 {
		if active, err := processActive(state.PID); err == nil && !active {
			return true
		}
	}
	return false
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
		fmt.Fprintf(os.Stderr, "forward tcp %s to guest vsock port %d\n", addr, forward.GuestPort)
		listeners = append(listeners, listener)
		go servePortForward(listener, vsockSocketPath(opts), uint32(forward.GuestPort))
	}
	watchWorkspaceRuntime(ctx, opts)
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
			GuestPort: guestShellPort(config),
		})
	}
	if config.ExecPort != 0 {
		forwards = append(forwards, vmkit.PortForward{
			Protocol:  "tcp",
			Host:      "127.0.0.1",
			HostPort:  config.ExecPort,
			GuestPort: guestExecPort(config),
		})
	}
	return forwards
}

// guestShellPort and guestExecPort return the in-guest vsock port for the shell
// and exec services, which differs from the host-side port only for a fork that
// resumed a guest listening on the source's ports.
func guestShellPort(config vmkit.Config) uint16 {
	if config.GuestShellPort != 0 {
		return config.GuestShellPort
	}
	return config.ShellPort
}

func guestExecPort(config vmkit.Config) uint16 {
	if config.GuestExecPort != 0 {
		return config.GuestExecPort
	}
	return config.ExecPort
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
		if listener.Target == secretsListenerTarget {
			bundle, err := resolveSecretsBundle(context.Background(), config)
			if err != nil {
				set.Close()
				return nil, fmt.Errorf("resolve secrets: %w", err)
			}
			path := firecrackerGuestVsockPath(opts, listener.Port)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				set.Close()
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				set.Close()
				return nil, err
			}
			unixListener, err := net.Listen("unix", path)
			if err != nil {
				set.Close()
				return nil, fmt.Errorf("listen secrets vsock port %d: %w", listener.Port, err)
			}
			// The secrets socket carries the plaintext bundle, so restrict it to
			// the owner (firecracker runs as the same user). Default socket perms
			// are world-accessible.
			if err := os.Chmod(path, 0o600); err != nil {
				_ = unixListener.Close()
				set.Close()
				return nil, fmt.Errorf("restrict secrets vsock socket %d: %w", listener.Port, err)
			}
			onDemand := make(map[string]string, len(config.OnDemandSecrets))
			for _, ref := range config.OnDemandSecrets {
				onDemand[ref.Name] = ref.Ref
			}
			srv := newSecretsServer(opts.Name, opts.StateDir, bundle, onDemand, config.SecretsAudit)
			set.listeners = append(set.listeners, unixListener)
			go serveSecretsListener(unixListener, srv)
			continue
		}
		if listener.Target == secretxfer.CACertTarget {
			path := firecrackerGuestVsockPath(opts, listener.Port)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				set.Close()
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				set.Close()
				return nil, err
			}
			unixListener, err := net.Listen("unix", path)
			if err != nil {
				set.Close()
				return nil, fmt.Errorf("listen cacert vsock port %d: %w", listener.Port, err)
			}
			// CA cert is not secret (it is installed in the guest trust store), so
			// default socket permissions are fine here.
			caCertPath := filepath.Join(opts.StateDir, opts.Name, "egress-ca.pem")
			set.listeners = append(set.listeners, unixListener)
			go serveCACertListener(unixListener, caCertPath)
			continue
		}
		if !isAllowedVsockTarget(opts, listener.Target) {
			set.Close()
			return nil, fmt.Errorf("firecracker vsock listener %d target must be host:port or the workspace result path", listener.Port)
		}
		path := firecrackerGuestVsockPath(opts, listener.Port)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			set.Close()
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

// serveCACertListener accepts guest connections and sends the egress CA cert
// PEM (caCertPath) to each. The cert is written by prepareTAPNATForStart
// before any listeners are served, so the file exists when connections arrive.
// If the file is missing or unreadable, the connection is logged and closed.
func serveCACertListener(listener net.Listener, caCertPath string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			pem, err := os.ReadFile(caCertPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read cacert for vsock guest: %v\n", err)
				return
			}
			if err := secretxfer.ServeCACert(c, pem); err != nil {
				fmt.Fprintf(os.Stderr, "serve cacert to guest: %v\n", err)
			}
		}(conn)
	}
}

func handleGuestVsockConnection(conn net.Conn, target string) {
	const maxResultBytes int64 = 16 * 1024 * 1024
	defer func() { _ = conn.Close() }()
	if tcpTarget, ok := parseTCPAddr(target); ok {
		remote, err := net.Dial("tcp", tcpTarget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect vsock target %s: %v\n", tcpTarget, err)
			return
		}
		defer func() { _ = remote.Close() }()
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
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "create result directory for %s: %v\n", target, err)
		return
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
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
	defer func() { _ = conn.Close() }()
	vsock, reader, err := dialGuestVsock(udsPath, guestPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect guest vsock port %d: %v\n", guestPort, err)
		return
	}
	defer func() { _ = vsock.Close() }()
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
		if state.EgressMediatorPID != 0 {
			terminateAuxProcess(state.EgressMediatorPID)
		}
		cleanupTransientFirewallRules(state.FirewallRules)
		cleanupTransientNetworkDevices(state.NetworkDevices)
		cleanupUserNetworkProcess(opts)
		req := runtimeStateRequest(vmkit.Request{}, state)
		if err := writeProcessStateWithProcessesAndNetwork(opts, req, finalState, 0, 0, 0, 0, nil, nil, errorText); err != nil {
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
	return writeProcessStateWithProcessesAndNetwork(opts, req, state, pid, portForwardPID, 0, 0, networkDevices, firewallRules, errorText)
}

func writeProcessStateWithProcessesAndNetwork(opts Options, req vmkit.Request, state vmkit.VMState, pid, portForwardPID, vsockListenerPID, egressMediatorPID int, networkDevices []transientNetworkDevice, firewallRules []transientFirewallRule, errorText string) error {
	if req.Identity == nil || req.Config == nil {
		return fmt.Errorf("workspace request is missing identity or config")
	}
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
		Event:             fileEvent,
		Config:            *req.Config,
		PID:               pid,
		PortForwardPID:    portForwardPID,
		VsockListenerPID:  vsockListenerPID,
		EgressMediatorPID: egressMediatorPID,
		NetworkDevices:    append([]transientNetworkDevice{}, networkDevices...),
		FirewallRules:     append([]transientFirewallRule{}, firewallRules...),
		SerialLogPath:     serialLogPath(opts),
		SerialInputPath:   serialInputPath(opts),
		UpdatedAt:         now.Format(time.RFC3339),
		Error:             errorText,
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
	if detail, ok := inactivePortForwarderDetail(state); ok {
		return vmkit.ReadinessSignal{
			Ready:      false,
			ObservedAt: &observedAt,
			Detail:     detail,
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
		if detail, ok := inactivePortForwarderDetail(state); ok {
			return vmkit.ReadinessSignal{
				Ready:      false,
				ObservedAt: &observedAt,
				Detail:     detail,
			}, true
		}
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

func inactivePortForwarderDetail(state runtimeState) (string, bool) {
	if state.PortForwardPID == 0 {
		return "", false
	}
	active, err := processActive(state.PortForwardPID)
	if err != nil || active {
		return "", false
	}
	return fmt.Sprintf("port forwarder process %d is not running", state.PortForwardPID), true
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

// runtimeStateRequest rebuilds a request whose config is the recorded runtime
// config, so state rewrites (stop, pause/resume, snapshot, apply) never drop
// runtime fields the originating verb's sparse config does not carry. Only the
// caller's StateDir is preserved; everything else comes from the state file —
// a field-by-field copy here is exactly how shell/exec ports were once lost.
func runtimeStateRequest(req vmkit.Request, state runtimeState) vmkit.Request {
	if req.Identity == nil {
		identity := state.Event.Identity
		req.Identity = &identity
	}
	config := state.Config
	if req.Config != nil && strings.TrimSpace(req.Config.StateDir) != "" {
		config.StateDir = req.Config.StateDir
	}
	req.Config = &config
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

// terminateRecordedCompanions kills any companion processes recorded in the
// workspace runtime state. Only the recorded companion PIDs are touched; the
// VM process entry is left alone.
func terminateRecordedCompanions(opts Options) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return
	}
	if state.PortForwardPID != 0 {
		terminateAuxProcess(state.PortForwardPID)
	}
	if state.VsockListenerPID != 0 {
		terminateAuxProcess(state.VsockListenerPID)
	}
	if state.EgressMediatorPID != 0 {
		terminateAuxProcess(state.EgressMediatorPID)
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
	if err := tmp.Chmod(0o600); err != nil {
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
