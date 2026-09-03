//go:build linux

package firecracker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/netlimit"
	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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
	pollDelay := startupPollInitial
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
		case <-time.After(pollDelay):
		}
		pollDelay = nextStartupPollDelay(pollDelay, 25*time.Millisecond)
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

func vsockListenerLogPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "vsock-listener.log")
}

// vsockListenerReadyPath is the marker the detached vsock-listener process
// writes once its listeners are up. Its presence is how the launching parent
// distinguishes "listeners serving" from "process died during startup" — the
// listeners themselves live in a detached process the parent cannot observe
// directly.
func vsockListenerReadyPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "vsock-listener.ready")
}

func startVsockListenerProcess(opts Options) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logPath := vsockListenerLogPath(opts)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	// Clear a prior run's readiness marker so waitForVsockListenersReady cannot
	// mistake a stale marker for this launch having come up.
	if err := os.Remove(vsockListenerReadyPath(opts)); err != nil && !os.IsNotExist(err) {
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

// vsockListenerReadyTimeout bounds how long a launch waits for the detached
// vsock-listener process to bind its listeners. It is generous because
// listener startup may resolve broker/secret credentials through an external
// helper (a cloud secret manager); a genuine startup failure is caught the
// instant the process exits, so this only backstops a process that hangs
// without ever failing or succeeding.
const vsockListenerReadyTimeout = 60 * time.Second

// waitForVsockListenersReady blocks until the detached vsock-listener process
// signals its listeners are up (the readiness marker appears), or fails. A
// process that exits before signalling has failed to start its listener set —
// e.g. an unresolvable broker secret, an unreadable upstream CA, or a socket it
// could not bind — and that failure is returned with the process's own log so
// the caller can fail the workspace loudly. Without this the failure was
// silent: the process died, the workspace still reported running, and every
// broker/secret vsock service the guest reached simply reset the connection
// with no operator-visible cause.
func waitForVsockListenersReady(opts Options, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollDelay := startupPollInitial
	for {
		if _, err := os.Stat(vsockListenerReadyPath(opts)); err == nil {
			return nil
		}
		active, err := processActive(pid)
		if err != nil {
			return fmt.Errorf("inspect vsock listener process %d: %w", pid, err)
		}
		if !active {
			detail := strings.TrimSpace(readTextFile(vsockListenerLogPath(opts)))
			if detail == "" {
				detail = "vsock listener process exited before its listeners were ready"
			}
			return fmt.Errorf("vsock listeners failed to start: %s", detail)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vsock listeners not ready after %s", timeout)
		}
		time.Sleep(pollDelay)
		pollDelay = nextStartupPollDelay(pollDelay, 50*time.Millisecond)
	}
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
	// Signal readiness only after the whole listener set is bound: the launching
	// parent waits on this to tell a serving workspace from one whose listeners
	// died on startup.
	if err := os.WriteFile(vsockListenerReadyPath(opts), []byte("ready\n"), 0o600); err != nil {
		return fmt.Errorf("signal vsock listeners ready: %w", err)
	}
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
	// The per-VM reaper is started once at detached-start success (startProcess /
	// startUserNetworkProcess); this companion only watches for its own exit.
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

// effectiveVsockListeners returns the listeners to serve for a config,
// synthesizing the mediation listener when an enabled mediation config has no
// listener on its port — mirroring the apple-vf supervisor. pkg/workspace
// always pairs them, but a direct library caller that sets only
// Config.Mediation would otherwise boot a guest dialing a port nothing
// serves; with failClosed mediation that is silent total egress loss while
// mediation readiness still reports from config.
func effectiveVsockListeners(config *vmkit.Config) []vmkit.VsockListener {
	if config == nil {
		return nil
	}
	listeners := config.VsockListeners
	mediation := config.Mediation
	if mediation == nil || !mediation.Enabled || mediation.Port == 0 {
		return listeners
	}
	for _, listener := range listeners {
		if listener.Port == mediation.Port {
			return listeners
		}
	}
	synthesized := make([]vmkit.VsockListener, 0, len(listeners)+1)
	synthesized = append(synthesized, listeners...)
	return append(synthesized, vmkit.VsockListener{Port: mediation.Port, Target: mediation.Target})
}

func startVsockListeners(opts Options, config *vmkit.Config) (*vsockListenerSet, error) {
	effective := effectiveVsockListeners(config)
	if len(effective) == 0 {
		return &vsockListenerSet{}, nil
	}
	set := &vsockListenerSet{}
	for _, listener := range effective {
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
			srv.WithSessionID(opts.SessionID)
			set.listeners = append(set.listeners, unixListener)
			go serveSecretsListener(unixListener, srv)
			continue
		}
		if listener.Target == broker.ListenerTarget {
			brokerListener, err := startBrokerListener(opts, config, listener.Port)
			if err != nil {
				set.Close()
				return nil, err
			}
			set.listeners = append(set.listeners, brokerListener)
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
		go serveVsockListener(unixListener, listener, opts.StateDir)
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

// maxVsockListenerConns mirrors the apple-vf supervisor's per-listener bound
// (maxSocketConnections): a guest opening connections in a loop must not
// exhaust host file descriptors and goroutines (ASK tenet 8). Connections
// beyond the bound are refused (closed), never queued.
const maxVsockListenerConns = netlimit.DefaultMaxConnections

// serveBoundedAccepts runs an accept loop that handles at most limit
// connections concurrently, closing excess connections fail-closed.
func serveBoundedAccepts(listener net.Listener, limit int, handle func(net.Conn)) {
	limited := netlimit.New(listener, limit)
	defer func() { _ = limited.Close() }()
	for {
		conn, err := limited.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			handle(c)
		}(conn)
	}
}

func serveVsockListener(listener net.Listener, config vmkit.VsockListener, stateDir string) {
	var fallbackWarning sync.Once
	serveBoundedAccepts(listener, maxVsockListenerConns, func(conn net.Conn) {
		handleGuestVsockConnection(conn, config.Target, config.ModelRunnerKey, config.ModelRef, stateDir, func(r modelrunner.Record) {
			fallbackWarning.Do(func() {
				fmt.Fprintf(os.Stderr, "model runner key %q unavailable; forwarding model %q to fallback runner %q\n", config.ModelRunnerKey, config.ModelRef, r.Key)
			})
		})
	})
}

// serveCACertListener accepts guest connections and sends the egress CA cert
// PEM (caCertPath) to each. The cert is written by prepareTAPNATForStart
// before any listeners are served, so the file exists when connections arrive.
// If the file is missing or unreadable, the connection is logged and closed.
func serveCACertListener(listener net.Listener, caCertPath string) {
	serveBoundedAccepts(listener, maxVsockListenerConns, func(c net.Conn) {
		defer func() { _ = c.Close() }()
		pem, err := os.ReadFile(caCertPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read cacert for vsock guest: %v\n", err)
			return
		}
		if err := secretxfer.ServeCACert(c, pem); err != nil {
			fmt.Fprintf(os.Stderr, "serve cacert to guest: %v\n", err)
		}
	})
}

func handleGuestVsockConnection(conn net.Conn, target string, modelRunnerKey string, modelRef string, stateDir string, warnFallback func(modelrunner.Record)) {
	const maxResultBytes int64 = 16 * 1024 * 1024
	defer func() { _ = conn.Close() }()
	if tcpTarget, ok := parseTCPAddr(target); ok {
		// When a model ref is recorded, resolve the current runner on each
		// connection so the forward survives a runner restart. Fall back to
		// the static target when resolution fails (runner not found).
		if modelRef != "" && stateDir != "" {
			if r, ok := modelrunner.FindByKeyOrModelRef(stateDir, modelRunnerKey, modelRef); ok {
				if modelRunnerKey != "" && r.Key != modelRunnerKey && warnFallback != nil {
					warnFallback(r)
				}
				tcpTarget = fmt.Sprintf("%s:%d", r.Host, r.Port)
			}
		}
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
	if err := writeFileAtomically(target, data, 0o600); err != nil {
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
	serveBoundedAccepts(listener, netlimit.DefaultMaxConnections, func(conn net.Conn) {
		proxyTCPToGuestVsock(conn, udsPath, guestPort)
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return dialGuestVsockContext(ctx, udsPath, guestPort)
}

// dialGuestVsockContext opens one Firecracker host-initiated vsock connection
// while honoring the caller's deadline through both the Unix-socket dial and
// Firecracker's CONNECT acknowledgement. The returned connection has no
// deadline: callers own its lifetime after the handshake succeeds.
func dialGuestVsockContext(ctx context.Context, udsPath string, guestPort uint32) (net.Conn, *bufio.Reader, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", udsPath)
	if err != nil {
		return nil, nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
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
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, err
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
