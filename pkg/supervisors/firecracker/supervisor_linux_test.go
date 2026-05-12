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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
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
			Network:     &vmkit.NetworkConfig{Mode: "isolated"},
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

func TestStartVsockListenersWritesGuestResult(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "demo"}
	resultPath := filepath.Join(dir, "demo", "result.json")
	set, err := startVsockListeners(opts, &vmkit.Config{
		VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: resultPath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	conn, err := net.Dial("unix", firecrackerGuestVsockPath(opts, 1024))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(resultPath)
		if err == nil {
			if string(data) != `{"ok":true}` {
				t.Fatalf("result = %s", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("result not written: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInspectReturnsRuntimeMetadata(t *testing.T) {
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
			KernelPath:  "/tmp/kernel",
			RootfsPath:  "/tmp/rootfs.ext4",
			StateDir:    dir,
			MemoryMiB:   512,
			CPUCount:    2,
			SerialInput: true,
			Network: &vmkit.NetworkConfig{
				Mode:   "nat",
				IP:     "10.43.1.2/29",
				Subnet: "10.43.1.0/29",
			},
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     "127.0.0.1:9900",
				FailClosed: true,
			},
		},
	}
	if err := os.MkdirAll(filepath.Join(dir, "agent-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInputPath(opts), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := `{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":0,"stdout":"ok\n"}`
	if err := os.WriteFile(filepath.Join(dir, "agent-1", "result.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatal(err)
	}
	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "inspect",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err != nil {
		t.Fatalf("inspect: resp=%+v err=%v", resp, err)
	}
	if resp.Readiness == nil || !resp.Readiness.GuestReady.Ready || !resp.Readiness.ShellReady.Ready || !resp.Readiness.ResultReady.Ready || !resp.Readiness.MediationReady.Ready {
		t.Fatalf("readiness = %#v", resp.Readiness)
	}
	if resp.Result == nil || resp.Result.ExitCode != 0 || resp.Result.CompletedAt != "2026-05-02T00:00:01Z" || resp.Result.Stdout != "ok\n" {
		t.Fatalf("result = %#v", resp.Result)
	}
	if resp.Network == nil || resp.Network.Mode != "nat" || resp.Network.IP != "10.43.1.2/29" {
		t.Fatalf("network = %#v", resp.Network)
	}
	if resp.Mediation == nil || !resp.Mediation.Required || !resp.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", resp.Mediation)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Readiness.GuestReady.Ready || !state.Readiness.ShellReady.Ready || !state.Readiness.ResultReady.Ready || !state.Readiness.MediationReady.Ready {
		t.Fatalf("persisted readiness = %#v", state.Readiness)
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

func TestValidateFirecrackerConfigAcceptsUserNetworkMode(t *testing.T) {
	if err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "user"}}); err != nil {
		t.Fatalf("validateFirecrackerConfig user: %v", err)
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

func TestWriteConfigAddsNATNetworkInterfaceAndBootArgs(t *testing.T) {
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
			Network: &vmkit.NetworkConfig{
				Mode:    "nat",
				IP:      "10.43.12.2/29",
				Gateway: "10.43.12.1",
				DNS:     []string{"1.1.1.1", "8.8.8.8"},
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
	if len(cfg.NetworkInterfaces) != 1 {
		t.Fatalf("network interfaces = %#v", cfg.NetworkInterfaces)
	}
	bootArgs := cfg.BootSource.BootArgs
	if !strings.Contains(bootArgs, "microagent_net_if=eth0") ||
		!strings.Contains(bootArgs, "microagent_net_ip=10.43.12.2/29") ||
		!strings.Contains(bootArgs, "microagent_net_gw=10.43.12.1") ||
		!strings.Contains(bootArgs, "microagent_net_dns=1.1.1.1,8.8.8.8") {
		t.Fatalf("boot args = %q", bootArgs)
	}
}

func TestBuildNATFirewallRulesUsesNftablesExpressions(t *testing.T) {
	rules, err := buildNATFirewallRules("magtap1234", "10.43.12.0/29")
	if err != nil {
		t.Fatalf("buildNATFirewallRules: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("rules = %#v", rules)
	}
	if rules[0].Table != nftMicroagentTable || rules[0].Chain != nftNATPostroutingChain || rules[0].Comment == "" {
		t.Fatalf("nat rule metadata = %#v", rules[0].transientFirewallRule)
	}
	if !containsExpr[*expr.Masq](rules[0].Exprs) {
		t.Fatalf("nat rule missing masquerade expression: %#v", rules[0].Exprs)
	}
	if rules[1].Chain != nftForwardChain || !containsVerdict(rules[1].Exprs, expr.VerdictAccept) {
		t.Fatalf("forward rule = %#v", rules[1])
	}
	if !containsExpr[*expr.Ct](rules[2].Exprs) || !containsVerdict(rules[2].Exprs, expr.VerdictAccept) {
		t.Fatalf("established forward rule = %#v", rules[2])
	}
}

func TestNATForwardChainPrecedesHostFilterChains(t *testing.T) {
	if nftForwardPriority >= int(*nftables.ChainPriorityFilter) {
		t.Fatalf("forward priority = %d, want before filter priority %d", nftForwardPriority, *nftables.ChainPriorityFilter)
	}
}

func TestFirecrackerNetworkSetupDoesNotExecIPOrIPTables(t *testing.T) {
	data, err := os.ReadFile("supervisor_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, needle := range []string{
		`exec.` + `Command("ip"`,
		`exec.` + `CommandContext(ctx, "ip"`,
		`exec.` + `LookPath("ip"`,
		`exec.` + `Command("iptables"`,
		`exec.` + `CommandContext(ctx, "iptables"`,
		`exec.` + `LookPath("iptables"`,
		`exec.` + `Command("xtables-nft-multi"`,
		`exec.` + `LookPath("xtables-nft-multi"`,
	} {
		if strings.Contains(source, needle) {
			t.Fatalf("firecracker network setup still shells out through %s", needle)
		}
	}
}

func TestEnsureNetAdminInheritableRejectsMissingInheritable(t *testing.T) {
	oldGetCaps := getProcessCapabilities
	oldGetEUID := getEffectiveUID
	t.Cleanup(func() {
		getProcessCapabilities = oldGetCaps
		getEffectiveUID = oldGetEUID
	})
	getEffectiveUID = func() int { return 1000 }
	getProcessCapabilities = func() (processCapabilities, error) {
		mask := uint64(1) << uint(unix.CAP_NET_ADMIN)
		return processCapabilities{
			Effective: mask,
			Permitted: mask,
		}, nil
	}
	err := ensureNetAdminInheritable()
	if err == nil {
		t.Fatal("ensureNetAdminInheritable accepted missing inheritable CAP_NET_ADMIN")
	}
	if !strings.Contains(err.Error(), "effective, permitted, and inheritable") || strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureNetAdminInheritableAcceptsEIP(t *testing.T) {
	oldGetCaps := getProcessCapabilities
	oldGetEUID := getEffectiveUID
	t.Cleanup(func() {
		getProcessCapabilities = oldGetCaps
		getEffectiveUID = oldGetEUID
	})
	getEffectiveUID = func() int { return 1000 }
	getProcessCapabilities = func() (processCapabilities, error) {
		mask := uint64(1) << uint(unix.CAP_NET_ADMIN)
		return processCapabilities{
			Effective:   mask,
			Permitted:   mask,
			Inheritable: mask,
		}, nil
	}
	if err := ensureNetAdminInheritable(); err != nil {
		t.Fatalf("ensureNetAdminInheritable: %v", err)
	}
}

func TestFirecrackerSysProcAttrAddsAmbientNetAdminOnlyForNetworkedVMs(t *testing.T) {
	if attr := firecrackerSysProcAttr(false, false); attr != nil {
		t.Fatalf("isolated attr = %#v", attr)
	}
	attr := firecrackerSysProcAttr(true, true)
	if attr == nil || !attr.Setpgid {
		t.Fatalf("networked detached attr = %#v", attr)
	}
	if len(attr.AmbientCaps) != 1 || attr.AmbientCaps[0] != uintptr(unix.CAP_NET_ADMIN) {
		t.Fatalf("ambient caps = %#v", attr.AmbientCaps)
	}
}

func TestDetachedUserNetworkRequestPreservesConfiguredTimeout(t *testing.T) {
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{TimeoutSeconds: 300},
	}
	inner := detachedUserNetworkRequest(req)
	if inner.Command != "run" {
		t.Fatalf("command = %q, want run", inner.Command)
	}
	if inner.Config == nil || inner.Config.TimeoutSeconds != 300 {
		t.Fatalf("timeout = %#v, want preserved", inner.Config)
	}
	if req.Config.TimeoutSeconds != 300 {
		t.Fatalf("original request timeout mutated to %d", req.Config.TimeoutSeconds)
	}
}

func TestUserNetworkDisableRunTimeoutEnvOnlyDisablesRunTimeout(t *testing.T) {
	t.Setenv(userNetworkDisableRunTimeoutEnv, "1")
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{TimeoutSeconds: 300},
	}
	opts := Supervisor{}.normalizedOptions(req)
	if opts.Timeout >= 0 {
		t.Fatalf("run timeout = %s, want disabled", opts.Timeout)
	}
	req.Command = "start"
	opts = Supervisor{}.normalizedOptions(req)
	if opts.Timeout != 300*time.Second {
		t.Fatalf("start timeout = %s, want configured timeout", opts.Timeout)
	}
}

func containsExpr[T expr.Any](exprs []expr.Any) bool {
	for _, candidate := range exprs {
		if _, ok := candidate.(T); ok {
			return true
		}
	}
	return false
}

func containsVerdict(exprs []expr.Any, kind expr.VerdictKind) bool {
	for _, candidate := range exprs {
		verdict, ok := candidate.(*expr.Verdict)
		if ok && verdict.Kind == kind {
			return true
		}
	}
	return false
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
	if cfg.Vsock == nil || cfg.Vsock.GuestCID != firecrackerGuestCID(opts) || cfg.Vsock.UDSPath == "" {
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
	vsockListener := exec.Command("sleep", "30")
	if err := vsockListener.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = vsockListener.Process.Kill()
		_, _ = vsockListener.Process.Wait()
	})
	if err := os.MkdirAll(filepath.Dir(vsockSocketPath(opts)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vsockSocketPath(opts), []byte("socket placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, nil, nil, ""); err != nil {
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
	if err := waitForProcessExit(context.Background(), vsockListener.Process.Pid, time.Second); err != nil {
		t.Fatalf("vsock listener still active: %v", err)
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
	if state.Event.State != vmkit.StateQuarantined || state.PID != vmProcess.Process.Pid || state.PortForwardPID != 0 || state.VsockListenerPID != 0 || len(state.NetworkDevices) != 0 {
		t.Fatalf("state = %#v", state)
	}
}

func TestEnsureCanDeleteRejectsRunningStateWithoutPID(t *testing.T) {
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
		},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := ensureCanDelete(opts); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("ensureCanDelete error = %v, want running rejection", err)
	}
}

func TestEnsureCanDeleteRejectsActiveUserNetworkProcess(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command: "stop",
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
		},
	}
	if err := writeProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	networkProcess := exec.Command("sleep", "30")
	if err := networkProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = networkProcess.Process.Kill()
		_, _ = networkProcess.Process.Wait()
	})
	if err := os.WriteFile(userNetworkPIDPath(opts), []byte(strconv.Itoa(networkProcess.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCanDelete(opts); err == nil || !strings.Contains(err.Error(), "user network process is running") {
		t.Fatalf("ensureCanDelete error = %v, want user network rejection", err)
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
