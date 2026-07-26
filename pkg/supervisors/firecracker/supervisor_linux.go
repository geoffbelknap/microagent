//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type Options struct {
	Name               string
	StateDir           string
	Timeout            time.Duration
	FirecrackerPath    string
	ResolveFirecracker func() (string, error)
	// Confinement selects the VMM-process confinement mode for this backend:
	// "auto" (default), "off", "jailer", or "rootless". Resolved from
	// MICROAGENT_CONFINEMENT. Inert until the launch path wires it in.
	Confinement string
}

type Supervisor struct {
	Options Options
}

func (s Supervisor) Do(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	if err := vmkit.ValidateRequest(req); err != nil {
		return vmkit.Response{}, err
	}
	if err := validateFirecrackerRequest(req); err != nil {
		return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
	}
	opts := s.normalizedOptions(req)
	switch req.Command {
	case "host":
		return hostResponse(opts)
	case "check":
		return vmkit.Response{OK: true, Backend: vmkit.BackendLinuxKVM}, nil
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
	case "gc":
		return gcWorkspace(opts)
	case "halt":
		return stopWorkspace(ctx, opts, req, syscall.SIGTERM, vmkit.StateHalted)
	case "quarantine":
		return quarantineWorkspace(ctx, opts, req)
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
			return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
		}
		cleanupWorkspaceState(opts)
		return eventResponse(req, vmkit.StateStopped, ""), nil
	case "console":
		err := fmt.Errorf("firecracker supervisor console command is unsupported; use serial input FIFO")
		return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
	default:
		err := fmt.Errorf("unknown firecracker command %q", req.Command)
		return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
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
	case "user", "isolated":
		return nil
	default:
		return fmt.Errorf("firecracker network.mode %q is unsupported; use user or isolated", mode)
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
		Backend: vmkit.BackendLinuxKVM,
		Host: &vmkit.HostSupport{
			Backend:                 vmkit.BackendLinuxKVM,
			Architecture:            runtime.GOARCH,
			BinaryPath:              path,
			BinaryVersion:           binaryVersion(path),
			VirtualizationSupported: fileExists("/dev/kvm"),
			KVMAvailable:            fileExists("/dev/kvm"),
			VsockAvailable:          fileExists("/dev/vhost-vsock"),
			PauseResumeAvailable:    true,
			SnapshotCreateAvailable: true,
			SnapshotAvailable:       true,
			ConsoleAvailable:        true,
			ConsoleMode:             "interactive",
		},
		Kernel: &vmkit.KernelSupport{
			Backend:      vmkit.BackendLinuxKVM,
			Architecture: runtime.GOARCH,
			Status:       "unknown",
		},
	}
	// Report confinement honestly: only when a non-off mode actually resolves
	// for this host's knob + facts (resolveConfinementMode fails closed, so a
	// non-off result means the host supports and will apply it). Confinement is
	// on by default ("auto"); this is off only on hosts that support neither a
	// root jailer nor rootless user namespaces, or when explicitly disabled.
	confOpts := opts
	if strings.TrimSpace(confOpts.Confinement) == "" {
		confOpts.Confinement = resolveConfinementKnob()
	}
	if mode, err := resolveConfinementMode(confOpts); err == nil && mode != confinementOff {
		resp.Host.ConfinementMode = mode.String()
		resp.Host.ConfinementActive = true
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
	if req.Command == "run" && os.Getenv(userNetworkResidentEnv) == "1" {
		// The resident user-network supervisor waits for the VM's whole life, so the
		// foreground run-timeout must not apply. Lifetime is governed by the declared
		// lease — enforced out-of-band by the deadman watcher + gc sweep, which are
		// idle-based and renewable. A fixed timeout here would kill an active VM.
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
	if opts.Confinement == "" {
		opts.Confinement = resolveConfinementKnob()
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
	// Stopping records that a clean host-initiated stop/halt was requested while
	// the workspace was still Running. It is set before firecracker is signaled,
	// so the supervise loop's inspect re-classification (which would otherwise read
	// the killed command's non-zero result.json and report Failed) instead resolves
	// an intentional stop to Stopped once firecracker is actually dead. It is moot
	// once a terminal state is written, and a fresh Start writes a new runtime state
	// without it.
	Stopping bool `json:"stopping,omitempty"`
}

type guestResult struct {
	StartedAt string `json:"started_at"`
	ExitedAt  string `json:"exited_at"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
	// PoweredOff is set by guest init when the run ended because of an
	// intentional power-off (busybox poweroff/halt/reboot or a host-initiated
	// graceful shutdown), not because the workspace command exited on its own.
	// When set, the run is a clean stop regardless of the interrupted command's
	// exit code.
	PoweredOff bool `json:"powered_off,omitempty"`
}
