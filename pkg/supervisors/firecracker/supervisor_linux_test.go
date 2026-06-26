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
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
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
			Backend:   vmkit.BackendLinuxKVM,
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
			// Exercises serial-input plumbing with a fake firecracker, not
			// confinement; pin it off so it doesn't take the (now default-on)
			// confined launch path, which can't unshare/pivot_root under plain
			// `go test`.
			Confinement: "off",
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

func TestGuestHaltedStateWaitsForDelayedFailureResult(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "demo"}
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		result := `{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":42,"stdout":"failed\n"}`
		_ = os.WriteFile(filepath.Join(dir, "demo", "result.json"), []byte(result), 0o644)
	}()

	state, detail := guestHaltedState(opts, time.Second)

	if state != vmkit.StateFailed {
		t.Fatalf("state = %q, want %q", state, vmkit.StateFailed)
	}
	if detail != "guest exited with code 42" {
		t.Fatalf("detail = %q, want guest exit detail", detail)
	}
}

func TestGuestHaltedStateClassifiesPowerOffAsStopped(t *testing.T) {
	cases := []struct {
		name       string
		result     string
		wantState  vmkit.VMState
		wantDetail string
	}{
		{
			name:      "powered off with non-zero exit classifies as stopped",
			result:    `{"exit_code":143,"error":"signal: killed","powered_off":true}`,
			wantState: vmkit.StateStopped,
		},
		{
			name:       "non-zero exit without power-off marker stays failed",
			result:     `{"exit_code":143,"error":"signal: killed"}`,
			wantState:  vmkit.StateFailed,
			wantDetail: "signal: killed",
		},
		{
			name:      "clean zero exit classifies as stopped",
			result:    `{"exit_code":0}`,
			wantState: vmkit.StateStopped,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := Options{StateDir: dir, Name: "demo"}
			if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "demo", "result.json"), []byte(tc.result), 0o644); err != nil {
				t.Fatal(err)
			}

			state, detail := guestHaltedState(opts, 0)

			if state != tc.wantState {
				t.Fatalf("state = %q, want %q", state, tc.wantState)
			}
			if tc.wantDetail != "" && detail != tc.wantDetail {
				t.Fatalf("detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}

// TestInspectClassifiesStoppingWorkspaceAsStopped covers the host-`stop` race:
// when an intentional stop records Stopping before killing firecracker, the
// supervise loop's inspect re-classification of the now-dead firecracker must
// resolve to Stopped — not Failed from the killed command's non-zero
// result.json. A workspace dying the same way WITHOUT the Stopping intent (a
// genuine crash) must still classify as Failed.
func TestInspectClassifiesStoppingWorkspaceAsStopped(t *testing.T) {
	cases := []struct {
		name      string
		stopping  bool
		wantState vmkit.VMState
	}{
		{name: "stopping intent resolves dead firecracker to stopped", stopping: true, wantState: vmkit.StateStopped},
		{name: "no stopping intent classifies the killed command as failed", stopping: false, wantState: vmkit.StateFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := Options{Name: "agent-1", StateDir: dir}
			req := vmkit.Request{
				Command: "run",
				Identity: &vmkit.Identity{
					RequestID: "req-1",
					RuntimeID: "agent-1",
					Role:      vmkit.RoleWorkload,
					Backend:   vmkit.BackendLinuxKVM,
				},
				Config: &vmkit.Config{StateDir: dir},
			}
			if err := os.MkdirAll(filepath.Join(dir, "agent-1"), 0o755); err != nil {
				t.Fatal(err)
			}
			// The killed workspace command left a non-zero result: guestHaltedState
			// would classify this Failed unless the stop intent overrides it.
			result := `{"exit_code":143,"error":"signal: killed"}`
			if err := os.WriteFile(filepath.Join(dir, "agent-1", "result.json"), []byte(result), 0o644); err != nil {
				t.Fatal(err)
			}
			// Record the workspace Running with a firecracker PID that is dead and
			// does not reference this workspace, so inspect's liveness check sees
			// firecracker as gone and enters the reclassification branch.
			deadPID := unusedPID(t)
			if err := writeProcessState(opts, req, vmkit.StateRunning, deadPID, ""); err != nil {
				t.Fatal(err)
			}
			if tc.stopping {
				state, err := readRuntimeState(opts)
				if err != nil {
					t.Fatal(err)
				}
				if err := persistStopIntent(opts, state); err != nil {
					t.Fatal(err)
				}
			}

			resp, err := inspectWorkspace(opts)
			if err != nil {
				t.Fatalf("inspectWorkspace: %v", err)
			}
			if resp.Event == nil || resp.Event.State != tc.wantState {
				t.Fatalf("inspect state = %#v, want %q", resp.Event, tc.wantState)
			}
			persisted, err := readRuntimeState(opts)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Event.State != tc.wantState {
				t.Fatalf("persisted state = %q, want %q", persisted.Event.State, tc.wantState)
			}
		})
	}
}

// unusedPID returns a PID that is not currently a live process, so liveness
// checks treat it as a dead firecracker.
func unusedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn throwaway process: %v", err)
	}
	pid := cmd.ProcessState.Pid()
	if pid <= 0 {
		t.Fatalf("throwaway pid = %d", pid)
	}
	return pid
}

func TestRuntimeHasResultListener(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "demo"}
	state := runtimeState{
		Event: eventFile{Identity: vmkit.Identity{RuntimeID: "demo"}},
		Config: vmkit.Config{
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: filepath.Join(dir, "demo", "result.json")}},
		},
	}

	if !runtimeHasResultListener(opts, state) {
		t.Fatal("runtimeHasResultListener = false, want true")
	}
}

func TestInspectReturnsRuntimeMetadata(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	mediationListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mediationListener.Close()
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath:  "/tmp/kernel",
			RootfsPath:  "/tmp/rootfs.ext4",
			StateDir:    dir,
			MemoryMiB:   512,
			CPUCount:    2,
			SerialInput: true,
			Network: &vmkit.NetworkConfig{
				Mode:   "user",
				IP:     "10.43.1.2/29",
				Subnet: "10.43.1.0/29",
			},
			Mediation: &vmkit.MediationConfig{
				Enabled:    true,
				Required:   true,
				Port:       2048,
				Target:     mediationListener.Addr().String(),
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
	// A genuinely-running workspace: a live process whose argv carries the
	// workspace state path, so the inspect liveness check sees firecracker as
	// alive and returns runtime metadata instead of reconciling it to stopped.
	vm := exec.Command("sleep", "3600")
	vm.Args = []string{filepath.Join(dir, opts.Name), "3600"}
	if err := vm.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vm.Process.Kill(); _, _ = vm.Process.Wait() })
	if err := writeProcessState(opts, req, vmkit.StateRunning, vm.Process.Pid, ""); err != nil {
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
	if resp.Network == nil || resp.Network.Mode != "user" || resp.Network.IP != "10.43.1.2/29" {
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

func TestValidateFirecrackerConfigRejectsRemovedNetworkModes(t *testing.T) {
	for _, mode := range []string{"bridged", "nat", "named"} {
		err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: mode}})
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("validateFirecrackerConfig(%q) err = %v", mode, err)
		}
	}
}

func TestSupervisorCheckAcceptsIsolatedFirecrackerNetworkMode(t *testing.T) {
	req := vmkit.Request{
		Command: "check",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
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
	if !resp.OK || resp.Backend != vmkit.BackendLinuxKVM {
		t.Fatalf("response = %+v err = %v", resp, err)
	}
}

func TestWriteConfigAddsUserNetworkInterfaceAndBootArgs(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   opts.StateDir,
			MemoryMiB:  512,
			CPUCount:   2,
			ShellPort:  24279,
			ExecPort:   25279,
			Network: &vmkit.NetworkConfig{
				Mode:    "user",
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
		!strings.Contains(bootArgs, "microagent_shell_port=24279") ||
		!strings.Contains(bootArgs, "microagent_exec_port=25279") ||
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

func TestStaticTAPNATAddressPlanPreservesDeclaredNetwork(t *testing.T) {
	plan, err := staticTAPNATAddressPlan(vmkit.NetworkConfig{
		Mode:    "nat",
		IP:      "10.43.210.2/29",
		Subnet:  "10.43.210.0/29",
		Gateway: "10.43.210.1",
		DNS:     []string{"9.9.9.9"},
	})
	if err != nil {
		t.Fatalf("staticTAPNATAddressPlan: %v", err)
	}
	if plan.Subnet != "10.43.210.0/29" || plan.GuestCIDR != "10.43.210.2/29" || plan.Gateway != "10.43.210.1" || plan.HostCIDR != "10.43.210.1/29" {
		t.Fatalf("plan = %#v", plan)
	}
	runtimeNetwork := runtimeNetworkConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{
		Mode:    "nat",
		IP:      "10.43.210.2/29",
		Subnet:  "10.43.210.0/29",
		Gateway: "10.43.210.1",
		DNS:     []string{"9.9.9.9"},
		Routes:  []string{"0.0.0.0/0 via 10.43.210.1"},
	}}, plan.Subnet, plan.GuestCIDR, plan.Gateway)
	if runtimeNetwork.IP != "10.43.210.2/29" || runtimeNetwork.Subnet != "10.43.210.0/29" || runtimeNetwork.Gateway != "10.43.210.1" {
		t.Fatalf("runtime network = %#v", runtimeNetwork)
	}
	if len(runtimeNetwork.DNS) != 1 || runtimeNetwork.DNS[0] != "9.9.9.9" {
		t.Fatalf("runtime DNS = %#v", runtimeNetwork.DNS)
	}
	if len(runtimeNetwork.Routes) != 1 || runtimeNetwork.Routes[0] != "0.0.0.0/0 via 10.43.210.1" {
		t.Fatalf("runtime routes = %#v", runtimeNetwork.Routes)
	}
}

func TestStaticTAPNATAddressPlanRejectsIncompleteStaticNetwork(t *testing.T) {
	if _, err := staticTAPNATAddressPlan(vmkit.NetworkConfig{IP: "10.43.210.2/29"}); err == nil || !strings.Contains(err.Error(), "requires network.ip and network.gateway") {
		t.Fatalf("err = %v, want missing gateway", err)
	}
	if _, err := staticTAPNATAddressPlan(vmkit.NetworkConfig{IP: "10.43.210.2", Gateway: "10.43.210.1"}); err == nil || !strings.Contains(err.Error(), "parse firecracker static network.ip") {
		t.Fatalf("err = %v, want CIDR parse failure", err)
	}
	if _, err := staticTAPNATAddressPlan(vmkit.NetworkConfig{IP: "10.43.210.2/29", Subnet: "10.43.211.0/29", Gateway: "10.43.210.1"}); err == nil || !strings.Contains(err.Error(), "must contain network.ip and network.gateway") {
		t.Fatalf("err = %v, want subnet mismatch", err)
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

func TestFirecrackerSysProcAttrSetsPgidOnlyWhenDetached(t *testing.T) {
	if attr := firecrackerSysProcAttr(false); attr != nil {
		t.Fatalf("foreground attr = %#v", attr)
	}
	attr := firecrackerSysProcAttr(true)
	if attr == nil || !attr.Setpgid {
		t.Fatalf("detached attr = %#v", attr)
	}
	if len(attr.AmbientCaps) != 0 {
		t.Fatalf("detached attr must carry no ambient caps, got %#v", attr.AmbientCaps)
	}
}

func TestDetachedUserNetworkRequestPreservesConfiguredTimeout(t *testing.T) {
	req := vmkit.Request{
		Command: "start",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
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

func TestPortForwarderIncludesShellAndExecPorts(t *testing.T) {
	forwards := portForwarderForwards(vmkit.Config{
		ShellPort: 24279,
		ExecPort:  25279,
		Network: &vmkit.NetworkConfig{
			PortForwards: []vmkit.PortForward{{
				Protocol:  "tcp",
				Host:      "0.0.0.0",
				HostPort:  8581,
				GuestPort: 8581,
			}},
		},
	})
	if len(forwards) != 3 {
		t.Fatalf("forwards len = %d, want 3", len(forwards))
	}
	shell := forwards[1]
	if shell.Protocol != "tcp" || shell.Host != "127.0.0.1" || shell.HostPort != 24279 || shell.GuestPort != 24279 {
		t.Fatalf("shell forward = %#v", shell)
	}
	exec := forwards[2]
	if exec.Protocol != "tcp" || exec.Host != "127.0.0.1" || exec.HostPort != 25279 || exec.GuestPort != 25279 {
		t.Fatalf("exec forward = %#v", exec)
	}
	if !needsPortForwarder(&vmkit.Config{ShellPort: 24279}) {
		t.Fatal("shell port should require port forwarder")
	}
	if !needsPortForwarder(&vmkit.Config{ExecPort: 25279}) {
		t.Fatal("exec port should require port forwarder")
	}
}

func TestRunPortForwarderOpensAndClosesExecPort(t *testing.T) {
	dir := t.TempDir()
	port := freeTCPPort(t)
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir, ExecPort: port},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunPortForwarder(ctx, opts)
	}()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("exec port forwarder did not listen on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunPortForwarder did not stop after cancel")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("exec port still in use after forwarder stop: %v", err)
	}
	_ = listener.Close()
}

func TestWaitForPortForwarderReadyRequiresReachableListener(t *testing.T) {
	port := freeTCPPort(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	live := startSleepProcess(t)
	config := vmkit.Config{ExecPort: port}
	if err := waitForPortForwarderReady(context.Background(), live.Process.Pid, config, time.Second); err != nil {
		t.Fatalf("waitForPortForwarderReady: %v", err)
	}
}

func TestWaitForPortForwarderReadyReportsExitedProcess(t *testing.T) {
	config := vmkit.Config{ExecPort: freeTCPPort(t)}
	err := waitForPortForwarderReady(context.Background(), deadProcessPID(t), config, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "exited before listeners became ready") {
		t.Fatalf("waitForPortForwarderReady error = %v, want exited process detail", err)
	}
}

func TestFirecrackerShellReadinessRequiresLiveShellTarget(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "agent-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInputPath(opts), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := runtimeState{
		Event: eventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendLinuxKVM},
			State:      vmkit.StateRunning,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config:          vmkit.Config{StateDir: dir, ShellPort: 24279, SerialInput: true},
		SerialInputPath: serialInputPath(opts),
		SerialLogPath:   serialLogPath(opts),
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	readiness := readinessFromRuntimeState(state)
	if readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v, want not ready before shell target is reachable", readiness.ShellReady)
	}
	if err := os.WriteFile(serialLogPath(opts), []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readiness = readinessFromRuntimeState(state)
	if readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v, want not ready when only the guest helper log exists", readiness.ShellReady)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	state.Config.ShellPort = uint16(port)
	readiness = readinessFromRuntimeState(state)
	if !readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v, want ready when shell target is reachable", readiness.ShellReady)
	}
}

func TestFirecrackerReadinessReportsDeadPortForwarder(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "agent-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInputPath(opts), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := runtimeState{
		Event: eventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendLinuxKVM},
			State:      vmkit.StateRunning,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config:          vmkit.Config{StateDir: dir, ShellPort: freeTCPPort(t), ExecPort: freeTCPPort(t), SerialInput: true},
		PortForwardPID:  deadProcessPID(t),
		SerialInputPath: serialInputPath(opts),
		SerialLogPath:   serialLogPath(opts),
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	readiness := readinessFromRuntimeState(state)
	if readiness.ShellReady.Ready || !strings.Contains(readiness.ShellReady.Detail, "port forwarder process") {
		t.Fatalf("shell readiness = %#v, want dead port forwarder detail", readiness.ShellReady)
	}
	if readiness.ExecReady.Ready || !strings.Contains(readiness.ExecReady.Detail, "port forwarder process") {
		t.Fatalf("exec readiness = %#v, want dead port forwarder detail", readiness.ExecReady)
	}
}

func TestUserNetworkResidentRunTimeoutDisabledRegardlessOfLease(t *testing.T) {
	t.Setenv(userNetworkResidentEnv, "1")
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{TimeoutSeconds: 300},
	}
	// The resident supervisor waits for the VM's whole life; the foreground
	// run-timeout never applies. Lifetime is the declared lease, enforced
	// out-of-band by the deadman watcher + gc sweep (idle-based, renewable) — so a
	// fixed timeout here is wrong whether or not a lease is set.
	opts := Supervisor{}.normalizedOptions(req)
	if opts.Timeout >= 0 {
		t.Fatalf("no-lease resident run timeout = %s, want disabled (permanent)", opts.Timeout)
	}
	req.Config.LeaseSeconds = 45
	opts = Supervisor{}.normalizedOptions(req)
	if opts.Timeout >= 0 {
		t.Fatalf("leased resident run timeout = %s, want disabled (lease enforced out-of-band)", opts.Timeout)
	}
	// Non-resident start keeps the configured run-timeout.
	req.Config.LeaseSeconds = 0
	req.Command = "start"
	opts = Supervisor{}.normalizedOptions(req)
	if opts.Timeout != 300*time.Second {
		t.Fatalf("start timeout = %s, want configured timeout", opts.Timeout)
	}
}

func TestUserNetworkRuntimePIDPrefersHostPastaPIDFile(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Dir(userNetworkPIDPath(opts)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userNetworkPIDPath(opts), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if got := userNetworkRuntimePID(opts, cmd); got != 4242 {
		t.Fatalf("userNetworkRuntimePID = %d, want host pasta pid", got)
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
			Backend:   vmkit.BackendLinuxKVM,
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
			Backend:   vmkit.BackendLinuxKVM,
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
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
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

func TestBackgroundTerminalWriteDoesNotDemoteQuarantine(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	startReq := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-run",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, startReq, vmkit.StateRunning, 1234, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	quarantineReq := vmkit.Request{
		Command: "quarantine",
		Identity: &vmkit.Identity{
			RequestID: "req-quarantine",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, quarantineReq, vmkit.StateQuarantined, 1234, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}

	if err := writeProcessStateWithProcessesAndNetwork(opts, startReq, vmkit.StateStopped, 0, 0, 0, 0, nil, nil, "late run exit"); err != nil {
		t.Fatal(err)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateQuarantined || state.Event.Identity.RequestID != "req-quarantine" {
		t.Fatalf("state = %#v, want quarantine preserved", state.Event)
	}

	stopReq := vmkit.Request{
		Command: "stop",
		Identity: &vmkit.Identity{
			RequestID: "req-stop",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, stopReq, vmkit.StateStopped, 0, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	state, err = readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateStopped || state.Event.Identity.RequestID != "req-stop" {
		t.Fatalf("state = %#v, want explicit stop to leave quarantine", state.Event)
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
			Backend:   vmkit.BackendLinuxKVM,
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
			Backend:   vmkit.BackendLinuxKVM,
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

func TestDetachedStartExitErrorDetectsImmediateExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 7")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	err := detachedStartExitError(cmd, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("detachedStartExitError = %v, want exit status 7", err)
	}
}

func TestDetachedStartExitErrorIgnoresRunningProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := detachedStartExitError(cmd, 10*time.Millisecond); err != nil {
		t.Fatalf("detachedStartExitError = %v, want nil", err)
	}
}

func TestWriteConfigOmitsNetworkInterfaceForIsolated(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
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

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(port)
}

type fakeVMController struct {
	states     []string
	snapshots  [][2]string
	loads      [][2]string
	loadResume bool
	err        error
	snapErr    error
	loadErr    error
}

func (f *fakeVMController) patchVMState(_ context.Context, state string) error {
	f.states = append(f.states, state)
	return f.err
}

func (f *fakeVMController) createSnapshot(_ context.Context, snapshotPath, memFilePath string) error {
	f.snapshots = append(f.snapshots, [2]string{snapshotPath, memFilePath})
	return f.snapErr
}

func (f *fakeVMController) loadSnapshot(_ context.Context, snapshotPath, memFilePath string, resume bool, _ []networkOverride) error {
	f.loads = append(f.loads, [2]string{snapshotPath, memFilePath})
	f.loadResume = resume
	return f.loadErr
}

func withFakeVMController(t *testing.T, fake *fakeVMController) {
	t.Helper()
	previous := newVMStateController
	newVMStateController = func(string) vmStateController { return fake }
	t.Cleanup(func() { newVMStateController = previous })
}

func startSleepProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func pauseResumeRequest(dir string) vmkit.Request {
	return vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
}

func TestPausePatchesVMStateAndPreservesAuxProcesses(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	vmProcess := startSleepProcess(t)
	forwarder := startSleepProcess(t)
	vsockListener := startSleepProcess(t)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "pause",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err != nil {
		t.Fatalf("pause: resp=%+v err=%v", resp, err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StatePaused {
		t.Fatalf("response = %+v", resp)
	}
	if len(fake.states) != 1 || fake.states[0] != "Paused" {
		t.Fatalf("controller states = %#v, want [Paused]", fake.states)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StatePaused {
		t.Fatalf("persisted state = %s, want paused", state.Event.State)
	}
	if state.PID != vmProcess.Process.Pid || state.PortForwardPID != forwarder.Process.Pid || state.VsockListenerPID != vsockListener.Process.Pid {
		t.Fatalf("pause dropped process state: %#v", state)
	}
}

func TestResumePatchesVMStateBackToRunning(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	vmProcess := startSleepProcess(t)
	forwarder := startSleepProcess(t)
	vsockListener := startSleepProcess(t)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StatePaused, vmProcess.Process.Pid, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "resume",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err != nil {
		t.Fatalf("resume: resp=%+v err=%v", resp, err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("response = %+v", resp)
	}
	if len(fake.states) != 1 || fake.states[0] != "Resumed" {
		t.Fatalf("controller states = %#v, want [Resumed]", fake.states)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateRunning {
		t.Fatalf("persisted state = %s, want running", state.Event.State)
	}
	if state.PortForwardPID != forwarder.Process.Pid || state.VsockListenerPID != vsockListener.Process.Pid {
		t.Fatalf("resume dropped process state: %#v", state)
	}
}

func TestPauseRejectsWorkspaceThatIsNotRunning(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	if err := writeProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "pause",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err == nil {
		t.Fatal("expected pause to reject a stopped workspace")
	}
	if len(fake.states) != 0 {
		t.Fatalf("controller should not be called, got %#v", fake.states)
	}
}

func TestResumeRejectsWorkspaceThatIsNotPaused(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "resume",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
	})
	if err == nil {
		t.Fatal("expected resume to reject a running workspace")
	}
	if len(fake.states) != 0 {
		t.Fatalf("controller should not be called, got %#v", fake.states)
	}
}

func TestSnapshotForkBindDetectsCrossWorkspace(t *testing.T) {
	opts := Options{Name: "fork1", StateDir: "/state"}
	m := vmkit.SnapshotManifest{VsockUDSPath: "/state/forksrc/vsock.sock"}
	src, dst, need := snapshotForkBind(opts, m)
	if !need {
		t.Fatal("a snapshot baked at another workspace's vsock path should need a bind")
	}
	if src != "/state/forksrc" || dst != "/state/fork1" {
		t.Fatalf("bind = %q -> %q, want /state/forksrc -> /state/fork1", src, dst)
	}
}

func TestSnapshotForkBindSkipsResumeInPlace(t *testing.T) {
	opts := Options{Name: "ws", StateDir: "/state"}
	m := vmkit.SnapshotManifest{VsockUDSPath: "/state/ws/vsock.sock"}
	if _, _, need := snapshotForkBind(opts, m); need {
		t.Fatal("resume-in-place (same workspace) must not need a bind")
	}
}

func TestSnapshotForkBindSkipsWhenNoVsock(t *testing.T) {
	opts := Options{Name: "fork1", StateDir: "/state"}
	if _, _, need := snapshotForkBind(opts, vmkit.SnapshotManifest{}); need {
		t.Fatal("a snapshot with no vsock path needs no bind")
	}
}

func TestForkMountExecArgsMapRoot(t *testing.T) {
	withRoot := forkMountExecArgs(true, "/sup", "/state/src", "/state/fork", "/fc", []string{"--api-sock", "/state/fork/api.sock"})
	if withRoot[0] != "--map-root-user" || withRoot[1] != "--mount" {
		t.Fatalf("host-side fork args = %v, want --map-root-user --mount first", withRoot)
	}
	if !containsSeq(withRoot, []string{"--", "/fc", "--api-sock", "/state/fork/api.sock"}) {
		t.Fatalf("firecracker argv missing after --: %v", withRoot)
	}

	// A user-networked fork is already root inside pasta's userns: no
	// --map-root-user, just a nested mount namespace.
	inNS := forkMountExecArgs(false, "/sup", "/state/src", "/state/fork", "/fc", []string{"--api-sock", "/state/fork/api.sock"})
	if inNS[0] != "--mount" {
		t.Fatalf("user-networked fork args = %v, want --mount first (no --map-root-user)", inNS)
	}
	for _, a := range inNS {
		if a == "--map-root-user" {
			t.Fatalf("user-networked fork must not remap root: %v", inNS)
		}
	}
	if !containsSeq(inNS, []string{"--bind-src", "/state/src", "--bind-dst", "/state/fork"}) {
		t.Fatalf("bind spec missing: %v", inNS)
	}
}

func containsSeq(haystack, needle []string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestPrepareSnapshotRestoreRollsBackRootfs(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	kernel := filepath.Join(dir, "kernel")
	if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(rootfs, []byte("LIVE-disk-with-marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelSHA, err := fileSHA256(kernel)
	if err != nil {
		t.Fatal(err)
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "base")
	if err := vmkit.WriteSnapshotManifest(snapDir, vmkit.SnapshotManifest{Tag: "base", NetworkMode: "isolated", KernelSHA256: kernelSHA}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, vmkit.SnapshotRootfsName), []byte("SNAPSHOT-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "r", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{KernelPath: kernel, RootfsPath: rootfs, StateDir: dir, Network: &vmkit.NetworkConfig{Mode: "isolated"}},
		Tag:      "base",
	}
	if err := prepareSnapshotRestore(opts, req); err != nil {
		t.Fatalf("prepareSnapshotRestore: %v", err)
	}
	data, err := os.ReadFile(rootfs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "SNAPSHOT-disk" {
		t.Fatalf("rootfs = %q, want SNAPSHOT-disk (rolled back)", data)
	}
}

func TestPrepareSnapshotRestoreRequiresSecretRehydrateConfig(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	kernel := filepath.Join(dir, "kernel")
	if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(rootfs, []byte("LIVE-disk-with-marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelSHA, err := fileSHA256(kernel)
	if err != nil {
		t.Fatal(err)
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "base")
	if err := vmkit.WriteSnapshotManifest(snapDir, vmkit.SnapshotManifest{
		Tag:                 "base",
		KernelSHA256:        kernelSHA,
		SecretsMaterialized: true,
		SecretsPurged:       true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, vmkit.SnapshotRootfsName), []byte("SNAPSHOT-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "r", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{KernelPath: kernel, RootfsPath: rootfs, StateDir: dir},
		Tag:      "base",
	}
	err = prepareSnapshotRestore(opts, req)
	if err == nil || !strings.Contains(err.Error(), "requires materialized secret references") {
		t.Fatalf("err = %v, want missing secret refs rejection", err)
	}
	data, readErr := os.ReadFile(rootfs)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "LIVE-disk-with-marker" {
		t.Fatalf("rootfs = %q, want live disk left untouched", data)
	}

	req.Config.Secrets = []vmkit.SecretRef{{Name: "API", Ref: "env:TOKEN"}}
	req.Config.SecretsControlPort = 1028
	if err := prepareSnapshotRestore(opts, req); err != nil {
		t.Fatalf("prepareSnapshotRestore with rehydrate config: %v", err)
	}
}

func TestPrepareSnapshotRestoreRejectsKernelSkew(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	kernel := filepath.Join(dir, "kernel")
	if err := os.WriteFile(kernel, []byte("the-real-kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "base")
	if err := vmkit.WriteSnapshotManifest(snapDir, vmkit.SnapshotManifest{Tag: "base", KernelSHA256: "deadbeef-different"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, vmkit.SnapshotRootfsName), []byte("snap"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "r", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{KernelPath: kernel, RootfsPath: filepath.Join(dir, "rootfs.ext4"), StateDir: dir},
		Tag:      "base",
	}
	err := prepareSnapshotRestore(opts, req)
	if err == nil || !strings.Contains(err.Error(), "kernel") {
		t.Fatalf("err = %v, want kernel skew rejection", err)
	}
}

func snapshotSourceRequest(t *testing.T, dir string) vmkit.Request {
	t.Helper()
	kernel := filepath.Join(dir, "kernel")
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfs, []byte("rootfs-bytes-coherent"), 0o644); err != nil {
		t.Fatal(err)
	}
	return vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: kernel,
			RootfsPath: rootfs,
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
			Network:    &vmkit.NetworkConfig{Mode: "user", IP: "10.43.0.2/29"},
		},
	}
}

func TestSnapshotCreateAutoPausesCreatesResumes(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	vmProcess := startSleepProcess(t)
	forwarder := startSleepProcess(t)
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, forwarder.Process.Pid, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-1",
	})
	if err != nil {
		t.Fatalf("snapshot: resp=%+v err=%v", resp, err)
	}
	// Auto-pause then resume around the snapshot.
	if len(fake.states) != 2 || fake.states[0] != "Paused" || fake.states[1] != "Resumed" {
		t.Fatalf("controller states = %#v, want [Paused Resumed]", fake.states)
	}
	if len(fake.snapshots) != 1 {
		t.Fatalf("createSnapshot calls = %d, want 1", len(fake.snapshots))
	}
	snapDir := vmkit.SnapshotDir(dir, "agent-1", "snap-1")
	if fake.snapshots[0][0] != filepath.Join(snapDir, vmkit.SnapshotVMStateName) {
		t.Fatalf("snapshot path = %q", fake.snapshots[0][0])
	}
	if fake.snapshots[0][1] != filepath.Join(snapDir, vmkit.SnapshotMemoryName) {
		t.Fatalf("mem path = %q", fake.snapshots[0][1])
	}
	// Coherent rootfs copy taken while paused.
	rootfsCopy := filepath.Join(snapDir, vmkit.SnapshotRootfsName)
	if data, err := os.ReadFile(rootfsCopy); err != nil || string(data) != "rootfs-bytes-coherent" {
		t.Fatalf("rootfs copy = %q err=%v", data, err)
	}
	manifest, err := vmkit.ReadSnapshotManifest(snapDir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tag != "snap-1" || manifest.NetworkMode != "user" || manifest.VCPUCount != 2 || manifest.MemoryMiB != 512 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.KernelSHA256 == "" {
		t.Fatal("manifest kernel sha256 is empty")
	}
	if manifest.CreatedAt == "" {
		t.Fatal("manifest createdAt is empty")
	}
	if manifest.RootfsArtifact != vmkit.SnapshotRootfsName {
		t.Fatalf("RootfsArtifact = %q, want %q", manifest.RootfsArtifact, vmkit.SnapshotRootfsName)
	}
	if got, want := manifest.MachineStateArtifacts, vmkit.FirecrackerSnapshotArtifacts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MachineStateArtifacts = %#v, want %#v", got, want)
	}
	if manifest.SecretsMaterialized || manifest.SecretsPurged {
		t.Fatalf("manifest secret fields = materialized:%t purged:%t, want both false", manifest.SecretsMaterialized, manifest.SecretsPurged)
	}
	// Workspace returns to running, aux processes preserved.
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateRunning || state.PID != vmProcess.Process.Pid || state.PortForwardPID != forwarder.Process.Pid {
		t.Fatalf("post-snapshot state = %#v", state)
	}
}

func TestSnapshotCreateRejectsMaterializedSecretsWithoutControlPort(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	req.Config.Secrets = []vmkit.SecretRef{{Name: "API", Ref: "env:TOKEN"}}
	req.Config.SecretsControlPort = 0
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-1",
	})
	if err == nil {
		t.Fatal("expected snapshot to fail closed without a secrets control port")
	}
	if resp.OK {
		t.Fatalf("response OK = true, want false")
	}
	for _, want := range []string{"cannot purge secrets for snapshot", "materialized secrets", "no secrets control port"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	}
	if len(fake.snapshots) != 0 {
		t.Fatalf("createSnapshot calls = %d, want 0", len(fake.snapshots))
	}
	if _, statErr := os.Stat(vmkit.SnapshotDir(dir, "agent-1", "snap-1")); !os.IsNotExist(statErr) {
		t.Fatalf("snapshot dir stat err = %v, want not exist", statErr)
	}
}

func TestSnapshotCreateKeepsRuntimeConfigPorts(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	req.Config.ShellPort = 24279
	req.Config.ExecPort = 25279
	req.Config.GuestShellPort = 22001
	req.Config.GuestExecPort = 42001
	req.Config.SecretsControlPort = 1028
	req.Config.ModelGuestPort = 11434
	req.Config.ModelVsockPort = 62100
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	withFakeVMController(t, &fakeVMController{})

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-1",
	})
	if err != nil {
		t.Fatalf("snapshot: resp=%+v err=%v", resp, err)
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Config.ShellPort != 24279 || state.Config.ExecPort != 25279 {
		t.Fatalf("snapshot dropped shell/exec ports from runtime config: %#v", state.Config)
	}
	if state.Config.GuestShellPort != 22001 || state.Config.GuestExecPort != 42001 {
		t.Fatalf("snapshot dropped guest shell/exec ports: %#v", state.Config)
	}
	if state.Config.SecretsControlPort != 1028 {
		t.Fatalf("snapshot dropped secrets control port: %#v", state.Config)
	}
	if state.Config.ModelGuestPort != 11434 || state.Config.ModelVsockPort != 62100 {
		t.Fatalf("snapshot dropped model pairing ports: %#v", state.Config)
	}
}

func TestPauseResumeKeepsRuntimeConfigPorts(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := pauseResumeRequest(dir)
	req.Config.ShellPort = 24279
	req.Config.ExecPort = 25279
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StateRunning, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	withFakeVMController(t, &fakeVMController{})

	for _, command := range []string{"pause", "resume"} {
		resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
			Command:  command,
			Identity: req.Identity,
			Config:   &vmkit.Config{StateDir: dir},
		})
		if err != nil {
			t.Fatalf("%s: resp=%+v err=%v", command, resp, err)
		}
		state, err := readRuntimeState(opts)
		if err != nil {
			t.Fatal(err)
		}
		if state.Config.ShellPort != 24279 || state.Config.ExecPort != 25279 {
			t.Fatalf("%s dropped shell/exec ports from runtime config: %#v", command, state.Config)
		}
	}
}

func TestSnapshotCreateInPlaceWhenAlreadyPaused(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	vmProcess := startSleepProcess(t)
	if err := writeProcessState(opts, req, vmkit.StatePaused, vmProcess.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	resp, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-paused",
	})
	if err != nil {
		t.Fatalf("snapshot: resp=%+v err=%v", resp, err)
	}
	// Already paused: no pause/resume transitions, snapshot in place.
	if len(fake.states) != 0 {
		t.Fatalf("controller states = %#v, want none (already paused)", fake.states)
	}
	if len(fake.snapshots) != 1 {
		t.Fatalf("createSnapshot calls = %d, want 1", len(fake.snapshots))
	}
	state, err := readRuntimeState(opts)
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StatePaused {
		t.Fatalf("workspace should stay paused, got %s", state.Event.State)
	}
}

func TestSnapshotRejectsStoppedWorkspace(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := snapshotSourceRequest(t, dir)
	if err := writeProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVMController{}
	withFakeVMController(t, fake)

	_, err := Supervisor{}.Do(context.Background(), vmkit.Request{
		Command:  "snapshot",
		Identity: req.Identity,
		Config:   &vmkit.Config{StateDir: dir},
		Tag:      "snap-x",
	})
	if err == nil {
		t.Fatal("expected snapshot to reject a stopped workspace")
	}
	if len(fake.snapshots) != 0 {
		t.Fatalf("createSnapshot should not be called, got %#v", fake.snapshots)
	}
}

func TestFirecrackerBootArgsIncludesSecretsPort(t *testing.T) {
	args := firecrackerBootArgs(&vmkit.Config{SecretsPort: 1026})
	if !strings.Contains(args, "microagent_secrets_port=1026") {
		t.Fatalf("boot args missing secrets port: %q", args)
	}
	none := firecrackerBootArgs(&vmkit.Config{})
	if strings.Contains(none, "microagent_secrets_port") {
		t.Fatalf("boot args should omit secrets port when zero: %q", none)
	}
}

func TestFirecrackerBootArgsRequestsResetShutdown(t *testing.T) {
	// Firecracker guests must shut down via RESTART (reboot=k) so a modern kernel
	// with no power-off handler still exits the VMM. The marker is always present.
	args := firecrackerBootArgs(&vmkit.Config{})
	if !strings.Contains(args, "microagent_shutdown=reset") {
		t.Fatalf("boot args missing shutdown marker: %q", args)
	}
}

func TestFirecrackerBootArgsIncludesSecretsAPI(t *testing.T) {
	args := firecrackerBootArgs(&vmkit.Config{
		SecretsPort:     1026,
		OnDemandSecrets: []vmkit.SecretRef{{Name: "DB", Ref: "env:X"}},
	})
	if !strings.Contains(args, "microagent_secrets_api=1") {
		t.Fatalf("boot args missing secrets api flag: %q", args)
	}
	none := firecrackerBootArgs(&vmkit.Config{SecretsPort: 1026})
	if strings.Contains(none, "microagent_secrets_api") {
		t.Fatalf("boot args should omit secrets api when no on-demand secrets: %q", none)
	}
}

func TestFirecrackerBootArgsIncludesSecretsControlPort(t *testing.T) {
	args := firecrackerBootArgs(&vmkit.Config{SecretsControlPort: 1028})
	if !strings.Contains(args, "microagent_secrets_ctl_port=1028") {
		t.Fatalf("boot args missing control port: %q", args)
	}
	none := firecrackerBootArgs(&vmkit.Config{})
	if strings.Contains(none, "microagent_secrets_ctl_port") {
		t.Fatalf("boot args should omit control port when zero: %q", none)
	}
}

// The guest listens on its baked vsock ports, which can differ from the host
// bind ports after a host-port fallback (see ensureBindableManagementPorts) or
// a fork. Boot args must tell the guest its own (guest) ports, not the host
// bind ports, or the host->guest bridge targets the wrong vsock port.
func TestFirecrackerBootArgsUsesGuestPortsWhenSet(t *testing.T) {
	args := firecrackerBootArgs(&vmkit.Config{
		ShellPort:      40000,
		ExecPort:       60000,
		GuestShellPort: 22500,
		GuestExecPort:  42500,
	})
	if !strings.Contains(args, "microagent_shell_port=22500") {
		t.Fatalf("boot args should use guest shell port, got %q", args)
	}
	if !strings.Contains(args, "microagent_exec_port=42500") {
		t.Fatalf("boot args should use guest exec port, got %q", args)
	}
	// When no distinct guest port is set, the host port doubles as the guest port.
	plain := firecrackerBootArgs(&vmkit.Config{ShellPort: 40000, ExecPort: 60000})
	if !strings.Contains(plain, "microagent_shell_port=40000") {
		t.Fatalf("boot args should fall back to host shell port, got %q", plain)
	}
	if !strings.Contains(plain, "microagent_exec_port=60000") {
		t.Fatalf("boot args should fall back to host exec port, got %q", plain)
	}
}

// On WSL2, Windows reserves dynamic TCP port ranges that are unbindable inside
// the distro even though no Linux process holds them. A name-hashed shell/exec
// host port can land in such a range and fail to bind. ensureBindableManagementPorts
// must detect an unbindable host port, move the host bind to a free port, and
// preserve the original as the guest vsock port so the bridge still targets it.
func TestEnsureBindableManagementPortsFallsBackWhenHostPortUnavailable(t *testing.T) {
	// An active listener makes its port unbindable (EADDRINUSE even with
	// SO_REUSEADDR), standing in for a WSL2-reserved host port.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := uint16(busy.Addr().(*net.TCPAddr).Port)

	freeL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := uint16(freeL.Addr().(*net.TCPAddr).Port)
	_ = freeL.Close()

	cfg := &vmkit.Config{ShellPort: busyPort, ExecPort: freePort}
	ensureBindableManagementPorts(cfg)

	if cfg.ShellPort == busyPort {
		t.Fatalf("unbindable shell host port %d should have been reassigned", busyPort)
	}
	if cfg.GuestShellPort != busyPort {
		t.Fatalf("guest shell vsock port should preserve original %d, got %d", busyPort, cfg.GuestShellPort)
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(cfg.ShellPort))))
	if err != nil {
		t.Fatalf("reassigned shell host port %d is not bindable: %v", cfg.ShellPort, err)
	}
	_ = l.Close()

	// A bindable port is left untouched, and no guest override is introduced.
	if cfg.ExecPort != freePort {
		t.Fatalf("bindable exec host port should be unchanged, got %d want %d", cfg.ExecPort, freePort)
	}
	if cfg.GuestExecPort != 0 {
		t.Fatalf("guest exec port should stay unset when no fallback occurs, got %d", cfg.GuestExecPort)
	}
}

func TestMoveManagementHostPortsPreservesGuestPorts(t *testing.T) {
	cfg := &vmkit.Config{ShellPort: 24279, ExecPort: 25279}
	if !moveManagementHostPorts(cfg) {
		t.Fatal("moveManagementHostPorts = false, want changed")
	}
	if cfg.ShellPort == 24279 || cfg.ShellPort == 0 {
		t.Fatalf("shell host port = %d, want reassigned", cfg.ShellPort)
	}
	if cfg.ExecPort == 25279 || cfg.ExecPort == 0 {
		t.Fatalf("exec host port = %d, want reassigned", cfg.ExecPort)
	}
	if cfg.ShellPort == cfg.ExecPort {
		t.Fatalf("shell and exec host ports both assigned %d", cfg.ShellPort)
	}
	if cfg.GuestShellPort != 24279 {
		t.Fatalf("guest shell port = %d, want original 24279", cfg.GuestShellPort)
	}
	if cfg.GuestExecPort != 25279 {
		t.Fatalf("guest exec port = %d, want original 25279", cfg.GuestExecPort)
	}
	for _, port := range []uint16{cfg.ShellPort, cfg.ExecPort} {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
		if err != nil {
			t.Fatalf("reassigned host port %d is not bindable: %v", port, err)
		}
		_ = listener.Close()
	}
}

func TestFirecrackerBootArgsModelForward(t *testing.T) {
	args := firecrackerBootArgs(&vmkit.Config{ModelGuestPort: 11434, ModelVsockPort: 62100})
	if !strings.Contains(args, "microagent_model_fwd=11434:62100") {
		t.Fatalf("missing model fwd cmdline: %q", args)
	}
	none := firecrackerBootArgs(&vmkit.Config{})
	if strings.Contains(none, "microagent_model_fwd") {
		t.Fatalf("boot args should omit model fwd when zero: %q", none)
	}
}

func TestUserNetworkArgsStopOptionParsingBeforeCommand(t *testing.T) {
	args := userNetworkArgs("/usr/local/bin/sup", "/state/pasta.pid", `{"command":"run"}`, false)
	want := []string{
		"--config-net",
		"--quiet",
		"--pid", "/state/pasta.pid",
		"--",
		"/usr/local/bin/sup",
		"--request-json", `{"command":"run"}`,
	}
	if len(args) != len(want) {
		t.Fatalf("userNetworkArgs = %q, want %q", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("userNetworkArgs[%d] = %q, want %q (full: %q)", i, args[i], want[i], args)
		}
	}
}

func TestUserNetworkArgsIPv4Only(t *testing.T) {
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	if !has(userNetworkArgs("sup", "pid", "{}", true), "-4") {
		t.Error("ipv4Only=true must pass -4 to pasta")
	}
	if has(userNetworkArgs("sup", "pid", "{}", false), "-4") {
		t.Error("ipv4Only=false must not pass -4 to pasta")
	}
}

func TestIsRoutableIPv6(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"2606:4700::1111", true},
		{"2001:db8::1", true},
		{"fd7a:115c:a1e0::1", true}, // ULA (e.g. Tailscale) counts as routable
		{"fe80::1", false},          // link-local
		{"::1", false},              // loopback
		{"10.0.0.1", false},         // IPv4
		{"8.8.8.8", false},          // public IPv4
	}
	for _, c := range cases {
		if got := isRoutableIPv6(net.ParseIP(c.ip)); got != c.want {
			t.Errorf("isRoutableIPv6(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func deadProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	return pid
}

func TestWaitForProcessExitEvent(t *testing.T) {
	// Returns true promptly (event-driven) when the watched process exits.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan bool, 1)
	go func() { done <- waitForProcessExitEvent(context.Background(), pid, 10*time.Second) }()
	time.Sleep(150 * time.Millisecond)
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	select {
	case got := <-done:
		if !got {
			t.Fatal("waitForProcessExitEvent = false on process exit, want true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForProcessExitEvent did not return within 3s of the process exiting")
	}

	// An already-dead pid returns true immediately (ESRCH).
	if !waitForProcessExitEvent(context.Background(), pid, time.Second) {
		t.Fatal("waitForProcessExitEvent = false for a dead pid, want true")
	}

	// A canceled context returns false rather than blocking out the budget.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live2 := exec.Command("sleep", "30")
	if err := live2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live2.Process.Kill(); _, _ = live2.Process.Wait() }()
	if waitForProcessExitEvent(ctx, live2.Process.Pid, 5*time.Second) {
		t.Fatal("waitForProcessExitEvent = true on canceled context, want false")
	}
}

func TestCompanionShouldExitConditions(t *testing.T) {
	live := startSleepProcess(t)
	dead := deadProcessPID(t)
	cases := []struct {
		name  string
		state vmkit.VMState
		pid   int
		want  bool
	}{
		{"running with live vm", vmkit.StateRunning, live.Process.Pid, false},
		{"paused with live vm", vmkit.StatePaused, live.Process.Pid, false},
		{"starting without pid", vmkit.StateStarting, 0, false},
		{"running with dead vm", vmkit.StateRunning, dead, true},
		{"stopped", vmkit.StateStopped, 0, true},
		{"halted", vmkit.StateHalted, 0, true},
		{"failed", vmkit.StateFailed, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := Options{Name: "agent-1", StateDir: dir}
			req := vmkit.Request{
				Command:  "run",
				Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
				Config:   &vmkit.Config{StateDir: dir},
			}
			if err := writeProcessState(opts, req, tc.state, tc.pid, ""); err != nil {
				t.Fatal(err)
			}
			if got := companionShouldExit(opts); got != tc.want {
				t.Fatalf("companionShouldExit(%s, pid=%d) = %v, want %v", tc.state, tc.pid, got, tc.want)
			}
		})
	}
	t.Run("missing runtime state", func(t *testing.T) {
		opts := Options{Name: "agent-1", StateDir: t.TempDir()}
		if !companionShouldExit(opts) {
			t.Fatal("companionShouldExit = false for missing runtime state, want true")
		}
	})
}

// A workspace recorded as running whose firecracker process is gone must be
// reconciled by inspect, not reported as still running. The guest here exited
// non-zero via a reset-path shutdown (no "System halted"/"Power down" serial
// marker), so reconciliation must trigger on PID liveness, not GuestHalted —
// otherwise supervise's WaitForSupervised polls inspect forever and hangs.
func TestInspectReconcilesDeadVM(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, deadProcessPID(t), ""); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "agent-1", "result.json")
	if err := os.WriteFile(resultPath, []byte(`{"exit_code":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateFailed {
		t.Fatalf("inspectWorkspace = %+v, want state failed (dead VM must be reconciled, not reported running)", resp.Event)
	}
}

// A workspace recorded running whose pid has been recycled by an unrelated live
// process (firecrackerAlive=false: the pid no longer carries this workspace's
// argv) must reconcile to a terminal state WITHOUT signaling that pid -- doing so
// SIGTERMs an innocent process group. This previously killed the e2e harness,
// which seeds the workspace pid to its own shell ($$).
func TestInspectDoesNotSignalReusedPID(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() }()
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, victim.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	resp, err := inspectWorkspace(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Event == nil || resp.Event.State == vmkit.StateRunning {
		t.Fatalf("inspectWorkspace did not reconcile a stale workspace: %+v", resp.Event)
	}
	if active, _ := processActive(victim.Process.Pid); !active {
		t.Fatal("reconcile signaled an unrelated (pid-reused) process; it must only signal this workspace's own firecracker")
	}
}

func TestRunPortForwarderExitsWhenVMProcessDies(t *testing.T) {
	dir := t.TempDir()
	port := freeTCPPort(t)
	opts := Options{Name: "agent-1", StateDir: dir}
	vm := startSleepProcess(t)
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir, ExecPort: port},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, vm.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RunPortForwarder(context.Background(), opts)
	}()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exec port forwarder did not listen on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := vm.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = vm.Process.Wait()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("RunPortForwarder did not exit after the VM process died")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("exec port still in use after forwarder exit: %v", err)
	}
	_ = listener.Close()
}

func TestRunVsockListenerExitsWhenWorkspaceStateRemoved(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	vm := startSleepProcess(t)
	resultPath := filepath.Join(dir, "agent-1", "result.json")
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config: &vmkit.Config{
			StateDir:       dir,
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: resultPath}},
		},
	}
	if err := writeProcessState(opts, req, vmkit.StateRunning, vm.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RunVsockListener(context.Background(), opts)
	}()
	socketPath := firecrackerGuestVsockPath(opts, 1024)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("vsock listener socket %s did not appear", socketPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Remove(filepath.Join(dir, "agent-1", "runtime.json")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("RunVsockListener did not exit after runtime state removal")
	}
}

func TestForegroundExitTerminatesRecordedCompanions(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	forwarder := startSleepProcess(t)
	vsockListener := startSleepProcess(t)
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, 0, forwarder.Process.Pid, vsockListener.Process.Pid, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	terminateRecordedCompanions(opts)
	if err := waitForProcessExit(context.Background(), forwarder.Process.Pid, 3*time.Second); err != nil {
		t.Fatalf("port forwarder still active: %v", err)
	}
	if err := waitForProcessExit(context.Background(), vsockListener.Process.Pid, 3*time.Second); err != nil {
		t.Fatalf("vsock listener still active: %v", err)
	}
}

func TestEnsureCanDeleteRejectsDeadVMWithLiveCompanions(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir}
	forwarder := startSleepProcess(t)
	req := vmkit.Request{
		Command:  "run",
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeProcessStateWithProcessesAndNetwork(opts, req, vmkit.StateRunning, deadProcessPID(t), forwarder.Process.Pid, 0, 0, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	err := ensureCanDelete(opts)
	if err == nil || !strings.Contains(err.Error(), "port forwarder") {
		t.Fatalf("ensureCanDelete error = %v, want port forwarder running error", err)
	}
}

func TestProcessIdentityReferencesWorkspace(t *testing.T) {
	const ws = "/state/feature-matrix"
	cases := []struct {
		name      string
		cmdline   []byte
		mountinfo []byte
		wsPath    string
		want      bool
	}{
		{"unconfined argv carries ws path",
			[]byte("/usr/bin/firecracker\x00--api-sock\x00/state/feature-matrix/run/api.sock\x00"), nil, ws, true},
		{"confined jail bind in mountinfo (jail-relative argv)",
			[]byte("/firecracker\x00--config-file\x00/run/firecracker.json\x00"),
			[]byte("277 268 253:1 /state/feature-matrix/jail / rw - ext4 /dev/vda1 rw\n"), ws, true},
		{"neither references the workspace (reused pid)",
			[]byte("/firecracker\x00--config-file\x00/run/firecracker.json\x00"),
			[]byte("100 1 8:1 / / rw - ext4 /dev/sda1 rw\n"), ws, false},
		{"another workspace's jail does not match (reuse-safe)", nil,
			[]byte("277 268 253:1 /state/other-ws/jail / rw - ext4 /dev/vda1 rw\n"), ws, false},
		{"prefix-name workspace jail does not match", nil,
			[]byte("277 268 253:1 /state/feature-matrix-2/jail / rw - ext4 /dev/vda1 rw\n"), ws, false},
		{"empty workspace path", []byte("/firecracker\x00"), nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processIdentityReferencesWorkspace(tc.cmdline, tc.mountinfo, tc.wsPath); got != tc.want {
				t.Errorf("processIdentityReferencesWorkspace = %v, want %v", got, tc.want)
			}
		})
	}
}
