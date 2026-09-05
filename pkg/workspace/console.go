package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/geoffbelknap/microagent/internal/consoleproto"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const consoleCapabilityTimeout = 100 * time.Millisecond

type ShellTarget struct {
	Network   string
	Address   string
	RuntimeID string
	Port      uint32
}

type ConsoleOptions struct {
	StateDir            string
	Name                string
	ReadyTimeout        time.Duration
	SendTimeout         time.Duration
	RequireCommandReady bool
	DialTarget          func(context.Context, ShellTarget) (net.Conn, error)
}

type ConsoleReadTimeoutError struct {
	Workspace     string
	Timeout       time.Duration
	PartialOutput string
}

func (err ConsoleReadTimeoutError) Error() string {
	if err.PartialOutput != "" {
		return fmt.Sprintf("console command for workspace %s timed out after %s before completion; partial output: %q", err.Workspace, err.Timeout, err.PartialOutput)
	}
	return fmt.Sprintf("console command for workspace %s timed out after %s before completion", err.Workspace, err.Timeout)
}

type ConsoleCompletionUnknownError struct {
	Workspace     string
	PartialOutput string
}

func (err ConsoleCompletionUnknownError) Error() string {
	if err.PartialOutput != "" {
		return fmt.Sprintf("console command for workspace %s ended before completion marker; partial output: %q", err.Workspace, err.PartialOutput)
	}
	return fmt.Sprintf("console command for workspace %s ended before completion marker", err.Workspace)
}

func DialConsole(ctx context.Context, opts ConsoleOptions) (net.Conn, error) {
	if err := validateConsoleAvailable(opts); err != nil {
		return nil, err
	}
	return dialConsoleShell(ctx, opts, true)
}

// ResizeConsole updates the PTY dimensions for a negotiated interactive
// console. supported is false for an older guest that did not advertise the
// resize protocol; callers may continue the byte-stream session in that case.
func ResizeConsole(conn net.Conn, rows, cols int) (supported bool, err error) {
	resizer, ok := conn.(interface {
		consoleResizeSupported() bool
		resizeConsole(rows, cols int) error
	})
	if !ok || !resizer.consoleResizeSupported() {
		return false, nil
	}
	return true, resizer.resizeConsole(rows, cols)
}

// WaitConsoleCommandReady waits until the guest console can complete a shell
// command without opening an additional interactive session for the caller.
func WaitConsoleCommandReady(ctx context.Context, opts ConsoleOptions) error {
	if err := validateConsoleAvailable(opts); err != nil {
		return err
	}
	opts.RequireCommandReady = true
	conn, err := dialConsoleShell(ctx, opts, false)
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

func validateConsoleAvailable(opts ConsoleOptions) error {
	if err := ValidateName(opts.Name); err != nil {
		return err
	}
	state, _, err := LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return err
	}
	if state == vmkit.StateQuarantined {
		return operation.New(operation.ErrorConflict, "workspace %s is quarantined; console input is disabled", opts.Name)
	}
	if state == "" {
		return WorkspaceNotFoundError{Name: opts.Name}
	}
	if state == vmkit.StatePaused {
		return operation.New(operation.ErrorConflict, "workspace %s is paused; resume it first", opts.Name)
	}
	if state != vmkit.StateRunning {
		return operation.New(operation.ErrorConflict, "workspace %s is not running; console input is unavailable in state %s", opts.Name, state)
	}
	return nil
}

func SendConsoleCommand(ctx context.Context, opts ConsoleOptions, command string, output io.Writer) error {
	if opts.SendTimeout < 0 {
		return operation.New(operation.ErrorValidation, "connect timeout must not be negative")
	}
	conn, err := DialConsole(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return sendConsoleCommandOnConn(conn, opts, command, output)
}

func sendConsoleCommandOnConn(conn net.Conn, opts ConsoleOptions, command string, output io.Writer) error {
	token := strconv.FormatInt(time.Now().UnixNano(), 10)
	beginMarker := "__MICROAGENT_BEGIN_" + token + "__"
	doneMarker := "__MICROAGENT_DONE_" + token + "__"
	statusVar := "__ma_status"
	tokenVar := "__ma_token"
	text := strings.TrimRight(strings.ReplaceAll(command, "\n", "; "), " \t\r;")
	if text != "" {
		text += "; "
	}
	// Frame output after terminal echo has been disabled. Filtering whole lines
	// by wrapper variable names can discard command output sharing an echoed
	// line, as well as legitimate output that happens to mention those names.
	text = "stty -echo\r" + tokenVar + "=" + token + "; " +
		"printf '__MICROAGENT_BEGIN_%s__' \"$" + tokenVar + "\"; " + text
	text += statusVar + "=$?; "
	text += tokenVar + "=" + token + "; "
	text += "printf '\\r\\n__MICROAGENT_DONE_%s__%s\\r\\n' \"$" + tokenVar + "\" \"$" + statusVar + "\"; "
	text += "exit\r"
	if _, err := io.WriteString(conn, text); err != nil {
		return err
	}
	var captured bytes.Buffer
	writer := output
	if writer == nil {
		writer = io.Discard
	}
	_ = conn.SetReadDeadline(time.Now().Add(opts.SendTimeout))
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			captured.Write(buf[:n])
			if bytes.Contains(captured.Bytes(), []byte(beginMarker)) && bytes.Contains(captured.Bytes(), []byte(doneMarker)) {
				_, writeErr := io.WriteString(writer, cleanConsoleSendOutput(captured.String(), token))
				return writeErr
			}
		}
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				partial := cleanConsoleSendOutput(captured.String(), token)
				_, _ = io.WriteString(writer, partial)
				return ConsoleReadTimeoutError{
					Workspace:     opts.Name,
					Timeout:       opts.SendTimeout,
					PartialOutput: partial,
				}
			}
			if errors.Is(err, io.EOF) {
				partial := cleanConsoleSendOutput(captured.String(), token)
				_, _ = io.WriteString(writer, partial)
				return ConsoleCompletionUnknownError{Workspace: opts.Name, PartialOutput: partial}
			}
			return err
		}
	}
}

func cleanConsoleSendOutput(text, token string) string {
	beginMarker := "__MICROAGENT_BEGIN_" + token + "__"
	doneMarker := "__MICROAGENT_DONE_" + token + "__"
	if idx := strings.Index(text, beginMarker); idx >= 0 {
		text = text[idx+len(beginMarker):]
		if end := strings.Index(text, doneMarker); end >= 0 {
			text = text[:end]
		}
		return text
	}
	// A session that ends before the begin marker has only startup diagnostics
	// and possible echoed wrapper input; retain the existing error cleanup.
	if idx := strings.Index(text, doneMarker); idx >= 0 {
		text = text[:idx]
	}
	lines := strings.SplitAfter(text, "\n")
	cleaned := lines[:0]
	for _, line := range lines {
		switch {
		case strings.Contains(line, "__ma_status"):
			continue
		case strings.Contains(line, "__ma_token"):
			continue
		case strings.Contains(line, "__MICROAGENT_DONE_%s__%s"):
			continue
		case strings.Contains(line, "stty -echo"):
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "")
}

func ConsoleTarget(name string, state RuntimeState) (ShellTarget, error) {
	port := uint32(state.Config.ShellPort)
	if port == 0 {
		port = uint32(ShellPortForName(name))
	}
	return ShellTarget{Network: "tcp", Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), Port: port}, nil
}

func DialShellTarget(ctx context.Context, target ShellTarget) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", target.Address)
}

func ShellTargetDescription(target ShellTarget) string {
	return target.Address
}

func ProbeShellTarget(ctx context.Context, target ShellTarget, timeout time.Duration, dialTarget func(context.Context, ShellTarget) (net.Conn, error)) (time.Duration, error) {
	if timeout <= 0 {
		timeout = 150 * time.Millisecond
	}
	if dialTarget == nil {
		dialTarget = DialShellTarget
	}
	start := time.Now()
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialTarget(dialCtx, target)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	_ = conn.Close()
	return elapsed, nil
}

func ProbeShellCommand(ctx context.Context, opts ConsoleOptions, target ShellTarget, timeout time.Duration, dialTarget func(context.Context, ShellTarget) (net.Conn, error)) (time.Duration, error) {
	if timeout <= 0 {
		timeout = time.Second
	}
	if dialTarget == nil {
		dialTarget = DialShellTarget
	}
	start := time.Now()
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	conn, err := dialTarget(dialCtx, target)
	cancel()
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	conn = negotiateConsoleConnection(conn)
	opts.SendTimeout = timeout
	err = sendConsoleCommandOnConn(conn, opts, ":", io.Discard)
	_ = conn.Close()
	return time.Since(start), err
}

func dialConsoleShell(ctx context.Context, opts ConsoleOptions, returnConnection bool) (net.Conn, error) {
	if opts.ReadyTimeout < 0 {
		return nil, fmt.Errorf("connect ready-timeout must not be negative")
	}
	state, err := ReadRuntimeState(Options{StateDir: opts.StateDir, Name: opts.Name})
	if err != nil {
		return nil, err
	}
	target, err := ConsoleTarget(opts.Name, state)
	if err != nil {
		return nil, err
	}
	dialTarget := opts.DialTarget
	if dialTarget == nil {
		dialTarget = DialShellTarget
	}
	rawDialTarget := dialTarget
	dialTarget = func(ctx context.Context, target ShellTarget) (net.Conn, error) {
		conn, err := rawDialTarget(ctx, target)
		if err != nil {
			return nil, err
		}
		return negotiateConsoleConnection(conn), nil
	}
	if opts.ReadyTimeout <= 0 {
		if opts.RequireCommandReady {
			probeTimeout := opts.SendTimeout
			if probeTimeout <= 0 {
				probeTimeout = time.Second
			}
			if _, err := ProbeShellCommand(ctx, opts, target, probeTimeout, dialTarget); err != nil {
				return nil, fmt.Errorf("guest shell is not ready for workspace %s at %s: %w", opts.Name, ShellTargetDescription(target), err)
			}
			if !returnConnection {
				return nil, nil
			}
		}
		conn, err := dialTarget(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("guest shell is not ready for workspace %s at %s: %w", opts.Name, ShellTargetDescription(target), err)
		}
		if returnConnection {
			return conn, nil
		}
		_ = conn.Close()
		return nil, nil
	}
	deadline := time.Now().Add(opts.ReadyTimeout)
	var lastErr error
	for {
		guestShellPort := state.Config.ShellPort
		restoredShellEndpoint := state.Config.GuestShellPort != 0
		if state.Config.GuestShellPort != 0 {
			guestShellPort = state.Config.GuestShellPort
		}
		// A restored guest does not replay the pre-snapshot serial line that
		// announced its shell listener. Prove that endpoint with a command
		// round trip instead of waiting forever for historical log output.
		if target.Network == "tcp" && guestShellPort != 0 && !restoredShellEndpoint && !shellHelperListening(state.SerialLogPath, guestShellPort) {
			lastErr = fmt.Errorf("guest shell helper is not listening on port %d", guestShellPort)
		} else {
			if opts.RequireCommandReady || restoredShellEndpoint {
				probeTimeout := opts.SendTimeout
				if probeTimeout <= 0 || probeTimeout > time.Second {
					probeTimeout = time.Second
				}
				_, err := ProbeShellCommand(ctx, opts, target, probeTimeout, dialTarget)
				if err == nil {
					if !returnConnection {
						return nil, nil
					}
					conn, err := dialTarget(ctx, target)
					if err == nil {
						return conn, nil
					}
				}
				lastErr = err
			} else {
				dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				conn, err := dialTarget(dialCtx, target)
				cancel()
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("guest shell is not ready for workspace %s at %s: %w", opts.Name, ShellTargetDescription(target), lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type negotiatedConsoleConnection struct {
	net.Conn
	buffer         []byte
	supportsResize bool
	writeMu        sync.Mutex
}

func negotiateConsoleConnection(conn net.Conn) net.Conn {
	if _, ok := conn.(*negotiatedConsoleConnection); ok {
		return conn
	}
	capability := []byte(consoleproto.CapabilityV1)
	buffer := make([]byte, 0, len(capability))
	_ = conn.SetReadDeadline(time.Now().Add(consoleCapabilityTimeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for len(buffer) < len(capability) {
		one := make([]byte, 1)
		n, err := conn.Read(one)
		if n > 0 {
			buffer = append(buffer, one[0])
			if !bytes.Equal(buffer, capability[:len(buffer)]) {
				return &negotiatedConsoleConnection{Conn: conn, buffer: buffer}
			}
			if len(buffer) == len(capability) {
				return &negotiatedConsoleConnection{Conn: conn, supportsResize: true}
			}
		}
		if err != nil {
			return &negotiatedConsoleConnection{Conn: conn, buffer: buffer}
		}
	}
	return &negotiatedConsoleConnection{Conn: conn, buffer: buffer}
}

func (conn *negotiatedConsoleConnection) consoleResizeSupported() bool {
	return conn.supportsResize
}

func (conn *negotiatedConsoleConnection) Read(buffer []byte) (int, error) {
	if len(conn.buffer) > 0 {
		n := copy(buffer, conn.buffer)
		conn.buffer = conn.buffer[n:]
		return n, nil
	}
	return conn.Conn.Read(buffer)
}

func (conn *negotiatedConsoleConnection) Write(data []byte) (int, error) {
	conn.writeMu.Lock()
	defer conn.writeMu.Unlock()
	return conn.Conn.Write(data)
}

func (conn *negotiatedConsoleConnection) resizeConsole(rows, cols int) error {
	frame, err := consoleproto.EncodeResize(rows, cols)
	if err != nil {
		return err
	}
	conn.writeMu.Lock()
	defer conn.writeMu.Unlock()
	if _, err := conn.Conn.Write(frame); err != nil {
		return fmt.Errorf("write console resize: %w", err)
	}
	return nil
}
