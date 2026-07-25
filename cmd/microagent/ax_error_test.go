package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
)

// TestSubstringClassifierInventory pins the exact fallback text-matching
// table mapStructuredError falls back to once every typed check has missed.
// mapStructuredError's substring tail reads this same table (see
// matchSubstringClassifierRule), so this test cannot drift from the actual
// classifier behavior by accident: editing a pattern, kind, remediation, or
// retry_after_ms here without also editing substringClassifierPatterns (or
// vice versa) fails this test. Extend the typed section of mapStructuredError
// instead of this table whenever a typed error is available for a category;
// rewording an upstream error that a rule below relies on silently
// reclassifies its retryability with no compiler signal to catch it.
func TestSubstringClassifierInventory(t *testing.T) {
	want := []substringClassifierRule{
		{
			Patterns:    []string{"not found", "no such file"},
			Kind:        errorKindNotFound,
			Remediation: "Check the workspace name, file path, image reference, or state directory and retry.",
		},
		{
			Patterns:    []string{"already ", "cannot start from state", "not running", "specified twice", "cancelled"},
			Kind:        errorKindConflict,
			Remediation: "Inspect current state, resolve the conflicting operation, and retry.",
		},
		{
			Patterns:    []string{"not supported", "unsupported", "not available"},
			Kind:        errorKindUnsupported,
			Remediation: "Choose a supported backend, command, flag, or host configuration.",
		},
		{
			Patterns:    []string{"permission denied", "access denied", "requires administrator"},
			Kind:        errorKindPolicyDenied,
			Remediation: "Run with the required host permissions or adjust the host policy outside microagent.",
		},
		{
			Patterns:     []string{"timeout", "temporar", "unreachable", "connection refused", "connection reset"},
			Kind:         errorKindTransient,
			Remediation:  "Retry after the host resource or runtime service becomes available.",
			RetryAfterMS: 1000,
		},
		{
			Patterns:    []string{"no space", "too large", "exceeds"},
			Kind:        errorKindResourceExhausted,
			Remediation: "Free host resources, lower the requested size, or increase the configured limit.",
		},
		{
			Patterns:    []string{"usage:", "requires ", "unexpected ", "must "},
			Kind:        errorKindPermanent,
			Remediation: "Correct the command arguments and retry.",
		},
	}
	got := substringClassifierPatterns()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("substringClassifierPatterns() drifted from the pinned inventory:\ngot:  %#v\nwant: %#v", got, want)
	}
}

// TestTypedErrorClassification covers every typed check mapStructuredError
// added ahead of the substring tail in this task, one real error value per
// type. flag.ErrHelp, workspace.ConsoleReadTimeoutError,
// workspace.ConsoleCompletionUnknownError, and workspace.WorkspaceNotFoundError
// were already typed before this task and are covered by
// TestStructuredErrorMapping in main_test.go; they are not repeated here.
func TestTypedErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		kind      structuredErrorKind
		retryable bool
	}{
		{
			// Isolation proof for the os.ErrNotExist typed check: this subtest
			// fails if the typed check is removed, because fs.PathError text
			// "file does not exist" avoids every substring pattern ("not found",
			// "no such file", etc.) and cannot fall through to the substring tail.
			name:      "os.ErrNotExist wrapped in fs.PathError",
			err:       &fs.PathError{Op: "open", Path: "/no/such/request.json", Err: os.ErrNotExist},
			kind:      errorKindNotFound,
			retryable: false,
		},
		{
			name:      "os.ErrPermission wrapped in fs.PathError",
			err:       &fs.PathError{Op: "open", Path: "/no/access", Err: os.ErrPermission},
			kind:      errorKindPolicyDenied,
			retryable: false,
		},
		{
			name:      "workspace.WaitTimeoutError",
			err:       workspace.WaitTimeoutError{Name: "research", Timeout: time.Second, LastState: vmkit.StateRunning},
			kind:      errorKindTransient,
			retryable: true,
		},
		{
			name:      "workspace.ExecRetryExhaustedError",
			err:       workspace.ExecRetryExhaustedError{Retries: 3, WallClock: time.Second, LastErr: execclient.UnreachableError{Addr: "127.0.0.1:1", Err: errors.New("dial failed")}},
			kind:      errorKindTransient,
			retryable: true,
		},
		{
			name:      "execclient.UnreachableError",
			err:       execclient.UnreachableError{Addr: "127.0.0.1:1", Err: errors.New("dial failed")},
			kind:      errorKindTransient,
			retryable: true,
		},
		{
			name:      "vmkit.UnsupportedFeatureError",
			err:       vmkit.UnsupportedFeatureError{Backend: "apple-vf", FeatureID: "workspace.snapshot", Operation: "snapshot create", Reason: "backend gap"},
			kind:      errorKindUnsupported,
			retryable: false,
		},
		{
			// Typed check for context.DeadlineExceeded changed classification
			// for bare ctx.Err() returns: previously permanent/retryable=false
			// (text "context deadline exceeded" matched no substring pattern),
			// now transient/retryable=true (intentional — deadline expiry is
			// retryable). Bare constructor (not wrapped) ensures the typed path
			// is exercised. Reachable from pkg/workspace/wait.go:98,
			// pkg/workspace/supervise.go:73/170/195, pkg/workspace/console.go:300,
			// pkg/workspace/exec.go:178, cmd/microagent/console.go:42,
			// cmd/microagent/mcp.go:81.
			name:      "context.DeadlineExceeded",
			err:       context.DeadlineExceeded,
			kind:      errorKindTransient,
			retryable: true,
		},
		{
			name:      "net.Error timeout",
			err:       fakeNetTimeoutError{},
			kind:      errorKindTransient,
			retryable: true,
		},
		{
			name:      "context.Canceled",
			err:       context.Canceled,
			kind:      errorKindPermanent,
			retryable: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapStructuredError(tt.err, "req-test")
			if got.Kind != tt.kind {
				t.Fatalf("Kind = %q, want %q (err %v)", got.Kind, tt.kind, tt.err)
			}
			if got.Retryable != tt.retryable {
				t.Fatalf("Retryable = %v, want %v (err %v)", got.Retryable, tt.retryable, tt.err)
			}
			if got.Remediation == "" {
				t.Fatalf("Remediation is empty (err %v)", tt.err)
			}
		})
	}
}

func TestOperationErrorClassificationIgnoresMessageWording(t *testing.T) {
	tests := []struct {
		name       string
		operation  operation.ErrorKind
		structured structuredErrorKind
		retryable  bool
	}{
		{name: "validation", operation: operation.ErrorValidation, structured: errorKindPermanent},
		{name: "conflict", operation: operation.ErrorConflict, structured: errorKindConflict},
		{name: "not found", operation: operation.ErrorNotFound, structured: errorKindNotFound},
		{name: "resource exhausted", operation: operation.ErrorResourceExhausted, structured: errorKindResourceExhausted, retryable: true},
		{name: "unsupported", operation: operation.ErrorUnsupported, structured: errorKindUnsupported},
		{name: "policy denied", operation: operation.ErrorPolicyDenied, structured: errorKindPolicyDenied},
		{name: "transient", operation: operation.ErrorTransient, structured: errorKindTransient, retryable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, message := range []string{"opaque failure alpha", "entirely different wording beta"} {
				got := mapStructuredError(operation.New(tt.operation, "%s", message), "req-test")
				if got.Kind != tt.structured || got.Retryable != tt.retryable {
					t.Fatalf("mapStructuredError(%q) = kind %q retryable %v, want %q/%v", message, got.Kind, got.Retryable, tt.structured, tt.retryable)
				}
			}
		})
	}
}

// fakeNetTimeoutError is a minimal net.Error whose Timeout() reports true, to
// exercise mapStructuredError's generic net.Error typed check independent of
// any concrete *net.OpError construction.
type fakeNetTimeoutError struct{}

func (fakeNetTimeoutError) Error() string   { return "fake i/o timeout" }
func (fakeNetTimeoutError) Timeout() bool   { return true }
func (fakeNetTimeoutError) Temporary() bool { return true }

// TestAXCreateMissingRequestJSONClassifiesNotFound pins the OBSERVABLE
// not_found classification end-to-end through the CLI. `create --request-json
// <missing path>` produces an AX error envelope with kind not_found. This test
// cannot serve as an isolation proof for the typed check vs. substring pattern
// because readRequest (os.ReadFile) returns an *fs.PathError whose Error() text
// includes "no such file", which matches the substring pattern directly.
// The genuine isolation proof is TestTypedErrorClassification's
// "os.ErrNotExist wrapped in fs.PathError" subtest: that constructed error's
// text "file does not exist" avoids every substring pattern and fails if the
// typed check is removed.
func TestAXCreateMissingRequestJSONClassifiesNotFound(t *testing.T) {
	missing := t.TempDir() + "/does-not-exist.json"
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test setup: %q unexpectedly exists", missing)
	}

	_, readErr := readRequest(missing)
	if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("readRequest(%q) err = %v, want an os.ErrNotExist chain", missing, readErr)
	}
	var pathErr *fs.PathError
	if !errors.As(readErr, &pathErr) {
		t.Fatalf("readRequest(%q) err = %#v, want an *fs.PathError in the chain", missing, readErr)
	}

	stdout, stderr, code := runMainCapture(t, "--mode=ax", "create", "--request-json", missing)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%s stderr=%s)", code, stdout, stderr)
	}
	if len(stderr) != 0 {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	var envelope axEnvelope
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		t.Fatalf("decode AX stdout %q: %v", stdout, err)
	}
	if envelope.OK {
		t.Fatalf("envelope.OK = true, want false")
	}
	if envelope.Error == nil || envelope.Error.Kind != errorKindNotFound {
		t.Fatalf("envelope.Error = %#v, want kind %q", envelope.Error, errorKindNotFound)
	}
}
