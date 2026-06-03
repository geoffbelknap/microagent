package modelrunner

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// spawnProcess launches argv (argv[0] is the binary) as a detached host process
// with stdout/stderr redirected to logPath, and returns its PID. It is a package
// var so lifecycle code can be unit-tested without launching real processes.
var spawnProcess = func(argv []string, logPath string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("empty argv")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// probeHealth performs a single GET against url; healthy when status < 400. It is
// a package var so waitHealthy can be unit-tested deterministically.
var probeHealth = func(ctx context.Context, url string, timeout time.Duration) error {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

func allocateFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if p.Signal(syscall.Signal(0)) != nil {
		return false
	}
	// A detached process that has been signalled becomes a zombie on Linux
	// (kill -0 still succeeds on zombies). Treat zombies as dead.
	return !processIsZombie(pid)
}

func stopProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// waitHealthy polls the engine's health endpoint until it responds healthy or
// the overall deadline elapses.
func waitHealthy(ctx context.Context, host string, port int, healthPath string, overall time.Duration) error {
	url := fmt.Sprintf("http://%s:%d%s", host, port, healthPath)
	deadline := time.Now().Add(overall)
	for {
		if err := probeHealth(ctx, url, time.Second); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("model runner did not become healthy at %s within %s", url, overall)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
