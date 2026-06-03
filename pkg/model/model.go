package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/workspace"
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

type PullOptions struct {
	StateDir string
	ModelRef string
	Token    string
}

var httpGet = func(ctx context.Context, url, token string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}

func Pull(ctx context.Context, opts PullOptions) (Record, error) {
	canonical, url, err := resolveHFURL(opts.ModelRef)
	if err != nil {
		return Record{}, err
	}
	if opts.StateDir == "" {
		opts.StateDir = workspace.StateDir()
	}
	token := opts.Token
	for _, env := range []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN"} {
		if token != "" {
			break
		}
		token = os.Getenv(env)
	}
	outputPath := ModelPath(opts.StateDir, canonical)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return Record{}, err
	}
	body, _, err := httpGet(ctx, url, token)
	if err != nil {
		return Record{}, err
	}
	defer body.Close()
	tmp := outputPath + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return Record{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(f, hash), body)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return Record{}, copyErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return Record{}, closeErr
	}
	if err := os.Rename(tmp, outputPath); err != nil {
		os.Remove(tmp)
		return Record{}, err
	}
	record := Record{
		ModelRef:    canonical,
		ResolvedRef: url,
		Digest:      "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		OutputPath:  outputPath,
		SizeBytes:   size,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := Upsert(opts.StateDir, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func resolveHFURL(ref string) (canonical, downloadURL string, err error) {
	raw := strings.TrimSpace(ref)
	if raw == "" {
		return "", "", fmt.Errorf("model reference is required")
	}
	for _, p := range []string{"https://", "http://", "hf.co/", "huggingface.co/"} {
		raw = strings.TrimPrefix(raw, p)
	}
	org, rest, ok := strings.Cut(raw, "/")
	if !ok {
		return "", "", fmt.Errorf("model reference %q must be <org>/<repo>/<file.gguf>", ref)
	}
	repo, tail, ok := strings.Cut(rest, "/")
	if !ok {
		return "", "", fmt.Errorf("model reference %q must be <org>/<repo>/<file.gguf>", ref)
	}
	rev := "main"
	if r, after, hasRev := strings.Cut(repo, "@"); hasRev {
		repo, rev = r, after
	}
	if strings.HasPrefix(tail, "resolve/") {
		parts := strings.SplitN(strings.TrimPrefix(tail, "resolve/"), "/", 2)
		if len(parts) == 2 {
			rev, tail = parts[0], parts[1]
		}
	}
	if !strings.HasSuffix(tail, ".gguf") {
		return "", "", fmt.Errorf("model reference %q must point to a .gguf file", ref)
	}
	if org == "" || repo == "" {
		return "", "", fmt.Errorf("model reference %q must be <org>/<repo>/<file.gguf>", ref)
	}
	canonical = fmt.Sprintf("hf.co/%s/%s@%s/%s", org, repo, rev, tail)
	downloadURL = fmt.Sprintf("https://huggingface.co/%s/%s/resolve/%s/%s", org, repo, rev, tail)
	return canonical, downloadURL, nil
}

type PruneResult struct {
	Removed []Record `json:"removed"`
	Deleted []Record `json:"deleted,omitempty"`
	Kept    []Record `json:"kept"`
}

func Remove(stateDir, ref string, deleteFiles bool) (PruneResult, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return PruneResult{}, err
	}
	var res PruneResult
	var kept []Record
	found := false
	for _, m := range idx.Models {
		if MatchesRef(m, ref) {
			found = true
			res.Removed = append(res.Removed, m)
			if deleteFiles && m.OutputPath != "" {
				if err := os.Remove(m.OutputPath); err == nil {
					res.Deleted = append(res.Deleted, m)
				} else if !os.IsNotExist(err) {
					return PruneResult{}, err
				}
			}
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		return PruneResult{}, fmt.Errorf("model %q not found", ref)
	}
	res.Kept = kept
	return res, WriteIndex(stateDir, Index{Models: kept})
}

func Prune(stateDir string, deleteFiles bool) (PruneResult, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return PruneResult{}, err
	}
	var res PruneResult
	var kept []Record
	for _, m := range idx.Models {
		if m.OutputPath == "" {
			res.Removed = append(res.Removed, m)
			continue
		}
		if _, statErr := os.Stat(m.OutputPath); statErr != nil {
			if os.IsNotExist(statErr) {
				res.Removed = append(res.Removed, m)
				continue
			}
			return PruneResult{}, statErr
		}
		if deleteFiles {
			err := os.Remove(m.OutputPath)
			if err == nil {
				res.Deleted = append(res.Deleted, m)
				res.Removed = append(res.Removed, m)
				continue
			}
			if !os.IsNotExist(err) {
				return PruneResult{}, err
			}
			// File already gone: treat as removed.
			res.Removed = append(res.Removed, m)
			continue
		}
		kept = append(kept, m)
	}
	res.Kept = kept
	return res, WriteIndex(stateDir, Index{Models: kept})
}
