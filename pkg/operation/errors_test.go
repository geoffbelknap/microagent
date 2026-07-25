package operation

import (
	"errors"
	"io/fs"
	"testing"
)

func TestErrorClassificationSurvivesMessageChanges(t *testing.T) {
	first := New(ErrorConflict, "workspace is already running")
	second := New(ErrorConflict, "current state prevents this transition")

	for _, err := range []error{first, second} {
		if !IsKind(err, ErrorConflict) {
			t.Fatalf("IsKind(%v, conflict) = false", err)
		}
	}
}

func TestWrapPreservesCause(t *testing.T) {
	err := Wrap(ErrorNotFound, fs.ErrNotExist)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("errors.Is(%v, fs.ErrNotExist) = false", err)
	}
	if !IsKind(err, ErrorNotFound) {
		t.Fatalf("IsKind(%v, not_found) = false", err)
	}
}
