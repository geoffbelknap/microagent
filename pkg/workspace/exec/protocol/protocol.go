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
	// A result may carry stdout and stderr at the per-stream output ceiling.
	// JSON encodes byte slices as base64, which adds roughly 33% overhead, and
	// the surrounding envelope adds more bytes. Two 10 MiB streams therefore
	// approach 27 MiB on the wire, so use a 40 MiB framing ceiling for headroom.
	DefaultMaxMessageBytes = 4 * DefaultOutputLimitBytes
	DefaultTimeout         = 5 * time.Minute
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

// ExecOperation distinguishes ordinary command execution from the narrow
// lifecycle control carried over the same guest channel. The empty value is
// the legacy spelling of exec so existing exec.v1 clients remain compatible.
type ExecOperation string

const (
	ExecOperationExec     ExecOperation = "exec"
	ExecOperationShutdown ExecOperation = "shutdown"
)

func (operation ExecOperation) normalized() ExecOperation {
	if operation == "" {
		return ExecOperationExec
	}
	return operation
}

func (operation ExecOperation) Validate() error {
	switch operation.normalized() {
	case ExecOperationExec, ExecOperationShutdown:
		return nil
	default:
		return fmt.Errorf("exec operation must be one of %q or %q, got %q", ExecOperationExec, ExecOperationShutdown, operation)
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
	Operation              ExecOperation     `json:"operation,omitempty"`
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

// NewShutdownRequest asks guest init to begin its graceful power-off path.
// PID 1 chooses and forwards the configured OCI stop signal; the host never
// supplies a signal in this request.
func NewShutdownRequest() ExecRequest {
	return ExecRequest{
		ProtocolVersion: CurrentProtocolVersion,
		Operation:       ExecOperationShutdown,
		Mode:            ExecModeSingleResponse,
	}
}

func (req ExecRequest) IsShutdown() bool {
	return req.Operation.normalized() == ExecOperationShutdown
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
	if err := req.Operation.Validate(); err != nil {
		return err
	}
	if req.IsShutdown() {
		if req.Mode != ExecModeSingleResponse {
			return fmt.Errorf("shutdown operation requires mode %q", ExecModeSingleResponse)
		}
		if len(req.Argv) != 0 || len(req.Env) != 0 || req.Cwd != "" || len(req.Stdin) != 0 ||
			req.TimeoutMS != 0 || req.OutputLimitBytesStdout != 0 || req.OutputLimitBytesStderr != 0 {
			return fmt.Errorf("shutdown operation does not accept exec arguments")
		}
		return nil
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

// ExecStreamKind tags a frame in a streaming exec response (Mode=stream).
type ExecStreamKind string

const (
	ExecStreamStdout ExecStreamKind = "stdout"
	ExecStreamStderr ExecStreamKind = "stderr"
	ExecStreamResult ExecStreamKind = "result"
)

func (kind ExecStreamKind) Validate() error {
	switch kind {
	case ExecStreamStdout, ExecStreamStderr, ExecStreamResult:
		return nil
	default:
		return fmt.Errorf("exec stream kind must be one of %q, %q, or %q, got %q", ExecStreamStdout, ExecStreamStderr, ExecStreamResult, kind)
	}
}

// ExecStreamMessage is one frame of a streaming exec response. The server emits
// zero or more stdout/stderr chunk frames as the command runs, then exactly one
// result frame carrying the final ExecResult. In stream mode the result frame's
// Stdout/Stderr are empty — those bytes were delivered as chunks — but
// ExitCode, Status, timing, and the truncation flags are populated as usual.
type ExecStreamMessage struct {
	ProtocolVersion string         `json:"protocol_version"`
	Kind            ExecStreamKind `json:"kind"`
	Data            []byte         `json:"data,omitempty"`
	Result          *ExecResult    `json:"result,omitempty"`
}

// NewExecStreamChunk builds a stdout/stderr chunk frame copying data.
func NewExecStreamChunk(kind ExecStreamKind, data []byte) ExecStreamMessage {
	return ExecStreamMessage{
		ProtocolVersion: CurrentProtocolVersion,
		Kind:            kind,
		Data:            append([]byte(nil), data...),
	}
}

// NewExecStreamResult builds the terminal result frame.
func NewExecStreamResult(result ExecResult) ExecStreamMessage {
	return ExecStreamMessage{
		ProtocolVersion: CurrentProtocolVersion,
		Kind:            ExecStreamResult,
		Result:          &result,
	}
}

func (msg ExecStreamMessage) Validate() error {
	if msg.ProtocolVersion != "" && msg.ProtocolVersion != CurrentProtocolVersion {
		return fmt.Errorf("unsupported exec protocol version %q", msg.ProtocolVersion)
	}
	if err := msg.Kind.Validate(); err != nil {
		return err
	}
	if msg.Kind == ExecStreamResult {
		if msg.Result == nil {
			return fmt.Errorf("exec stream result frame requires a result")
		}
		return msg.Result.Validate()
	}
	if msg.Result != nil {
		return fmt.Errorf("exec stream %q frame must not carry a result", msg.Kind)
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
