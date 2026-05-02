//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const configPath = "/etc/microagent/run.json"
const resultConnectTimeout = 15 * time.Second

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
