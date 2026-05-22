package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	CurrentProtocolVersion  = "exec.v1"
	DefaultOutputLimitBytes = 10 * 1024 * 1024
	DefaultMaxMessageBytes  = 2 * DefaultOutputLimitBytes
	DefaultTimeout          = 5 * time.Minute
)

type ExecMode string

const (
	ExecModeSingleResponse ExecMode = "single_response"
	ExecModeStream         ExecMode = "stream"
)

func (mode ExecMode) Validate() error {
	switch mode {
	case ExecModeSingleResponse, ExecModeStream:
		return nil
	default:
		return fmt.Errorf("exec mode must be one of %q or %q, got %q", ExecModeSingleResponse, ExecModeStream, mode)
	}
}

func (mode *ExecMode) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	value := ExecMode(raw)
	if err := value.Validate(); err != nil {
		return err
	}
	*mode = value
	return nil
}

type ExecStatus string

const (
	ExecStatusExited        ExecStatus = "exited"
	ExecStatusSignaled      ExecStatus = "signaled"
	ExecStatusTimedOut      ExecStatus = "timed_out"
	ExecStatusFailedToStart ExecStatus = "failed_to_start"
)

func (status ExecStatus) Validate() error {
	switch status {
	case ExecStatusExited, ExecStatusSignaled, ExecStatusTimedOut, ExecStatusFailedToStart:
		return nil
	default:
		return fmt.Errorf("exec status must be one of %q, %q, %q, or %q, got %q", ExecStatusExited, ExecStatusSignaled, ExecStatusTimedOut, ExecStatusFailedToStart, status)
	}
}

func (status *ExecStatus) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	value := ExecStatus(raw)
	if err := value.Validate(); err != nil {
		return err
	}
	*status = value
	return nil
}

type ExecRequest struct {
	ProtocolVersion        string            `json:"protocol_version"`
	Mode                   ExecMode          `json:"mode"`
	Argv                   []string          `json:"argv"`
	Env                    map[string]string `json:"env,omitempty"`
	Cwd                    string            `json:"cwd,omitempty"`
	Stdin                  []byte            `json:"stdin,omitempty"`
	TimeoutMS              int64             `json:"timeout_ms,omitempty"`
	OutputLimitBytesStdout int64             `json:"output_limit_bytes_stdout,omitempty"`
	OutputLimitBytesStderr int64             `json:"output_limit_bytes_stderr,omitempty"`
}

func NewExecRequest(argv []string) ExecRequest {
	return ExecRequest{
		ProtocolVersion: CurrentProtocolVersion,
		Mode:            ExecModeSingleResponse,
		Argv:            append([]string(nil), argv...),
	}
}

func (req ExecRequest) Validate() error {
	if req.ProtocolVersion != "" && req.ProtocolVersion != CurrentProtocolVersion {
		return fmt.Errorf("unsupported exec protocol version %q", req.ProtocolVersion)
	}
	if req.Mode == "" {
		return fmt.Errorf("exec mode is required")
	}
	if err := req.Mode.Validate(); err != nil {
		return err
	}
	if len(req.Argv) == 0 {
		return fmt.Errorf("exec argv is required")
	}
	for i, arg := range req.Argv {
		if arg == "" {
			return fmt.Errorf("exec argv[%d] must not be empty", i)
		}
	}
	if req.TimeoutMS < 0 {
		return fmt.Errorf("exec timeout_ms must not be negative")
	}
	if req.OutputLimitBytesStdout < 0 {
		return fmt.Errorf("exec output_limit_bytes_stdout must not be negative")
	}
	if req.OutputLimitBytesStdout > DefaultOutputLimitBytes {
		return fmt.Errorf("exec output_limit_bytes_stdout exceeds maximum %d", DefaultOutputLimitBytes)
	}
	if req.OutputLimitBytesStderr < 0 {
		return fmt.Errorf("exec output_limit_bytes_stderr must not be negative")
	}
	if req.OutputLimitBytesStderr > DefaultOutputLimitBytes {
		return fmt.Errorf("exec output_limit_bytes_stderr exceeds maximum %d", DefaultOutputLimitBytes)
	}
	if len(req.Stdin) > DefaultOutputLimitBytes {
		return fmt.Errorf("exec stdin exceeds maximum %d", DefaultOutputLimitBytes)
	}
	return nil
}

type ExecResult struct {
	ProtocolVersion string     `json:"protocol_version"`
	StartedAt       string     `json:"started_at,omitempty"`
	CompletedAt     string     `json:"completed_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Stdout          []byte     `json:"stdout"`
	Stderr          []byte     `json:"stderr"`
	StdoutTruncated bool       `json:"stdout_truncated"`
	StderrTruncated bool       `json:"stderr_truncated"`
	Status          ExecStatus `json:"status"`
	Error           *ExecError `json:"error,omitempty"`
}

func NewExecResult(status ExecStatus) ExecResult {
	return ExecResult{
		ProtocolVersion: CurrentProtocolVersion,
		Stdout:          []byte{},
		Stderr:          []byte{},
		Status:          status,
	}
}

func (result ExecResult) Validate() error {
	if result.ProtocolVersion != "" && result.ProtocolVersion != CurrentProtocolVersion {
		return fmt.Errorf("unsupported exec protocol version %q", result.ProtocolVersion)
	}
	if err := result.Status.Validate(); err != nil {
		return err
	}
	if result.ExitCode != nil && result.Status != ExecStatusExited {
		return fmt.Errorf("exec exit_code is only valid when status is %q", ExecStatusExited)
	}
	if result.Error != nil {
		if err := result.Error.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type ExecError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (err ExecError) Error() string {
	if err.Detail != "" {
		return fmt.Sprintf("%s: %s (%s)", err.Code, err.Message, err.Detail)
	}
	if err.Code != "" {
		return fmt.Sprintf("%s: %s", err.Code, err.Message)
	}
	return err.Message
}

func (err ExecError) Validate() error {
	if err.Code == "" {
		return fmt.Errorf("exec error code is required")
	}
	if err.Message == "" {
		return fmt.Errorf("exec error message is required")
	}
	return nil
}

func EncodeMessage(w io.Writer, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data) > int(^uint32(0)) {
		return fmt.Errorf("exec protocol message length %d exceeds uint32 length prefix", len(data))
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func DecodeMessage(r io.Reader, out any) error {
	return DecodeMessageWithMax(r, out, DefaultMaxMessageBytes)
}

func DecodeMessageWithMax(r io.Reader, out any, maxBytes uint32) error {
	if out == nil {
		return errors.New("exec protocol decode target is nil")
	}
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length > maxBytes {
		return fmt.Errorf("exec protocol message length %d exceeds maximum %d", length, maxBytes)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
