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

func TestWriteFetchedSecretsHappyPath(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	bundle := secretxfer.Bundle{
		ProtocolVersion: secretxfer.ProtocolVersion,
		Secrets:         []secretxfer.Entry{{Name: "API_KEY", Value: []byte("sekret")}},
	}
	go func() {
		_ = secretxfer.ServeBundle(server, server, bundle)
	}()
	root := filepath.Join(t.TempDir(), "secrets")
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeFetchedSecrets(client, root); err != nil {
		t.Fatalf("writeFetchedSecrets: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "API_KEY"))
	if err != nil || string(data) != "sekret" {
		t.Fatalf("API_KEY not written correctly: %q err=%v", data, err)
	}
}

func TestWriteFetchedSecretsPropagatesServerClose(t *testing.T) {
	server, client := net.Pipe()
	server.Close()
	t.Cleanup(func() { client.Close() })
	root := filepath.Join(t.TempDir(), "secrets")
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if err := writeFetchedSecrets(client, root); err == nil {
		t.Fatal("expected error when the server closes without a bundle")
	}
}
