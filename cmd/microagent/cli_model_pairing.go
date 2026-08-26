package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// ensureModelPairing resolves modelRefRaw, pulls the blob if it is missing from
// the store, ensures a host model runner holding opts.Name, and wires
// opts.Model (canonical ref), opts.ModelTarget, and the guest model env. It
// returns a release func that drops the holder (a no-op when modelRefRaw is
// empty). Callers that outlive the boot (start) ignore the release func; the
// holder is then dropped by the next lifecycle verb.
func ensureModelPairing(ctx context.Context, opts *workspaceOptions, modelRefRaw, modelToken string) (func(), error) {
	if strings.TrimSpace(modelRefRaw) == "" {
		return func() {}, nil
	}
	if modelToken == "" {
		if v := os.Getenv("HF_TOKEN"); v != "" {
			modelToken = v
		} else if v := os.Getenv("HUGGING_FACE_HUB_TOKEN"); v != "" {
			modelToken = v
		}
	}
	canonical, _, err := model.Resolve(modelRefRaw)
	if err != nil {
		return nil, err
	}
	rec, err := model.Find(opts.StateDir, canonical)
	if err != nil {
		// Not in the store — auto-pull (one-shot convenience).
		rec, err = model.Pull(ctx, model.PullOptions{StateDir: opts.StateDir, ModelRef: modelRefRaw, Token: modelToken, Progress: opts.Progress})
		if err != nil {
			return nil, fmt.Errorf("pull model %s: %w", modelRefRaw, err)
		}
	}
	engine, runnerConfig, err := resolveModelRunner(modelRunnerOverridesFromSpec(opts.ModelRunner))
	if err != nil {
		return nil, err
	}
	runner, err := modelrunner.Ensure(ctx, modelrunner.EnsureOptions{
		StateDir:     opts.StateDir,
		ModelRef:     rec.ModelRef,
		ModelPath:    rec.OutputPath,
		Engine:       engine,
		Holder:       opts.Name,
		ReadyTimeout: 120 * time.Second,
		RunnerConfig: runnerConfig,
		Progress:     opts.Progress,
	})
	if err != nil {
		return nil, fmt.Errorf("start model runner: %w", err)
	}
	// Activate pairing on the workspace options.
	opts.Model = rec.ModelRef
	runnerTarget := fmt.Sprintf("%s:%d", runner.Host, runner.Port)
	modelTarget := runnerTarget
	mediation, err := modelMediationConfigFromSpec(opts.ModelMediation)
	if err != nil {
		_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
		return nil, err
	}
	var mediator *hostworker.ProcessRecord
	if mediation.Enabled {
		execPath, err := os.Executable()
		if err != nil {
			_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
			return nil, fmt.Errorf("resolve microagent executable for model mediation: %w", err)
		}
		workerID := strings.TrimSpace(runner.Key)
		if workerID == "" {
			workerID = runnerTarget
		}
		mediated, err := ensureHostWorkerMediator(ctx, hostworker.ProcessOptions{
			StateDir:        opts.StateDir,
			WorkspaceID:     opts.Name,
			Capability:      hostworker.DefaultCapability,
			WorkerID:        workerID,
			TargetBaseURL:   "http://" + runnerTarget + "/v1",
			ModelRef:        rec.ModelRef,
			Mode:            mediation.Mode,
			PolicyURL:       mediation.PolicyURL,
			PolicyFile:      mediation.PolicyFile,
			PolicyTimeout:   mediation.PolicyTimeout,
			UpstreamTimeout: 180 * time.Second,
			ExecPath:        execPath,
		})
		if err != nil {
			_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
			return nil, fmt.Errorf("start model mediator: %w", err)
		}
		mediator = &mediated
		modelTarget = fmt.Sprintf("%s:%d", mediated.Host, mediated.Port)
	}
	opts.ModelTarget = modelTarget
	opts.ModelRunnerKey = runner.Key
	// The vsock forward re-resolves the runner per connection only when it
	// points at the runner. Pointed at the mediator it must stay pinned, or the
	// supervisor would dial past the mediator and drop the workspace's model
	// traffic out of policy and audit; the mediator re-resolves its own
	// upstream instead.
	opts.ModelTargetMediated = mediation.Enabled
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	modelURL := fmt.Sprintf("http://127.0.0.1:%d/v1", workspace.DefaultModelGuestPort)
	opts.Env["MICROAGENT_MODEL_URL"] = modelURL
	opts.Env["OPENAI_BASE_URL"] = modelURL
	if err := appendModelWorkerAttachedEvent(*opts, runner, modelURL, mediator); err != nil {
		if mediator != nil {
			_ = releaseHostWorkerMediator(opts.StateDir, opts.Name, hostworker.DefaultCapability)
		}
		_ = modelrunner.Release(opts.StateDir, rec.ModelRef, opts.Name)
		return nil, err
	}
	stateDir, modelRef, holder, backend := opts.StateDir, rec.ModelRef, opts.Name, opts.Backend
	return func() {
		if mediator != nil {
			_ = releaseHostWorkerMediator(stateDir, holder, hostworker.DefaultCapability)
		}
		_ = modelrunner.Release(stateDir, modelRef, holder)
		_ = appendModelWorkerReleasedEvent(stateDir, holder, backend, modelRef)
	}, nil
}

func appendModelWorkerAttachedEvent(opts workspaceOptions, runner modelrunner.Record, modelURL string, mediator *hostworker.ProcessRecord) error {
	fields := []string{
		"model_ref=" + runner.ModelRef,
		"engine=" + runner.Engine,
		fmt.Sprintf("pid=%d", runner.PID),
		"runner_config_digest=" + runner.RunnerConfigDigest,
		"holder=" + opts.Name,
		"model_url=" + modelURL,
	}
	if mediator == nil {
		fields = append(fields, "mediation=direct")
	} else {
		fields = append(fields,
			"mediation=host-worker",
			"mediation_mode="+string(mediator.Mode),
			fmt.Sprintf("mediator_pid=%d", mediator.PID),
			fmt.Sprintf("mediator_port=%d", mediator.Port),
			"mediator_audit_log="+mediator.AuditLogPath,
		)
	}
	detail := modelWorkerEventDetail("attached", fields)
	return appendModelWorkerEventIfWorkspaceExists(opts.StateDir, opts.Name, opts.Backend, vmkit.StateStarting, detail)
}

func appendModelWorkerReleasedEvent(stateDir, name, backend, modelRef string) error {
	state := latestWorkspaceEventState(stateDir, name)
	if state == vmkit.StateUnknown {
		state = vmkit.StateHalted
	}
	detail := modelWorkerEventDetail("released", []string{
		"model_ref=" + modelRef,
		"holder=" + name,
	})
	return appendModelWorkerEventIfWorkspaceExists(stateDir, name, backend, state, detail)
}

func modelWorkerEventDetail(action string, fields []string) string {
	parts := []string{"model_worker=" + action}
	for _, field := range fields {
		if strings.HasSuffix(field, "=") {
			continue
		}
		parts = append(parts, field)
	}
	return strings.Join(parts, " ")
}

func appendModelWorkerEventIfWorkspaceExists(stateDir, name, backend string, state vmkit.VMState, detail string) error {
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(name) == "" {
		return nil
	}
	workspaceDir := filepath.Join(stateDir, name)
	if _, err := os.Stat(workspaceDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(backend) == "" {
		backend = hostBackend()
	}
	event := workspaceEventFile{
		Identity: vmkit.Identity{
			RequestID: newRequestID(),
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   backend,
		},
		State:      state,
		Detail:     detail,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return appendWorkspaceEvent(filepath.Join(workspaceDir, "events.json"), event)
}

func latestWorkspaceEventState(stateDir, name string) vmkit.VMState {
	events, err := workspace.ReadEvents(stateDir, name)
	if err != nil || len(events) == 0 {
		return vmkit.StateUnknown
	}
	return events[len(events)-1].State
}

type modelMediationConfig struct {
	Enabled       bool
	Mode          hostworker.Mode
	PolicyURL     string
	PolicyFile    string
	PolicyTimeout time.Duration
}

func modelMediationConfigFromEnv() (modelMediationConfig, error) {
	return modelMediationConfigFromSpec(workspace.ModelMediationSpec{})
}

func modelMediationConfigFromSpec(spec workspace.ModelMediationSpec) (modelMediationConfig, error) {
	rawMode := strings.ToLower(strings.TrimSpace(firstNonEmpty(spec.Mode, os.Getenv(envModelMediation))))
	policyURL := strings.TrimSpace(firstNonEmpty(spec.PolicyURL, os.Getenv(envModelPolicyURL)))
	policyFile := strings.TrimSpace(firstNonEmpty(spec.PolicyFile, os.Getenv(envModelPolicyFile)))
	if rawMode == "" && (policyURL != "" || policyFile != "") {
		rawMode = "policy"
	}
	if rawMode == "" || rawMode == "off" || rawMode == "0" || rawMode == "false" || rawMode == "disabled" {
		return modelMediationConfig{}, nil
	}
	cfg := modelMediationConfig{Enabled: true, PolicyTimeout: 2 * time.Second}
	switch rawMode {
	case "local", "local-allow", "allow":
		cfg.Mode = hostworker.ModeLocalAllow
	case "policy":
		cfg.Mode = hostworker.ModePolicy
	default:
		return modelMediationConfig{}, fmt.Errorf("%s must be off, local-allow, or policy", envModelMediation)
	}
	timeout, err := durationValue(envModelPolicyTimeout, firstNonEmpty(spec.PolicyTimeout, os.Getenv(envModelPolicyTimeout)), cfg.PolicyTimeout)
	if err != nil {
		return modelMediationConfig{}, err
	}
	cfg.PolicyTimeout = timeout
	cfg.PolicyURL = policyURL
	cfg.PolicyFile = policyFile
	if cfg.Mode != hostworker.ModePolicy && (cfg.PolicyURL != "" || cfg.PolicyFile != "") {
		return modelMediationConfig{}, fmt.Errorf("model policy source requires model mediation policy mode")
	}
	if cfg.Mode == hostworker.ModePolicy {
		switch {
		case cfg.PolicyURL != "" && cfg.PolicyFile != "":
			return modelMediationConfig{}, fmt.Errorf("%s and %s are mutually exclusive", envModelPolicyURL, envModelPolicyFile)
		case cfg.PolicyURL == "" && cfg.PolicyFile == "":
			return modelMediationConfig{}, fmt.Errorf("%s=policy requires %s or %s", envModelMediation, envModelPolicyURL, envModelPolicyFile)
		}
	}
	return cfg, nil
}

func durationValue(name, raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		seconds, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("%s must be a Go duration like 250ms or 2s, or a number of seconds", name)
		}
		duration = time.Duration(seconds * float64(time.Second))
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return duration, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mergeModelRunnerSpec(base, override workspace.ModelRunnerSpec) workspace.ModelRunnerSpec {
	out := base
	if strings.TrimSpace(override.Backend) != "" {
		out.Backend = override.Backend
	}
	if strings.TrimSpace(override.GPU) != "" {
		out.GPU = override.GPU
	}
	if strings.TrimSpace(override.BackendModel) != "" {
		out.BackendModel = override.BackendModel
	}
	if strings.TrimSpace(override.ServedModel) != "" {
		out.ServedModel = override.ServedModel
	}
	if len(override.Command) != 0 {
		out.Command = append([]string{}, override.Command...)
	}
	if strings.TrimSpace(override.Name) != "" {
		out.Name = override.Name
	}
	if strings.TrimSpace(override.HealthPath) != "" {
		out.HealthPath = override.HealthPath
	}
	if len(override.Args) != 0 {
		out.Args = append([]string{}, override.Args...)
	}
	if len(override.Env) != 0 {
		out.Env = append([]string{}, override.Env...)
	}
	return out
}

func mergeModelMediationSpec(base, override workspace.ModelMediationSpec) workspace.ModelMediationSpec {
	out := base
	if strings.TrimSpace(override.Mode) != "" {
		out.Mode = override.Mode
	}
	if strings.TrimSpace(override.PolicyURL) != "" {
		out.PolicyURL = override.PolicyURL
	}
	if strings.TrimSpace(override.PolicyFile) != "" {
		out.PolicyFile = override.PolicyFile
	}
	if strings.TrimSpace(override.PolicyTimeout) != "" {
		out.PolicyTimeout = override.PolicyTimeout
	}
	return out
}

type modelRunnerOverrides struct {
	Backend      string
	GPU          string
	BackendModel string
	ServedModel  string
	CommandRaw   string
	Command      []string
	Name         string
	HealthPath   string
	Args         []string
	Env          []string
}

func modelRunnerOverridesFromSpec(spec workspace.ModelRunnerSpec) modelRunnerOverrides {
	return modelRunnerOverrides{
		Backend:      spec.Backend,
		GPU:          spec.GPU,
		BackendModel: spec.BackendModel,
		ServedModel:  spec.ServedModel,
		Command:      append([]string{}, spec.Command...),
		Name:         spec.Name,
		HealthPath:   spec.HealthPath,
		Args:         append([]string{}, spec.Args...),
		Env:          append([]string{}, spec.Env...),
	}
}

func resolveModelRunner(overrides modelRunnerOverrides) (modelrunner.Engine, modelrunner.RunnerConfig, error) {
	return modelrunner.ResolveRunner(modelrunner.RunnerOverrides{
		Backend: overrides.Backend, GPU: overrides.GPU,
		BackendModel: overrides.BackendModel, ServedModel: overrides.ServedModel,
		CommandRaw: overrides.CommandRaw, Command: overrides.Command,
		Name: overrides.Name, HealthPath: overrides.HealthPath,
		Args: overrides.Args, Env: overrides.Env,
	})
}

// pendingModelRelease captures the workspace's paired model ref now (delete
// removes the manifest) and returns a release func to invoke once the
// lifecycle verb has succeeded. Best-effort throughout: a missing manifest or
// runner makes the func a no-op, and a stale holder is reclaimed by the next
// verb.
func pendingModelRelease(stateDir, name, backend string) func() {
	manifest, err := workspace.ReadManifest(stateDir, name)
	if err != nil {
		return func() {
			_ = releaseHostWorkerMediator(stateDir, name, hostworker.DefaultCapability)
		}
	}
	modelRef := strings.TrimSpace(manifest.Model)
	if modelRef == "" {
		return func() {
			_ = releaseHostWorkerMediator(stateDir, name, hostworker.DefaultCapability)
		}
	}
	return func() {
		_ = releaseHostWorkerMediator(stateDir, name, hostworker.DefaultCapability)
		_ = modelrunner.Release(stateDir, modelRef, name)
		_ = appendModelWorkerReleasedEvent(stateDir, name, backend, modelRef)
	}
}
