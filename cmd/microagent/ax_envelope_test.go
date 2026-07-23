package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteJSONAXWrapsResult pins Design Decision 2: under the AX profile,
// writeJSON emits a single {ok:true, result:<value>} envelope rather than the
// bare value.
func TestWriteJSONAXWrapsResult(t *testing.T) {
	old := globalOutputMode
	t.Cleanup(func() { globalOutputMode = old })
	globalOutputMode = outputModeAX

	path := filepath.Join(t.TempDir(), "out.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(f, map[string]any{"hello": "world"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if env["ok"] != true {
		t.Fatalf("ok = %#v, want true", env["ok"])
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result type = %T (%#v)", env["result"], env["result"])
	}
	if result["hello"] != "world" {
		t.Fatalf("result.hello = %#v, want world", result["hello"])
	}
	if _, hasErr := env["error"]; hasErr {
		t.Fatalf("error key present on success envelope: %#v", env)
	}
}

// TestWriteJSONUXStaysBare guards that the plain --json (UX) profile keeps the
// bare body; only AX wraps.
func TestWriteJSONUXStaysBare(t *testing.T) {
	oldMode := globalOutputMode
	oldFmt := outputFormat
	t.Cleanup(func() {
		globalOutputMode = oldMode
		outputFormat = oldFmt
	})
	globalOutputMode = outputModeUX
	outputFormat = "json"

	path := filepath.Join(t.TempDir(), "out.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(f, map[string]any{"hello": "world"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if got["hello"] != "world" {
		t.Fatalf("bare output = %#v, want {hello:world}", got)
	}
	if _, hasOK := got["ok"]; hasOK {
		t.Fatalf("UX+json output was wrapped unexpectedly: %#v", got)
	}
}

// TestAXEnvelopeListSuccess is the end-to-end success path: `--mode ax list`
// produces one ok:true envelope whose result carries the command body.
func TestAXEnvelopeListSuccess(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	stdoutPath := filepath.Join(dir, "stdout.json")
	f, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	runErr := run(t.Context(), []string{"--mode=ax", "list", "--state-dir", stateDir}, f)
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if runErr != nil {
		t.Fatalf("run list: %v", runErr)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if env["ok"] != true {
		t.Fatalf("ok = %#v, want true", env["ok"])
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result type = %T (%#v)", env["result"], env["result"])
	}
	if _, ok := result["workspaces"]; !ok {
		t.Fatalf("result.workspaces missing: %#v", result)
	}
}

// TestAXEnvelopeErrorOnStdout is the end-to-end failure path at the main level:
// an AX error is a single ok:false envelope on STDOUT (not stderr), stderr
// carries no JSON, and the process exit code is 1 (microagent itself failed).
func TestAXEnvelopeErrorOnStdout(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	stdout, stderr, code := runMainCapture(t, "--mode=ax", "status", "no-such-ws", "--state-dir", stateDir)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	var env map[string]any
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if env["ok"] != false {
		t.Fatalf("ok = %#v, want false", env["ok"])
	}
	if _, hasResult := env["result"]; hasResult {
		t.Fatalf("result key present on error envelope: %#v", env)
	}
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("error type = %T (%#v)", env["error"], env["error"])
	}
	if errObj["kind"] != string(errorKindNotFound) {
		t.Fatalf("error.kind = %#v, want %q", errObj["kind"], errorKindNotFound)
	}

	if json.Valid(bytes.TrimSpace(stderr)) && len(bytes.TrimSpace(stderr)) > 0 {
		t.Fatalf("stderr contained JSON, want none: %q", stderr)
	}
}

// TestAXEnvelopeErrorIsSingleDocument enforces the one-document contract: the
// failing case's stdout decodes exactly once, with nothing trailing.
func TestAXEnvelopeErrorIsSingleDocument(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	stdout, _, _ := runMainCapture(t, "--mode=ax", "status", "no-such-ws", "--state-dir", stateDir)

	dec := json.NewDecoder(bytes.NewReader(stdout))
	var first any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("decode first document from %q: %v", stdout, err)
	}
	if dec.More() {
		t.Fatalf("stdout carried more than one JSON document: %q", stdout)
	}
}

// runMainCapture drives the main-level entry point (runMain) against captured
// stdout/stderr files, returning both streams and the process exit code. It is
// the harness for asserting the AX error-envelope-on-stdout policy that main
// owns (run alone returns the error without rendering it).
func runMainCapture(t *testing.T, args ...string) (stdout, stderr []byte, code int) {
	t.Helper()
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout")
	stderrPath := filepath.Join(dir, "stderr")
	outFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	code = runMain(t.Context(), args, outFile, errFile)
	if closeErr := outFile.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := errFile.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	stdout, err = os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err = os.ReadFile(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	return stdout, stderr, code
}
