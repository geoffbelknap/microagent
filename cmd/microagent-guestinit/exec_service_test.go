//go:build linux

package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestStructuredExecServiceHappyPath(t *testing.T) {
	result := roundTripStructuredExec(t, execRequest("echo", "hello"))
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if string(result.Stdout) != "hello\n" {
		t.Fatalf("stdout = %q, want hello newline", result.Stdout)
	}
	if len(result.Stderr) != 0 {
		t.Fatalf("stderr = %q, want empty", result.Stderr)
	}
}

func TestStructuredExecServiceDefaultsAbsentProtocolVersion(t *testing.T) {
	req := execRequest("true")
	req.ProtocolVersion = ""
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if result.ProtocolVersion != execprotocol.CurrentProtocolVersion {
		t.Fatalf("protocol_version = %q, want %q", result.ProtocolVersion, execprotocol.CurrentProtocolVersion)
	}
}

func TestStructuredExecServiceSeparatesStdoutAndStderr(t *testing.T) {
	result := roundTripStructuredExec(t, execRequest("sh", "-c", "echo out; echo err >&2"))
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if string(result.Stdout) != "out\n" {
		t.Fatalf("stdout = %q, want out newline", result.Stdout)
	}
	if string(result.Stderr) != "err\n" {
		t.Fatalf("stderr = %q, want err newline", result.Stderr)
	}
}

func TestStructuredExecServiceNonzeroExitIsStructuredResult(t *testing.T) {
	result := roundTripStructuredExec(t, execRequest("sh", "-c", "exit 7"))
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 7)
	if result.Error != nil {
		t.Fatalf("error = %#v, want nil", result.Error)
	}
}

func TestStructuredExecServiceMissingExecutable(t *testing.T) {
	result := roundTripStructuredExec(t, execRequest("does-not-exist-xyz"))
	assertExecStatus(t, result, execprotocol.ExecStatusFailedToStart)
	if result.ExitCode != nil {
		t.Fatalf("exit_code = %d, want nil", *result.ExitCode)
	}
	if result.Error == nil || result.Error.Code != "failed_to_start" {
		t.Fatalf("error = %#v, want failed_to_start", result.Error)
	}
}

func TestStructuredExecServiceAppliesEnv(t *testing.T) {
	req := execRequest("sh", "-c", "echo $TEST_VAR")
	req.Env = map[string]string{"TEST_VAR": "hello"}
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if string(result.Stdout) != "hello\n" {
		t.Fatalf("stdout = %q, want hello newline", result.Stdout)
	}
}

func TestStructuredExecServiceAppliesCwd(t *testing.T) {
	req := execRequest("pwd")
	req.Cwd = "/tmp"
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if strings.TrimSpace(string(result.Stdout)) != "/tmp" {
		t.Fatalf("stdout = %q, want /tmp", result.Stdout)
	}
}

func TestStructuredExecServiceDeliversStdin(t *testing.T) {
	req := execRequest("cat")
	req.Stdin = []byte("input bytes")
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if !bytes.Equal(result.Stdout, req.Stdin) {
		t.Fatalf("stdout = %q, want stdin %q", result.Stdout, req.Stdin)
	}
}

func TestStructuredExecServiceTimeout(t *testing.T) {
	req := execRequest("sleep", "5")
	req.TimeoutMS = 100
	start := time.Now()
	result := roundTripStructuredExec(t, req)
	elapsed := time.Since(start)
	assertExecStatus(t, result, execprotocol.ExecStatusTimedOut)
	if result.ExitCode != nil {
		t.Fatalf("exit_code = %d, want nil", *result.ExitCode)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout response took %s, want under 2s", elapsed)
	}
}

func TestStructuredExecServiceStdoutLimit(t *testing.T) {
	req := execRequest("sh", "-c", "printf 1234567890")
	req.OutputLimitBytesStdout = 4
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if string(result.Stdout) != "1234" {
		t.Fatalf("stdout = %q, want 1234", result.Stdout)
	}
	if !result.StdoutTruncated {
		t.Fatal("stdout_truncated = false, want true")
	}
}

func TestStructuredExecServiceStderrLimit(t *testing.T) {
	req := execRequest("sh", "-c", "printf 1234567890 >&2")
	req.OutputLimitBytesStderr = 4
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	if string(result.Stderr) != "1234" {
		t.Fatalf("stderr = %q, want 1234", result.Stderr)
	}
	if !result.StderrTruncated {
		t.Fatal("stderr_truncated = false, want true")
	}
}

func TestStructuredExecServiceProcessContinuesAfterStdoutLimit(t *testing.T) {
	req := execRequest("sh", "-c", "printf 1234567890; exit 7")
	req.OutputLimitBytesStdout = 4
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 7)
	if string(result.Stdout) != "1234" {
		t.Fatalf("stdout = %q, want 1234", result.Stdout)
	}
	if !result.StdoutTruncated {
		t.Fatal("stdout_truncated = false, want true")
	}
}

func TestStructuredExecServiceBinarySafety(t *testing.T) {
	req := execRequest("sh", "-c", "printf '\\000\\001\\177\\377'")
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusExited)
	assertExitCode(t, result, 0)
	want := []byte{0, 1, 127, 255}
	if !bytes.Equal(result.Stdout, want) {
		t.Fatalf("stdout bytes = %#v, want %#v", result.Stdout, want)
	}
}

func TestStructuredExecServiceRejectsInvalidRequest(t *testing.T) {
	req := execprotocol.NewExecRequest(nil)
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusFailedToStart)
	if result.Error == nil || result.Error.Code != "invalid_request" {
		t.Fatalf("error = %#v, want invalid_request", result.Error)
	}
}

func TestStructuredExecServiceRejectsUnsupportedMode(t *testing.T) {
	req := execRequest("true")
	req.Mode = execprotocol.ExecModeStream
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusFailedToStart)
	if result.Error == nil || result.Error.Code != "unsupported_mode" {
		t.Fatalf("error = %#v, want unsupported_mode", result.Error)
	}
}

func TestStructuredExecServiceRejectsUnsupportedVersion(t *testing.T) {
	req := execRequest("true")
	req.ProtocolVersion = "exec.v2"
	result := roundTripStructuredExec(t, req)
	assertExecStatus(t, result, execprotocol.ExecStatusFailedToStart)
	if result.Error == nil || result.Error.Code != "unsupported_protocol_version" {
		t.Fatalf("error = %#v, want unsupported_protocol_version", result.Error)
	}
}

func TestBoundedExecBufferPreservesPrefixAndReportsTruncation(t *testing.T) {
	buf := newBoundedExecBuffer(4)
	if n, err := buf.Write([]byte("12")); n != 2 || err != nil {
		t.Fatalf("first Write = (%d, %v), want (2, nil)", n, err)
	}
	if n, err := buf.Write([]byte("345")); n != 3 || err != nil {
		t.Fatalf("second Write = (%d, %v), want (3, nil)", n, err)
	}
	if got := string(buf.Bytes()); got != "1234" {
		t.Fatalf("buffer = %q, want 1234", got)
	}
	if !buf.Truncated() {
		t.Fatal("Truncated = false, want true")
	}
}

func TestBoundedExecBufferExactLimitDoesNotTruncate(t *testing.T) {
	buf := newBoundedExecBuffer(4)
	if _, err := buf.Write([]byte("1234")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.Truncated() {
		t.Fatal("Truncated = true, want false")
	}
}

func TestMergeExecEnvOverridesBaseAndSorts(t *testing.T) {
	got := mergeExecEnv([]string{"B=base", "A=base", "BAD"}, map[string]string{"B": "override", "C": "extra"})
	want := []string{"A=base", "B=override", "C=extra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %#v, want %#v", got, want)
	}
}

func roundTripStructuredExec(t *testing.T, req execprotocol.ExecRequest) execprotocol.ExecResult {
	t.Helper()
	client, server := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		handleStructuredExecConnection(server, structuredExecService{
			env:              baseTestEnv(),
			terminationGrace: 25 * time.Millisecond,
			now:              time.Now,
		})
		errCh <- nil
	}()
	if err := execprotocol.EncodeMessage(client, req); err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	var result execprotocol.ExecResult
	if err := execprotocol.DecodeMessage(client, &result); err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("close client: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server: %v", err)
	}
	if result.Stdout == nil {
		t.Fatal("stdout = nil, want empty or populated slice")
	}
	if result.Stderr == nil {
		t.Fatal("stderr = nil, want empty or populated slice")
	}
	return result
}

func execRequest(argv ...string) execprotocol.ExecRequest {
	req := execprotocol.NewExecRequest(argv)
	req.ProtocolVersion = execprotocol.CurrentProtocolVersion
	req.Mode = execprotocol.ExecModeSingleResponse
	return req
}

func baseTestEnv() []string {
	env := os.Environ()
	if !slices.ContainsFunc(env, func(entry string) bool {
		return strings.HasPrefix(entry, "PATH=")
	}) {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	return env
}

func assertExecStatus(t *testing.T, result execprotocol.ExecResult, want execprotocol.ExecStatus) {
	t.Helper()
	if result.Status != want {
		t.Fatalf("status = %q, want %q; error=%#v stdout=%q stderr=%q", result.Status, want, result.Error, result.Stdout, result.Stderr)
	}
}

func assertExitCode(t *testing.T, result execprotocol.ExecResult, want int) {
	t.Helper()
	if result.ExitCode == nil {
		t.Fatalf("exit_code = nil, want %d; error=%#v stderr=%q", want, result.Error, result.Stderr)
	}
	if *result.ExitCode != want {
		t.Fatalf("exit_code = %d, want %d; error=%#v stderr=%q", *result.ExitCode, want, result.Error, result.Stderr)
	}
}
