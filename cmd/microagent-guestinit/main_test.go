//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestMountGuestFilesystemsMountsProcSysAndDevPTS(t *testing.T) {
	oldMount := mountGuestFilesystem
	t.Cleanup(func() { mountGuestFilesystem = oldMount })
	var calls []guestFilesystem
	mountGuestFilesystem = func(source, target, fsType string, flags uintptr, data string) error {
		calls = append(calls, guestFilesystem{Source: source, Target: target, FSType: fsType, Flags: flags})
		return nil
	}
	if err := mountGuestFilesystems(); err != nil {
		t.Fatalf("mountGuestFilesystems: %v", err)
	}
	if !reflect.DeepEqual(calls, guestFilesystems) {
		t.Fatalf("mount calls = %#v, want %#v", calls, guestFilesystems)
	}
}

func TestMountGuestFilesystemsAllowsAlreadyMounted(t *testing.T) {
	oldMount := mountGuestFilesystem
	t.Cleanup(func() { mountGuestFilesystem = oldMount })
	mountGuestFilesystem = func(string, string, string, uintptr, string) error {
		return syscall.EBUSY
	}
	if err := mountGuestFilesystems(); err != nil {
		t.Fatalf("mountGuestFilesystems: %v", err)
	}
}

func TestMountGuestFilesystemsRejectsMountFailure(t *testing.T) {
	oldMount := mountGuestFilesystem
	t.Cleanup(func() { mountGuestFilesystem = oldMount })
	mountGuestFilesystem = func(string, string, string, uintptr, string) error {
		return syscall.EPERM
	}
	err := mountGuestFilesystems()
	if err == nil {
		t.Fatal("mountGuestFilesystems error = nil")
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("mountGuestFilesystems err = %v", err)
	}
}

func TestEnsureGuestFDSymlinksCreatesStandardLinks(t *testing.T) {
	oldSymlink := symlinkGuestDevice
	t.Cleanup(func() { symlinkGuestDevice = oldSymlink })
	var calls []guestFDSymlink
	symlinkGuestDevice = func(target, path string) error {
		calls = append(calls, guestFDSymlink{Target: target, Path: path})
		return nil
	}
	if err := ensureGuestFDSymlinks(); err != nil {
		t.Fatalf("ensureGuestFDSymlinks: %v", err)
	}
	if !reflect.DeepEqual(calls, guestFDSymlinks) {
		t.Fatalf("symlink calls = %#v, want %#v", calls, guestFDSymlinks)
	}
}

func TestEnsureGuestFDSymlinksAllowsExisting(t *testing.T) {
	oldSymlink := symlinkGuestDevice
	t.Cleanup(func() { symlinkGuestDevice = oldSymlink })
	symlinkGuestDevice = func(string, string) error {
		return os.ErrExist
	}
	if err := ensureGuestFDSymlinks(); err != nil {
		t.Fatalf("ensureGuestFDSymlinks: %v", err)
	}
}

func TestEnsureGuestFDSymlinksRejectsFailure(t *testing.T) {
	oldSymlink := symlinkGuestDevice
	t.Cleanup(func() { symlinkGuestDevice = oldSymlink })
	symlinkGuestDevice = func(string, string) error {
		return syscall.EPERM
	}
	err := ensureGuestFDSymlinks()
	if err == nil {
		t.Fatal("ensureGuestFDSymlinks error = nil")
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("ensureGuestFDSymlinks err = %v", err)
	}
}

func TestParseTCPVsockBridges(t *testing.T) {
	got, err := parseTCPVsockBridges("3128=3128,127.0.0.1:8081=8081")
	if err != nil {
		t.Fatalf("parseTCPVsockBridges: %v", err)
	}
	want := []tcpVsockBridge{
		{Listen: "127.0.0.1:3128", Port: 3128},
		{Listen: "127.0.0.1:8081", Port: 8081},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bridges = %#v, want %#v", got, want)
	}
}

func TestParseTCPVsockBridgesRejectsBadEntries(t *testing.T) {
	for _, raw := range []string{"3128", "3128=0", "3128=nope"} {
		if _, err := parseTCPVsockBridges(raw); err == nil {
			t.Fatalf("parseTCPVsockBridges(%q) error = nil, want error", raw)
		}
	}
}

func TestEnvValuePrefersConfigEnv(t *testing.T) {
	t.Setenv(tcpVsockListenersEnv, "from-process")
	got := envValue([]string{tcpVsockListenersEnv + "=from-config"}, tcpVsockListenersEnv)
	if got != "from-config" {
		t.Fatalf("envValue = %q, want from-config", got)
	}
}

func TestCmdlineRequestsDHCP(t *testing.T) {
	for _, cmdline := range []string{
		"console=hvc0 root=/dev/vda ip=dhcp",
		"ip=on console=hvc0",
		"root=/dev/vda ip=any",
	} {
		if !cmdlineRequestsDHCP(cmdline) {
			t.Fatalf("cmdlineRequestsDHCP(%q) = false, want true", cmdline)
		}
	}
	if cmdlineRequestsDHCP("console=hvc0 root=/dev/vda ip=off") {
		t.Fatal("cmdlineRequestsDHCP(ip=off) = true, want false")
	}
}

func TestCmdlineAllowsGatewayDNSFallback(t *testing.T) {
	if !cmdlineAllowsGatewayDNSFallback("console=hvc0 microagent_dns_fallback_gateway=1 ip=dhcp") {
		t.Fatal("cmdlineAllowsGatewayDNSFallback = false, want true")
	}
	if cmdlineAllowsGatewayDNSFallback("console=hvc0 ip=dhcp") {
		t.Fatal("cmdlineAllowsGatewayDNSFallback without flag = true, want false")
	}
}

func TestCmdlineDNSNameservers(t *testing.T) {
	got := cmdlineDNSNameservers("console=hvc0 microagent_dns=192.168.64.1,not-ip,8.8.8.8,192.168.64.1")
	want := []string{"192.168.64.1", "8.8.8.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cmdlineDNSNameservers = %#v, want %#v", got, want)
	}
}

func TestKernelConfigOverrideUpdatesShellPort(t *testing.T) {
	cfg := config{ShellPort: 22000}
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "console=ttyS0 microagent_shell_port=24279 root=/dev/vda"); err != nil {
		t.Fatalf("applyKernelConfigOverridesFromCmdline: %v", err)
	}
	if cfg.ShellPort != 24279 {
		t.Fatalf("ShellPort = %d, want 24279", cfg.ShellPort)
	}
}

func TestKernelConfigOverrideUpdatesExecPort(t *testing.T) {
	cfg := config{ExecPort: 23000}
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "console=ttyS0 microagent_exec_port=25279 root=/dev/vda"); err != nil {
		t.Fatalf("applyKernelConfigOverridesFromCmdline: %v", err)
	}
	if cfg.ExecPort != 25279 {
		t.Fatalf("ExecPort = %d, want 25279", cfg.ExecPort)
	}
}

func TestKernelConfigOverrideRejectsBadShellPort(t *testing.T) {
	cfg := config{ShellPort: 22000}
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "microagent_shell_port=0"); err == nil {
		t.Fatal("applyKernelConfigOverridesFromCmdline error = nil, want bad shell port error")
	}
}

func TestKernelConfigOverrideRejectsBadExecPort(t *testing.T) {
	cfg := config{ExecPort: 23000}
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "microagent_exec_port=0"); err == nil {
		t.Fatal("applyKernelConfigOverridesFromCmdline error = nil, want bad exec port error")
	}
}

func TestInteractiveShellExitIsNotLaunchFailure(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 7").Run()
	if err == nil {
		t.Fatal("shell exit error = nil")
	}
	if shellLaunchFailed(err) {
		t.Fatalf("shellLaunchFailed(%v) = true, want false", err)
	}
}

func TestInteractiveShellMissingBinaryIsLaunchFailure(t *testing.T) {
	err := exec.Command("/definitely/missing/microagent-shell").Run()
	if err == nil {
		t.Fatal("missing shell error = nil")
	}
	if !shellLaunchFailed(err) {
		t.Fatalf("shellLaunchFailed(%v) = false, want true", err)
	}
}

func TestConsoleShellCommandDefaultsToBinSh(t *testing.T) {
	got, err := consoleShellCommand("")
	if err != nil {
		t.Fatalf("consoleShellCommand: %v", err)
	}
	want := []string{"/bin/sh", "-i"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("consoleShellCommand = %#v, want %#v", got, want)
	}
}

func TestConsoleShellCommandUsesConfiguredShell(t *testing.T) {
	dir := t.TempDir()
	shellPath := filepath.Join(dir, "bash")
	if err := os.WriteFile(shellPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := consoleShellCommand(shellPath)
	if err != nil {
		t.Fatalf("consoleShellCommand: %v", err)
	}
	want := []string{shellPath, "-i"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("consoleShellCommand = %#v, want %#v", got, want)
	}
}

func TestConsoleShellCommandRejectsMissingShell(t *testing.T) {
	_, err := consoleShellCommand("/definitely/missing/microagent-shell")
	if err == nil {
		t.Fatal("consoleShellCommand error = nil")
	}
}

func TestResolveGuestCommandUsesGuestPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker-entrypoint.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveGuestCommand([]string{"docker-entrypoint.sh", "serve"}, []string{"PATH=" + dir})
	if err != nil {
		t.Fatalf("resolveGuestCommand: %v", err)
	}
	want := []string{bin, "serve"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveGuestCommand = %#v, want %#v", got, want)
	}
}

func TestResolveGuestCommandRejectsMissingBareCommand(t *testing.T) {
	_, err := resolveGuestCommand([]string{"definitely-missing-microagent-service"}, []string{"PATH=" + t.TempDir()})
	if err == nil {
		t.Fatal("resolveGuestCommand error = nil")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("resolveGuestCommand error = %v, want ErrNotFound", err)
	}
}

func TestValidateHostname(t *testing.T) {
	for _, hostname := range []string{"research", "homebridge-1"} {
		if err := validateHostname(hostname); err != nil {
			t.Fatalf("validateHostname(%q): %v", hostname, err)
		}
	}
	for _, hostname := range []string{"bad_name", "-bad", "bad-", ""} {
		if err := validateHostname(hostname); err == nil {
			t.Fatalf("validateHostname(%q) error = nil", hostname)
		}
	}
}

func TestParseKernelPNPNameservers(t *testing.T) {
	got := parseKernelPNPNameservers(`
#PROTO: DHCP
domain local
nameserver 192.168.64.1
# nameserver 8.8.8.8
nameserver not-an-ip
nameserver 192.168.64.1
`)
	want := []string{"192.168.64.1", "8.8.8.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nameservers = %#v, want %#v", got, want)
	}
}

func TestDefaultGatewayNameserver(t *testing.T) {
	route := filepath.Join(t.TempDir(), "route")
	data := `Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
eth0 00000000 0140A8C0 0003 0 0 0 00000000 0 0 0
`
	if err := os.WriteFile(route, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := defaultGatewayNameserver(route)
	if !ok || got != "192.168.64.1" {
		t.Fatalf("defaultGatewayNameserver = %q, %v; want 192.168.64.1, true", got, ok)
	}
}

func TestApplyKernelConfigOverridesSecretsControlPort(t *testing.T) {
	var cfg config
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "microagent_secrets_ctl_port=1028"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg.SecretsControlPort != 1028 {
		t.Fatalf("SecretsControlPort = %d, want 1028", cfg.SecretsControlPort)
	}
	var cfg2 config
	if err := applyKernelConfigOverridesFromCmdline(&cfg2, "microagent_secrets_ctl_port=bad"); err == nil {
		t.Fatal("expected error for non-numeric control port")
	}
}

func TestApplyKernelConfigOverridesSecretsAPI(t *testing.T) {
	var cfg config
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "microagent_secrets_port=1026 microagent_secrets_api=1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !cfg.SecretsAPI {
		t.Fatal("SecretsAPI should be true when microagent_secrets_api=1")
	}
	var cfg2 config
	if err := applyKernelConfigOverridesFromCmdline(&cfg2, "microagent_secrets_port=1026"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg2.SecretsAPI {
		t.Fatal("SecretsAPI should default false")
	}
}

func TestApplyKernelConfigOverridesSecretsPort(t *testing.T) {
	var cfg config
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "init=/sbin/microagent-init microagent_secrets_port=1026"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg.SecretsPort != 1026 {
		t.Fatalf("SecretsPort = %d, want 1026", cfg.SecretsPort)
	}
	var cfg2 config
	if err := applyKernelConfigOverridesFromCmdline(&cfg2, "microagent_secrets_port=notanumber"); err == nil {
		t.Fatal("expected error for non-numeric secrets port")
	}
}

func TestApplyKernelConfigModelFwd(t *testing.T) {
	var cfg config
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "console=ttyS0 microagent_model_fwd=11434:62100 rw"); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.ModelGuestPort != 11434 || cfg.ModelVsockPort != 62100 {
		t.Fatalf("got guest=%d vsock=%d", cfg.ModelGuestPort, cfg.ModelVsockPort)
	}
	// Malformed value is rejected.
	var bad config
	if err := applyKernelConfigOverridesFromCmdline(&bad, "microagent_model_fwd=notaport"); err == nil {
		t.Fatal("expected error for malformed model_fwd")
	}
}
