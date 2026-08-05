//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

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
	}}, plan)
	if runtimeNetwork.IP != "10.43.210.2/29" || runtimeNetwork.Subnet != "10.43.210.0/29" || runtimeNetwork.Gateway != "10.43.210.1" {
		t.Fatalf("runtime network = %#v", runtimeNetwork)
	}
	if runtimeNetwork.IPv6 == "" || runtimeNetwork.IPv6Subnet == "" || runtimeNetwork.IPv6Gateway == "" {
		t.Fatalf("runtime IPv6 network = %#v", runtimeNetwork)
	}
	if len(runtimeNetwork.DNS) != 1 || runtimeNetwork.DNS[0] != "9.9.9.9" {
		t.Fatalf("runtime DNS = %#v", runtimeNetwork.DNS)
	}
	if len(runtimeNetwork.Routes) != 2 || runtimeNetwork.Routes[0] != "0.0.0.0/0 via 10.43.210.1" || !strings.HasPrefix(runtimeNetwork.Routes[1], "::/0 via ") {
		t.Fatalf("runtime routes = %#v", runtimeNetwork.Routes)
	}
}

func TestStaticTAPNATAddressPlanPreservesDeclaredIPv6(t *testing.T) {
	plan, err := staticTAPNATAddressPlan(vmkit.NetworkConfig{
		IP: "10.43.210.2/29", Subnet: "10.43.210.0/29", Gateway: "10.43.210.1",
		IPv6: "fd00:1234::2/64", IPv6Subnet: "fd00:1234::/64", IPv6Gateway: "fd00:1234::1",
	})
	if err != nil {
		t.Fatalf("staticTAPNATAddressPlan: %v", err)
	}
	if plan.GuestCIDRv6 != "fd00:1234::2/64" || plan.SubnetV6 != "fd00:1234::/64" || plan.GatewayV6 != "fd00:1234::1" || plan.HostCIDRv6 != "fd00:1234::1/64" {
		t.Fatalf("IPv6 plan = %#v", plan)
	}
	if _, err := staticTAPNATAddressPlan(vmkit.NetworkConfig{
		IP: "10.43.210.2/29", Subnet: "10.43.210.0/29", Gateway: "10.43.210.1", IPv6: "fd00:1234::2/64",
	}); err == nil || !strings.Contains(err.Error(), "requires network.ipv6") {
		t.Fatalf("incomplete IPv6 config error = %v", err)
	}
}

func TestTapNATAddressPlanAcceptsIPv6OverrideWithDefaultIPv4(t *testing.T) {
	plan, err := tapNATAddressPlan(Options{Name: "ipv6-only", StateDir: t.TempDir()}, &vmkit.Config{Network: &vmkit.NetworkConfig{
		IPv6: "fd00:1234::2/64", IPv6Subnet: "fd00:1234::/64", IPv6Gateway: "fd00:1234::1",
	}})
	if err != nil {
		t.Fatalf("tapNATAddressPlan: %v", err)
	}
	if plan.GuestCIDR == "" || plan.Gateway == "" {
		t.Fatalf("default IPv4 plan = %#v", plan)
	}
	if plan.GuestCIDRv6 != "fd00:1234::2/64" || plan.SubnetV6 != "fd00:1234::/64" || plan.GatewayV6 != "fd00:1234::1" {
		t.Fatalf("IPv6 override = %#v", plan)
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
	probeDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			probeDone <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var command strings.Builder
		buf := make([]byte, 1024)
		for !strings.Contains(command.String(), "exit\r") {
			n, readErr := conn.Read(buf)
			if n > 0 {
				command.Write(buf[:n])
			}
			if readErr != nil {
				probeDone <- readErr
				return
			}
		}
		text := command.String()
		const tokenPrefix = "__ma_token="
		start := strings.Index(text, tokenPrefix)
		if start < 0 {
			probeDone <- fmt.Errorf("probe command has no token assignment: %q", text)
			return
		}
		start += len(tokenPrefix)
		end := start
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		if end == start {
			probeDone <- fmt.Errorf("probe command has empty token: %q", text)
			return
		}
		_, writeErr := fmt.Fprintf(conn, "\r\n__MICROAGENT_DONE_%s__0\r\n", text[start:end])
		probeDone <- writeErr
	}()
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
		t.Fatalf("shell readiness = %#v, want ready after a shell command round trip", readiness.ShellReady)
	}
	if err := <-probeDone; err != nil {
		t.Fatalf("shell command probe server: %v", err)
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
