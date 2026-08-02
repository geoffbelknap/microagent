//go:build linux

package firecracker

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/geoffbelknap/microagent/pkg/workspace"
	"golang.org/x/sys/unix"
)

// acquireRuntimeLease takes the workspace's lifetime lock without waiting. The
// returned file owns the flock and must stay open for the VM's entire life.
// flocks are visible across PID namespaces, unlike kill(pid, 0) and /proc.
func acquireRuntimeLease(opts Options) (*os.File, error) {
	path := workspace.RuntimeLeasePath(opts.StateDir, opts.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, fmt.Errorf("workspace %s runtime lease is already held; another VM may be running outside this PID namespace", opts.Name)
		}
		return nil, err
	}
	return f, nil
}
