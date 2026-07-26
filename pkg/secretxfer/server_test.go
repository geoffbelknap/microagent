package secretxfer

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestResolveBundleResolvesEnvRefsAndFailsClosed(t *testing.T) {
	t.Setenv("SECRETXFER_TEST_VALUE", "resolved-value")
	bundle, err := ResolveBundle(context.Background(), &vmkit.Config{
		Secrets: []vmkit.SecretRef{{Name: "API", Ref: "env:SECRETXFER_TEST_VALUE"}},
	})
	if err != nil {
		t.Fatalf("ResolveBundle: %v", err)
	}
	if len(bundle.Secrets) != 1 || bundle.Secrets[0].Name != "API" || string(bundle.Secrets[0].Value) != "resolved-value" {
		t.Fatalf("bundle = %#v", bundle)
	}

	if _, err := ResolveBundle(context.Background(), &vmkit.Config{
		Secrets: []vmkit.SecretRef{{Name: "MISSING", Ref: "env:SECRETXFER_TEST_UNSET_VALUE"}},
	}); err == nil {
		t.Fatal("unresolvable reference did not fail the bundle")
	}

	if _, err := ResolveBundle(context.Background(), &vmkit.Config{
		Secrets: []vmkit.SecretRef{
			{Name: "API", Ref: "env:SECRETXFER_TEST_VALUE"},
			{Name: "API", Ref: "env:SECRETXFER_TEST_VALUE"},
		},
	}); err == nil {
		t.Fatal("duplicate name did not fail the bundle")
	}
}

func TestServerDoesNotReleaseOnDemandSecretWhenAuditWriteFails(t *testing.T) {
	t.Setenv("SECRETXFER_TEST_ON_DEMAND", "must-not-leak")
	oldAppend := appendAccessRecord
	appendAccessRecord = func(string, AccessRecord) error {
		return errors.New("disk full")
	}
	t.Cleanup(func() { appendAccessRecord = oldAppend })

	srv := NewServer("ws", t.TempDir(), Bundle{}, map[string]string{"DB": "env:SECRETXFER_TEST_ON_DEMAND"}, true)
	client, server := net.Pipe()
	go srv.Handle(server)
	if err := EncodeMessage(client, Request{ProtocolVersion: ProtocolVersion, Name: "DB"}); err != nil {
		t.Fatal(err)
	}
	var response GetResponse
	if err := DecodeMessage(client, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	_ = client.Close()
	if response.Error != "secret access audit failed" || len(response.Value) != 0 {
		t.Fatalf("response = %#v, want audit failure without secret value", response)
	}
}

func TestServerServesMaterializedBundleAndOnDemand(t *testing.T) {
	t.Setenv("SECRETXFER_TEST_ON_DEMAND", "on-demand-value")
	dir := t.TempDir()
	bundle := Bundle{ProtocolVersion: ProtocolVersion, Secrets: []Entry{{Name: "API", Value: []byte("materialized")}}}
	srv := NewServer("ws", dir, bundle, map[string]string{"DB": "env:SECRETXFER_TEST_ON_DEMAND"}, true)

	// Materialized bundle: empty-name request returns every entry.
	client, server := net.Pipe()
	go srv.Handle(server)
	if err := EncodeMessage(client, Request{ProtocolVersion: ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var got Bundle
	if err := DecodeMessage(client, &got); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	_ = client.Close()
	if len(got.Secrets) != 1 || got.Secrets[0].Name != "API" || string(got.Secrets[0].Value) != "materialized" {
		t.Fatalf("bundle = %#v", got)
	}

	// On-demand by name resolves lazily.
	client, server = net.Pipe()
	go srv.Handle(server)
	if err := EncodeMessage(client, Request{ProtocolVersion: ProtocolVersion, Name: "DB"}); err != nil {
		t.Fatal(err)
	}
	var resp GetResponse
	if err := DecodeMessage(client, &resp); err != nil {
		t.Fatalf("decode on-demand: %v", err)
	}
	_ = client.Close()
	if resp.Error != "" || string(resp.Value) != "on-demand-value" {
		t.Fatalf("on-demand resp = %#v", resp)
	}

	// Undeclared on-demand names are denied without resolution.
	client, server = net.Pipe()
	go srv.Handle(server)
	if err := EncodeMessage(client, Request{ProtocolVersion: ProtocolVersion, Name: "NOPE"}); err != nil {
		t.Fatal(err)
	}
	var denied GetResponse
	if err := DecodeMessage(client, &denied); err != nil {
		t.Fatalf("decode denied: %v", err)
	}
	_ = client.Close()
	if denied.Error == "" || len(denied.Value) != 0 {
		t.Fatalf("denied resp = %#v", denied)
	}

	// Audit recorded each access without leaking values.
	data, err := os.ReadFile(AccessLogPath(dir, "ws"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	log := string(data)
	for _, needle := range []string{"API", "materialize", "DB", "on-demand", "NOPE", "denied"} {
		if !strings.Contains(log, needle) {
			t.Fatalf("audit log missing %q:\n%s", needle, log)
		}
	}
	for _, leaked := range []string{"materialized", "on-demand-value"} {
		if strings.Contains(log, leaked) {
			t.Fatalf("audit log leaked value %q:\n%s", leaked, log)
		}
	}
}
