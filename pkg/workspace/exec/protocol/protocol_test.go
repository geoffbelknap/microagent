package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestExecRequestJSONRoundTrip(t *testing.T) {
	req := ExecRequest{
		ProtocolVersion:        CurrentProtocolVersion,
		Mode:                   ExecModeSingleResponse,
		Argv:                   []string{"/bin/sh", "-lc", "cat"},
		Env:                    map[string]string{"A": "B"},
		Cwd:                    "/work",
		Stdin:                  []byte("hello"),
		TimeoutMS:              1000,
		OutputLimitBytesStdout: 1024,
		OutputLimitBytesStderr: 2048,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got ExecRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("decoded request = %#v, want %#v", got, req)
	}
}

func TestExecResultJSONRoundTripStatuses(t *testing.T) {
	exitCode := 7
	tests := []ExecResult{
		{
			ProtocolVersion: CurrentProtocolVersion,
			StartedAt:       "2026-05-22T00:00:00Z",
			CompletedAt:     "2026-05-22T00:00:01Z",
			ExitCode:        &exitCode,
			Stdout:          []byte("stdout"),
			Stderr:          []byte("stderr"),
			Status:          ExecStatusExited,
		},
		{
			ProtocolVersion: CurrentProtocolVersion,
			Stdout:          []byte("partial stdout"),
			Stderr:          []byte("partial stderr"),
			StdoutTruncated: true,
			StderrTruncated: true,
			Status:          ExecStatusSignaled,
			Error:           &ExecError{Code: "signal", Message: "terminated", Detail: "SIGTERM"},
		},
		{
			ProtocolVersion: CurrentProtocolVersion,
			Stdout:          []byte{},
			Stderr:          []byte("timeout"),
			Status:          ExecStatusTimedOut,
			Error:           &ExecError{Code: "timeout", Message: "deadline exceeded"},
		},
		{
			ProtocolVersion: CurrentProtocolVersion,
			Stdout:          []byte{},
			Stderr:          []byte{},
			Status:          ExecStatusFailedToStart,
			Error:           &ExecError{Code: "not_found", Message: "executable not found"},
		},
	}
	for _, result := range tests {
		t.Run(string(result.Status), func(t *testing.T) {
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var got ExecResult
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, result) {
				t.Fatalf("decoded result = %#v, want %#v", got, result)
			}
		})
	}
}

func TestFramingRoundTripSingleAndMultipleMessages(t *testing.T) {
	first := NewExecRequest([]string{"echo", "one"})
	second := NewExecRequest([]string{"echo", "two"})
	var stream bytes.Buffer
	if err := EncodeMessage(&stream, first); err != nil {
		t.Fatal(err)
	}
	if err := EncodeMessage(&stream, second); err != nil {
		t.Fatal(err)
	}

	var gotFirst ExecRequest
	if err := DecodeMessage(&stream, &gotFirst); err != nil {
		t.Fatal(err)
	}
	var gotSecond ExecRequest
	if err := DecodeMessage(&stream, &gotSecond); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFirst, first) {
		t.Fatalf("first = %#v, want %#v", gotFirst, first)
	}
	if !reflect.DeepEqual(gotSecond, second) {
		t.Fatalf("second = %#v, want %#v", gotSecond, second)
	}
	if err := DecodeMessage(&stream, &ExecRequest{}); !errors.Is(err, io.EOF) {
		t.Fatalf("DecodeMessage EOF err = %v, want io.EOF", err)
	}
}

func TestDecodeMessageRejectsLengthAboveMaxBeforePayloadRead(t *testing.T) {
	var stream bytes.Buffer
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], 1024)
	stream.Write(prefix[:])
	stream.Write(bytes.Repeat([]byte{0xaa}, 16))

	err := DecodeMessageWithMax(&stream, &ExecRequest{}, 10)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("DecodeMessageWithMax err = %v, want maximum error", err)
	}
	if stream.Len() != 16 {
		t.Fatalf("decoder read payload bytes; remaining = %d, want 16", stream.Len())
	}
}

func TestDefaultMaxMessageBytesIncludesJSONEncodingHeadroom(t *testing.T) {
	if DefaultMaxMessageBytes != 4*DefaultOutputLimitBytes {
		t.Fatalf("DefaultMaxMessageBytes = %d, want %d", DefaultMaxMessageBytes, 4*DefaultOutputLimitBytes)
	}
}

func TestExecStatusValidation(t *testing.T) {
	for _, status := range []ExecStatus{ExecStatusExited, ExecStatusSignaled, ExecStatusTimedOut, ExecStatusFailedToStart} {
		if err := status.Validate(); err != nil {
			t.Fatalf("%q Validate: %v", status, err)
		}
		data, _ := json.Marshal(status)
		var decoded ExecStatus
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("%q UnmarshalJSON: %v", status, err)
		}
	}
	if err := ExecStatus("bad").Validate(); err == nil {
		t.Fatal("bad status Validate error = nil")
	}
	var decoded ExecStatus
	if err := json.Unmarshal([]byte(`"bad"`), &decoded); err == nil {
		t.Fatal("bad status UnmarshalJSON error = nil")
	}
}

func TestExecModeValidation(t *testing.T) {
	for _, mode := range []ExecMode{ExecModeSingleResponse, ExecModeStream} {
		if err := mode.Validate(); err != nil {
			t.Fatalf("%q Validate: %v", mode, err)
		}
		data, _ := json.Marshal(mode)
		var decoded ExecMode
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("%q UnmarshalJSON: %v", mode, err)
		}
	}
	if err := ExecMode("bad").Validate(); err == nil {
		t.Fatal("bad mode Validate error = nil")
	}
	var decoded ExecMode
	if err := json.Unmarshal([]byte(`"bad"`), &decoded); err == nil {
		t.Fatal("bad mode UnmarshalJSON error = nil")
	}
}

func TestExecStreamMessageRoundTripAndValidate(t *testing.T) {
	chunk := NewExecStreamChunk(ExecStreamStdout, []byte("hello"))
	var buf bytes.Buffer
	if err := EncodeMessage(&buf, chunk); err != nil {
		t.Fatalf("encode chunk: %v", err)
	}
	var got ExecStreamMessage
	if err := DecodeMessage(&buf, &got); err != nil {
		t.Fatalf("decode chunk: %v", err)
	}
	if got.Kind != ExecStreamStdout || string(got.Data) != "hello" {
		t.Fatalf("chunk round-trip = %#v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("chunk Validate: %v", err)
	}

	code := 0
	result := NewExecResult(ExecStatusExited)
	result.ExitCode = &code
	resultFrame := NewExecStreamResult(result)
	if err := resultFrame.Validate(); err != nil {
		t.Fatalf("result frame Validate: %v", err)
	}

	// A result frame without a result is invalid.
	if err := (ExecStreamMessage{Kind: ExecStreamResult}).Validate(); err == nil {
		t.Fatal("result frame without result should be invalid")
	}
	// A chunk frame carrying a result is invalid.
	if err := (ExecStreamMessage{Kind: ExecStreamStdout, Result: &result}).Validate(); err == nil {
		t.Fatal("chunk frame with result should be invalid")
	}
	// Unknown kind is invalid.
	if err := (ExecStreamMessage{Kind: "weird"}).Validate(); err == nil {
		t.Fatal("unknown stream kind should be invalid")
	}
}

func TestExecResultEmptyOutputRoundTripsAsNonNilSlices(t *testing.T) {
	result := NewExecResult(ExecStatusFailedToStart)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got ExecResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Stdout == nil {
		t.Fatal("Stdout = nil, want empty slice")
	}
	if got.Stderr == nil {
		t.Fatal("Stderr = nil, want empty slice")
	}
	if len(got.Stdout) != 0 || len(got.Stderr) != 0 {
		t.Fatalf("output lengths = %d/%d, want 0/0", len(got.Stdout), len(got.Stderr))
	}
}

func TestBinaryByteFieldsRoundTrip(t *testing.T) {
	stdout := []byte{0x00, 0x01, 0x02, 0x7f, 0x80, 0xff}
	stderr := []byte{'\n', '\r', '\t', 0x00, 0xfe}
	stdin := []byte{0x00, 'i', 'n', 0xff}

	req := NewExecRequest([]string{"cat"})
	req.Stdin = stdin
	reqData, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var gotReq ExecRequest
	if err := json.Unmarshal(reqData, &gotReq); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotReq.Stdin, stdin) {
		t.Fatalf("stdin = %v, want %v", gotReq.Stdin, stdin)
	}

	result := NewExecResult(ExecStatusExited)
	exitCode := 0
	result.ExitCode = &exitCode
	result.Stdout = stdout
	result.Stderr = stderr
	resultData, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var gotResult ExecResult
	if err := json.Unmarshal(resultData, &gotResult); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotResult.Stdout, stdout) {
		t.Fatalf("stdout = %v, want %v", gotResult.Stdout, stdout)
	}
	if !bytes.Equal(gotResult.Stderr, stderr) {
		t.Fatalf("stderr = %v, want %v", gotResult.Stderr, stderr)
	}
}

func TestExitCodeUnsetDiffersFromZero(t *testing.T) {
	withZero := NewExecResult(ExecStatusExited)
	zero := 0
	withZero.ExitCode = &zero
	without := NewExecResult(ExecStatusTimedOut)

	if withZero.ExitCode == nil || *withZero.ExitCode != 0 {
		t.Fatalf("withZero.ExitCode = %#v, want pointer to zero", withZero.ExitCode)
	}
	if without.ExitCode != nil {
		t.Fatalf("without.ExitCode = %#v, want nil", without.ExitCode)
	}
}

func TestValidateRequest(t *testing.T) {
	req := NewExecRequest([]string{"true"})
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate valid request: %v", err)
	}

	tests := []struct {
		name string
		req  ExecRequest
		want string
	}{
		{name: "missing mode", req: ExecRequest{ProtocolVersion: CurrentProtocolVersion, Argv: []string{"true"}}, want: "mode"},
		{name: "missing argv", req: ExecRequest{ProtocolVersion: CurrentProtocolVersion, Mode: ExecModeSingleResponse}, want: "argv"},
		{name: "empty argv element", req: ExecRequest{ProtocolVersion: CurrentProtocolVersion, Mode: ExecModeSingleResponse, Argv: []string{""}}, want: "argv[0]"},
		{name: "bad version", req: ExecRequest{ProtocolVersion: "other", Mode: ExecModeSingleResponse, Argv: []string{"true"}}, want: "version"},
		{name: "negative timeout", req: ExecRequest{ProtocolVersion: CurrentProtocolVersion, Mode: ExecModeSingleResponse, Argv: []string{"true"}, TimeoutMS: -1}, want: "timeout_ms"},
		{name: "stdout too high", req: ExecRequest{ProtocolVersion: CurrentProtocolVersion, Mode: ExecModeSingleResponse, Argv: []string{"true"}, OutputLimitBytesStdout: DefaultOutputLimitBytes + 1}, want: "stdout"},
		{name: "stderr too high", req: ExecRequest{ProtocolVersion: CurrentProtocolVersion, Mode: ExecModeSingleResponse, Argv: []string{"true"}, OutputLimitBytesStderr: DefaultOutputLimitBytes + 1}, want: "stderr"},
		{name: "stdin too high", req: ExecRequest{ProtocolVersion: CurrentProtocolVersion, Mode: ExecModeSingleResponse, Argv: []string{"true"}, Stdin: make([]byte, DefaultOutputLimitBytes+1)}, want: "stdin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateResultAndError(t *testing.T) {
	result := NewExecResult(ExecStatusExited)
	exitCode := 0
	result.ExitCode = &exitCode
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate valid result: %v", err)
	}

	badStatus := NewExecResult(ExecStatus("bad"))
	if err := badStatus.Validate(); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("bad status err = %v", err)
	}

	badVersion := NewExecResult(ExecStatusExited)
	badVersion.ProtocolVersion = "other"
	if err := badVersion.Validate(); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("bad version err = %v", err)
	}

	badExitCode := NewExecResult(ExecStatusTimedOut)
	badExitCode.ExitCode = &exitCode
	if err := badExitCode.Validate(); err == nil || !strings.Contains(err.Error(), "exit_code") {
		t.Fatalf("bad exit code err = %v", err)
	}

	badError := NewExecResult(ExecStatusFailedToStart)
	badError.Error = &ExecError{Message: "missing code"}
	if err := badError.Validate(); err == nil || !strings.Contains(err.Error(), "code") {
		t.Fatalf("bad nested error err = %v", err)
	}
}
