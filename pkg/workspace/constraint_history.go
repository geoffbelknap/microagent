package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/eventhistory"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const DefaultMaxConstraintRevisions = eventhistory.DefaultMaxEvents

// ConstraintRevision is a host-owned reconstruction point for the effective
// workspace constraints. Manifest is a complete snapshot rather than a diff,
// so every retained entry remains independently reconstructable after bounded
// retention removes older entries.
type ConstraintRevision struct {
	vmkit.ConstraintRevisionRef
	Manifest     *Manifest                  `json:"manifest,omitempty"`
	Verification *vmkit.RuntimeVerification `json:"verification,omitempty"`
}

func ConstraintHistoryPath(stateDir, name string) string {
	return filepath.Join(stateDir, "workspaces", name, "constraint-history.json")
}

// ReadConstraintHistory returns retained revisions oldest first. Existing
// records are never silently replaced when the history is malformed.
func ReadConstraintHistory(stateDir, name string) ([]ConstraintRevision, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	revisions, err := eventhistory.Read[ConstraintRevision](ConstraintHistoryPath(stateDir, name), eventhistory.Options{})
	if err != nil {
		return nil, fmt.Errorf("workspace %s constraint history: %w", name, err)
	}
	return revisions, nil
}

func constraintHistoryStatus(stateDir, name string) (*vmkit.ConstraintHistoryStatus, error) {
	revisions, err := ReadConstraintHistory(stateDir, name)
	if err != nil {
		return nil, err
	}
	status := &vmkit.ConstraintHistoryStatus{
		Path:       ConstraintHistoryPath(stateDir, name),
		Count:      len(revisions),
		MaxEntries: DefaultMaxConstraintRevisions,
	}
	if len(revisions) > 0 {
		oldest := revisions[0].ConstraintRevisionRef
		latest := revisions[len(revisions)-1].ConstraintRevisionRef
		status.Oldest = &oldest
		status.Latest = &latest
	}
	return status, nil
}

func appendConstraintRevision(opts Options, trigger string, manifest *Manifest) error {
	return appendConstraintRevisionWithLimit(opts, trigger, manifest, DefaultMaxConstraintRevisions)
}

func appendConstraintRevisionWithLimit(opts Options, trigger string, manifest *Manifest, maxEntries int) error {
	name := strings.TrimSpace(opts.Name)
	if name == "" && manifest != nil {
		name = manifest.Name
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	ref := vmkit.ConstraintRevisionRef{
		EventID:       fmt.Sprintf("constraint-%d", time.Now().UnixNano()),
		RequestID:     NewRequestID(),
		RuntimeID:     name,
		Purpose:       opts.Purpose,
		CorrelationID: opts.CorrelationID,
		Trigger:       strings.TrimSpace(trigger),
		ObservedAt:    time.Now().UTC(),
	}
	if manifest != nil {
		if ref.Purpose == "" {
			ref.Purpose = manifest.Purpose
		}
		if ref.CorrelationID == "" {
			ref.CorrelationID = manifest.CorrelationID
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			return fmt.Errorf("encode manifest constraint revision: %w", err)
		}
		ref.ManifestSHA256 = sha256Hex(data)
	}
	configPath := ConfigDiskFile(opts.StateDir, name)
	if data, err := os.ReadFile(configPath); err == nil {
		ref.ConfigDiskSHA256 = sha256Hex(data)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("hash config disk constraint revision: %w", err)
	}
	revision := ConstraintRevision{ConstraintRevisionRef: ref, Manifest: manifest}
	if manifest != nil {
		revision.Verification = manifest.Verification
	}
	return eventhistory.Append(ConstraintHistoryPath(opts.StateDir, name), revision, eventhistory.Options{MaxEvents: maxEntries})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readConstraintCurrent(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func rollbackConstraintCurrent(path string, previous []byte, existed bool) error {
	if existed {
		return writeFileAtomic(path, previous, 0o600)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
