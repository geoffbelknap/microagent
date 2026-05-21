package workspace

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

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
		matches := regexp.MustCompile(`__ma_token=([0-9]+)`).FindStringSubmatch(string(buf[:n]))
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
