package secretxfer

import (
	"net"
	"testing"
	"time"
)

func TestSendControlOK(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	var gotOp string
	go func() {
		var req ControlRequest
		if err := DecodeMessage(server, &req); err != nil {
			return
		}
		gotOp = req.Op
		_ = EncodeMessage(server, ControlResponse{ProtocolVersion: ProtocolVersion, OK: true})
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := SendControl(client, client, OpPurge); err != nil {
		t.Fatalf("SendControl: %v", err)
	}
	if gotOp != OpPurge {
		t.Fatalf("op = %q, want %q", gotOp, OpPurge)
	}
}

func TestSendControlPropagatesError(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	go func() {
		var req ControlRequest
		_ = DecodeMessage(server, &req)
		_ = EncodeMessage(server, ControlResponse{ProtocolVersion: ProtocolVersion, OK: false, Error: "boom"})
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := SendControl(client, client, OpRehydrate); err == nil {
		t.Fatal("expected error when the agent reports failure")
	}
}
