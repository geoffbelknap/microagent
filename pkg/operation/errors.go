// Package operation defines backend-neutral contracts shared by microagent
// application operations and their CLI and MCP adapters.
package operation

import (
	"errors"
	"fmt"
)

// ErrorKind is a stable, machine-readable operation failure category.
type ErrorKind string

const (
	ErrorValidation        ErrorKind = "validation"
	ErrorConflict          ErrorKind = "conflict"
	ErrorNotFound          ErrorKind = "not_found"
	ErrorResourceExhausted ErrorKind = "resource_exhausted"
	ErrorUnsupported       ErrorKind = "unsupported"
	ErrorPolicyDenied      ErrorKind = "policy_denied"
	ErrorTransient         ErrorKind = "transient"
)

// Error classifies an operation failure without coupling its category to the
// human-readable message. Err remains available through errors.Is/As.
type Error struct {
	Kind ErrorKind
	Err  error
}

func (e Error) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return e.Err.Error()
}

func (e Error) Unwrap() error {
	return e.Err
}

// New returns a classified error with a formatted human-readable message.
func New(kind ErrorKind, format string, args ...any) error {
	return Error{Kind: kind, Err: fmt.Errorf(format, args...)}
}

// Wrap preserves err while assigning a stable operation category.
func Wrap(kind ErrorKind, err error) error {
	if err == nil {
		return nil
	}
	return Error{Kind: kind, Err: err}
}

// IsKind reports whether err contains an operation error with kind.
func IsKind(err error, kind ErrorKind) bool {
	var classified Error
	return errors.As(err, &classified) && classified.Kind == kind
}
