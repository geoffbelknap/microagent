package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
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
	RetryAfterMS  int64               `json:"retry_after_ms,omitempty"`
	CorrelationID string              `json:"correlation_id"`
}

type errorEnvelope struct {
	OK    bool            `json:"ok"`
	Error structuredError `json:"error"`
}

func writeAXError(stderr *os.File, err error) error {
	if err == nil {
		return nil
	}
	enc := json.NewEncoder(stderr)
	enc.SetIndent("", "  ")
	return enc.Encode(errorEnvelope{OK: false, Error: mapStructuredError(err, newRequestID())})
}

func mapStructuredError(err error, correlationID string) structuredError {
	mapped := structuredError{
		Kind:          errorKindPermanent,
		Message:       err.Error(),
		CorrelationID: correlationID,
	}
	if errors.Is(err, flag.ErrHelp) {
		mapped.Remediation = "Re-run the command without --help, or choose one of the documented command forms."
		return mapped
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "not found"), strings.Contains(text, "no such file"), errors.Is(err, os.ErrNotExist):
		mapped.Kind = errorKindNotFound
		mapped.Remediation = "Check the workspace name, file path, image reference, or state directory and retry."
	case strings.Contains(text, "already "), strings.Contains(text, "cannot start from state"), strings.Contains(text, "specified twice"), strings.Contains(text, "cancelled"):
		mapped.Kind = errorKindConflict
		mapped.Remediation = "Inspect current state, resolve the conflicting operation, and retry."
	case strings.Contains(text, "not supported"), strings.Contains(text, "unsupported"), strings.Contains(text, "not available"):
		mapped.Kind = errorKindUnsupported
		mapped.Remediation = "Choose a supported backend, command, flag, or host configuration."
	case strings.Contains(text, "permission denied"), strings.Contains(text, "access denied"), strings.Contains(text, "requires administrator"):
		mapped.Kind = errorKindPolicyDenied
		mapped.Remediation = "Run with the required host permissions or adjust the host policy outside microagent."
	case strings.Contains(text, "timeout"), strings.Contains(text, "temporar"), strings.Contains(text, "connection refused"), strings.Contains(text, "connection reset"):
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
