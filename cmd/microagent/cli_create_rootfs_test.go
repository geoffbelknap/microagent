package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
)

func TestParseWorkspaceOptionsAcceptsPositionalNameWithImageCommand(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"homebridge",
		"--image", "homebridge/homebridge:latest",
		"--image-command",
		"--network", "user",
		"--publish", "8581:8581",
		"--size-mib", "4096",
		"--restart", "always",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "homebridge" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if !opts.UseImageCommand {
		t.Fatal("UseImageCommand = false")
	}
}

func TestParseWorkspaceOptionsAcceptsServiceCommand(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"homebridge",
		"--image", "homebridge/homebridge:latest",
		"--service-command", "/opt/homebridge/start.sh --allow-root",
		"--network", "user",
		"--publish", "8581:8581",
		"--size-mib", "4096",
		"--restart", "always",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "homebridge" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if opts.ServiceCommand != "/opt/homebridge/start.sh --allow-root" {
		t.Fatalf("ServiceCommand = %q", opts.ServiceCommand)
	}
}

func TestParseWorkspaceOptionsRejectsImageAndServiceCommand(t *testing.T) {
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"homebridge",
		"--image", "homebridge/homebridge:latest",
		"--image-command",
		"--service-command", "/opt/homebridge/start.sh --allow-root",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot use both") {
		t.Fatalf("parseWorkspaceOptions err = %v", err)
	}
}

func TestWorkspaceBuildCommandKeepsSetupResultPort(t *testing.T) {
	command, port := workspaceBuildCommandAndPort(workspaceOptions{
		Entrypoint:      "/app/entrypoint.sh",
		SetupCommands:   []string{"echo setup"},
		ResultPort:      1024,
		PrepareForStart: true,
	})
	if port != 1024 {
		t.Fatalf("port = %d, want 1024", port)
	}
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "echo setup") {
		t.Fatalf("command = %#v", command)
	}
	if strings.Contains(joined, "/etc/microagent/run.json") {
		t.Fatalf("setup command should not embed guest config reset: %#v", command)
	}
}

func TestCreateWorkspaceRootfsUsesPulledBaseline(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "images", "rootfs", "baseline.ext4")
	if err := os.MkdirAll(filepath.Dir(baseline), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseline, []byte("baseline"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, imagecache.Record{
		ImageRef:    "local/busybox:baseline",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  baseline,
		SizeBytes:   8,
		LastUsedAt:  "2026-05-06T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := createWorkspaceRootfs(t.Context(), workspaceOptions{
		StateDir:        dir,
		Name:            "research",
		ImageRef:        "local/busybox:baseline",
		Architecture:    "arm64",
		Profile:         "small",
		RestartPolicy:   "never",
		Network:         vmkit.NetworkConfig{Mode: defaultNetworkMode},
		MemoryMiB:       512,
		CPUCount:        2,
		SizeMiB:         1024,
		PrepareForStart: true,
	})
	if err != nil {
		t.Fatalf("createWorkspaceRootfs: %v", err)
	}
	data, err := os.ReadFile(result.RootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "baseline" {
		t.Fatalf("rootfs = %q", data)
	}
	if result.Image.BuilderPhase != "copy-baseline" {
		t.Fatalf("image provenance = %#v", result.Image)
	}
}

// writeLocalImageLayout writes a tiny single-layer OCI image directly into a
// committed-OCI layout at dir, tagged with ref, without depending on
// pkg/commit's own extraction machinery (debugfs/guest-mediated copy).
func writeLocalImageLayout(t *testing.T, dir, ref string) {
	t.Helper()
	var layerBuf bytes.Buffer
	tw := tar.NewWriter(&layerBuf)
	content := "microagent-local-image-test\n"
	if err := tw.WriteHeader(&tar.Header{Name: "etc/microagent-local-image.txt", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	layerBytes := layerBuf.Bytes()
	layerDigest := digest.FromBytes(layerBytes)
	configBytes, err := json.Marshal(map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"rootfs": map[string]any{
			"type":     "layers",
			"diff_ids": []string{layerDigest.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configDigest := digest.FromBytes(configBytes)
	manifestBytes, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    configDigest,
			Size:      int64(len(configBytes)),
		},
		Layers: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    layerDigest,
			Size:      int64(len(layerBytes)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := digest.FromBytes(manifestBytes)

	store, err := oci.New(dir)
	if err != nil {
		t.Fatalf("oci.New: %v", err)
	}
	ctx := context.Background()
	push := func(data []byte, mediaType string, dgst digest.Digest) {
		t.Helper()
		desc := ocispec.Descriptor{MediaType: mediaType, Digest: dgst, Size: int64(len(data))}
		if err := store.Push(ctx, desc, bytes.NewReader(data)); err != nil {
			t.Fatalf("push %s: %v", mediaType, err)
		}
	}
	push(layerBytes, ocispec.MediaTypeImageLayer, layerDigest)
	push(configBytes, ocispec.MediaTypeImageConfig, configDigest)
	manifestDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: manifestDigest, Size: int64(len(manifestBytes))}
	push(manifestBytes, ocispec.MediaTypeImageManifest, manifestDigest)
	if err := store.Tag(ctx, manifestDesc, ref); err != nil {
		t.Fatalf("tag %s: %v", ref, err)
	}
}

// TestCreateWorkspaceRootfsResolvesLocallyCommittedImage confirms
// createWorkspaceRootfs threads LocalImageLayout = commit.LayoutPath(StateDir)
// into the rootfs.BuildRequest it builds: a workspace create for a ref that
// only exists in the local committed-OCI layout succeeds with no network.
func TestCreateWorkspaceRootfsResolvesLocallyCommittedImage(t *testing.T) {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		t.Skip("mke2fs not available")
	}

	dir := t.TempDir()
	const ref = "microagent-local-image-test.invalid/demo:v1"
	writeLocalImageLayout(t, commit.LayoutPath(dir), ref)

	result, err := createWorkspaceRootfs(t.Context(), workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		ImageRef:      ref,
		Architecture:  "amd64",
		Profile:       "small",
		RestartPolicy: "never",
		Network:       vmkit.NetworkConfig{Mode: defaultNetworkMode},
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       64,
		Mke2fsPath:    mke2fsPath,
	})
	if err != nil {
		t.Fatalf("createWorkspaceRootfs: %v", err)
	}
	if _, err := os.Stat(result.RootfsPath); err != nil {
		t.Fatalf("rootfs output: %v", err)
	}
}

func TestDefaultGuestInitPathResolvesHomebrewSymlink(t *testing.T) {
	dir := t.TempDir()
	cellarBin := filepath.Join(dir, "Cellar", "microagent", "0.1.14", "bin")
	cellarLibexec := filepath.Join(dir, "Cellar", "microagent", "0.1.14", "libexec")
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
	guestInit := filepath.Join(cellarLibexec, "microagent-guestinit-arm64")
	if err := os.WriteFile(guestInit, []byte("guest-init"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedGuestInit, err := filepath.EvalSymlinks(guestInit)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(homebrewBin, "microagent")
	symlinkOrSkip(t, executable, link)
	if got := defaultGuestInitPathFromExecutable(link, "arm64"); got != resolvedGuestInit {
		t.Fatalf("defaultGuestInitPathFromExecutable() = %q, want %q", got, resolvedGuestInit)
	}
}

func TestWorkspaceHasGuestCommand(t *testing.T) {
	if !workspaceHasGuestCommand(workspaceOptions{SetupCommands: []string{"echo setup"}}) {
		t.Fatal("setup command should count as guest work")
	}
	if !workspaceHasGuestCommand(workspaceOptions{ExecCommand: "echo run"}) {
		t.Fatal("exec command should count as guest work")
	}
	if !workspaceHasGuestCommand(workspaceOptions{ServiceCommand: "sleep infinity"}) {
		t.Fatal("service command should count as guest work")
	}
	if workspaceHasGuestCommand(workspaceOptions{SetupCommands: []string{"  "}}) {
		t.Fatal("blank setup command should not count as guest work")
	}
}

func TestConsoleLooksReady(t *testing.T) {
	tests := []struct {
		output string
		want   bool
	}{
		{output: "microagent login:", want: true},
		{output: "root@vm:/# ", want: true},
		{output: "user@vm:~$ ", want: true},
		{output: "booting kernel", want: false},
	}
	for _, tt := range tests {
		if got := consoleLooksReady(tt.output); got != tt.want {
			t.Fatalf("consoleLooksReady(%q) = %v, want %v", tt.output, got, tt.want)
		}
	}
}

func TestWaitForConsoleReadyUsesSerialPrompt(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "serial.log")
	if err := os.WriteFile(logPath, []byte("boot\n/ # "), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := waitForConsoleReady(t.Context(), logPath, time.Second); err != nil {
		t.Fatalf("waitForConsoleReady: %v", err)
	}
}

func TestRunConnectRejectsNegativeReadyTimeoutForInteractive(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "connect.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runConnect(t.Context(), []string{"research", "--state-dir", dir, "--ready-timeout", "-1"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "ready-timeout must not be negative") {
		t.Fatalf("runConnect err = %v", err)
	}
}

func TestWorkspaceShellReadinessRequiresReachableShellTarget(t *testing.T) {
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
	state := workspaceRuntimeState{
		Event: workspaceEventFile{
			Identity:   vmkit.Identity{RuntimeID: "agent", Backend: vmkit.BackendAppleVF},
			State:      vmkit.StateRunning,
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Config:          vmkit.Config{StateDir: dir, SerialInput: true, ShellPort: 24279},
		SerialInputPath: inputPath,
		SerialLogPath:   serialPath,
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	readiness := workspaceReadinessFromRuntime(state)
	if readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v, want not ready before shell target is reachable", readiness.ShellReady)
	}
	if err := os.WriteFile(serialPath, []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readiness = workspaceReadinessFromRuntime(state)
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
		_, err = fmt.Fprintf(conn, "\r\n__MICROAGENT_DONE_%s__0\r\n", token)
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
	readiness = workspaceReadinessFromRuntime(state)
	if !readiness.ShellReady.Ready {
		t.Fatalf("shell readiness = %#v, want ready when shell target completes a command probe", readiness.ShellReady)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("shell target probe server: %v", err)
	}
}
