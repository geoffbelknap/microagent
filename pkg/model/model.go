package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Record struct {
	ModelRef    string `json:"model_ref"`
	ResolvedRef string `json:"resolved_ref,omitempty"`
	Digest      string `json:"digest,omitempty"`
	OutputPath  string `json:"output_path,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	LastUsedAt  string `json:"last_used_at"`
}

type Index struct {
	Models []Record `json:"models"`
}

func IndexPath(stateDir string) string {
	return filepath.Join(stateDir, "models", "index.json")
}

func ReadIndex(stateDir string) (Index, error) {
	data, err := os.ReadFile(IndexPath(stateDir))
	if os.IsNotExist(err) {
		return Index{}, nil
	}
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}

func WriteIndex(stateDir string, idx Index) error {
	if err := os.MkdirAll(filepath.Dir(IndexPath(stateDir)), 0o755); err != nil {
		return err
	}
	Sort(idx.Models)
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(IndexPath(stateDir), data, 0o644)
}

func Upsert(stateDir string, record Record) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range idx.Models {
		if existing.ModelRef == record.ModelRef {
			idx.Models[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		idx.Models = append(idx.Models, record)
	}
	return WriteIndex(stateDir, idx)
}

func List(stateDir string) ([]Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return nil, err
	}
	Sort(idx.Models)
	return idx.Models, nil
}

func Sort(models []Record) {
	sort.Slice(models, func(i, j int) bool { return models[i].ModelRef < models[j].ModelRef })
}

func ModelPath(stateDir, canonicalRef string) string {
	sum := sha256.Sum256([]byte(canonicalRef))
	name := hex.EncodeToString(sum[:])[:24] + ".gguf"
	return filepath.Join(stateDir, "models", "blobs", name)
}

func MatchesRef(m Record, ref string) bool {
	return m.ModelRef == ref || m.ResolvedRef == ref || m.Digest == ref
}

func Find(stateDir, ref string) (Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for _, m := range idx.Models {
		if MatchesRef(m, ref) {
			return m, nil
		}
	}
	return Record{}, fmt.Errorf("model %q not found", ref)
}
