package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyToWorkspaceRequiresRemoteParentDir(t *testing.T) {
	dir := t.TempDir()
	useFakeE2FSCK(t, dir)
	rootfs := filepath.Join(dir, "workspaces", "demo", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfs), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeExtRootfs(t, rootfs)
	source := filepath.Join(dir, "input.json")
	if err := os.WriteFile(source, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "debugfs.log")
	debugfs := filepath.Join(dir, "debugfs")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \"$*\" in\n" +
		"  *'stat /workspace'*) echo '/workspace: File not found' ;;\n" +
		"esac\n"
	if err := os.WriteFile(debugfs, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(dir, debugfs, source, "demo:/workspace/input.json"); err == nil {
		t.Fatal("expected missing parent directory error")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, "-R stat /workspace "+rootfs) {
		t.Fatalf("debugfs log missing stat:\n%s", log)
	}
	if strings.Contains(log, "mkdir") || strings.Contains(log, "write") {
		t.Fatalf("debugfs unexpectedly mutated image:\n%s", log)
	}
}

func TestCopyToWorkspaceWritesWhenRemoteParentExists(t *testing.T) {
	dir := t.TempDir()
	e2fsckLog := useFakeE2FSCK(t, dir)
	rootfs := filepath.Join(dir, "workspaces", "demo", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfs), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeExtRootfs(t, rootfs)
	source := filepath.Join(dir, "input.json")
	if err := os.WriteFile(source, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "debugfs.log")
	debugfs := filepath.Join(dir, "debugfs")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \"$*\" in\n" +
		"  *'stat /workspace'*) echo 'Inode: 12   Type: directory    Mode:  0755' ;;\n" +
		"esac\n"
	if err := os.WriteFile(debugfs, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(dir, debugfs, source, "demo:/workspace/input.json"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, "-w -R write "+source+" /workspace/input.json "+rootfs) {
		t.Fatalf("debugfs log missing write:\n%s", log)
	}
	if !strings.Contains(log, "-w -R rm /workspace/input.json "+rootfs) {
		t.Fatalf("debugfs log missing pre-write rm:\n%s", log)
	}
	data, err = os.ReadFile(e2fsckLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-fy "+rootfs) {
		t.Fatalf("e2fsck log missing filesystem reconciliation:\n%s", data)
	}
}

func TestRunDebugFSDetectsDiagnosticFailures(t *testing.T) {
	dir := t.TempDir()
	debugfs := filepath.Join(dir, "debugfs")
	if err := os.WriteFile(debugfs, []byte("#!/bin/sh\necho 'write: File not found by ext2_lookup'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runDebugFS(debugfs, filepath.Join(dir, "rootfs.ext4"), true, "write input /missing/input")
	if err == nil {
		t.Fatal("expected debugfs diagnostic failure")
	}
}

func TestRunDebugFSDetectsAlreadyExistsFailure(t *testing.T) {
	dir := t.TempDir()
	debugfs := filepath.Join(dir, "debugfs")
	if err := os.WriteFile(debugfs, []byte("#!/bin/sh\necho 'write: Ext2 file already exists'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runDebugFS(debugfs, filepath.Join(dir, "rootfs.ext4"), true, "write input /workspace/input")
	if err == nil {
		t.Fatal("expected debugfs already-exists diagnostic failure")
	}
}

func TestReconcileExt4JournalAcceptsCorrectedFilesystem(t *testing.T) {
	dir := t.TempDir()
	e2fsck := filepath.Join(dir, "e2fsck")
	if err := os.WriteFile(e2fsck, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := e2fsckPath
	e2fsckPath = e2fsck
	t.Cleanup(func() { e2fsckPath = old })
	rootfs := filepath.Join(dir, "rootfs.ext4")
	writeFakeExtRootfs(t, rootfs)
	if err := reconcileExt4Journal(rootfs); err != nil {
		t.Fatal(err)
	}
}

func useFakeE2FSCK(t *testing.T, dir string) string {
	t.Helper()
	logPath := filepath.Join(dir, "e2fsck.log")
	e2fsck := filepath.Join(dir, "e2fsck")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n"
	if err := os.WriteFile(e2fsck, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := e2fsckPath
	e2fsckPath = e2fsck
	t.Cleanup(func() { e2fsckPath = old })
	return logPath
}

func writeFakeExtRootfs(t *testing.T, path string) {
	t.Helper()
	data := make([]byte, 1082)
	data[1080] = 0x53
	data[1081] = 0xef
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
