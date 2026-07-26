package model

import (
	"context"

	"github.com/geoffbelknap/microagent/pkg/modelrunner"
)

type ServeOptions struct {
	StateDir  string
	ModelRef  string
	Token     string
	Dedicated bool
	Runner    modelrunner.RunnerOverrides
}

func Serve(ctx context.Context, opts ServeOptions) (modelrunner.Record, error) {
	canonical, _, err := Resolve(opts.ModelRef)
	if err != nil {
		return modelrunner.Record{}, err
	}
	rec, err := Find(opts.StateDir, canonical)
	if err != nil {
		rec, err = Pull(ctx, PullOptions{
			StateDir: opts.StateDir, ModelRef: opts.ModelRef, Token: opts.Token,
		})
		if err != nil {
			return modelrunner.Record{}, err
		}
	}
	engine, config, err := modelrunner.ResolveRunner(opts.Runner)
	if err != nil {
		return modelrunner.Record{}, err
	}
	return modelrunner.Ensure(ctx, modelrunner.EnsureOptions{
		StateDir: opts.StateDir, ModelRef: rec.ModelRef, ModelPath: rec.OutputPath,
		Engine: engine, Pinned: true, Dedicated: opts.Dedicated, RunnerConfig: config,
	})
}
