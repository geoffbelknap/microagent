//go:build linux

package firecracker

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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

func TestFirecrackerBootArgsIncludesHostname(t *testing.T) {
	args := firecrackerBootArgs(&vmkit.Config{Hostname: "research-vm"})
	if !strings.Contains(args, "microagent_hostname=research-vm") {
		t.Fatalf("boot args missing hostname: %q", args)
	}
	none := firecrackerBootArgs(&vmkit.Config{})
	if strings.Contains(none, "microagent_hostname") {
		t.Fatalf("boot args should omit hostname when empty: %q", none)
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

func TestFirecrackerBootArgsClearsXsaves(t *testing.T) {
	// A guest that boots with XSAVES available can fault repeatedly in
	// restore_fpregs_from_fpstate after a snapshot restore until it panics
	// (confirmed live). The marker is always present, unconditionally.
	args := firecrackerBootArgs(&vmkit.Config{})
	if !strings.Contains(args, "clearcpuid=xsaves") {
		t.Fatalf("boot args missing xsaves clear: %q", args)
	}
}

func TestFirecrackerBootArgsSkipsUnusedKeyboardProbe(t *testing.T) {
	args := firecrackerBootArgs(&vmkit.Config{})
	if !strings.Contains(args, "i8042.nokbd") {
		t.Fatalf("boot args missing unused keyboard probe suppression: %q", args)
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
