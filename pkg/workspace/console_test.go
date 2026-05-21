package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

var consoleSendTokenPattern = regexp.MustCompile(`__ma_token=([0-9]+)`)

func TestSendConsoleCommandReturnsTimeoutWithPartialOutput(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendFirecracker}
	req := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	req.Config.ShellPort = 24279
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialInputPath(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialLogPath(dir, name), []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("partial output\n"))
		time.Sleep(200 * time.Millisecond)
	}()
	dialTarget := func(_ctx context.Context, _target ShellTarget) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	}

	var output strings.Builder
	err = SendConsoleCommand(t.Context(), ConsoleOptions{
		StateDir:     dir,
		Name:         name,
		ReadyTimeout: time.Second,
		SendTimeout:  25 * time.Millisecond,
		DialTarget:   dialTarget,
	}, "echo hello", &output)
	<-done
	var timeoutErr ConsoleReadTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want ConsoleReadTimeoutError", err)
	}
	if timeoutErr.PartialOutput != "partial output\n" {
		t.Fatalf("partial output = %q", timeoutErr.PartialOutput)
	}
	if output.String() != "partial output\n" {
		t.Fatalf("writer output = %q", output.String())
	}
}

func TestSendConsoleCommandCompletionMarkerIsSuccess(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendFirecracker}
	req := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	req.Config.ShellPort = 24279
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialInputPath(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialLogPath(dir, name), []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		matches := consoleSendTokenPattern.FindStringSubmatch(string(buf[:n]))
		if len(matches) != 2 {
			return
		}
		_, _ = conn.Write([]byte("echo hello\r\nhello\r\n__MICROAGENT_DONE_" + matches[1] + "__0\r\n"))
	}()
	dialTarget := func(_ctx context.Context, _target ShellTarget) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	}

	var output strings.Builder
	if err := SendConsoleCommand(t.Context(), ConsoleOptions{
		StateDir:     dir,
		Name:         name,
		ReadyTimeout: time.Second,
		SendTimeout:  time.Second,
		DialTarget:   dialTarget,
	}, "true", &output); err != nil {
		t.Fatalf("SendConsoleCommand: %v", err)
	}
	<-done
	if output.String() != "echo hello\r\nhello\r\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSendConsoleCommandPreservesPromptLikeControlOutput(t *testing.T) {
	dir, name := writeRunningConsoleState(t)
	expected := "~ # prompt-like\r\nliteral carriage\rreturn\r\n\x1b[31mred\x1b[0m\r\n$PS1 > not a prompt\r\n"
	dialTarget, wait := scriptedConsoleDialer(t, []scriptedConsoleSession{{
		Output: expected,
	}})

	var output strings.Builder
	if err := SendConsoleCommand(t.Context(), ConsoleOptions{
		StateDir:     dir,
		Name:         name,
		ReadyTimeout: time.Second,
		SendTimeout:  time.Second,
		DialTarget:   dialTarget,
	}, "printf prompt-like", &output); err != nil {
		t.Fatalf("SendConsoleCommand: %v", err)
	}
	wait()
	if output.String() != expected {
		t.Fatalf("output bytes = %q, want %q", output.String(), expected)
	}
}

func TestSendConsoleCommandClosesSessionBeforeNextSend(t *testing.T) {
	dir, name := writeRunningConsoleState(t)
	dialTarget, wait := scriptedConsoleDialer(t, []scriptedConsoleSession{
		{Output: "echo-state=on\r\n"},
		{Output: "echo-state=on\r\n"},
	})

	for i := 0; i < 2; i++ {
		var output strings.Builder
		if err := SendConsoleCommand(t.Context(), ConsoleOptions{
			StateDir:     dir,
			Name:         name,
			ReadyTimeout: time.Second,
			SendTimeout:  time.Second,
			DialTarget:   dialTarget,
		}, "stty -a | grep -q echo && printf echo-state=on", &output); err != nil {
			t.Fatalf("SendConsoleCommand %d: %v", i+1, err)
		}
		if output.String() != "echo-state=on\r\n" {
			t.Fatalf("output %d = %q, want echo-state=on", i+1, output.String())
		}
	}
	wait()
}

type scriptedConsoleSession struct {
	Output string
}

func writeRunningConsoleState(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	name := "agent"
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendFirecracker}
	req := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	req.Config.ShellPort = 24279
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialInputPath(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialLogPath(dir, name), []byte("microagent-init: shell helper listening on vsock port 24279\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, name
}

func scriptedConsoleDialer(t *testing.T, sessions []scriptedConsoleSession) (func(context.Context, ShellTarget) (net.Conn, error), func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer listener.Close()
		for i, session := range sessions {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			if err := serveScriptedConsoleSession(conn, session); err != nil {
				done <- fmt.Errorf("session %d: %w", i+1, err)
				return
			}
		}
		done <- nil
	}()
	dialTarget := func(_ctx context.Context, _target ShellTarget) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	}
	wait := func() {
		t.Helper()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	return dialTarget, wait
}

func serveScriptedConsoleSession(conn net.Conn, session scriptedConsoleSession) error {
	defer conn.Close()
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	if !strings.Contains(string(buf[:n]), "stty -echo") {
		return fmt.Errorf("send command did not disable terminal echo: %q", string(buf[:n]))
	}
	matches := consoleSendTokenPattern.FindStringSubmatch(string(buf[:n]))
	if len(matches) != 2 {
		return fmt.Errorf("send command did not include completion token: %q", string(buf[:n]))
	}
	if _, err := conn.Write([]byte(session.Output + "__MICROAGENT_DONE_" + matches[1] + "__0\r\n")); err != nil {
		return err
	}
	buf = make([]byte, 1)
	_, err = conn.Read(buf)
	if err != io.EOF {
		return fmt.Errorf("session was not closed after completion marker: %v", err)
	}
	return nil
}
