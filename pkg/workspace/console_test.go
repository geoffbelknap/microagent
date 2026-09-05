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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/internal/consoleproto"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

var consoleSendTokenPattern = regexp.MustCompile(`__ma_token=([0-9]+)`)

func TestNegotiatedConsoleResize(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = server.Close() })
	gotResize := make(chan consoleproto.Resize, 1)
	go func() {
		_, _ = io.WriteString(server, consoleproto.CapabilityV1)
		_, _ = consoleproto.CopyInput(io.Discard, server, func(resize consoleproto.Resize) error {
			gotResize <- resize
			return nil
		})
	}()

	conn := negotiateConsoleConnection(client)
	supported, err := ResizeConsole(conn, 47, 132)
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Fatal("resize not supported after v1 capability")
	}
	select {
	case got := <-gotResize:
		if got != (consoleproto.Resize{Rows: 47, Cols: 132}) {
			t.Fatalf("resize = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("resize frame was not received")
	}
}

func TestLegacyConsolePreservesOutputAndDoesNotResize(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = server.Close() })
	go func() { _, _ = io.WriteString(server, "legacy prompt") }()
	conn := negotiateConsoleConnection(client)
	supported, err := ResizeConsole(conn, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	if supported {
		t.Fatal("legacy console unexpectedly supports resize")
	}
	buffer := make([]byte, len("legacy prompt"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "legacy prompt" {
		t.Fatalf("output = %q", buffer)
	}
}

func TestDialConsoleRejectsPausedWorkspace(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	req, err := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteProcessState(opts, req, vmkit.StatePaused, 123, ""); err != nil {
		t.Fatal(err)
	}
	_, err = DialConsole(context.Background(), ConsoleOptions{StateDir: dir, Name: name})
	if err == nil || !strings.Contains(err.Error(), "paused; resume it first") {
		t.Fatalf("err = %v, want paused; resume it first", err)
	}
}

func TestSendConsoleCommandReturnsTimeoutWithPartialOutput(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	req, err := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
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
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	req, err := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
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
		_, _ = conn.Write([]byte("__MICROAGENT_BEGIN_" + matches[1] + "__echo hello\r\nhello\r\n__MICROAGENT_DONE_" + matches[1] + "__0\r\n"))
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

func TestSendConsoleCommandEOFBeforeMarkerIsError(t *testing.T) {
	dir, name := writeRunningConsoleState(t)
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
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte("partial output\r\n"))
	}()
	dialTarget := func(_ctx context.Context, _target ShellTarget) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	}

	var output strings.Builder
	err = SendConsoleCommand(t.Context(), ConsoleOptions{
		StateDir:     dir,
		Name:         name,
		ReadyTimeout: time.Second,
		SendTimeout:  time.Second,
		DialTarget:   dialTarget,
	}, "echo hello", &output)
	<-done
	var unknownErr ConsoleCompletionUnknownError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("err = %v, want ConsoleCompletionUnknownError", err)
	}
	if unknownErr.PartialOutput != "partial output\r\n" {
		t.Fatalf("partial output = %q", unknownErr.PartialOutput)
	}
	if output.String() != "partial output\r\n" {
		t.Fatalf("writer output = %q", output.String())
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

func TestSendConsoleCommandPreservesControlVariableText(t *testing.T) {
	dir, name := writeRunningConsoleState(t)
	expected := "persistedhost-copied __ma_status=example\r\n__ma_token is user output\r\nstty -echo is user output\r\n"
	dialTarget, wait := scriptedConsoleDialer(t, []scriptedConsoleSession{{Output: expected}})
	var output strings.Builder
	if err := SendConsoleCommand(t.Context(), ConsoleOptions{
		StateDir: dir, Name: name, ReadyTimeout: time.Second,
		SendTimeout: time.Second, DialTarget: dialTarget,
	}, "printf data", &output); err != nil {
		t.Fatal(err)
	}
	wait()
	if output.String() != expected {
		t.Fatalf("command output was filtered: got %q, want %q", output.String(), expected)
	}
}

func TestConsoleCommandOutputBoundaries(t *testing.T) {
	const token = "123456"
	const begin = "__MICROAGENT_BEGIN_" + token + "__"
	const done = "__MICROAGENT_DONE_" + token + "__"
	for _, tc := range []struct{ name, raw, want string }{
		{"echo and output share line", "~ # echoed __ma_token=123456; " + begin + "persistedhost-copied\r\n" + done + "0\r\n", "persistedhost-copied\r\n"},
		{"unterminated command output", begin + "persisted" + done + "0", "persisted"},
		{"partial output after begin", begin + "__ma_status=user data\r\n", "__ma_status=user data\r\n"},
		{"empty command output", begin + done + "0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanConsoleSendOutput(tc.raw, token); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
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

func TestCommandReadinessRejectsAcceptThenClose(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	req, err := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	req.Config.ShellPort = uint16(port)
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialInputPath(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialLogPath(dir, name), []byte(fmt.Sprintf("microagent-init: shell helper listening on vsock port %d\n", port)), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	state, err := ReadRuntimeState(Options{StateDir: dir, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	signal, ok := ShellReadinessSignalWithMode(t.Context(), state, time.Second, ShellReadinessProbeCommand)
	<-done
	if !ok {
		t.Fatal("readiness signal not reported")
	}
	if signal.Ready {
		t.Fatalf("Ready = true, want false; detail=%q", signal.Detail)
	}
	if !strings.Contains(signal.Detail, "command probe failed") {
		t.Fatalf("Detail = %q, want command probe failure", signal.Detail)
	}
}

func TestDialConsoleProbesRestoredShellWithoutHistoricalSerialMarker(t *testing.T) {
	dir := t.TempDir()
	name := "restored"
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	req, err := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	req.Config.ShellPort = 32001
	req.Config.GuestShellPort = 31001
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialInputPath(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SerialLogPath(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	var calls int
	var returnedServer net.Conn
	dialTarget := func(_ context.Context, _ ShellTarget) (net.Conn, error) {
		calls++
		client, server := net.Pipe()
		if calls == 1 {
			go func() {
				_ = serveScriptedConsoleSession(server, scriptedConsoleSession{})
			}()
		} else {
			returnedServer = server
		}
		return client, nil
	}

	conn, err := DialConsole(t.Context(), ConsoleOptions{
		StateDir:     dir,
		Name:         name,
		ReadyTimeout: time.Second,
		SendTimeout:  time.Second,
		DialTarget:   dialTarget,
	})
	if err != nil {
		t.Fatalf("DialConsole: %v", err)
	}
	defer conn.Close()
	defer returnedServer.Close()
	if calls != 2 {
		t.Fatalf("dial calls = %d, want readiness probe plus returned session", calls)
	}
}

func TestWaitConsoleCommandReadyDoesNotOpenReturnedSession(t *testing.T) {
	dir, name := writeRunningConsoleState(t)
	var calls int
	dialTarget := func(_ context.Context, _ ShellTarget) (net.Conn, error) {
		calls++
		client, server := net.Pipe()
		go func() {
			_ = serveScriptedConsoleSession(server, scriptedConsoleSession{})
		}()
		return client, nil
	}

	err := WaitConsoleCommandReady(t.Context(), ConsoleOptions{
		StateDir:     dir,
		Name:         name,
		ReadyTimeout: time.Second,
		SendTimeout:  time.Second,
		DialTarget:   dialTarget,
	})
	if err != nil {
		t.Fatalf("WaitConsoleCommandReady: %v", err)
	}
	if calls != 1 {
		t.Fatalf("dial calls = %d, want readiness probe only", calls)
	}
}

type scriptedConsoleSession struct {
	Output string
}

func writeRunningConsoleState(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	name := "agent"
	opts := Options{Name: name, StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	req, err := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := conn.Write([]byte("echoed __ma_status __ma_token wrapper\r\n__MICROAGENT_BEGIN_" + matches[1] + "__" + session.Output + "__MICROAGENT_DONE_" + matches[1] + "__0\r\n")); err != nil {
		return err
	}
	buf = make([]byte, 1)
	_, err = conn.Read(buf)
	if err != io.EOF {
		return fmt.Errorf("session was not closed after completion marker: %v", err)
	}
	return nil
}
