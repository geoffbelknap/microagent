package client

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestClientExecRoundTrip(t *testing.T) {
	addr, stop := startExecProtocolServer(t, func(conn net.Conn) {
		var req protocol.ExecRequest
		if err := protocol.DecodeMessage(conn, &req); err != nil {
			t.Errorf("DecodeMessage request: %v", err)
			return
		}
		if len(req.Argv) != 2 || req.Argv[0] != "echo" || req.Argv[1] != "hello" {
			t.Errorf("argv = %#v, want echo hello", req.Argv)
		}
		code := 0
		result := protocol.NewExecResult(protocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stdout = []byte("hello\n")
		if err := protocol.EncodeMessage(conn, result); err != nil {
			t.Errorf("EncodeMessage result: %v", err)
		}
	})
	defer stop()

	result, err := New(addr).Exec(context.Background(), protocol.NewExecRequest([]string{"echo", "hello"}))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Status != protocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 || string(result.Stdout) != "hello\n" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientExecDialFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = New(addr).Exec(context.Background(), protocol.NewExecRequest([]string{"true"}))
	var unreachable UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("err = %T %v, want UnreachableError", err, err)
	}
}

func TestClientExecDecodeFailure(t *testing.T) {
	addr, stop := startExecProtocolServer(t, func(conn net.Conn) {
		var req protocol.ExecRequest
		if err := protocol.DecodeMessage(conn, &req); err != nil {
			t.Errorf("DecodeMessage request: %v", err)
			return
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], 4)
		_, _ = conn.Write(prefix[:])
		_, _ = conn.Write([]byte("{bad"))
	})
	defer stop()

	_, err := New(addr).Exec(context.Background(), protocol.NewExecRequest([]string{"true"}))
	var protocolErr ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("err = %T %v, want ProtocolError", err, err)
	}
}

func TestClientExecContextCancellationMidCall(t *testing.T) {
	addr, stop := startExecProtocolServer(t, func(conn net.Conn) {
		var req protocol.ExecRequest
		_ = protocol.DecodeMessage(conn, &req)
		time.Sleep(time.Second)
	})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := New(addr).Exec(ctx, protocol.NewExecRequest([]string{"true"}))
	if err == nil {
		t.Fatal("Exec err = nil, want context/decode deadline error")
	}
	var protocolErr ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("err = %T %v, want ProtocolError", err, err)
	}
}

func startExecProtocolServer(t *testing.T, handle func(net.Conn)) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				t.Errorf("Accept: %v", err)
			}
			return
		}
		defer conn.Close()
		handle(conn)
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
		<-done
	}
}
