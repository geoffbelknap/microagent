//go:build linux

package firecracker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestDialGuestVsockUsesFirecrackerConnectHandshake(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err.Error()
			return
		}
		defer conn.Close()
		buf := make([]byte, len("CONNECT 8080\n"))
		if _, err := io.ReadFull(conn, buf); err != nil {
			done <- err.Error()
			return
		}
		if _, err := conn.Write([]byte("OK 1234\npayload")); err != nil {
			done <- err.Error()
			return
		}
		done <- string(buf)
	}()
	conn, reader, err := dialGuestVsock(socketPath, 8080)
	if err != nil {
		t.Fatalf("dialGuestVsock: %v", err)
	}
	defer conn.Close()
	if got := <-done; got != "CONNECT 8080\n" {
		t.Fatalf("handshake = %q", got)
	}
	payload := make([]byte, len("payload"))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q", payload)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatal(err)
	}
}

func TestOpenSerialInputFIFOCreatesNamedPipe(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	file, err := openSerialInputFIFO(opts)
	if err != nil {
		t.Fatalf("openSerialInputFIFO: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(serialInputPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("serial input is not a fifo: %s", info.Mode())
	}
}

func TestOpenSerialInputFIFORejectsRegularFile(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Dir(serialInputPath(opts)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInputPath(opts), []byte("not a fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := openSerialInputFIFO(opts); err == nil {
		_ = file.Close()
		t.Fatal("openSerialInputFIFO accepted regular file")
	}
}

func TestRunConnectsSerialInputToFirecrackerStdin(t *testing.T) {
	dir := t.TempDir()
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf 'ready\n'
IFS= read -r line
printf 'got:%s\n' "$line"
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath:  filepath.Join(dir, "Image"),
			RootfsPath:  filepath.Join(dir, "rootfs.ext4"),
			StateDir:    dir,
			MemoryMiB:   128,
			CPUCount:    1,
			SerialInput: true,
		},
	}
	done := make(chan error, 1)
	go func() {
		resp, err := (Supervisor{Options: Options{
			Name:            "research",
			StateDir:        dir,
			FirecrackerPath: fakeFirecracker,
			Timeout:         2 * time.Second,
		}}).Do(context.Background(), req)
		if err != nil {
			done <- err
			return
		}
		if !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
			done <- unexpectedResponseError{response: resp}
			return
		}
		done <- nil
	}()
	inputPath := filepath.Join(dir, "research", "serial.in")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(inputPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", inputPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	input, err := os.OpenFile(inputPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString("hello\n"); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("firecracker supervisor did not exit")
	}
	serial, err := os.ReadFile(filepath.Join(dir, "research", "serial.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serial), "ready\n") || !strings.Contains(string(serial), "got:hello\n") {
		t.Fatalf("serial log = %q", serial)
	}
}

func TestServePortForwardUsesRequestedVsockPort(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err.Error()
			return
		}
		defer conn.Close()
		buf := make([]byte, len("CONNECT 8080\n"))
		if _, err := io.ReadFull(conn, buf); err != nil {
			done <- err.Error()
			return
		}
		done <- string(buf)
	}()
	hostListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostListener.Close()
	go servePortForward(hostListener, socketPath, 9090)
	conn, err := net.Dial("tcp", hostListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := <-done; got != "CONNECT 9090\n" {
		t.Fatalf("handshake = %q", got)
	}
}

func TestSerialInputFIFOUsesFIFOType(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	file, err := openSerialInputFIFO(opts)
	if err != nil {
		t.Fatalf("openSerialInputFIFO: %v", err)
	}
	defer file.Close()
	var stat syscall.Stat_t
	if err := syscall.Stat(serialInputPath(opts), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		t.Fatalf("mode = %#o, want fifo", stat.Mode)
	}
}

func TestValidateFirecrackerConfigRejectsUnsupportedNetworkMode(t *testing.T) {
	err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "open"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("validateFirecrackerConfig err = %v", err)
	}
}

func TestValidateFirecrackerConfigAcceptsIsolatedNetworkMode(t *testing.T) {
	if err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "isolated"}}); err != nil {
		t.Fatalf("validateFirecrackerConfig isolated: %v", err)
	}
}

func TestValidateFirecrackerConfigRejectsBridgedWithoutInterface(t *testing.T) {
	err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "bridged"}})
	if err == nil || !strings.Contains(err.Error(), "network.interface is required") {
		t.Fatalf("validateFirecrackerConfig err = %v", err)
	}
}

func TestSupervisorCheckAcceptsIsolatedFirecrackerNetworkMode(t *testing.T) {
	req := vmkit.Request{
		Command: "check",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   t.TempDir(),
			Network:    &vmkit.NetworkConfig{Mode: "isolated"},
		},
	}
	resp, err := Supervisor{}.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Supervisor.Do rejected isolated network mode: resp=%+v err=%v", resp, err)
	}
	if !resp.OK || resp.Backend != vmkit.BackendFirecracker {
		t.Fatalf("response = %+v err = %v", resp, err)
	}
}

func TestWriteConfigAddsBridgedNetworkInterface(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   opts.StateDir,
			MemoryMiB:  512,
			CPUCount:   2,
			Network:    &vmkit.NetworkConfig{Mode: "bridged", Interface: "br0"},
		},
	}
	if err := writeConfig(opts, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	var cfg config
	data, err := os.ReadFile(configPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.NetworkInterfaces) != 1 {
		t.Fatalf("network interfaces = %#v", cfg.NetworkInterfaces)
	}
	if cfg.NetworkInterfaces[0].IfaceID != "eth0" || cfg.NetworkInterfaces[0].HostDevName == "" || cfg.NetworkInterfaces[0].GuestMAC == "" {
		t.Fatalf("network interface = %#v", cfg.NetworkInterfaces[0])
	}
}

func TestWriteConfigAddsVsockForMediation(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   opts.StateDir,
			MemoryMiB:  512,
			CPUCount:   2,
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     "127.0.0.1:9900",
				FailClosed: true,
			},
		},
	}
	if err := writeConfig(opts, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	var cfg config
	data, err := os.ReadFile(configPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Vsock == nil || cfg.Vsock.GuestCID != 3 || cfg.Vsock.UDSPath == "" {
		t.Fatalf("vsock = %#v", cfg.Vsock)
	}
}

func TestQuarantinePreservesVMPIDAndSeversHostSideEffects(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     "127.0.0.1:9900",
				FailClosed: true,
			},
		},
	}
	vmProcess := exec.Command("sleep", "30")
	if err := vmProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = vmProcess.Process.Kill()
		_, _ = vmProcess.Process.Wait()
	})
	forwarder := exec.Command("sleep", "30")
	if err := forwarder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = forwarder.Process.Kill()
		_, _ = forwarder.Process.Wait()
	})
	if err := os.MkdirAll(filepath.Dir(vsockSocketPath(opts)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vsockSocketPath(opts), []byte("socket placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeProcessStateWithForwarder(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	quarantineReq := vmkit.Request{
		Command:  "quarantine",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	}
	resp, err := Supervisor{}.Do(context.Background(), quarantineReq)
	if err != nil {
		t.Fatalf("quarantine: resp=%+v err=%v", resp, err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateQuarantined {
		t.Fatalf("response = %+v", resp)
	}
	if err := waitForProcessExit(context.Background(), forwarder.Process.Pid, time.Second); err != nil {
		t.Fatalf("forwarder still active: %v", err)
	}
	active, err := processActive(vmProcess.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatalf("vm pid %d was stopped by quarantine", vmProcess.Process.Pid)
	}
	if _, err := os.Stat(vsockSocketPath(opts)); !os.IsNotExist(err) {
		t.Fatalf("vsock socket stat err = %v, want not exist", err)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateQuarantined || state.PID != vmProcess.Process.Pid || state.PortForwardPID != 0 || len(state.NetworkDevices) != 0 {
		t.Fatalf("state = %#v", state)
	}
}

func TestWriteConfigOmitsNetworkInterfaceForIsolated(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   opts.StateDir,
			MemoryMiB:  512,
			CPUCount:   2,
			Network:    &vmkit.NetworkConfig{Mode: "isolated"},
		},
	}
	if err := writeConfig(opts, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	var cfg config
	data, err := os.ReadFile(configPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.NetworkInterfaces) != 0 {
		t.Fatalf("network interfaces = %#v", cfg.NetworkInterfaces)
	}
}

type unexpectedResponseError struct {
	response vmkit.Response
}

func (e unexpectedResponseError) Error() string {
	return "unexpected response"
}
