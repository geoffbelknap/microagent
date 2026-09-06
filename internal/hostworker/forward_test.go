package hostworker

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestForwardPreservesBytesAndHalfClose(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	// A response produced only after EOF proves the bridge preserves half-close.
	payload := bytes.Repeat([]byte{0, 255, 13, 10}, (33<<20)/4)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		data, err := io.ReadAll(conn)
		if err == nil && !bytes.Equal(data, payload) {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			_, err = conn.Write([]byte("after-eof"))
		}
		serverDone <- err
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveForward(ctx, listener, upstream.Addr().String(), nil, nil) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil || string(response) != "after-eof" {
		t.Fatalf("response %q, %v", response, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	// Shutdown must disconnect an idle client rather than hang on its read.
	accepted := make(chan struct{})
	idleDone := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		close(accepted)
		if err != nil {
			idleDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = io.ReadAll(conn)
		idleDone <- err
	}()
	idle, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("idle client never reached upstream")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forwarder did not close active connections")
	}
	if err := <-idleDone; err != nil {
		t.Fatalf("upstream was not disconnected: %v", err)
	}
}

func TestForwardRejectsPolicy(t *testing.T) {
	for _, opts := range []Options{
		{Mode: ModeForward, PolicyURL: "http://127.0.0.1:1"},
		{Mode: ModeForward, PolicyFile: "policy.json"},
	} {
		if err := Run(t.Context(), opts); err == nil {
			t.Fatal("forwarder accepted a policy it cannot enforce")
		}
	}
}
