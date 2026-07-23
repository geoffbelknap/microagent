package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

// TestAXSuppressesResultEnvelope pins the one-document suppression logic (F1 +
// F2, Design Decision 2). Outside AX nothing is suppressed; under AX a non-nil
// error (the error envelope stands in) or a pending --wait (the wait-outcome
// envelope stands in) suppresses the intermediate result write.
func TestAXSuppressesResultEnvelope(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		mode    outputMode
		err     error
		waiting bool
		want    bool
	}{
		{"ux-error-nowait", outputModeUX, boom, false, false},
		{"ux-wait", outputModeUX, nil, true, false},
		{"ux-error-wait", outputModeUX, boom, true, false},
		{"ax-success-nowait", outputModeAX, nil, false, false},
		{"ax-error-nowait", outputModeAX, boom, false, true},
		{"ax-success-wait", outputModeAX, nil, true, true},
		{"ax-error-wait", outputModeAX, boom, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := axSuppressesResultEnvelope(tc.mode, tc.err, tc.waiting); got != tc.want {
				t.Fatalf("axSuppressesResultEnvelope(%q, err=%v, waiting=%v) = %v, want %v",
					tc.mode, tc.err, tc.waiting, got, tc.want)
			}
		})
	}
}

// TestAXRunFamilyFailureIsSingleDocument is the F1 regression guard: a failing
// run-family command emits exactly one document under AX — the {ok:false,error}
// envelope — never a result envelope followed by the error envelope. `run` with
// no image fails deterministically offline (bare `create` defaults to pulling an
// image and does real work, so it is unsuitable for an offline unit test).
func TestAXRunFamilyFailureIsSingleDocument(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	stdout, _, code := runMainCapture(t, "--mode=ax", "run", "--state-dir", stateDir)
	if code == 0 {
		t.Fatalf("expected nonzero exit for a run with no image, got 0; stdout=%q", stdout)
	}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	var env map[string]any
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode run-failure stdout %q: %v", stdout, err)
	}
	if dec.More() {
		t.Fatalf("run failure emitted more than one document: %q", stdout)
	}
	if env["ok"] != false {
		t.Fatalf("ok = %#v, want false; stdout=%q", env["ok"], stdout)
	}
	if _, hasResult := env["result"]; hasResult {
		t.Fatalf("result key present on failure envelope: %#v", env)
	}
}

// TestF3GCEnvelopeUnderAXBareUnderUX proves a former writeJSON bypasser (gc,
// which now routes through writeJSON) wraps under AX and stays bare under
// UX+--json. gc against an empty state dir needs no VM.
func TestF3GCEnvelopeUnderAXBareUnderUX(t *testing.T) {
	t.Run("ax-wraps", func(t *testing.T) {
		got := runGCStdout(t, "--mode=ax", "gc", "--state-dir", filepath.Join(t.TempDir(), "state"))
		if got["ok"] != true {
			t.Fatalf("ok = %#v, want true; got %#v", got["ok"], got)
		}
		result, ok := got["result"].(map[string]any)
		if !ok {
			t.Fatalf("result type = %T (%#v)", got["result"], got["result"])
		}
		if _, ok := result["checked"]; !ok {
			t.Fatalf("result.checked missing: %#v", result)
		}
	})
	t.Run("ux-bare", func(t *testing.T) {
		got := runGCStdout(t, "--json", "gc", "--state-dir", filepath.Join(t.TempDir(), "state"))
		if _, hasOK := got["ok"]; hasOK {
			t.Fatalf("UX+json gc output was wrapped unexpectedly: %#v", got)
		}
		if _, ok := got["checked"]; !ok {
			t.Fatalf("bare gc output missing checked: %#v", got)
		}
	})
}

// runGCStdout runs the given args through run() against a fresh stdout file and
// decodes the single JSON document it wrote.
func runGCStdout(t *testing.T, args ...string) map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if runErr := run(t.Context(), args, f); runErr != nil {
		t.Fatalf("run %v: %v", args, runErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return got
}

// TestAXTextSuccessRendersText is F4: under `--mode ax --output text` a command
// with a human rendering (version, gated by outputStructured) renders text, not
// a JSON envelope, while keeping AX exit semantics.
func TestAXTextSuccessRendersText(t *testing.T) {
	stdout, _, code := runMainCapture(t, "--mode=ax", "--output", "text", "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	trimmed := bytes.TrimSpace(stdout)
	if json.Valid(trimmed) {
		t.Fatalf("ax+text version rendered JSON, want human text: %q", stdout)
	}
	if !bytes.HasPrefix(trimmed, []byte("microagent ")) {
		t.Fatalf("ax+text version = %q, want a 'microagent <version>' text line", stdout)
	}
}

// TestAXTextFailureNoStdoutJSON is F4: under `--mode ax --output text` a failure
// prints the plain error to stderr and writes no JSON to stdout, but still exits
// nonzero (AX exit semantics preserved).
func TestAXTextFailureNoStdoutJSON(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	stdout, stderr, code := runMainCapture(t, "--mode=ax", "--output", "text", "status", "no-such-ws", "--state-dir", stateDir)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(bytes.TrimSpace(stdout)) != 0 {
		t.Fatalf("ax+text failure wrote to stdout, want empty: %q", stdout)
	}
	if len(bytes.TrimSpace(stderr)) == 0 {
		t.Fatalf("ax+text failure wrote nothing to stderr, want a plain error line")
	}
	if json.Valid(bytes.TrimSpace(stderr)) {
		t.Fatalf("ax+text failure stderr looks like JSON, want plain text: %q", stderr)
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
