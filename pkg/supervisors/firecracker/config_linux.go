//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func writeConfig(opts Options, req vmkit.Request) error {
	kernelImage := req.Config.KernelPath
	rootfsPath := req.Config.RootfsPath
	vsockUDS := vsockSocketPath(opts)
	diskPath := func(d vmkit.Disk) string { return d.Path }
	configDiskPath := req.Config.ConfigDiskPath
	if mode, _ := resolveConfinementMode(opts); mode != confinementOff {
		// Confined: Firecracker runs inside the jail (pivot_root), so its config
		// references jail-relative paths — static artifacts hard-linked in
		// (/kernel, /rootfs.ext4, /disks/<name>, /config.disk) and the vsock
		// UDS in the workspace dir bound at /run. The host-side path helpers
		// are unchanged.
		layout := confinedJailLayout(opts, req.Config, "")
		kernelImage = layout.Kernel.Guest
		rootfsPath = layout.Rootfs.Guest
		vsockUDS = "/run/" + filepath.Base(vsockSocketPath(opts))
		byName := make(map[string]string, len(layout.Disks))
		for _, d := range layout.Disks {
			byName[d.ID] = d.Guest
		}
		diskPath = func(d vmkit.Disk) string { return byName[d.Name] }
		if configDiskPath != "" {
			configDiskPath = layout.ConfigDisk.Guest
		}
	}
	cfg := config{
		BootSource: bootSource{
			KernelImagePath: kernelImage,
			BootArgs:        firecrackerBootArgs(req.Config),
		},
		Drives: []drive{{
			DriveID:      "rootfs",
			PathOnHost:   rootfsPath,
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
			UDSPath:  vsockUDS,
		}
	}
	if iface, ok := firecrackerNetworkInterface(opts, req.Config); ok {
		cfg.NetworkInterfaces = append(cfg.NetworkInterfaces, iface)
	}
	for _, disk := range req.Config.Disks {
		cfg.Drives = append(cfg.Drives, drive{
			DriveID:      disk.Name,
			PathOnHost:   diskPath(disk),
			IsRootDevice: false,
			IsReadOnly:   disk.Mode == "ro",
		})
	}
	// The config disk attaches LAST — after the rootfs and every declared
	// disk — so declared-disk device indices are unchanged and the boot-arg
	// announcement (microagent_config=) can name its position.
	if configDiskPath != "" {
		cfg.Drives = append(cfg.Drives, drive{
			DriveID:      "config",
			PathOnHost:   configDiskPath,
			IsRootDevice: false,
			IsReadOnly:   true,
		})
	}
	if err := os.MkdirAll(filepath.Dir(configPath(opts)), 0o700); err != nil {
		return err
	}
	return writeJSONFile(configPath(opts), cfg)
}

func firecrackerBootArgs(config *vmkit.Config) string {
	// microagent_shutdown=reset tells the guest init to RESTART (reboot=k -> i8042
	// reset, which reliably exits Firecracker) before trying POWER_OFF. A modern
	// guest kernel under Firecracker has no power-off handler, so a POWER_OFF-first
	// shutdown halts the CPU without returning and the VMM is never told to exit.
	// Only the Firecracker supervisor sets this; other backends keep POWER_OFF-first.
	//
	// clearcpuid=xsaves: a guest that boots with XSAVES available can, after a
	// Firecracker snapshot restore, fault repeatedly in restore_fpregs_from_fpstate
	// (#GP on XRSTORS) until the recursion overruns the task's kernel stack guard
	// page and the guest panics - confirmed live on this host (AMD Ryzen 9 5900X,
	// Firecracker 1.15.1): every restore of a guest booted without this flag
	// crashed the same way, and every restore of one booted with it did not.
	// XSAVES is the only xsave variant that can restore supervisor xstate
	// components; clearing it forces the compacted-but-user-only XSAVEC path
	// instead (still present - only xsaves drops out of /proc/cpuinfo), which
	// this bug does not reach. This choice is baked in at the ORIGINAL boot, not
	// at restore time: PUT /snapshot/load does not take boot args, so a snapshot
	// captured from a guest booted before this change still crashes on restore.
	//
	// Firecracker exposes serial and vsock management channels, not a guest PS/2
	// keyboard. Skipping the unused keyboard-port probe avoids the controller's
	// device-detection timeout on every cold boot. reboot=k remains valid: x86's
	// BOOT_KBD reset path writes the controller command port directly and does
	// not depend on the i8042 input driver having registered a keyboard device.
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "pci=off", "root=/dev/vda", "rw", "init=/sbin/microagent-init", "microagent_shutdown=reset", "clearcpuid=xsaves", "i8042.nokbd"}
	// The guest listens on its own vsock ports, which differ from the host bind
	// ports when a fork or a host-port fallback (ensureBindableManagementPorts)
	// has moved the host side. Tell the guest its own ports, not the host ports.
	if config != nil && guestShellPort(*config) != 0 {
		args = append(args, fmt.Sprintf("microagent_shell_port=%d", guestShellPort(*config)))
	}
	if config != nil && guestExecPort(*config) != 0 {
		args = append(args, fmt.Sprintf("microagent_exec_port=%d", guestExecPort(*config)))
	}
	if config != nil && config.Hostname != "" {
		args = append(args, "microagent_hostname="+config.Hostname)
	}
	if config != nil && config.ConfigDiskPath != "" {
		args = append(args, "microagent_config="+vmkit.VirtioBlockDevice(1+len(config.Disks)))
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
	if networkMode(config) == "user" && config != nil && config.Network != nil && config.Network.IP != "" && config.Network.Gateway != "" {
		args = append(args,
			"microagent_net_if=eth0",
			"microagent_net_ip="+config.Network.IP,
			"microagent_net_gw="+config.Network.Gateway,
		)
		if config.Network.IPv6 != "" && config.Network.IPv6Gateway != "" {
			args = append(args,
				"microagent_net_ip6="+config.Network.IPv6,
				"microagent_net_gw6="+config.Network.IPv6Gateway,
			)
		}
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
		return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
	}
	if state.Event.State != vmkit.StateRunning {
		err := fmt.Errorf("firecracker apply only live-reloads running workspaces")
		return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
	}
	if !sameGuestPortForwardShape(networkPortForwards(state.Config.Network), networkPortForwards(req.Config.Network)) {
		err := fmt.Errorf("firecracker apply can only live-reload host bind changes for existing port forwards; stop and start the workspace for port, guest port, protocol, or network mode changes")
		return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
	}
	if state.PortForwardPID != 0 {
		_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
		state.PortForwardPID = 0
	}
	state.Config.Network = req.Config.Network
	runtimeReq := runtimeStateRequest(req, state)
	if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
		return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
	}
	if needsPortForwarder(req.Config) {
		pid, err := startReadyPortForwarderProcessWithManagementPortRetry(context.Background(), opts, runtimeReq.Config, func() error {
			return writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, "")
		})
		if err != nil {
			_ = writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, 0, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, err.Error())
			return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
		}
		state.PortForwardPID = pid
		if err := writeProcessStateWithProcessesAndNetwork(opts, runtimeReq, vmkit.StateRunning, state.PID, pid, state.VsockListenerPID, state.EgressMediatorPID, state.NetworkDevices, state.FirewallRules, ""); err != nil {
			_ = signalProcessGroup(pid, syscall.SIGTERM)
			return vmkit.Response{Backend: vmkit.BackendLinuxKVM, Error: err.Error()}, err
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
	if networkMode(config) != "user" {
		return networkInterface{}, false
	}
	return networkInterface{
		IfaceID:     "eth0",
		GuestMAC:    firecrackerGuestMAC(opts.Name),
		HostDevName: tapName(opts),
	}, true
}

func firecrackerSysProcAttr(detached bool) *syscall.SysProcAttr {
	if !detached {
		return nil
	}
	return &syscall.SysProcAttr{Setpgid: detached}
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
