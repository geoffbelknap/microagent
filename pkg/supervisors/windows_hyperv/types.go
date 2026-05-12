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
	PrepareNetwork(ctx context.Context, spec computeSystemSpec) (networkAttachment, error)
	CleanupNetwork(ctx context.Context, state runtimeState) error
	Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error)
	Start(ctx context.Context, id string) error
	Shutdown(ctx context.Context, id string) error
	Kill(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	Wait(ctx context.Context, id string) error
}

type computeSystemSpec struct {
	Name              string
	StateDir          string
	Identity          vmkit.Identity
	Config            vmkit.Config
	NetworkID         string
	NetworkEndpointID string
}

type computeSystemHandle struct {
	ID                string
	RuntimeID         string
	NetworkID         string
	NetworkEndpointID string
	RuntimeNetwork    *vmkit.NetworkConfig
}

type networkAttachment struct {
	NetworkID         string
	NetworkEndpointID string
	RuntimeNetwork    *vmkit.NetworkConfig
}

type runtimeListenerSet interface {
	Wait(ctx context.Context) error
	Close() error
}

type hcsClient interface {
	CreateComputeSystem(ctx context.Context, id string, document []byte) (computeSystemHandle, error)
	GrantVMAccess(ctx context.Context, vmID, path string) error
	StartComputeSystem(ctx context.Context, id string) error
	ShutdownComputeSystem(ctx context.Context, id string) error
	KillComputeSystem(ctx context.Context, id string) error
	DeleteComputeSystem(ctx context.Context, id string) error
	WaitComputeSystem(ctx context.Context, id string) error
}

func (s Supervisor) runtimeAdapter() runtimeAdapter {
	if s.adapter != nil {
		return s.adapter
	}
	return defaultAdapter{}
}
