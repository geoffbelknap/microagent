package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
)

type structuredErrorKind string

const (
	errorKindTransient         structuredErrorKind = "transient"
	errorKindPermanent         structuredErrorKind = "permanent"
	errorKindConflict          structuredErrorKind = "conflict"
	errorKindNotFound          structuredErrorKind = "not_found"
	errorKindResourceExhausted structuredErrorKind = "resource_exhausted"
	errorKindUnsupported       structuredErrorKind = "unsupported"
	errorKindPolicyDenied      structuredErrorKind = "policy_denied"
)

type structuredError struct {
	Kind          structuredErrorKind `json:"kind"`
	Message       string              `json:"message"`
	Remediation   string              `json:"remediation,omitempty"`
	Retryable     bool                `json:"retryable"`
	RetryAfterMS  int64               `json:"retry_after_ms,omitempty"`
	PartialOutput string              `json:"partial_output,omitempty"`
	CorrelationID string              `json:"correlation_id"`
}

// axEnvelope is the single AX-profile response document. Every AX response is
// exactly one of these on stdout: {ok:true, result:<value>} on success or
// {ok:false, error:{...}} on failure. The two payload fields are mutually
// exclusive (omitempty keeps the unused one out of the wire form).
type axEnvelope struct {
	OK     bool             `json:"ok"`
	Result any              `json:"result,omitempty"`
	Error  *structuredError `json:"error,omitempty"`
}

func writeAXErrorTo(w io.Writer, err error) error {
	if err == nil {
		return nil
	}
	mapped := mapStructuredError(err, newRequestID())
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(axEnvelope{OK: false, Error: &mapped})
}

// mapStructuredError classifies err into the AX structured-error shape.
//
// Typed checks (errors.Is/errors.As against a real Go error type or a
// standard-library sentinel) are the primary classification path and always
// run first: they cannot be defeated by rewording an upstream error's message
// text. The substring tail below only runs once every typed check has
// missed, for the categories that have no corresponding Go type to check
// (CLI usage errors, state-conflict messages assembled inline, etc). Adding
// or growing a typed check here is the correct fix for a category that is
// currently substring-only; editing the substring tail is not.
func mapStructuredError(err error, correlationID string) (mapped structuredError) {
	mapped = structuredError{
		Kind:          errorKindPermanent,
		Message:       err.Error(),
		CorrelationID: correlationID,
	}
	defer func() {
		mapped.Retryable = structuredErrorKindRetryable(mapped.Kind)
	}()

	if errors.Is(err, flag.ErrHelp) {
		mapped.Remediation = "Re-run the command without --help, or choose one of the documented command forms."
		return mapped
	}
	var consoleTimeout workspace.ConsoleReadTimeoutError
	if errors.As(err, &consoleTimeout) {
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.PartialOutput = consoleTimeout.PartialOutput
		mapped.Remediation = "Retry after the guest shell has produced a complete response, or increase the connect timeout."
		return mapped
	}
	var consoleUnknown workspace.ConsoleCompletionUnknownError
	if errors.As(err, &consoleUnknown) {
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.PartialOutput = consoleUnknown.PartialOutput
		mapped.Remediation = "Retry after the guest shell can complete a command response."
		return mapped
	}
	if errors.Is(err, workspace.WorkspaceNotFoundError{}) {
		mapped.Kind = errorKindNotFound
		mapped.Remediation = "Run workspace.list to inspect available workspaces, or workspace.create to create the requested workspace."
		return mapped
	}
	// os.ErrNotExist reaches here unwrapped from paths like readRequest's
	// os.ReadFile (e.g. a missing --request-json file), and wrapped in an
	// *fs.PathError from most other stdlib file operations; errors.Is drills
	// through either shape.
	if errors.Is(err, os.ErrNotExist) {
		mapped.Kind = errorKindNotFound
		mapped.Remediation = "Check the workspace name, file path, image reference, or state directory and retry."
		return mapped
	}
	if errors.Is(err, os.ErrPermission) {
		mapped.Kind = errorKindPolicyDenied
		mapped.Remediation = "Run with the required host permissions or adjust the host policy outside microagent."
		return mapped
	}
	var waitTimeout workspace.WaitTimeoutError
	if errors.As(err, &waitTimeout) {
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.Remediation = "Retry after the host resource or runtime service becomes available."
		return mapped
	}
	// ExecRetryExhaustedError is only ever constructed after workspace.Exec's
	// own retry loop already determined the underlying failure was a
	// transient connection problem (see IsRetryableExecTransient); its
	// wrapped LastErr may reword freely without affecting this check.
	var execRetryExhausted workspace.ExecRetryExhaustedError
	if errors.As(err, &execRetryExhausted) {
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.Remediation = "Retry after the host resource or runtime service becomes available."
		return mapped
	}
	// UnreachableError always means the structured-exec dial itself failed;
	// that is transient regardless of the OS-level reason in its wrapped Err.
	var execUnreachable execclient.UnreachableError
	if errors.As(err, &execUnreachable) {
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.Remediation = "Retry after the host resource or runtime service becomes available."
		return mapped
	}
	var unsupportedFeature vmkit.UnsupportedFeatureError
	if errors.As(err, &unsupportedFeature) {
		mapped.Kind = errorKindUnsupported
		mapped.Remediation = "Choose a supported backend, command, flag, or host configuration."
		return mapped
	}
	if errors.Is(err, context.DeadlineExceeded) {
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.Remediation = "Retry after the host resource or runtime service becomes available."
		return mapped
	}
	// net.Error timeouts (e.g. a dial or a conn.SetDeadline read/write
	// expiring) are distinct from context.DeadlineExceeded: the deadline is
	// enforced by the net package itself, not by ctx, so this is a second,
	// independently necessary check. It also transparently covers a timeout
	// wrapped inside execclient.ProtocolError via that type's Unwrap.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.Remediation = "Retry after the host resource or runtime service becomes available."
		return mapped
	}
	if errors.Is(err, context.Canceled) {
		// A canceled context means a signal (Ctrl-C) or an expired parent
		// context interrupted the operation, not a failure microagent can
		// retry on the caller's behalf: kind stays the default `permanent`.
		// context.Canceled's message ("context canceled") never matched the
		// substring tail's "cancelled" pattern (different spelling), so this
		// typed check documents a classification that already held by
		// accident; the remediation wording below replaces the generic
		// "inspect correlation_id" fallback with cancellation-specific text.
		mapped.Remediation = "The command was interrupted by a cancellation signal or an expired timeout; re-run it if the operation should still happen."
		return mapped
	}

	text := strings.ToLower(err.Error())
	if rule, ok := matchSubstringClassifierRule(text); ok {
		mapped.Kind = rule.Kind
		mapped.Remediation = rule.Remediation
		mapped.RetryAfterMS = rule.RetryAfterMS
	}
	if mapped.Remediation == "" {
		mapped.Remediation = fmt.Sprintf("Inspect correlation_id %s in surrounding logs and retry after correcting the reported condition.", correlationID)
	}
	return mapped
}

// substringClassifierRule is one fallback text-matching case: if err's
// lowercased message contains any of Patterns, the classifier applies Kind,
// Remediation, and RetryAfterMS.
type substringClassifierRule struct {
	Patterns     []string
	Kind         structuredErrorKind
	Remediation  string
	RetryAfterMS int64
}

// substringClassifierPatterns is the ordered fallback text-matching tail of
// mapStructuredError, tried only once every typed check above has missed. It
// is the single source both mapStructuredError and
// TestSubstringClassifierInventory read from, so the two can never drift:
// changing what the classifier actually does and changing what the test
// expects are the same edit. Rewording an upstream error's message that a
// rule here happens to rely on silently reclassifies its retryability with
// no compiler signal; whenever a typed error is available for a category,
// wire it into the typed section of mapStructuredError above instead of
// adding to or editing this table.
func substringClassifierPatterns() []substringClassifierRule {
	return []substringClassifierRule{
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
}

// matchSubstringClassifierRule returns the first rule (in table order) whose
// pattern is contained in text, mirroring a Go switch statement's top-to-
// bottom, first-true-case-wins evaluation.
func matchSubstringClassifierRule(text string) (substringClassifierRule, bool) {
	for _, rule := range substringClassifierPatterns() {
		for _, pattern := range rule.Patterns {
			if strings.Contains(text, pattern) {
				return rule, true
			}
		}
	}
	return substringClassifierRule{}, false
}
