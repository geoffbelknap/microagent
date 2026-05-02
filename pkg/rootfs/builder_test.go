package rootfs

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteInitInjectsCommandAndValidEnv(t *testing.T) {
	dir := t.TempDir()
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo", "hello world"}, map[string]string{
		"GOOD_ENV": "ok",
		"bad-env":  "ignored",
	}, "", 0)
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
	}, initBinary, 1024)
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
		strings.Contains(text, "bad-env") {
		t.Fatalf("unexpected run config: %s", text)
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
