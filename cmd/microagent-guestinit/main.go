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

type config struct {
	Command []string `json:"command"`
	Port    uint32   `json:"port"`
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
	if len(cfg.Command) > 0 {
		cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
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
	}
	res.ExitCode = code
	res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := sendResult(cfg.Port, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return code
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
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("open vsock: %w", err)
	}
	defer unix.Close(fd)
	addr := &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}
	if err := unix.Connect(fd, addr); err != nil {
		return fmt.Errorf("connect vsock port %d: %w", port, err)
	}
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
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
