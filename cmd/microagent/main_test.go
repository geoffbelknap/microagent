package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	firecrackersupervisor "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
	windowshyperv "github.com/geoffbelknap/microagent/pkg/supervisors/windows_hyperv"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_FIRECRACKER_SUPERVISOR_HELPER") == "1" {
		var req vmkit.Request
		if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(vmkit.Response{OK: false, Backend: hostBackend(), Error: err.Error()})
			os.Exit(1)
		}
		resp, err := firecrackersupervisor.Supervisor{}.Do(context.Background(), req)
		_ = json.NewEncoder(os.Stdout).Encode(resp)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunVersionAliases(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		dir := t.TempDir()
		stdoutPath := filepath.Join(dir, "stdout.txt")
		stdout, err := os.Create(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		err = run(t.Context(), args, stdout)
		if closeErr := stdout.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "microagent ") {
			t.Fatalf("version output = %q", data)
		}
	}
}

func TestHelpListsPauseAndResume(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"help"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run help: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	help := string(data)
	for _, command := range []string{"pause", "resume", "snapshot"} {
		if !strings.Contains(help, "\n  "+command+" ") {
			t.Fatalf("help missing %q command:\n%s", command, help)
		}
	}
}

func TestParseForkSnapshotRef(t *testing.T) {
	source, tag, err := parseForkSnapshotRef("base:pre-upgrade")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if source != "base" || tag != "pre-upgrade" {
		t.Fatalf("got source=%q tag=%q, want base/pre-upgrade", source, tag)
	}
	for _, bad := range []string{"", "base", "base:", ":tag", "   "} {
		if _, _, err := parseForkSnapshotRef(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestReorderFlagArgsKeepsTagAndFromSnapshotValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		flag string
		want string
	}{
		{"snapshot tag", []string{"agent-1", "--tag", "base", "--state-dir", "/s"}, "-tag", "base"},
		{"from-snapshot", []string{"agent-1", "--from-snapshot", "base", "--state-dir", "/s"}, "-from-snapshot", "base"},
		{"secret", []string{"app", "--secret", "API=env:TOKEN"}, "-secret", "API=env:TOKEN"},
		{"secrets-env-file", []string{"app", "--image", "img", "--secrets-env-file", "/tmp/app.env"}, "-secrets-env-file", "/tmp/app.env"},
		{"secret-on-demand", []string{"app", "--secret-on-demand", "DB=env:DB"}, "-secret-on-demand", "DB=env:DB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reordered := reorderFlagArgs(tc.args)
			// The value flag and its argument must stay adjacent so flag parsing
			// does not mistake the value for a positional and the positional name
			// for an extra argument.
			found := false
			for i := 0; i+1 < len(reordered); i++ {
				if reordered[i] == tc.flag && reordered[i+1] == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("reorderFlagArgs(%v) = %v; %s value not kept adjacent", tc.args, reordered, tc.flag)
			}
		})
	}
}

func TestRunSnapshotCreateParsesTagAndName(t *testing.T) {
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// With a missing supervisor the create fails at dispatch, but it must get
	// past argument parsing — proving --tag is recognized as a value flag and
	// the positional name is accepted rather than mistaken for extra arguments.
	rerr := run(t.Context(), []string{"snapshot", "create", "agent-1", "--tag", "base", "--state-dir", dir, "--supervisor", filepath.Join(dir, "no-supervisor")}, out)
	_ = out.Close()
	if rerr == nil {
		t.Fatal("expected dispatch error with a missing supervisor")
	}
	if strings.Contains(rerr.Error(), "usage:") {
		t.Fatalf("snapshot create mis-parsed --tag/name: %v", rerr)
	}
}

func TestRunSnapshotListAndRemove(t *testing.T) {
	dir := t.TempDir()
	name := "agent-1"
	for _, tag := range []string{"snap-a", "snap-b"} {
		sdir := vmkit.SnapshotDir(dir, name, tag)
		if err := vmkit.WriteSnapshotManifest(sdir, vmkit.SnapshotManifest{Tag: tag, MemoryMiB: 512, CreatedAt: "2026-06-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}

	listPath := filepath.Join(dir, "list.json")
	stdout, err := os.Create(listPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "snapshot", "list", name, "--state-dir", dir}, stdout)
	_ = stdout.Close()
	if err != nil {
		t.Fatalf("snapshot list: %v", err)
	}
	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "snap-a") || !strings.Contains(string(data), "snap-b") {
		t.Fatalf("snapshot list output missing tags:\n%s", data)
	}

	rmOut, err := os.Create(filepath.Join(dir, "rm.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"snapshot", "rm", name, "snap-a", "--state-dir", dir}, rmOut)
	_ = rmOut.Close()
	if err != nil {
		t.Fatalf("snapshot rm: %v", err)
	}
	if _, err := os.Stat(vmkit.SnapshotDir(dir, name, "snap-a")); !os.IsNotExist(err) {
		t.Fatalf("snap-a should be removed, stat err = %v", err)
	}
}

func TestCreateHelpUsesWorkspaceHelp(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"create", "--help"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run create --help: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "microagent create") ||
		!strings.Contains(text, "-entrypoint <command>") ||
		!strings.Contains(text, "-v SRC:DST[:ro|rw]") ||
		!strings.Contains(text, "-p host:guest[/tcp]") ||
		!strings.Contains(text, "-dry-run") {
		t.Fatalf("create help = %s", text)
	}
	if strings.Contains(text, "Rootfs image path") {
		t.Fatalf("create help exposed low-level supervisor flags: %s", text)
	}
}

func TestHighLevelCommandHelpDoesNotFallThroughToSupervisorFlags(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{command: "start", want: "Usage of start:"},
		{command: "delete", want: "Confirm workspace deletion without prompting"},
		{command: "status", want: "Workspace name"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			dir := t.TempDir()
			stdoutPath := filepath.Join(dir, "stdout.txt")
			stdout, err := os.Create(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			err = run(t.Context(), []string{tt.command, "--help"}, stdout)
			if closeErr := stdout.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatalf("run %s --help: %v", tt.command, err)
			}
			data, err := os.ReadFile(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if !strings.Contains(text, tt.want) {
				t.Fatalf("%s help = %s, want %q", tt.command, text, tt.want)
			}
			if strings.Contains(text, "Rootfs image path") || strings.Contains(text, "Read request JSON") {
				t.Fatalf("%s help exposed low-level supervisor flags: %s", tt.command, text)
			}
		})
	}
}

func TestGlobalJSONOutputSwitch(t *testing.T) {
	outputFormat = ""
	globalOutputMode = ""
	t.Cleanup(func() {
		outputFormat = ""
		globalOutputMode = ""
	})
	args := parseGlobalFlags([]string{"--json", "doctor"})
	if outputFormat != "json" {
		t.Fatalf("outputFormat = %q, want json", outputFormat)
	}
	if len(args) != 1 || args[0] != "doctor" {
		t.Fatalf("args = %#v", args)
	}
}

func TestGlobalOutputModeSwitch(t *testing.T) {
	outputFormat = ""
	globalOutputMode = ""
	t.Cleanup(func() {
		outputFormat = ""
		globalOutputMode = ""
	})
	args := parseGlobalFlags([]string{"--mode=ax", "profiles"})
	if globalOutputMode != outputModeAX {
		t.Fatalf("globalOutputMode = %q, want ax", globalOutputMode)
	}
	if len(args) != 1 || args[0] != "profiles" {
		t.Fatalf("args = %#v", args)
	}
}

func TestAXModeVersionOutput(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--mode=ax", "version"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run version: %v", err)
	}
	var got map[string]string
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode version output %q: %v", data, err)
	}
	if got["name"] != "microagent" || got["version"] == "" {
		t.Fatalf("version output = %#v", got)
	}
}

func TestAXModeFromEnvironment(t *testing.T) {
	t.Setenv("MICROAGENT_MODE", "ax")
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"profiles"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run profiles: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || !strings.Contains(string(data), "profiles") {
		t.Fatalf("profiles output = %q, want JSON", data)
	}
}

func TestAXModeLogsOutput(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	name := "log-test"
	if err := os.MkdirAll(filepath.Dir(workspace.SerialLogPath(stateDir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.SerialLogPath(stateDir, name), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--mode=ax", "logs", name, "--state-dir", stateDir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run logs: %v", err)
	}
	var got map[string]string
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode logs output %q: %v", data, err)
	}
	if got["workspace"] != name || got["logs"] != "hello\n" {
		t.Fatalf("logs output = %#v", got)
	}
}

func TestStructuredExecRequiresSeparator(t *testing.T) {
	err := run(t.Context(), []string{"exec", "research", "echo hello"}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "usage: microagent exec") {
		t.Fatalf("err = %v, want exec usage", err)
	}
}

func TestStructuredExecUXWritesSeparatedStreamsAndCommandExit(t *testing.T) {
	_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		if strings.Join(req.Argv, " ") != "sh -c echo out; echo err >&2; exit 7" {
			t.Fatalf("argv = %#v", req.Argv)
		}
		code := 7
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stdout = []byte("out\n")
		result.Stderr = []byte("err\n")
		return result
	})
	defer stop()
	stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, port)
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err = runStructuredExec(t.Context(), []string{"research", "--state-dir", stateDir, "--", "sh", "-c", "echo out; echo err >&2; exit 7"}, stdout, &stderr)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	var exitErr cliExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 || !exitErr.Silent {
		t.Fatalf("err = %#v, want silent exit 7", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "out\n" {
		t.Fatalf("stdout = %q", data)
	}
	if stderr.String() != "err\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestStructuredExecBuildsExpectedRequestShape(t *testing.T) {
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	if err := os.WriteFile(stdinPath, []byte("input bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := make(chan execprotocol.ExecRequest, 1)
	_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		seen <- req
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		return result
	})
	defer stop()
	stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, port)
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err = runStructuredExec(t.Context(), []string{
		"research",
		"--state-dir", stateDir,
		"--env", "TEST_VAR=hello",
		"--cwd", "/work",
		"--timeout", "30s",
		"--stdin", stdinPath,
		"--stdout-limit", "1024",
		"--stderr-limit", "2048",
		"--", "cat",
	}, stdout, &stderr)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runStructuredExec: %v", err)
	}
	req := <-seen
	if strings.Join(req.Argv, " ") != "cat" || req.Env["TEST_VAR"] != "hello" || req.Cwd != "/work" || string(req.Stdin) != "input bytes" || req.TimeoutMS != 30000 || req.OutputLimitBytesStdout != 1024 || req.OutputLimitBytesStderr != 2048 {
		t.Fatalf("request = %#v", req)
	}
}

func TestStructuredExecUXTruncationWarningsAndStatusExitCodes(t *testing.T) {
	tests := []struct {
		name string
		res  execprotocol.ExecResult
		code int
		warn string
	}{
		{name: "timeout", res: execprotocol.NewExecResult(execprotocol.ExecStatusTimedOut), code: execTimeoutExitCode},
		{name: "signaled", res: execprotocol.NewExecResult(execprotocol.ExecStatusSignaled), code: execSignaledExitCode},
		{name: "failed to start", res: execprotocol.NewExecResult(execprotocol.ExecStatusFailedToStart), code: execFailedToStartCode},
		{name: "truncated", res: func() execprotocol.ExecResult {
			code := 0
			result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
			result.ExitCode = &code
			result.Stdout = []byte("abc")
			result.Stderr = []byte("def")
			result.StdoutTruncated = true
			result.StderrTruncated = true
			return result
		}(), code: 0, warn: "stdout truncated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
				return tt.res
			})
			defer stop()
			stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, port)
			stdoutPath := filepath.Join(t.TempDir(), "stdout")
			stdout, err := os.Create(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			err = runStructuredExec(t.Context(), []string{"research", "--state-dir", stateDir, "--", "status-probe"}, stdout, &stderr)
			if closeErr := stdout.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if tt.code == 0 && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tt.code != 0 {
				var exitErr cliExitError
				if !errors.As(err, &exitErr) || exitErr.Code != tt.code {
					t.Fatalf("err = %#v, want exit %d", err, tt.code)
				}
			}
			if tt.warn != "" && !strings.Contains(stderr.String(), tt.warn) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.warn)
			}
		})
	}
}

func TestStructuredExecAXWritesResultAndIgnoresCommandExit(t *testing.T) {
	oldMode := globalOutputMode
	t.Cleanup(func() { globalOutputMode = oldMode })
	globalOutputMode = outputModeAX
	_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		code := 7
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stdout = []byte("out\n")
		return result
	})
	defer stop()
	stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, port)
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := runStructuredExec(t.Context(), []string{"research", "--state-dir", stateDir, "--", "sh", "-c", "exit 7"}, stdout, &stderr); err != nil {
		t.Fatalf("runStructuredExec: %v", err)
	}
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope structuredExecAXEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode AX result %q: %v", data, err)
	}
	if !envelope.OK || envelope.Result == nil || envelope.Result.ExitCode == nil || *envelope.Result.ExitCode != 7 || string(envelope.Result.Stdout) != "out\n" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if envelope.RetryCount != 0 || envelope.RetryWallClockMS != 0 || envelope.Metadata.RetryCount != 0 {
		t.Fatalf("retry metadata = %#v", envelope)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestStructuredExecAXServiceErrorWritesStructuredErrorToStdout(t *testing.T) {
	oldMode := globalOutputMode
	t.Cleanup(func() { globalOutputMode = oldMode })
	globalOutputMode = outputModeAX
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err = runStructuredExec(t.Context(), []string{"missing", "--state-dir", t.TempDir(), "--", "true"}, stdout, &stderr)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	var exitErr cliExitError
	if !errors.As(err, &exitErr) || exitErr.Code != execServiceErrorExitCode {
		t.Fatalf("err = %#v, want service exit", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope structuredExecAXEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode AX error %q: %v", data, err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Kind != errorKindNotFound || envelope.Error.Retryable {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestStructuredExecServiceErrorKinds(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) []string
		kind  structuredErrorKind
	}{
		{
			name: "not running",
			setup: func(t *testing.T) []string {
				stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateStopped, 45000)
				return []string{"research", "--state-dir", stateDir, "--", "true"}
			},
			kind: errorKindConflict,
		},
		{
			name: "unreachable",
			setup: func(t *testing.T) []string {
				stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, unusedTCPPort(t))
				return []string{"research", "--state-dir", stateDir, "--", "true"}
			},
			kind: errorKindTransient,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"_ux", func(t *testing.T) {
			oldMode := globalOutputMode
			t.Cleanup(func() { globalOutputMode = oldMode })
			globalOutputMode = outputModeUX
			stdoutPath := filepath.Join(t.TempDir(), "stdout")
			stdout, err := os.Create(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			err = runStructuredExec(t.Context(), tt.setup(t), stdout, &stderr)
			if closeErr := stdout.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err == nil {
				t.Fatal("runStructuredExec err = nil, want service error")
			}
			if got := mapStructuredError(err, "req-test").Kind; got != tt.kind {
				t.Fatalf("kind = %q, want %q for err %v", got, tt.kind, err)
			}
		})
		t.Run(tt.name+"_ax", func(t *testing.T) {
			oldMode := globalOutputMode
			t.Cleanup(func() { globalOutputMode = oldMode })
			globalOutputMode = outputModeAX
			stdoutPath := filepath.Join(t.TempDir(), "stdout")
			stdout, err := os.Create(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			err = runStructuredExec(t.Context(), tt.setup(t), stdout, &stderr)
			if closeErr := stdout.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			var exitErr cliExitError
			if !errors.As(err, &exitErr) || exitErr.Code != execServiceErrorExitCode {
				t.Fatalf("err = %#v, want service exit", err)
			}
			data, err := os.ReadFile(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			var envelope structuredExecAXEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("decode AX error %q: %v", data, err)
			}
			if envelope.Error == nil || envelope.Error.Kind != tt.kind {
				t.Fatalf("kind = %#v, want %q", envelope.Error, tt.kind)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestStructuredErrorMapping(t *testing.T) {
	tests := []struct {
		name                string
		err                 error
		kind                structuredErrorKind
		remediationContains string
	}{
		{name: "unsupported", err: fmt.Errorf("microagent exec is not supported"), kind: errorKindUnsupported},
		{name: "not found", err: os.ErrNotExist, kind: errorKindNotFound},
		{name: "workspace not found", err: workspace.WorkspaceNotFoundError{Name: "missing"}, kind: errorKindNotFound, remediationContains: "workspace.create"},
		{name: "conflict", err: fmt.Errorf("workspace demo is already running"), kind: errorKindConflict},
		{name: "transient", err: fmt.Errorf("connect timeout"), kind: errorKindTransient},
		{name: "console read timeout", err: workspace.ConsoleReadTimeoutError{Workspace: "research", Timeout: time.Second, PartialOutput: "partial\n"}, kind: errorKindTransient},
		{name: "console completion unknown", err: workspace.ConsoleCompletionUnknownError{Workspace: "research", PartialOutput: "partial\n"}, kind: errorKindTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapStructuredError(tt.err, "req-test")
			if got.Kind != tt.kind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.kind)
			}
			if got.CorrelationID != "req-test" {
				t.Fatalf("CorrelationID = %q, want req-test", got.CorrelationID)
			}
			if strings.TrimSpace(got.Remediation) == "" {
				t.Fatalf("Remediation is empty")
			}
			if tt.remediationContains != "" && !strings.Contains(got.Remediation, tt.remediationContains) {
				t.Fatalf("Remediation = %q, want to contain %q", got.Remediation, tt.remediationContains)
			}
			switch typed := tt.err.(type) {
			case workspace.ConsoleReadTimeoutError:
				if got.PartialOutput != typed.PartialOutput {
					t.Fatalf("PartialOutput = %q, want %q", got.PartialOutput, typed.PartialOutput)
				}
			case workspace.ConsoleCompletionUnknownError:
				if got.PartialOutput != typed.PartialOutput {
					t.Fatalf("PartialOutput = %q, want %q", got.PartialOutput, typed.PartialOutput)
				}
			}
		})
	}
}

func TestFirecrackerDoctorDoesNotRequireAppleVFSupervisor(t *testing.T) {
	resp, err := firecrackerDoctorResponse(
		vmkit.BackendFirecracker,
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
	if resp.Backend != vmkit.BackendFirecracker {
		t.Fatalf("Backend = %q, want %q", resp.Backend, vmkit.BackendFirecracker)
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
		vmkit.BackendFirecracker,
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
	if hostBackend() == vmkit.BackendWindowsHyperV {
		wantConsoleMode = "hvsock"
	}
	if !strings.Contains(text, fmt.Sprintf(`"backend": "%s"`, hostBackend())) ||
		!strings.Contains(text, `"kernel"`) ||
		!strings.Contains(text, `"consoleAvailable": true`) ||
		!strings.Contains(text, fmt.Sprintf(`"consoleMode": "%s"`, wantConsoleMode)) {
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
	otherBackend := vmkit.BackendFirecracker
	if hostBackend() == vmkit.BackendFirecracker {
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
	if !stringSliceContains(contract.Backends, vmkit.BackendAppleVF) || !stringSliceContains(contract.Backends, vmkit.BackendFirecracker) {
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

func TestRequestForCommandMapsHumanCommands(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCommand string
	}{
		{
			name:        "doctor",
			args:        []string{"doctor"},
			wantCommand: "host",
		},
		{
			name: "create",
			args: []string{
				"create",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "prepare",
		},
		{
			name: "create dry run",
			args: []string{
				"create",
				"--dry-run",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "check",
		},
		{
			name: "start",
			args: []string{
				"start",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "start",
		},
		{
			name:        "status",
			args:        []string{"status", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "inspect",
		},
		{
			name:        "stop",
			args:        []string{"stop", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "stop",
		},
		{
			name:        "quarantine",
			args:        []string{"quarantine", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "quarantine",
		},
		{
			name:        "pause",
			args:        []string{"pause", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "pause",
		},
		{
			name:        "resume",
			args:        []string{"resume", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "resume",
		},
		{
			name:        "kill",
			args:        []string{"kill", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "kill",
		},
		{
			name:        "delete",
			args:        []string{"delete", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "delete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := requestForCommand(tt.args[0], newFlagSet(tt.args[0]), reorderFlagArgs(tt.args[1:]))
			if err != nil {
				t.Fatalf("requestForCommand: %v", err)
			}
			if req.Command != tt.wantCommand {
				t.Fatalf("Command = %q, want %q", req.Command, tt.wantCommand)
			}
			if tt.args[0] != "doctor" && req.Identity.RuntimeID != "agent-1" {
				t.Fatalf("RuntimeID = %q, want agent-1", req.Identity.RuntimeID)
			}
		})
	}
}

func TestRequestForCommandReadsJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{KernelPath: "/tmp/kernel", RootfsPath: "/tmp/rootfs.ext4", StateDir: "/tmp/state"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := requestForCommand("create", newFlagSet("create"), []string{"-json", path})
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if got.Command != "prepare" {
		t.Fatalf("Command = %q, want prepare", got.Command)
	}
	if got.Identity.RuntimeID != "agent-1" {
		t.Fatalf("RuntimeID = %q, want agent-1", got.Identity.RuntimeID)
	}
}

func TestRequestForCommandParsesVsock(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--vsock", "1024=127.0.0.1:8200",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if len(req.Config.VsockListeners) != 1 {
		t.Fatalf("VsockListeners len = %d, want 1", len(req.Config.VsockListeners))
	}
	listener := req.Config.VsockListeners[0]
	if listener.Port != 1024 || listener.Target != "127.0.0.1:8200" {
		t.Fatalf("listener = %#v", listener)
	}
}

func TestRequestForCommandParsesNetwork(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--backend", hostBackend(),
		"--network", "bridged",
		"--network-interface", "en0",
		"--publish", "127.0.0.1:8080:80/tcp",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if req.Config.Network == nil {
		t.Fatal("network config is nil")
	}
	if req.Config.Network.Mode != "bridged" {
		t.Fatalf("network mode = %q, want bridged", req.Config.Network.Mode)
	}
	if req.Config.Network.Interface != "en0" {
		t.Fatalf("network interface = %q, want en0", req.Config.Network.Interface)
	}
	if len(req.Config.Network.PortForwards) != 1 {
		t.Fatalf("port forwards len = %d, want 1", len(req.Config.Network.PortForwards))
	}
	forward := req.Config.Network.PortForwards[0]
	if forward.Host != "127.0.0.1" || forward.HostPort != 8080 || forward.GuestPort != 80 || forward.Protocol != "tcp" {
		t.Fatalf("forward = %#v", forward)
	}
}

func TestRequestForCommandRejectsIsolatedPublish(t *testing.T) {
	_, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--network", "isolated",
		"--publish", "127.0.0.1:8080:80/tcp",
	}))
	if err == nil {
		t.Fatal("requestForCommand accepted isolated publish")
	}
}

func TestRequestForCommandAcceptsAppleVFPublish(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--backend", vmkit.BackendAppleVF,
		"--publish", "127.0.0.1:8080:80/tcp",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if req.Config == nil || req.Config.Network == nil || len(req.Config.Network.PortForwards) != 1 {
		t.Fatalf("network = %#v", req.Config.Network)
	}
}

func TestRequestForCommandRejectsUnsupportedPortForwardProtocol(t *testing.T) {
	_, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--publish", "127.0.0.1:8080:80/udp",
	}))
	if err == nil {
		t.Fatal("requestForCommand accepted udp port forward")
	}
}

func TestRequestForCommandParsesDisk(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--disk", "constraints=/tmp/constraints.ext4:/config:ro",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if len(req.Config.Disks) != 1 {
		t.Fatalf("Disks len = %d, want 1", len(req.Config.Disks))
	}
	disk := req.Config.Disks[0]
	if disk.Name != "constraints" || disk.Path != "/tmp/constraints.ext4" || disk.Mountpoint != "/config" || disk.Mode != "ro" {
		t.Fatalf("disk = %#v", disk)
	}
}

func TestWorkspaceRequestIncludesVsockMappings(t *testing.T) {
	mediation := vmkit.MediationConfig{
		Enabled:    true,
		Required:   true,
		Port:       2048,
		Target:     "127.0.0.1:9900",
		FailClosed: true,
	}
	req := workspaceRequest(workspaceOptions{
		Name:           "agent-1",
		Backend:        "apple-vf",
		KernelPath:     "/tmp/kernel",
		MemoryMiB:      512,
		CPUCount:       2,
		ResultPort:     1024,
		VsockListeners: []vmkit.VsockListener{{Port: 3128, Target: "127.0.0.1:19000"}},
		Mediation:      &mediation,
	}, "run", "/tmp/rootfs.ext4")
	if len(req.Config.VsockListeners) != 3 {
		t.Fatalf("VsockListeners len = %d, want 3", len(req.Config.VsockListeners))
	}
	if req.Config.VsockListeners[1].Port != 3128 || req.Config.VsockListeners[1].Target != "127.0.0.1:19000" {
		t.Fatalf("enforcer listener = %#v", req.Config.VsockListeners[1])
	}
	if req.Config.VsockListeners[2].Port != 2048 || req.Config.VsockListeners[2].Target != "127.0.0.1:9900" {
		t.Fatalf("mediation listener = %#v", req.Config.VsockListeners[2])
	}
	if req.Config.Mediation == nil || !req.Config.Mediation.Required || !req.Config.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", req.Config.Mediation)
	}
}

func TestWorkspaceRequestIncludesDisks(t *testing.T) {
	req := workspaceRequest(workspaceOptions{
		Name:       "agent-1",
		Backend:    "apple-vf",
		KernelPath: "/tmp/kernel",
		MemoryMiB:  512,
		CPUCount:   2,
		Disks: []workspaceDisk{{
			Name:       "workspace",
			Path:       "/tmp/workspace.ext4",
			Mountpoint: "/workspace",
			Mode:       "rw",
		}},
	}, "run", "/tmp/rootfs.ext4")
	if len(req.Config.Disks) != 1 {
		t.Fatalf("Disks len = %d, want 1", len(req.Config.Disks))
	}
	if req.Config.Disks[0].Mountpoint != "/workspace" || req.Config.Disks[0].Mode != "rw" {
		t.Fatalf("disk = %#v", req.Config.Disks[0])
	}
}

func TestRunUsesSupervisorOverride(t *testing.T) {
	dir := t.TempDir()
	supervisor := filepath.Join(dir, "supervisor")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); print(json.dumps({"ok": True, "backend": %q, "event": {"identity": req["identity"], "state": "prepared", "observedAt": "2026-05-02T00:00:00Z"}}))'
`, hostBackend())
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"create",
		"--supervisor", supervisor,
		"--backend", hostBackend(),
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) {
		t.Fatalf("stdout missing prepared state: %s", data)
	}
}

func TestRunStatusUsesWorkspaceStateDefaults(t *testing.T) {
	dir := t.TempDir()
	req := vmkit.Request{
		Command: "inspect",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendAppleVF,
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research", Backend: vmkit.BackendAppleVF}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatalf("writeWorkspaceProcessState: %v", err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", RestartPolicy: "always", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"status",
		"--state-dir", dir,
		"--name", "research",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "running"`) || !strings.Contains(string(data), `"restartPolicy": "always"`) {
		t.Fatalf("status output = %s", data)
	}
}

func TestWriteWorkspaceProcessStateAppendsEventHistory(t *testing.T) {
	dir := t.TempDir()
	req := vmkit.Request{
		Command: "inspect",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendAppleVF,
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	opts := workspaceOptions{StateDir: dir, Name: "research", Backend: vmkit.BackendAppleVF}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StatePrepared, 0, ""); err != nil {
		t.Fatalf("write prepared state: %v", err)
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateHalted, 0, ""); err != nil {
		t.Fatalf("write halted state: %v", err)
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateQuarantined, 0, ""); err != nil {
		t.Fatalf("write quarantined state: %v", err)
	}
	var events []workspaceEventFile
	data, err := os.ReadFile(filepath.Join(dir, "research", "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].State != vmkit.StatePrepared || events[1].State != vmkit.StateHalted || events[2].State != vmkit.StateQuarantined {
		t.Fatalf("events = %#v, want prepared, halted, then quarantined", events)
	}
}

func TestStatusReportsRecordedVerificationForPreparedWorkspace(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	initPath := filepath.Join(dir, "microagent-init")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("kernel-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initPath, []byte("init-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Backend:       hostBackend(),
		KernelPath:    kernelPath,
		GuestInitPath: initPath,
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}
	result := workspaceResult{
		Workspace:  "research",
		RootfsPath: rootfsPath,
		Image: rootfs.Provenance{
			ImageRef:    "docker.io/library/busybox:1.36",
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
		},
	}
	verification, err := buildWorkspaceVerification(opts, result)
	if err != nil {
		t.Fatal(err)
	}
	opts.Verification = &verification
	if err := writeWorkspaceManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: kernelPath,
			RootfsPath: rootfsPath,
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StatePrepared, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Verification == nil || !resp.Verification.OK {
		t.Fatalf("verification = %#v, want recorded verification without divergence", resp.Verification)
	}
	if len(resp.Verification.Divergence) != 0 {
		t.Fatalf("divergence = %#v, want none for prepared status fast path", resp.Verification.Divergence)
	}
	if resp.Verification.ImageDigest != "sha256:abc" || resp.Verification.Kernel.SHA256 == "" || resp.Verification.Rootfs.RecordedSHA256 == "" {
		t.Fatalf("verification details missing: %#v", resp.Verification)
	}
}

func TestStatusReportsReadinessSignals(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	serialInput := serialInputPath(dir, "research")
	if err := os.MkdirAll(filepath.Dir(serialInput), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInput, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath(workspaceOptions{StateDir: dir, Name: "research"}), []byte(`{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Readiness == nil {
		t.Fatal("readiness missing")
	}
	if !resp.Readiness.GuestReady.Ready || !resp.Readiness.ShellReady.Ready || !resp.Readiness.ResultReady.Ready {
		t.Fatalf("readiness = %#v, want all ready", resp.Readiness)
	}
	if resp.Result == nil || resp.Result.ExitCode != 0 || resp.Result.CompletedAt != "2026-05-02T00:00:01Z" {
		t.Fatalf("result = %#v, want structured result", resp.Result)
	}
}

func TestInspectAliasDefaultsToJSONStatus(t *testing.T) {
	dir := t.TempDir()
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "inspect.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"inspect", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run inspect: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"runtimeID": "research"`) || !strings.Contains(string(data), `"state": "stopped"`) {
		t.Fatalf("inspect output = %s", data)
	}
}

func TestRunResultReportsStructuredResult(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	resultJSON := `{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":7,"stdout":"done\n","stderr":"warn\n"}`
	if err := os.WriteFile(resultPath(workspaceOptions{StateDir: dir, Name: "research"}), []byte(resultJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "result.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"result", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run result: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result == nil {
		t.Fatal("result missing")
	}
	if resp.Result.Identity.RuntimeID != "research" || resp.Result.ExitCode != 7 || resp.Result.Stdout != "done\n" || resp.Result.Stderr != "warn\n" {
		t.Fatalf("result = %#v", resp.Result)
	}
	if resp.Result.ResultPath == "" || resp.Result.Backend != hostBackend() {
		t.Fatalf("result metadata = %#v", resp.Result)
	}
}

func TestRunDeleteRemovesSavedWorkspaceState(t *testing.T) {
	if hostBackend() == vmkit.BackendWindowsHyperV {
		t.Skip("windows-hyperv delete uses in-process HCS state, not executable supervisor fixtures")
	}
	dir := t.TempDir()
	supervisor := filepath.Join(dir, "supervisor")
	backend := hostBackend()
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); assert req["command"] == "delete"; print(json.dumps({"ok": True, "backend": "` + backend + `", "event": {"identity": req["identity"], "state": "stopped", "detail": "deleted", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"delete",
		"--backend", backend,
		"--supervisor", supervisor,
		"--state-dir", dir,
		"--yes",
		"research",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspaces", "research")); !os.IsNotExist(err) {
		t.Fatalf("workspace root still exists after delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "research")); !os.IsNotExist(err) {
		t.Fatalf("runtime state still exists after delete: %v", err)
	}
}

func TestDeleteRequiresConfirmationWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	oldTerminal := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = oldTerminal })
	stdinIsTerminal = func() bool { return false }
	_, err := runDeleteWorkspace(t.Context(), workspaceOptions{StateDir: dir, Name: "research", Backend: vmkit.BackendAppleVF}, false, false)
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("err = %v, want --yes confirmation error", err)
	}
}

func TestDeleteCancelsWhenConfirmationDeclines(t *testing.T) {
	dir := t.TempDir()
	oldTerminal := stdinIsTerminal
	oldConfirm := readConfirmation
	t.Cleanup(func() {
		stdinIsTerminal = oldTerminal
		readConfirmation = oldConfirm
	})
	stdinIsTerminal = func() bool { return true }
	readConfirmation = func(string) (bool, error) { return false, nil }
	_, err := runDeleteWorkspace(t.Context(), workspaceOptions{StateDir: dir, Name: "research", Backend: vmkit.BackendAppleVF}, false, false)
	if err == nil || !strings.Contains(err.Error(), "delete cancelled") {
		t.Fatalf("err = %v, want cancellation", err)
	}
}

func TestRunRootFSValidatesRequiredFlags(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runRootFS(t.Context(), []string{"build"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "image_ref is required") {
		t.Fatalf("err = %v, want image_ref validation", err)
	}
}

func TestRootFSExecMapsToShellCommand(t *testing.T) {
	var req rootfs.BuildRequest
	execCommand := "echo hello"
	if strings.TrimSpace(execCommand) != "" {
		req.Command = []string{"/bin/sh", "-lc", execCommand}
	}
	if got := strings.Join(req.Command, " "); got != "/bin/sh -lc echo hello" {
		t.Fatalf("Command = %q", got)
	}
}

func TestParseWorkspaceOptionsForRun(t *testing.T) {
	dir := t.TempDir()
	setupPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(setupPath, []byte("#!/bin/sh\necho from-file\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("run", []string{
		"--image", "docker.io/library/ubuntu:24.04",
		"--exec", "uname -a",
		"--setup", "apt-get update",
		"--setup", "apt-get install -y git",
		"--setup-file", setupPath,
		"--entrypoint", "/app/entrypoint.sh",
		"--shell", "/bin/bash",
		"--hostname", "research-vm",
		"--env", "AGENCY_AGENT_NAME=research",
		"--env", "AGENCY_MODEL=standard",
		"--name", "research",
		"--kernel", "/tmp/kernel",
		"--state-dir", "/tmp/microagent-state",
		"--mke2fs", "/tmp/mke2fs",
		"--arch", "arm64",
		"--memory", "1024",
		"--cpus", "4",
		"--size-mib", "2048",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.ExecCommand != "uname -a" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if len(opts.SetupCommands) != 3 || opts.SetupCommands[0] != "apt-get update" || opts.SetupCommands[1] != "apt-get install -y git" || !strings.Contains(opts.SetupCommands[2], "echo from-file") {
		t.Fatalf("SetupCommands = %#v", opts.SetupCommands)
	}
	if opts.Entrypoint != "/app/entrypoint.sh" {
		t.Fatalf("Entrypoint = %q", opts.Entrypoint)
	}
	if opts.ConsoleShell != "/bin/bash" {
		t.Fatalf("ConsoleShell = %q", opts.ConsoleShell)
	}
	if opts.Hostname != "research-vm" {
		t.Fatalf("Hostname = %q", opts.Hostname)
	}
	if opts.Env["AGENCY_AGENT_NAME"] != "research" || opts.Env["AGENCY_MODEL"] != "standard" {
		t.Fatalf("Env = %#v", opts.Env)
	}
	if opts.Name != "research" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if opts.KernelPath != "/tmp/kernel" {
		t.Fatalf("KernelPath = %q", opts.KernelPath)
	}
	if opts.MemoryMiB != 1024 || opts.CPUCount != 4 || opts.SizeMiB != 2048 {
		t.Fatalf("resource opts = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsModelFlagAndSpecPrecedence(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "microagent.yaml")
	if err := os.WriteFile(specPath, []byte("name: demo\nimage: docker.io/library/ubuntu:24.04\nmodel: org/spec-repo/spec.gguf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := parseWorkspaceOptions("create", []string{"--file", specPath})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Model != "org/spec-repo/spec.gguf" {
		t.Fatalf("spec model not applied: %q", opts.Model)
	}

	opts, err = parseWorkspaceOptions("create", []string{"--file", specPath, "--model", "org/flag-repo/flag.gguf"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Model != "org/flag-repo/flag.gguf" {
		t.Fatalf("--model flag should win over spec: %q", opts.Model)
	}

	opts, err = parseWorkspaceOptions("create", []string{"demo", "--model", "org/flag-repo/flag.gguf"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Model != "org/flag-repo/flag.gguf" {
		t.Fatalf("create --model not parsed: %q", opts.Model)
	}
}

func TestEnsureModelPairingNoModelIsNoOp(t *testing.T) {
	opts := workspaceOptions{Name: "ws", StateDir: t.TempDir()}
	release, err := ensureModelPairing(context.Background(), &opts, "", "")
	if err != nil {
		t.Fatalf("ensureModelPairing: %v", err)
	}
	if release == nil {
		t.Fatal("no-op pairing must return a non-nil release func")
	}
	release()
	if opts.Model != "" || opts.ModelTarget != "" || opts.Env != nil {
		t.Fatalf("opts mutated without a model: model=%q target=%q env=%#v", opts.Model, opts.ModelTarget, opts.Env)
	}
}

func TestEnsureModelPairingRejectsInvalidRef(t *testing.T) {
	opts := workspaceOptions{Name: "ws", StateDir: t.TempDir()}
	if _, err := ensureModelPairing(context.Background(), &opts, "not-a-ref", ""); err == nil {
		t.Fatal("ensureModelPairing accepted an invalid model ref")
	}
}

func TestPendingModelRelease(t *testing.T) {
	dir := t.TempDir()
	// Missing manifest must yield a silent no-op.
	pendingModelRelease(dir, "ghost")()

	opts := workspace.DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = dir
	opts.Model = "hf.co/org/repo@main/m.gguf"
	if err := workspace.WriteManifest(opts); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	idx := modelrunner.Index{Runners: []modelrunner.Record{{
		Key:      "hf.co/org/repo@main/m.gguf",
		ModelRef: "hf.co/org/repo@main/m.gguf",
		PID:      99999999, // dead PID: release stops it best-effort
		Holders:  []string{"ws"},
	}}}
	if err := modelrunner.WriteIndex(dir, idx); err != nil {
		t.Fatalf("write index: %v", err)
	}
	// The ref is captured at call time: removing the manifest afterwards (as
	// delete does) must not stop the release.
	release := pendingModelRelease(dir, "ws")
	if err := os.RemoveAll(filepath.Join(dir, "workspaces", "ws")); err != nil {
		t.Fatal(err)
	}
	release()
	after, err := modelrunner.ReadIndex(dir)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(after.Runners) != 0 {
		t.Fatalf("runner not released: %+v", after.Runners)
	}
}

func TestParseWorkspaceOptionsForCreateDefaultsImageAndPositionalName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--arch", "amd64",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "research" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if opts.ImageRef != defaultWorkspaceImageAMD64 {
		t.Fatalf("ImageRef = %q, want %q", opts.ImageRef, defaultWorkspaceImageAMD64)
	}
	if opts.Hostname != "research" {
		t.Fatalf("Hostname = %q", opts.Hostname)
	}
	if opts.MemoryMiB != defaultWorkspaceMemoryMiB || opts.CPUCount != 2 || opts.SizeMiB != rootfs.DefaultSizeMiB {
		t.Fatalf("defaults = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsAppliesResourceProfile(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--profile", "medium",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Profile != "medium" || opts.MemoryMiB != 2048 || opts.CPUCount != 2 || opts.SizeMiB != 8192 {
		t.Fatalf("profile resources = profile %q memory %d cpus %d size %d", opts.Profile, opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidConsoleShell(t *testing.T) {
	for _, shellPath := range []string{"bash", "/bin/../bin/bash"} {
		_, err := parseWorkspaceOptions("create", []string{
			"research",
			"--image", "docker.io/library/ubuntu:24.04",
			"--shell", shellPath,
		})
		if err == nil {
			t.Fatalf("parseWorkspaceOptions accepted shell %q", shellPath)
		}
	}
}

func TestParseWorkspaceOptionsRejectsInvalidHostname(t *testing.T) {
	for _, hostname := range []string{"bad_name", "-bad", strings.Repeat("a", 64)} {
		_, err := parseWorkspaceOptions("create", []string{
			"research",
			"--image", "docker.io/library/ubuntu:24.04",
			"--hostname", hostname,
		})
		if err == nil {
			t.Fatalf("parseWorkspaceOptions accepted hostname %q", hostname)
		}
	}
}

func TestParseWorkspaceOptionsLetsExplicitResourcesOverrideProfile(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--profile", "large",
		"--memory", "3072",
		"--cpus", "3",
		"--size-mib", "12288",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Profile != "large" || opts.MemoryMiB != 3072 || opts.CPUCount != 3 || opts.SizeMiB != 12288 {
		t.Fatalf("resources = profile %q memory %d cpus %d size %d", opts.Profile, opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsAcceptsRestartPolicy(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--restart", "on-failure",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.RestartPolicy != "on-failure" {
		t.Fatalf("RestartPolicy = %q", opts.RestartPolicy)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidRestartPolicy(t *testing.T) {
	_, err := parseWorkspaceOptions("create", []string{"research", "--restart", "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "restart policy") {
		t.Fatalf("err = %v, want restart validation", err)
	}
}

func TestShouldRestartWorkspace(t *testing.T) {
	tests := []struct {
		policy string
		state  vmkit.VMState
		want   bool
	}{
		{policy: "never", state: vmkit.StateFailed, want: false},
		{policy: "on-failure", state: vmkit.StateFailed, want: true},
		{policy: "on-failure", state: vmkit.StateStopped, want: false},
		{policy: "always", state: vmkit.StateStopped, want: true},
		{policy: "always", state: vmkit.StateFailed, want: true},
		{policy: "always", state: vmkit.StateRunning, want: false},
	}
	for _, tt := range tests {
		if got := shouldRestartWorkspace(tt.policy, tt.state); got != tt.want {
			t.Fatalf("shouldRestartWorkspace(%q, %q) = %v, want %v", tt.policy, tt.state, got, tt.want)
		}
	}
}

func TestParseWorkspaceOptionsRejectsUnknownProfile(t *testing.T) {
	_, err := parseWorkspaceOptions("create", []string{"research", "--profile", "huge"})
	if err == nil || !strings.Contains(err.Error(), "unknown resource profile") {
		t.Fatalf("err = %v, want unknown profile", err)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidResources(t *testing.T) {
	_, err := parseWorkspaceOptions("create", []string{"research", "--memory", "0"})
	if err == nil || !strings.Contains(err.Error(), "memory must be positive") {
		t.Fatalf("err = %v, want memory validation", err)
	}
	_, err = parseWorkspaceOptions("create", []string{"research", "--size-mib", "0"})
	if err == nil || !strings.Contains(err.Error(), "size-mib must be positive") {
		t.Fatalf("err = %v, want size validation", err)
	}
}

func TestParseWorkspaceOptionsAcceptsDiskAndBundle(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--disk", "workspace=/tmp/workspace.ext4:/workspace:rw",
		"--bundle", "constraints=/tmp/constraints.tar:/config:ro",
		"--output", "report=/workspace/report.json",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.Disks) != 2 {
		t.Fatalf("Disks len = %d, want 2", len(opts.Disks))
	}
	if opts.Disks[0].Name != "workspace" || opts.Disks[0].Bundle {
		t.Fatalf("disk = %#v", opts.Disks[0])
	}
	if opts.Disks[1].Name != "constraints" || !opts.Disks[1].Bundle || opts.Disks[1].Mode != "ro" {
		t.Fatalf("bundle = %#v", opts.Disks[1])
	}
	if len(opts.Outputs) != 1 || opts.Outputs[0].Name != "report" || opts.Outputs[0].Path != "/workspace/report.json" {
		t.Fatalf("outputs = %#v", opts.Outputs)
	}
}

func TestParseWorkspaceOptionsAcceptsSafeContainerStyleVolumes(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"-v", "/tmp/config.tar:/config:ro",
		"--volume", "/tmp/workspace.ext4:/workspace",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.Disks) != 2 {
		t.Fatalf("Disks len = %d, want 2", len(opts.Disks))
	}
	if opts.Disks[0].Name != "config" || opts.Disks[0].Path != "/tmp/config.tar" || opts.Disks[0].Mountpoint != "/config" || opts.Disks[0].Mode != "ro" || !opts.Disks[0].Bundle {
		t.Fatalf("bundle volume = %#v", opts.Disks[0])
	}
	if opts.Disks[1].Name != "workspace" || opts.Disks[1].Path != "/tmp/workspace.ext4" || opts.Disks[1].Mountpoint != "/workspace" || opts.Disks[1].Mode != "rw" || opts.Disks[1].Bundle {
		t.Fatalf("disk volume = %#v", opts.Disks[1])
	}
}

func TestParseWorkspaceOptionsRejectsHostBindMountVolume(t *testing.T) {
	dir := t.TempDir()
	_, err := parseWorkspaceOptions("create", []string{
		"research",
		"-v", dir + ":/workspace:rw",
	})
	if err == nil || !strings.Contains(err.Error(), "does not expose host bind mounts") {
		t.Fatalf("err = %v, want host bind mount rejection", err)
	}
}

func TestParseWorkspaceOptionsAcceptsManagedVolumeByName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--volume", "cache:/cache:rw",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(opts.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(opts.Disks))
	}
	d := opts.Disks[0]
	if !d.ManagedVolume || d.Name != "cache" || d.Mountpoint != "/cache" || d.Mode != "rw" {
		t.Fatalf("unexpected managed volume disk: %+v", d)
	}
}

func TestParseWorkspaceOptionsRejectsUnsupportedVolumeSource(t *testing.T) {
	_, err := parseWorkspaceOptions("create", []string{
		"research",
		"--volume", "./data.bin:/data:rw",
	})
	if err == nil || !strings.Contains(err.Error(), "not host bind mounts") {
		t.Fatalf("err = %v, want unsupported volume rejection", err)
	}
}

func TestParseWorkspaceOptionsNetworkNameImpliesNamedMode(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--network-name", "devnet",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Network.Mode != "named" || opts.Network.Name != "devnet" {
		t.Fatalf("expected named mode for devnet, got mode=%q name=%q", opts.Network.Mode, opts.Network.Name)
	}
}

func TestParseWorkspaceOptionsNetworkNameConflictsWithMode(t *testing.T) {
	_, err := parseWorkspaceOptions("create", []string{
		"research",
		"--network", "nat",
		"--network-name", "devnet",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v, want named/mode conflict", err)
	}
}

func TestParseWorkspaceOptionsRejectsUnsupportedContainerCompatibilityFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "privileged",
			args: []string{"--privileged", "docker.io/library/busybox:1.36", "true"},
			want: "--privileged is not supported",
		},
		{
			name: "pod",
			args: []string{"--pod", "new:demo", "docker.io/library/busybox:1.36", "true"},
			want: "does not implement pods",
		},
		{
			name: "bind mount",
			args: []string{"--mount", "type=bind,source=/tmp,target=/workspace", "docker.io/library/busybox:1.36", "true"},
			want: "does not expose host bind mounts",
		},
		{
			name: "capability",
			args: []string{"--cap-add", "NET_ADMIN", "docker.io/library/busybox:1.36", "true"},
			want: "microVM boundary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseWorkspaceOptions("run", tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseWorkspaceOptionsAcceptsMediation(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--mediation", "2048=127.0.0.1:9900",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Mediation == nil || !opts.Mediation.Enabled || !opts.Mediation.Required || !opts.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", opts.Mediation)
	}
	if opts.Mediation.Port != 2048 || opts.Mediation.Target != "127.0.0.1:9900" {
		t.Fatalf("mediation endpoint = %#v", opts.Mediation)
	}
}

func TestParseWorkspaceOptionsAcceptsOptionalMediation(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--mediation", "2048=127.0.0.1:9900",
		"--mediation-optional",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Mediation == nil || !opts.Mediation.Enabled || opts.Mediation.Required || opts.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", opts.Mediation)
	}
}

func TestParseWorkspaceOptionsRejectsOptionalMediationWithoutMapping(t *testing.T) {
	_, err := parseWorkspaceOptions("create", []string{"research", "--mediation-optional"})
	if err == nil || !strings.Contains(err.Error(), "requires --mediation") {
		t.Fatalf("err = %v, want mediation mapping error", err)
	}
}

func TestParseWorkspaceOptionsReadsSpecFile(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "microagent.yaml")
	spec := `
name: research
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
entrypoint: /app/start.sh
shell: /bin/bash
hostname: research-vm
setup:
  - mkdir -p /workspace
  - file: ./setup.sh
  - run: echo ready > /workspace/status
env:
  MICROAGENT_NAME: research
resources:
  memoryMiB: 3072
  cpuCount: 3
  sizeMiB: 12288
network:
  mode: nat
  forwards:
    - host: 127.0.0.1
      hostPort: 8080
      guestPort: 80
      protocol: tcp
  dns:
    - 1.1.1.1
  routes:
    - 0.0.0.0/0
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:9900
  failClosed: true
disks:
  - name: workspace
    path: /tmp/workspace.ext4
    mountpoint: /workspace
    mode: rw
bundles:
  - name: config
    path: /tmp/config.tar
    mountpoint: /config
    mode: ro
outputs:
  - name: report
    path: /workspace/report.json
files:
  - src: ./agent.py
    dst: /app/agent.py
    mode: "0755"
`
	if err := os.WriteFile(filepath.Join(dir, "agent.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte("#!/bin/sh\napt-get update\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("create", []string{"--file", specPath, "--backend", hostBackend()})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "research" || opts.ImageRef != "docker.io/library/ubuntu:24.04" || opts.Profile != "medium" || opts.RestartPolicy != "on-failure" {
		t.Fatalf("identity/image/profile = %#v", opts)
	}
	if opts.Entrypoint != "/app/start.sh" || opts.ConsoleShell != "/bin/bash" || opts.Hostname != "research-vm" || len(opts.SetupCommands) != 3 {
		t.Fatalf("commands = entrypoint %q shell %q hostname %q setup %#v", opts.Entrypoint, opts.ConsoleShell, opts.Hostname, opts.SetupCommands)
	}
	if !strings.Contains(opts.SetupCommands[1], "apt-get update") {
		t.Fatalf("setup file command = %q", opts.SetupCommands[1])
	}
	if opts.Env["MICROAGENT_NAME"] != "research" {
		t.Fatalf("env = %#v", opts.Env)
	}
	if opts.MemoryMiB != 3072 || opts.CPUCount != 3 || opts.SizeMiB != 12288 {
		t.Fatalf("resources = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
	if opts.Network.Mode != "nat" || len(opts.Network.PortForwards) != 1 || opts.Network.PortForwards[0].HostPort != 8080 || len(opts.Network.DNS) != 1 {
		t.Fatalf("network = %#v", opts.Network)
	}
	if opts.Mediation == nil || !opts.Mediation.Required || opts.Mediation.Port != 2048 || opts.Mediation.Target != "127.0.0.1:9900" {
		t.Fatalf("mediation = %#v", opts.Mediation)
	}
	if len(opts.Disks) != 2 || opts.Disks[0].Name != "workspace" || opts.Disks[1].Name != "config" || !opts.Disks[1].Bundle {
		t.Fatalf("disks = %#v", opts.Disks)
	}
	if len(opts.Outputs) != 1 || opts.Outputs[0].Name != "report" || opts.Outputs[0].Path != "/workspace/report.json" {
		t.Fatalf("outputs = %#v", opts.Outputs)
	}
	if len(opts.Files) != 1 || opts.Files[0].SourcePath != filepath.Join(dir, "agent.py") || opts.Files[0].Path != "/app/agent.py" || opts.Files[0].Mode != "0755" {
		t.Fatalf("files = %#v", opts.Files)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidSpecFiles(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "agent.py")
	if err := os.WriteFile(srcPath, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		spec string
		want string
	}{
		{
			name: "missing source",
			spec: "name: bad\nfiles:\n  - src: ./missing.py\n    dst: /app/agent.py\n",
			want: "file src",
		},
		{
			name: "relative dst",
			spec: "name: bad\nfiles:\n  - src: ./agent.py\n    dst: app/agent.py\n",
			want: "file dst must be absolute",
		},
		{
			name: "duplicate dst",
			spec: "name: bad\nfiles:\n  - src: ./agent.py\n    dst: /app/agent.py\n  - src: ./agent.py\n    dst: /app/agent.py\n",
			want: "duplicate file dst",
		},
		{
			name: "missing setup file",
			spec: "name: bad\nsetup:\n  - file: ./missing.sh\n",
			want: "setup file",
		},
		{
			name: "ambiguous setup entry",
			spec: "name: bad\nsetup:\n  - run: echo ok\n    file: ./agent.py\n",
			want: "cannot use both run and file",
		},
		{
			name: "misnested network",
			spec: "name: bad\nresources:\n  memoryMiB: 1024\n  network:\n    mode: user\n",
			want: `unknown field "network" under resources; move network to the top level`,
		},
		{
			name: "unknown top-level field",
			spec: "name: bad\nnetwrok:\n  mode: user\n", //nolint:misspell // deliberate typo: exercises unknown-field rejection
			want: `unknown top-level field "netwrok"`,   //nolint:misspell // deliberate typo: exercises unknown-field rejection
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specPath := filepath.Join(dir, tt.name+".yaml")
			if err := os.WriteFile(specPath, []byte(tt.spec), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := parseWorkspaceOptions("create", []string{"--file", specPath})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseWorkspaceOptionsFlagsOverrideSpecFile(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "microagent.yaml")
	spec := `
name: from-spec
image: docker.io/library/busybox:1.36
profile: large
env:
  MODE: spec
resources:
  memoryMiB: 4096
`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("create", []string{
		"--file", specPath,
		"--name", "from-flag",
		"--image", "docker.io/library/ubuntu:24.04",
		"--profile", "small",
		"--memory", "1536",
		"--env", "MODE=flag",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "from-flag" || opts.ImageRef != "docker.io/library/ubuntu:24.04" || opts.Profile != "small" {
		t.Fatalf("overrides = name %q image %q profile %q", opts.Name, opts.ImageRef, opts.Profile)
	}
	if opts.MemoryMiB != 1536 || opts.CPUCount != 2 || opts.SizeMiB != rootfs.DefaultSizeMiB {
		t.Fatalf("resources = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
	if opts.Env["MODE"] != "flag" {
		t.Fatalf("env = %#v", opts.Env)
	}
}

func TestParseWorkspaceOptionsFindsDefaultSpecFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.WriteFile(filepath.Join(dir, "microagent.yaml"), []byte("name: default-spec\nprofile: tiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("create", nil)
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "default-spec" || opts.Profile != "tiny" || opts.MemoryMiB != 256 {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestRunProfilesPrintsExactConfigs(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "profiles.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "profiles"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run profiles: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"name": "medium"`) ||
		!strings.Contains(text, `"memory_mib": 2048`) ||
		!strings.Contains(text, `"size_mib": 8192`) {
		t.Fatalf("profiles output = %s", data)
	}
}

func TestPerfBootRejectsInvalidIterations(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "perf.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"boot", "--iterations", "0"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "iterations must be positive") {
		t.Fatalf("runPerf err = %v", err)
	}
}

func TestSummarizePerfIterations(t *testing.T) {
	summary := summarizePerfIterations([]perfIteration{
		{Name: "one", OK: true, DurationMs: 30},
		{Name: "two", OK: true, DurationMs: 10},
		{Name: "three", OK: true, DurationMs: 20},
	})
	if summary.Count != 3 || summary.MinMs != 10 || summary.AvgMs != 20 || summary.MaxMs != 30 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestParseRSSKiB(t *testing.T) {
	rss, err := parseRSSKiB([]byte("  12345\n"))
	if err != nil {
		t.Fatalf("parseRSSKiB: %v", err)
	}
	if rss != 12345 {
		t.Fatalf("rss = %d", rss)
	}
	if _, err := parseRSSKiB([]byte("")); err == nil {
		t.Fatal("parseRSSKiB accepted empty ps output")
	}
}

func TestRunPerfFootprintRequiresRunningPID(t *testing.T) {
	dir := t.TempDir()
	testFirecrackerRuntimeState(t, dir, "research", vmkit.StateStopped, 0)
	stdoutPath := filepath.Join(dir, "footprint.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerfFootprint([]string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "does not have a running process pid") {
		t.Fatalf("runPerfFootprint err = %v", err)
	}
}

func TestSummarizeRSSSamples(t *testing.T) {
	summary := summarizeRSSSamples([]perfRSSSample{
		{RSSKiB: 40},
		{RSSKiB: 20},
		{RSSKiB: 30},
	})
	if summary.Count != 3 || summary.MinKiB != 20 || summary.AvgKiB != 30 || summary.MaxKiB != 40 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRunPerfSteadyRejectsInvalidSampling(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "steady.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"steady", "research", "--duration", "1", "--interval", "2", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "interval must be less than or equal to duration") {
		t.Fatalf("runPerf err = %v", err)
	}
}

func TestImagesListAndPruneUseLocalIndex(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordImageProvenance(dir, rootfs.Provenance{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "images.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runImages([]string{"list", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runImages list: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"digest": "sha256:abc"`) {
		t.Fatalf("images output = %s", data)
	}
	if err := os.Remove(rootfsPath); err != nil {
		t.Fatal(err)
	}
	pruned, err := pruneImageRecords(dir, false)
	if err != nil {
		t.Fatalf("pruneImageRecords: %v", err)
	}
	if len(pruned.Removed) != 1 || len(pruned.Kept) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
}

func TestImagesPruneDeleteRemovesReusableBaselines(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := upsertImageRecord(dir, imageRecord{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := pruneImageRecords(dir, true)
	if err != nil {
		t.Fatalf("pruneImageRecords: %v", err)
	}
	if len(pruned.Deleted) != 2 || len(pruned.Kept) != 0 || len(pruned.Removed) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("rootfs still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRunImagesPruneDeleteRequiresConfirmationWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	oldTerminal := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = oldTerminal })
	stdinIsTerminal = func() bool { return false }
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runImages([]string{"prune", "--delete", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("err = %v, want --yes confirmation error", err)
	}
}

func TestRunPruneImagesDeletesReusableBaselinesWithYes(t *testing.T) {
	oldOutput := outputFormat
	t.Cleanup(func() { outputFormat = oldOutput })
	outputFormat = "text"
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upsertImageRecord(dir, imageRecord{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "prune.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPrune([]string{"--images", "--yes", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPrune: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Deleted: 1") {
		t.Fatalf("prune output = %s", data)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("rootfs still exists or stat failed unexpectedly: %v", err)
	}
}

func TestImagesPruneDeleteKeepsWorkspaceRootfs(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upsertImageRecord(dir, imageRecord{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	pruned, err := pruneImageRecords(dir, true)
	if err != nil {
		t.Fatalf("pruneImageRecords: %v", err)
	}
	if len(pruned.Kept) != 1 || len(pruned.Deleted) != 0 || len(pruned.Removed) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
	if _, err := os.Stat(rootfsPath); err != nil {
		t.Fatalf("workspace rootfs was removed: %v", err)
	}
}

func TestImagesTagCreatesAlias(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upsertImageRecord(dir, imageRecord{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  "2026-05-06T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	tagged, err := tagImageRecord(dir, "sha256:abc", "local/busybox:baseline")
	if err != nil {
		t.Fatalf("tagImageRecord: %v", err)
	}
	if tagged.ImageRef != "local/busybox:baseline" || tagged.OutputPath != rootfsPath {
		t.Fatalf("tagged = %#v", tagged)
	}
	images, err := listImageRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 {
		t.Fatalf("images len = %d, want 2: %#v", len(images), images)
	}
}

func TestImagesRemoveAliasKeepsSharedBaseline(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := upsertImageRecord(dir, imageRecord{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := removeImageRecords(dir, "local/busybox:baseline", true)
	if err != nil {
		t.Fatalf("removeImageRecords: %v", err)
	}
	if len(removed.Removed) != 1 || len(removed.Deleted) != 0 || len(removed.Kept) != 1 {
		t.Fatalf("removed = %#v", removed)
	}
	if _, err := os.Stat(rootfsPath); err != nil {
		t.Fatalf("baseline was removed: %v", err)
	}
}

func TestImagesRemoveDigestDeletesUnsharedBaseline(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := upsertImageRecord(dir, imageRecord{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := removeImageRecords(dir, "sha256:abc", true)
	if err != nil {
		t.Fatalf("removeImageRecords: %v", err)
	}
	if len(removed.Deleted) != 2 || len(removed.Removed) != 0 || len(removed.Kept) != 0 {
		t.Fatalf("removed = %#v", removed)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("baseline still exists or stat failed unexpectedly: %v", err)
	}
}

func TestStartUsesPersistedWorkspaceResources(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("start state writing with a fake executable supervisor is Apple VF-specific")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfsPath := workspace.WorkspaceRootfsPath(dir, "research", vmkit.BackendAppleVF)
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "medium",
		RestartPolicy: "always",
		MemoryMiB:     2048,
		CPUCount:      2,
		SizeMiB:       8192,
	}); err != nil {
		t.Fatal(err)
	}
	supervisor := filepath.Join(dir, "supervisor")
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); print(json.dumps({"ok": True, "backend": "apple-vf", "event": {"identity": req["identity"], "state": "running", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"start",
		"research",
		"--state-dir", dir,
		"--backend", vmkit.BackendAppleVF,
		"--supervisor", supervisor,
		"--kernel", filepath.Join(dir, "Image"),
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run start: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "research"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Config.MemoryMiB != 2048 || state.Config.CPUCount != 2 {
		t.Fatalf("runtime config = memory %d cpus %d", state.Config.MemoryMiB, state.Config.CPUCount)
	}
	manifest, err := readWorkspaceManifest(dir, "research")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Restart != "always" {
		t.Fatalf("restart = %q", manifest.Restart)
	}
}

func TestStartRejectsQuarantinedWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfsPath := workspace.WorkspaceRootfsPath(dir, "research", vmkit.BackendAppleVF)
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		Config: &vmkit.Config{
			KernelPath: kernelPath,
			RootfsPath: filepath.Join(dir, "workspaces", "research", "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateQuarantined, 4242, ""); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"start",
		"research",
		"--state-dir", dir,
		"--backend", hostBackend(),
		"--kernel", kernelPath,
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "is quarantined with preserved pid 4242") {
		t.Fatalf("err = %v, want quarantined start rejection", err)
	}
}

func TestStartRejectsRunningWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendAppleVF,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkspaceCanStart(dir, "research"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v, want running start rejection", err)
	}
}

func TestRunNetworkReportsManifestAndRuntimeNetwork(t *testing.T) {
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network: vmkit.NetworkConfig{
			Mode: "nat",
			PortForwards: []vmkit.PortForward{
				{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
			},
			DNS: []string{"1.1.1.1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Network:    &vmkit.NetworkConfig{Mode: "nat", IP: "192.168.64.2", Routes: []string{"0.0.0.0/0"}},
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "network.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runNetwork([]string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runNetwork: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Forward: tcp 127.0.0.1:8080 -> guest:80") || !strings.Contains(text, "IP: 192.168.64.2") {
		t.Fatalf("network output = %s", data)
	}
}

func TestApplyUpdatesStoppedWorkspaceNetwork(t *testing.T) {
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network: vmkit.NetworkConfig{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "homebridge.yaml")
	spec := []byte(`name: homebridge
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: 8581
      guestPort: 8581
      protocol: tcp
`)
	if err := os.WriteFile(specPath, spec, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "apply.out"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"apply", "--file", specPath, "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	manifest, err := readWorkspaceManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].Host; got != "0.0.0.0" {
		t.Fatalf("forward host = %q, want 0.0.0.0", got)
	}
}

func TestApplyRejectsLiveNonHostNetworkChange(t *testing.T) {
	dir := t.TempDir()
	originalNetwork := vmkit.NetworkConfig{
		Mode:         "user",
		PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network:       originalNetwork,
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "homebridge",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Network:    &originalNetwork,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "homebridge"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "homebridge.yaml")
	spec := []byte(`name: homebridge
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: 8581
      guestPort: 8582
      protocol: tcp
`)
	if err := os.WriteFile(specPath, spec, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "apply.out"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"apply", "--file", specPath, "--state-dir", dir, "--backend", vmkit.BackendFirecracker}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "host bind changes") {
		t.Fatalf("err = %v, want host-bind-only rejection", err)
	}
	manifest, err := readWorkspaceManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].GuestPort; got != 8581 {
		t.Fatalf("guest port = %d, want unchanged 8581", got)
	}
}

func TestStatusReportsRuntimeNetworkAssignment(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network:       vmkit.NetworkConfig{Mode: "nat"},
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Network: &vmkit.NetworkConfig{
				Mode:    "nat",
				IP:      "10.43.12.2/29",
				Subnet:  "10.43.12.0/29",
				Gateway: "10.43.12.1",
				DNS:     []string{"1.1.1.1", "8.8.8.8"},
				Routes:  []string{"0.0.0.0/0 via 10.43.12.1"},
			},
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "status.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runWorkspaceStateCommand(context.Background(), "status", []string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"runtime"`) ||
		!strings.Contains(string(data), `"ip": "10.43.12.2/29"`) ||
		!strings.Contains(string(data), `"subnet": "10.43.12.0/29"`) {
		t.Fatalf("status output = %s", data)
	}
}

func TestStatusReportsDeclaredArtifacts(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	opts := workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Disks: []workspaceDisk{{
			Name:       "config",
			SourcePath: "/tmp/config.tar",
			Path:       filepath.Join(dir, "workspaces", "research", "config.ext4"),
			Mountpoint: "/config",
			Mode:       "ro",
			Bundle:     true,
		}},
		Outputs: []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}
	if err := writeWorkspaceManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StatePrepared, 0, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Artifacts == nil || len(resp.Artifacts.Ingress) != 1 || len(resp.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", resp.Artifacts)
	}
	if resp.Artifacts.Ingress[0].Name != "config" || resp.Artifacts.Ingress[0].Kind != "bundle" || resp.Artifacts.Ingress[0].Mountpoint != "/config" {
		t.Fatalf("ingress = %#v", resp.Artifacts.Ingress[0])
	}
	if resp.Artifacts.Egress[0].Name != "report" || resp.Artifacts.Egress[0].Path != "/workspace/report.json" {
		t.Fatalf("egress = %#v", resp.Artifacts.Egress[0])
	}
}

func TestArtifactsCommandListsDeclaredArtifacts(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Disks: []workspaceDisk{{
			Name:       "config",
			SourcePath: "/tmp/config.tar",
			Path:       filepath.Join(dir, "workspaces", "research", "config.ext4"),
			Mountpoint: "/config",
			Mode:       "ro",
			Bundle:     true,
		}},
		Outputs: []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "artifacts.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"artifacts", "research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run artifacts: %v", err)
	}
	var result artifactsResult
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Workspace != "research" || len(result.Artifacts.Ingress) != 1 || len(result.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", result)
	}
	if result.Artifacts.Egress[0].Name != "report" || result.Artifacts.Egress[0].Path != "/workspace/report.json" {
		t.Fatalf("egress = %#v", result.Artifacts.Egress[0])
	}
}

func TestArtifactGetCopiesDeclaredRootfsOutput(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	workspaceDir := filepath.Join(dir, "workspaces", "research")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
		Outputs:   []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := getWorkspaceArtifact(dir, debugfs, "research", "report", targetDir)
	if err != nil {
		t.Fatalf("getWorkspaceArtifact: %v", err)
	}
	if result.Artifact != "report" || result.Disk != "rootfs" || result.Direction != "from-workspace" {
		t.Fatalf("result = %#v", result)
	}
	if data, err := os.ReadFile(filepath.Join(targetDir, "report.json")); err != nil || string(data) != "fake-dump" {
		t.Fatalf("artifact data = %q err=%v", data, err)
	}
}

func TestArtifactGetMapsOutputUnderAttachedDiskMount(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	workspaceDir := filepath.Join(dir, "workspaces", "research")
	diskPath := filepath.Join(workspaceDir, "disks", "workspace.ext4")
	if err := os.MkdirAll(filepath.Dir(diskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diskPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
		Disks:     []workspaceDisk{{Name: "workspace", Path: diskPath, Mountpoint: "/workspace", Mode: "rw"}},
		Outputs:   []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := getWorkspaceArtifact(dir, debugfs, "research", "report", targetDir)
	if err != nil {
		t.Fatalf("getWorkspaceArtifact: %v", err)
	}
	if result.Disk != "workspace" || result.Source != "research:workspace:/report.json" {
		t.Fatalf("result = %#v", result)
	}
	logData, err := os.ReadFile(filepath.Join(dir, "debugfs.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "-R|dump|/report.json|") {
		t.Fatalf("debugfs log = %s", logData)
	}
}

func TestRunArtifactGetCommand(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	workspaceDir := filepath.Join(dir, "workspaces", "research")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
		Outputs:   []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"artifacts", "get", "research", "report", targetDir, "--state-dir", dir, "--debugfs", debugfs}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run artifacts get: %v", err)
	}
	var result copyResult
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Artifact != "report" || result.Workspace != "research" || result.Direction != "from-workspace" {
		t.Fatalf("result = %#v", result)
	}
}

func TestArtifactGetRejectsUndeclaredOutput(t *testing.T) {
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := getWorkspaceArtifact(dir, "debugfs", "research", "missing", filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("err = %v, want undeclared artifact error", err)
	}
}

func TestStatusReportsMediationReadiness(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mediation := vmkit.MediationConfig{
		Enabled:    true,
		Required:   true,
		Port:       2048,
		Target:     listener.Addr().String(),
		FailClosed: true,
	}
	opts := workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Mediation:     &mediation,
	}
	if err := writeWorkspaceManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Mediation:  &mediation,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mediation == nil || !resp.Mediation.Required || !resp.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", resp.Mediation)
	}
	if resp.Readiness == nil || !resp.Readiness.MediationReady.Ready {
		t.Fatalf("readiness = %#v", resp.Readiness)
	}
	if !strings.Contains(resp.Readiness.MediationReady.Detail, "port=2048") {
		t.Fatalf("mediation detail = %q", resp.Readiness.MediationReady.Detail)
	}
	if !strings.Contains(resp.Readiness.MediationReady.Detail, "reachable") {
		t.Fatalf("mediation detail = %q, want live reachability detail", resp.Readiness.MediationReady.Detail)
	}
}

func TestSuperviseWorkspaceSkipsNeverPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfsPath := workspace.WorkspaceRootfsPath(dir, "research", hostBackend())
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := superviseWorkspace(t.Context(), superviseOptions{
		StateDir:       dir,
		Name:           "research",
		Backend:        hostBackend(),
		Architecture:   defaultGuestArch(),
		KernelPath:     kernelPath,
		KernelExplicit: true,
		SupervisorPath: "/tmp/supervisor",
	})
	if err != nil {
		t.Fatalf("superviseWorkspace: %v", err)
	}
	if result.Policy != "never" || !result.Stopped || result.Restarts != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCloneWorkspaceCopiesStoppedWorkspace(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "workspaces", "template")
	if err := os.MkdirAll(filepath.Join(sourceDir, "disks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "disks", "workspace.ext4"), []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "template",
		Profile:   "medium",
		MemoryMiB: 2048,
		CPUCount:  2,
		SizeMiB:   8192,
		Disks: []workspaceDisk{{
			Name:       "workspace",
			Path:       filepath.Join(sourceDir, "disks", "workspace.ext4"),
			Mountpoint: "/workspace",
			Mode:       "rw",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "template", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "template"}, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	result, err := cloneWorkspace(dir, "template", "copy")
	if err != nil {
		t.Fatalf("cloneWorkspace: %v", err)
	}
	if result.Workspace != "copy" || result.Profile != "medium" || result.Resources.MemoryMiB != 2048 {
		t.Fatalf("clone result = %#v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "workspaces", "copy", "rootfs.ext4")); err != nil || string(data) != "rootfs" {
		t.Fatalf("cloned rootfs = %q err=%v", data, err)
	}
	manifest, err := readWorkspaceManifest(dir, "copy")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "copy" {
		t.Fatalf("manifest name = %q", manifest.Name)
	}
	wantDiskPath := filepath.Join(dir, "workspaces", "copy", "disks", "workspace.ext4")
	if len(manifest.Disks) != 1 || manifest.Disks[0].Path != wantDiskPath {
		t.Fatalf("manifest disks = %#v, want path %q", manifest.Disks, wantDiskPath)
	}
	event, err := readWorkspaceEvent(workspaceOptions{StateDir: dir, Name: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if event.State != vmkit.StatePrepared || !strings.Contains(event.Detail, "template") {
		t.Fatalf("event = %#v", event)
	}
}

func TestCloneWorkspaceRejectsActiveSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "active", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "active", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "active", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "active"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	_, err := cloneWorkspace(dir, "active", "copy")
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped validation", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "workspaces", "copy")); !os.IsNotExist(statErr) {
		t.Fatalf("target was created despite failed clone: %v", statErr)
	}
}

func TestCloneWorkspaceRejectsEventOnlyActiveSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "active", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "active", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := workspaceEventFile{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "active", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		State:      vmkit.StateRunning,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "active", "event.json"), event); err != nil {
		t.Fatal(err)
	}
	_, err := cloneWorkspace(dir, "active", "copy")
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped validation", err)
	}
}

func TestRunCloneCommand(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "template"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "template", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "template", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "clone", "template", "copy", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run clone: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"workspace": "copy"`) || !strings.Contains(string(data), `"state": "prepared"`) {
		t.Fatalf("clone output = %s", data)
	}
}

func TestCopyWorkspaceFileToRootfs(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "research", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := copyWorkspaceFile(dir, debugfs, source, "research:/workspace/hello.txt")
	if err != nil {
		t.Fatalf("copyWorkspaceFile: %v", err)
	}
	if result.Direction != "to-workspace" || result.Disk != "rootfs" || result.Bytes != 5 {
		t.Fatalf("result = %#v", result)
	}
	logData, err := os.ReadFile(filepath.Join(dir, "debugfs.log"))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "-w|-R|write|"+source+"|/workspace/hello.txt") {
		t.Fatalf("debugfs log = %s", logText)
	}
}

func TestCopyWorkspaceFileFromAttachedDisk(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	workspaceDir := filepath.Join(dir, "workspaces", "research")
	diskPath := filepath.Join(workspaceDir, "disks", "workspace.ext4")
	if err := os.MkdirAll(filepath.Dir(diskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diskPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
		Disks:     []workspaceDisk{{Name: "workspace", Path: diskPath, Mountpoint: "/workspace", Mode: "rw"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := copyWorkspaceFile(dir, debugfs, "research:workspace:/notes.txt", targetDir)
	if err != nil {
		t.Fatalf("copyWorkspaceFile: %v", err)
	}
	if result.Direction != "from-workspace" || result.Disk != "workspace" {
		t.Fatalf("result = %#v", result)
	}
	targetPath := filepath.Join(targetDir, "notes.txt")
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-dump" {
		t.Fatalf("dumped data = %q", data)
	}
}

func TestCopyWorkspaceFileRejectsActiveWorkspace(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "active", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "active", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "active", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "active"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := copyWorkspaceFile(dir, debugfs, source, "active:/hello.txt")
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped validation", err)
	}
}

func TestCopyWorkspaceFileRejectsTwoRemoteEndpoints(t *testing.T) {
	_, err := copyWorkspaceFile(t.TempDir(), "debugfs", "a:/x", "b:/y")
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("err = %v, want endpoint validation", err)
	}
}

func TestRunCPCommand(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "research", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "cp", "--debugfs", debugfs, "--state-dir", dir, source, "research:/hello.txt"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run cp: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"direction": "to-workspace"`) || !strings.Contains(string(data), `"workspace": "research"`) {
		t.Fatalf("cp output = %s", data)
	}
}

func fakeDebugFS(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "debugfs.cmd")
		script := `@echo off
setlocal EnableDelayedExpansion
set "log=` + filepath.Join(dir, "debugfs.log") + `"
set "line=%*"
set "line=!line:"=!"
set "line=!line: =|!"
>>"%log%" echo !line!
:args
if "%~1"=="" goto done_args
if "%~1"=="-R" (
  set "cmd=%~2"
  set "target="
  for %%P in (!cmd!) do set "target=%%~P"
  if "!cmd:~0,5!"=="dump " (
    >"!target!" <nul set /p "=fake-dump"
  )
)
shift
goto args
:done_args
`
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "debugfs")
	script := `#!/usr/bin/env bash
set -euo pipefail
log="` + filepath.Join(dir, "debugfs.log") + `"
# Strip the double quotes that the host adds around -R request arguments,
# mirroring how real debugfs tokenizes quoted request words.
printf '%s\n' "$*" | tr -d '"' | tr ' ' '|' >> "$log"
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [[ "${args[$i]}" == "-R" ]]; then
    cmd="${args[$((i+1))]}"
    if [[ "$cmd" == dump\ * ]]; then
      target="${cmd##* }"
      target="${target%\"}"
      target="${target#\"}"
      printf fake-dump > "$target"
    fi
  fi
done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCreateDispatchKeepsLowLevelSupervisorCreate(t *testing.T) {
	if !shouldUseHighLevelCreate([]string{"research"}) {
		t.Fatal("positional create should use high-level workspace create")
	}
	if !shouldUseHighLevelCreate([]string{"--name", "research"}) {
		t.Fatal("--name create should use high-level workspace create")
	}
	if shouldUseHighLevelCreate([]string{"--id", "agent", "--rootfs", "/tmp/rootfs.ext4", "--kernel", "/tmp/Image"}) {
		t.Fatal("low-level rootfs create should stay on supervisor create path")
	}
}

func TestWorkspaceSupervisorSelectsHostBackendOnly(t *testing.T) {
	opts := workspaceOptions{Backend: hostBackend()}
	if hostBackend() == vmkit.BackendAppleVF {
		opts.SupervisorPath = "/tmp/applevf"
	}
	supervisor, err := workspaceSupervisor(opts)
	if err != nil {
		t.Fatalf("host supervisor: %v", err)
	}
	if hostBackend() == vmkit.BackendWindowsHyperV {
		if _, ok := supervisor.(windowshyperv.Supervisor); !ok {
			t.Fatalf("host supervisor = %T, want windowshyperv.Supervisor", supervisor)
		}
	} else {
		executable, ok := supervisor.(vmkit.ExecutableSupervisor)
		if !ok {
			t.Fatalf("host supervisor = %T, want vmkit.ExecutableSupervisor", supervisor)
		}
		if hostBackend() == vmkit.BackendFirecracker && executable.Path != "microagent-firecracker-supervisor" {
			t.Fatalf("firecracker supervisor path = %q", executable.Path)
		}
		if hostBackend() == vmkit.BackendAppleVF && executable.Path != "/tmp/applevf" {
			t.Fatalf("apple vf supervisor path = %q", executable.Path)
		}
	}

	otherBackend := vmkit.BackendFirecracker
	if hostBackend() == vmkit.BackendFirecracker {
		otherBackend = vmkit.BackendAppleVF
	} else if hostBackend() == vmkit.BackendWindowsHyperV {
		otherBackend = vmkit.BackendFirecracker
	}
	if _, err := workspaceSupervisor(workspaceOptions{Backend: otherBackend}); err == nil {
		t.Fatalf("workspaceSupervisor(%q) err = nil, want host-only rejection", otherBackend)
	}
}

func TestParseWorkspaceOptionsUsesHostSupervisorDefault(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", []string{
		"--image", "docker.io/library/busybox:1.36",
		"--exec", "true",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	want := defaultSupervisorPath(hostBackend())
	if opts.SupervisorPath != want {
		t.Fatalf("SupervisorPath = %q, want %q", opts.SupervisorPath, want)
	}
}

func TestParseWorkspaceOptionsAcceptsContainerStyleRunCommand(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", []string{
		"docker.io/library/busybox:1.36",
		"echo",
		"hello world",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/busybox:1.36" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.ExecCommand != "exec 'echo' 'hello world'" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if opts.UseImageCommand {
		t.Fatal("UseImageCommand = true")
	}
}

func TestParseWorkspaceOptionsRunImageDefaultsToImageCommand(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", []string{
		"docker.io/library/busybox:1.36",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/busybox:1.36" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.ExecCommand != "" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if !opts.UseImageCommand {
		t.Fatal("UseImageCommand = false")
	}
}

func TestParseWorkspaceOptionsRunPositionalCommandConflictsWithExec(t *testing.T) {
	_, err := parseWorkspaceOptions("run", []string{
		"--image", "docker.io/library/busybox:1.36",
		"--exec", "true",
		"echo",
	})
	if err == nil || !strings.Contains(err.Error(), "both --exec and positional command") {
		t.Fatalf("err = %v, want positional command conflict", err)
	}
}

func TestParseWorkspaceOptionsAcceptsContainerStyleRunAliases(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", []string{
		"-e", "GREETING=hello",
		"-p", "127.0.0.1:18080:8080/tcp",
		"--rm",
		"docker.io/library/busybox:1.36",
		"printenv",
		"GREETING",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Env["GREETING"] != "hello" {
		t.Fatalf("Env = %#v", opts.Env)
	}
	if opts.Keep {
		t.Fatal("Keep = true")
	}
	if len(opts.Network.PortForwards) != 1 {
		t.Fatalf("PortForwards = %#v", opts.Network.PortForwards)
	}
	forward := opts.Network.PortForwards[0]
	if forward.Host != "127.0.0.1" || forward.HostPort != 18080 || forward.GuestPort != 8080 || forward.Protocol != "tcp" {
		t.Fatalf("PortForward = %#v", forward)
	}
	if opts.ExecCommand != "exec 'printenv' 'GREETING'" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
}

func TestParseWorkspaceOptionsRejectsRunRmKeepConflict(t *testing.T) {
	_, err := parseWorkspaceOptions("run", []string{
		"--rm",
		"--keep",
		"docker.io/library/busybox:1.36",
		"true",
	})
	if err == nil || !strings.Contains(err.Error(), "both --rm and --keep") {
		t.Fatalf("err = %v, want --rm --keep conflict", err)
	}
}

func TestParseWorkspaceOptionsPreservesExplicitSupervisor(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", []string{
		"--supervisor", "/tmp/microagent-supervisor",
		"--image", "docker.io/library/busybox:1.36",
		"--exec", "true",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.SupervisorPath != "/tmp/microagent-supervisor" {
		t.Fatalf("SupervisorPath = %q", opts.SupervisorPath)
	}
}

func TestDefaultPerfBootOptionsUsesHostSupervisorDefault(t *testing.T) {
	opts := defaultPerfBootOptions()
	want := defaultSupervisorPath(hostBackend())
	if opts.SupervisorPath != want {
		t.Fatalf("SupervisorPath = %q, want %q", opts.SupervisorPath, want)
	}
}

func firecrackerSupervisorHelper(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "microagent-firecracker-supervisor")
	script := fmt.Sprintf("#!/usr/bin/env bash\nGO_WANT_FIRECRACKER_SUPERVISOR_HELPER=1 %q\n", executable)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func processStillActive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if !platformProcessStillActive(pid) {
		return false
	}
	if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 && fields[2] == "Z" {
			return false
		}
	}
	return true
}

func TestWorkspaceCommandRunsSetupBeforeExec(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		SetupCommands: []string{"apt-get update", "apt-get install -y git"},
		ExecCommand:   "uname -a",
	})
	want := "set -eu\napt-get update\napt-get install -y git\nuname -a"
	if command != want {
		t.Fatalf("workspaceCommand = %q, want %q", command, want)
	}
}

func TestWorkspaceCommandAllowsMultiCommandExec(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		SetupCommands: []string{"echo setup"},
		ExecCommand:   "echo one; echo two",
	})
	want := "set -eu\necho setup\necho one; echo two"
	if command != want {
		t.Fatalf("workspaceCommand = %q, want %q", command, want)
	}
}

func TestExecWantsHelpIgnoresGuestArgvAfterSeparator(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"guest -h after separator", []string{"ws", "--", "psql", "-h", "x"}, false},
		{"guest --help after separator", []string{"ws", "--", "wget", "--help"}, false},
		{"guest literal help after separator", []string{"ws", "--", "help"}, false},
		{"cli --help", []string{"--help"}, true},
		{"cli -h before separator", []string{"ws", "-h"}, true},
		{"cli help word", []string{"help"}, true},
		{"cli --help with guest argv", []string{"ws", "--help", "--", "ls"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := execWantsHelp(tc.args); got != tc.want {
				t.Fatalf("execWantsHelp(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestWorkspaceCommandLeavesGuestConfigResetToRootfsBuilder(t *testing.T) {
	opts := workspaceOptions{
		Entrypoint:      "/app/entrypoint.sh",
		ConsoleShell:    "/bin/bash",
		Hostname:        "research-vm",
		SetupCommands:   []string{"echo setup"},
		Env:             map[string]string{"AGENCY_AGENT_NAME": "research"},
		Disks:           []workspaceDisk{{Name: "constraints", Path: "/tmp/constraints.ext4", Mountpoint: "/config", Mode: "ro"}},
		Network:         vmkit.NetworkConfig{Mode: defaultNetworkMode, PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8080, GuestPort: 80}}},
		ResultPort:      1024,
		PrepareForStart: true,
	}
	command := workspaceCommand(opts)
	if !strings.Contains(command, "echo setup") {
		t.Fatalf("workspaceCommand missing setup: %q", command)
	}
	// The reset line is composed by the rootfs builder (which merges image
	// env), not the workspace command script.
	if strings.Contains(command, "/etc/microagent/run.json") {
		t.Fatalf("workspaceCommand should not embed guest config reset: %q", command)
	}
	finalCommand, finalMode, reset := workspace.FinalCommandAndMode(opts)
	if !reset || finalMode != "" {
		t.Fatalf("FinalCommandAndMode = %#v, %q, %v; want reset with empty mode", finalCommand, finalMode, reset)
	}
	if strings.Join(finalCommand, " ") != "/bin/sh -lc /app/entrypoint.sh" {
		t.Fatalf("finalCommand = %#v", finalCommand)
	}
}

func TestFinalCommandAndModeUsesServiceCommandWithSetup(t *testing.T) {
	finalCommand, finalMode, reset := workspace.FinalCommandAndMode(workspaceOptions{
		ServiceCommand:  "/opt/app/serve.sh",
		SetupCommands:   []string{"echo setup"},
		PrepareForStart: true,
	})
	if !reset || finalMode != "managed-service" {
		t.Fatalf("FinalCommandAndMode = %#v, %q, %v; want managed-service reset", finalCommand, finalMode, reset)
	}
	if strings.Join(finalCommand, " ") != "/bin/sh -lc /opt/app/serve.sh" {
		t.Fatalf("finalCommand = %#v", finalCommand)
	}
}

func TestFinalCommandAndModeSkipsServiceOnlyAndPlainStarts(t *testing.T) {
	if _, _, reset := workspace.FinalCommandAndMode(workspaceOptions{
		ServiceCommand:  "/opt/app/serve.sh",
		PrepareForStart: true,
	}); reset {
		t.Fatal("service-only create should not need a guest config reset")
	}
	if _, _, reset := workspace.FinalCommandAndMode(workspaceOptions{
		Entrypoint:      "/app/entrypoint.sh",
		PrepareForStart: true,
	}); reset {
		t.Fatal("create without setup/exec should not need a guest config reset")
	}
	if _, _, reset := workspace.FinalCommandAndMode(workspaceOptions{
		Entrypoint:    "/app/entrypoint.sh",
		SetupCommands: []string{"echo setup"},
	}); reset {
		t.Fatal("non-prepare runs should not need a guest config reset")
	}
}

func TestWorkspaceBuildCommandUsesStartConfigWhenNoSetupIsNeeded(t *testing.T) {
	command, port := workspaceBuildCommandAndPort(workspaceOptions{
		Entrypoint:      "/app/entrypoint.sh",
		ResultPort:      1024,
		PrepareForStart: true,
	})
	if port != 1024 {
		t.Fatalf("port = %d, want 1024", port)
	}
	if strings.Join(command, " ") != "/bin/sh -lc /app/entrypoint.sh" {
		t.Fatalf("command = %#v", command)
	}
}

func TestCreateWorkspaceRootfsCanUseImageCommand(t *testing.T) {
	opts := workspaceOptions{
		ImageRef:        "local/busybox:baseline",
		Architecture:    "arm64",
		ResultPort:      1024,
		PrepareForStart: true,
		UseImageCommand: true,
	}
	command, port := workspaceBuildCommandAndPort(opts)
	if len(command) != 0 {
		t.Fatalf("command = %#v, want image command from OCI config", command)
	}
	if port != 0 {
		t.Fatalf("port = %d, want 0", port)
	}
}

func TestCreateWorkspaceRootfsCanUseServiceCommand(t *testing.T) {
	opts := workspaceOptions{
		ImageRef:        "homebridge/homebridge:latest",
		Architecture:    "arm64",
		ResultPort:      1024,
		PrepareForStart: true,
		ServiceCommand:  "/opt/homebridge/start.sh --allow-root",
	}
	command, port := workspaceBuildCommandAndPort(opts)
	if strings.Join(command, " ") != "/bin/sh -lc /opt/homebridge/start.sh --allow-root" {
		t.Fatalf("command = %#v", command)
	}
	if port != 0 {
		t.Fatalf("port = %d, want 0", port)
	}
}

func TestCreateWorkspaceRootfsRunsSetupBeforeManagedService(t *testing.T) {
	opts := workspaceOptions{
		ImageRef:        "docker.io/library/ubuntu:24.04",
		Architecture:    "arm64",
		ResultPort:      1024,
		PrepareForStart: true,
		SetupCommands:   []string{"echo setup"},
		ServiceCommand:  "/usr/local/bin/microagent-homebridge",
	}
	command, port := workspaceBuildCommandAndPort(opts)
	if port != 1024 {
		t.Fatalf("port = %d, want 1024", port)
	}
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "echo setup") {
		t.Fatalf("command = %#v", command)
	}
	finalCommand, finalMode, reset := workspace.FinalCommandAndMode(opts)
	if !reset || finalMode != "managed-service" {
		t.Fatalf("FinalCommandAndMode = %#v, %q, %v; want managed-service reset", finalCommand, finalMode, reset)
	}
	if !strings.Contains(strings.Join(finalCommand, " "), "/usr/local/bin/microagent-homebridge") {
		t.Fatalf("finalCommand = %#v", finalCommand)
	}
}

func TestRunHighLevelCreateDoesNotRenderEmptyResultOnPreflightFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runHighLevelCreate(t.Context(), []string{
		"port-check",
		"--state-dir", dir,
		"--publish", portText + ":80",
		"--size-mib", "512",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "host port 127.0.0.1:"+portText+" is unavailable") {
		t.Fatalf("runHighLevelCreate err = %v", err)
	}
	out, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(out), "Workspace:") {
		t.Fatalf("stdout = %q", string(out))
	}
}

func TestRunStartWorkspaceDoesNotRenderEmptyResultOnMissingWorkspace(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runStartWorkspace(t.Context(), []string{"missing", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "workspace.json") {
		t.Fatalf("runStartWorkspace err = %v", err)
	}
	out, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(out), "Workspace:") {
		t.Fatalf("stdout = %q", string(out))
	}
}

func TestFormatProgressEventSupportsIndeterminateGuestSetup(t *testing.T) {
	got := formatProgressEvent(rootfs.ProgressEvent{
		Phase:         "guest-setup",
		Message:       "running guest setup",
		Current:       65,
		Indeterminate: true,
	})
	if !strings.Contains(got, "running guest setup") || !strings.Contains(got, "1m05s") {
		t.Fatalf("progress = %q", got)
	}
}

func TestWriteCreateResultSuppressesSuccessfulSetupLogs(t *testing.T) {
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	result := workspaceResult{
		Workspace: "homebridge",
		Network:   networkSpec{Mode: "user"},
		Result: &guestResult{
			ExitCode: 0,
			Stdout:   "Homebridge Installation Complete!\n",
			Stderr:   "debconf: delaying package configuration\n",
		},
	}
	if err := writeCreateResult(stdout, result, nil); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "Homebridge Installation Complete") || strings.Contains(text, "debconf") || strings.Contains(text, "Exit code") {
		t.Fatalf("create output included setup logs: %q", text)
	}
	if !strings.Contains(text, "Workspace: homebridge") || !strings.Contains(text, "Network: user") {
		t.Fatalf("create output missing summary: %q", text)
	}
}

func TestWriteCreateResultKeepsFailedSetupLogs(t *testing.T) {
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	result := workspaceResult{
		Workspace: "homebridge",
		Result: &guestResult{
			ExitCode: 127,
			Stderr:   "setup failed\n",
			Error:    "exit status 127",
		},
	}
	if err := writeCreateResult(stdout, result, errors.New("exit status 127")); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Exit code: 127") || !strings.Contains(text, "setup failed") {
		t.Fatalf("create output omitted failure logs: %q", text)
	}
}

func TestParseWorkspaceOptionsAcceptsPositionalNameWithImageCommand(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"homebridge",
		"--image", "homebridge/homebridge:latest",
		"--image-command",
		"--network", "nat",
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
	opts, err := parseWorkspaceOptions("create", []string{
		"homebridge",
		"--image", "homebridge/homebridge:latest",
		"--service-command", "/opt/homebridge/start.sh --allow-root",
		"--network", "nat",
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
	_, err := parseWorkspaceOptions("create", []string{
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
	if err := upsertImageRecord(dir, imageRecord{
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

func TestConnectShellTargetUsesWindowsHyperVRuntimeID(t *testing.T) {
	state := workspace.RuntimeState{
		Event: workspace.EventFile{
			Identity: vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendWindowsHyperV},
			State:    vmkit.StateRunning,
		},
		Config:                 vmkit.Config{ShellPort: 25000},
		ComputeSystemRuntimeID: "11111111-1111-1111-1111-111111111111",
	}
	target, err := workspace.ConsoleTarget("agent-1", state)
	if err != nil {
		t.Fatal(err)
	}
	if target.Network != "hvsock" || target.RuntimeID != "11111111-1111-1111-1111-111111111111" || target.Port != 25000 {
		t.Fatalf("target = %#v", target)
	}
}

func TestConnectShellTargetRejectsWindowsHyperVWithoutRuntimeID(t *testing.T) {
	state := workspace.RuntimeState{
		Event: workspace.EventFile{
			Identity: vmkit.Identity{RuntimeID: "agent-1", Backend: vmkit.BackendWindowsHyperV},
			State:    vmkit.StateRunning,
		},
		Config: vmkit.Config{ShellPort: 25000},
	}
	if _, err := workspace.ConsoleTarget("agent-1", state); err == nil || !strings.Contains(err.Error(), "compute system runtime ID") {
		t.Fatalf("ConsoleTarget err = %v, want compute system runtime ID", err)
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

func TestWindowsHyperVConnectSmoke(t *testing.T) {
	if os.Getenv("MICROAGENT_WINDOWS_HYPERV_SMOKE") != "1" {
		t.Skip("set MICROAGENT_WINDOWS_HYPERV_SMOKE=1 to run the Windows Hyper-V connect smoke test")
	}
	kernelPath := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_KERNEL"))
	if kernelPath == "" {
		t.Fatal("MICROAGENT_WINDOWS_HYPERV_KERNEL is required")
	}
	guestInitPath := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_GUESTINIT"))
	if guestInitPath == "" {
		guestInitPath = filepath.Join("..", "..", ".build", "dev", "microagent-guestinit-amd64")
	}
	if _, err := os.Stat(guestInitPath); err != nil {
		t.Fatalf("guest init %q: %v", guestInitPath, err)
	}
	stateDir := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_STATE_DIR"))
	if stateDir == "" {
		var err error
		stateDir, err = os.MkdirTemp("", "microagent-windows-hyperv-connect-*")
		if err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("state dir: %s", stateDir)
	// The detached start spawns the runtime listener helper (exec bridge)
	// from the running executable, so start must go through the real CLI.
	cliPath := filepath.Join(t.TempDir(), "microagent.exe")
	buildCmd(t, filepath.Join("..", ".."), cliPath, "./cmd/microagent", "", "")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	workspaceOpts := workspace.Options{
		Name:            "windows-hyperv-connect",
		Backend:         vmkit.BackendWindowsHyperV,
		Architecture:    "amd64",
		StateDir:        stateDir,
		KernelPath:      kernelPath,
		GuestInitPath:   guestInitPath,
		ImageRef:        "docker.io/library/busybox:1.36",
		ServiceCommand:  "sleep 60",
		PrepareForStart: true,
		Timeout:         time.Minute,
		Keep:            true,
		MemoryMiB:       512,
		CPUCount:        2,
		Network:         vmkit.NetworkConfig{Mode: "user"},
	}
	if _, err := workspace.BuildRootfs(ctx, workspaceOpts); err != nil {
		t.Fatalf("BuildRootfs: %v", err)
	}
	if err := workspace.WriteManifest(workspaceOpts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = runExternalOutput(cleanupCtx, cliPath, "stop", "windows-hyperv-connect", "--state-dir", stateDir)
		_, _ = runExternalOutput(cleanupCtx, cliPath, "delete", "windows-hyperv-connect", "--state-dir", stateDir, "--yes")
	})
	runExternal(t, ctx, cliPath, "start", "windows-hyperv-connect", "--state-dir", stateDir, "--kernel", kernelPath)
	waitForWorkspaceState(t, stateDir, "windows-hyperv-connect", vmkit.StateRunning, 30*time.Second)
	// Shell readiness is probed over hv_sock, so allow the guest shell
	// helper a bounded window to come up after the compute system starts.
	readyDeadline := time.Now().Add(30 * time.Second)
	for {
		status, err := workspace.Status(workspace.Options{
			Name:     "windows-hyperv-connect",
			Backend:  vmkit.BackendWindowsHyperV,
			StateDir: stateDir,
		})
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Readiness != nil && status.Readiness.ShellReady.Ready {
			break
		}
		if time.Now().After(readyDeadline) {
			logWindowsHyperVSmokeState(t, stateDir, "windows-hyperv-connect")
			t.Fatalf("shell readiness = %#v", status.Readiness)
		}
		time.Sleep(500 * time.Millisecond)
	}

	stdoutPath := filepath.Join(stateDir, "connect.out")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runConnect(ctx, []string{"windows-hyperv-connect", "--state-dir", stateDir, "--send", "echo CONNECT_SMOKE", "--ready-timeout", "45", "--timeout", "3"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "CONNECT_SMOKE") {
		t.Fatalf("connect output = %q", data)
	}
}

func TestWindowsHyperVMediationSmoke(t *testing.T) {
	if os.Getenv("MICROAGENT_WINDOWS_HYPERV_MEDIATION_SMOKE") != "1" {
		t.Skip("set MICROAGENT_WINDOWS_HYPERV_MEDIATION_SMOKE=1 to run the Windows Hyper-V mediation smoke test")
	}
	kernelPath := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_KERNEL"))
	if kernelPath == "" {
		t.Fatal("MICROAGENT_WINDOWS_HYPERV_KERNEL is required")
	}
	dir := t.TempDir()
	workspaceName := fmt.Sprintf("whv-med-%d", time.Now().UnixNano()%1000000000)
	cliPath := filepath.Join(dir, "microagent.exe")
	guestInitPath := filepath.Join(dir, "microagent-guestinit")
	probePath := filepath.Join(dir, "mediation-probe")
	buildCmd(t, filepath.Join("..", ".."), cliPath, "./cmd/microagent", "", "")
	buildCmd(t, filepath.Join("..", ".."), guestInitPath, "./cmd/microagent-guestinit", "linux", "amd64")
	buildMediationProbe(t, dir, probePath)

	hostListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostListener.Close()
	observed := make(chan string, 1)
	go func() {
		conn, err := hostListener.Accept()
		if err != nil {
			observed <- "accept: " + err.Error()
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			observed <- "read: " + err.Error()
			return
		}
		_, _ = conn.Write([]byte(`{"ok":true}` + "\n"))
		observed <- line
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	workspaceOpts := workspace.Options{
		Name:            workspaceName,
		Backend:         vmkit.BackendWindowsHyperV,
		Architecture:    "amd64",
		StateDir:        dir,
		KernelPath:      kernelPath,
		GuestInitPath:   guestInitPath,
		ImageRef:        "docker.io/library/busybox:1.36",
		ServiceCommand:  "/usr/local/bin/mediation-probe",
		PrepareForStart: true,
		Env:             map[string]string{"MICROAGENT_RUNTIME_ID": workspaceName, "MEDIATION_PORT": "2048"},
		MemoryMiB:       512,
		CPUCount:        2,
		SizeMiB:         1024,
		Network:         vmkit.NetworkConfig{Mode: "user"},
		Mediation: &vmkit.MediationConfig{
			Enabled:    true,
			Required:   true,
			Port:       2048,
			Target:     hostListener.Addr().String(),
			FailClosed: true,
		},
		Files: []workspace.File{{
			SourcePath: probePath,
			Path:       "/usr/local/bin/mediation-probe",
			Mode:       "0755",
		}},
	}
	if _, err := workspace.BuildRootfs(ctx, workspaceOpts); err != nil {
		t.Fatalf("BuildRootfs: %v", err)
	}
	if err := workspace.WriteManifest(workspaceOpts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = runExternalOutput(cleanupCtx, cliPath, "stop", workspaceName, "--state-dir", dir)
		_, _ = runExternalOutput(cleanupCtx, cliPath, "delete", workspaceName, "--state-dir", dir, "--yes")
	})
	runExternal(t, ctx, cliPath, "start", workspaceName, "--state-dir", dir, "--kernel", kernelPath)
	select {
	case line := <-observed:
		if !strings.Contains(line, `"signal":"ready"`) || !strings.Contains(line, `"runtimeID":"`+workspaceName+`"`) {
			t.Fatalf("observed mediation line = %q", line)
		}
	case <-time.After(90 * time.Second):
		logWindowsHyperVSmokeState(t, dir, workspaceName)
		t.Fatal("timed out waiting for mediated guest message")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var runtimeState workspace.RuntimeState
	runtimeData, err := os.ReadFile(filepath.Join(dir, workspaceName, "runtime.json"))
	if err != nil {
		t.Fatalf("read runtime.json: %v", err)
	}
	if err := json.Unmarshal(runtimeData, &runtimeState); err != nil {
		t.Fatalf("parse runtime.json: %v\n%s", err, runtimeData)
	}
	if runtimeState.VsockListenerPID == 0 {
		t.Fatalf("runtime.json missing vsock listener pid:\n%s", runtimeData)
	}
	if runtimeState.Config.Mediation == nil || !runtimeState.Config.Mediation.Enabled || runtimeState.Config.Mediation.Port != 2048 {
		t.Fatalf("runtime.json missing mediation config:\n%s", runtimeData)
	}
}

func TestWindowsHyperVExecSmoke(t *testing.T) {
	if os.Getenv("MICROAGENT_WINDOWS_HYPERV_SMOKE") != "1" {
		t.Skip("set MICROAGENT_WINDOWS_HYPERV_SMOKE=1 to run the Windows Hyper-V exec smoke test")
	}
	kernelPath := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_KERNEL"))
	if kernelPath == "" {
		t.Fatal("MICROAGENT_WINDOWS_HYPERV_KERNEL is required")
	}
	imageRef := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_IMAGE"))
	if imageRef == "" {
		imageRef = "docker.io/library/busybox:1.36"
	}
	dir := t.TempDir()
	workspaceName := fmt.Sprintf("whv-exec-%d", time.Now().UnixNano()%1000000000)
	cliPath := filepath.Join(dir, "microagent.exe")
	guestInitPath := filepath.Join(dir, "microagent-guestinit")
	// The detached start spawns the runtime listener helper from the running
	// executable, so the smoke must drive the real CLI binary end to end.
	buildCmd(t, filepath.Join("..", ".."), cliPath, "./cmd/microagent", "", "")
	buildCmd(t, filepath.Join("..", ".."), guestInitPath, "./cmd/microagent-guestinit", "linux", "amd64")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Structured exec rides hv_sock, not the guest NIC; isolated networking
	// keeps the smoke independent of host HNS state and privileges.
	workspaceOpts := workspace.Options{
		Name:            workspaceName,
		Backend:         vmkit.BackendWindowsHyperV,
		Architecture:    "amd64",
		StateDir:        dir,
		KernelPath:      kernelPath,
		GuestInitPath:   guestInitPath,
		ImageRef:        imageRef,
		ServiceCommand:  "sleep 120",
		PrepareForStart: true,
		MemoryMiB:       512,
		CPUCount:        2,
		Network:         vmkit.NetworkConfig{Mode: "isolated"},
	}
	if _, err := workspace.BuildRootfs(ctx, workspaceOpts); err != nil {
		t.Fatalf("BuildRootfs: %v", err)
	}
	if err := workspace.WriteManifest(workspaceOpts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = runExternalOutput(cleanupCtx, cliPath, "stop", workspaceName, "--state-dir", dir)
		_, _ = runExternalOutput(cleanupCtx, cliPath, "delete", workspaceName, "--state-dir", dir, "--yes")
	})
	runExternal(t, ctx, cliPath, "start", workspaceName, "--state-dir", dir, "--kernel", kernelPath)
	waitForWorkspaceState(t, dir, workspaceName, vmkit.StateRunning, 30*time.Second)

	// Buffered exec round-trips both output streams and a zero exit.
	out := runExternal(t, ctx, cliPath, "exec", workspaceName, "--state-dir", dir, "--", "sh", "-c", "echo EXEC_SMOKE_STDOUT; echo EXEC_SMOKE_STDERR >&2")
	if !strings.Contains(string(out), "EXEC_SMOKE_STDOUT") || !strings.Contains(string(out), "EXEC_SMOKE_STDERR") {
		logWindowsHyperVSmokeState(t, dir, workspaceName)
		t.Fatalf("exec output = %q", out)
	}

	// Streamed exec delivers all stdout lines in order.
	streamOut := runExternal(t, ctx, cliPath, "exec", workspaceName, "--state-dir", dir, "--stream", "--", "sh", "-c", "for i in 1 2 3 4 5; do echo line-$i; done")
	lines := []string{}
	for _, line := range strings.Split(string(streamOut), "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "line-") {
			lines = append(lines, trimmed)
		}
	}
	if strings.Join(lines, " ") != "line-1 line-2 line-3 line-4 line-5" {
		logWindowsHyperVSmokeState(t, dir, workspaceName)
		t.Fatalf("streamed lines = %v, want line-1..line-5 in order", lines)
	}

	// A non-zero guest exit propagates as the CLI exit code.
	_, err := runExternalOutput(ctx, cliPath, "exec", workspaceName, "--state-dir", dir, "--", "sh", "-c", "exit 7")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		logWindowsHyperVSmokeState(t, dir, workspaceName)
		t.Fatalf("non-zero exec err = %v, want exit code 7", err)
	}

	// Readiness reports the exec and shell channels answering, not just HCS start.
	statusOut := runExternal(t, ctx, cliPath, "status", workspaceName, "--state-dir", dir)
	var status struct {
		Readiness struct {
			ShellReady struct {
				Ready bool `json:"ready"`
			} `json:"shellReady"`
			ExecReady struct {
				Ready  bool   `json:"ready"`
				Detail string `json:"detail"`
			} `json:"execReady"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal(statusOut, &status); err != nil {
		t.Fatalf("parse status: %v\n%s", err, statusOut)
	}
	if !status.Readiness.ExecReady.Ready || !strings.Contains(status.Readiness.ExecReady.Detail, "round-trip ready") {
		logWindowsHyperVSmokeState(t, dir, workspaceName)
		t.Fatalf("exec readiness = %+v, want channel-signaled ready", status.Readiness.ExecReady)
	}
	if !status.Readiness.ShellReady.Ready {
		logWindowsHyperVSmokeState(t, dir, workspaceName)
		t.Fatalf("shell readiness = %+v, want hv_sock probe ready", status.Readiness.ShellReady)
	}
}

func buildCmd(t *testing.T, workdir, output, pkg, goos, goarch string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", output, pkg)
	cmd.Dir = workdir
	cmd.Env = os.Environ()
	if goos != "" {
		cmd.Env = append(cmd.Env, "GOOS="+goos)
	}
	if goarch != "" {
		cmd.Env = append(cmd.Env, "GOARCH="+goarch)
	}
	if goos == "linux" {
		cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
}

func buildMediationProbe(t *testing.T, dir, output string) {
	t.Helper()
	source := filepath.Join(dir, "mediation-probe.go")
	code := `package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	port := uint32(2048)
	if raw := strings.TrimSpace(os.Getenv("MEDIATION_PORT")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid MEDIATION_PORT: %v\n", err)
			os.Exit(2)
		}
		port = uint32(parsed)
	}
	fd, err := dialHostVsock(port, 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial mediation vsock: %v\n", err)
		os.Exit(1)
	}
	defer unix.Close(fd)
	file := os.NewFile(uintptr(fd), "mediation-vsock")
	if file == nil {
		fmt.Fprintln(os.Stderr, "wrap mediation fd")
		os.Exit(1)
	}
	defer file.Close()
	runtimeID := os.Getenv("MICROAGENT_RUNTIME_ID")
	if runtimeID == "" {
		runtimeID = "unknown"
	}
	if _, err := file.WriteString("{\"signal\":\"ready\",\"runtimeID\":\"" + runtimeID + "\"}\n"); err != nil {
		fmt.Fprintf(os.Stderr, "write mediation message: %v\n", err)
		os.Exit(1)
	}
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "read mediation response: %v\n", err)
		os.Exit(1)
	}
	if !strings.Contains(line, "\"ok\":true") {
		fmt.Fprintf(os.Stderr, "unexpected response: %s\n", line)
		os.Exit(1)
	}
	fmt.Println("MEDIATION_OK")
}

func dialHostVsock(port uint32, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		if err == nil {
			err = unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port})
			if err == nil {
				return fd, nil
			}
			_ = unix.Close(fd)
		}
		lastErr = err
		if time.Now().After(deadline) {
			return -1, lastErr
		}
		time.Sleep(100 * time.Millisecond)
	}
}
`
	if err := os.WriteFile(source, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	buildCmd(t, filepath.Join("..", ".."), output, source, "linux", "amd64")
}

func runExternal(t *testing.T, ctx context.Context, exe string, args ...string) []byte {
	t.Helper()
	out, err := runExternalOutput(ctx, exe, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", exe, strings.Join(args, " "), err, out)
	}
	return out
}

func runExternalOutput(ctx context.Context, exe string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	return cmd.CombinedOutput()
}

func logWindowsHyperVSmokeState(t *testing.T, stateDir, name string) {
	t.Helper()
	for _, file := range []string{"serial.log", "hvsock-listener.log", "runtime.json"} {
		if data, readErr := os.ReadFile(filepath.Join(stateDir, name, file)); readErr == nil {
			t.Logf("%s:\n%s", file, data)
		}
	}
}

func waitForWorkspaceState(t *testing.T, stateDir, name string, want vmkit.VMState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		state, _, err := workspace.LatestStartState(stateDir, name)
		if err == nil && state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("workspace %s did not reach %s before timeout; last state=%s err=%v", name, want, state, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestCopyConsoleInputNormalizesNewlines(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo ready\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("echo ready\r")) || dst.String() != "echo ready\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputStripsBracketedPasteMarkers(t *testing.T) {
	var dst bytes.Buffer
	input := "\x1b[200~hostname -I\x1b[201~\n"
	written, err := copyConsoleInput(&dst, strings.NewReader(input))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("hostname -I\r")) || dst.String() != "hostname -I\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputDetachesOnCtrlBracket(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo before\n"+string([]byte{consoleDetachByte})+"echo after\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("echo before\r")) || dst.String() != "echo before\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputDetachesOnCtrlPCtrlQ(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo before\n"+string([]byte{consoleDetachPrefix, consoleDetachSuffix})+"echo after\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("echo before\r")) || dst.String() != "echo before\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputKeepsCtrlPWithoutCtrlQ(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo "+string([]byte{consoleDetachPrefix, 'x'})+"\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	want := "echo " + string([]byte{consoleDetachPrefix, 'x'}) + "\r"
	if written != int64(len(want)) || dst.String() != want {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyShellInputPreservesCarriageReturns(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyShellInput(&dst, strings.NewReader("echo ready\r"))
	if err != nil {
		t.Fatalf("copyShellInput: %v", err)
	}
	if written != int64(len("echo ready\r")) || dst.String() != "echo ready\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyShellInputDetachesOnCtrlPCtrlQ(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyShellInput(&dst, strings.NewReader("echo before\n"+string([]byte{consoleDetachPrefix, consoleDetachSuffix})+"echo after\n"))
	if err != nil {
		t.Fatalf("copyShellInput: %v", err)
	}
	if written != int64(len("echo before\n")) || dst.String() != "echo before\n" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestDataAfterOffsetIgnoresOldConsoleMarkers(t *testing.T) {
	data := []byte("old marker\nnew marker\n")
	got := dataAfterOffset(data, int64(len(data)), int64(len("old marker\n")))
	if string(got) != "new marker\n" {
		t.Fatalf("dataAfterOffset = %q", got)
	}
}

func TestRunPSListsWorkspaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	eventDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := vmkit.Event{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		State:      vmkit.StateStopped,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", RestartPolicy: "on-failure", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "ps.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPS([]string{"--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPS: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"name": "research"`) || !strings.Contains(string(got), `"state": "stopped"`) || !strings.Contains(string(got), `"restart": "on-failure"`) {
		t.Fatalf("ps output = %s", got)
	}
}

func TestRunPSCanPrintHumanOutput(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "text")
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := vmkit.Event{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		State:      vmkit.StateStopped,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "ps.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPS([]string{"--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPS: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "NAME") || !strings.Contains(string(got), "research") || strings.Contains(string(got), `"workspaces"`) {
		t.Fatalf("ps human output = %s", got)
	}
}

func TestRunLogsPrintsSerialLog(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "serial.log"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "logs.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runLogs(context.Background(), []string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("logs = %q", got)
	}
}

func TestWriteSerialTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serial.log")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	n, err := writeSerialTail(path, 3, out)
	if err != nil {
		t.Fatalf("writeSerialTail: %v", err)
	}
	if n != 3 {
		t.Fatalf("wrote %d bytes, want 3", n)
	}
	// A not-yet-created serial log reads as empty, not an error.
	missing, err := writeSerialTail(filepath.Join(dir, "nope.log"), 0, out)
	if err != nil || missing != 0 {
		t.Fatalf("missing file: n=%d err=%v", missing, err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "def" {
		t.Fatalf("tail = %q, want %q", got, "def")
	}
}

func TestFollowLogsExitsWhenNotRunning(t *testing.T) {
	dir := t.TempDir()
	name := "research"
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "serial.log"), []byte("boot output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- followLogs(context.Background(), dir, name, out) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followLogs: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("followLogs did not return for a workspace that is not running")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "boot output") {
		t.Fatalf("followLogs output missing serial content: %q", got)
	}
}

func TestRunEventsSnapshotAndFollow(t *testing.T) {
	dir := t.TempDir()
	name := "research"
	wsDir := filepath.Join(dir, name)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	eventsJSON := `[
	  {"identity":{},"state":"running","detail":"runtime is started","observedAt":"2026-06-01T00:00:01Z"},
	  {"identity":{},"state":"halted","detail":"clean shutdown","observedAt":"2026-06-01T00:00:09Z"}
	]`
	if err := os.WriteFile(filepath.Join(wsDir, "events.json"), []byte(eventsJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	events, err := workspace.ReadEvents(dir, name)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 2 || events[0].State != vmkit.StateRunning || events[1].State != vmkit.StateHalted {
		t.Fatalf("events = %#v", events)
	}

	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- followEvents(context.Background(), dir, name, nil, out) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followEvents: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("followEvents did not return for a terminal workspace")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "running") || !strings.Contains(string(got), "halted") {
		t.Fatalf("followEvents output = %q", got)
	}
}

func TestReadEventsMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	events, err := workspace.ReadEvents(dir, "absent")
	if err != nil || events != nil {
		t.Fatalf("missing events: events=%v err=%v", events, err)
	}
	wsDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "events.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadEvents(dir, "broken"); err == nil {
		t.Fatal("expected error for malformed events.json")
	}
}

func TestHighLevelCreateDetection(t *testing.T) {
	if !hasFlagValue([]string{"--image", "ubuntu:24.04"}, "image") {
		t.Fatal("expected --image to be detected")
	}
	if !hasFlagValue([]string{"--image=ubuntu:24.04"}, "image") {
		t.Fatal("expected --image=value to be detected")
	}
	if hasFlagValue([]string{"--kernel", "/tmp/kernel"}, "image") {
		t.Fatal("did not expect image flag")
	}
	if !shouldUseHighLevelCreate([]string{"test"}) {
		t.Fatal("expected positional create name to use high-level create")
	}
	if shouldUseHighLevelCreate([]string{"--rootfs", "/tmp/rootfs.ext4", "--id", "agent-1"}) {
		t.Fatal("legacy rootfs create should not use high-level create")
	}
}

func TestParseWorkspaceOptionsAcceptsPositionalName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{"test", "--image", "docker.io/library/ubuntu:24.04"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "test" {
		t.Fatalf("Name = %q, want test", opts.Name)
	}
}

func TestFirecrackerStopTerminatesRecordedPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"stop", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run stop: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateStopped || state.PID != 0 {
		t.Fatalf("state = %#v, want stopped with no pid", state)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("process %d still active", cmd.Process.Pid)
	}
}

func TestFirecrackerHaltRecordsHaltedState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"halt", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run halt: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateHalted || state.PID != 0 {
		t.Fatalf("state = %#v, want halted with no pid", state)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("process %d still active", cmd.Process.Pid)
	}
}

func TestFirecrackerQuarantinePreservesRecordedPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"quarantine", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run quarantine: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateQuarantined || state.PID != cmd.Process.Pid {
		t.Fatalf("state = %#v, want quarantined with preserved pid", state)
	}
	if !processStillActive(cmd.Process.Pid) {
		t.Fatalf("process %d was stopped by quarantine", cmd.Process.Pid)
	}
}

func TestFirecrackerKillTerminatesRecordedPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"kill", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run kill: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateStopped || state.PID != 0 {
		t.Fatalf("state = %#v, want stopped with no pid", state)
	}
}

func TestFirecrackerDeleteStopsRunningPIDWithYes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"delete", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t), "--yes"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run delete: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "agent-1", "runtime.json")); !os.IsNotExist(statErr) {
		t.Fatalf("runtime state still exists after delete: %v", statErr)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("running process still exists after delete")
	}
}

func TestFirecrackerLegacyCreatePreparesStateLocally(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	kernel := filepath.Join(dir, "Image")
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(kernel, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfs, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"create",
		"--rootfs", rootfs,
		"--kernel", kernel,
		"--id", "agent-1",
		"--state-dir", dir,
		"--vsock", "1024=127.0.0.1:8200",
		"--supervisor", firecrackerSupervisorHelper(t),
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "agent-1", "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) || !strings.Contains(string(data), `"backend": "firecracker"`) {
		t.Fatalf("runtime state = %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-1", "firecracker.json")); err != nil {
		t.Fatalf("firecracker config missing: %v", err)
	}
	configData, err := os.ReadFile(filepath.Join(dir, "agent-1", "firecracker.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Vsock *struct {
			VsockID  string `json:"vsock_id"`
			GuestCID uint32 `json:"guest_cid"`
			UDSPath  string `json:"uds_path"`
		} `json:"vsock"`
	}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if config.Vsock == nil || config.Vsock.VsockID != "vsock0" || config.Vsock.GuestCID < 3 || config.Vsock.UDSPath == "" {
		t.Fatalf("firecracker config missing vsock: %s", configData)
	}
}

func TestFirecrackerStatusReadsPreparedState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor lifecycle tests require linux")
	}
	dir := t.TempDir()
	req := vmkit.Request{
		Command: "prepare",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if _, err := (firecrackersupervisor.Supervisor{Options: firecrackersupervisor.Options{Name: "research", StateDir: dir}}).Do(t.Context(), req); err != nil {
		t.Fatalf("firecracker prepare: %v", err)
	}
	stdout, err := os.Create(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) || !strings.Contains(string(data), `"backend": "firecracker"`) {
		t.Fatalf("status output = %s", data)
	}
}

func testFirecrackerRuntimeState(t *testing.T, dir, name string, state vmkit.VMState, pid int) vmkit.Request {
	t.Helper()
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: name}, req, state, pid, ""); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestKernelInstallFromLocalAndVerify(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Image")
	if err := os.WriteFile(src, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("kernel")))
	out := filepath.Join(dir, "kernels", "Image")
	stdoutPath := filepath.Join(dir, "install.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runKernelInstall(t.Context(), []string{"--from", src, "--sha256", sum, "--out", out}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runKernelInstall: %v", err)
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "kernel" {
		t.Fatalf("installed kernel = %q, %v", data, err)
	}
	verifyOut, err := os.Create(filepath.Join(dir, "verify.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = runKernelVerify([]string{"--path", out, "--sha256", sum}, verifyOut)
	if closeErr := verifyOut.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runKernelVerify: %v", err)
	}
}

func TestDefaultKernelManifestHasAppleVFArm64(t *testing.T) {
	kernel, ok := defaultKernel(vmkit.BackendAppleVF, "arm64")
	if !ok {
		t.Fatal("missing apple-vf arm64 kernel")
	}
	if kernel.URL == "" || kernel.SHA256 == "" {
		t.Fatalf("kernel = %#v", kernel)
	}
}

func TestDefaultKernelManifestHasFirecrackerAMD64(t *testing.T) {
	kernel, ok := defaultKernel(vmkit.BackendFirecracker, "amd64")
	if !ok {
		t.Fatal("missing firecracker amd64 kernel")
	}
	if kernel.URL != "https://github.com/geoffbelknap/microagent-kernels/releases/download/kernels-6.1.155-r2/microagent-kernel-6.1.155-firecracker-amd64" {
		t.Fatalf("url = %q", kernel.URL)
	}
	if kernel.SHA256 != "4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0" {
		t.Fatalf("sha256 = %q", kernel.SHA256)
	}
}

func TestDefaultKernelManifestHasFirecrackerARM64(t *testing.T) {
	kernel, ok := defaultKernel(vmkit.BackendFirecracker, "arm64")
	if !ok {
		t.Fatal("missing firecracker arm64 kernel")
	}
	if kernel.URL != "https://github.com/geoffbelknap/microagent-kernels/releases/download/kernels-6.1.155-r3/microagent-kernel-6.1.155-firecracker-arm64" {
		t.Fatalf("url = %q", kernel.URL)
	}
	if kernel.SHA256 != "bd91c4f5c15e497b99ac0c96977a92e68a0c11d3c72267104f5fb968994c4a71" {
		t.Fatalf("sha256 = %q", kernel.SHA256)
	}
}

func TestDefaultKernelManifestHasWindowsHyperVAMD64(t *testing.T) {
	kernel, ok := defaultKernel(vmkit.BackendWindowsHyperV, "amd64")
	if !ok {
		t.Fatal("missing windows-hyperv amd64 kernel")
	}
	if kernel.URL != "https://github.com/geoffbelknap/microagent-kernels/releases/download/kernels-6.12.22-r1/microagent-kernel-6.12.22-windows-hyperv-amd64" {
		t.Fatalf("url = %q", kernel.URL)
	}
	if kernel.SHA256 != "8623b349a95fa536891e0d292d198396504aad8308c7619083994f7553707a92" {
		t.Fatalf("sha256 = %q", kernel.SHA256)
	}
}

func TestDefaultKernelSupportReportsDownloadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Image")
	originalDefaults := defaultKernels
	defaultKernels = []kernelManifestEntry{
		{
			Backend:      vmkit.BackendAppleVF,
			Architecture: "arm64",
			URL:          "https://example.com/kernel",
			SHA256:       "abc123",
		},
	}
	t.Cleanup(func() {
		defaultKernels = originalDefaults
	})

	support := defaultKernelSupportForPath(vmkit.BackendAppleVF, "arm64", path)
	if support.Status != "downloadable" {
		t.Fatalf("status = %q, want downloadable", support.Status)
	}
	if support.SHA256 != "abc123" {
		t.Fatalf("sha256 = %q, want abc123", support.SHA256)
	}
}

func TestEnsureWorkspaceKernelSkipsExplicitKernel(t *testing.T) {
	opts := workspaceOptions{
		Backend:        vmkit.BackendAppleVF,
		Architecture:   "arm64",
		KernelPath:     filepath.Join(t.TempDir(), "missing"),
		KernelExplicit: true,
	}
	if err := ensureWorkspaceKernel(t.Context(), &opts); err != nil {
		t.Fatalf("ensureWorkspaceKernel: %v", err)
	}
}

func TestEnsureWorkspaceKernelInstallsDefaultKernel(t *testing.T) {
	kernelBytes := []byte("test kernel")
	sum := sha256.Sum256(kernelBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(kernelBytes)
	}))
	t.Cleanup(server.Close)

	originalDefaults := defaultKernels
	defaultKernels = []kernelManifestEntry{
		{
			Backend:      vmkit.BackendAppleVF,
			Architecture: "arm64",
			URL:          server.URL,
			SHA256:       fmt.Sprintf("%x", sum),
		},
	}
	t.Cleanup(func() {
		defaultKernels = originalDefaults
	})

	opts := workspaceOptions{
		Backend:      vmkit.BackendAppleVF,
		Architecture: "arm64",
		KernelPath:   filepath.Join(t.TempDir(), "Image"),
	}
	if err := ensureWorkspaceKernel(t.Context(), &opts); err != nil {
		t.Fatalf("ensureWorkspaceKernel: %v", err)
	}
	got, err := os.ReadFile(opts.KernelPath)
	if err != nil {
		t.Fatalf("read kernel: %v", err)
	}
	if string(got) != string(kernelBytes) {
		t.Fatalf("kernel bytes = %q, want %q", got, kernelBytes)
	}
}

func TestFirecrackerGuestHaltedDetectsKernelShutdown(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor is linux-only")
	}
	dir := t.TempDir()
	serialPath := filepath.Join(dir, "serial.log")
	if firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("missing serial log reported halted")
	}
	if err := os.WriteFile(serialPath, []byte("[ 1.0 ] reboot: System halted\n"), 0o644); err != nil {
		t.Fatalf("write serial log: %v", err)
	}
	if !firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("system halt was not detected")
	}
	if err := os.WriteFile(serialPath, []byte("[ 1.0 ] reboot: Power down\n"), 0o644); err != nil {
		t.Fatalf("write serial log: %v", err)
	}
	if !firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("power down was not detected")
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func contractItemSliceContains(items []vmkit.ContractItem, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func startCommandExecServer(t *testing.T, handle func(execprotocol.ExecRequest) execprotocol.ExecResult) (string, uint16, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					t.Errorf("exec server accept: %v", err)
				}
				return
			}
			go func() {
				defer conn.Close()
				var req execprotocol.ExecRequest
				if err := execprotocol.DecodeMessage(conn, &req); err != nil {
					t.Errorf("decode exec request: %v", err)
					return
				}
				if strings.Join(req.Argv, " ") == "true" {
					code := 0
					result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
					result.ExitCode = &code
					_ = execprotocol.EncodeMessage(conn, result)
					return
				}
				if err := execprotocol.EncodeMessage(conn, handle(req)); err != nil {
					t.Errorf("encode exec result: %v", err)
				}
			}()
		}
	}()
	return listener.Addr().String(), uint16(portValue), func() {
		_ = listener.Close()
		<-done
	}
}

func writeCommandExecRuntimeState(t *testing.T, name string, state vmkit.VMState, execPort uint16) string {
	t.Helper()
	dir := t.TempDir()
	opts := workspace.Options{Name: name, StateDir: dir, Backend: vmkit.BackendFirecracker, ExecPort: execPort}
	req := workspace.Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err := workspace.WriteProcessState(opts, req, state, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	return dir
}

func unusedTCPPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(portValue)
}

func TestWriteDoctorResponseTextIncludesNetworkingSection(t *testing.T) {
	oldOutput := outputFormat
	t.Cleanup(func() { outputFormat = oldOutput })
	outputFormat = "text"
	f, err := os.CreateTemp(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	resp := vmkit.Response{
		OK:      true,
		Backend: "firecracker",
		Host: &vmkit.HostSupport{
			Backend:                "firecracker",
			Architecture:           "amd64",
			IPForwardEnabled:       true,
			IsolatedNetworkReady:   true,
			UserNetworkReady:       true,
			PrivilegedNetworkReady: false, // cap missing
		},
	}
	if err := writeDoctorResponse(f, resp); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Name())
	out := string(data)
	if !strings.Contains(out, "Networking:") {
		t.Errorf("expected a Networking section, got:\n%s", out)
	}
	if !strings.Contains(out, "setup-networking") {
		t.Errorf("expected remediation hint, got:\n%s", out)
	}
}
