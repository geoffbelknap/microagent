package modelservice

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/modelrunner"
)

func TestResolverTracksRunnerAndWarnsOnceOnFallback(t *testing.T) {
	dir := t.TempDir()
	const ref = "hf.co/test/model@main/model.gguf"
	var warnings bytes.Buffer
	resolve := UpstreamResolver(dir, "paired", ref, &warnings)
	write := func(runners ...modelrunner.Record) {
		t.Helper()
		if err := modelrunner.WriteIndex(dir, modelrunner.Index{Runners: runners}); err != nil {
			t.Fatal(err)
		}
	}
	paired := modelrunner.Record{Key: "paired", ModelRef: ref, Host: "127.0.0.1", Port: 31002, PID: os.Getpid()}
	alternative := modelrunner.Record{Key: "alternative", ModelRef: ref, Host: "127.0.0.1", Port: 31001, PID: os.Getpid()}
	write(alternative, paired)
	if got := resolve(); got != "127.0.0.1:31002" {
		t.Fatalf("paired = %q", got)
	}
	paired.Port = 31003
	write(alternative, paired)
	if got := resolve(); got != "127.0.0.1:31003" {
		t.Fatalf("restart = %q", got)
	}
	write(alternative)
	for range 2 {
		if got := resolve(); got != "127.0.0.1:31001" {
			t.Fatalf("fallback = %q", got)
		}
	}
	if strings.Count(warnings.String(), "fallback runner") != 1 {
		t.Fatalf("warnings = %q", warnings.String())
	}
	write()
	if got := resolve(); got != "" {
		t.Fatalf("missing runner = %q", got)
	}
	if err := os.WriteFile(modelrunner.IndexPath(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got != "" {
		t.Fatalf("invalid registry = %q", got)
	}
}

func TestAttachRejectsPolicyInForwardMode(t *testing.T) {
	for _, policy := range []Options{{PolicyURL: "http://127.0.0.1:1"}, {PolicyFile: "policy.json"}} {
		policy.StateDir, policy.WorkspaceID, policy.ExecPath = t.TempDir(), "test", "/unused"
		policy.Runner = modelrunner.Record{Key: "runner", ModelRef: "model", Host: "127.0.0.1", Port: 31000}
		if _, err := Attach(t.Context(), policy); err == nil || !strings.Contains(err.Error(), "cannot enforce") {
			t.Fatalf("forward policy error = %v", err)
		}
	}
}
