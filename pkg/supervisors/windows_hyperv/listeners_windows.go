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
	if req.Config == nil || (len(req.Config.VsockListeners) == 0 && !hasPortForwards(req.Config)) {
		return nil, nil
	}
	for _, listener := range req.Config.VsockListeners {
		if !isAllowedHVSockTarget(req, listener.Target) {
			return nil, fmt.Errorf("windows-hyperv vsock listener %d target must be host:port or the workspace result path", listener.Port)
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
	return target == resultPath(req)
}

func copySerialPipe(pipePath, target string) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	timeout := 30 * time.Second
	conn, err := winio.DialPipe(pipePath, &timeout)
	if err != nil {
		return
	}
	defer conn.Close()
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
	defer conn.Close()
	s.result <- writeHVSockResult(conn, target)
}

func handleHVSockConnection(conn net.Conn, target string) {
	defer conn.Close()
	if tcpTarget, ok := parseTCPAddr(target); ok {
		remote, err := net.Dial("tcp", tcpTarget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect windows-hyperv hvsocket target %s: %v\n", tcpTarget, err)
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
	if err := writeHVSockResult(conn, target); err != nil {
		fmt.Fprintf(os.Stderr, "write windows-hyperv hvsocket result %s: %v\n", target, err)
	}
}

func servePublishedPortForward(listener net.Listener, vmID guid.GUID, forward vmkit.PortForward) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept windows-hyperv published tcp connection: %v\n", err)
			return
		}
		go proxyTCPToHVSock(conn, vmID, uint32(forward.HostPort))
	}
}

func proxyTCPToHVSock(conn net.Conn, vmID guid.GUID, guestPort uint32) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hvsock, err := dialHVSockPortHook(ctx, vmID, guestPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect windows-hyperv guest hvsocket port %d: %v\n", guestPort, err)
		return
	}
	defer hvsock.Close()
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

func writeHVSockResult(conn net.Conn, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
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
