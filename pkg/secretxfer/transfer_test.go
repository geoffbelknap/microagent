package secretxfer

import (
	"net"
	"testing"
	"time"
)

func TestServeAndFetchOverPipe(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })

	bundle := Bundle{
		ProtocolVersion: ProtocolVersion,
		Secrets:         []Entry{{Name: "API_KEY", Value: []byte("sekret")}},
	}
	errCh := make(chan error, 1)
	go func() { errCh <- ServeBundle(server, server, bundle) }()

	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	got, err := FetchBundle(client)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Secrets) != 1 || got.Secrets[0].Name != "API_KEY" || string(got.Secrets[0].Value) != "sekret" {
		t.Fatalf("unexpected bundle: %+v", got)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestFetchRejectsWrongProtocol(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	go func() {
		var req Request
		_ = DecodeMessage(server, &req)
		_ = EncodeMessage(server, Bundle{ProtocolVersion: "secrets.v999"})
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := FetchBundle(client); err == nil {
		t.Fatal("expected protocol-version mismatch error")
	}
}
