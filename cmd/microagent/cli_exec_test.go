package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestStructuredExecRequiresSeparator(t *testing.T) {
	err := run(t.Context(), []string{"exec", "research", "echo hello"}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "usage: microagent exec") {
		t.Fatalf("err = %v, want exec usage", err)
	}
}

func TestStructuredExecWritesSeparatedStreamsAndCommandExit(t *testing.T) {
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

func TestStructuredExecTruncationWarningsAndStatusExitCodes(t *testing.T) {
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

func TestStructuredExecJSONWritesTypedResult(t *testing.T) {
	oldFormat := outputFormat
	t.Cleanup(func() {
		outputFormat = oldFormat
	})
	outputFormat = "json"
	_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		code := 0
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
	if err := runStructuredExec(t.Context(), []string{"research", "--state-dir", stateDir, "--", "echo", "out"}, stdout, &stderr); err != nil {
		t.Fatalf("runStructuredExec: %v", err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var result execprotocol.ExecResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode JSON result %q: %v", data, err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || string(result.Stdout) != "out\n" {
		t.Fatalf("result = %#v", result)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
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
		t.Run(tt.name, func(t *testing.T) {
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
	}
}

// TestExecUsesStreamingPath guards that JSON output remains buffered.
func TestExecUsesStreamingPath(t *testing.T) {
	tests := []struct {
		name          string
		streamRequest bool
		structured    bool
		wantStreaming bool
	}{
		{name: "ux no stream", streamRequest: false, structured: false, wantStreaming: false},
		{name: "text stream", streamRequest: true, structured: false, wantStreaming: true},
		{name: "json stream requested but forced buffered", streamRequest: true, structured: true, wantStreaming: false},
		{name: "json no stream", streamRequest: false, structured: true, wantStreaming: false},
		{name: "text no stream", streamRequest: false, structured: false, wantStreaming: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := execUsesStreamingPath(tt.streamRequest, tt.structured); got != tt.wantStreaming {
				t.Fatalf("execUsesStreamingPath(%v, %v) = %v, want %v", tt.streamRequest, tt.structured, got, tt.wantStreaming)
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
