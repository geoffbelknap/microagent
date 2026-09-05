package workspace

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestManifestAndStatusLifecycleAreLibraryOwned(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "agency-task",
		StateDir:       dir,
		Backend:        HostBackend(),
		Profile:        "small",
		RestartPolicy:  "never",
		MemoryMiB:      512,
		CPUCount:       2,
		SizeMiB:        1024,
		Network:        vmkit.NetworkConfig{Mode: "user"},
		ServiceCommand: "/opt/homebridge/start.sh --allow-root",
		ImageDefaults: rootfs.ImageDefaults{
			User: "1000:1000", WorkingDir: "/homebridge", ExposedPorts: []string{"8581/tcp"},
		},
		RootfsBase: &vmkit.RootfsBase{
			SHA256:    "0123456789abcdef",
			Immutable: true,
		},
		Disks: []Disk{{
			Name:       "work",
			Path:       "/tmp/work.ext4",
			Mountpoint: "/work",
			Mode:       "rw",
		}},
		Outputs: []Output{{Name: "result", Path: "/work/result.txt"}},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "agency-task")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Name != "agency-task" || manifest.Artifacts.Egress[0].Name != "result" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.Service != "/opt/homebridge/start.sh --allow-root" {
		t.Fatalf("manifest Service = %q", manifest.Service)
	}
	if manifest.RootfsBase == nil || manifest.RootfsBase.SHA256 != "0123456789abcdef" || !manifest.RootfsBase.Immutable {
		t.Fatalf("manifest rootfs base = %#v", manifest.RootfsBase)
	}

	req, err := Request(opts, "run", filepath.Join(dir, "workspaces", "agency-task", "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("status response = %#v", resp)
	}
	if resp.Artifacts == nil || len(resp.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", resp.Artifacts)
	}
	if resp.ImageDefaults == nil || resp.ImageDefaults.User != "1000:1000" || resp.ImageDefaults.WorkingDir != "/homebridge" {
		t.Fatalf("status image defaults = %#v", resp.ImageDefaults)
	}
	if resp.RootfsBase == nil || resp.RootfsBase.SHA256 != "0123456789abcdef" || !resp.RootfsBase.Immutable {
		t.Fatalf("status rootfs base = %#v", resp.RootfsBase)
	}
	// Status surfaces the machine-readable egress capture report (provider +
	// coverage), computed from the recorded backend/network/egress mode.
	if resp.EgressCapture == nil || resp.EgressCapture.Provider == "" || resp.EgressCapture.Mode == "" {
		t.Fatalf("status response missing egress capture report: %#v", resp.EgressCapture)
	}
	if _, err := os.Stat(filepath.Join(dir, "agency-task", "runtime.json")); err != nil {
		t.Fatalf("runtime.json not written: %v", err)
	}
	artifacts, err := ArtifactsFor(dir, "agency-task")
	if err != nil {
		t.Fatalf("ArtifactsFor: %v", err)
	}
	if len(artifacts.Egress) != 1 || artifacts.Egress[0].Name != "result" {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "agency-task" || entries[0].State != string(vmkit.StateRunning) {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestMissingLegacyGuestInitPathUsesRecordedContentIdentity(t *testing.T) {
	recorded := &vmkit.VerifiedArtifact{
		Path:   "/home/linuxbrew/.linuxbrew/Cellar/microagent-latest/old/libexec/microagent-guestinit-arm64",
		SHA256: "0123456789abcdef",
	}
	verification := vmkit.RuntimeVerification{OK: true}
	artifact := initArtifactForStatus(t.TempDir(), "legacy-workspace", recorded, &verification)
	if artifact.Path != "" {
		t.Fatalf("legacy init response path = %q, want obsolete path omitted", artifact.Path)
	}
	if artifact.SHA256 != recorded.SHA256 || artifact.RecordedSHA256 != recorded.SHA256 {
		t.Fatalf("legacy init identity = %#v, want recorded SHA-256 preserved", artifact)
	}
	if len(verification.Divergence) != 0 {
		t.Fatalf("missing package-manager source reported runtime divergence: %#v", verification.Divergence)
	}
}

func TestMissingPinnedGuestInitArtifactIsDivergence(t *testing.T) {
	stateDir := t.TempDir()
	recorded := &vmkit.VerifiedArtifact{
		Path:   guestInitArtifactPath(stateDir, "agent", "arm64", "0123456789abcdef"),
		SHA256: "0123456789abcdef",
	}
	verification := vmkit.RuntimeVerification{OK: true}
	artifact := initArtifactForStatus(stateDir, "agent", recorded, &verification)
	if artifact.Error == "" {
		t.Fatalf("missing pinned init = %#v, want an error", artifact)
	}
	if len(verification.Divergence) != 1 || verification.Divergence[0].Artifact != "init" {
		t.Fatalf("missing pinned init divergence = %#v", verification.Divergence)
	}
}

func TestListIgnoresTerminalRuntimeOnlyRecords(t *testing.T) {
	dir := t.TempDir()
	writeState := func(name string, state vmkit.VMState) {
		t.Helper()
		opts := Options{StateDir: dir, Name: name}
		req := vmkit.Request{
			Identity: &vmkit.Identity{RequestID: "req-" + name, RuntimeID: name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
			Config:   &vmkit.Config{StateDir: dir},
		}
		if err := WriteProcessState(opts, req, state, 1234, ""); err != nil {
			t.Fatalf("WriteProcessState(%s): %v", name, err)
		}
	}

	writeState("deleted", vmkit.StateStopped)
	writeState("live", vmkit.StateRunning)

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "live" {
		t.Fatalf("entries = %#v, want only live runtime-only workspace", entries)
	}

	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "saved"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeState("saved", vmkit.StateStopped)

	entries, err = List(dir)
	if err != nil {
		t.Fatalf("List after saved manifest: %v", err)
	}
	got := map[string]string{}
	for _, entry := range entries {
		got[entry.Name] = entry.State
	}
	if len(got) != 2 || got["live"] != string(vmkit.StateRunning) || got["saved"] != string(vmkit.StateStopped) {
		t.Fatalf("entries = %#v, want live runtime-only and saved terminal workspace", entries)
	}
}

func TestDefaultHostnameSanitizesWorkspaceName(t *testing.T) {
	tests := map[string]string{
		"homebridge":            "homebridge",
		"Home_Bridge.local":     "home-bridge-local",
		"---":                   "microagent",
		strings.Repeat("a", 70): strings.Repeat("a", 63),
	}
	for name, want := range tests {
		if got := DefaultHostname(name); got != want {
			t.Fatalf("DefaultHostname(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestValidateHostnameRejectsInvalidValues(t *testing.T) {
	for _, hostname := range []string{"bad_name", "-bad", "bad-", "", strings.Repeat("a", 64)} {
		if err := ValidateHostname(hostname); err == nil {
			t.Fatalf("ValidateHostname(%q) error = nil", hostname)
		}
	}
}

func TestBackendOwnsRuntimeState(t *testing.T) {
	if !backendOwnsRuntimeState(vmkit.BackendLinuxKVM) {
		t.Fatalf("backendOwnsRuntimeState(%q) = false, want true", vmkit.BackendLinuxKVM)
	}
	if backendOwnsRuntimeState(vmkit.BackendAppleVF) {
		t.Fatalf("backendOwnsRuntimeState(%q) = true, want false", vmkit.BackendAppleVF)
	}
}

func TestStatusNonLiveStatesMeasureAndCompareRootfs(t *testing.T) {
	for _, state := range []vmkit.VMState{vmkit.StatePrepared, vmkit.StateHalted, vmkit.StateStopped, vmkit.StateQuarantined, vmkit.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			dir := t.TempDir()
			kernelPath := filepath.Join(dir, "Image")
			if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
				t.Fatal(err)
			}
			rootfsPath := filepath.Join(dir, "workspaces", "agent", "rootfs.ext4")
			if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rootfsPath, []byte("tampered rootfs"), 0o644); err != nil {
				t.Fatal(err)
			}
			opts := Options{
				Name:          "agent",
				StateDir:      dir,
				Backend:       HostBackend(),
				KernelPath:    kernelPath,
				Profile:       "tiny",
				RestartPolicy: DefaultRestartPolicy,
				Verification: &vmkit.RuntimeVerification{
					OK: true,
					Kernel: &vmkit.VerifiedArtifact{
						Path:   kernelPath,
						SHA256: "recorded-kernel",
					},
					Rootfs: &vmkit.VerifiedArtifact{
						Path:   rootfsPath,
						SHA256: "recorded-rootfs",
					},
				},
			}
			if err := WriteManifest(opts); err != nil {
				t.Fatalf("WriteManifest: %v", err)
			}
			req, err := Request(opts, "inspect", rootfsPath, "req-1")
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			req.Config.KernelPath = kernelPath
			req.Config.RootfsPath = rootfsPath
			if err := WriteProcessState(opts, req, state, 0, ""); err != nil {
				t.Fatalf("WriteProcessState: %v", err)
			}

			start := time.Now()
			resp, err := Status(opts)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if elapsed := time.Since(start); elapsed >= time.Second {
				t.Fatalf("Status elapsed = %s, want < 1s", elapsed)
			}
			if resp.Verification == nil || resp.Verification.Rootfs == nil {
				t.Fatalf("verification = %#v", resp.Verification)
			}
			if resp.Verification.Rootfs.Error != "" {
				t.Fatalf("rootfs verification error = %q", resp.Verification.Rootfs.Error)
			}
			if resp.Verification.Rootfs.SHA256 == "recorded-rootfs" || resp.Verification.Rootfs.RecordedSHA256 != "recorded-rootfs" {
				t.Fatalf("rootfs verification = %#v, want fresh checksum compared with recorded checksum", resp.Verification.Rootfs)
			}
			rootfsDiverged := false
			for _, divergence := range resp.Verification.Divergence {
				rootfsDiverged = rootfsDiverged || divergence.Artifact == "rootfs"
			}
			if resp.Verification.OK || !rootfsDiverged {
				t.Fatalf("verification = %#v, want rootfs divergence", resp.Verification)
			}
			if resp.Readiness == nil {
				t.Fatal("readiness missing")
			}
			if resp.Readiness.ExecReady.Ready || !strings.Contains(resp.Readiness.ExecReady.Detail, "live readiness unavailable") {
				t.Fatalf("exec readiness = %#v, want fast unavailable detail", resp.Readiness.ExecReady)
			}
			if resp.Readiness.ShellReady.Ready || !strings.Contains(resp.Readiness.ShellReady.Detail, "live readiness unavailable") {
				t.Fatalf("shell readiness = %#v, want fast unavailable detail", resp.Readiness.ShellReady)
			}
			if resp.Readiness.ResultReady.Ready || !strings.Contains(resp.Readiness.ResultReady.Detail, "live readiness unavailable") {
				t.Fatalf("result readiness = %#v, want fast unavailable detail", resp.Readiness.ResultReady)
			}
		})
	}
}

func TestStatusMissingWorkspaceReturnsNotFound(t *testing.T) {
	_, err := Status(Options{Name: "no-such-workspace", StateDir: t.TempDir(), Backend: HostBackend()})
	var notFound WorkspaceNotFoundError
	if !errors.As(err, &notFound) || notFound.Name != "no-such-workspace" {
		t.Fatalf("Status err = %v, want WorkspaceNotFoundError", err)
	}
}

func TestStatusMalformedRuntimeStateIsNotNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "runtime.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Status(Options{Name: "agent", StateDir: dir, Backend: HostBackend()})
	if err == nil {
		t.Fatal("Status err = nil, want malformed-state error")
	}
	var notFound WorkspaceNotFoundError
	if errors.As(err, &notFound) {
		t.Fatalf("Status err = %v, want corrupt state surfaced, not WorkspaceNotFoundError", err)
	}
}

func TestStatusRunningWorkspaceStillChecksCurrentRootfs(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingRootfs := filepath.Join(dir, "workspaces", "agent", "rootfs.ext4")
	opts := Options{
		Name:          "agent",
		StateDir:      dir,
		Backend:       HostBackend(),
		KernelPath:    kernelPath,
		Profile:       "tiny",
		RestartPolicy: DefaultRestartPolicy,
		Verification: &vmkit.RuntimeVerification{
			OK:     true,
			Rootfs: &vmkit.VerifiedArtifact{Path: missingRootfs, SHA256: "recorded-rootfs"},
		},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	req, err := Request(opts, "inspect", missingRootfs, "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	req.Config.KernelPath = kernelPath
	req.Config.RootfsPath = missingRootfs
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 0, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Verification == nil || resp.Verification.Rootfs == nil || resp.Verification.Rootfs.Error == "" {
		t.Fatalf("rootfs verification = %#v, want current rootfs error for running workspace", resp.Verification)
	}
	// POSIX reports "no such file"; Windows reports "cannot find the file".
	if !strings.Contains(resp.Verification.Rootfs.Error, "no such file") && !strings.Contains(resp.Verification.Rootfs.Error, "cannot find the file") {
		t.Fatalf("rootfs error = %q", resp.Verification.Rootfs.Error)
	}
}

func TestReadinessFromRuntimeRequiresLiveShellTarget(t *testing.T) {
	for _, backend := range []string{vmkit.BackendLinuxKVM, vmkit.BackendAppleVF} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			runtimeDir := filepath.Join(dir, "agent")
			inputPath := filepath.Join(runtimeDir, "serial.in")
			serialPath := filepath.Join(runtimeDir, "serial.log")
			if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inputPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			state := RuntimeState{
				Event: EventFile{
					Identity:   vmkit.Identity{RuntimeID: "agent", Backend: backend},
					State:      vmkit.StateRunning,
					ObservedAt: time.Now().UTC().Format(time.RFC3339),
				},
				Config:          vmkit.Config{StateDir: dir, SerialInput: true, ShellPort: 24279},
				SerialInputPath: inputPath,
				SerialLogPath:   serialPath,
				StartedAt:       time.Now().UTC().Format(time.RFC3339),
			}
			readiness := readinessFromRuntime(state)
			if readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want not ready before shell target is reachable", readiness.ShellReady)
			}
			if !strings.Contains(readiness.ShellReady.Detail, "command probe failed") {
				t.Fatalf("shell readiness detail = %q, want command probe failure detail", readiness.ShellReady.Detail)
			}
			if err := os.WriteFile(serialPath, []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			readiness = readinessFromRuntime(state)
			if readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want not ready when only the guest helper log exists", readiness.ShellReady)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serveDone := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serveDone <- err
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				var command strings.Builder
				buf := make([]byte, 1024)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						command.Write(buf[:n])
						if strings.Contains(command.String(), "exit\r") {
							break
						}
					}
					if err != nil {
						serveDone <- err
						return
					}
				}
				text := command.String()
				tokenStart := strings.Index(text, "__ma_token=")
				if tokenStart == -1 {
					serveDone <- fmt.Errorf("command %q missing token assignment", text)
					return
				}
				tokenStart += len("__ma_token=")
				tokenEnd := strings.Index(text[tokenStart:], ";")
				if tokenEnd == -1 {
					serveDone <- fmt.Errorf("command %q missing token terminator", text)
					return
				}
				token := text[tokenStart : tokenStart+tokenEnd]
				_, err = fmt.Fprintf(conn, "__MICROAGENT_BEGIN_%s__\r\n__MICROAGENT_DONE_%s__0\r\n", token, token)
				serveDone <- err
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
			readiness = readinessFromRuntime(state)
			if !readiness.ShellReady.Ready {
				t.Fatalf("shell readiness = %#v, want ready when shell target completes a command probe", readiness.ShellReady)
			}
			if !strings.Contains(readiness.ShellReady.Detail, "command round-trip ready at") {
				t.Fatalf("shell readiness detail = %q", readiness.ShellReady.Detail)
			}
			if err := <-serveDone; err != nil {
				t.Fatalf("shell target probe server: %v", err)
			}
		})
	}
}
