package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestFirecrackerDoctorDoesNotRequireAppleVFSupervisor(t *testing.T) {
	resp, err := firecrackerDoctorResponse(
		vmkit.BackendLinuxKVM,
		"amd64",
		func() (string, error) { return "/usr/local/bin/firecracker", nil },
		func(diagnostics.Options) (string, error) {
			return "/usr/local/bin/microagent-firecracker-supervisor", nil
		},
		func(diagnostics.Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
		func(path string) (os.FileInfo, error) {
			switch path {
			case "/dev/kvm", "/dev/vhost-vsock", "/dev/net/tun":
				return fakeFileInfo{name: filepath.Base(path)}, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		func(path string) string {
			if path != "/usr/local/bin/firecracker" {
				t.Fatalf("version path = %q", path)
			}
			return "Firecracker v1.15.1"
		},
		func(name string) (string, error) {
			if name == "pasta" {
				return "/usr/bin/pasta", nil
			}
			return "", os.ErrNotExist
		},
		func(path string) ([]byte, error) {
			switch path {
			case "/proc/sys/kernel/unprivileged_userns_clone":
				return []byte("1\n"), nil
			case "/proc/sys/user/max_user_namespaces":
				return []byte("32768\n"), nil
			}
			return nil, os.ErrNotExist
		},
		func() error { return nil },
	)
	if err != nil {
		t.Fatalf("firecrackerDoctorResponse: %v", err)
	}
	if !resp.OK {
		t.Fatalf("OK = false, error = %q", resp.Error)
	}
	if resp.Backend != vmkit.BackendLinuxKVM {
		t.Fatalf("Backend = %q, want %q", resp.Backend, vmkit.BackendLinuxKVM)
	}
	if resp.Host == nil {
		t.Fatal("Host is nil")
	}
	if resp.Host.BinaryPath != "/usr/local/bin/firecracker" {
		t.Fatalf("BinaryPath = %q", resp.Host.BinaryPath)
	}
	if resp.Host.BinaryVersion != "Firecracker v1.15.1" {
		t.Fatalf("BinaryVersion = %q", resp.Host.BinaryVersion)
	}
	if !resp.Host.VirtualizationSupported || !resp.Host.KVMAvailable || !resp.Host.VsockAvailable {
		t.Fatalf("Host support = %+v", resp.Host)
	}
	if !resp.Host.ConsoleAvailable || resp.Host.ConsoleMode != "interactive" {
		t.Fatalf("Console support = %+v", resp.Host)
	}
}

func TestFirecrackerDoctorReportsMissingHostSupport(t *testing.T) {
	resp, err := firecrackerDoctorResponse(
		vmkit.BackendLinuxKVM,
		"amd64",
		func() (string, error) { return "", fmt.Errorf("firecracker binary not found") },
		func(diagnostics.Options) (string, error) {
			return "", fmt.Errorf("microagent Firecracker supervisor not found")
		},
		func(diagnostics.Options) (string, error) { return "", fmt.Errorf("microagent guest init not found") },
		func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		func(string) string { return "" },
		func(string) (string, error) { return "", os.ErrNotExist },
		func(string) ([]byte, error) { return nil, os.ErrNotExist },
		func() error { return nil },
	)
	if err == nil {
		t.Fatal("firecrackerDoctorResponse returned nil error")
	}
	if resp.OK {
		t.Fatal("OK = true, want false")
	}
	if resp.Host == nil {
		t.Fatal("Host is nil")
	}
	if resp.Host.FrameworkAvailable || resp.Host.VirtualizationSupported || resp.Host.KVMAvailable {
		t.Fatalf("Host support = %+v", resp.Host)
	}
	if !strings.Contains(resp.Error, "firecracker binary not found") || !strings.Contains(resp.Error, "/dev/kvm") {
		t.Fatalf("Error = %q", resp.Error)
	}
}

func TestHostCommandReportsHostBackendDiagnosticsWithoutFailing(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "host.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "host", "--backend", hostBackend(), "--arch", defaultGuestArch()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run host: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	wantConsoleMode := "interactive"
	hasConfinementMode := strings.Contains(text, `"confinementMode": "off"`) ||
		strings.Contains(text, `"confinementMode": "jailer"`) ||
		strings.Contains(text, `"confinementMode": "rootless"`) ||
		strings.Contains(text, `"confinementMode": "seatbelt"`)
	// Console availability derives from supervisor presence, which may be absent
	// in a bare unit environment; require only that the field is reported, and
	// that the mode is present when the console is actually available.
	if strings.Contains(text, `"consoleAvailable": true`) &&
		!strings.Contains(text, fmt.Sprintf(`"consoleMode": "%s"`, wantConsoleMode)) {
		t.Fatalf("console reported available without mode %q: %s", wantConsoleMode, data)
	}
	if !strings.Contains(text, fmt.Sprintf(`"backend": "%s"`, hostBackend())) ||
		!strings.Contains(text, `"kernel"`) ||
		!strings.Contains(text, `"consoleAvailable"`) ||
		!hasConfinementMode {
		t.Fatalf("host output = %s", data)
	}
}

func TestHostUnknownSubcommandErrors(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	err := run(t.Context(), []string{"host", "bogus-subcommand"}, f)
	if err == nil || !strings.Contains(err.Error(), "bogus-subcommand") {
		t.Fatalf("expected unknown host subcommand error, got %v", err)
	}
}

func TestHostNoSubcommandStillReportsDiagnostics(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	// Existing behavior must be preserved: `host` with only flags reports.
	if err := run(t.Context(), []string{"--json", "host", "--backend", hostBackend(), "--arch", defaultGuestArch()}, f); err != nil {
		t.Fatalf("host report should not error: %v", err)
	}
	data, _ := os.ReadFile(f.Name())
	if !strings.Contains(string(data), "\"backend\"") {
		t.Fatalf("expected diagnostics JSON, got: %s", data)
	}
}

func TestHostCommandRejectsNonHostBackend(t *testing.T) {
	otherBackend := vmkit.BackendLinuxKVM
	if hostBackend() == vmkit.BackendLinuxKVM {
		otherBackend = vmkit.BackendAppleVF
	}
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "host", "--backend", otherBackend}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "is not available in this") {
		t.Fatalf("run host err = %v, want host-only backend rejection", err)
	}
}

func TestContractCommandReportsBackendNeutralRuntimeContract(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "contract.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"contract"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run contract: %v", err)
	}
	var contract vmkit.RuntimeContract
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Version != "agent-runtime.v1" {
		t.Fatalf("version = %q", contract.Version)
	}
	if !stringSliceContains(contract.Backends, vmkit.BackendAppleVF) || !stringSliceContains(contract.Backends, vmkit.BackendLinuxKVM) {
		t.Fatalf("backends = %#v", contract.Backends)
	}
	if !contractItemSliceContains(contract.Commands, "quarantine") || !contractItemSliceContains(contract.ReadinessSignals, "mediationReady") || !contractItemSliceContains(contract.ResultFields, "exitCode") {
		t.Fatalf("contract = %#v", contract)
	}
}

func TestAugmentHostSupportReportsAppleVFConsole(t *testing.T) {
	resp := vmkit.Response{Backend: vmkit.BackendAppleVF}
	augmentHostSupport(&resp, doctorOptions{Backend: vmkit.BackendAppleVF, Arch: "arm64", SupervisorPath: "/tmp/supervisor"})
	if resp.Host == nil {
		t.Fatal("Host is nil")
	}
	if resp.Host.SupervisorPath != "/tmp/supervisor" || !resp.Host.SupervisorAvailable {
		t.Fatalf("supervisor support = %+v", resp.Host)
	}
	if !resp.Host.ConsoleAvailable || resp.Host.ConsoleMode != "interactive" {
		t.Fatalf("console support = %+v", resp.Host)
	}
}

func TestResolveFirecrackerPathUsesEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "firecracker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_FIRECRACKER", path)
	got, err := resolveFirecrackerPath()
	if err != nil {
		t.Fatalf("resolveFirecrackerPath: %v", err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}

func TestDefaultFirecrackerPathResolvesHomebrewSymlink(t *testing.T) {
	dir := t.TempDir()
	cellarVersion := "test-version"
	cellarBin := filepath.Join(dir, "Cellar", "microagent", cellarVersion, "bin")
	cellarLibexec := filepath.Join(dir, "Cellar", "microagent", cellarVersion, "libexec")
	homebrewBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(cellarBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cellarLibexec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homebrewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(cellarBin, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	firecracker := filepath.Join(cellarLibexec, "firecracker")
	if err := os.WriteFile(firecracker, []byte("firecracker"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedFirecracker, err := filepath.EvalSymlinks(firecracker)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(homebrewBin, "microagent")
	symlinkOrSkip(t, executable, link)
	if got := defaultFirecrackerPathFromExecutable(link); got != resolvedFirecracker {
		t.Fatalf("defaultFirecrackerPathFromExecutable() = %q, want %q", got, resolvedFirecracker)
	}
}

func TestDefaultPackagedKernelPathResolvesHomebrewSymlink(t *testing.T) {
	dir := t.TempDir()
	cellarVersion := "test-version"
	cellarBin := filepath.Join(dir, "Cellar", "microagent", cellarVersion, "bin")
	cellarLibexec := filepath.Join(dir, "Cellar", "microagent", cellarVersion, "libexec")
	homebrewBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(cellarBin, 0o755); err != nil {
		t.Fatal(err)
	}
	kernelDir := filepath.Join(cellarLibexec, "kernels", "apple-vf", "arm64")
	if err := os.MkdirAll(kernelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homebrewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(cellarBin, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	kernel := filepath.Join(kernelDir, "Image")
	if err := os.WriteFile(kernel, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedKernel, err := filepath.EvalSymlinks(kernel)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(homebrewBin, "microagent")
	symlinkOrSkip(t, executable, link)
	if got := defaultPackagedKernelPathFromExecutable(link, "apple-vf", "arm64"); got != resolvedKernel {
		t.Fatalf("defaultPackagedKernelPathFromExecutable() = %q, want %q", got, resolvedKernel)
	}
}

func TestFirstOutputLine(t *testing.T) {
	output := "\nFirecracker v1.15.1\n\n2026-05-02T17:44:08 [anonymous-instance:main] Firecracker exiting successfully. exit_code=0\n"
	if got := firstOutputLine(output); got != "Firecracker v1.15.1" {
		t.Fatalf("firstOutputLine() = %q", got)
	}
}

func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" && strings.Contains(err.Error(), "A required privilege is not held by the client") {
			t.Skipf("symlink privilege unavailable on windows: %v", err)
		}
		t.Fatal(err)
	}
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
