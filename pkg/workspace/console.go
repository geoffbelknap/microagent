package workspace

import (
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
	StateDir     string
	Name         string
	ReadyTimeout time.Duration
	SendTimeout  time.Duration
	DialTarget   func(context.Context, ShellTarget) (net.Conn, error)
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
	text := strings.ReplaceAll(command, "\n", "\r")
	if !strings.HasSuffix(text, "\r") {
		text += "\r"
	}
	text += "exit\r"
	if _, err := io.WriteString(conn, text); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(opts.SendTimeout))
	_, err = io.Copy(output, conn)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return nil
	}
	return err
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
		if target.Network == "tcp" && state.Config.ShellPort != 0 && !shellHelperListening(state.SerialLogPath, state.Config.ShellPort) {
			lastErr = fmt.Errorf("guest shell helper is not listening on port %d", state.Config.ShellPort)
		} else {
			dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			conn, err := dialTarget(dialCtx, target)
			cancel()
			if err == nil {
				return conn, nil
			}
			lastErr = err
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
