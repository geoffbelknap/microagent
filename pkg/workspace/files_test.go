package workspace

import (
	"os"
	"path/filepath"
	"runtime"
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
	debugfs := writeFakeDebugFS(t, dir, logPath, "'/workspace: File not found'")
	if _, err := Copy(dir, debugfs, source, "demo:/workspace/input.json"); err == nil {
		t.Fatal("expected missing parent directory error")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := debugFSLogForAssert(string(data))
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
	debugfs := writeFakeDebugFS(t, dir, logPath, "'Inode: 12   Type: directory    Mode:  0755'")
	if _, err := Copy(dir, debugfs, source, "demo:/workspace/input.json"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := debugFSLogForAssert(string(data))
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
	debugfs := writeFakeCommand(t, dir, "debugfs", "echo 'write: File not found by ext2_lookup'\n", "echo write: File not found by ext2_lookup\r\n")
	err := runDebugFS(debugfs, filepath.Join(dir, "rootfs.ext4"), true, "write input /missing/input")
	if err == nil {
		t.Fatal("expected debugfs diagnostic failure")
	}
}

func TestRunDebugFSDetectsAlreadyExistsFailure(t *testing.T) {
	dir := t.TempDir()
	debugfs := writeFakeCommand(t, dir, "debugfs", "echo 'write: Ext2 file already exists'\n", "echo write: Ext2 file already exists\r\n")
	err := runDebugFS(debugfs, filepath.Join(dir, "rootfs.ext4"), true, "write input /workspace/input")
	if err == nil {
		t.Fatal("expected debugfs already-exists diagnostic failure")
	}
}

func TestReconcileExt4JournalAcceptsCorrectedFilesystem(t *testing.T) {
	dir := t.TempDir()
	e2fsck := writeFakeCommand(t, dir, "e2fsck", "exit 1\n", "exit /b 1\r\n")
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
	e2fsck := writeFakeCommand(t, dir, "e2fsck",
		"printf '%s\\n' \"$*\" >> "+shellQuoteForTest(logPath)+"\n",
		"@echo %*>> \""+logPath+"\"\r\n",
	)
	old := e2fsckPath
	e2fsckPath = e2fsck
	t.Cleanup(func() { e2fsckPath = old })
	return logPath
}

func writeFakeDebugFS(t *testing.T, dir, logPath, statOutput string) string {
	t.Helper()
	return writeFakeCommand(t, dir, "debugfs",
		"printf '%s\\n' \"$*\" >> "+shellQuoteForTest(logPath)+"\n"+
			"case \"$*\" in\n"+
			"  *'stat /workspace'*) echo "+statOutput+" ;;\n"+
			"esac\n",
		"@echo %*>> \""+logPath+"\"\r\n"+
			"@echo %* | findstr /C:\"stat /workspace\" >NUL\r\n"+
			"@if %ERRORLEVEL% EQU 0 echo "+strings.Trim(statOutput, "'")+" \r\n",
	)
}

func writeFakeCommand(t *testing.T, dir, name, shellBody, cmdBody string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\n" + shellBody
	if runtime.GOOS == "windows" {
		path += ".cmd"
		body = cmdBody
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func debugFSLogForAssert(log string) string {
	return strings.ReplaceAll(log, "\"", "")
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
