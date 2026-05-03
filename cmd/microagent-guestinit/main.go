//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const configPath = "/etc/microagent/run.json"
const resultConnectTimeout = 15 * time.Second
const tcpVsockListenersEnv = "MICROAGENT_VSOCK_TCP_LISTENERS"

type config struct {
	Command []string `json:"command"`
	Env     []string `json:"env,omitempty"`
	Port    uint32   `json:"port"`
	Mode    string   `json:"mode,omitempty"`
	Mounts  []mount  `json:"mounts,omitempty"`
}

type mount struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	Mode       string `json:"mode"`
}

type result struct {
	StartedAt string `json:"started_at"`
	ExitedAt  string `json:"exited_at"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	code := run()
	poweroff()
	os.Exit(code)
}

func run() int {
	cfg, err := readConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	res := result{StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	code := 0
	if err := mountDisks(cfg.Mounts); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := startTCPVsockBridges(cfg.Env); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if len(cfg.Command) > 0 {
		cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
		cmd.Env = guestEnv(cfg.Env)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		res.Stdout = stdout.String()
		res.Stderr = stderr.String()
		fmt.Print(res.Stdout)
		fmt.Fprint(os.Stderr, res.Stderr)
		if err != nil {
			code = exitCode(err)
			res.Error = err.Error()
		}
	} else {
		if err := attachConsole(); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
			res.ExitCode = code
			res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = sendResult(cfg.Port, res)
			return code
		}
		if err := syscall.Exec("/bin/sh", []string{"sh", "-i"}, guestEnv(cfg.Env)); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
		}
	}
	res.ExitCode = code
	res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := sendResult(cfg.Port, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return code
}

func mountDisks(mounts []mount) error {
	for _, mount := range mounts {
		if mount.Device == "" || mount.Mountpoint == "" {
			return fmt.Errorf("mount device and mountpoint are required")
		}
		if err := os.MkdirAll(mount.Mountpoint, 0o755); err != nil {
			return fmt.Errorf("create mountpoint %s: %w", mount.Mountpoint, err)
		}
		flags := uintptr(0)
		if mount.Mode == "ro" {
			flags = unix.MS_RDONLY
		} else if mount.Mode != "rw" {
			return fmt.Errorf("mount %s mode must be ro or rw", mount.Mountpoint)
		}
		if err := unix.Mount(mount.Device, mount.Mountpoint, "ext4", flags, ""); err != nil {
			return fmt.Errorf("mount %s at %s: %w", mount.Device, mount.Mountpoint, err)
		}
	}
	return nil
}

func guestEnv(extra []string) []string {
	env := os.Environ()
	for _, entry := range extra {
		if entry != "" {
			env = append(env, entry)
		}
	}
	return env
}

type tcpVsockBridge struct {
	Listen string
	Port   uint32
}

func startTCPVsockBridges(env []string) error {
	specs, err := parseTCPVsockBridges(envValue(env, tcpVsockListenersEnv))
	if err != nil {
		return err
	}
	for _, spec := range specs {
		listener, err := net.Listen("tcp", spec.Listen)
		if err != nil {
			return fmt.Errorf("listen %s for vsock bridge: %w", spec.Listen, err)
		}
		go serveTCPVsockBridge(listener, spec.Port)
	}
	return nil
}

func parseTCPVsockBridges(raw string) ([]tcpVsockBridge, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	bridges := make([]tcpVsockBridge, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		left, right, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%s entry %q must be listen=vsockPort", tcpVsockListenersEnv, part)
		}
		listen := normalizeTCPListen(left)
		port, err := strconv.ParseUint(strings.TrimSpace(right), 10, 32)
		if err != nil || port == 0 {
			return nil, fmt.Errorf("%s entry %q has invalid vsock port", tcpVsockListenersEnv, part)
		}
		bridges = append(bridges, tcpVsockBridge{Listen: listen, Port: uint32(port)})
	}
	return bridges, nil
}

func normalizeTCPListen(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "127.0.0.1:0"
	}
	if strings.Contains(value, ":") {
		return value
	}
	return "127.0.0.1:" + value
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return os.Getenv(key)
}

func serveTCPVsockBridge(listener net.Listener, port uint32) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go proxyTCPToHostVsock(conn, port)
	}
}

func proxyTCPToHostVsock(conn net.Conn, port uint32) {
	defer conn.Close()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open vsock for bridge port %d: %v\n", port, err)
		return
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}); err != nil {
		_ = unix.Close(fd)
		fmt.Fprintf(os.Stderr, "connect vsock bridge port %d: %v\n", port, err)
		return
	}
	file := os.NewFile(uintptr(fd), "vsock")
	if file == nil {
		_ = unix.Close(fd)
		return
	}
	defer file.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(file, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, file)
		done <- struct{}{}
	}()
	<-done
}

func attachConsole() error {
	for _, path := range []string{"/dev/hvc0", "/dev/console"} {
		fd, err := unix.Open(path, unix.O_RDWR, 0)
		if err != nil {
			continue
		}
		if _, err := unix.Setsid(); err != nil && err != unix.EPERM {
			_ = unix.Close(fd)
			return fmt.Errorf("start console session: %w", err)
		}
		_ = unix.IoctlSetInt(fd, unix.TIOCSCTTY, 0)
		for target := 0; target <= 2; target++ {
			if err := unix.Dup2(fd, target); err != nil {
				_ = unix.Close(fd)
				return fmt.Errorf("attach %s to fd %d: %w", path, target, err)
			}
		}
		if fd > 2 {
			_ = unix.Close(fd)
		}
		return nil
	}
	return fmt.Errorf("open guest console: /dev/hvc0 and /dev/console are unavailable")
}

func readConfig() (config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return config{}, fmt.Errorf("read %s: %w", configPath, err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", configPath, err)
	}
	return cfg, nil
}

func sendResult(port uint32, res result) error {
	if port == 0 {
		return nil
	}
	addr := &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}
	deadline := time.Now().Add(resultConnectTimeout)
	var fd int
	var err error
	for {
		fd, err = unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		if err != nil {
			return fmt.Errorf("open vsock: %w", err)
		}
		if err = unix.Connect(fd, addr); err == nil {
			break
		}
		_ = unix.Close(fd)
		if time.Now().After(deadline) {
			return fmt.Errorf("connect vsock port %d: %w", port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer unix.Close(fd)
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return fmt.Errorf("write result: %w", err)
		}
		data = data[n:]
	}
	return nil
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return 1
}

func poweroff() {
	unix.Sync()
	time.Sleep(250 * time.Millisecond)
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART)
}
