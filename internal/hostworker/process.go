package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProcessRecord struct {
	Key               string `json:"key"`
	WorkspaceID       string `json:"workspace_id"`
	Capability        string `json:"capability"`
	WorkerID          string `json:"worker_id"`
	TargetBaseURL     string `json:"target_base_url"`
	ModelRef          string `json:"model_ref,omitempty"`
	Mode              Mode   `json:"mode"`
	PolicyURL         string `json:"policy_url,omitempty"`
	PolicyFile        string `json:"policy_file,omitempty"`
	PolicyFileSHA256  string `json:"policy_file_sha256,omitempty"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	PID               int    `json:"pid"`
	LogPath           string `json:"log_path,omitempty"`
	AuditLogPath      string `json:"audit_log_path,omitempty"`
	StartedAt         string `json:"started_at"`
	PolicyTimeoutMS   int64  `json:"policy_timeout_ms,omitempty"`
	UpstreamTimeoutMS int64  `json:"upstream_timeout_ms,omitempty"`
}

type ProcessIndex struct {
	Mediators []ProcessRecord `json:"mediators"`
}

type ProcessOptions struct {
	StateDir      string
	WorkspaceID   string
	Capability    string
	WorkerID      string
	TargetBaseURL string
	// ModelRef, when set, is the canonical model ref of the runner this
	// mediator fronts. The mediator re-resolves that runner before each
	// proxied request so a runner restart does not strand the workspace.
	ModelRef        string
	Mode            Mode
	PolicyURL       string
	PolicyFile      string
	PolicyTimeout   time.Duration
	UpstreamTimeout time.Duration
	ExecPath        string
	Host            string
}

var (
	spawnProcess        = spawnDetachedProcess
	stopProcess         = stopDetachedProcess
	processLive         = processAlive
	probeMediatorHealth = defaultProbeMediatorHealth
)

func EnsureProcess(ctx context.Context, opts ProcessOptions) (ProcessRecord, error) {
	if strings.TrimSpace(opts.StateDir) == "" {
		return ProcessRecord{}, fmt.Errorf("state dir is required")
	}
	if strings.TrimSpace(opts.WorkspaceID) == "" {
		return ProcessRecord{}, fmt.Errorf("workspace id is required")
	}
	if strings.TrimSpace(opts.TargetBaseURL) == "" {
		return ProcessRecord{}, fmt.Errorf("target base URL is required")
	}
	if strings.TrimSpace(opts.ExecPath) == "" {
		return ProcessRecord{}, fmt.Errorf("exec path is required")
	}
	mode := opts.Mode
	if mode == "" {
		mode = ModeLocalAllow
	}
	if mode == ModePassthrough {
		mode = ModeLocalAllow
	}
	switch mode {
	case ModeForward, ModeLocalAllow, ModePolicy:
	default:
		return ProcessRecord{}, fmt.Errorf("unsupported process mediation mode %q", mode)
	}
	var policyFileSource policyFileSource
	if mode == ModeForward && (strings.TrimSpace(opts.PolicyURL) != "" || strings.TrimSpace(opts.PolicyFile) != "") {
		return ProcessRecord{}, fmt.Errorf("byte forwarding cannot enforce a policy")
	}
	if mode == ModePolicy {
		hasPolicyURL := strings.TrimSpace(opts.PolicyURL) != ""
		hasPolicyFile := strings.TrimSpace(opts.PolicyFile) != ""
		switch {
		case hasPolicyURL && hasPolicyFile:
			return ProcessRecord{}, fmt.Errorf("policy URL and policy file are mutually exclusive")
		case hasPolicyFile:
			_, source, err := LoadFilePolicy(opts.PolicyFile)
			if err != nil {
				return ProcessRecord{}, err
			}
			policyFileSource = source
		case !hasPolicyURL:
			return ProcessRecord{}, fmt.Errorf("policy URL or policy file is required for policy mediation")
		}
	}
	capability := strings.TrimSpace(opts.Capability)
	if capability == "" {
		capability = DefaultCapability
	}
	host := strings.TrimSpace(opts.Host)
	if host == "" {
		host = defaultListenHost
	}
	policyTimeout := opts.PolicyTimeout
	if policyTimeout <= 0 {
		policyTimeout = defaultPolicyTimeout
	}
	upstreamTimeout := opts.UpstreamTimeout
	if upstreamTimeout <= 0 {
		upstreamTimeout = defaultUpstreamTimeout
	}
	key := processKey(opts.WorkspaceID, capability)
	if err := os.MkdirAll(processDir(opts.StateDir), 0o700); err != nil {
		return ProcessRecord{}, err
	}
	idx, err := ReadProcessIndex(opts.StateDir)
	if err != nil {
		return ProcessRecord{}, err
	}
	for i, existing := range idx.Mediators {
		if existing.Key != key {
			continue
		}
		if processLive(existing.PID) && sameProcessConfig(existing, opts, capability, mode, host, policyTimeout, upstreamTimeout, policyFileSource) {
			return existing, nil
		}
		if existing.PID > 0 {
			_ = stopProcess(existing.PID)
		}
		idx.Mediators = append(idx.Mediators[:i], idx.Mediators[i+1:]...)
		break
	}
	port, err := allocateFreePort()
	if err != nil {
		return ProcessRecord{}, err
	}
	logPath := filepath.Join(processDir(opts.StateDir), sanitizeKey(key)+".log")
	auditLogPath := filepath.Join(processDir(opts.StateDir), sanitizeKey(key)+".jsonl")
	if mode == ModeForward {
		auditLogPath = ""
	}
	args := []string{
		opts.ExecPath,
		"--host-worker-mediator",
		"--target-base-url", opts.TargetBaseURL,
		"--bind-host", host,
		"--bind-port", strconv.Itoa(port),
		"--log-path", auditLogPath,
		"--mode", string(mode),
		"--workspace-id", opts.WorkspaceID,
		"--capability", capability,
		"--worker-id", opts.WorkerID,
	}
	if policyTimeout > 0 {
		args = append(args, "--policy-timeout", policyTimeout.String())
	}
	if upstreamTimeout > 0 {
		args = append(args, "--upstream-timeout", upstreamTimeout.String())
	}
	if strings.TrimSpace(opts.ModelRef) != "" {
		args = append(args, "--model-ref", strings.TrimSpace(opts.ModelRef), "--state-dir", opts.StateDir)
	}
	if strings.TrimSpace(opts.PolicyURL) != "" {
		args = append(args, "--policy-url", opts.PolicyURL)
	}
	if policyFileSource.Path != "" {
		args = append(args, "--policy-file", policyFileSource.Path)
	}
	pid, err := spawnProcess(args, nil, logPath)
	if err != nil {
		return ProcessRecord{}, err
	}
	rec := ProcessRecord{
		Key:               key,
		WorkspaceID:       opts.WorkspaceID,
		Capability:        capability,
		WorkerID:          strings.TrimSpace(opts.WorkerID),
		TargetBaseURL:     opts.TargetBaseURL,
		ModelRef:          strings.TrimSpace(opts.ModelRef),
		Mode:              mode,
		PolicyURL:         strings.TrimSpace(opts.PolicyURL),
		PolicyFile:        policyFileSource.Path,
		PolicyFileSHA256:  policyFileSource.SHA256,
		Host:              host,
		Port:              port,
		PID:               pid,
		LogPath:           logPath,
		AuditLogPath:      auditLogPath,
		StartedAt:         time.Now().UTC().Format(time.RFC3339),
		PolicyTimeoutMS:   policyTimeout.Milliseconds(),
		UpstreamTimeoutMS: upstreamTimeout.Milliseconds(),
	}
	idx.Mediators = append(idx.Mediators, rec)
	if err := WriteProcessIndex(opts.StateDir, idx); err != nil {
		_ = stopProcess(pid)
		return ProcessRecord{}, err
	}
	if err := waitMediatorHealthy(ctx, rec, 10*time.Second); err != nil {
		_ = stopProcess(pid)
		_ = RemoveProcess(opts.StateDir, key)
		return ProcessRecord{}, err
	}
	return rec, nil
}

func ReleaseProcess(stateDir, workspaceID, capability string) error {
	key := processKey(workspaceID, nonEmpty(capability, DefaultCapability))
	idx, err := ReadProcessIndex(stateDir)
	if err != nil {
		return err
	}
	var kept []ProcessRecord
	for _, rec := range idx.Mediators {
		if rec.Key == key {
			_ = stopProcess(rec.PID)
			continue
		}
		kept = append(kept, rec)
	}
	return WriteProcessIndex(stateDir, ProcessIndex{Mediators: kept})
}

func RemoveProcess(stateDir, key string) error {
	idx, err := ReadProcessIndex(stateDir)
	if err != nil {
		return err
	}
	var kept []ProcessRecord
	for _, rec := range idx.Mediators {
		if rec.Key != key {
			kept = append(kept, rec)
		}
	}
	return WriteProcessIndex(stateDir, ProcessIndex{Mediators: kept})
}

func ReadProcessIndex(stateDir string) (ProcessIndex, error) {
	data, err := os.ReadFile(ProcessIndexPath(stateDir))
	if os.IsNotExist(err) {
		return ProcessIndex{}, nil
	}
	if err != nil {
		return ProcessIndex{}, err
	}
	var idx ProcessIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return ProcessIndex{}, err
	}
	return idx, nil
}

func WriteProcessIndex(stateDir string, idx ProcessIndex) error {
	if err := os.MkdirAll(processDir(stateDir), 0o700); err != nil {
		return err
	}
	sort.Slice(idx.Mediators, func(i, j int) bool { return idx.Mediators[i].Key < idx.Mediators[j].Key })
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(ProcessIndexPath(stateDir), data, 0o600)
}

func ProcessIndexPath(stateDir string) string {
	return filepath.Join(processDir(stateDir), "index.json")
}

func processDir(stateDir string) string {
	return filepath.Join(stateDir, "host-workers")
}

func processKey(workspaceID, capability string) string {
	return workspaceID + "#" + capability
}

func sameProcessConfig(rec ProcessRecord, opts ProcessOptions, capability string, mode Mode, host string, policyTimeout, upstreamTimeout time.Duration, policyFileSource policyFileSource) bool {
	return rec.WorkspaceID == opts.WorkspaceID &&
		rec.Capability == capability &&
		rec.WorkerID == strings.TrimSpace(opts.WorkerID) &&
		rec.TargetBaseURL == opts.TargetBaseURL &&
		rec.ModelRef == strings.TrimSpace(opts.ModelRef) &&
		rec.Mode == mode &&
		rec.PolicyURL == strings.TrimSpace(opts.PolicyURL) &&
		rec.PolicyFile == policyFileSource.Path &&
		rec.PolicyFileSHA256 == policyFileSource.SHA256 &&
		rec.Host == host &&
		rec.PolicyTimeoutMS == policyTimeout.Milliseconds() &&
		rec.UpstreamTimeoutMS == upstreamTimeout.Milliseconds()
}

func waitMediatorHealthy(ctx context.Context, rec ProcessRecord, overall time.Duration) error {
	url := fmt.Sprintf("http://%s:%d/healthz", rec.Host, rec.Port)
	deadline := time.Now().Add(overall)
	for {
		probe := probeMediatorHealth
		if rec.Mode == ModeForward {
			probe = probeForwardHealth
		}
		if err := probe(ctx, url, time.Second); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("host worker mediator did not become healthy at %s within %s", url, overall)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func probeForwardHealth(ctx context.Context, endpoint string, timeout time.Duration) error {
	parsed, err := parseEndpointURL(endpoint, "health")
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return err
	}
	return conn.Close()
}

func defaultProbeMediatorHealth(ctx context.Context, url string, timeout time.Duration) error {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

func spawnDetachedProcess(argv []string, env []string, logPath string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("empty argv")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command(argv[0], argv[1:]...)
	if len(env) != 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func allocateFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func sanitizeKey(key string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "@", "_", "#", "_", "=", "_", " ", "_")
	return replacer.Replace(key)
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
