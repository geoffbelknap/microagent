package windows_hyperv

import (
	"context"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type Options struct {
	Name     string
	StateDir string
}

type Supervisor struct {
	Options Options
	adapter runtimeAdapter
}

type runtimeAdapter interface {
	Host(ctx context.Context) (vmkit.HostSupport, error)
	Check(ctx context.Context) error
	Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error)
	Start(ctx context.Context, id string) error
	Shutdown(ctx context.Context, id string) error
	Kill(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
}

type computeSystemSpec struct {
	Name     string
	StateDir string
	Identity vmkit.Identity
	Config   vmkit.Config
}

type computeSystemHandle struct {
	ID string
}

func (s Supervisor) runtimeAdapter() runtimeAdapter {
	if s.adapter != nil {
		return s.adapter
	}
	return defaultAdapter{}
}
