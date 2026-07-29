//go:build linux

package main

import (
	"archive/tar"
	"bytes"
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

func TestKernelConfigOverrideSetsHostname(t *testing.T) {
	cfg := config{}
	if err := applyKernelConfigOverridesFromCmdline(&cfg, "console=ttyS0 microagent_hostname=research-vm root=/dev/vda"); err != nil {
		t.Fatalf("applyKernelConfigOverridesFromCmdline: %v", err)
	}
	if cfg.Hostname != "research-vm" {
		t.Fatalf("Hostname = %q, want research-vm", cfg.Hostname)
	}
	// Hostname is cmdline-only: with no parameter it stays empty and
	// configureHostname leaves the image's own hostname untouched.
	none := config{}
	if err := applyKernelConfigOverridesFromCmdline(&none, "console=ttyS0 root=/dev/vda"); err != nil {
		t.Fatalf("applyKernelConfigOverridesFromCmdline: %v", err)
	}
	if none.Hostname != "" {
		t.Fatalf("Hostname = %q, want empty without the parameter", none.Hostname)
	}
}

func TestKernelConfigOverrideRejectsBadHostname(t *testing.T) {
	for _, bad := range []string{"bad_name", "-bad", "UPPER..bad!"} {
		cfg := config{}
		if err := applyKernelConfigOverridesFromCmdline(&cfg, "microagent_hostname="+bad); err == nil {
			t.Fatalf("applyKernelConfigOverridesFromCmdline accepted hostname %q", bad)
		}
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

func TestShutdownCoordinatorPowerOffWinsAndMarks(t *testing.T) {
	// captureSends records every result the coordinator emits so the test can
	// assert the final/authoritative result without a live vsock connection.
	type capture struct {
		results []result
	}

	withCapture := func(t *testing.T) (*capture, func()) {
		t.Helper()
		cap := &capture{}
		prev := sendResultFunc
		sendResultFunc = func(_ uint32, res result) error {
			cap.results = append(cap.results, res)
			return nil
		}
		return cap, func() { sendResultFunc = prev }
	}

	t.Run("power-off before command result suppresses the killed result", func(t *testing.T) {
		cap, restore := withCapture(t)
		defer restore()
		c := &shutdownCoordinator{}

		c.emitPowerOffResult(0, result{})
		sent := c.emitCommandResult(0, result{ExitCode: 143, Error: "signal: killed"})

		if sent {
			t.Fatal("emitCommandResult sent a result after power-off claimed emission")
		}
		if len(cap.results) != 1 {
			t.Fatalf("emitted %d results, want exactly 1 (the power-off result)", len(cap.results))
		}
		if !cap.results[0].PoweredOff {
			t.Fatal("emitted result is not marked powered_off")
		}
		if cap.results[0].ExitedAt == "" {
			t.Fatal("power-off result missing exited_at timestamp")
		}
	})

	t.Run("power-off after command result still has the last word", func(t *testing.T) {
		cap, restore := withCapture(t)
		defer restore()
		c := &shutdownCoordinator{}

		// The command is killed by the shutdown and emits first; the power
		// handler fires immediately after and must overwrite it on the host.
		sent := c.emitCommandResult(0, result{ExitCode: 143, Error: "signal: killed"})
		c.emitPowerOffResult(0, result{})

		if !sent {
			t.Fatal("emitCommandResult should send when no power-off has claimed yet")
		}
		if len(cap.results) != 2 {
			t.Fatalf("emitted %d results, want 2 (killed result then power-off result)", len(cap.results))
		}
		last := cap.results[len(cap.results)-1]
		if !last.PoweredOff {
			t.Fatal("final emitted result is not marked powered_off")
		}
	})

	t.Run("command failure without power-off is emitted unmarked", func(t *testing.T) {
		cap, restore := withCapture(t)
		defer restore()
		c := &shutdownCoordinator{}

		sent := c.emitCommandResult(0, result{ExitCode: 1, Error: "boom"})

		if !sent {
			t.Fatal("emitCommandResult should send a genuine failure")
		}
		if len(cap.results) != 1 {
			t.Fatalf("emitted %d results, want 1", len(cap.results))
		}
		if cap.results[0].PoweredOff {
			t.Fatal("genuine failure must not be marked powered_off")
		}
	})
}

func TestShutdownResetFirst(t *testing.T) {
	if !shutdownResetFirst("console=ttyS0 microagent_shutdown=reset root=/dev/vda") {
		t.Fatal("expected reset-first when microagent_shutdown=reset is present")
	}
	if shutdownResetFirst("console=ttyS0 root=/dev/vda") {
		t.Fatal("expected power-off-first when marker absent")
	}
	if shutdownResetFirst("microagent_shutdown=poweroff") {
		t.Fatal("expected power-off-first for a non-reset marker value")
	}
}

// --- config disk ---

func writeConfigDeviceFile(t *testing.T, entries map[string][]byte, first string) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(name string, data []byte) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o640, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write(first, entries[first])
	for name, data := range entries {
		if name != first {
			write(name, data)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	// Pad like the host does: a device is larger than its tar payload.
	payload := append(buf.Bytes(), make([]byte, 4096)...)
	path := filepath.Join(t.TempDir(), "config.disk")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadConfigFromDeviceParsesRunConfig(t *testing.T) {
	device := writeConfigDeviceFile(t, map[string][]byte{
		"run.json": []byte(`{"command":["/bin/echo","hi"],"mode":"service","env":["A=1"],"port":1024,"maintenance":true}` + "\n"),
	}, "run.json")
	cfg, err := readConfigFromDevice(device)
	if err != nil {
		t.Fatalf("readConfigFromDevice: %v", err)
	}
	if len(cfg.Command) != 2 || cfg.Command[0] != "/bin/echo" || cfg.Mode != "service" || cfg.Port != 1024 || !cfg.Maintenance {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestReadConfigFromDeviceMaterializesDeclaredFiles(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "seed.txt")
	device := writeConfigDeviceFile(t, map[string][]byte{
		"run.json":                        []byte(`{"command":[]}` + "\n"),
		"files" + targetDir + "/seed.txt": []byte("seed-content\n"),
	}, "run.json")
	if _, err := readConfigFromDevice(device); err != nil {
		t.Fatalf("readConfigFromDevice: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("declared file not materialized: %v", err)
	}
	if string(data) != "seed-content\n" {
		t.Fatalf("declared file = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("declared file mode = %v, want 0640 from the archive header", info.Mode().Perm())
	}
}

func TestReadConfigFromDeviceRejectsForeignFirstEntry(t *testing.T) {
	device := writeConfigDeviceFile(t, map[string][]byte{
		"not-run.json": []byte("{}"),
	}, "not-run.json")
	if _, err := readConfigFromDevice(device); err == nil {
		t.Fatal("a device whose first entry is not run.json must fail closed")
	}
}

func TestValidConfigDevice(t *testing.T) {
	for device, want := range map[string]bool{
		"/dev/vdb":           true,
		"/dev/vdaa":          true,
		"/dev/vd":            false,
		"/dev/sda":           false,
		"/dev/vdb1":          false,
		"../etc/passwd":      false,
		"/dev/vdb; rm -rf /": false,
	} {
		if got := validConfigDevice(device); got != want {
			t.Errorf("validConfigDevice(%q) = %v, want %v", device, got, want)
		}
	}
}
