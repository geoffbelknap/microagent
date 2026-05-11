//go:build windows

package windows_hyperv

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const maxWindowsHyperVResultBytes = 16 * 1024 * 1024

type hvSocketListenerSet struct {
	listeners []net.Listener
	done      chan error
	once      sync.Once
}

func startRuntimeListeners(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
	if req.Config == nil || len(req.Config.VsockListeners) == 0 {
		return nil, nil
	}
	if !hasResultTarget(req) {
		for _, listener := range req.Config.VsockListeners {
			if listener.Target != resultPath(req) {
				return nil, fmt.Errorf("windows-hyperv vsock listener %d target must be the workspace result path", listener.Port)
			}
		}
		return nil, nil
	}
	vmID, err := guid.FromString(handle.RuntimeID)
	if err != nil {
		return nil, fmt.Errorf("parse HCS runtime ID %q: %w", handle.RuntimeID, err)
	}
	set := &hvSocketListenerSet{done: make(chan error, len(req.Config.VsockListeners))}
	serialListener, err := winio.ListenPipe(serialPipePath(req.Identity.RuntimeID), nil)
	if err != nil {
		return nil, fmt.Errorf("listen windows-hyperv serial pipe: %w", err)
	}
	set.listeners = append(set.listeners, serialListener)
	go copySerial(serialListener, serialLogPath(req))
	started := 0
	for _, listener := range req.Config.VsockListeners {
		if listener.Target != resultPath(req) {
			_ = set.Close()
			return nil, fmt.Errorf("windows-hyperv vsock listener %d target must be the workspace result path", listener.Port)
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
		go set.acceptResult(l, listener.Target)
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

func copySerial(listener net.Listener, target string) {
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = io.Copy(file, conn)
}

func (s *hvSocketListenerSet) acceptResult(listener net.Listener, target string) {
	conn, err := listener.Accept()
	if err != nil {
		s.done <- err
		return
	}
	defer conn.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		s.done <- err
		return
	}
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		s.done <- err
		return
	}
	_, copyErr := io.Copy(file, io.LimitReader(conn, maxWindowsHyperVResultBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		s.done <- copyErr
		return
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		s.done <- closeErr
		return
	}
	info, err := os.Stat(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		s.done <- err
		return
	}
	if info.Size() > maxWindowsHyperVResultBytes {
		_ = os.Remove(tmp)
		s.done <- fmt.Errorf("windows-hyperv result for %s exceeded %d bytes", target, maxWindowsHyperVResultBytes)
		return
	}
	s.done <- os.Rename(tmp, target)
}

func (s *hvSocketListenerSet) Wait(ctx context.Context) error {
	select {
	case err := <-s.done:
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
