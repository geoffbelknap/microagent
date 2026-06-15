package modelrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Record is one tracked host model-server process.
type Record struct {
	Key                string   `json:"key"`
	ModelRef           string   `json:"model_ref"`
	Engine             string   `json:"engine"`
	BinPath            string   `json:"bin_path,omitempty"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	PID                int      `json:"pid"`
	RunnerCommand      []string `json:"runner_command,omitempty"`
	RunnerArgs         []string `json:"runner_args,omitempty"`
	RunnerEnvKeys      []string `json:"runner_env_keys,omitempty"`
	RunnerConfigDigest string   `json:"runner_config_digest,omitempty"`
	Dedicated          bool     `json:"dedicated,omitempty"`
	Pinned             bool     `json:"pinned,omitempty"`
	Holders            []string `json:"holders,omitempty"`
	LogPath            string   `json:"log_path,omitempty"`
	StartedAt          string   `json:"started_at"`
	ReadyAt            string   `json:"ready_at,omitempty"`
}

type Index struct {
	Runners []Record `json:"runners"`
}

func IndexPath(stateDir string) string {
	return filepath.Join(stateDir, "runners", "index.json")
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
	if err := os.MkdirAll(filepath.Dir(IndexPath(stateDir)), 0o700); err != nil {
		return err
	}
	sort.Slice(idx.Runners, func(i, j int) bool { return idx.Runners[i].Key < idx.Runners[j].Key })
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(IndexPath(stateDir), data, 0o600)
}

func runnerKey(modelRef string, dedicated bool, holder string, configDigest string) string {
	if configDigest != "" {
		modelRef += "#runner=" + configDigest
	}
	if dedicated {
		return modelRef + "#" + holder
	}
	return modelRef
}

// EnsureOptions configures Ensure. The model blob must already exist on disk at
// ModelPath (callers resolve it from the model store).
type EnsureOptions struct {
	StateDir     string
	ModelRef     string
	ModelPath    string
	Engine       Engine
	Holder       string // workspace name to add as a holder; "" for none
	Pinned       bool   // operator 'model serve' pin: survives holders → 0
	Dedicated    bool   // give this holder its own runner
	Host         string // default 127.0.0.1
	ReadyTimeout time.Duration
	RunnerConfig RunnerConfig
}

// Ensure returns a ready runner for the model, reusing a live one for the same
// key (shared by ModelRef, or ModelRef#holder when dedicated) or starting one.
func Ensure(ctx context.Context, opts EnsureOptions) (Record, error) {
	if opts.ModelRef == "" || opts.ModelPath == "" {
		return Record{}, fmt.Errorf("model ref and path are required")
	}
	if opts.Engine == nil {
		return Record{}, fmt.Errorf("engine is required")
	}
	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}
	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runnerConfig, err := normalizeRunnerConfig(opts.RunnerConfig)
	if err != nil {
		return Record{}, err
	}
	configDigest := runnerConfig.Digest()
	key := runnerKey(opts.ModelRef, opts.Dedicated, opts.Holder, configDigest)
	idx, err := ReadIndex(opts.StateDir)
	if err != nil {
		return Record{}, err
	}
	for i, r := range idx.Runners {
		if r.Key == key {
			if processAlive(r.PID) {
				idx.Runners[i].Holders = addHolder(r.Holders, opts.Holder)
				if opts.Pinned {
					idx.Runners[i].Pinned = true
				}
				if err := WriteIndex(opts.StateDir, idx); err != nil {
					return Record{}, err
				}
				return idx.Runners[i], nil
			}
			idx.Runners = append(idx.Runners[:i], idx.Runners[i+1:]...)
			break
		}
	}
	port, err := allocateFreePort()
	if err != nil {
		return Record{}, err
	}
	argv := opts.Engine.Argv(opts.ModelPath, host, port)
	logPath := filepath.Join(opts.StateDir, "runners", sanitizeKey(key)+".log")
	pid, err := spawnProcess(argv, runnerConfig.Env, logPath)
	if err != nil {
		return Record{}, err
	}
	rec := Record{
		Key:                key,
		ModelRef:           opts.ModelRef,
		Engine:             opts.Engine.Name(),
		BinPath:            argv[0],
		Host:               host,
		Port:               port,
		PID:                pid,
		RunnerCommand:      append([]string{}, runnerConfig.Command...),
		RunnerArgs:         append([]string{}, runnerConfig.Args...),
		RunnerEnvKeys:      runnerConfig.EnvKeys(),
		RunnerConfigDigest: configDigest,
		Dedicated:          opts.Dedicated,
		Pinned:             opts.Pinned,
		Holders:            addHolder(nil, opts.Holder),
		LogPath:            logPath,
		StartedAt:          time.Now().UTC().Format(time.RFC3339),
	}
	idx.Runners = append(idx.Runners, rec)
	if err := WriteIndex(opts.StateDir, idx); err != nil {
		_ = stopProcess(pid)
		return Record{}, err
	}
	if err := waitHealthy(ctx, host, port, opts.Engine.HealthPath(), timeout); err != nil {
		_ = stopProcess(pid)
		_ = removeKey(opts.StateDir, key)
		return Record{}, fmt.Errorf("model runner for %s failed readiness: %w", opts.ModelRef, err)
	}
	rec.ReadyAt = time.Now().UTC().Format(time.RFC3339)
	if err := upsert(opts.StateDir, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Release drops holder from any runner of modelRef; an unpinned runner with no
// remaining holders is stopped and removed.
func Release(stateDir, modelRef, holder string) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	var kept []Record
	for _, r := range idx.Runners {
		if r.ModelRef == modelRef && containsHolder(r.Holders, holder) {
			r.Holders = removeHolder(r.Holders, holder)
			if len(r.Holders) == 0 && !r.Pinned {
				_ = stopProcess(r.PID)
				continue
			}
		}
		kept = append(kept, r)
	}
	return WriteIndex(stateDir, Index{Runners: kept})
}

// Stop force-stops and removes every runner for modelRef (ignores pinned).
func Stop(stateDir, modelRef string) (int, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return 0, err
	}
	var kept []Record
	stopped := 0
	for _, r := range idx.Runners {
		if r.ModelRef == modelRef {
			_ = stopProcess(r.PID)
			stopped++
			continue
		}
		kept = append(kept, r)
	}
	if stopped == 0 {
		return 0, fmt.Errorf("no runner for model %q", modelRef)
	}
	return stopped, WriteIndex(stateDir, Index{Runners: kept})
}

// List returns live runners, self-healing the registry by dropping records whose
// process is gone.
func List(stateDir string) ([]Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return nil, err
	}
	var live []Record
	changed := false
	for _, r := range idx.Runners {
		if processAlive(r.PID) {
			live = append(live, r)
		} else {
			changed = true
		}
	}
	if changed {
		if err := WriteIndex(stateDir, Index{Runners: live}); err != nil {
			return nil, err
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Key < live[j].Key })
	return live, nil
}

func addHolder(holders []string, holder string) []string {
	if holder == "" {
		return holders
	}
	for _, h := range holders {
		if h == holder {
			return holders
		}
	}
	return append(holders, holder)
}

func removeHolder(holders []string, holder string) []string {
	var out []string
	for _, h := range holders {
		if h != holder {
			out = append(out, h)
		}
	}
	return out
}

func containsHolder(holders []string, holder string) bool {
	for _, h := range holders {
		if h == holder {
			return true
		}
	}
	return false
}

func sanitizeKey(key string) string {
	return strings.NewReplacer("/", "_", ":", "_", "@", "_", "#", "_").Replace(key)
}

func upsert(stateDir string, rec Record) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	for i, r := range idx.Runners {
		if r.Key == rec.Key {
			idx.Runners[i] = rec
			return WriteIndex(stateDir, idx)
		}
	}
	idx.Runners = append(idx.Runners, rec)
	return WriteIndex(stateDir, idx)
}

func removeKey(stateDir, key string) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	var kept []Record
	for _, r := range idx.Runners {
		if r.Key != key {
			kept = append(kept, r)
		}
	}
	return WriteIndex(stateDir, Index{Runners: kept})
}
