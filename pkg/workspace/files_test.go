package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func forceDebugFSCopyPath(t *testing.T) {
	t.Helper()
}

func TestCopyToWorkspaceRequiresRemoteParentDir(t *testing.T) {
	forceDebugFSCopyPath(t)
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
	if _, err := Copy(t.Context(), dir, debugfs, source, "demo:/workspace/input.json"); err == nil {
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
	forceDebugFSCopyPath(t)
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
	var events []operation.ProgressEvent
	if _, err := CopyWithOptions(t.Context(), CopyOptions{
		StateDir: dir, DebugFSPath: debugfs, Source: source, Target: "demo:/workspace/input.json",
		Progress: func(event operation.ProgressEvent) { events = append(events, event) },
	}); err != nil {
		t.Fatal(err)
	}
	var phases []string
	for _, event := range events {
		phases = append(phases, event.Phase)
		if strings.Contains(event.Message, source) || strings.Contains(event.Message, "input.json") {
			t.Fatalf("copy progress exposed a caller path: %#v", event)
		}
	}
	assertProgressPhaseOrder(t, phases, []string{"copy_validate", "copy_reconcile", "copy_write", "copy_written"})
	last := events[len(events)-1]
	if last.Bytes != int64(len(`{"ok":true}`)) || last.TotalBytes != last.Bytes {
		t.Fatalf("copy byte progress = %#v", last)
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

func TestCopyFromWorkspaceQuotesDebugFSArguments(t *testing.T) {
	forceDebugFSCopyPath(t)
	dir := t.TempDir()
	useFakeE2FSCK(t, dir)
	rootfs := filepath.Join(dir, "workspaces", "demo", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfs), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeExtRootfs(t, rootfs)
	// A local target directory containing a space exercises the quoting: the
	// temp dump path lands inside it and must survive debugfs tokenization.
	targetDir := filepath.Join(dir, "out dir")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "debugfs.log")
	debugfs := writeFakeDebugFS(t, dir, logPath, "''")
	if _, err := Copy(t.Context(), dir, debugfs, "demo:/workspace/out.json", filepath.Join(targetDir, "result.json")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// Unescape the Windows .cmd shim's re-escaped quotes so the quoting
	// assertion checks the same tokens on every host.
	log := strings.ReplaceAll(string(data), `\"`, `"`)
	if !strings.Contains(log, `-R dump "/workspace/out.json" "`) && !strings.Contains(log, `-R "dump "/workspace/out.json" "`) {
		t.Fatalf("debugfs log missing quoted dump arguments:\n%s", log)
	}
	if !strings.Contains(log, `out dir/.microagent-cp-`) && !strings.Contains(log, `out dir\.microagent-cp-`) {
		t.Fatalf("debugfs log missing quoted temp path inside spaced directory:\n%s", log)
	}
}

func TestGetArtifactRejectsCraftedManifestPath(t *testing.T) {
	forceDebugFSCopyPath(t)
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspaces", "demo")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A crafted egress path that would smuggle a second debugfs token.
	manifest := `{"name":"demo","restart":"never","resources":{"memory_mib":256,"cpu_count":1},` +
		`"artifacts":{"egress":[{"name":"report","path":"/etc/passwd /tmp/owned"}]}}`
	if err := os.WriteFile(filepath.Join(workspaceDir, "workspace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "debugfs.log")
	debugfs := writeFakeDebugFS(t, dir, logPath, "''")
	_, err := GetArtifact(t.Context(), dir, debugfs, "demo", "report", filepath.Join(dir, "report.json"))
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("expected whitespace rejection for crafted artifact path, got %v", err)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatal("debugfs ran despite invalid artifact path")
	}
}

func TestParseRemoteCopyEndpointDisambiguatesDrivePaths(t *testing.T) {
	for _, local := range []string{`C:\Users\geoff\out`, "C:/Users/geoff/out", `d:\data`, "x:/tmp"} {
		if _, remote, err := parseRemoteCopyEndpoint(local); err != nil || remote {
			t.Fatalf("parseRemoteCopyEndpoint(%q) = remote=%v err=%v, want local path", local, remote, err)
		}
	}
	endpoint, remote, err := parseRemoteCopyEndpoint("demo:/etc/conf")
	if err != nil || !remote || endpoint.Workspace != "demo" || endpoint.Path != "/etc/conf" {
		t.Fatalf("parseRemoteCopyEndpoint(demo:/etc/conf) = %+v remote=%v err=%v", endpoint, remote, err)
	}
}

func TestQuoteDebugFSArgRejectsInjection(t *testing.T) {
	for _, bad := range []string{
		"",
		`/path/with"quote`,
		"/path/with\nnewline",
		"/path/with\rreturn",
		"/path/with\x00nul",
		"/path/with\ttab",
		"-rf",
	} {
		if _, err := quoteDebugFSArg(bad); err == nil {
			t.Fatalf("quoteDebugFSArg(%q) unexpectedly succeeded", bad)
		}
	}
	got, err := quoteDebugFSArg("/path with space/file.json")
	if err != nil || got != `"/path with space/file.json"` {
		t.Fatalf("quoteDebugFSArg = %q, %v", got, err)
	}
	req, err := debugfsRequest("dump", "/a/b", "/tmp/out")
	if err != nil || req != `dump "/a/b" "/tmp/out"` {
		t.Fatalf("debugfsRequest = %q, %v", req, err)
	}
	if _, err := debugfsRequest("dump", "/a/b", "/tmp/out\nrm /etc"); err == nil {
		t.Fatal("debugfsRequest accepted newline injection")
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
			"  *'stat \"/workspace\"'*) echo "+statOutput+" ;;\n"+
			"esac\n",
		"@echo %*>> \""+logPath+"\"\r\n"+
			"@echo %* | findstr /C:\"stat\" >NUL\r\n"+
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
	// The Windows .cmd shim echoes the re-escaped command line, so strip
	// escaped quotes before bare ones.
	log = strings.ReplaceAll(log, "\\\"", "")
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
