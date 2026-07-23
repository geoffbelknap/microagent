package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/workspace"
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
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, workspace.WorkspaceNotFoundError{}):
		mapped.Kind = errorKindNotFound
		mapped.Remediation = "Run workspace.list to inspect available workspaces, or workspace.create to create the requested workspace."
	case strings.Contains(text, "not found"), strings.Contains(text, "no such file"), errors.Is(err, os.ErrNotExist):
		mapped.Kind = errorKindNotFound
		mapped.Remediation = "Check the workspace name, file path, image reference, or state directory and retry."
	case strings.Contains(text, "already "), strings.Contains(text, "cannot start from state"), strings.Contains(text, "not running"), strings.Contains(text, "specified twice"), strings.Contains(text, "cancelled"):
		mapped.Kind = errorKindConflict
		mapped.Remediation = "Inspect current state, resolve the conflicting operation, and retry."
	case strings.Contains(text, "not supported"), strings.Contains(text, "unsupported"), strings.Contains(text, "not available"):
		mapped.Kind = errorKindUnsupported
		mapped.Remediation = "Choose a supported backend, command, flag, or host configuration."
	case strings.Contains(text, "permission denied"), strings.Contains(text, "access denied"), strings.Contains(text, "requires administrator"):
		mapped.Kind = errorKindPolicyDenied
		mapped.Remediation = "Run with the required host permissions or adjust the host policy outside microagent."
	case strings.Contains(text, "timeout"), strings.Contains(text, "temporar"), strings.Contains(text, "unreachable"), strings.Contains(text, "connection refused"), strings.Contains(text, "connection reset"):
		mapped.Kind = errorKindTransient
		mapped.RetryAfterMS = 1000
		mapped.Remediation = "Retry after the host resource or runtime service becomes available."
	case strings.Contains(text, "no space"), strings.Contains(text, "too large"), strings.Contains(text, "exceeds"):
		mapped.Kind = errorKindResourceExhausted
		mapped.Remediation = "Free host resources, lower the requested size, or increase the configured limit."
	case strings.Contains(text, "usage:"), strings.Contains(text, "requires "), strings.Contains(text, "unexpected "), strings.Contains(text, "must "):
		mapped.Remediation = "Correct the command arguments and retry."
	}
	if mapped.Remediation == "" {
		mapped.Remediation = fmt.Sprintf("Inspect correlation_id %s in surrounding logs and retry after correcting the reported condition.", correlationID)
	}
	return mapped
}
