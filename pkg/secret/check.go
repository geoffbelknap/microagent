package secret

import (
	"context"
	"fmt"
	"strings"
)

// CheckResult reports the outcome of validating one NAME=<ref> entry. It never
// contains the secret value — only its byte length and provenance.
type CheckResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Source  string `json:"source,omitempty"`
	Bytes   int    `json:"bytes"`
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Check validates a single "NAME=<scheme>:<ref>" entry: it resolves the
// reference, then reports ok, the source scheme, the value's byte length, and
// any plaintext warning. The secret value itself is never returned or logged.
// Unlike Resolve, Check does not fire the registry warn sink — the warning is
// surfaced through CheckResult.Warning instead.
func (r *Registry) Check(ctx context.Context, entry string) CheckResult {
	name, ref, ok := strings.Cut(entry, "=")
	if !ok || name == "" {
		return CheckResult{Name: name, OK: false, Error: fmt.Sprintf("entry %q must be NAME=<scheme>:<ref>", entry)}
	}
	value, scheme, warning, err := r.resolve(ctx, ref)
	if err != nil {
		return CheckResult{Name: name, OK: false, Source: scheme, Error: err.Error()}
	}
	return CheckResult{Name: name, OK: true, Source: scheme, Bytes: len(value), Warning: warning}
}
