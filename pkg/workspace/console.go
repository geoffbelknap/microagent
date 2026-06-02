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
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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
	if err := ValidateName(opts.Name); err != nil {
		return nil, err
	}
	state, _, err := LatestStartState(opts.StateDir, opts.Name)
	if err != nil {
		return nil, err
	}
	if state == vmkit.StateQuarantined {
		return nil, fmt.Errorf("workspace %s is quarantined; console input is disabled", opts.Name)
	}
	if state == "" {
		return nil, WorkspaceNotFoundError{Name: opts.Name}
	}
	if state == vmkit.StatePaused {
		return nil, fmt.Errorf("workspace %s is paused; resume it first", opts.Name)
	}
	if state != vmkit.StateRunning {
		return nil, fmt.Errorf("workspace %s is not running; console input is unavailable in state %s", opts.Name, state)
	}
	return dialConsoleShell(ctx, opts)
}

func SendConsoleCommand(ctx context.Context, opts ConsoleOptions, command string, output io.Writer) error {
	if opts.SendTimeout < 0 {
		return fmt.Errorf("connect timeout must not be negative")
	}
	conn, err := DialConsole(ctx, opts)
	if err != nil {
		return err
	}
	defer conn.Close()
	return sendConsoleCommandOnConn(conn, opts, command, output)
}

func sendConsoleCommandOnConn(conn net.Conn, opts ConsoleOptions, command string, output io.Writer) error {
	token := strconv.FormatInt(time.Now().UnixNano(), 10)
	doneMarker := "__MICROAGENT_DONE_" + token + "__"
	statusVar := "__ma_status"
	tokenVar := "__ma_token"
	text := strings.TrimRight(strings.ReplaceAll(command, "\n", "; "), " \t\r;")
	if text != "" {
		text += "; "
	}
	text = "stty -echo\r" + text
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
			if bytes.Contains(captured.Bytes(), []byte(doneMarker)) {
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
	doneMarker := "__MICROAGENT_DONE_" + token + "__"
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
	if state.Event.Identity.Backend == vmkit.BackendWindowsHyperV {
		runtimeID := strings.TrimSpace(state.ComputeSystemRuntimeID)
		if runtimeID == "" {
			return ShellTarget{}, fmt.Errorf("windows-hyperv connect requires compute system runtime ID in runtime.json")
		}
		return ShellTarget{Network: "hvsock", RuntimeID: runtimeID, Port: port}, nil
	}
	return ShellTarget{Network: "tcp", Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), Port: port}, nil
}

func DialShellTarget(ctx context.Context, target ShellTarget) (net.Conn, error) {
	if target.Network == "hvsock" {
		return dialWindowsHyperVShell(ctx, target.RuntimeID, target.Port)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", target.Address)
}

func ShellTargetDescription(target ShellTarget) string {
	if target.Network == "hvsock" {
		return fmt.Sprintf("hvsock:%s:%d", target.RuntimeID, target.Port)
	}
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
	opts.SendTimeout = timeout
	err = sendConsoleCommandOnConn(conn, opts, ":", io.Discard)
	_ = conn.Close()
	return time.Since(start), err
}

func dialConsoleShell(ctx context.Context, opts ConsoleOptions) (net.Conn, error) {
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
	if opts.ReadyTimeout <= 0 {
		conn, err := dialTarget(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("guest shell is not ready for workspace %s at %s: %w", opts.Name, ShellTargetDescription(target), err)
		}
		return conn, nil
	}
	deadline := time.Now().Add(opts.ReadyTimeout)
	var lastErr error
	for {
		guestShellPort := state.Config.ShellPort
		if state.Config.GuestShellPort != 0 {
			guestShellPort = state.Config.GuestShellPort
		}
		if target.Network == "tcp" && guestShellPort != 0 && !shellHelperListening(state.SerialLogPath, guestShellPort) {
			lastErr = fmt.Errorf("guest shell helper is not listening on port %d", guestShellPort)
		} else {
			if opts.RequireCommandReady {
				probeTimeout := opts.SendTimeout
				if probeTimeout <= 0 || probeTimeout > time.Second {
					probeTimeout = time.Second
				}
				_, err := ProbeShellCommand(ctx, opts, target, probeTimeout, dialTarget)
				if err == nil {
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
