package rootfs

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestWriteInitInjectsCommandAndValidEnv(t *testing.T) {
	dir := t.TempDir()
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo", "hello world"}, "", map[string]string{
		"GOOD_ENV": "ok",
		"bad-env":  "ignored",
	}, "", 0, 0, nil, nil, "", "research")
	if err != nil {
		t.Fatalf("writeInit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sbin", "microagent-init"))
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "export GOOD_ENV='ok'") {
		t.Fatalf("init missing valid env: %s", text)
	}
	if !strings.Contains(text, "hostname 'research'") {
		t.Fatalf("init missing hostname setup: %s", text)
	}
	if strings.Contains(text, "bad-env") {
		t.Fatalf("init included invalid env: %s", text)
	}
	if !strings.Contains(text, "set -- '/bin/echo' 'hello world'") {
		t.Fatalf("init missing command: %s", text)
	}
	if !strings.Contains(text, "mkdir -p /proc /sys /dev") {
		t.Fatalf("init missing mount point setup: %s", text)
	}
}

func TestWriteInitCopiesGuestBinaryAndConfig(t *testing.T) {
	dir := t.TempDir()
	initBinary := filepath.Join(dir, "guestinit")
	if err := os.WriteFile(initBinary, []byte("guest-init"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo", "hello"}, "service", map[string]string{
		"GOOD_ENV": "ok",
		"bad-env":  "ignored",
	}, initBinary, 1024, 22222, []Mount{{Device: "/dev/vdb", Mountpoint: "/config", Mode: "ro"}}, []PortForward{{Protocol: "tcp", HostPort: 8080, GuestPort: 80}}, "/bin/bash", "research")
	if err != nil {
		t.Fatalf("writeInit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sbin", "microagent-init"))
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	if string(data) != "guest-init" {
		t.Fatalf("init binary = %q", data)
	}
	config, err := os.ReadFile(filepath.Join(dir, "etc", "microagent", "run.json"))
	if err != nil {
		t.Fatalf("read run config: %v", err)
	}
	text := string(config)
	if !strings.Contains(text, `"port":1024`) ||
		!strings.Contains(text, `"shellPort":22222`) ||
		!strings.Contains(text, `"mode":"service"`) ||
		!strings.Contains(text, `"/bin/echo"`) ||
		!strings.Contains(text, `"GOOD_ENV=ok"`) ||
		!strings.Contains(text, `"/config"`) ||
		!strings.Contains(text, `"consoleShell":"/bin/bash"`) ||
		!strings.Contains(text, `"hostname":"research"`) ||
		!strings.Contains(text, `"hostPort":8080`) ||
		strings.Contains(text, "bad-env") {
		t.Fatalf("unexpected run config: %s", text)
	}
}

func TestBuildCommandCanSkipImageCommand(t *testing.T) {
	image := ocispec.Image{}
	image.Config.Entrypoint = []string{"/entrypoint"}
	image.Config.Cmd = []string{"serve"}

	got := buildCommand(BuildRequest{NoImageCommand: true}, image)
	if len(got) != 0 {
		t.Fatalf("buildCommand = %#v, want no command", got)
	}
}

func TestBuildCommandUsesImageCommandByDefault(t *testing.T) {
	image := ocispec.Image{}
	image.Config.Entrypoint = []string{"/entrypoint"}
	image.Config.Cmd = []string{"serve"}

	got := buildCommand(BuildRequest{}, image)
	want := []string{"/entrypoint", "serve"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("buildCommand = %#v, want %#v", got, want)
	}
}

func TestBuildGuestEnvIncludesImageEnvAndRequestOverrides(t *testing.T) {
	image := ocispec.Image{}
	image.Config.Env = []string{
		"PATH=/usr/local/bin:/usr/bin",
		"IMAGE_ONLY=present",
		"OVERRIDE=image",
		"bad-env=ignored",
	}

	got := buildGuestEnv(map[string]string{
		"OVERRIDE": "request",
		"REQUEST":  "present",
		"bad-env":  "ignored",
	}, image)

	for key, want := range map[string]string{
		"PATH":       "/usr/local/bin:/usr/bin",
		"IMAGE_ONLY": "present",
		"OVERRIDE":   "request",
		"REQUEST":    "present",
	} {
		if got[key] != want {
			t.Fatalf("env[%s] = %q, want %q in %#v", key, got[key], want, got)
		}
	}
	if _, ok := got["bad-env"]; ok {
		t.Fatalf("env included invalid name: %#v", got)
	}
}

func TestSplitRegistryReferenceNormalizesDockerHubRefs(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantRepoRef   string
		wantReference string
	}{
		{
			name:          "official image with tag",
			raw:           "ubuntu:24.04",
			wantRepoRef:   "docker.io/library/ubuntu",
			wantReference: "24.04",
		},
		{
			name:          "official image with digest",
			raw:           "ubuntu@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			wantRepoRef:   "docker.io/library/ubuntu",
			wantReference: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:          "docker hub namespace with tag",
			raw:           "homebridge/homebridge:latest",
			wantRepoRef:   "docker.io/homebridge/homebridge",
			wantReference: "latest",
		},
		{
			name:          "docker hub namespace without tag",
			raw:           "homebridge/homebridge",
			wantRepoRef:   "docker.io/homebridge/homebridge",
			wantReference: "latest",
		},
		{
			name:          "docker io official shorthand",
			raw:           "docker.io/ubuntu:24.04",
			wantRepoRef:   "docker.io/library/ubuntu",
			wantReference: "24.04",
		},
		{
			name:          "explicit docker io namespace",
			raw:           "docker.io/homebridge/homebridge:latest",
			wantRepoRef:   "docker.io/homebridge/homebridge",
			wantReference: "latest",
		},
		{
			name:          "explicit ghcr registry",
			raw:           "ghcr.io/example/agent:latest",
			wantRepoRef:   "ghcr.io/example/agent",
			wantReference: "latest",
		},
		{
			name:          "explicit localhost registry",
			raw:           "localhost:5000/example/agent:latest",
			wantRepoRef:   "localhost:5000/example/agent",
			wantReference: "latest",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRepoRef, gotReference, err := splitRegistryReference(tc.raw)
			if err != nil {
				t.Fatalf("splitRegistryReference: %v", err)
			}
			if gotRepoRef != tc.wantRepoRef || gotReference != tc.wantReference {
				t.Fatalf("splitRegistryReference(%q) = %q, %q; want %q, %q", tc.raw, gotRepoRef, gotReference, tc.wantRepoRef, tc.wantReference)
			}
		})
	}
}

func TestWriteDeclaredFilesCopiesSourceIntoStage(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.sh")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeDeclaredFiles(dir, []File{{SourcePath: src, Path: "/app/source.sh", Mode: "0700"}}); err != nil {
		t.Fatalf("writeDeclaredFiles: %v", err)
	}
	target := filepath.Join(dir, "app", "source.sh")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho ok\n" {
		t.Fatalf("target content = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		modes, err := readStageModes(dir)
		if err != nil {
			t.Fatalf("read stage modes after host mode %#o: %v", info.Mode().Perm(), err)
		}
		if modes["app/source.sh"] != 0o700 {
			t.Fatalf("host mode = %#o and recorded mode = %#o, want recorded 0700", info.Mode().Perm(), modes["app/source.sh"])
		}
	}
}

func TestWriteDeclaredFilesRejectsRelativeGuestPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeDeclaredFiles(dir, []File{{SourcePath: src, Path: "app/source.txt"}}); err == nil {
		t.Fatal("expected relative guest path to be rejected")
	}
}

func TestWriteInitDoesNotFollowStageSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "sbin")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo"}, "", nil, "", 0, 0, nil, nil, "", "")
	if err == nil {
		t.Fatal("expected symlinked init parent to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "microagent-init")); !os.IsNotExist(err) {
		t.Fatalf("init escaped stage or stat failed: %v", err)
	}
}

func TestWriteDeclaredFilesDoesNotFollowStageSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "app")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	err := writeDeclaredFiles(dir, []File{{SourcePath: src, Path: "/app/source.txt"}})
	if err == nil {
		t.Fatal("expected symlinked file parent to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "source.txt")); !os.IsNotExist(err) {
		t.Fatalf("declared file escaped stage or stat failed: %v", err)
	}
}

func TestExtractLayerAllowsAbsoluteSymlinkWithinGuestRoot(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "etc/alternatives/awk", Typeflag: tar.TypeSymlink, Linkname: "/usr/bin/mawk", Mode: 0o777}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("extractLayer: %v", err)
	}
	target, err := readExtractedSymlinkTarget(filepath.Join(dir, "etc", "alternatives", "awk"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "/usr/bin/mawk" {
		t.Fatalf("symlink target = %q", target)
	}
}

func TestExtractLayerRejectsAbsoluteSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "app", Typeflag: tar.TypeSymlink, Linkname: "/../../outside", Mode: 0o777}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected absolute symlink escape to be rejected")
	}
}

func TestExtractLayerAllowsRelativeSymlinkWithinGuestRoot(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "bin", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatalf("write dir: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "bin/env", Mode: 0o755, Size: 2}); err != nil {
		t.Fatalf("write file header: %v", err)
	}
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "usr/bin/env", Typeflag: tar.TypeSymlink, Linkname: "../../bin/env", Mode: 0o777}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("extractLayer: %v", err)
	}
	target, err := readExtractedSymlinkTarget(filepath.Join(dir, "usr", "bin", "env"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "../../bin/env" {
		t.Fatalf("symlink target = %q", target)
	}
}

func TestWriteStageTarPreservesWindowsSymlinkMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSymlinkMarker(filepath.Join(dir, "usr", "bin", "env"), "../../bin/env"); err != nil {
		t.Fatalf("writeSymlinkMarker: %v", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeStageTar(dir, tw); err != nil {
		t.Fatalf("writeStageTar: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		header, err := tr.Next()
		if err != nil {
			t.Fatalf("missing symlink entry: %v", err)
		}
		if header.Name != "usr/bin/env" {
			continue
		}
		if header.Typeflag != tar.TypeSymlink || header.Linkname != "../../bin/env" {
			t.Fatalf("header = %#v, want symlink to ../../bin/env", header)
		}
		return
	}
}

func TestWriteStageTarPreservesRecordedMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sbin", "microagent-init"), []byte("guest-init"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := recordStageMode(root, "sbin/microagent-init", 0o755); err != nil {
		t.Fatalf("recordStageMode: %v", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeStageTar(dir, tw); err != nil {
		t.Fatalf("writeStageTar: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		header, err := tr.Next()
		if err != nil {
			t.Fatalf("missing init entry: %v", err)
		}
		if header.Name != "sbin/microagent-init" {
			continue
		}
		if os.FileMode(header.Mode).Perm() != 0o755 {
			t.Fatalf("mode = %#o, want 0755", os.FileMode(header.Mode).Perm())
		}
		return
	}
}

func TestEnsureGuestRuntimeDirsCreatesMountpointsAndRecordsModes(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGuestRuntimeDirs(dir); err != nil {
		t.Fatalf("ensureGuestRuntimeDirs: %v", err)
	}
	for _, rel := range []string{"proc", "sys", filepath.Join("dev", "pts")} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", rel)
		}
	}
	modes, err := readStageModes(dir)
	if err != nil {
		t.Fatalf("readStageModes: %v", err)
	}
	if modes["proc"] != 0o755 || modes["sys"] != 0o755 || modes["dev/pts"] != 0o755 {
		t.Fatalf("runtime dir modes = %#v", modes)
	}
}

func TestExtractLayerRejectsRelativeSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "app/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside", Mode: 0o777}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected relative symlink escape to be rejected")
	}
}

func TestExtractLayerRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 4}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte("nope")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected traversal entry to be rejected")
	}
}

func TestExtractLayerAppliesWhiteout(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := addTarFile(tw, "etc/old", "old"); err != nil {
		t.Fatalf("add old: %v", err)
	}
	if err := addTarFile(tw, "etc/.wh.old", ""); err != nil {
		t.Fatalf("add whiteout: %v", err)
	}
	if err := addTarFile(tw, "etc/new", "new"); err != nil {
		t.Fatalf("add new: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("extract layer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "old")); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed unexpectedly: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "etc", "new")); err != nil || string(data) != "new" {
		t.Fatalf("new file = %q, %v", string(data), err)
	}
}

func addTarFile(tw *tar.Writer, name, body string) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
		return err
	}
	_, err := tw.Write([]byte(body))
	return err
}

func readExtractedSymlinkTarget(path string) (string, error) {
	target, err := os.Readlink(path)
	if err == nil {
		return target, nil
	}
	if markerTarget, ok, markerErr := readSymlinkMarker(path); markerErr == nil && ok {
		return markerTarget, nil
	}
	return "", err
}
