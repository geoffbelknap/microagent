package modelservice

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// PairOptions contains transient credentials and the trusted companion path.
// Model runner and mediation settings come from workspace.Options and their
// documented environment defaults. Token is never stored in workspace state.
type PairOptions struct {
	Token    string
	ExecPath string
}

var (
	findModel           = model.Find
	pullModel           = model.Pull
	ensureModelRunner   = modelrunner.Ensure
	attachModelService  = Attach
	releaseModelRunner  = modelrunner.Release
	releaseModelService = Release
)

// Pair prepares the model named by opts.Model, acquires a runner hold, attaches
// its host service, and wires the guest environment. It updates opts only after
// successful setup. The returned best-effort cleanup releases both resources.
// For detached workspaces, retain the pairing until teardown instead of
// deferring cleanup across Start. PendingRelease handles a later lifecycle call.
func Pair(ctx context.Context, opts *workspace.Options, config PairOptions) (func(), error) {
	if opts == nil {
		return nil, fmt.Errorf("workspace options are required")
	}
	modelRefRaw, modelToken := opts.Model, config.Token
	if strings.TrimSpace(modelRefRaw) == "" {
		return func() {}, nil
	}
	if err := workspace.ValidateName(opts.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.StateDir) == "" || strings.TrimSpace(config.ExecPath) == "" {
		return nil, fmt.Errorf("model pairing requires a state directory and companion executable")
	}
	// Validate caller configuration before downloads or acquiring a runner hold.
	mediation, err := modelMediationConfigFromSpec(opts.ModelMediation)
	if err != nil {
		return nil, err
	}
	engine, runnerConfig, err := modelrunner.ResolveRunner(modelRunnerOverridesFromSpec(opts.ModelRunner))
	if err != nil {
		return nil, err
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
	rec, err := findModel(opts.StateDir, canonical)
	if err != nil {
		// Not in the store — auto-pull (one-shot convenience).
		rec, err = pullModel(ctx, model.PullOptions{StateDir: opts.StateDir, ModelRef: modelRefRaw, Token: modelToken, Progress: opts.Progress})
		if err != nil {
			return nil, fmt.Errorf("pull model %s: %w", modelRefRaw, err)
		}
	}
	runner, err := ensureModelRunner(ctx, modelrunner.EnsureOptions{
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
	attachment, err := attachModelService(ctx, Options{
		StateDir: opts.StateDir, WorkspaceID: opts.Name, ExecPath: config.ExecPath,
		Runner: runner, Mode: string(mediation.Mode), PolicyURL: mediation.PolicyURL,
		PolicyFile: mediation.PolicyFile, PolicyTimeout: mediation.PolicyTimeout,
	})
	if err != nil {
		_ = releaseModelRunner(opts.StateDir, rec.ModelRef, opts.Name)
		return nil, fmt.Errorf("start model service: %w", err)
	}
	// Copy the caller's map so a failed event write cannot leave partial wiring.
	original := opts
	updated := *opts
	updated.Env = maps.Clone(opts.Env)
	opts = &updated
	opts.Model = rec.ModelRef
	opts.ModelTarget = attachment.Target
	opts.ModelRunnerKey = runner.Key
	opts.ModelTargetStable = true
	opts.ModelTargetMediated = mediation.Enabled
	var mediator *Attachment
	if mediation.Enabled {
		mediator = &attachment
	}
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	modelURL := fmt.Sprintf("http://127.0.0.1:%d/v1", workspace.DefaultModelGuestPort)
	opts.Env["MICROAGENT_MODEL_URL"] = modelURL
	opts.Env["OPENAI_BASE_URL"] = modelURL
	if err := appendModelWorkerAttachedEvent(*opts, runner, modelURL, mediator); err != nil {
		_ = releaseModelService(opts.StateDir, opts.Name)
		_ = releaseModelRunner(opts.StateDir, rec.ModelRef, opts.Name)
		return nil, err
	}
	*original = updated
	stateDir, modelRef, holder, backend := opts.StateDir, rec.ModelRef, opts.Name, opts.Backend
	return func() {
		_ = releaseModelService(stateDir, holder)
		_ = releaseModelRunner(stateDir, modelRef, holder)
		_ = appendModelWorkerReleasedEvent(stateDir, holder, backend, modelRef)
	}, nil
}
