//go:build linux

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
)

func TestHandleControlConnPurge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "API_KEY"), []byte("sekret"), 0o400); err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	purged := false
	go handleControlConn(server,
		func() error { purged = true; return secretxfer.PurgeSecrets(root) },
		func() error { return nil })
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := secretxfer.SendControl(client, client, secretxfer.OpPurge); err != nil {
		t.Fatalf("SendControl: %v", err)
	}
	if !purged {
		t.Fatal("purge handler not called")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("secrets not purged: %d entries", len(entries))
	}
}

func TestHandleControlConnRehydrate(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	rehydrated := false
	go handleControlConn(server,
		func() error { return nil },
		func() error { rehydrated = true; return nil })
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := secretxfer.SendControl(client, client, secretxfer.OpRehydrate); err != nil {
		t.Fatalf("SendControl: %v", err)
	}
	if !rehydrated {
		t.Fatal("rehydrate handler not called")
	}
}

func TestHandleControlConnUnknownOp(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	go handleControlConn(server, func() error { return nil }, func() error { return nil })
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := secretxfer.SendControl(client, client, "bogus"); err == nil {
		t.Fatal("expected error for unknown op")
	}
}
