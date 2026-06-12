//go:build windows

package windows_hyperv

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const maxWindowsHyperVResultBytes = 16 * 1024 * 1024

var dialHVSockPortHook = dialHVSockPort

type hvSocketListenerSet struct {
	listeners []net.Listener
	result    chan error
	once      sync.Once
}

func startRuntimeListeners(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
	if req.Config == nil || (len(req.Config.VsockListeners) == 0 && !hasPortForwards(req.Config) && !hasExecBridge(req.Config)) {
		return nil, nil
	}
	for _, listener := range req.Config.VsockListeners {
		if !isAllowedHVSockTarget(req, listener.Target) {
			return nil, fmt.Errorf("windows-hyperv vsock listener %d target must be host:port, the secrets service, or the workspace result path", listener.Port)
		}
	}
	vmID, err := guid.FromString(handle.RuntimeID)
	if err != nil {
		return nil, fmt.Errorf("parse HCS runtime ID %q: %w", handle.RuntimeID, err)
	}
	set := &hvSocketListenerSet{}
	if hasResultTarget(req) {
		set.result = make(chan error, 1)
	}
	go copySerialPipe(serialPipePath(req.Identity.RuntimeID), serialLogPath(req))
	started := 0
	for _, listener := range req.Config.VsockListeners {
		// Resolve the secrets bundle before the listener (and the boot)
		// exists: an unresolvable reference fails the start, never a guest
		// waiting on a half-served bundle.
		var secrets *secretxfer.Server
		if listener.Target == secretxfer.ServerTarget {
			bundle, err := secretxfer.ResolveBundle(ctx, req.Config)
			if err != nil {
				_ = set.Close()
				return nil, fmt.Errorf("resolve secrets: %w", err)
			}
			secrets = secretxfer.NewServer(req.Identity.RuntimeID, req.Config.StateDir, bundle, secretxfer.OnDemandRefs(req.Config), req.Config.SecretsAudit)
		}
		l, err := winio.ListenHvsock(&winio.HvsockAddr{
			VMID:      vmID,
			ServiceID: winio.VsockServiceID(listener.Port),
		})
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("listen windows-hyperv hvsocket port %d: %w", listener.Port, err)
		}
		set.listeners = append(set.listeners, l)
		started++
		if secrets != nil {
			go secrets.Serve(l)
			continue
		}
		go set.serve(l, listener.Target, listener.Target == resultPath(req))
	}
	if req.Config.Network != nil {
		for _, forward := range req.Config.Network.PortForwards {
			if forward.Protocol != "" && forward.Protocol != "tcp" {
				continue
			}
			host := strings.TrimSpace(forward.Host)
			if host == "" {
				host = "127.0.0.1"
			}
			addr := net.JoinHostPort(host, strconv.Itoa(int(forward.HostPort)))
			l, err := net.Listen("tcp", addr)
			if err != nil {
				_ = set.Close()
				return nil, fmt.Errorf("listen windows-hyperv published tcp %s: %w", addr, err)
			}
			set.listeners = append(set.listeners, l)
			started++
			go servePublishedPortForward(l, vmID, forward)
		}
	}
	if hasExecBridge(req.Config) {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(req.Config.ExecPort)))
		l, err := net.Listen("tcp", addr)
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("listen windows-hyperv structured exec %s: %w", addr, err)
		}
		set.listeners = append(set.listeners, l)
		started++
		go serveTCPToHVSockForward(l, vmID, uint32(guestExecPort(*req.Config)), "structured exec")
	}
	if started == 0 {
		return nil, nil
	}
	return set, nil
}

func hasResultTarget(req vmkit.Request) bool {
	if req.Config == nil || req.Identity == nil {
		return false
	}
	for _, listener := range req.Config.VsockListeners {
		if listener.Target == resultPath(req) {
			return true
		}
	}
	return false
}

func isAllowedHVSockTarget(req vmkit.Request, target string) bool {
	if _, ok := parseTCPAddr(target); ok {
		return true
	}
	if target == secretxfer.ServerTarget {
		return true
	}
	return target == resultPath(req)
}

func copySerialPipe(pipePath, target string) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	timeout := 30 * time.Second
	conn, err := winio.DialPipe(pipePath, &timeout)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.Copy(file, conn)
}

func (s *hvSocketListenerSet) serve(listener net.Listener, target string, resultTarget bool) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if resultTarget {
				s.result <- err
			}
			return
		}
		if resultTarget {
			s.acceptResult(conn, target)
			return
		}
		go handleHVSockConnection(conn, target)
	}
}

func (s *hvSocketListenerSet) acceptResult(conn net.Conn, target string) {
	defer func() { _ = conn.Close() }()
	s.result <- writeHVSockResult(conn, target)
}

func handleHVSockConnection(conn net.Conn, target string) {
	defer func() { _ = conn.Close() }()
	if tcpTarget, ok := parseTCPAddr(target); ok {
		remote, err := net.Dial("tcp", tcpTarget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect windows-hyperv hvsocket target %s: %v\n", tcpTarget, err)
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
	if err := writeHVSockResult(conn, target); err != nil {
		fmt.Fprintf(os.Stderr, "write windows-hyperv hvsocket result %s: %v\n", target, err)
	}
}

func servePublishedPortForward(listener net.Listener, vmID guid.GUID, forward vmkit.PortForward) {
	serveTCPToHVSockForward(listener, vmID, uint32(forward.HostPort), "published tcp")
}

func serveTCPToHVSockForward(listener net.Listener, vmID guid.GUID, guestPort uint32, label string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept windows-hyperv %s connection: %v\n", label, err)
			return
		}
		go proxyTCPToHVSock(conn, vmID, guestPort)
	}
}

func proxyTCPToHVSock(conn net.Conn, vmID guid.GUID, guestPort uint32) {
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hvsock, err := dialHVSockPortHook(ctx, vmID, guestPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect windows-hyperv guest hvsocket port %d: %v\n", guestPort, err)
		return
	}
	defer func() { _ = hvsock.Close() }()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(hvsock, conn)
		closeWriteConn(hvsock)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, hvsock)
		closeWriteConn(conn)
		done <- struct{}{}
	}()
	<-done
	_ = conn.Close()
	_ = hvsock.Close()
}

func dialHVSockPort(ctx context.Context, vmID guid.GUID, guestPort uint32) (net.Conn, error) {
	return winio.Dial(ctx, &winio.HvsockAddr{
		VMID:      vmID,
		ServiceID: winio.VsockServiceID(guestPort),
	})
}

// shellHVSockProbeHook lets tests substitute a deterministic shell probe.
var shellHVSockProbeHook = probeShellHVSock

// probeShellHVSock dials the guest shell service over hv_sock and reports how
// long the dial took. A successful dial means the guest shell channel accepts.
// The hv_sock transport can hold a connect attempt far past context
// cancellation while the guest is still booting, so the dial runs in its own
// goroutine and the probe returns at the timeout regardless.
func probeShellHVSock(ctx context.Context, state runtimeState, timeout time.Duration) (time.Duration, error) {
	runtimeID := strings.TrimSpace(state.ComputeSystemRuntimeID)
	if runtimeID == "" {
		return 0, fmt.Errorf("windows-hyperv shell probe requires compute system runtime ID in runtime state")
	}
	vmID, err := guid.FromString(runtimeID)
	if err != nil {
		return 0, fmt.Errorf("parse HCS runtime ID %q: %w", runtimeID, err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	type dialResult struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan dialResult, 1)
	go func() {
		conn, err := dialHVSockPortHook(dialCtx, vmID, uint32(guestShellPort(state.Config)))
		resultCh <- dialResult{conn: conn, err: err}
	}()
	select {
	case result := <-resultCh:
		elapsed := time.Since(start)
		if result.err != nil {
			return elapsed, result.err
		}
		_ = result.conn.Close()
		return elapsed, nil
	case <-dialCtx.Done():
		// Reap the dial if it ever completes so the connection does not leak.
		go func() {
			if result := <-resultCh; result.err == nil {
				_ = result.conn.Close()
			}
		}()
		return time.Since(start), fmt.Errorf("dial timed out after %s: %w", timeout, dialCtx.Err())
	}
}

func writeHVSockResult(conn net.Conn, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, io.LimitReader(conn, maxWindowsHyperVResultBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	info, err := os.Stat(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if info.Size() > maxWindowsHyperVResultBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("windows-hyperv result for %s exceeded %d bytes", target, maxWindowsHyperVResultBytes)
	}
	return os.Rename(tmp, target)
}

func (s *hvSocketListenerSet) Wait(ctx context.Context) error {
	if s.result == nil {
		return nil
	}
	select {
	case err := <-s.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *hvSocketListenerSet) Close() error {
	var err error
	s.once.Do(func() {
		for _, listener := range s.listeners {
			if closeErr := listener.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
	})
	return err
}

func parseTCPAddr(target string) (string, bool) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || port == "" || strings.ContainsAny(host, `/\`) {
		return "", false
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

func closeWriteConn(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if writer, ok := conn.(closeWriter); ok {
		_ = writer.CloseWrite()
	}
}
