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
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo", "hello world"}, map[string]string{
		"GOOD_ENV": "ok",
		"bad-env":  "ignored",
	}, "", 0, nil, nil)
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
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo", "hello"}, map[string]string{
		"GOOD_ENV": "ok",
		"bad-env":  "ignored",
	}, initBinary, 1024, []Mount{{Device: "/dev/vdb", Mountpoint: "/config", Mode: "ro"}}, []PortForward{{Protocol: "tcp", HostPort: 8080, GuestPort: 80}})
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
		!strings.Contains(text, `"/bin/echo"`) ||
		!strings.Contains(text, `"GOOD_ENV=ok"`) ||
		!strings.Contains(text, `"/config"`) ||
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
		t.Fatalf("mode = %#o, want 0700", info.Mode().Perm())
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
		t.Fatal(err)
	}
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo"}, nil, "", 0, nil, nil)
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
		t.Fatal(err)
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
	target, err := os.Readlink(filepath.Join(dir, "etc", "alternatives", "awk"))
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
	target, err := os.Readlink(filepath.Join(dir, "usr", "bin", "env"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "../../bin/env" {
		t.Fatalf("symlink target = %q", target)
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
