//go:build linux

package firecracker

import (
	"bufio"
	"context"
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
	case "stop":
		return stopWorkspace(ctx, opts, req, syscall.SIGTERM)
	case "kill":
		return stopWorkspace(ctx, opts, req, syscall.SIGKILL)
	case "delete":
		if err := ensureCanDelete(opts); err != nil {
			return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
		}
		cleanupWorkspaceState(opts)
		return eventResponse(req, vmkit.StateStopped, ""), nil
	default:
		err := fmt.Errorf("unknown firecracker command %q", req.Command)
		return vmkit.Response{Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
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
	BootSource bootSource    `json:"boot-source"`
	Drives     []drive       `json:"drives"`
	Machine    machineConfig `json:"machine-config"`
	Vsock      *vsockConfig  `json:"vsock,omitempty"`
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

type eventFile struct {
	Identity   vmkit.Identity `json:"identity"`
	State      vmkit.VMState  `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ObservedAt string         `json:"observedAt"`
}

type runtimeState struct {
	Event           eventFile    `json:"event"`
	Config          vmkit.Config `json:"config"`
	PID             int          `json:"pid,omitempty"`
	PortForwardPID  int          `json:"portForwardPid,omitempty"`
	SerialLogPath   string       `json:"serialLogPath"`
	SerialInputPath string       `json:"serialInputPath,omitempty"`
	StartedAt       string       `json:"startedAt,omitempty"`
	UpdatedAt       string       `json:"updatedAt"`
	Error           string       `json:"error,omitempty"`
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
	if err := writeConfig(opts, req); err != nil {
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	if err := os.MkdirAll(filepath.Dir(serialLogPath(opts)), 0o755); err != nil {
		return vmkit.Response{}, err
	}
	serialLog, err := os.OpenFile(serialLogPath(opts), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return vmkit.Response{}, err
	}
	var serialInput *os.File
	if req.Config.SerialInput {
		input, inputErr := openSerialInputFIFO(opts)
		if inputErr != nil {
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
		if serialInput != nil {
			_ = serialInput.Close()
		}
		_ = serialLog.Close()
		_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
		return failedResponse(req, err.Error()), err
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, cmd.Process.Pid, ""); err != nil {
		_ = cmd.Process.Kill()
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
			if serialInput != nil {
				_ = serialInput.Close()
			}
			_ = serialLog.Close()
			_ = writeProcessState(opts, req, vmkit.StateFailed, 0, err.Error())
			return failedResponse(req, err.Error()), err
		}
		portForwardPID = pid
		if err := writeProcessStateWithForwarder(opts, req, vmkit.StateRunning, cmd.Process.Pid, portForwardPID, ""); err != nil {
			_ = signalProcessGroup(portForwardPID, syscall.SIGTERM)
			_ = cmd.Process.Kill()
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
	if closeErr != nil && errorText == "" {
		state = vmkit.StateFailed
		errorText = closeErr.Error()
	}
	if inputCloseErr != nil && errorText == "" {
		state = vmkit.StateFailed
		errorText = inputCloseErr.Error()
	}
	if err := writeProcessState(opts, req, state, 0, errorText); err != nil && waitErr == nil && closeErr == nil && inputCloseErr == nil {
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

func stopWorkspace(ctx context.Context, opts Options, req vmkit.Request, signal syscall.Signal) (vmkit.Response, error) {
	state, err := readRuntimeState(opts)
	if err != nil {
		return vmkit.Response{}, err
	}
	if state.PID == 0 {
		if state.PortForwardPID != 0 {
			_ = signalProcessGroup(state.PortForwardPID, syscall.SIGTERM)
		}
		if err := writeProcessState(opts, runtimeStateRequest(req, state), vmkit.StateStopped, 0, ""); err != nil {
			return vmkit.Response{}, err
		}
		return eventResponse(req, vmkit.StateStopped, ""), nil
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
	if err := writeProcessState(opts, runtimeStateRequest(req, state), vmkit.StateStopped, 0, ""); err != nil {
		return vmkit.Response{}, err
	}
	return eventResponse(req, vmkit.StateStopped, ""), nil
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
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/sbin/microagent-init",
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

func needsVsock(config *vmkit.Config) bool {
	if config == nil {
		return false
	}
	if len(config.VsockListeners) != 0 {
		return true
	}
	return config.Network != nil && len(config.Network.PortForwards) != 0
}

func hasPortForwards(config *vmkit.Config) bool {
	return config != nil && config.Network != nil && len(config.Network.PortForwards) != 0
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
		_, _ = io.Copy(vsock, conn)
		closeWriteConn(vsock)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, reader)
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
	runtime := runtimeState{
		Event:           fileEvent,
		Config:          *req.Config,
		PID:             pid,
		PortForwardPID:  portForwardPID,
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
