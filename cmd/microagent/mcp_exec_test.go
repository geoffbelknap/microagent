package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestMCPWorkspaceExecReturnsStructuredResult(t *testing.T) {
	seen := make(chan execprotocol.ExecRequest, 1)
	_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		seen <- req
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stdout = []byte("Linux demo\n")
		return result
	})
	defer stop()
	stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, port)
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name":      "research",
		"state_dir": stateDir,
		"argv":      []any{"uname", "-a"},
		"env":       map[string]any{"TEST_VAR": "hello"},
		"cwd":       "/tmp",
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	result, ok := envelope["result"].(execprotocol.ExecResult)
	if !ok {
		t.Fatalf("result type = %T", envelope["result"])
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || string(result.Stdout) != "Linux demo\n" || result.Status != execprotocol.ExecStatusExited {
		t.Fatalf("result = %#v", result)
	}
	req := <-seen
	if strings.Join(req.Argv, " ") != "uname -a" || req.Env["TEST_VAR"] != "hello" || req.Cwd != "/tmp" {
		t.Fatalf("request = %#v", req)
	}
	meta := mcpEnvelopeMeta(t, envelope)
	if meta["retry_count"] != 0 {
		t.Fatalf("meta.retry_count = %#v, want 0", meta["retry_count"])
	}
	if meta["retry_wall_clock_ms"] != int64(0) {
		t.Fatalf("meta.retry_wall_clock_ms = %#v, want 0", meta["retry_wall_clock_ms"])
	}
}

// mcpEnvelopeMeta returns the transport meta block of an MCP tool envelope.
func mcpEnvelopeMeta(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	meta, ok := envelope["meta"].(map[string]any)
	if !ok {
		t.Fatalf("envelope meta type = %T (%#v)", envelope["meta"], envelope)
	}
	return meta
}

func TestMCPWorkspaceExecRetriesTCPReset(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("Linux demo\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"uname", "-a"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 1 {
		t.Fatalf("meta.retry_count = %#v, want 1", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
	result := envelope["result"].(execprotocol.ExecResult)
	if string(result.Stdout) != "Linux demo\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func TestMCPWorkspaceExecRetriesConnectionRefused(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("ok\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 1 {
		t.Fatalf("meta.retry_count = %#v, want 1", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func TestMCPWorkspaceExecRetriesConnectionTimeout(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("ok\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 1 {
		t.Fatalf("meta.retry_count = %#v, want 1", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func TestMCPWorkspaceExecRetryExhaustionReturnsStructuredError(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		err := workspace.ExecRetryExhaustedError{Retries: 3, WallClock: time.Millisecond, LastErr: execclient.UnreachableError{Addr: "127.0.0.1:45000", Err: syscall.ECONNREFUSED}}
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{Count: 3, WallClock: time.Millisecond, Exhausted: true}, err
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want retry-exhausted error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	meta := mcpEnvelopeMeta(t, envelope)
	if meta["retry_count"] != 3 {
		t.Fatalf("meta.retry_count = %#v, want 3", meta["retry_count"])
	}
	if meta["retry_exhausted"] != true {
		t.Fatalf("meta.retry_exhausted = %#v, want true", meta["retry_exhausted"])
	}
	structured, ok := envelope["error"].(structuredError)
	if !ok {
		t.Fatalf("error type = %T", envelope["error"])
	}
	if structured.Kind != errorKindTransient {
		t.Fatalf("kind = %q, want transient", structured.Kind)
	}
	if !structured.Retryable {
		t.Fatalf("retryable = false, want true")
	}
	if !strings.Contains(structured.Message, "persisted after 3 retries") {
		t.Fatalf("message = %q, want retry exhaustion detail", structured.Message)
	}
}

func TestMCPWorkspaceExecRetryExhaustionIncludesErrorEnvelopeMetadata(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		err := workspace.ExecRetryExhaustedError{Retries: 3, WallClock: time.Millisecond, LastErr: execclient.UnreachableError{Addr: "127.0.0.1:45000", Err: syscall.ECONNREFUSED}}
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{Count: 3, WallClock: time.Millisecond, Exhausted: true}, err
	})
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "workspace.exec",
			"arguments": map[string]any{
				"name": "research",
				"argv": []string{"true"},
			},
		},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %#v, want JSON-RPC error", responses[0])
	}
	data, ok := errObj["data"].(map[string]any)
	if !ok {
		t.Fatalf("error data = %#v", errObj["data"])
	}
	if data["kind"] != string(errorKindTransient) || data["retryable"] != true {
		t.Fatalf("error data classification = %#v", data)
	}
	meta, ok := data["meta"].(map[string]any)
	if !ok {
		t.Fatalf("error data meta = %#v", data["meta"])
	}
	if meta["retry_count"] != float64(3) {
		t.Fatalf("meta.retry_count = %#v, want 3", meta["retry_count"])
	}
	if meta["retry_exhausted"] != true {
		t.Fatalf("meta.retry_exhausted = %#v, want true", meta["retry_exhausted"])
	}
	if _, ok := meta["retry_wall_clock_ms"].(float64); !ok {
		t.Fatalf("meta.retry_wall_clock_ms missing or non-number: %#v", meta)
	}
}

func TestMCPWorkspaceExecDoesNotRetryExecCompletedErrors(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		code := 127
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stderr = []byte("command not found\n")
		return result, workspace.ExecRetryMetadata{}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"missing-command"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 0 {
		t.Fatalf("meta.retry_count = %#v, want 0", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func TestMCPWorkspaceExecDoesNotRetryWorkspaceNotRunning(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{}, fmt.Errorf("workspace research is not running; structured exec is unavailable in state stopped")
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want workspace-not-running error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if mcpEnvelopeMeta(t, envelope)["retry_count"] != 0 {
		t.Fatalf("meta.retry_count = %#v, want 0", mcpEnvelopeMeta(t, envelope)["retry_count"])
	}
}

func stubMCPWorkspaceExec(t *testing.T, fn func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error)) {
	t.Helper()
	originalExec := mcpWorkspaceExec
	mcpWorkspaceExec = fn
	t.Cleanup(func() {
		mcpWorkspaceExec = originalExec
	})
}

func successfulMCPExecResult(stdout string) execprotocol.ExecResult {
	code := 0
	result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
	result.ExitCode = &code
	result.Stdout = []byte(stdout)
	return result
}
