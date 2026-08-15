package model

import (
	"context"

	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/operation"
)

type ServeOptions struct {
	StateDir  string
	ModelRef  string
	Token     string
	Dedicated bool
	Runner    modelrunner.RunnerOverrides
	Progress  operation.ProgressFunc
}

func Serve(ctx context.Context, opts ServeOptions) (modelrunner.Record, error) {
	progress := operation.NewReporter(opts.Progress)
	progress.Emit(operation.ProgressEvent{Operation: "model_serve", Phase: "model_resolve", Label: "Serve model", Message: "resolving model"})
	canonical, _, err := Resolve(opts.ModelRef)
	if err != nil {
		return modelrunner.Record{}, err
	}
	rec, err := Find(opts.StateDir, canonical)
	if err != nil {
		rec, err = Pull(ctx, PullOptions{
			StateDir: opts.StateDir, ModelRef: opts.ModelRef, Token: opts.Token, Progress: opts.Progress,
		})
		if err != nil {
			return modelrunner.Record{}, err
		}
	} else {
		progress.Emit(operation.ProgressEvent{Operation: "model_serve", Phase: "model_cache", Label: "Serve model", Message: "using cached model"})
	}
	progress.Emit(operation.ProgressEvent{Operation: "model_serve", Phase: "runner_select", Label: "Serve model", Message: "selecting model runner"})
	engine, config, err := modelrunner.ResolveRunner(opts.Runner)
	if err != nil {
		return modelrunner.Record{}, err
	}
	return modelrunner.Ensure(ctx, modelrunner.EnsureOptions{
		StateDir: opts.StateDir, ModelRef: rec.ModelRef, ModelPath: rec.OutputPath,
		Engine: engine, Pinned: true, Dedicated: opts.Dedicated, RunnerConfig: config, Progress: opts.Progress,
	})
}
