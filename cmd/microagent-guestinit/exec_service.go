//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	osexec "os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
	"golang.org/x/sys/unix"
)

const defaultStructuredExecTerminationGrace = 250 * time.Millisecond

// defaultStructuredExecWaitDelay bounds how long Wait waits for stdout/stderr to
// drain AFTER the command exits. Normal output drains in well under this; the
// bound only trips when a child the command spawned inherited the pipe and holds
// it open, which would otherwise block Wait forever.
const defaultStructuredExecWaitDelay = 5 * time.Second

type execReadWriteCloser interface {
	io.Reader
	io.Writer
	io.Closer
}

type structuredExecService struct {
	env              []string
	terminationGrace time.Duration
	waitDelay        time.Duration
	now              func() time.Time
}

func startStructuredExecService(port uint16, env []string) error {
	if port == 0 {
		return nil
	}
	cmd := osexec.Command(os.Args[0], "exec-service", strconv.Itoa(int(port)))
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start structured exec service on port %d: %w", port, err)
	}
	return nil
}

func runExecService(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: microagent-init exec-service <port>")
		return 127
	}
	port, err := parseUint16(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse exec service port: %v\n", err)
		return 127
	}
	fd, err := openStructuredExecListener(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	service := structuredExecService{
		env:              os.Environ(),
		terminationGrace: defaultStructuredExecTerminationGrace,
		waitDelay:        defaultStructuredExecWaitDelay,
		now:              time.Now,
	}
	serveStructuredExecConnections(fd, service)
	return 0
}

func openStructuredExecListener(port uint16) (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("open vsock structured exec listener for port %d: %w", port, err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: uint32(port)}); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("bind vsock structured exec listener for port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 8); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("listen on vsock structured exec port %d: %w", port, err)
	}
	log.Printf("microagent-init: structured exec service listening on vsock port %d", port)
	return fd, nil
}

func serveStructuredExecConnections(fd int, service structuredExecService) {
	for {
		connFD, _, err := unix.Accept(fd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept structured exec request: %v\n", err)
			_ = unix.Close(fd)
			return
		}
		go runStructuredExecConnection(connFD, service)
	}
}

func runStructuredExecConnection(fd int, service structuredExecService) {
	file := os.NewFile(uintptr(fd), "structured-exec-vsock")
	if file == nil {
		_ = unix.Close(fd)
		return
	}
	handleStructuredExecConnection(file, service)
}

func handleStructuredExecConnection(conn execReadWriteCloser, service structuredExecService) {
	defer conn.Close()
	if service.terminationGrace == 0 {
		service.terminationGrace = defaultStructuredExecTerminationGrace
	}
	if service.waitDelay == 0 {
		service.waitDelay = defaultStructuredExecWaitDelay
	}
	if service.now == nil {
		service.now = time.Now
	}
	var req execprotocol.ExecRequest
	if err := execprotocol.DecodeMessage(conn, &req); err != nil {
		_ = execprotocol.EncodeMessage(conn, execServiceErrorResult("invalid_request", "decode exec request", err.Error(), service.now))
		return
	}
	if req.Mode == execprotocol.ExecModeStream {
		handleStreamingExecConnection(conn, req, service)
		return
	}
	result := handleStructuredExecRequest(req, service)
	if err := execprotocol.EncodeMessage(conn, result); err != nil {
		log.Printf("microagent-init: encode structured exec response: %v", err)
	}
}

// handleStreamingExecConnection serves a stream-mode request: it always speaks
// the frame protocol back, delivering errors and the terminal status as a result
// frame so the streaming client decodes a uniform sequence.
func handleStreamingExecConnection(conn execReadWriteCloser, req execprotocol.ExecRequest, service structuredExecService) {
	sendResult := func(result execprotocol.ExecResult) {
		if err := execprotocol.EncodeMessage(conn, execprotocol.NewExecStreamResult(result)); err != nil {
			log.Printf("microagent-init: encode streaming exec result: %v", err)
		}
	}
	if req.ProtocolVersion != "" && req.ProtocolVersion != execprotocol.CurrentProtocolVersion {
		sendResult(execServiceErrorResult("unsupported_protocol_version", "unsupported exec protocol version", req.ProtocolVersion, service.now))
		return
	}
	if err := req.Validate(); err != nil {
		sendResult(execServiceErrorResult("invalid_request", "invalid exec request", err.Error(), service.now))
		return
	}
	sendResult(executeStreamingExec(conn, req, service))
}

func handleStructuredExecRequest(req execprotocol.ExecRequest, service structuredExecService) execprotocol.ExecResult {
	if req.ProtocolVersion != "" && req.ProtocolVersion != execprotocol.CurrentProtocolVersion {
		return execServiceErrorResult("unsupported_protocol_version", "unsupported exec protocol version", req.ProtocolVersion, service.now)
	}
	if err := req.Validate(); err != nil {
		return execServiceErrorResult("invalid_request", "invalid exec request", err.Error(), service.now)
	}
	return executeStructuredExec(req, service)
}

func execServiceErrorResult(code, message, detail string, now func() time.Time) execprotocol.ExecResult {
	if now == nil {
		now = time.Now
	}
	ts := now().UTC().Format(time.RFC3339Nano)
	result := execprotocol.NewExecResult(execprotocol.ExecStatusFailedToStart)
	result.StartedAt = ts
	result.CompletedAt = ts
	result.Error = &execprotocol.ExecError{
		Code:    code,
		Message: message,
		Detail:  detail,
	}
	return result
}

func executeStructuredExec(req execprotocol.ExecRequest, service structuredExecService) execprotocol.ExecResult {
	started := service.now().UTC()
	result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
	result.StartedAt = started.Format(time.RFC3339Nano)

	stdout := newBoundedExecBuffer(execOutputLimit(req.OutputLimitBytesStdout))
	stderr := newBoundedExecBuffer(execOutputLimit(req.OutputLimitBytesStderr))
	cmd := osexec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Env = mergeExecEnv(service.env, req.Env)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if req.Stdin != nil {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureExecCommand(cmd, service)

	if err := cmd.Start(); err != nil {
		result.Status = execprotocol.ExecStatusFailedToStart
		result.CompletedAt = service.now().UTC().Format(time.RFC3339Nano)
		result.Stdout = stdout.Bytes()
		result.Stderr = stderr.Bytes()
		result.StdoutTruncated = stdout.Truncated()
		result.StderrTruncated = stderr.Truncated()
		result.Error = &execprotocol.ExecError{
			Code:    "failed_to_start",
			Message: "failed to start command",
			Detail:  err.Error(),
		}
		return result
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	timeout := execTimeout(req.TimeoutMS)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waitCh:
		applyExecWaitResult(&result, cmd, err)
	case <-timer.C:
		terminateTimedOutProcess(cmd, waitCh, service.terminationGrace)
		result.Status = execprotocol.ExecStatusTimedOut
		result.ExitCode = nil
	}

	result.CompletedAt = service.now().UTC().Format(time.RFC3339Nano)
	result.Stdout = stdout.Bytes()
	result.Stderr = stderr.Bytes()
	result.StdoutTruncated = stdout.Truncated()
	result.StderrTruncated = stderr.Truncated()
	return result
}

// executeStreamingExec runs the command, emitting stdout/stderr as chunk frames
// on conn as they arrive, and returns the terminal ExecResult. In stream mode
// the returned result carries status/exit/timing/truncation but not the output
// bytes — those were delivered as chunks.
func executeStreamingExec(conn execReadWriteCloser, req execprotocol.ExecRequest, service structuredExecService) execprotocol.ExecResult {
	started := service.now().UTC()
	result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
	result.StartedAt = started.Format(time.RFC3339Nano)

	var mu sync.Mutex
	stdout := newExecStreamWriter(conn, &mu, execprotocol.ExecStreamStdout, execOutputLimit(req.OutputLimitBytesStdout))
	stderr := newExecStreamWriter(conn, &mu, execprotocol.ExecStreamStderr, execOutputLimit(req.OutputLimitBytesStderr))
	cmd := osexec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Env = mergeExecEnv(service.env, req.Env)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if req.Stdin != nil {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureExecCommand(cmd, service)

	if err := cmd.Start(); err != nil {
		result.Status = execprotocol.ExecStatusFailedToStart
		result.CompletedAt = service.now().UTC().Format(time.RFC3339Nano)
		result.StdoutTruncated = stdout.Truncated()
		result.StderrTruncated = stderr.Truncated()
		result.Error = &execprotocol.ExecError{
			Code:    "failed_to_start",
			Message: "failed to start command",
			Detail:  err.Error(),
		}
		return result
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	timeout := execTimeout(req.TimeoutMS)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waitCh:
		applyExecWaitResult(&result, cmd, err)
	case <-timer.C:
		terminateTimedOutProcess(cmd, waitCh, service.terminationGrace)
		result.Status = execprotocol.ExecStatusTimedOut
		result.ExitCode = nil
	}

	result.CompletedAt = service.now().UTC().Format(time.RFC3339Nano)
	result.StdoutTruncated = stdout.Truncated()
	result.StderrTruncated = stderr.Truncated()
	return result
}

// execStreamWriter emits each Write as an exec stream chunk frame on a shared
// connection (serialized by mu so stdout and stderr frames never interleave
// mid-message), enforcing the same per-stream byte ceiling as the buffered path.
type execStreamWriter struct {
	conn      execReadWriteCloser
	mu        *sync.Mutex
	kind      execprotocol.ExecStreamKind
	limit     int64
	written   int64
	truncated bool
}

func newExecStreamWriter(conn execReadWriteCloser, mu *sync.Mutex, kind execprotocol.ExecStreamKind, limit int64) *execStreamWriter {
	return &execStreamWriter{conn: conn, mu: mu, kind: kind, limit: limit}
}

func (w *execStreamWriter) Write(p []byte) (int, error) {
	n := len(p)
	chunk := p
	if w.limit > 0 {
		remaining := w.limit - w.written
		if remaining <= 0 {
			if n > 0 {
				w.truncated = true
			}
			return n, nil
		}
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
			w.truncated = true
		}
	}
	if len(chunk) == 0 {
		return n, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := execprotocol.EncodeMessage(w.conn, execprotocol.NewExecStreamChunk(w.kind, chunk)); err != nil {
		return n, err
	}
	w.written += int64(len(chunk))
	return n, nil
}

func (w *execStreamWriter) Truncated() bool {
	return w.truncated
}

func execTimeout(timeoutMS int64) time.Duration {
	if timeoutMS <= 0 {
		return execprotocol.DefaultTimeout
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func execOutputLimit(limit int64) int64 {
	if limit <= 0 {
		return execprotocol.DefaultOutputLimitBytes
	}
	return limit
}

func applyExecWaitResult(result *execprotocol.ExecResult, cmd *osexec.Cmd, err error) {
	if err == nil {
		code := 0
		result.Status = execprotocol.ExecStatusExited
		result.ExitCode = &code
		return
	}
	// The command exited but a child it spawned inherited stdout/stderr and held
	// the pipe open past WaitDelay. The command itself finished; report its real
	// exit code rather than a wait failure. (A nonzero exit returns an ExitError,
	// handled below; ErrWaitDelay is only returned on an otherwise-clean exit.)
	if errors.Is(err, osexec.ErrWaitDelay) {
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		result.Status = execprotocol.ExecStatusExited
		result.ExitCode = &code
		result.Error = nil
		return
	}
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) {
		result.Status = execprotocol.ExecStatusFailedToStart
		result.ExitCode = nil
		result.Error = &execprotocol.ExecError{
			Code:    "wait_failed",
			Message: "failed while waiting for command",
			Detail:  err.Error(),
		}
		return
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		result.Status = execprotocol.ExecStatusFailedToStart
		result.ExitCode = nil
		result.Error = &execprotocol.ExecError{
			Code:    "wait_failed",
			Message: "failed to inspect command exit status",
			Detail:  err.Error(),
		}
		return
	}
	if status.Signaled() {
		result.Status = execprotocol.ExecStatusSignaled
		result.ExitCode = nil
		result.Error = &execprotocol.ExecError{
			Code:    "signaled",
			Message: "command terminated by signal",
			Detail:  status.Signal().String(),
		}
		return
	}
	code := status.ExitStatus()
	result.Status = execprotocol.ExecStatusExited
	result.ExitCode = &code
	result.Error = nil
}

// configureExecCommand puts the command in its own process group and bounds the
// post-exit I/O wait, so a command that spawns children can be torn down as a
// group on timeout, and Wait never blocks forever when a lingering child inherits
// stdout/stderr and holds the pipe open.
func configureExecCommand(cmd *osexec.Cmd, service structuredExecService) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.WaitDelay = service.waitDelay
}

func terminateTimedOutProcess(cmd *osexec.Cmd, waitCh <-chan error, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	// Signal the whole process group (Setpgid made the child its group leader), so
	// a command that spawned children — including a daemon that inherited
	// stdout/stderr — is torn down, not just the direct child (which may already
	// have exited while the daemon keeps the pipe open).
	signalExecProcessGroup(cmd.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-waitCh:
		return
	case <-timer.C:
		signalExecProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
		<-waitCh
	}
}

// signalExecProcessGroup sends sig to the process group led by pid (negative pid),
// falling back to the single process if the group signal fails.
func signalExecProcessGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}

func mergeExecEnv(base []string, extra map[string]string) []string {
	merged := make(map[string]string, len(base)+len(extra))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		merged[key] = value
	}
	for key, value := range extra {
		if key != "" {
			merged[key] = value
		}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+merged[key])
	}
	return env
}

type boundedExecBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func newBoundedExecBuffer(limit int64) *boundedExecBuffer {
	return &boundedExecBuffer{limit: limit}
}

func (b *boundedExecBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:int(remaining)])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *boundedExecBuffer) Bytes() []byte {
	if b.buf.Len() == 0 {
		return []byte{}
	}
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *boundedExecBuffer) Truncated() bool {
	return b.truncated
}
